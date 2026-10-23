package whalebridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/gwmw"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/routes"
	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
	"github.com/ChisaAlter/WhaleBridge/internal/usage"
)

const runtimeChildEnv = "WHALEBRIDGE_RUNTIME_TEST_CHILD"
const runtimePrivateKey = "11111111111111111111111111111111"
const runtimeSecret = "runtime-private-word"
const runtimeMiddlewareMark = " middleware-ready"

// Only the child owns process globals, registrations and RunContext's workers.
// A file cancels its context on Windows too, without relying on os.Interrupt.
func TestWhaleBridgeRuntimeComponentChild(t *testing.T) {
	if os.Getenv(runtimeChildEnv) != "1" {
		return
	}
	testenv.Offline()
	usage.OTelEvery = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if _, err := os.Stat(os.Getenv("WHALEBRIDGE_RUNTIME_STOP")); err == nil {
					cancel()
					return
				}
			}
		}
	}()
	err := RunContext(ctx, "runtime-lifecycle-test")
	cancel()
	<-watchDone
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"order", "rotate", "usage", "pace", "weight", "free"} {
		if routes.Served(name) {
			t.Errorf("strategy %s survived RunContext", name)
		}
	}
	if n := len(gwmw.Agents()); n != 0 {
		t.Errorf("%d middleware registrations survived RunContext", n)
	}
	// This also catches a leaked refresh callback that resurrects a masker.
	if _, ok := gwmw.Served(); ok {
		t.Error("masker survived RunContext")
	}
	if n := len(usage.Observers()); n != 0 {
		t.Errorf("%d observers survived RunContext", n)
	}
	if usage.OTelEnabled() {
		t.Error("exporter survived RunContext with export still configured on")
	}
}

func TestWhaleBridgeRuntimeLifecycle(t *testing.T) {
	componentHTTPTestOffline.Do(testenv.Offline)
	vendor := newRuntimeVendor(t)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer collector.Close()
	p := startRuntimeComponent(t, vendor.server.URL, collector.URL)

	// All four are explicitly off in plugins.yml, but each is configured in
	// its owning file. No request may have to bootstrap the first Apply.
	p.wantStates(t, map[string]string{"rotation": "active", "middleware": "active", "redact": "active", "otel": "active"})
	first := p.chat("first", false)
	first.requireOK(t)
	if !strings.Contains(string(first.body), runtimeSecret) || !strings.Contains(string(first.body), runtimeMiddlewareMark) {
		t.Fatalf("first request did not use masking and middleware: %s", first.body)
	}
	seen := vendor.take(t)
	if strings.Contains(seen.content, runtimeSecret) || !strings.Contains(seen.content, runtimeMiddlewareMark) {
		t.Fatalf("first vendor request did not pass synchronous middleware/masking: %+v", seen)
	}

	// Each otherwise unserved row is the only non-Smart configuration. A live
	// catalog scan would miss hidden accounts and persistent hidden groups.
	for _, kind := range []string{"hidden-account", "off-provider", "hidden-group", "disabled-group"} {
		t.Run(kind, func(t *testing.T) {
			p.writeProviders(t, vendor.server.URL, kind)
			p.refresh(t, http.StatusOK)
			p.wantState(t, "rotation", "active")
		})
	}
	p.writeProviders(t, vendor.server.URL, "none")
	p.refresh(t, http.StatusOK)
	p.wantInactive(t, "rotation")

	p.writeProviders(t, vendor.server.URL, "live")
	p.refresh(t, http.StatusOK)
	p.wantState(t, "rotation", "active")
	for _, label := range []string{"rotate-a", "rotate-b"} {
		p.chat(label, false).requireOK(t)
	}
	a, b := vendor.take(t), vendor.take(t)
	if a.key == b.key || !slices.Contains([]string{"Bearer first-key", "Bearer second-key"}, a.key) || !slices.Contains([]string{"Bearer first-key", "Bearer second-key"}, b.key) {
		t.Fatalf("registered strategy did not rotate actual requests: %q, %q", a.key, b.key)
	}

	// A corrupt control file must report an error and retain the running set.
	p.write(t, "plugins.yml", []byte("plugins: ["))
	p.refresh(t, http.StatusInternalServerError)
	if s := p.status(t); s.Error == "" {
		t.Fatal("runtime status did not retain the refresh error")
	}
	p.wantStates(t, map[string]string{"rotation": "active", "middleware": "active", "redact": "active", "otel": "active"})
	p.chat("last-good", false).requireOK(t)
	vendor.take(t)
	p.write(t, "plugins.yml", runtimePluginConfig())
	p.refresh(t, http.StatusOK)

	// Refresh is independent of the gateway request lease. It closes normal
	// admission until the old stream finishes, then removes the old policy.
	held := vendor.hold(t, "refresh-stream")
	stream := p.chatAsync("refresh-stream", true)
	held.wait(t)
	p.writeProviders(t, vendor.server.URL, "none")
	refreshed := p.apiAsync(http.MethodPost, "/api/runtime/refresh")
	p.until(t, "refresh waiting for the stream", func() bool {
		s := p.status(t)
		return s.Pending && s.Active > 0
	})
	queued := p.chatAsync("queued-after-refresh", false)
	select {
	case result := <-queued:
		t.Fatalf("new request bypassed pending refresh: HTTP %d: %s (%v)", result.status, result.body, result.err)
	case <-time.After(75 * time.Millisecond):
	}
	held.unblock()
	(<-stream).requireRestoredStream(t)
	(<-refreshed).requireOK(t)
	(<-queued).requireOK(t)
	vendor.take(t)
	vendor.take(t)
	p.wantInactive(t, "rotation")

	// A cancelled owner stops readiness immediately, but leaves the stream's
	// serving context, masker and observers alive until the handler finishes.
	p.writeProviders(t, vendor.server.URL, "live")
	p.refresh(t, http.StatusOK)
	held = vendor.hold(t, "shutdown-stream")
	stream = p.chatAsync("shutdown-stream", true)
	held.wait(t)
	p.requestStop(t)
	p.until(t, "readiness removal before drain", func() bool {
		_, err := os.Stat(filepath.Join(p.data, "state.json"))
		return os.IsNotExist(err)
	})
	select {
	case <-p.done:
		t.Fatalf("RunContext returned before the held stream finished: %s", p.logs())
	case <-held.cancelled:
		t.Fatal("owner cancellation cancelled the accepted vendor stream")
	case <-time.After(75 * time.Millisecond):
	}
	if rejected := p.chat("after-stop", false); rejected.err == nil && rejected.status != http.StatusServiceUnavailable {
		t.Fatalf("shutdown admitted a new request: HTTP %d: %s", rejected.status, rejected.body)
	}
	held.unblock()
	(<-stream).requireRestoredStream(t)
	p.wait(t)
	for _, address := range []string{strings.TrimPrefix(p.origin, "http://"), strings.TrimPrefix(p.gateway, "http://")} {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			conn.Close()
			t.Errorf("listener %s survived RunContext", address)
		}
	}
}

type runtimeStatus struct {
	Plugins []struct {
		ID    string `json:"id"`
		State string `json:"state"`
		Why   string `json:"why"`
	} `json:"plugins"`
	Pending bool   `json:"pending"`
	Active  int    `json:"active"`
	Error   string `json:"error"`
}

type runtimeReply struct {
	status int
	body   []byte
	err    error
}

func (r runtimeReply) requireOK(t *testing.T) {
	t.Helper()
	if r.err != nil || r.status != http.StatusOK {
		t.Fatalf("HTTP %d: %s (%v)", r.status, r.body, r.err)
	}
}

func (r runtimeReply) requireRestoredStream(t *testing.T) {
	t.Helper()
	r.requireOK(t)
	var content strings.Builder
	for _, line := range strings.Split(string(r.body), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var event struct {
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &event) == nil && len(event.Choices) > 0 {
			content.WriteString(event.Choices[0].Delta.Content)
		}
	}
	if !strings.Contains(content.String(), runtimeSecret) || !strings.Contains(content.String(), runtimeMiddlewareMark) || !strings.Contains(string(r.body), "[DONE]") || strings.Contains(content.String(), "{{") {
		t.Fatalf("accepted stream was truncated or not restored: %s", r.body)
	}
}

type runtimeComponent struct {
	data, origin, gateway, stop, logPath string
	client                               *http.Client
	cmd                                  *exec.Cmd
	done                                 chan struct{}
	waitErr                              error
}

func startRuntimeComponent(t *testing.T, vendor, collector string) *runtimeComponent {
	t.Helper()
	root := t.TempDir()
	p := &runtimeComponent{data: filepath.Join(root, "component"), stop: filepath.Join(root, "stop"), logPath: filepath.Join(root, "component.log"), done: make(chan struct{})}
	native, dsh := filepath.Join(root, "native"), filepath.Join(root, "dsh")
	for _, path := range []string{p.data, native, dsh, filepath.Join(p.data, "cache", "models")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	p.writeProviders(t, vendor, "hidden-account")
	p.write(t, "plugins.yml", runtimePluginConfig())
	p.write(t, "gateway.key", []byte(runtimePrivateKey))
	p.write(t, "cache/models.json", []byte(`{}`))
	p.writeJSON(t, "cache/models/relay.json", map[string]any{"base": vendor + "/v1", "fetched": time.Now(), "models": []map[string]any{{"id": "echo", "name": "Echo", "context": 8192, "output": 4096}}})
	p.writeJSON(t, "settings.json", map[string]any{"noAccountDiscovery": true, "quotaReads": "off", "redactWords": []string{runtimeSecret}, "otel": map[string]any{"enabled": true, "endpoint": collector, "sessions": false}})
	module := filepath.Join(root, "fixture.middleware.js")
	if err := os.WriteFile(module, []byte(`export function onRequest(body) { body.messages[0].content += " middleware-ready"; return body; }`), 0600); err != nil {
		t.Fatal(err)
	}
	p.writeJSON(t, "plugins.json", map[string]any{"plugins": []map[string]any{{"spec": module}}})
	// Run publishes gateway.URL(), so reserve a concrete loopback port rather
	// than publishing :0. Only this child ever changes its MAGPIE_ADDR.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	ln.Close()
	p.gateway = "http://" + address
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(p.logPath)
	if err != nil {
		t.Fatal(err)
	}
	p.cmd = exec.Command(executable, "-test.run=^TestWhaleBridgeRuntimeComponentChild$", "-test.timeout=90s")
	overrides := map[string]string{runtimeChildEnv: "1", "WHALEBRIDGE_RUNTIME_STOP": p.stop, "LAUNCHER_COMPONENT_ID": "whalebridge", "LAUNCHER_COMPONENT_DATA_DIR": p.data, "WHALEBRIDGE_DSH_HOME": dsh, "WHALEBRIDGE_GATEWAY_TOKEN": runtimePrivateKey, "MAGPIE_ADDR": address, "MAGPIE_KEY": "", "MAGPIE_PLUGIN_MARKET": "off", "MAGPIE_BUN": "", "HOME": native, "USERPROFILE": native, "APPDATA": filepath.Join(native, "AppData", "Roaming"), "LOCALAPPDATA": filepath.Join(native, "AppData", "Local"), "PATH": native, "HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": "", "MAGPIE_OTEL_ENABLED": "true", "MAGPIE_OTEL_ENDPOINT": collector, "MAGPIE_OTEL_HEADERS": "", "MAGPIE_OTEL_METRICS": "false", "MAGPIE_OTEL_BODIES": "false", "MAGPIE_OTEL_BODIES_WHOLE": "false"}
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if _, changed := overrides[strings.ToUpper(name)]; !changed {
			p.cmd.Env = append(p.cmd.Env, value)
		}
	}
	for name, value := range overrides {
		p.cmd.Env = append(p.cmd.Env, name+"="+value)
	}
	p.cmd.Stdout, p.cmd.Stderr = log, log
	if err := p.cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	go func() { p.waitErr = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
			<-p.done
		}
		_ = log.Close()
	})
	var ready struct{ URL string }
	p.until(t, "component readiness", func() bool {
		b, err := os.ReadFile(filepath.Join(p.data, "state.json"))
		return err == nil && json.Unmarshal(b, &ready) == nil && ready.URL != ""
	})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	p.client = &http.Client{Jar: jar, Timeout: 15 * time.Second}
	response, err := p.client.Get(ready.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("launcher cookie exchange: HTTP %d", response.StatusCode)
	}
	p.origin = response.Request.URL.Scheme + "://" + response.Request.URL.Host
	return p
}

func runtimePluginConfig() []byte {
	return []byte("preset: all\nplugins:\n  - id: rotation\n    enabled: false\n  - id: middleware\n    enabled: false\n  - id: redact\n    enabled: false\n  - id: otel\n    enabled: false\n")
}

func (p *runtimeComponent) writeProviders(t *testing.T, vendor, kind string) {
	t.Helper()
	relay := provider.Provider{ID: "relay", Name: "Relay", Chat: vendor + "/v1", Models: []string{"echo"}, Key: "first-key", Keys: []provider.KeyAccount{{Key: "second-key"}}, Affinity: provider.AffinityOff}
	if kind == "live" {
		relay.Routing = provider.Rotate
	}
	providers, groups := []provider.Provider{relay}, []provider.Group{}
	switch kind {
	case "hidden-account":
		providers = append(providers, provider.Provider{ID: "future-account", Hidden: true, Routing: provider.Rotate})
	case "off-provider":
		providers = append(providers, provider.Provider{ID: "off-provider", Chat: vendor + "/v1", Key: "off-key", Models: []string{"echo"}, Off: true, Routing: provider.Weighted})
	case "hidden-group":
		groups = append(groups, provider.Group{ID: "auto-kept", Hidden: true, Routing: provider.FreeFirst, Members: []string{"relay/echo"}})
	case "disabled-group":
		groups = append(groups, provider.Group{ID: "disabled-kept", Disabled: true, Routing: provider.Pace, Members: []string{"relay/echo"}})
	}
	p.writeJSON(t, "providers.json", map[string]any{"providers": providers, "groups": groups, "noAutoGroups": true})
}

func (p *runtimeComponent) write(t *testing.T, name string, b []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(p.data, filepath.FromSlash(name)), b, 0600); err != nil {
		t.Fatal(err)
	}
}
func (p *runtimeComponent) writeJSON(t *testing.T, name string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	p.write(t, name, b)
}
func (p *runtimeComponent) logs() string { b, _ := os.ReadFile(p.logPath); return string(b) }
func (p *runtimeComponent) until(t *testing.T, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ready() {
		select {
		case <-p.done:
			t.Fatalf("child exited while waiting for %s: %v\n%s", what, p.waitErr, p.logs())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s\n%s", what, p.logs())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func (p *runtimeComponent) requestStop(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(p.stop, nil, 0600); err != nil {
		t.Fatal(err)
	}
}
func (p *runtimeComponent) wait(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
		if p.waitErr != nil {
			t.Fatalf("RunContext child: %v\n%s", p.waitErr, p.logs())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("RunContext did not finish draining\n%s", p.logs())
	}
}
func (p *runtimeComponent) request(method, url string, body []byte, gateway bool) runtimeReply {
	r, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return runtimeReply{err: err}
	}
	r.Header.Set("Content-Type", "application/json")
	if gateway {
		r.Header.Set("Authorization", "Bearer "+runtimePrivateKey)
	} else {
		r.Header.Set("Origin", p.origin)
	}
	response, err := p.client.Do(r)
	if err != nil {
		return runtimeReply{err: err}
	}
	defer response.Body.Close()
	b, err := io.ReadAll(response.Body)
	return runtimeReply{response.StatusCode, b, err}
}
func (p *runtimeComponent) apiAsync(method, path string) <-chan runtimeReply {
	done := make(chan runtimeReply, 1)
	go func() { done <- p.request(method, p.origin+path, []byte(`{}`), false) }()
	return done
}
func (p *runtimeComponent) chat(label string, stream bool) runtimeReply {
	b, _ := json.Marshal(map[string]any{"model": "relay/echo", "stream": stream, "messages": []map[string]string{{"role": "user", "content": runtimeSecret + " " + label}}})
	return p.request(http.MethodPost, p.gateway+"/v1/chat/completions", b, true)
}
func (p *runtimeComponent) chatAsync(label string, stream bool) <-chan runtimeReply {
	done := make(chan runtimeReply, 1)
	go func() { done <- p.chat(label, stream) }()
	return done
}
func (p *runtimeComponent) status(t *testing.T) runtimeStatus {
	t.Helper()
	r := p.request(http.MethodGet, p.origin+"/api/runtime", nil, false)
	r.requireOK(t)
	var s runtimeStatus
	if err := json.Unmarshal(r.body, &s); err != nil {
		t.Fatalf("runtime status: %v: %s", err, r.body)
	}
	return s
}
func (p *runtimeComponent) refresh(t *testing.T, status int) {
	t.Helper()
	r := p.request(http.MethodPost, p.origin+"/api/runtime/refresh", []byte(`{}`), false)
	if r.err != nil || r.status != status {
		t.Fatalf("refresh: want HTTP %d, got %d: %s (%v)\n%s", status, r.status, r.body, r.err, p.logs())
	}
}
func (p *runtimeComponent) wantStates(t *testing.T, expected map[string]string) {
	t.Helper()
	s := p.status(t)
	if len(s.Plugins) != len(expected) {
		t.Fatalf("assembly is not finite: %+v", s)
	}
	for _, plugin := range s.Plugins {
		if expected[plugin.ID] != plugin.State {
			t.Fatalf("runtime %s=%s, expected %s: %s", plugin.ID, plugin.State, expected[plugin.ID], plugin.Why)
		}
	}
}
func (p *runtimeComponent) wantState(t *testing.T, id, state string) {
	t.Helper()
	for _, plugin := range p.status(t).Plugins {
		if plugin.ID == id {
			if plugin.State != state {
				t.Fatalf("runtime %s=%s, expected %s: %s", id, plugin.State, state, plugin.Why)
			}
			return
		}
	}
	t.Fatalf("missing runtime plugin %s", id)
}
func (p *runtimeComponent) wantInactive(t *testing.T, id string) {
	t.Helper()
	for _, plugin := range p.status(t).Plugins {
		if plugin.ID == id && (plugin.State == "off" || plugin.State == "disposed") {
			return
		}
	}
	t.Fatalf("runtime %s did not turn off: %+v", id, p.status(t))
}

type runtimeVendorRequest struct{ content, key string }
type runtimeBarrier struct {
	started, release, cancelled chan struct{}
	once                        sync.Once
}

func (b *runtimeBarrier) unblock() { b.once.Do(func() { close(b.release) }) }
func (b *runtimeBarrier) wait(t *testing.T) {
	t.Helper()
	select {
	case <-b.started:
	case <-time.After(10 * time.Second):
		t.Fatal("vendor stream did not reach its barrier")
	}
}

type runtimeVendor struct {
	server   *httptest.Server
	seen     chan runtimeVendorRequest
	mu       sync.Mutex
	barriers map[string]*runtimeBarrier
}

func newRuntimeVendor(t *testing.T) *runtimeVendor {
	t.Helper()
	v := &runtimeVendor{seen: make(chan runtimeVendorRequest, 32), barriers: map[string]*runtimeBarrier{}}
	v.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"echo"}]}`)
			return
		}
		var request struct {
			Stream   bool                       `json:"stream"`
			Messages []struct{ Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) == 0 {
			http.Error(w, "bad fixture request", http.StatusBadRequest)
			return
		}
		content := request.Messages[0].Content
		v.seen <- runtimeVendorRequest{content, r.Header.Get("Authorization")}
		if !request.Stream {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": "chat-fixture", "model": "echo", "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
			return
		}
		v.mu.Lock()
		var barrier *runtimeBarrier
		for label, b := range v.barriers {
			if strings.Contains(content, label) {
				barrier = b
				break
			}
		}
		v.mu.Unlock()
		if barrier == nil {
			http.Error(w, "missing fixture barrier", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(text string) {
			b, _ := json.Marshal(map[string]any{"id": "chat-fixture", "object": "chat.completion.chunk", "model": "echo", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": text}, "finish_reason": nil}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		chunk("opening ")
		close(barrier.started)
		select {
		case <-barrier.release:
		case <-r.Context().Done():
			close(barrier.cancelled)
			return
		}
		// Split a masked value between events so restoration must remain live.
		middle := len(content) / 2
		if end := strings.Index(content, "}}"); end > 2 {
			middle = end - 3
		}
		chunk(content[:middle])
		chunk(content[middle:])
		io.WriteString(w, "data: {\"id\":\"chat-fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(v.server.Close)
	return v
}
func (v *runtimeVendor) hold(t *testing.T, label string) *runtimeBarrier {
	t.Helper()
	b := &runtimeBarrier{started: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{})}
	v.mu.Lock()
	v.barriers[label] = b
	v.mu.Unlock()
	t.Cleanup(b.unblock)
	return b
}
func (v *runtimeVendor) take(t *testing.T) runtimeVendorRequest {
	t.Helper()
	select {
	case request := <-v.seen:
		return request
	case <-time.After(5 * time.Second):
		t.Fatal("vendor request was not observed")
		return runtimeVendorRequest{}
	}
}
