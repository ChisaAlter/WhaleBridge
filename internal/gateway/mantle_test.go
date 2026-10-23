package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

type mantleCall struct {
	path string
	head http.Header
	body map[string]any
}
type mantleFixture struct {
	mu    sync.Mutex
	calls []mantleCall
}

func (m *mantleFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	model, _ := body["model"].(string)
	m.mu.Lock()
	m.calls = append(m.calls, mantleCall{r.URL.Path, r.Header.Clone(), body})
	m.mu.Unlock()
	if r.URL.Path == "/anthropic/v1/messages" {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg1","model":"`+model+`","usage":{"input_tokens":5}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from claude"}}`,
			`data: {"type":"content_block_stop","index":0}`, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`, `data: {"type":"message_stop"}`))
		return
	}
	rest, openai := strings.CutPrefix(r.URL.Path, "/openai/v1/")
	if !openai {
		var ok bool
		rest, ok = strings.CutPrefix(r.URL.Path, "/v1/")
		if !ok {
			http.NotFound(w, r)
			return
		}
	}
	if model == "unavailable.server" {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"error":{"message":"model isn't supported on this route"}}`)
		return
	}
	if model == "other.failure" && openai {
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"error":{"message":"not authorized"}}`)
		return
	}
	wantsOpenAI := model == "openai.gpt-5.5" || model == "xai.grok-4.3" || model == "newmaker.model" || model == "other.failure"
	if model == "ghost.model" || wantsOpenAI != openai {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"message":"model `+model+` isn't supported on this route at `+r.URL.Path+`"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	switch rest {
	case "chat/completions":
		_, _ = io.WriteString(w, sse(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from chat"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`, `data: [DONE]`))
	case "responses":
		_, _ = io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"r1","status":"in_progress"}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[]}}`,
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"m1","delta":"from responses"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"from responses"}]}}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":5,"output_tokens":2}}}`))
	default:
		http.NotFound(w, r)
	}
}

func (m *mantleFixture) since(n int) []mantleCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls[n:])
}
func mantlePaths(calls []mantleCall) []string {
	var paths []string
	for _, c := range calls {
		paths = append(paths, c.path)
	}
	return paths
}

// Both cold requests reach the guessed route before either refusal returns.
// A learns the alternate route first; B must still retry its own other route,
// and B's two refusals must not revoke A's demonstrated route.
func TestMantleConcurrentInFlightRoutes(t *testing.T) {
	for _, secondRefuses := range []bool{false, true} {
		for _, noResponseRequest := range []bool{false, true} {
			name := "both-succeed"
			if secondRefuses {
				name = "second-refuses"
			}
			if noResponseRequest {
				name += "/response-without-request"
			}
			t.Run(name, func(t *testing.T) {
				fresh(t)
				entered := make(chan string, 2)
				releaseA, releaseB := make(chan struct{}), make(chan struct{})
				letA := sync.OnceFunc(func() { close(releaseA) })
				letB := sync.OnceFunc(func() { close(releaseB) })
				var mu sync.Mutex
				paths := map[string][]string{}
				fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					id, _ := body["fixture_id"].(string)
					mu.Lock()
					paths[id] = append(paths[id], r.URL.Path)
					mu.Unlock()
					if r.URL.Path == "/v1/chat/completions" {
						entered <- id
						if id == "A" {
							<-releaseA
						} else {
							<-releaseB
						}
					}
					if r.URL.Path == "/v1/chat/completions" || id == "B" && secondRefuses {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":{"message":"model isn't supported on this route at `+r.URL.Path+`"}}`)
						return
					}
					_, _ = io.WriteString(w, `{"ok":true}`)
				}))
				t.Cleanup(fake.Close)
				t.Cleanup(letA)
				t.Cleanup(letB)
				p := provider.Provider{ID: "mantle-concurrent", Preset: "bedrock-mantle", Key: "synthetic", Chat: fake.URL + "/v1"}
				s := New()
				if noResponseRequest {
					s.client.Transport = mantleMissingRequestTransport{base: s.client.Transport}
				}
				type result struct {
					code int
					body string
					err  error
				}
				ask := func(id string) result {
					res, err := s.forward(context.Background(), p, provider.Chat, "/chat/completions", []byte(`{"model":"newmaker.concurrent","fixture_id":"`+id+`"}`), nil)
					if err != nil {
						return result{err: err}
					}
					defer res.Body.Close()
					b, err := io.ReadAll(res.Body)
					return result{res.StatusCode, string(b), err}
				}
				wait := func(ch <-chan result) result {
					t.Helper()
					select {
					case got := <-ch:
						return got
					case <-time.After(5 * time.Second):
						t.Fatal("Mantle forward did not finish")
						return result{}
					}
				}
				a, b := make(chan result, 1), make(chan result, 1)
				go func() { a <- ask("A") }()
				go func() { b <- ask("B") }()
				seen := map[string]bool{}
				for range 2 {
					select {
					case id := <-entered:
						seen[id] = true
					case <-time.After(5 * time.Second):
						t.Fatal("both requests did not reach the cold guessed route")
					}
				}
				if !seen["A"] || !seen["B"] {
					t.Fatal("cold route barrier did not observe A and B")
				}
				letA()
				if got := wait(a); got.err != nil || got.code != 200 || s.fits(p.ID, mantleRouteKey("newmaker.concurrent"), provider.Chat) {
					t.Fatalf("A did not learn route: %+v", got)
				}
				letB()
				got := wait(b)
				want := 200
				if secondRefuses {
					want = 400
				}
				if got.err != nil || got.code != want {
					t.Fatalf("B did not finish on its own alternate: %+v", got)
				}
				if secondRefuses && !strings.Contains(got.body, "at /v1/chat/completions") {
					t.Fatalf("B lost first refusal: %s", got.body)
				}
				if s.fits(p.ID, mantleRouteKey("newmaker.concurrent"), provider.Chat) {
					t.Fatal("B revoked A's demonstrated route")
				}
				if got := ask("C"); got.err != nil || got.code != 200 {
					t.Fatalf("cached followup: %+v", got)
				}
				mu.Lock()
				defer mu.Unlock()
				for _, id := range []string{"A", "B"} {
					if !slices.Equal(paths[id], []string{"/v1/chat/completions", "/openai/v1/chat/completions"}) {
						t.Fatalf("%s sent %v", id, paths[id])
					}
				}
				if !slices.Equal(paths["C"], []string{"/openai/v1/chat/completions"}) {
					t.Fatalf("cache followup sent %v", paths["C"])
				}
			})
		}
	}
}

// A custom doer may omit Request on its response; retain the actual base
// recorded by this forward instead of relying on a global route guess.
type mantleMissingRequestTransport struct{ base http.RoundTripper }

func (t mantleMissingRequestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	res, err := base.RoundTrip(r)
	if res != nil {
		res.Request = nil
	}
	return res, err
}

func TestMantleRoutesThroughPrivateGateway(t *testing.T) {
	fresh(t)
	up := &mantleFixture{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p, err := provider.FromPreset("bedrock-mantle")
	if err != nil {
		t.Fatal(err)
	}
	p.Key, p.Chat, p.Responses, p.Anthropic = "synthetic-bedrock", srv.URL+"/v1", srv.URL+"/v1", srv.URL+"/anthropic"
	p.Models = []string{"anthropic.claude-opus-4-7", "openai.gpt-oss-120b", "openai.gpt-5.5", "xai.grok-4.3", "newmaker.model", "ghost.model", "unavailable.server", "other.failure"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	h := s.Handler()
	ask := func(path, body string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer test-private-gateway-key")
		r.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	chat := func(model string) string {
		return `{"model":"bedrock-mantle/` + model + `","max_tokens":20,"stream":true,"service_tier":"priority","messages":[{"role":"user","content":"hi"}]}`
	}
	for _, tc := range []struct{ path, body, want, text string }{
		{"/v1/chat/completions", chat("openai.gpt-oss-120b"), "/v1/chat/completions", "from chat"},
		{"/v1/responses", `{"model":"bedrock-mantle/openai.gpt-5.5","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, "/openai/v1/responses", "from responses"},
		{"/v1/messages", `{"model":"bedrock-mantle/anthropic.claude-opus-4-7","stream":true,"max_tokens":20,"metadata":{"user_id":"private-device"},"messages":[{"role":"user","content":"hi"}]}`, "/anthropic/v1/messages", "from claude"},
	} {
		n := len(up.since(0))
		code, body := ask(tc.path, tc.body)
		calls := up.since(n)
		if code != 200 || !strings.Contains(body, tc.text) || !slices.Equal(mantlePaths(calls), []string{tc.want}) {
			t.Fatalf("route %s: %d %s paths=%v", tc.want, code, body, mantlePaths(calls))
		}
		c := calls[0]
		if tc.path == "/v1/messages" {
			if c.head.Get("x-api-key") != p.Key || c.head.Get("Authorization") != "" || c.body["metadata"] != nil {
				t.Fatalf("Messages headers/body %v %v", c.head, c.body)
			}
		} else if c.head.Get("Authorization") != "Bearer "+p.Key {
			t.Fatalf("upstream key %v", c.head)
		}
		if tc.path == "/v1/chat/completions" && (c.body["max_completion_tokens"] != float64(20) || c.body["max_tokens"] != nil) {
			t.Fatalf("Bedrock Chat fields %v", c.body)
		}
	}
	for i := range 2 {
		n := len(up.since(0))
		code, body := ask("/v1/chat/completions", chat("newmaker.model"))
		paths := mantlePaths(up.since(n))
		want := []string{"/v1/chat/completions", "/openai/v1/chat/completions"}
		if i > 0 {
			want = want[1:]
		}
		if code != 200 || !strings.Contains(body, "from chat") || !slices.Equal(paths, want) {
			t.Fatalf("unknown prefix %d: %d %s %v", i, code, body, paths)
		}
	}
	// Direct forward isolates Mantle's two-prefix retry from the gateway's
	// unrelated error fallbacks: at most two sends, first error preserved.
	for range 2 {
		n := len(up.since(0))
		res, err := s.forward(context.Background(), p, provider.Chat, "/chat/completions", []byte(`{"model":"ghost.model","messages":[]}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 400 || !strings.Contains(string(b), "at /v1/chat/completions") || !slices.Equal(mantlePaths(up.since(n)), []string{"/v1/chat/completions", "/openai/v1/chat/completions"}) || !s.fits(p.ID, mantleRouteKey("ghost.model"), provider.Chat) {
			t.Fatalf("two wrong routes: %d %s %v", res.StatusCode, b, mantlePaths(up.since(n)))
		}
	}
	// A 5xx must not teach a route, even when its text resembles a 4xx.
	n := len(up.since(0))
	res, err := s.forward(context.Background(), p, provider.Chat, "/chat/completions", []byte(`{"model":"unavailable.server"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 500 || len(up.since(n)) != 1 || !s.fits(p.ID, mantleRouteKey("unavailable.server"), provider.Chat) {
		t.Fatal("a 5xx changed the Mantle route")
	}
	// If the alternate prefix recognizes the model but refuses authorization,
	// that response is preserved, and the recognized prefix remains learned.
	n = len(up.since(0))
	res, err = s.forward(context.Background(), p, provider.Chat, "/chat/completions", []byte(`{"model":"other.failure"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 403 || !strings.Contains(string(b), "not authorized") || len(up.since(n)) != 2 || s.fits(p.ID, mantleRouteKey("other.failure"), provider.Chat) {
		t.Fatal("alternate route's non-routing refusal was lost")
	}
	// Admission remains private: the new route does not bypass component auth.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chat("openai.gpt-oss-120b"))))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Mantle accepted missing private key: %d", w.Code)
	}
}
