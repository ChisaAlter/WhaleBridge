package gateway

import (
	"bytes"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

func TestWhaleBridgeClaudePrivateEndpoint(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer native-claude-login" || r.Header.Get("x-api-key") != "vendor-key" {
			t.Errorf("vendor received client sign-in or no vendor credential: %v", r.Header)
		}
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/count_tokens") {
			io.WriteString(w, `{"input_tokens":23}`)
			return
		}
		io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"Claude pong"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "bridge", Key: "vendor-key", Anthropic: up.URL, Models: []string{"claude-sonnet-4-5"}, AuthHeader: provider.AuthXAPIKey}); err != nil {
		t.Fatal(err)
	}
	if got := WhaleBridgeAnthropicURL(); got != URL()+"/_whalebridge/test-private-gateway-key/anthropic" {
		t.Fatalf("private Claude address = %q", got)
	}
	h := New().Handler()
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/_whalebridge/test-private-gateway-key/anthropic/v1/messages", 200},
		{"POST", "/_whalebridge/test-private-gateway-key/anthropic/messages", 200},
		{"POST", "/_whalebridge/test-private-gateway-key/anthropic/v1/messages/count_tokens", 200},
		{"POST", "/v1/messages", 401},
		{"POST", "/_whalebridge/wrong/anthropic/v1/messages", 401},
		{"POST", "/_whalebridge/test-private-gateway-key/anthropic/v1/responses", 404},
		{"POST", "/_whalebridge/test-private-gateway-key/anthropic/alpha/search", 404},
		{"GET", "/_whalebridge/test-private-gateway-key/anthropic/v1/models", 404},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"model":"bridge/claude-sonnet-4-5","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`))
		r.Header.Set("Authorization", "Bearer native-claude-login")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s %s: %d, want %d: %s", tc.method, tc.path, w.Code, tc.want, w.Body)
		}
		if tc.want == 200 && !strings.Contains(w.Body.String(), map[bool]string{true: "input_tokens", false: "Claude pong"}[strings.HasSuffix(tc.path, "count_tokens")]) {
			t.Errorf("not served: %s", w.Body)
		}
	}
}

func TestWhaleBridgeNewRoutesKeepPrivateAdmission(t *testing.T) {
	fresh(t)
	h := New().Handler()
	for _, path := range []string{AlphaSearchPath, "/v1" + AlphaSearchPath, CodexPath + AlphaSearchPath} {
		for _, key := range []string{"", "test-private-gateway-key"} {
			r := httptest.NewRequest("POST", path, strings.NewReader(`{"model":"missing/model","commands":{}}`))
			if key != "" {
				r.Header.Set("Authorization", "Bearer "+key)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 401
			if key != "" {
				want = 200
			}
			if w.Code != want {
				t.Errorf("%s key=%v: %d want %d: %s", path, key != "", w.Code, want, w.Body)
			}
		}
	}
	for _, path := range []string{CodexCatalogPath, "/v1/magpie/usage", "/v1/magpie/usage/requests"} {
		for _, key := range []string{"", "test-private-gateway-key"} {
			r := httptest.NewRequest("GET", path, nil)
			r.RemoteAddr = "127.0.0.1:12345"
			if key != "" {
				r.Header.Set("Authorization", "Bearer "+key)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 401
			if key != "" {
				want = 200
			}
			if w.Code != want {
				t.Errorf("GET %s key=%v: %d want %d: %s", path, key != "", w.Code, want, w.Body)
			}
		}
	}
	r := httptest.NewRequest("POST", "/v1/magpie/usage/requests", strings.NewReader(`{"items":[]}`))
	r.Header.Set("Authorization", "Bearer test-private-gateway-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("private usage ingest exposed: %d %s", w.Code, w.Body)
	}
}

func TestWhaleBridgeCodexNativeSearchPreservesAuth(t *testing.T) {
	fresh(t)
	called := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		b, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer native-codex-login" || r.URL.Path != CodexPath+AlphaSearchPath || !strings.Contains(string(b), "native query") {
			t.Errorf("native search lost auth/path/body: %q %s %s", r.Header.Get("Authorization"), r.URL.Path, b)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"encrypted_output":null,"output":"native results"}`)
	}))
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + CodexPath
	t.Cleanup(func() { provider.CodexBase = old })
	r := httptest.NewRequest("POST", "/_whalebridge/test-private-gateway-key"+CodexPath+AlphaSearchPath, strings.NewReader(`{"model":"gpt-6-astra","commands":{"search_query":[{"q":"native query"}]}}`))
	r.Header.Set("Authorization", "Bearer native-codex-login")
	r.Header.Set("chatgpt-account-id", "fixture-account")
	w := httptest.NewRecorder()
	New().Handler().ServeHTTP(w, r)
	if w.Code != 200 || !called || !strings.Contains(w.Body.String(), "native results") {
		t.Fatalf("native private search: called=%v %d %s", called, w.Code, w.Body)
	}
}

func TestNoListVideoCacheKeepsExplicitKind(t *testing.T) {
	fresh(t)
	p := provider.Provider{ID: "video-cache", Name: "Videos", Chat: "http://127.0.0.1:9/v1", Key: "fixture-key", Models: []string{"manual"}}
	if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{{ID: "old-paid-video", Films: true}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(catalog.LivePath(p.ID))
	if err != nil {
		t.Fatal(err)
	}
	if ms := customVideomakers(p); len(ms) != 1 || ms[0].ID != "old-paid-video" {
		t.Fatalf("ordinary cache: %+v", ms)
	}
	p.Preset = "baidu-qianfan" // the Token Plan lists nothing, unlike its former PAYG endpoint
	if p.ListsModels() {
		t.Fatal("fixture plan still lists models")
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if ms := customVideomakers(p); len(ms) != 0 {
		t.Fatalf("NoList used a former endpoint's videos: %+v", ms)
	}
	if err := provider.SetModelKind(p.ID+"/manual", provider.KindVideo); err != nil {
		t.Fatal(err)
	}
	if ms := customVideomakers(p); len(ms) != 1 || ms[0].ID != "manual" {
		t.Fatalf("explicit video was lost: %+v", ms)
	}
	after, err := os.ReadFile(catalog.LivePath(p.ID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("old cache was rewritten: %v", err)
	}
}

func TestEmbeddingsKeyPoolAndPerKeyModels(t *testing.T) {
	for _, narrowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "quota-failover", true: "key-models"}[narrowed], func(t *testing.T) {
			fresh(t)
			forgetSinks(t)
			var seen []string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				seen = append(seen, key)
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				if key == "first" {
					w.WriteHeader(429)
					io.WriteString(w, `{"error":{"message":"key quota exhausted"}}`)
					return
				}
				io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
			}))
			t.Cleanup(up.Close)
			p := provider.Provider{ID: "vectors", Chat: up.URL + "/v1", Key: "first", Keys: []provider.KeyAccount{{Name: "spare", Key: "second"}}, Models: []string{"embed", "chat"}}
			if narrowed {
				p.AccountModels = map[string][]string{provider.KeyID("first"): {"chat"}}
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{"model":"vectors/embed","input":"hello"}`))
			r.Header.Set("Authorization", "Bearer test-private-gateway-key")
			w := httptest.NewRecorder()
			New().Handler().ServeHTTP(w, r)
			want := []string{"first", "second"}
			if narrowed {
				want = []string{"second"}
			}
			if w.Code != 200 || !slices.Equal(seen, want) || !strings.Contains(w.Body.String(), "embedding") {
				t.Fatalf("status %d, tried %v want %v: %s", w.Code, seen, want, w.Body)
			}
		})
	}
}

func TestCursorToolImagesFollowTheirResults(t *testing.T) {
	r := &Request{Tools: []Tool{{Name: "Read", Schema: json.RawMessage(`{"type":"object"}`)}}, Messages: []Message{
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "Read", Args: json.RawMessage(`{}`)}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c1", Text: "screenshot", Images: []Part{{Kind: Image, MediaType: "image/png", Data: "AAE="}}}, {Kind: Text, Text: "and this"}}},
	}}
	var got []string
	for _, m := range cursorMessages(r, bridgeTools(r)) {
		got = append(got, string(m))
	}
	if len(got) != 4 || !strings.Contains(got[2], "it follows in the next message") || strings.Contains(got[2], "Uint8Array") || !strings.Contains(got[3], `"hex":"0001"`) || !strings.Contains(got[3], "Read (tool call c1)") || strings.Index(got[3], "0001") > strings.Index(got[3], "and this") {
		t.Fatalf("tool image placement: %s", strings.Join(got, "\n"))
	}
}

func TestPauseByNativeFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a&b <c> 中文")
	s, _, _ := ruled(t, provider.Rule{Use: "a/small", Pause: true, ExceptFolders: []string{dir}})
	for _, tc := range []struct{ folder, want string }{{dir, "from ka"}, {dir + "-other", "from kb"}, {"", "from kb"}} {
		body := chat("<environment_context><cwd>"+html.EscapeString(tc.folder)+"</cwd></environment_context>", nil, 0, "")
		if code, out := postAs(t, s, tc.folder, body); code != 200 || !strings.Contains(out, tc.want) {
			t.Fatalf("folder %q: %d %s, want %s", tc.folder, code, out, tc.want)
		}
	}
}
