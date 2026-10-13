package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/ChisaAlter/WhaleBridge/internal/access"
	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type ruleUp struct {
	mu    sync.Mutex
	key   string
	fail  int
	calls []string // "stream" or "json"
	seen  int      // requests that carried an image
	last  string   // the last request's body
}

func (u *ruleUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	stream := strings.Contains(string(b), `"stream":true`)
	u.mu.Lock()
	u.calls = append(u.calls, map[bool]string{true: "stream", false: "json"}[stream])
	u.last = string(b)
	if strings.Contains(string(b), "image_url") {
		u.seen++
	}
	fail := u.fail
	u.mu.Unlock()
	if fail != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fail)
		io.WriteString(w, `{"error":{"message":"down"}}`)
		return
	}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+u.key+`"}}]}`,
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"x","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3000,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2500}}}`,
			`data: [DONE]`,
		))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+u.key+`"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":3000,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2500}}}`)
}

func (u *ruleUp) n() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

// ruled sets up a group of a small model (a/small, 64k, text only) and a
// big one (b/big, 1M, images) with the rules given, a/small first.
func ruled(t *testing.T, rules ...provider.Rule) (*Server, *ruleUp, *ruleUp) {
	t.Helper()
	fresh(t)
	a, b := &ruleUp{key: "ka"}, &ruleUp{key: "kb"}
	no, yes := false, true
	for _, x := range []struct {
		id, model string
		up        *ruleUp
		m         catalog.Model
	}{
		{"a", "small", a, catalog.Model{ID: "small", Context: 64000, ImageInput: &no}},
		{"b", "big", b, catalog.Model{ID: "big", Context: 1000000, Images: true, ImageInput: &yes}},
	} {
		srv := httptest.NewServer(x.up)
		t.Cleanup(srv.Close)
		if err := provider.Save(provider.Provider{ID: x.id, Name: strings.ToUpper(x.id), Key: "k" + x.id, Models: []string{x.model}, Chat: srv.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive(x.id, srv.URL+"/v1", []catalog.Model{x.m}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "R", Members: []string{"a/small", "b/big"}, Routing: provider.Ordered, Rules: rules}); err != nil {
		t.Fatal(err)
	}
	return New(), a, b
}

// chat builds a Chat conversation: the user's words, each after the first
// answered by a tool call and its result when tools is set.
func chat(first string, turns []string, toolsAfter int, extra string) string {
	var ms []string
	for i, w := range append([]string{first}, turns...) {
		if i > 0 {
			ms = append(ms, `{"role":"assistant","content":"done"}`)
		}
		ms = append(ms, `{"role":"user","content":`+quote(w)+`}`)
	}
	for i := range toolsAfter {
		id := fmt.Sprintf("c%d", i)
		ms = append(ms, `{"role":"assistant","content":null,"tool_calls":[{"id":"`+id+`","type":"function","function":{"name":"ls","arguments":"{}"}}]}`,
			`{"role":"tool","tool_call_id":"`+id+`","content":"file.txt"}`)
	}
	return `{"model":"group/r"` + extra + `,"messages":[` + strings.Join(ms, ",") + `]}`
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func postOK(t *testing.T, s *Server, session, body string) (string, Route) {
	t.Helper()
	code, out := postAs(t, s, session, body)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	return out, lastRoute(s)
}
func forgetSinks(t *testing.T) {
	t.Helper()
	clear := func() {
		sunk.Lock()
		sunk.m = map[string]sinking{}
		sunk.Unlock()
		restingUntil.Lock()
		restingUntil.m = map[string]time.Time{}
		restingUntil.note = map[string]Rest{}
		restingUntil.Unlock()
		routed.Lock()
		routed.failures = map[string]int{}
		routed.Unlock()
	}
	clear()
	t.Cleanup(clear)
}
func rested(key string) {
	restingUntil.Lock()
	restingUntil.m[key] = time.Now().Add(-time.Second)
	restingUntil.Unlock()
}
func restsOf(cs []candidate) string {
	s := ""
	for _, c := range cs {
		s += c.rest + " "
	}
	return s
}
func wbSignedIn(t *testing.T, id, authID string, st http.Handler) provider.Provider {
	t.Helper()
	fresh(t)
	wbImages.Lock()
	wbImages.m = map[string]*wbImageList{}
	wbImages.Unlock()
	home := os.Getenv("HOME")
	base := filepath.Join(home, ".local", "share")
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support")
	case "windows":
		base = filepath.Join(home, "AppData", "Local")
	}
	future := strconv.FormatInt(time.Now().Add(24*time.Hour).UnixMilli(), 10)
	info := `{"account":{"uid":"u1","nickname":"me"},"auth":{"accessToken":"tok","refreshToken":"r","expiresAt":` + future + `,"refreshExpiresAt":` + future + `}}`
	auth := filepath.Join(base, "CodeBuddyExtension", "Data", "Public", "auth", authID+".info")
	if err := os.MkdirAll(filepath.Dir(auth), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(auth, []byte(info), 0o600); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(st)
	t.Cleanup(up.Close)
	t.Cleanup(provider.WorkBuddyBaseForTest(up.URL, up.URL))
	for _, p := range provider.All() {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("%s isn't signed in", id)
	return provider.Provider{}
}

type adaptiveVendor struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (v *adaptiveVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	v.mu.Lock()
	v.bodies = append(v.bodies, m)
	v.mu.Unlock()
	th, _ := m["thinking"].(map[string]any)
	model, _ := m["model"].(string)
	if th["type"] == "enabled" && strings.Contains(model, "5-5") || strings.Contains(model, "5.5") && th["type"] == "enabled" {
		w.WriteHeader(400)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"thinking.type.enabled is not supported for this model. Use thinking.type.adaptive and output_config.effort to control thinking behavior."}}`)
		return
	}
	if m["stream"] == true {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\""+model+"\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"`+model+`","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
}

func (v *adaptiveVendor) last() map[string]any {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.bodies[len(v.bodies)-1]
}

var pngBytes = []byte("\x89PNG\r\n\x1a\n fake")

type imagesAnswer struct {
	Model string `json:"model"`
	Data  []struct {
		B64 string `json:"b64_json"`
	} `json:"data"`
	Text  string `json:"text"`
	Usage struct {
		Input  int `json:"input_tokens"`
		Output int `json:"output_tokens"`
	} `json:"usage"`
}

func postImages(t *testing.T, s *Server, path, ct, body string) (int, imagesAnswer, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", ct)
	s.upstreamHandler().ServeHTTP(rec, req)
	var a imagesAnswer
	json.Unmarshal(rec.Body.Bytes(), &a)
	return rec.Code, a, rec.Body.String()
}

func TestOpenRouterImagesAPIDrawsImageModels(t *testing.T) {
	fresh(t)
	p := provider.Provider{ID: "openrouter", Name: "OpenRouter", Chat: "https://openrouter.ai/api/v1", Key: "key", Models: []string{"openai/gpt-image-2"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	var path, requestBody string
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		requestBody = string(b)
		response := `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(pngBytes) + `","media_type":"image/png"}],"usage":{"prompt_tokens":3,"completion_tokens":7}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	ref := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	code, answer, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"openrouter/openai/gpt-image-2","prompt":"a magpie","images":["`+ref+`"]}`)
	if code != http.StatusOK || len(answer.Data) != 1 || answer.Usage.Input != 3 || answer.Usage.Output != 7 {
		t.Fatalf("%d %s", code, raw)
	}
	if path != "/api/v1/images" {
		t.Fatalf("OpenRouter was asked at %q, want /api/v1/images", path)
	}
	var got struct {
		Model           string `json:"model"`
		Prompt          string `json:"prompt"`
		InputReferences []struct {
			Type     string `json:"type"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"input_references"`
	}
	if err := json.Unmarshal([]byte(requestBody), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "openai/gpt-image-2" || got.Prompt != "a magpie" || len(got.InputReferences) != 1 || got.InputReferences[0].Type != "image_url" || got.InputReferences[0].ImageURL.URL != ref {
		t.Fatalf("OpenRouter request: %s", requestBody)
	}
}
func TestOpenRouterImagesAPIFailureIsReturnedWithoutChatRetry(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "openrouter", Name: "OpenRouter", Chat: "https://openrouter.ai/api/v1", Key: "key", Models: []string{"openai/gpt-image-2"}}); err != nil {
		t.Fatal(err)
	}
	var paths []string
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		message := `{"error":{"message":"image model is unavailable on the images endpoint"}}`
		return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(message))}, nil
	})}
	code, _, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"openrouter/openai/gpt-image-2","prompt":"a magpie"}`)
	if code != http.StatusNotFound || !strings.Contains(raw, "images endpoint") {
		t.Fatalf("%d %s", code, raw)
	}
	if len(paths) != 1 || paths[0] != "/api/v1/images" {
		t.Fatalf("OpenRouter image request paths: %v", paths)
	}
}
func chatgpt(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	setHome(t, t.TempDir()) // no sign-ins but the test's
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return up
}
func codexSignedIn(t *testing.T, spares ...string) {
	t.Helper()
	fresh(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows finds the home there
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": acct}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(auth("me@example.com", "acct-1")), 0o600)
	var saved []map[string]any
	for i, s := range spares {
		saved = append(saved, map[string]any{"agent": "codex", "user": s, "on": true, "seen": time.Now(),
			"auth": auth(s, "acct-"+string(rune('2'+i)))})
	}
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON(saved), 0o600)
	// signed in behind magpie's back: an earlier test's look at its own
	// home within 30s would otherwise keep Codex's sign-in off the list
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
}
func outputText(t *testing.T, body string) (string, int) {
	t.Helper()
	for _, ev := range events(body) {
		if ev["type"] != "response.completed" {
			continue
		}
		resp, _ := ev["response"].(map[string]any)
		out, _ := resp["output"].([]any)
		var text strings.Builder
		for _, it := range out {
			m, _ := it.(map[string]any)
			cs, _ := m["content"].([]any)
			for _, c := range cs {
				cm, _ := c.(map[string]any)
				s, _ := cm["text"].(string)
				text.WriteString(s)
			}
		}
		return text.String(), len(out)
	}
	t.Fatalf("no response.completed in %q", body)
	return "", 0
}

type chatRecorded struct {
	mu  sync.Mutex
	got []string
}

func (f *chatRecorded) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

func chatRecordedOn(t *testing.T, id, model string) *chatRecorded {
	t.Helper()
	f := &chatRecorded{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.got = append(f.got, string(b))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n"+
			"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: up.URL + "/v1", Models: []string{model}}); err != nil {
		t.Fatal(err)
	}
	return f
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

const liteRequest = `{"model":"fake/m1","instructions":"","stream":true,"input":[
	{"type":"additional_tools","id":"at_1","role":"developer","tools":[
		{"type":"tool_search","execution":"client","description":"Search tools","parameters":{"type":"object"}},
		{"type":"namespace","name":"functions","description":"Default tools","tools":[
			{"type":"function","name":"shell","description":"Run a command","strict":false,"parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}},
			{"type":"custom","name":"apply_patch","description":"Patch files","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}}]},
		{"type":"namespace","name":"mcp__probe","description":"Probe","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}]},
	{"type":"message","id":"msg_1","role":"developer","content":[{"type":"input_text","text":"You are Codex."}]},
	{"type":"message","role":"user","content":[{"type":"input_text","text":"list the files"}]},
	{"type":"function_call","call_id":"c1","name":"shell","namespace":"functions","arguments":"{\"cmd\":\"ls\"}"},
	{"type":"function_call_output","call_id":"c1","output":"a.txt"}
]}`
const sealedHandoff = `{"type":"agent_message","author":"/root","recipient":"/root/worker","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"gAAAAATest_ciphertext=="}]}`
const fernetSealed = "gAAAAABpAbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJ=="

var sealedTaskFernet = strings.Replace(sealedHandoff, "gAAAAATest_ciphertext==", fernetSealed, 1)

const plainTask = `{"type":"agent_message","author":"/root","recipient":"/root/helper","content":[{"type":"input_text","text":"read the README"}]}`
const sealedWorkerMessage = `{"type":"agent_message","author":"/root/worker","recipient":"/root","content":[{"type":"input_text","text":"Message Type: MESSAGE\nTask name: /root\nSender: /root/worker\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"gAAAAATest_worker_message=="}]}`

type relayOfChatGPT struct {
	mu  sync.Mutex
	got []string
}

func (f *relayOfChatGPT) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.got = append(f.got, string(b))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(`data: {"type":"response.output_text.delta","delta":"on it"}`,
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`))
}

func (f *relayOfChatGPT) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

func relayOn(t *testing.T, id string) *relayOfChatGPT {
	t.Helper()
	f := &relayOfChatGPT{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "sk-" + id, Models: []string{"gpt-6-astra"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	return f
}

const leadAsk = `{"type":"message","role":"user","content":[{"type":"input_text","text":"spawn a worker"}]}`

func newCaller(t *testing.T, names ...string) ([]access.Key, []string) {
	t.Helper()
	s := settings.Load()
	s.LAN = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	var secrets []string
	for _, name := range names {
		secret, err := access.Update("add-key", access.Change{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, secret)
	}
	keys, err := access.List()
	if err != nil {
		t.Fatal(err)
	}
	return keys[len(keys)-len(names):], secrets
}
func said(evs []Event) string {
	var b strings.Builder
	last := EventKind(-1)
	for _, ev := range evs {
		tag := map[EventKind]string{KText: "text", KThink: "think", KToolStart: "call", KToolArgs: "args", KStop: "stop", KError: "error"}[ev.Kind]
		if tag == "" {
			continue
		}
		if ev.Kind != last || ev.Kind == KToolStart {
			b.WriteString("|" + tag + ":")
		}
		last = ev.Kind
		switch ev.Kind {
		case KToolStart:
			b.WriteString(ev.ID + "/" + ev.Name)
		case KStop:
			b.WriteString(ev.Stop)
		default:
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}
func restingNow(t *testing.T) []string {
	t.Helper()
	restingUntil.Lock()
	defer restingUntil.Unlock()
	var out []string
	for k := range restingUntil.m {
		out = append(out, k)
	}
	return out
}
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "workdir", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func answer(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/chat/completions") {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\n"+
			"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"r1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":9,"output_tokens":1}}`)
}
func errorOf(obj map[string]any) string {
	if e, ok := obj["error"].(map[string]any); ok {
		m, _ := e["message"].(string)
		return m
	}
	return ""
}

type eyeUp struct {
	mu     sync.Mutex
	fail   bool
	bodies map[string][]string
}

func (u *eyeUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	model := modelOf(body)
	u.mu.Lock()
	if u.bodies == nil {
		u.bodies = map[string][]string{}
	}
	u.bodies[model] = append(u.bodies[model], string(body))
	fail := u.fail
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(model, "eye") && fail {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"eye is down"}}`)
		return
	}
	answer := "ok"
	if strings.HasSuffix(model, "eye") {
		answer = "A red square with the word HELLO in it."
	}
	if strings.Contains(string(body), `"stream":true`) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"`+answer+`"}}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"`+answer+`"},"finish_reason":"stop"}]}`)
}

func (u *eyeUp) sent(model string) []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.bodies[model]...)
}

func eyed(t *testing.T) (*Server, *eyeUp) {
	t.Helper()
	fresh(t)
	u := &eyeUp{}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "probe", Name: "Probe", Chat: up.URL + "/v1", Key: "key", Models: []string{"text", "eye"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("probe", up.URL+"/v1", []catalog.Model{
		{ID: "text", ImageInput: imageInputBool(false)},
		{ID: "eye", Images: true, ImageInput: imageInputBool(true)},
	}); err != nil {
		t.Fatal(err)
	}
	return New(), u
}

type kimiPlan struct {
	*httptest.Server
	mu       sync.Mutex
	searched []string // query, with the headers kimi-cli sends
	results  []string // the tool results its model was given
	broken   bool
}

func newKimiPlan(t *testing.T) *kimiPlan {
	k := &kimiPlan{}
	k.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		k.mu.Lock()
		defer k.mu.Unlock()
		switch r.URL.Path {
		case "/coding/v1/search":
			var q map[string]any
			json.Unmarshal(b, &q)
			if r.Header.Get("Authorization") != "Bearer sk-kimi" {
				http.Error(w, `{"error":{"message":"bad key"}}`, 401)
				return
			}
			k.searched = append(k.searched, strings.Join([]string{q["text_query"].(string), r.Header.Get("User-Agent"), r.Header.Get("X-Msh-Tool-Call-Id"),
				strings.Trim(string(must(json.Marshal([]any{q["limit"], q["enable_page_crawling"], q["timeout_seconds"]}))), "[]")}, " | "))
			if k.broken {
				http.Error(w, `{"error":{"message":"search down"}}`, 500)
				return
			}
			io.WriteString(w, `{"search_results":[{"site_name":"Go","title":"Go <b>downloads</b>","url":"https://go.dev/dl/","snippet":"go1.27.1 is the latest release.","content":"","date":"2026-09-30","icon":"","mime":"text/html"},{"site_name":"","title":"","url":""}]}`)
		case "/coding/v1/chat/completions":
			var q struct {
				Tools []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tools"`
				Messages []map[string]any `json:"messages"`
			}
			json.Unmarshal(b, &q)
			if !slices.ContainsFunc(q.Tools, func(x struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}) bool {
				return x.Function.Name == "web_search"
			}) {
				http.Error(w, `{"error":{"message":"no search tool"}}`, 400)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if last := q.Messages[len(q.Messages)-1]; last["role"] == "tool" {
				s, _ := last["content"].(string)
				k.results = append(k.results, s)
				io.WriteString(w, sse(`data: {"id":"d","choices":[{"index":0,"delta":{"role":"assistant","content":"Go 1.27.1 is out."}}]}`,
					`data: {"id":"d","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
					`data: [DONE]`))
				return
			}
			io.WriteString(w, sse(`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"latest go\"}"}}]}}]}`,
				`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":30,"completion_tokens":7}}`,
				`data: [DONE]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(k.Close)
	return k
}

func must(b []byte, _ error) []byte { return b }
func relayOfEvery(t *testing.T) (*httptest.Server, func() (paths []string, bodies []string)) {
	t.Helper()
	var mu sync.Mutex
	var paths, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths, bodies = append(paths, r.URL.Path), append(bodies, string(b))
		mu.Unlock()
		stream := streamOf(b)
		switch {
		case r.URL.Path == "/v1/messages" && stream:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"x\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		case r.URL.Path == "/v1/messages":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		case r.URL.Path == "/v1/responses" && stream:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"i\",\"output_index\":0,\"content_index\":0,\"delta\":\"ok\"}\n\n"+
				"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"x\",\"output\":[{\"type\":\"message\",\"id\":\"i\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
		case r.URL.Path == "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"r","object":"response","status":"completed","model":"x","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		case stream:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\n"+
				"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c","object":"chat.completion","model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() ([]string, []string) {
		mu.Lock()
		defer mu.Unlock()
		p, b := paths, bodies
		paths, bodies = nil, nil
		return p, b
	}
}
func imageInputBool(v bool) *bool { return &v }

const volcengineCodingEmbeddingResponse = `{"created":1791450147,"id":"0217914501473213a391e43edef5299850de6066b8d72e29b2241","data":[{"embedding":[0.036376953125,-0.024658203125,-0.0150146484375,0.0198974609375,-0.0111083984375,0.0213623046875,-0.01519775390625,0.0164794921875,-0.000827789306640625,-0.005340576171875,0.00022411346435546875,-0.040771484375,0.036376953125,-0.01092529296875,-0.018798828125,0.0146484375,-0.0419921875,0.02978515625,0.00750732421875,-0.00799560546875,0.00121307373046875,0.0233154296875,-0.0103759765625,0.0059814453125,-0.010986328125,0.034423828125,0.0004425048828125,0.01409912109375,0.00238037109375,-0.032958984375,0.01263427734375,0.02685546875,-0.0113525390625,-0.0037994384765625,0.0057373046875,0.032470703125,0.01226806640625,-0.0057373046875,-0.00921630859375,-0.00518798828125,0.0108642578125,0.0103759765625,0.033935546875,0.00653076171875,-0.00579833984375,0.01171875,-0.046630859375,-0.0196533203125,-0.007537841796875,-0.03564453125,0.0272216796875,0.006591796875,0.00347900390625,-0.0146484375,-0.0028839111328125,-0.005706787109375,0.022216796875,0.004302978515625,-0.0123291015625,-0.00653076171875,0.029296875,-0.005462646484375,0.06298828125,0.003204345703125,-0.0031585693359375,-0.0172119140625,-0.038818359375,-0.016357421875,0.0157470703125,0.00157928466796875,-0.00921630859375,0.0260009765625,-0.00188446044921875,-0.039306640625,-0.039794921875,0.0308837890625,0.0322265625,-0.0245361328125,-0.011962890625,0.016357421875,-0.031982421875,-0.0311279296875,0.0042724609375,-0.0308837890625,-0.00384521484375,0.0211181640625,-0.01397705078125,0.01171875,0.0281982421875,0.0130615234375,0.0234375,0.011474609375,0.0172119140625,0.00335693359375,0.004486083984375,-0.0228271484375,-0.000255584716796875,-0.002655029296875,0.00141143798828125,-0.031982421875,0.007049560546875,-0.04931640625,-0.0034332275390625,0.003173828125,0.0260009765625,-0.01611328125,0.0135498046875,0.031982421875,-0.000919342041015625,-0.0146484375,-0.0294189453125,0.0281982421875,-0.053466796875,-0.0283203125,0.033447265625,0.00069427490234375,-0.0027618408203125,0.03173828125,0.061767578125,-0.03759765625,-0.0296630859375,-0.0301513671875,0.00347900390625,0.030029296875,0.042236328125,-0.0252685546875,-0.0038909912109375,-0.0294189453125,0.025634765625,0.0111083984375,0.02001953125,-0.020263671875,-0.004974365234375,-0.011474609375,0.0235595703125,-0.038330078125,-0.005218505859375,-0.01708984375,0.0032501220703125,0.007537841796875,0.0010528564453125,-0.0157470703125,-0.006622314453125,-0.036376953125,-0.027099609375,0.0010223388671875,-0.1455078125,-0.006439208984375,-0.0272216796875,0.013916015625,0.0018310546875,0.007232666015625,-0.00201416015625,-0.005401611328125,0.0098876953125,-0.00848388671875,0.0096435546875,0.0233154296875,-0.00677490234375,-0.0020599365234375,0.015625,-0.00147247314453125,-0.01043701171875,-0.00823974609375,-0.00750732421875,0.01177978515625,-0.021728515625,0.0050048828125,0.036376953125,-0.02783203125,0.003082275390625,0.017578125,-0.0120849609375,-0.000732421875,-0.00201416015625,-0.00018215179443359375,0.01171875,-0.008056640625,-0.021240234375,-0.009521484375,0.003631591796875,-0.04052734375,0.00970458984375,0.0091552734375,0.01483154296875,-0.03466796875,-0.00897216796875,-0.013427734375,0.03564453125,0.0260009765625,0.01007080078125,-0.0137939453125,-0.029296875,0.00390625,0.01092529296875,-0.00146484375,0.01165771484375,-0.0172119140625,-0.0021209716796875,-0.006988525390625,0.02197265625,0.01556396484375,0.00555419921875,0.0159912109375,0.0179443359375,-0.003448486328125,0.002044677734375,0.040771484375,0.037841796875,0.041259765625,-0.034423828125,0.0174560546875,0.002655029296875,0.01324462890625,-0.00933837890625,0.02392578125,-0.033935546875,-0.033935546875,0.006256103515625,0.01190185546875,-0.00909423828125,-0.016845703125,0.01544189453125,-0.00225830078125,0.0301513671875,0.031982421875,0.030029296875,-0.0089111328125,0.0157470703125,-0.021240234375,-0.04248046875,-0.033203125,0.018310546875,0.025390625,-0.019775390625,-0.009033203125,0.03564453125,0.0169677734375,-0.0267333984375,-0.0123291015625,-0.0057373046875,0.04150390625,-0.0301513671875,0.01214599609375,0.0029144287109375,-0.0225830078125,-0.047119140625,-0.0703125,0.029052734375,0.07177734375,-0.0069580078125,-0.037109375,-0.000102996826171875,-0.041259765625,0.052734375,-0.00726318359375,0.01544189453125,-0.01324462890625,-0.0146484375,-0.0120849609375,0.017333984375,0.0380859375,0.0263671875,0.01092529296875,-0.018310546875,-0.01153564453125,-0.028564453125,0.00732421875,0.00860595703125,-0.011474609375,0.005401611328125,-0.0341796875,-0.01483154296875,-0.00537109375,0.02783203125,0.0023193359375,0.0150146484375,-0.00008821487426757812,-0.004302978515625,0.0115966796875,-0.00726318359375,0.07275390625,0.0238037109375,0.022216796875,0.043212890625,0.000705718994140625,0.0081787109375,0.016357421875,0.0137939453125,0.0189208984375,0.0157470703125,0.0002689361572265625,-0.0322265625,0.03271484375,-0.004364013671875,0.049072265625,-0.0023040771484375,-0.0257568359375,-0.004058837890625,-0.0130615234375,0.01129150390625,0.02197265625,-0.00830078125,0.00244140625,0.042724609375,-0.011962890625,-0.0400390625,0.01129150390625,-0.0252685546875,-0.0034942626953125,-0.03271484375,0.01177978515625,-0.024169921875,0.00157928466796875,-0.00335693359375,0.01123046875,-0.04248046875,0.00848388671875,0.005645751953125,0.01263427734375,0.011474609375,0.022705078125,0.013916015625,0.00136566162109375,0.00885009765625,-0.025634765625,-0.022705078125,-0.009033203125,-0.015380859375,0.00054931640625,-0.0026397705078125,-0.0250244140625,-0.042724609375,-0.0023651123046875,-0.019287109375,0.05712890625,-0.010986328125,0.007049560546875,0.0220947265625,-0.00421142578125,0.0194091796875,-0.0380859375,-0.01397705078125,0.016845703125,0.05224609375,0.00010585784912109375,0.004302978515625,-0.00122833251953125,-0.00176239013671875,-0.0162353515625,0.00750732421875,-0.0274658203125,0.00274658203125,-0.007415771484375,-0.00171661376953125,-0.00848388671875,-0.0225830078125,-0.01019287109375,-0.0034332275390625,-0.022216796875,-0.0098876953125,0.01318359375,0.0137939453125,0.017822265625,0.00396728515625,-0.0260009765625,0.03369140625,0.022705078125,0.00933837890625,0.0019073486328125,0.025634765625,0.01318359375,-0.02099609375,0.0172119140625,0.00836181640625,0.0021209716796875,-0.025634765625,0.03173828125,-0.00970458984375,-0.008056640625,-0.0181884765625,0.0003414154052734375,-0.0233154296875,-0.00616455078125,0.02001953125,0.005950927734375,0.02001953125,0.0245361328125,-0.037353515625,-0.01544189453125,0.00494384765625,0.015380859375,-0.050048828125,-0.048583984375,-0.024658203125,0.0281982421875,-0.0267333984375,-0.000637054443359375,-0.042236328125,0.0169677734375,0.02001953125,-0.01190185546875,-0.0172119140625,0.004669189453125,0.03125,-0.06396484375,-0.006439208984375,-0.011474609375,-0.02294921875,-0.0213623046875,0.0025482177734375,-0.046630859375,0.041015625,-0.0004425048828125,0.0037078857421875,-0.0126953125,-0.022216796875,-0.00921630859375,-0.0052490234375,0.01519775390625,0.0172119140625,0.00140380859375,-0.00604248046875,-0.00653076171875,0.0159912109375,-0.046142578125,-0.0130615234375,0.00457763671875,0.005950927734375,0.0050048828125,-0.0050048828125,0.0218505859375,0.0024566650390625,0.003997802734375,0.016845703125,-0.00183868408203125,0.01336669921875,0.00116729736328125,-0.000377655029296875,-0.000827789306640625,0.016845703125,-0.0167236328125,0.00286865234375,0.0016326904296875,0.01397705078125,-0.00860595703125,0.020751953125,-0.0245361328125,0.04931640625,-0.004302978515625,0.0024566650390625,0.00830078125,-0.03515625,-0.023193359375,-0.0294189453125,0.01611328125,0.01519775390625,0.024169921875,0.0078125,0.0186767578125,-0.01141357421875,-0.03662109375,-0.0267333984375,0.025634765625,-0.0146484375,0.03369140625,-0.053466796875,-0.0174560546875,0.00750732421875,0.033935546875,0.000125885009765625,-0.000873565673828125,0.0478515625,-0.0113525390625,0.015380859375,0.015625,-0.021728515625,-0.030517578125,0.03466796875,-0.000926971435546875,-0.06005859375,0.003662109375,0.015869140625,0.0211181640625,0.0169677734375,0.024658203125,0.024658203125,0.0103759765625,0.03466796875,-0.004791259765625,-0.00848388671875,0.00933837890625,0.001983642578125,0.0013885498046875,-0.0103759765625,0.0194091796875,0.0098876953125,-0.005340576171875,-0.0030670166015625,-0.0233154296875,0.02685546875,0.0076904296875,0.033203125,0.007354736328125,0.01263427734375,0.022705078125,-0.000827789306640625,0.0004863739013671875,0.046630859375,0.0294189453125,-0.000949859619140625,-0.00994873046875,0.00396728515625,-0.0074462890625,0.011962890625,0.0233154296875,0.005096435546875,-0.0087890625,0.00860595703125,0.0439453125,-0.002288818359375,0.017578125,0.01055908203125,-0.00921630859375,0.02001953125,-0.0186767578125,0.01251220703125,0.0018157958984375,-0.050537109375,-0.0257568359375,0.01434326171875,-0.02001953125,0.01025390625,-0.01373291015625,0.050537109375,0.02294921875,-0.01043701171875,0.08544921875,0.010498046875,0.0218505859375,0.005950927734375,-0.0196533203125,0.0185546875,-0.03076171875,-0.0048828125,0.0250244140625,-0.024169921875,-0.002899169921875,0.001953125,-0.001129150390625,-0.0257568359375,0.033935546875,0.00689697265625,-0.009765625,-0.0301513671875,-0.00286865234375,0.0211181640625,0.05859375,-0.01324462890625,0.005645751953125,0.03857421875,-0.0137939453125,0.00390625,-0.0174560546875,0.0283203125,0.00029754638671875,-0.03466796875,-0.006988525390625,-0.0033416748046875,0.0137939453125,0.0301513671875,-0.01336669921875,-0.007415771484375,0.03271484375,-0.00147247314453125,0.00634765625,-0.0184326171875,0.026123046875,0.0103759765625,-0.02392578125,-0.0281982421875,0.004608154296875,-0.006256103515625,0.00010728836059570312,0.006805419921875,-0.004180908203125,0.027587890625,0.01904296875,-0.01318359375,0.015869140625,0.0107421875,-0.0234375,0.016845703125,-0.004669189453125,-0.02783203125,0.0238037109375,0.041748046875,0.015625,0.0096435546875,-0.01263427734375,-0.007415771484375,0.004852294921875,0.031982421875,-0.0257568359375,-0.007232666015625,0.0213623046875,-0.01019287109375,-0.021728515625,0.03955078125,0.00787353515625,-0.0024566650390625,0.01904296875,0.00762939453125,-0.0123291015625,-0.0159912109375,-0.00518798828125,0.00946044921875,-0.01300048828125,0.03125,0.01025390625,0.0203857421875,0.027099609375,0.02978515625,-0.0019989013671875,-0.00726318359375,0.0012969970703125,0.0048828125,-0.007354736328125,-0.0281982421875,0.0126953125,0.01416015625,-0.002410888671875,0.0361328125,-0.00921630859375,-0.0267333984375,-0.016845703125,-0.00860595703125,0.01129150390625,0.01129150390625,0.0024871826171875,0.02392578125,-0.00433349609375,-0.01043701171875,0.00147247314453125,0.00052642822265625,0.00830078125,-0.0301513671875,-0.04443359375,0.03857421875,0.02001953125,0.0196533203125,-0.049072265625,0.003662109375,0.0235595703125,-0.01904296875,0.01324462890625,-0.016845703125,-0.052001953125,-0.007781982421875,0.01214599609375,0.00933837890625,0.0025634765625,-0.018310546875,0.031494140625,0.03369140625,0.0186767578125,0.0303955078125,-0.003448486328125,-0.0208740234375,-0.007049560546875,0.00567626953125,0.0211181640625,-0.004486083984375,-0.0103759765625,0.049072265625,-0.037353515625,0.00130462646484375,-0.033203125,0.056640625,0.025634765625,-0.00151824951171875,0.00994873046875,-0.036865234375,0.02392578125,-0.0732421875,0.00762939453125,0.013916015625,0.01708984375,-0.0076904296875,-0.051513671875,0.00445556640625,-0.01025390625,-0.010009765625,0.00982666015625,0.005218505859375,0.03662109375,0.004302978515625,0.00604248046875,-0.034912109375,0.01019287109375,0.0013885498046875,0.031982421875,-0.0311279296875,-0.01361083984375,0.007171630859375,0.017578125,-0.0242919921875,-0.007415771484375,0.046142578125,0.0162353515625,-0.00689697265625,0.00714111328125,-0.00933837890625,0.0223388671875,0.002685546875,-0.01300048828125,0.015869140625,-0.03076171875,-0.01507568359375,0.0135498046875,-0.001220703125,-0.0037078857421875,-0.00482177734375,-0.029052734375,-0.0245361328125,-0.04248046875,-0.01409912109375,-0.09326171875,-0.01153564453125,0.0087890625,0.0257568359375,0.0279541015625,0.0294189453125,0.00201416015625,-0.005462646484375,-0.018310546875,0.0162353515625,0.0029449462890625,-0.012451171875,0.009033203125,0.023193359375,0.000659942626953125,-0.00384521484375,-0.01397705078125,0.0233154296875,-0.052978515625,-0.01251220703125,-0.00836181640625,0.01904296875,-0.043701171875,0.0107421875,-0.0107421875,-0.0260009765625,-0.0206298828125,-0.0189208984375,-0.0303955078125,-0.0030364990234375,-0.0211181640625,-0.0087890625,-0.003082275390625,0.006683349609375,-0.01300048828125,0.0069580078125,0.01904296875,-0.00982666015625,-0.021240234375,0.0137939453125,0.0003719329833984375,0.018310546875,-0.0103759765625,-0.01226806640625,0.0091552734375,-0.020263671875,-0.012451171875,-0.0146484375,-0.01708984375,0.0128173828125,-0.017333984375,-0.0164794921875,0.0224609375,0.00244140625,-0.007049560546875,-0.005340576171875,0.003448486328125,0.033447265625,-0.005645751953125,0.00750732421875,-0.0007781982421875,-0.03076171875,0.00531005859375,0.01373291015625,0.0172119140625,0.00049591064453125,0.0087890625,-0.0111083984375,0.021240234375,0.00604248046875,-0.0087890625,0.01263427734375,-0.00811767578125,0.04345703125,0.01373291015625,0.02392578125,0.01171875,-0.0361328125,0.006866455078125,0.00872802734375,0.013916015625,-0.01214599609375,0.00927734375,-0.0179443359375,0.00885009765625,0.0228271484375,0.01806640625,-0.015380859375,-0.0031585693359375,0.0023040771484375,0.0021820068359375,0.0026092529296875,-0.037841796875,-0.045166015625,-0.01507568359375,0.004119873046875,0.036376953125,-0.002105712890625,-0.0211181640625,-0.000850677490234375,-0.00970458984375,0.021728515625,0.004486083984375,0.031494140625,0.00144195556640625,0.0225830078125,-0.0120849609375,0.0322265625,-0.025634765625,-0.0050048828125,-0.01129150390625,-0.0286865234375,0.00689697265625,-0.004302978515625,0.0303955078125,0.0341796875,-0.0130615234375,-0.01129150390625,-0.032958984375,0.0257568359375,0.0172119140625,0.0164794921875,-0.017578125,0.013427734375,-0.00787353515625,-0.00921630859375,0.0211181640625,0.03369140625,-0.0164794921875,0.0208740234375,0.01708984375,0.00970458984375,-0.01507568359375,0.0235595703125,-0.02978515625,0.01287841796875,-0.00958251953125,-0.013916015625,0.00122833251953125,0.0107421875,-0.0281982421875,0.028564453125,0.0159912109375,0.00146484375,0.0123291015625,0.004302978515625,-0.007049560546875,-0.002410888671875,-0.01708984375,0.00836181640625,0.041748046875,0.029052734375,0.0020751953125,-0.03759765625,0.00823974609375,0.00396728515625,-0.00185394287109375,0.015869140625,-0.01251220703125,-0.0169677734375,-0.007354736328125,0.0111083984375,0.0164794921875,0.0166015625,0.005706787109375,0.033203125,-0.00193023681640625,0.03076171875,0.0194091796875,0.006805419921875,-0.0052490234375,-0.0218505859375,0.00921630859375,-0.00086212158203125,-0.00274658203125,-0.01507568359375,-0.0361328125,-0.0235595703125,0.02294921875,0.000766754150390625,-0.06787109375,0.02978515625,-0.007781982421875,0.0137939453125,-0.019775390625,-0.0225830078125,0.00010776519775390625,-0.0252685546875,-0.0184326171875,0.0201416015625,-0.00897216796875,0.00762939453125,-0.0164794921875,-0.002197265625,0.008056640625,-0.050048828125,-0.0152587890625,-0.0191650390625,-0.00115203857421875,0.003997802734375,0.0235595703125,-0.005218505859375,-0.010986328125,0.0111083984375,0.0263671875,-0.0198974609375,0.045166015625,0.0006256103515625,-0.019287109375,-0.0084228515625,-0.0198974609375,-0.03271484375,-0.003936767578125,0.04833984375,-0.002777099609375,0.00335693359375,-0.031494140625,-0.0078125,-0.020751953125,-0.019775390625,-0.0169677734375,0.0233154296875,-0.038818359375,-0.0169677734375,0.032958984375,-0.00171661376953125,0.0002002716064453125,0.00019168853759765625,0.01287841796875,-0.040283203125,0.00634765625,0.03564453125,-0.035888671875,0.0045166015625,0.000514984130859375,-0.01171875,-0.0267333984375,-0.01025390625,-0.033203125,-0.0031280517578125,0.005340576171875,-0.0191650390625,-0.00311279296875,0.0225830078125,0.0135498046875,0.01904296875,0.0281982421875,-0.003082275390625,0.021728515625,0.0213623046875,-0.018310546875,0.0260009765625,0.02685546875,0.0260009765625,0.0260009765625,-0.01507568359375,-0.0137939453125,0.033203125,0.0186767578125,-0.007720947265625,-0.00238037109375,0.00250244140625,-0.0087890625,0.0169677734375,-0.00098419189453125,0.00994873046875,0.0145263671875,-0.043212890625,-0.0128173828125,0.01171875,-0.0130615234375,0.006072998046875,0.0172119140625,-0.00946044921875,0.01611328125,0.00019741058349609375,-0.021484375,-0.0252685546875,-0.03564453125,0.026123046875,0.03857421875,0.007080078125,0.0167236328125,0.00567626953125,0.01055908203125,-0.01324462890625,-0.02490234375,-0.017822265625,0.0081787109375,0.01129150390625,0.0179443359375,0.024658203125,-0.0084228515625,-0.037353515625,-0.0120849609375,-0.00714111328125,0.005859375,-0.025390625,0.006072998046875,0.00189971923828125,0.020751953125,-0.004302978515625,0.006500244140625,-0.021728515625,-0.00011920928955078125,0.0128173828125,0.047119140625,-0.0194091796875,0.02001953125,0.006622314453125,0.0078125,0.017578125,-0.025634765625,-0.0189208984375,0.022216796875,-0.020263671875,0.004425048828125,0.017578125,0.044677734375,-0.005645751953125,0.01446533203125,-0.0172119140625,0.01226806640625,-0.037353515625,0.004974365234375,0.01318359375,-0.0115966796875,0.016845703125,0.003662109375,-0.02001953125,0.006683349609375,-0.01904296875,-0.01263427734375,0.0211181640625,0.0294189453125,-0.054443359375,0.01153564453125,-0.00183868408203125,-0.006072998046875,-0.005218505859375,-0.003814697265625,-0.0194091796875,0.0098876953125,-0.0036773681640625,-0.01123046875,0.0281982421875,-0.0017852783203125,0.03515625,0.01904296875,-0.01708984375,0.0172119140625,0.00506591796875,-0.0235595703125,0.0035400390625,0.00070953369140625,0.037353515625,-0.006256103515625,0.00102996826171875,-0.003448486328125,-0.00537109375,0.0260009765625,-0.040771484375,0.044189453125,-0.00118255615234375,0.00994873046875,0.0107421875,-0.0015411376953125,-0.0084228515625,-0.0111083984375,0.0211181640625,0.018798828125,-0.003173828125,-0.01104736328125,-0.0238037109375,-0.01177978515625,0.00689697265625,0.046875,-0.038818359375,-0.00299072265625,-0.00714111328125,-0.0137939453125,0.009765625,-0.0242919921875,0.01153564453125,-0.01226806640625,0.00069427490234375,-0.00244140625,-0.009033203125,-0.017578125,-0.01434326171875,-0.00537109375,0.017822265625,-0.015869140625,-0.010498046875,0.017578125,0.016845703125,0.00010061264038085938,0.017578125,-0.00057220458984375,0.00616455078125,-0.00927734375,0.013916015625,-0.001678466796875,0.002655029296875,-0.0111083984375,-0.0194091796875,0.0093994140625,0.042724609375,0.03369140625,-0.0189208984375,-0.0034942626953125,0.01263427734375,0.016845703125,-0.02001953125,0.005767822265625,-0.0191650390625,0.0076904296875,-0.04345703125,0.015625,-0.01190185546875,0.024169921875,0.0013427734375,-0.0245361328125,-0.020263671875,-0.02197265625,0.0003662109375,0.0113525390625,0.02783203125,0.028564453125,0.00726318359375,-0.0101318359375,-0.00006103515625,-0.0000553131103515625,-0.0341796875,-0.0301513671875,0.0028076171875,-0.07177734375,-0.00909423828125,-0.010009765625,-0.034423828125,-0.005889892578125,0.009033203125,0.017333984375,0.0016326904296875,0.01007080078125,-0.010986328125,-0.0235595703125,0.007537841796875,-0.02001953125,-0.036865234375,-0.00421142578125,-0.01153564453125,0.0157470703125,-0.022216796875,0.01446533203125,0.003814697265625,0.0076904296875,0.0078125,0.01226806640625,0.01171875,0.06005859375,-0.05078125,-0.0458984375,-0.0159912109375,-0.01446533203125,-0.01434326171875,-0.0234375,0.0103759765625,0.033935546875,0.02978515625,-0.00189971923828125,0.015869140625,-0.01007080078125,0.0224609375,0.013916015625,-0.0142822265625,-0.000789642333984375,0.0198974609375,-0.00848388671875,0.01092529296875,0.0208740234375,0.0213623046875,-0.044921875,0.00732421875,0.01318359375,0.01904296875,0.037841796875,-0.0186767578125,-0.034423828125,0.03271484375,-0.005126953125,0.0224609375,-0.0186767578125,0.0025634765625,0.025634765625,0.000667572021484375,-0.044677734375,0.034912109375,0.04443359375,0.021240234375,0.005340576171875,-0.00860595703125,-0.00439453125,-0.02490234375,0.0018157958984375,0.01129150390625,-0.030517578125,-0.0137939453125,0.03515625,-0.025390625,0.025390625,-0.007049560546875,0.023193359375,-0.0263671875,0.0107421875,0.0026397705078125,-0.00372314453125,-0.0203857421875,-0.0257568359375,0.002471923828125,0.0013885498046875,-0.0274658203125,-0.023193359375,-0.050048828125,0.01904296875,-0.032958984375,-0.00970458984375,-0.01397705078125,-0.0103759765625,0.0037689208984375,-0.00604248046875,0.0027923583984375,-0.0050048828125,-0.03515625,0.0498046875,-0.0234375,-0.0478515625,-0.0023345947265625,0.00107574462890625,0.0026702880859375,0.025634765625,0.007354736328125,0.004852294921875,0.0267333984375,-0.0028839111328125,0.004150390625,0.007354736328125,-0.025390625,-0.00518798828125,0.046142578125,-0.00518798828125,-0.00836181640625,0.01373291015625,0.006072998046875,0.0159912109375,-0.032958984375,0.0233154296875,-0.0103759765625,-0.017578125,-0.029052734375,-0.01190185546875,0.0233154296875,-0.010009765625,0.01483154296875,-0.0301513671875,0.0128173828125,-0.037353515625,-0.00750732421875,0.00811767578125,0.01214599609375,0.024169921875,-0.0296630859375,0.0113525390625,0.031982421875,-0.0250244140625,-0.0206298828125,-0.005218505859375,0.0228271484375,0.00555419921875,-0.00445556640625,0.00634765625,-0.0126953125,-0.01141357421875,0.00131988525390625,0.0142822265625,0.0111083984375,0.0311279296875,0.02099609375,-0.006683349609375,0.031982421875,0.005889892578125,0.032958984375,0.002197265625,-0.01708984375,0.03369140625,-0.007781982421875,-0.005584716796875,0.00604248046875,-0.02001953125,-0.005706787109375,0.0037078857421875,0.0135498046875,-0.006439208984375,-0.00909423828125,-0.021484375,-0.0048828125,-0.0196533203125,-0.036376953125,-0.002105712890625,0.0026702880859375,0.02294921875,0.0174560546875,-0.0146484375,0.0211181640625,-0.0034332275390625,0.062255859375,0.013427734375,0.00518798828125,-0.00201416015625,-0.026123046875,-0.024658203125,-0.03662109375,0.02294921875,0.020263671875,0.01373291015625,0.009521484375,0.0026397705078125,0.06298828125,0.0303955078125,0.0203857421875,0.016845703125,0.00238037109375,-0.043701171875,0.01129150390625,-0.01806640625,0.0230712890625,-0.052734375,0.0040283203125,0.03955078125,-0.030029296875,-0.01397705078125,-0.0016021728515625,-0.03759765625,0.045166015625,0.0152587890625,-0.0078125,0.01177978515625,-0.034423828125,0.0303955078125,0.039306640625,-0.0208740234375,-0.0279541015625,0.0035247802734375,0.007537841796875,-0.001983642578125,0.00162506103515625,-0.031982421875,-0.02685546875,-0.0260009765625,-0.0126953125,0.009521484375,-0.027587890625,-0.0218505859375,0.0203857421875,0.0390625,-0.037353515625,0.046875,0.03369140625,0.01287841796875,-0.0002536773681640625,0.0216064453125,0.011474609375,0.0224609375,-0.0242919921875,0.01007080078125,0.021240234375,-0.0120849609375,0.007781982421875,-0.0091552734375,0.028564453125,0.00762939453125,-0.031982421875,0.017333984375,-0.00823974609375,-0.01190185546875,-0.00811767578125,0.01263427734375,0.050537109375,0.031982421875,0.007598876953125,0.024658203125,-0.01507568359375,-0.01019287109375,0.0203857421875,-0.02783203125,-0.0185546875,0.03076171875,0.008056640625,0.0245361328125,0.040283203125,-0.00162506103515625,0.0400390625,0.032470703125,-0.0111083984375,0.00372314453125,0.062255859375,-0.0286865234375,-0.01544189453125,0.0179443359375,0.03662109375,-0.042236328125,0.0033416748046875,0.0029144287109375,0.000713348388671875,-0.0098876953125,-0.02197265625,-0.0126953125,0.0208740234375,0.00836181640625,-0.0390625,-0.003448486328125,0.00885009765625,-0.01300048828125,-0.047119140625,0.01806640625,-0.046630859375,-0.01544189453125,0.0087890625,0.0272216796875,-0.0189208984375,-0.018310546875,-0.0283203125,0.00311279296875,0.0145263671875,0.005462646484375,0.0057373046875,0.007720947265625,-0.027099609375,-0.03662109375,0.0242919921875,-0.0218505859375,0.0303955078125,0.01104736328125,-0.006622314453125,-0.00823974609375,0.017822265625,0.0169677734375,0.01177978515625,0.02783203125,-0.01336669921875,0.0166015625,0.00421142578125,-0.002349853515625,0.00060272216796875,0.0035400390625,-0.06787109375,-0.0024871826171875,0.03466796875,-0.002105712890625,0.000881195068359375,0.0208740234375,0.0181884765625,-0.0054931640625,0.0037994384765625,-0.001922607421875,-0.0014190673828125,-0.02099609375,0.013916015625,-0.01287841796875,-0.0091552734375,0.00830078125,-0.01092529296875,0.00579833984375,0.01336669921875,0.01025390625,0.0203857421875,0.03564453125,0.0301513671875,0.0020751953125,-0.011962890625,-0.00823974609375,0.01190185546875,0.03564453125,0.022216796875,0.018798828125,0.06884765625,-0.000545501708984375,0.0198974609375,0.002532958984375,0.02783203125,-0.00970458984375,-0.0196533203125,-0.032470703125,0.03173828125,0.006439208984375,-0.0135498046875,-0.0189208984375,-0.0159912109375,0.01055908203125,0.0013275146484375,0.021484375,0.0224609375,-0.0089111328125,-0.001190185546875,0.03564453125,0.003997802734375,-0.03515625,0.0107421875,0.00101470947265625,0.0035247802734375,-0.01068115234375,0.0172119140625,-0.01129150390625,0.00933837890625,-0.003997802734375,-0.01507568359375,-0.01287841796875,-0.02099609375,-0.012451171875,-0.017578125,-0.00860595703125,-0.0054931640625,0.01080322265625,0.0126953125,-0.016357421875,-0.0123291015625,0.0087890625,-0.005889892578125,0.0106201171875,0.0172119140625,-0.0186767578125,-0.03662109375,-0.0157470703125,0.019287109375,-0.030517578125,-0.0286865234375,-0.0181884765625,-0.0159912109375,0.007171630859375,-0.0130615234375,0.0263671875,0.007598876953125,0.050537109375,0.021484375,-0.01318359375,0.0078125,-0.0260009765625,-0.0233154296875,-0.010498046875,0.003204345703125,-0.02001953125,0.050048828125,-0.00347900390625,-0.0026397705078125,-0.010498046875,0.006072998046875,-0.0015411376953125,0.0225830078125,0.008056640625,0.0400390625,0.0001373291015625,0.0021514892578125,0.0162353515625,0.0069580078125,-0.03173828125,0.030517578125,0.0115966796875,-0.02978515625,-0.0029296875,0.123046875,-0.0034332275390625,0.040771484375,-0.01336669921875,-0.01251220703125,0.016357421875,0.00787353515625,0.0098876953125,0.00506591796875,0.04296875,-0.0108642578125,0.05419921875,0.00933837890625,-0.02783203125,0.040771484375,0.00011444091796875,-0.003387451171875,0.0006256103515625,0.0220947265625,-0.04443359375,0.0458984375,0.01336669921875,-0.0283203125,-0.015869140625,0.052001953125,0.012451171875,0.0164794921875,0.00799560546875,-0.0267333984375,0.004119873046875,0.00811767578125,0.006256103515625,-0.0027008056640625,0.000457763671875,-0.04833984375,-0.0164794921875,-0.0458984375,0.01556396484375,0.000499725341796875,0.011474609375,0.0400390625,0.0021820068359375,-0.013916015625,0.01611328125,-0.016845703125,-0.03271484375,-0.005859375,-0.0120849609375,-0.0052490234375,0.01708984375,0.00726318359375,-0.006439208984375,0.003997802734375,0.04931640625,-0.017333984375,0.0003414154052734375,0.006805419921875,-0.0027923583984375,-0.0011138916015625,-0.0164794921875,-0.007781982421875,0.0242919921875,0.017822265625,-0.002777099609375,0.0038604736328125,0.0150146484375,-0.004547119140625,-0.002685546875,0.0137939453125,-0.020751953125,0.017578125,0.01611328125,0.0036163330078125,-0.0130615234375,-0.01153564453125,0.00640869140625,0.01361083984375,-0.0033416748046875,0.00238037109375,-0.0250244140625,-0.003753662109375,0.01019287109375,0.0045166015625,-0.025634765625,-0.00018596649169921875,0.037353515625,-0.039306640625,-0.0164794921875,-0.0301513671875,0.0152587890625,-0.017333984375,-0.0018768310546875,0.0245361328125,0.01214599609375,0.0150146484375,0.000247955322265625,0.02294921875,-0.0186767578125,-0.01287841796875,-0.006622314453125,0.0123291015625,0.006134033203125,-0.01092529296875,-0.0186767578125,-0.0091552734375,-0.0059814453125,0.055908203125,0.017822265625,0.0106201171875,-0.0050048828125,0.01300048828125,0.005096435546875,0.01019287109375,0.006134033203125,0.01251220703125,0.0220947265625,0.0120849609375,-0.024658203125,-0.0002117156982421875,0.0024261474609375,0.006622314453125,-0.007781982421875,0.0179443359375,0.0242919921875,-0.00982666015625,-0.0069580078125,0.0111083984375,-0.0169677734375,-0.035400390625,-0.00141143798828125,0.01214599609375,0.007720947265625,-0.030517578125,-0.025390625,0.0023956298828125,-0.0196533203125,0.01300048828125,0.06591796875,0.01470947265625,0.0152587890625,-0.0172119140625,0.0179443359375,0.0263671875,0.018310546875,-0.002716064453125,-0.0257568359375,0.02001953125,-0.00025177001953125,-0.018310546875,0.0208740234375,0.01336669921875,-0.00970458984375,-0.0130615234375,0.0157470703125,-0.035400390625,0.0164794921875,0.002532958984375,-0.005645751953125,-0.000171661376953125,-0.0030364990234375,-0.0279541015625,0.00015735626220703125,-0.01043701171875,0.0203857421875,-0.0179443359375,0.016845703125,0.02392578125,0.050048828125,0.0108642578125,-0.0264892578125,-0.00185394287109375,0.005950927734375,-0.018310546875,0.009765625,-0.00787353515625,0.01043701171875,-0.043701171875,0.0115966796875,-0.0260009765625,0.0220947265625,0.02294921875,0.02294921875,0.02490234375,-0.01287841796875,-0.0234375,-0.01300048828125,0.0059814453125,-0.0048828125,-0.0252685546875,0.0263671875,0.007049560546875,-0.0018157958984375,0.00421142578125,-0.04248046875,-0.0022430419921875,-0.01055908203125,-0.0142822265625,0.01409912109375,-0.00799560546875,-0.00604248046875,0.007781982421875,-0.01416015625,-0.0038909912109375,0.007171630859375,0.00970458984375,-0.001495361328125,0.003936767578125,-0.037353515625,-0.0238037109375,0.005706787109375,-0.0283203125,0.01287841796875,0.0086669921875,0.01470947265625,-0.0174560546875,0.0223388671875,-0.03662109375,0.00653076171875,-0.00927734375,-0.004150390625,-0.0242919921875,-0.032958984375,0.0145263671875,0.0014495849609375,-0.00823974609375,-0.028564453125,-0.013427734375,0.0294189453125,0.04931640625,0.0162353515625,-0.0230712890625,0.00787353515625,0.00933837890625,-0.0091552734375,0.030517578125,-0.044921875,0.01190185546875,-0.002532958984375,0.038330078125,-0.00244140625,0.0157470703125,0.016845703125,-0.0498046875,-0.000453948974609375,-0.0478515625,-0.01190185546875,0.043701171875,0.0213623046875,0.0036773681640625,0.00640869140625,0.000885009765625,0.0108642578125,0.032958984375,-0.0245361328125,0.03955078125,0.0089111328125,0.00286865234375,-0.0028228759765625,-0.007171630859375,0.0281982421875,-0.04296875,0.00372314453125,0.0283203125,0.021240234375,0.01904296875,-0.0108642578125,-0.0286865234375,0.00170135498046875,-0.049072265625,0.00531005859375,-0.0057373046875,-0.01190185546875,0.0137939453125,-0.00396728515625,-0.013916015625,0.0025634765625,0.0225830078125,0.01519775390625,0.0137939453125,-0.004669189453125,0.0150146484375,0.0146484375,-0.00067138671875,-0.004913330078125,-0.018310546875,-0.04296875,-0.056640625,-0.03515625,-0.0274658203125,0.0301513671875,0.0069580078125,0.029296875,0.001922607421875,0.0057373046875,0.0135498046875,0.00286865234375,0.020263671875,0.01397705078125,-0.0012969970703125,0.00225830078125,-0.01116943359375,0.0189208984375,-0.01361083984375,0.007232666015625,0.0233154296875,0.018310546875,0.04052734375,0.01263427734375,0.016845703125,-0.0069580078125,-0.005859375,-0.04052734375,-0.009521484375,0.0038604736328125,-0.00140380859375,0.02099609375,-0.0096435546875,0.01544189453125,0.0220947265625,0.00634765625,-0.0003070831298828125,-0.0322265625,0.00121307373046875,0.01446533203125,0.01470947265625,0.0023345947265625,0.024169921875,0.01397705078125,-0.0107421875,-0.00823974609375,0.01068115234375,0.03466796875,0.024169921875,-0.01409912109375,-0.00799560546875,0.0196533203125,0.006988525390625,-0.00128936767578125,0.033935546875,-0.00653076171875,0.044189453125,-0.01007080078125,-0.00848388671875,0.018310546875,0.034423828125,0.00921630859375,-0.037109375,0.0033416748046875,-0.0186767578125,0.01263427734375,0.028564453125,0.00616455078125,0.0029296875,-0.01519775390625,-0.03125,-0.005126953125,0.004852294921875,0.016845703125,0.0035400390625,-0.031494140625,-0.00176239013671875,0.02783203125,-0.00927734375,-0.0128173828125,-0.004302978515625,-0.050537109375,-0.0245361328125,0.00015354156494140625,0.0029449462890625,0.035888671875,-0.00335693359375,0.057373046875,0.0034942626953125,0.0235595703125,0.00176239013671875,-0.0123291015625,-0.03662109375,0.0002880096435546875,-0.053466796875,0.006439208984375,-0.002197265625,-0.00007104873657226562,0.021484375,-0.005279541015625,0.044677734375,-0.0308837890625,-0.0024261474609375,-0.01361083984375,-0.0036773681640625,-0.01092529296875,0.003387451171875,0.00127410888671875,0.005462646484375,0.01556396484375,0.004364013671875,0.0478515625,0.002685546875,-0.004852294921875,0.020751953125,-0.003936767578125,0.03515625,0.005615234375,0.0145263671875,0.006134033203125,-0.01043701171875,-0.0089111328125,0.0179443359375,0.009521484375,-0.01373291015625,0.01361083984375,-0.06298828125,-0.000499725341796875,0.028564453125,0.028564453125,-0.022216796875,0.00970458984375,-0.0021514892578125,-0.0076904296875,0.05859375,-0.002410888671875,-0.07763671875,0.0211181640625,0.0211181640625,0.004241943359375,-0.004913330078125,0.06884765625,-0.028564453125,-0.0296630859375,-0.0556640625,0.0084228515625,0.0033416748046875,-0.005645751953125,-0.0142822265625,-0.0108642578125,0.011962890625,-0.030029296875,-0.00677490234375,0.0169677734375,-0.00146484375,0.005859375,0.005828857421875,0.00848388671875,0.0211181640625,0.030517578125,0.014892578125,-0.032470703125,-0.010986328125,0.006683349609375,-0.0010986328125,0.0004520416259765625,0.003448486328125,0.01055908203125],"index":0,"object":"embedding"}],"model":"doubao-embedding-vision","object":"list","usage":{"prompt_tokens":23,"total_tokens":23}}`
const volcengineAgentEmbeddingResponse = `{"created":1791450147,"id":"0217914501476757820404c56ac6dd9ce215ba8b602495ab75245","data":[{"embedding":[0.036865234375,-0.02392578125,-0.01495361328125,0.0201416015625,-0.010986328125,0.0225830078125,-0.0137939453125,0.016845703125,-0.002166748046875,-0.00592041015625,-0.0002593994140625,-0.042236328125,0.03466796875,-0.01165771484375,-0.0189208984375,0.016357421875,-0.0419921875,0.0302734375,0.00823974609375,-0.007080078125,0.00115203857421875,0.023681640625,-0.0107421875,0.0072021484375,-0.01025390625,0.035888671875,0.00165557861328125,0.0130615234375,0.00262451171875,-0.031494140625,0.01251220703125,0.02587890625,-0.01202392578125,-0.0022735595703125,0.00543212890625,0.0291748046875,0.0115966796875,-0.0033111572265625,-0.00799560546875,-0.004302978515625,0.0101318359375,0.0106201171875,0.036865234375,0.00567626953125,-0.0033721923828125,0.012939453125,-0.04638671875,-0.0181884765625,-0.0086669921875,-0.0341796875,0.0279541015625,0.00799560546875,0.0030975341796875,-0.0152587890625,-0.0017242431640625,-0.00469970703125,0.0244140625,0.0027313232421875,-0.0115966796875,-0.005340576171875,0.02978515625,-0.007080078125,0.0625,0.001983642578125,-0.00323486328125,-0.0166015625,-0.0390625,-0.0184326171875,0.016357421875,-0.00173187255859375,-0.01007080078125,0.0272216796875,-0.002685546875,-0.0390625,-0.039306640625,0.032470703125,0.031982421875,-0.0250244140625,-0.0113525390625,0.01611328125,-0.031982421875,-0.03125,0.0037841796875,-0.0306396484375,-0.0024871826171875,0.0224609375,-0.014892578125,0.01214599609375,0.02734375,0.0125732421875,0.02392578125,0.01123046875,0.0186767578125,0.003448486328125,0.006195068359375,-0.022705078125,-0.00115203857421875,-0.001800537109375,0.001129150390625,-0.032958984375,0.00701904296875,-0.05029296875,-0.002655029296875,0.004791259765625,0.026123046875,-0.0174560546875,0.013427734375,0.033203125,-0.000530242919921875,-0.01348876953125,-0.0302734375,0.026611328125,-0.052490234375,-0.026123046875,0.032470703125,-0.00016117095947265625,-0.003631591796875,0.032470703125,0.06201171875,-0.03515625,-0.0322265625,-0.0269775390625,0.004241943359375,0.030517578125,0.044189453125,-0.0250244140625,-0.0052490234375,-0.031005859375,0.02685546875,0.010009765625,0.01904296875,-0.020263671875,-0.00494384765625,-0.0115966796875,0.02197265625,-0.036865234375,-0.004730224609375,-0.0159912109375,0.00160980224609375,0.00701904296875,-0.00035858154296875,-0.015625,-0.006927490234375,-0.0361328125,-0.026123046875,0.000885009765625,-0.1416015625,-0.00762939453125,-0.025390625,0.01531982421875,0.0003871917724609375,0.004730224609375,-0.00067901611328125,-0.00469970703125,0.0093994140625,-0.00958251953125,0.0096435546875,0.0228271484375,-0.005523681640625,-0.001129150390625,0.017333984375,-0.000492095947265625,-0.0089111328125,-0.0103759765625,-0.00872802734375,0.0126953125,-0.020751953125,0.00396728515625,0.036865234375,-0.029052734375,0.0025482177734375,0.0169677734375,-0.0130615234375,-0.001708984375,-0.0030517578125,0.0002574920654296875,0.0126953125,-0.00885009765625,-0.0213623046875,-0.0098876953125,0.003143310546875,-0.041259765625,0.00994873046875,0.00958251953125,0.01324462890625,-0.034912109375,-0.00921630859375,-0.014404296875,0.035400390625,0.024658203125,0.01123046875,-0.0147705078125,-0.0286865234375,0.004241943359375,0.01141357421875,-0.00086212158203125,0.0108642578125,-0.01708984375,-0.0028228759765625,-0.007537841796875,0.021240234375,0.01287841796875,0.00372314453125,0.0177001953125,0.017578125,-0.004241943359375,0.002471923828125,0.04052734375,0.036865234375,0.039306640625,-0.035400390625,0.018310546875,0.0030975341796875,0.0133056640625,-0.008056640625,0.0250244140625,-0.031005859375,-0.03271484375,0.006011962890625,0.0157470703125,-0.00830078125,-0.01611328125,0.015625,-0.0025787353515625,0.029541015625,0.031005859375,0.0294189453125,-0.009033203125,0.01531982421875,-0.0216064453125,-0.041015625,-0.033935546875,0.01806640625,0.02587890625,-0.018798828125,-0.00958251953125,0.03466796875,0.01544189453125,-0.0291748046875,-0.01397705078125,-0.0028533935546875,0.041015625,-0.02978515625,0.01202392578125,0.00262451171875,-0.0224609375,-0.04931640625,-0.0703125,0.0301513671875,0.07177734375,-0.0064697265625,-0.037841796875,0.00131988525390625,-0.040771484375,0.051513671875,-0.00762939453125,0.017333984375,-0.013671875,-0.015869140625,-0.01214599609375,0.018310546875,0.039306640625,0.026123046875,0.0101318359375,-0.0184326171875,-0.01116943359375,-0.028076171875,0.00775146484375,0.0086669921875,-0.00970458984375,0.006622314453125,-0.034912109375,-0.01422119140625,-0.005401611328125,0.02587890625,0.003692626953125,0.0159912109375,-0.000530242919921875,-0.003509521484375,0.011962890625,-0.00970458984375,0.072265625,0.0250244140625,0.02099609375,0.04345703125,-0.00109100341796875,0.00787353515625,0.0159912109375,0.0137939453125,0.0177001953125,0.0177001953125,0.0010833740234375,-0.0322265625,0.032470703125,-0.0045166015625,0.048828125,-0.0031890869140625,-0.0240478515625,-0.0023040771484375,-0.01324462890625,0.01202392578125,0.0206298828125,-0.00836181640625,0.0023040771484375,0.043701171875,-0.0115966796875,-0.041259765625,0.01239013671875,-0.0250244140625,-0.004302978515625,-0.034423828125,0.01251220703125,-0.0235595703125,0.00433349609375,-0.0030975341796875,0.01123046875,-0.04345703125,0.008056640625,0.004913330078125,0.01214599609375,0.01129150390625,0.0218505859375,0.01470947265625,0.000850677490234375,0.01007080078125,-0.02587890625,-0.0244140625,-0.01123046875,-0.015625,0.000888824462890625,-0.0042724609375,-0.0228271484375,-0.0400390625,-0.00225830078125,-0.0205078125,0.05615234375,-0.0107421875,0.008544921875,0.021484375,-0.006011962890625,0.020751953125,-0.0380859375,-0.0164794921875,0.018798828125,0.052978515625,0.00147247314453125,0.004547119140625,0.00000356137752532959,-0.0040283203125,-0.0167236328125,0.00628662109375,-0.029052734375,0.0032806396484375,-0.007080078125,-0.001495361328125,-0.01055908203125,-0.02197265625,-0.0115966796875,-0.0018463134765625,-0.023193359375,-0.01080322265625,0.01312255859375,0.014892578125,0.0181884765625,0.003265380859375,-0.026123046875,0.033203125,0.0203857421875,0.0107421875,0.0020751953125,0.025390625,0.01275634765625,-0.0233154296875,0.0164794921875,0.007293701171875,0.000518798828125,-0.025146484375,0.032470703125,-0.0101318359375,-0.00830078125,-0.0174560546875,0.000186920166015625,-0.0244140625,-0.00628662109375,0.0206298828125,0.00653076171875,0.0206298828125,0.023681640625,-0.037109375,-0.0162353515625,0.00665283203125,0.01422119140625,-0.05078125,-0.048828125,-0.0262451171875,0.0284423828125,-0.0250244140625,-0.00030517578125,-0.041259765625,0.0167236328125,0.01953125,-0.0113525390625,-0.020263671875,0.0048828125,0.0302734375,-0.0654296875,-0.006561279296875,-0.01141357421875,-0.0233154296875,-0.0224609375,0.0028228759765625,-0.045654296875,0.04052734375,0.00225830078125,0.0034942626953125,-0.0133056640625,-0.0213623046875,-0.00921630859375,-0.0052490234375,0.01434326171875,0.0172119140625,0.00009965896606445312,-0.0064697265625,-0.00469970703125,0.0157470703125,-0.045654296875,-0.0133056640625,0.005828857421875,0.004974365234375,0.0052490234375,-0.0057373046875,0.0224609375,0.001800537109375,0.0037384033203125,0.016845703125,-0.0014801025390625,0.0147705078125,0.0019378662109375,-0.0003509521484375,-0.00159454345703125,0.018310546875,-0.01611328125,0.004791259765625,0.00119781494140625,0.0137939453125,-0.0091552734375,0.0201416015625,-0.024658203125,0.0478515625,-0.00506591796875,0.0030975341796875,0.00933837890625,-0.0361328125,-0.0233154296875,-0.029052734375,0.0177001953125,0.0145263671875,0.0224609375,0.00811767578125,0.019775390625,-0.01422119140625,-0.03759765625,-0.0255126953125,0.025146484375,-0.0140380859375,0.033447265625,-0.058349609375,-0.015869140625,0.00823974609375,0.03466796875,0.00095367431640625,-0.00015354156494140625,0.0478515625,-0.010009765625,0.0169677734375,0.0167236328125,-0.0224609375,-0.03173828125,0.0341796875,-0.000640869140625,-0.06298828125,0.004669189453125,0.0166015625,0.0208740234375,0.0155029296875,0.02587890625,0.0240478515625,0.00994873046875,0.0361328125,-0.004608154296875,-0.0072021484375,0.00830078125,0.000598907470703125,0.0038604736328125,-0.01287841796875,0.01953125,0.00921630859375,-0.005096435546875,-0.00250244140625,-0.02392578125,0.026611328125,0.01019287109375,0.032958984375,0.006988525390625,0.01141357421875,0.022216796875,0.000461578369140625,0.0002613067626953125,0.0478515625,0.0284423828125,-0.00160980224609375,-0.0091552734375,0.004180908203125,-0.00885009765625,0.01123046875,0.0255126953125,0.00689697265625,-0.00665283203125,0.009521484375,0.04345703125,-0.000957489013671875,0.0172119140625,0.011962890625,-0.0096435546875,0.0201416015625,-0.018798828125,0.0133056640625,0.0014801025390625,-0.051025390625,-0.0255126953125,0.01507568359375,-0.01953125,0.00994873046875,-0.0115966796875,0.051025390625,0.0255126953125,-0.01080322265625,0.08642578125,0.00982666015625,0.0225830078125,0.006561279296875,-0.019287109375,0.017333984375,-0.031982421875,-0.00390625,0.0269775390625,-0.0233154296875,-0.0029144287109375,0.0005645751953125,-0.0004444122314453125,-0.0240478515625,0.03466796875,0.0086669921875,-0.009765625,-0.0308837890625,-0.00372314453125,0.0205078125,0.05908203125,-0.01251220703125,0.0068359375,0.039306640625,-0.0145263671875,0.0019378662109375,-0.01611328125,0.02880859375,0.000942230224609375,-0.033935546875,-0.0059814453125,-0.0030059814453125,0.01416015625,0.0283203125,-0.01312255859375,-0.005615234375,0.033203125,-0.00124359130859375,0.005157470703125,-0.0184326171875,0.0272216796875,0.01104736328125,-0.0244140625,-0.028076171875,0.006378173828125,-0.004425048828125,-0.00010776519775390625,0.006072998046875,-0.0052490234375,0.0264892578125,0.020263671875,-0.01385498046875,0.016845703125,0.00811767578125,-0.0250244140625,0.018798828125,-0.00482177734375,-0.0283203125,0.026123046875,0.0380859375,0.0157470703125,0.00970458984375,-0.014404296875,-0.00665283203125,0.005615234375,0.032470703125,-0.025390625,-0.0079345703125,0.021728515625,-0.00994873046875,-0.02099609375,0.038818359375,0.0074462890625,-0.003173828125,0.019775390625,0.0089111328125,-0.01171875,-0.0147705078125,-0.005523681640625,0.0078125,-0.01275634765625,0.0308837890625,0.00909423828125,0.0211181640625,0.0269775390625,0.0286865234375,-0.0021514892578125,-0.00848388671875,0.00152587890625,0.0050048828125,-0.0079345703125,-0.0302734375,0.01251220703125,0.0133056640625,-0.0019073486328125,0.036865234375,-0.007171630859375,-0.0269775390625,-0.01806640625,-0.00970458984375,0.009765625,0.01031494140625,0.004302978515625,0.0250244140625,-0.003509521484375,-0.0106201171875,0.0022735595703125,0.002166748046875,0.00860595703125,-0.02978515625,-0.044921875,0.0380859375,0.01904296875,0.020263671875,-0.04736328125,0.00531005859375,0.0250244140625,-0.01953125,0.0125732421875,-0.01531982421875,-0.05029296875,-0.00653076171875,0.012939453125,0.01019287109375,0.0031890869140625,-0.0184326171875,0.0302734375,0.033203125,0.018798828125,0.0294189453125,-0.003692626953125,-0.021484375,-0.006256103515625,0.007110595703125,0.0203857421875,-0.00421142578125,-0.0108642578125,0.048095703125,-0.037353515625,0.000827789306640625,-0.032958984375,0.058349609375,0.026611328125,-0.002716064453125,0.0084228515625,-0.038330078125,0.0225830078125,-0.0703125,0.00897216796875,0.01507568359375,0.0166015625,-0.0078125,-0.05078125,0.0045166015625,-0.01104736328125,-0.00994873046875,0.0089111328125,0.005340576171875,0.0361328125,0.00506591796875,0.004547119140625,-0.03564453125,0.0096435546875,0.00183868408203125,0.03173828125,-0.03271484375,-0.01324462890625,0.0067138671875,0.0162353515625,-0.0224609375,-0.006195068359375,0.0478515625,0.01611328125,-0.0081787109375,0.006622314453125,-0.00726318359375,0.0224609375,0.0028533935546875,-0.013671875,0.01385498046875,-0.03173828125,-0.0162353515625,0.01422119140625,-0.0020599365234375,-0.004669189453125,-0.003448486328125,-0.0279541015625,-0.0247802734375,-0.042724609375,-0.01611328125,-0.08984375,-0.01220703125,0.0081787109375,0.0250244140625,0.0283203125,0.0286865234375,0.0017242431640625,-0.00531005859375,-0.0198974609375,0.0174560546875,0.00323486328125,-0.01312255859375,0.01025390625,0.0216064453125,0.0003509521484375,-0.0032196044921875,-0.012939453125,0.023681640625,-0.052490234375,-0.01275634765625,-0.0079345703125,0.019287109375,-0.042236328125,0.01129150390625,-0.01251220703125,-0.02587890625,-0.0206298828125,-0.018798828125,-0.0302734375,-0.00341796875,-0.0206298828125,-0.01104736328125,-0.00341796875,0.0068359375,-0.01220703125,0.006256103515625,0.0194091796875,-0.0093994140625,-0.02001953125,0.01239013671875,0.00119781494140625,0.018798828125,-0.00848388671875,-0.011962890625,0.009521484375,-0.0189208984375,-0.0125732421875,-0.0147705078125,-0.0167236328125,0.0118408203125,-0.01708984375,-0.0162353515625,0.023193359375,0.0022125244140625,-0.00579833984375,-0.006561279296875,0.0031280517578125,0.03271484375,-0.00628662109375,0.00799560546875,-0.0010833740234375,-0.030517578125,0.006622314453125,0.01361083984375,0.017578125,0.000244140625,0.01007080078125,-0.01116943359375,0.0234375,0.004852294921875,-0.00946044921875,0.01220703125,-0.00848388671875,0.043212890625,0.01287841796875,0.02392578125,0.01202392578125,-0.036376953125,0.006744384765625,0.01007080078125,0.01434326171875,-0.00994873046875,0.00885009765625,-0.016845703125,0.00958251953125,0.0235595703125,0.0177001953125,-0.0155029296875,-0.003509521484375,0.002166748046875,0.000934600830078125,0.0031280517578125,-0.036376953125,-0.048095703125,-0.014404296875,0.0037384033203125,0.0361328125,-0.0032196044921875,-0.0203857421875,-0.001678466796875,-0.00958251953125,0.0228271484375,0.00457763671875,0.03173828125,0.00136566162109375,0.0220947265625,-0.01239013671875,0.032470703125,-0.02587890625,-0.004119873046875,-0.01165771484375,-0.0308837890625,0.005859375,-0.0040283203125,0.02880859375,0.03369140625,-0.01470947265625,-0.010986328125,-0.033935546875,0.026123046875,0.0159912109375,0.0167236328125,-0.0169677734375,0.01214599609375,-0.00823974609375,-0.0081787109375,0.021240234375,0.033935546875,-0.0189208984375,0.021484375,0.017333984375,0.01068115234375,-0.0157470703125,0.0240478515625,-0.030517578125,0.01458740234375,-0.0118408203125,-0.015625,0.0003452301025390625,0.00994873046875,-0.026611328125,0.028076171875,0.0159912109375,0.00106048583984375,0.013671875,0.00396728515625,-0.0057373046875,-0.0014801025390625,-0.016357421875,0.00994873046875,0.040283203125,0.02880859375,0.00128936767578125,-0.037353515625,0.007110595703125,0.0037384033203125,-0.00170135498046875,0.01458740234375,-0.0123291015625,-0.016357421875,-0.007110595703125,0.010009765625,0.01611328125,0.0169677734375,0.006378173828125,0.03369140625,-0.002166748046875,0.031005859375,0.019775390625,0.007354736328125,-0.00360107421875,-0.0206298828125,0.0093994140625,-0.00000959634780883789,-0.0027008056640625,-0.0166015625,-0.03466796875,-0.0234375,0.023193359375,0.00008153915405273438,-0.06787109375,0.031494140625,-0.007080078125,0.01214599609375,-0.0198974609375,-0.0242919921875,0.00156402587890625,-0.024658203125,-0.0181884765625,0.0205078125,-0.00811767578125,0.00738525390625,-0.0137939453125,-0.0013580322265625,0.00860595703125,-0.050048828125,-0.0157470703125,-0.0206298828125,-0.001312255859375,0.0028533935546875,0.0244140625,-0.00616455078125,-0.0091552734375,0.0140380859375,0.02685546875,-0.0213623046875,0.044677734375,0.0022125244140625,-0.0205078125,-0.00775146484375,-0.0186767578125,-0.033447265625,-0.00077056884765625,0.048095703125,-0.0031280517578125,0.0027923583984375,-0.03125,-0.005706787109375,-0.02099609375,-0.0198974609375,-0.01806640625,0.023193359375,-0.0390625,-0.017333984375,0.031005859375,-0.0020599365234375,0.0010528564453125,0.0012054443359375,0.011962890625,-0.03955078125,0.0074462890625,0.03759765625,-0.03466796875,0.004119873046875,0.001312255859375,-0.0126953125,-0.0269775390625,-0.00885009765625,-0.033935546875,-0.006072998046875,0.00408935546875,-0.0184326171875,-0.002166748046875,0.0244140625,0.0137939453125,0.017333984375,0.026611328125,-0.002532958984375,0.02099609375,0.0205078125,-0.0194091796875,0.0247802734375,0.026611328125,0.0255126953125,0.0264892578125,-0.01507568359375,-0.01611328125,0.034912109375,0.0174560546875,-0.0093994140625,-0.0028533935546875,0.00299072265625,-0.010498046875,0.0169677734375,-0.00150299072265625,0.01214599609375,0.0140380859375,-0.043212890625,-0.01251220703125,0.01251220703125,-0.01239013671875,0.00616455078125,0.01806640625,-0.00946044921875,0.01513671875,-0.000385284423828125,-0.0191650390625,-0.026611328125,-0.035888671875,0.0283203125,0.03857421875,0.007476806640625,0.0162353515625,0.00579833984375,0.00946044921875,-0.0166015625,-0.0240478515625,-0.0179443359375,0.007354736328125,0.0113525390625,0.018798828125,0.0247802734375,-0.0089111328125,-0.033447265625,-0.013427734375,-0.0064697265625,0.004913330078125,-0.0244140625,0.00592041015625,0.001953125,0.0225830078125,-0.006195068359375,0.006378173828125,-0.0205078125,-0.000270843505859375,0.01123046875,0.04638671875,-0.0179443359375,0.0198974609375,0.0086669921875,0.00823974609375,0.0189208984375,-0.0257568359375,-0.0191650390625,0.020263671875,-0.020263671875,0.0048828125,0.018310546875,0.044921875,-0.006072998046875,0.01214599609375,-0.0166015625,0.01220703125,-0.0390625,0.004608154296875,0.01220703125,-0.01141357421875,0.0166015625,0.0035552978515625,-0.02099609375,0.006256103515625,-0.0169677734375,-0.011474609375,0.020751953125,0.0306396484375,-0.052978515625,0.010498046875,-0.0030059814453125,-0.006103515625,-0.005218505859375,-0.004791259765625,-0.0198974609375,0.0086669921875,-0.004669189453125,-0.0118408203125,0.026123046875,-0.003173828125,0.033935546875,0.0201416015625,-0.0167236328125,0.0169677734375,0.0052490234375,-0.0234375,0.0036773681640625,0.0005645751953125,0.037841796875,-0.0064697265625,0.00128936767578125,-0.00396728515625,-0.0064697265625,0.025390625,-0.04345703125,0.046875,-0.00131988525390625,0.01019287109375,0.01220703125,-0.00118255615234375,-0.00897216796875,-0.01068115234375,0.020263671875,0.0179443359375,-0.0030364990234375,-0.01177978515625,-0.0247802734375,-0.01123046875,0.006805419921875,0.046875,-0.039794921875,-0.000762939453125,-0.0052490234375,-0.01348876953125,0.01007080078125,-0.0247802734375,0.01068115234375,-0.0107421875,0.000782012939453125,-0.001617431640625,-0.008056640625,-0.01806640625,-0.01416015625,-0.006011962890625,0.0189208984375,-0.0145263671875,-0.01068115234375,0.0164794921875,0.0162353515625,-0.000637054443359375,0.016357421875,0.00067901611328125,0.006439208984375,-0.007659912109375,0.013671875,-0.00124359130859375,0.002105712890625,-0.01214599609375,-0.0185546875,0.009765625,0.04345703125,0.033203125,-0.018798828125,-0.00390625,0.01397705078125,0.01544189453125,-0.019287109375,0.0074462890625,-0.019287109375,0.0072021484375,-0.043212890625,0.01611328125,-0.011962890625,0.0228271484375,0.0021209716796875,-0.0244140625,-0.01953125,-0.0218505859375,0.00116729736328125,0.010498046875,0.0291748046875,0.0294189453125,0.00848388671875,-0.00994873046875,0.000537872314453125,-0.000873565673828125,-0.0341796875,-0.0294189453125,0.004150390625,-0.07275390625,-0.008544921875,-0.009765625,-0.035400390625,-0.004791259765625,0.0101318359375,0.018310546875,0.000759124755859375,0.00927734375,-0.0118408203125,-0.023193359375,0.008056640625,-0.0206298828125,-0.03564453125,-0.00628662109375,-0.01165771484375,0.016845703125,-0.0216064453125,0.01324462890625,0.005096435546875,0.008056640625,0.00811767578125,0.016357421875,0.01165771484375,0.060546875,-0.05224609375,-0.0458984375,-0.016845703125,-0.0169677734375,-0.01220703125,-0.0225830078125,0.00848388671875,0.03466796875,0.0299072265625,-0.00030517578125,0.0155029296875,-0.0098876953125,0.022705078125,0.013671875,-0.0126953125,-0.000396728515625,0.0216064453125,-0.0078125,0.010498046875,0.0211181640625,0.0205078125,-0.045654296875,0.00775146484375,0.012939453125,0.0185546875,0.038330078125,-0.0201416015625,-0.033935546875,0.032470703125,-0.00360107421875,0.021240234375,-0.0189208984375,0.0020599365234375,0.025390625,0.0002422332763671875,-0.04541015625,0.033203125,0.045166015625,0.0206298828125,0.005706787109375,-0.0096435546875,-0.00555419921875,-0.0262451171875,0.00150299072265625,0.010986328125,-0.029541015625,-0.01507568359375,0.03515625,-0.024658203125,0.0257568359375,-0.006195068359375,0.022705078125,-0.0284423828125,0.0111083984375,0.0029296875,-0.00457763671875,-0.022216796875,-0.02587890625,0.004486083984375,0.002166748046875,-0.0284423828125,-0.02099609375,-0.049560546875,0.018798828125,-0.03369140625,-0.01031494140625,-0.01348876953125,-0.00970458984375,0.004638671875,-0.006103515625,0.00244140625,-0.003997802734375,-0.03662109375,0.0517578125,-0.0230712890625,-0.048095703125,-0.0020751953125,0.001129150390625,0.003265380859375,0.02392578125,0.006011962890625,0.005157470703125,0.028076171875,-0.0014190673828125,0.004241943359375,0.007568359375,-0.02392578125,-0.00469970703125,0.046630859375,-0.004241943359375,-0.00762939453125,0.01214599609375,0.0068359375,0.0167236328125,-0.031982421875,0.0242919921875,-0.00982666015625,-0.01806640625,-0.029541015625,-0.011962890625,0.023681640625,-0.00946044921875,0.0140380859375,-0.0279541015625,0.01220703125,-0.037841796875,-0.00848388671875,0.0078125,0.012939453125,0.0257568359375,-0.0302734375,0.0108642578125,0.02880859375,-0.025390625,-0.01953125,-0.0057373046875,0.0250244140625,0.0048828125,-0.004302978515625,0.00616455078125,-0.01348876953125,-0.01220703125,0.0016632080078125,0.01397705078125,0.01202392578125,0.02978515625,0.021484375,-0.00689697265625,0.031005859375,0.00701904296875,0.033203125,0.002532958984375,-0.0185546875,0.03369140625,-0.0068359375,-0.007171630859375,0.00506591796875,-0.0194091796875,-0.006744384765625,0.00616455078125,0.01348876953125,-0.00592041015625,-0.00946044921875,-0.0213623046875,-0.00518798828125,-0.0201416015625,-0.035888671875,-0.000396728515625,0.0021209716796875,0.0247802734375,0.01953125,-0.014892578125,0.020263671875,-0.0048828125,0.0625,0.0137939453125,0.004913330078125,-0.0020294189453125,-0.02734375,-0.0244140625,-0.0361328125,0.02294921875,0.020263671875,0.0133056640625,0.00958251953125,0.003326416015625,0.06396484375,0.032958984375,0.020263671875,0.0166015625,0.001312255859375,-0.04541015625,0.00958251953125,-0.017333984375,0.0234375,-0.05322265625,0.00506591796875,0.038818359375,-0.031494140625,-0.014404296875,-0.0019073486328125,-0.0390625,0.04541015625,0.0137939453125,-0.00823974609375,0.01092529296875,-0.03466796875,0.029541015625,0.039306640625,-0.0205078125,-0.0308837890625,0.0022125244140625,0.00830078125,-0.00160980224609375,0.00262451171875,-0.032470703125,-0.0262451171875,-0.025390625,-0.01458740234375,0.0101318359375,-0.02587890625,-0.0224609375,0.0206298828125,0.039794921875,-0.03759765625,0.0458984375,0.032470703125,0.01312255859375,-0.0003681182861328125,0.023681640625,0.01251220703125,0.0228271484375,-0.0220947265625,0.009033203125,0.0224609375,-0.01239013671875,0.007080078125,-0.00811767578125,0.0283203125,0.0078125,-0.0299072265625,0.0159912109375,-0.0079345703125,-0.01214599609375,-0.009521484375,0.01214599609375,0.048095703125,0.0306396484375,0.0084228515625,0.0244140625,-0.0152587890625,-0.010009765625,0.020751953125,-0.0279541015625,-0.0181884765625,0.0301513671875,0.00738525390625,0.02685546875,0.041259765625,-0.00141143798828125,0.0419921875,0.03271484375,-0.01031494140625,0.0033111572265625,0.061279296875,-0.0284423828125,-0.0152587890625,0.0185546875,0.036865234375,-0.043212890625,0.001434326171875,0.00174713134765625,0.0002536773681640625,-0.0098876953125,-0.0220947265625,-0.01251220703125,0.0203857421875,0.009033203125,-0.03662109375,-0.003509521484375,0.00946044921875,-0.01275634765625,-0.04931640625,0.01708984375,-0.04638671875,-0.01458740234375,0.00830078125,0.027587890625,-0.0184326171875,-0.01953125,-0.02880859375,0.0029296875,0.01422119140625,0.0059814453125,0.005126953125,0.0106201171875,-0.0272216796875,-0.035888671875,0.023193359375,-0.0235595703125,0.02978515625,0.0123291015625,-0.007354736328125,-0.0089111328125,0.018798828125,0.017333984375,0.01043701171875,0.0283203125,-0.013671875,0.017333984375,0.003173828125,-0.001953125,0.0004425048828125,0.0028076171875,-0.0673828125,-0.00188446044921875,0.03466796875,-0.0020294189453125,0.000568389892578125,0.0216064453125,0.018798828125,-0.005157470703125,0.004058837890625,-0.0030059814453125,-0.00189208984375,-0.0206298828125,0.0130615234375,-0.01287841796875,-0.00958251953125,0.007354736328125,-0.0108642578125,0.0048828125,0.01348876953125,0.00994873046875,0.0203857421875,0.03564453125,0.029541015625,0.0009307861328125,-0.0106201171875,-0.007110595703125,0.0118408203125,0.036865234375,0.022705078125,0.01806640625,0.06640625,-0.00160980224609375,0.018798828125,0.001953125,0.02734375,-0.00982666015625,-0.018798828125,-0.032958984375,0.0302734375,0.00616455078125,-0.0145263671875,-0.0181884765625,-0.0140380859375,0.00982666015625,0.002288818359375,0.0206298828125,0.0218505859375,-0.008056640625,-0.000720977783203125,0.036865234375,0.004791259765625,-0.0361328125,0.01324462890625,0.000713348388671875,0.0045166015625,-0.0103759765625,0.017333984375,-0.01068115234375,0.007659912109375,-0.0030975341796875,-0.015625,-0.01312255859375,-0.0208740234375,-0.0133056640625,-0.0177001953125,-0.0096435546875,-0.00592041015625,0.009033203125,0.01324462890625,-0.0172119140625,-0.01141357421875,0.009765625,-0.00494384765625,0.01177978515625,0.0174560546875,-0.017822265625,-0.0361328125,-0.0147705078125,0.0206298828125,-0.0299072265625,-0.027587890625,-0.0185546875,-0.0172119140625,0.00665283203125,-0.014404296875,0.028076171875,0.00836181640625,0.051025390625,0.0203857421875,-0.01397705078125,0.007568359375,-0.0262451171875,-0.0234375,-0.0101318359375,0.0032806396484375,-0.0206298828125,0.052490234375,-0.00286865234375,-0.0004596710205078125,-0.0108642578125,0.005157470703125,-0.00016498565673828125,0.0224609375,0.00665283203125,0.039794921875,0.00006961822509765625,0.001861572265625,0.017333984375,0.006622314453125,-0.032470703125,0.031982421875,0.0118408203125,-0.028076171875,-0.00165557861328125,0.12353515625,-0.00262451171875,0.040283203125,-0.0140380859375,-0.01312255859375,0.0177001953125,0.0081787109375,0.00927734375,0.005157470703125,0.04345703125,-0.01068115234375,0.05224609375,0.0101318359375,-0.0286865234375,0.0419921875,-0.0004253387451171875,-0.00274658203125,-0.00135040283203125,0.0225830078125,-0.043212890625,0.048828125,0.01177978515625,-0.02978515625,-0.01507568359375,0.051513671875,0.01214599609375,0.0159912109375,0.0068359375,-0.025390625,0.0036773681640625,0.009033203125,0.0064697265625,-0.0021209716796875,0.00147247314453125,-0.048828125,-0.0174560546875,-0.045654296875,0.0140380859375,-0.00049591064453125,0.01214599609375,0.0419921875,0.00213623046875,-0.01470947265625,0.01513671875,-0.0185546875,-0.031005859375,-0.00616455078125,-0.0115966796875,-0.00634765625,0.0181884765625,0.00958251953125,-0.0042724609375,0.004058837890625,0.048583984375,-0.0186767578125,0.00109100341796875,0.0057373046875,-0.003173828125,-0.0004024505615234375,-0.0172119140625,-0.006195068359375,0.0247802734375,0.01708984375,-0.00445556640625,0.005523681640625,0.0145263671875,-0.004241943359375,-0.0032196044921875,0.01507568359375,-0.02197265625,0.01611328125,0.0169677734375,0.003143310546875,-0.01470947265625,-0.0107421875,0.0064697265625,0.01507568359375,-0.00482177734375,0.00046539306640625,-0.026123046875,-0.00457763671875,0.009033203125,0.005340576171875,-0.025146484375,-0.001220703125,0.036865234375,-0.0390625,-0.017822265625,-0.031494140625,0.014404296875,-0.0177001953125,-0.0020294189453125,0.02587890625,0.01202392578125,0.0140380859375,-0.0004825592041015625,0.024658203125,-0.0166015625,-0.01531982421875,-0.0078125,0.01275634765625,0.006195068359375,-0.0101318359375,-0.0177001953125,-0.0098876953125,-0.00726318359375,0.0576171875,0.01953125,0.01116943359375,-0.00433349609375,0.01361083984375,0.005462646484375,0.01104736328125,0.005523681640625,0.009033203125,0.0244140625,0.01141357421875,-0.02587890625,-0.000934600830078125,0.0020751953125,0.00872802734375,-0.005828857421875,0.017333984375,0.0235595703125,-0.0108642578125,-0.00634765625,0.01214599609375,-0.01531982421875,-0.0361328125,-0.00177764892578125,0.010986328125,0.0091552734375,-0.0294189453125,-0.0244140625,0.00213623046875,-0.0184326171875,0.01361083984375,0.0673828125,0.01434326171875,0.0162353515625,-0.0169677734375,0.0172119140625,0.0255126953125,0.0186767578125,-0.00115966796875,-0.026123046875,0.0196533203125,-0.000957489013671875,-0.0172119140625,0.0216064453125,0.01495361328125,-0.0093994140625,-0.01507568359375,0.016845703125,-0.035888671875,0.01611328125,0.0021209716796875,-0.005340576171875,0.00055694580078125,-0.0021209716796875,-0.0286865234375,-0.0015869140625,-0.0101318359375,0.0196533203125,-0.017333984375,0.01611328125,0.0240478515625,0.05078125,0.01153564453125,-0.026123046875,-0.0029449462890625,0.00823974609375,-0.019287109375,0.01141357421875,-0.00762939453125,0.00921630859375,-0.0439453125,0.0103759765625,-0.02734375,0.0206298828125,0.02392578125,0.0225830078125,0.0247802734375,-0.01171875,-0.0228271484375,-0.01202392578125,0.004241943359375,-0.004791259765625,-0.0234375,0.0262451171875,0.006439208984375,-0.0033111572265625,0.0036773681640625,-0.042724609375,-0.000553131103515625,-0.009521484375,-0.01470947265625,0.01422119140625,-0.007720947265625,-0.006072998046875,0.0086669921875,-0.01495361328125,-0.00634765625,0.006744384765625,0.0107421875,-0.00115203857421875,0.00183868408203125,-0.0361328125,-0.0247802734375,0.006561279296875,-0.0299072265625,0.01239013671875,0.00927734375,0.0147705078125,-0.0177001953125,0.023193359375,-0.0361328125,0.00592041015625,-0.0093994140625,-0.003143310546875,-0.0233154296875,-0.032958984375,0.01531982421875,0.00150299072265625,-0.00701904296875,-0.0279541015625,-0.01202392578125,0.0294189453125,0.05322265625,0.01806640625,-0.024658203125,0.00848388671875,0.01068115234375,-0.0089111328125,0.0308837890625,-0.044189453125,0.01104736328125,-0.00531005859375,0.0390625,-0.00286865234375,0.016357421875,0.01507568359375,-0.051513671875,0.000087738037109375,-0.049560546875,-0.01251220703125,0.0439453125,0.0235595703125,0.0030364990234375,0.005279541015625,0.0010528564453125,0.01068115234375,0.033447265625,-0.025146484375,0.040283203125,0.01019287109375,0.002655029296875,-0.0018157958984375,-0.007293701171875,0.0277099609375,-0.0439453125,0.0023956298828125,0.028076171875,0.021728515625,0.0184326171875,-0.01080322265625,-0.0294189453125,0.000885009765625,-0.048828125,0.004058837890625,-0.007720947265625,-0.0115966796875,0.01348876953125,-0.00341796875,-0.01385498046875,0.003173828125,0.0233154296875,0.0159912109375,0.01361083984375,-0.003936767578125,0.01495361328125,0.01611328125,-0.0024871826171875,-0.00390625,-0.017822265625,-0.042724609375,-0.05810546875,-0.0361328125,-0.02685546875,0.028076171875,0.007720947265625,0.02978515625,0.00299072265625,0.00531005859375,0.01385498046875,0.0028839111328125,0.0174560546875,0.0140380859375,-0.00107574462890625,0.0014495849609375,-0.01251220703125,0.02001953125,-0.0140380859375,0.007293701171875,0.0220947265625,0.0203857421875,0.041748046875,0.01397705078125,0.0167236328125,-0.00628662109375,-0.006744384765625,-0.0390625,-0.00872802734375,0.0032196044921875,-0.001007080078125,0.022216796875,-0.00958251953125,0.0140380859375,0.0213623046875,0.006439208984375,0.00156402587890625,-0.03173828125,-0.000232696533203125,0.01458740234375,0.015869140625,0.0023040771484375,0.0255126953125,0.0147705078125,-0.00958251953125,-0.006744384765625,0.00946044921875,0.033935546875,0.023193359375,-0.01324462890625,-0.006195068359375,0.0191650390625,0.00909423828125,0.0004329681396484375,0.034912109375,-0.00592041015625,0.045654296875,-0.008544921875,-0.007537841796875,0.0194091796875,0.033203125,0.0093994140625,-0.037109375,0.005157470703125,-0.016357421875,0.013427734375,0.02880859375,0.006072998046875,0.004058837890625,-0.01416015625,-0.029541015625,-0.004913330078125,0.00592041015625,0.01806640625,0.002532958984375,-0.031494140625,-0.0020904541015625,0.0286865234375,-0.010498046875,-0.01324462890625,-0.002960205078125,-0.051513671875,-0.02587890625,0.00116729736328125,0.0030975341796875,0.035400390625,-0.005096435546875,0.057373046875,-0.0003795623779296875,0.023193359375,-0.000019550323486328125,-0.01348876953125,-0.036865234375,0.00067901611328125,-0.052978515625,0.0068359375,-0.002410888671875,-0.0020294189453125,0.0191650390625,-0.00396728515625,0.044921875,-0.0308837890625,-0.00183868408203125,-0.0126953125,-0.004364013671875,-0.009765625,0.003509521484375,0.0004825592041015625,0.005157470703125,0.01495361328125,0.005218505859375,0.046630859375,0.00164031982421875,-0.005523681640625,0.0233154296875,-0.006378173828125,0.03369140625,0.005096435546875,0.01513671875,0.006805419921875,-0.01104736328125,-0.01007080078125,0.0191650390625,0.0103759765625,-0.01495361328125,0.013671875,-0.0625,0.00007343292236328125,0.028076171875,0.027587890625,-0.0228271484375,0.009765625,-0.00335693359375,-0.009765625,0.057373046875,-0.00140380859375,-0.07666015625,0.021240234375,0.0198974609375,0.0038299560546875,-0.0037841796875,0.068359375,-0.02734375,-0.02978515625,-0.055908203125,0.0072021484375,0.003631591796875,-0.00421142578125,-0.0147705078125,-0.01092529296875,0.01123046875,-0.028076171875,-0.005706787109375,0.0164794921875,-0.0014801025390625,0.005859375,0.00592041015625,0.00775146484375,0.020263671875,0.0322265625,0.0147705078125,-0.032958984375,-0.0091552734375,0.006988525390625,-0.00159454345703125,0.00109100341796875,0.0030975341796875,0.01220703125],"index":0,"object":"embedding"}],"model":"doubao-embedding-vision-251215","object":"list","usage":{"prompt_tokens":23,"total_tokens":23}}`
