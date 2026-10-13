package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/usage"
)

func TestWhaleBridgeProtocolEndpoints(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer vendor-key" {
			t.Errorf("upstream received gateway credentials: %q", r.Header.Get("Authorization"))
		}
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"}}]}\n\ndata: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "bridge", Name: "Bridge", Key: "vendor-key", Models: []string{"m"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	for _, tc := range []struct{ path, body string }{
		{"/v1/responses", `{"model":"bridge/m","input":"hello"}`},
		{"/v1/messages", `{"model":"bridge/m","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`},
		{"/v1beta/models/bridge/m:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, gw.URL+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer test-private-gateway-key")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 200 || !strings.Contains(string(b), "pong") {
				t.Fatalf("%d %s", res.StatusCode, b)
			}
		})
	}
}

func TestWhaleBridgeProviderEndpointsKeepPrivateAdmission(t *testing.T) {
	fresh(t)
	h := New().Handler()
	for _, path := range []string{"/v1/responses", "/v1/messages", "/v1/messages/count_tokens", "/v1/systemone", "/v1/images/generations", "/v1/images/edits", "/v1/embeddings", "/v1/rerank", "/v1/videos", "/v1beta/models/m:countTokens"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without private key: %d %s", path, rec.Code, rec.Body)
		}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer test-private-gateway-key")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Body.String() == "404 page not found\n" {
			t.Errorf("%s never reached provider handler", path)
		}
	}
	for _, path := range []string{"/mcp/test", "/api/hello", "/v1/magpie/limit", CodexPath + "/unmanaged"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer test-private-gateway-key")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("excluded client route %s exposed: %d", path, rec.Code)
		}
	}
	for _, path := range []string{"/v1/responses", "/v1/models", "/v1/models/bridge/m", "/v1beta/models", "/v1/videos/bad", "/v1/videos/bad/content"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test-private-gateway-key")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Body.String() == "404 page not found\n" {
			t.Errorf("%s never reached provider handler", path)
		}
	}
}

func TestWhaleBridgeClientCredentials(t *testing.T) {
	fresh(t)
	const key = "test-private-gateway-key"
	previous := usage.Agents
	usage.Agents = func() []usage.Known { return []usage.Known{{ID: "gemini"}, {ID: "codex"}} }
	t.Cleanup(func() { usage.Agents = previous })
	h := New().Handler()
	for _, tc := range []struct {
		name, header, value, query string
		want                       int
	}{
		{"bearer", "Authorization", "Bearer " + key, "", 200},
		{"anthropic", "x-api-key", key, "", 200},
		{"gemini", "x-goog-api-key", key, "", 200},
		{"query", "", "", "?key=" + key, 200},
		{"named", "x-goog-api-key", key + "-gemini", "", 200},
		{"unknown-agent", "x-api-key", key + "-invented", "", 401},
		{"upstream-public-token", "Authorization", "Bearer magpie", "", 401},
		{"missing", "", "", "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/v1/models"+tc.query, nil)
			if tc.header != "" {
				r.Header.Set(tc.header, tc.value)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body)
			}
		})
	}
}

func TestWhaleBridgeCodexPrivateEndpoint(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer vendor-key" {
			t.Errorf("upstream credential = %q", r.Header.Get("Authorization"))
		}
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"codex pong\"}}]}\n\ndata: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "bridge", Name: "Bridge", Key: "vendor-key", Models: []string{"m"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/_whalebridge/test-private-gateway-key" + CodexPath + "/responses", 200},
		{CodexPath + "/responses", 401},
		{"/_whalebridge/wrong" + CodexPath + "/responses", 401},
		{"/_whalebridge/test-private-gateway-key" + CodexPath + "/unmanaged", 404},
		{"/_whalebridge/test-private-gateway-key/v1/responses", 404},
	} {
		r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"model":"bridge/m","input":"hello","stream":true}`))
		r.Header.Set("Authorization", "Bearer original-native-login")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d, want %d: %s", tc.path, w.Code, tc.want, w.Body)
		}
		if tc.want == 200 && !strings.Contains(w.Body.String(), "codex pong") {
			t.Fatalf("Codex request did not reach the configured provider: %s", w.Body)
		}
	}
}
