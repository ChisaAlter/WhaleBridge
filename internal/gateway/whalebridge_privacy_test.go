package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/redact"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// Exercise the component's admitted Alpha search route, including each API's
// GET/POST query shape. Only the agent receives restored result text and URLs;
// the search API and any internal model round keep placeholders.
func TestWhaleBridgePrivacyAlphaSearch(t *testing.T) {
	const phone, email, term = "13812345678", "privacy.fixture@proton.me", "private-project-eight"
	query := "directory " + phone + " " + email + " " + term
	for _, vendor := range []string{"searxng", "tavily", "brave", "exa", "firecrawl", "you", "deepseek"} {
		t.Run(vendor, func(t *testing.T) {
			fresh(t)
			t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", "")
			if err := settings.Save(settings.Settings{Searcher: "off", RedactPersonal: true, RedactWords: []string{term}}); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var asked []string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query().Get("q")
				if r.Method == http.MethodPost {
					var in struct{ Query string }
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
						t.Error(err)
					}
					q = in.Query
				}
				mu.Lock()
				asked = append(asked, q)
				mu.Unlock()
				page := map[string]any{"title": "Directory " + q, "url": "https://example.test/" + q, "content": "Results for " + q, "description": "Results for " + q, "text": "Results for " + q}
				var out any = map[string]any{"results": []any{page}}
				switch vendor {
				case "brave":
					out = map[string]any{"web": map[string]any{"results": []any{page}}}
				case "firecrawl":
					out = map[string]any{"success": true, "data": map[string]any{"web": []any{page}}}
				case "you":
					out = map[string]any{"results": map[string]any{"web": []any{page}}}
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(out)
			}))
			t.Cleanup(api.Close)
			a := provider.SearchAPI{Vendor: vendor, URL: api.URL}
			if vendor != "searxng" {
				a.Key = "fixture-key"
			}
			if err := provider.SetSearchAPI(a); err != nil {
				t.Fatal(err)
			}
			s := New()
			h := s.Handler()
			body, _ := json.Marshal(map[string]any{"model": "missing/model", "commands": map[string]any{"search_query": []any{map[string]any{"q": query}}}})
			call := func(path, key string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
				r.RemoteAddr = "127.0.0.1:12345"
				if key != "" {
					r.Header.Set("Authorization", "Bearer "+key)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			if w := call("/v1"+AlphaSearchPath, ""); w.Code != 401 {
				t.Fatalf("private alpha route: %d %s", w.Code, w.Body)
			}
			if w := call(SearchMCPPath, "test-private-gateway-key"); w.Code != 404 {
				t.Fatalf("component exposed search MCP: %d %s", w.Code, w.Body)
			}
			w := call("/v1"+AlphaSearchPath, "test-private-gateway-key")
			var out alphaSearchReply
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
				t.Fatalf("alpha %d %s", w.Code, w.Body)
			}
			mu.Lock()
			sent := append([]string(nil), asked...)
			mu.Unlock()
			if len(sent) != 1 {
				t.Fatalf("API calls %q", sent)
			}
			for _, raw := range []string{phone, email, term} {
				if strings.Contains(sent[0], raw) {
					t.Fatalf("%s API leaked %q in %q", vendor, raw, sent[0])
				}
			}
			for _, marker := range []string{"{{PHONE_", "{{EMAIL_", "{{TERM_"} {
				if !strings.Contains(sent[0], marker) {
					t.Fatalf("API missing %s: %q", marker, sent)
				}
			}
			for _, want := range []string{"Directory " + query, "https://example.test/" + query, "Results for " + query} {
				if !strings.Contains(out.Output, want) {
					t.Fatalf("agent result missing %q: %s", want, out.Output)
				}
			}
			if strings.Contains(out.Output, "{{") {
				t.Fatalf("agent still received placeholders: %s", out.Output)
			}
			if vendor == "searxng" {
				// The existing MCP helper gets the same restoration without
				// admitting a new component route for it.
				r := httptest.NewRequest(http.MethodPost, SearchMCPPath, nil)
				said, err := s.searchMCPRun(r, query)
				if err != nil || !strings.Contains(said, "Results for "+query) || strings.Contains(said, "{{") {
					t.Fatalf("existing MCP helper: %s %v", said, err)
				}
			}
			// webSearch is also used to feed a model's tool round: those results
			// must not be restored before they go back to that model.
			said, hits, err := s.webSearch(context.Background(), query)
			if err != nil || len(hits) != 1 {
				t.Fatalf("internal search %v %v", hits, err)
			}
			if strings.Contains(said, phone) || strings.Contains(hits[0].Title, email) || strings.Contains(hits[0].URL, term) || !strings.Contains(said, "{{PHONE_") {
				t.Fatalf("internal search restored values: %s %+v", said, hits)
			}
		})
	}
}

// Kimi and WorkBuddy bypass the normal model path. Their own destination's
// local unmasked setting is respected; a remote address cannot get that grant.
func TestWhaleBridgePrivacyPlanSearch(t *testing.T) {
	fresh(t)
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", "")
	const query = "contact 13912345678 plan.fixture@proton.me"
	if err := settings.Save(settings.Settings{RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Query string `json:"query"`
			Text  string `json:"text_query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Error(err)
		}
		text := q.Query
		if r.URL.Path == "/coding/v1/search" {
			text = q.Text
		}
		mu.Lock()
		asked = append(asked, text)
		mu.Unlock()
		page := map[string]any{"title": "Found " + text, "url": "https://example.test/" + text, "snippet": "Echo " + text}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/coding/v1/search" {
			json.NewEncoder(w).Encode(map[string]any{"search_results": []any{page}})
		} else if r.URL.Path == wbSearchPath {
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "results": []any{page}})
		} else {
			t.Errorf("unexpected search path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	for _, kind := range []string{"kimi", "workbuddy"} {
		p := provider.Provider{ID: kind, Name: kind, Chat: up.URL + "/v1", Key: "fixture"}
		if kind == "kimi" {
			p.Preset = "kimi-code-cn"
			p.Chat = up.URL + "/coding/v1"
		}
		for _, unmasked := range []bool{false, true} {
			p.Unredacted = unmasked
			var said string
			var hits []Hit
			var err error
			if kind == "kimi" {
				said, hits, err = New().kimiSearch(context.Background(), p, query)
			} else {
				said, hits, err = New().wbSearch(context.Background(), p, query)
			}
			if err != nil || len(hits) != 1 {
				t.Fatalf("%s %v: %v %v", kind, unmasked, hits, err)
			}
			mu.Lock()
			sent := asked[len(asked)-1]
			mu.Unlock()
			if unmasked {
				if sent != query {
					t.Fatalf("local exception %s sent %q", kind, sent)
				}
			} else if strings.Contains(sent, "13912345678") || strings.Contains(sent, "plan.fixture@proton.me") || !strings.Contains(sent, "{{PHONE_") {
				t.Fatalf("%s sent %q", kind, sent)
			}
			if restored := redact.Restore(said, false); !strings.Contains(restored, query) {
				t.Fatalf("%s result lost original: %s", kind, restored)
			}
		}
	}
}

// The routing group sends its members the original user message, but its
// separate decision provider receives a masked state.message. The same real
// classifier path also preserves Jev's own local unmasked exception.
func TestWhaleBridgePrivacyJevForUnmaskedGroup(t *testing.T) {
	s, _, selected := ruled(t)
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", "")
	if err := settings.Save(settings.Settings{RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a/small", "b/big"} {
		p, _, ok := provider.Resolve(id)
		if !ok {
			t.Fatal(id)
		}
		p.Unredacted = true
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		if !p.SkipsRedaction() {
			t.Fatal("member must stay unmasked")
		}
	}
	var mu sync.Mutex
	var states []string
	j := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			State map[string]string `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Error(err)
		}
		mu.Lock()
		states = append(states, q.State["message"])
		mu.Unlock()
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("decision path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"jev-privacy","answers":{"intent":{"choice":"debugging","confidence":0.99},"effort":{"score":2}},"usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	t.Cleanup(j.Close)
	p := provider.Provider{ID: "privacy-jev", Name: "Privacy Jev", Decide: j.URL + "/v1", Key: "fixture", Models: []string{"jev-privacy"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	g, _, ok := provider.FindGroup("group/r")
	if !ok {
		t.Fatal("no group")
	}
	g.Classifier = p.ID + "/jev-privacy"
	g.Rules = []provider.Rule{{Use: "b/big", Intent: "debugging"}}
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	query := "mail decision.fixture@proton.me about parser crashes on 13712345678"
	out, _ := postOK(t, s, "privacy-masked", chat(query, nil, 0, ""))
	if !strings.Contains(out, "from kb") {
		t.Fatalf("decision did not route: %s", out)
	}
	mu.Lock()
	sent := states[len(states)-1]
	mu.Unlock()
	if strings.Contains(sent, "decision.fixture@proton.me") || strings.Contains(sent, "13712345678") || !strings.Contains(sent, "{{EMAIL_") {
		t.Fatalf("Jev received raw group message %q", sent)
	}
	selected.mu.Lock()
	memberBody := selected.last
	selected.mu.Unlock()
	if !strings.Contains(memberBody, query) {
		t.Fatalf("unmasked member lost original: %s", memberBody)
	}
	p.Unredacted = true
	if !p.SkipsRedaction() {
		t.Fatal("local Jev exception must apply")
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	query2 := "mail local-decision.fixture@proton.me about another parser failure on 13612345678"
	if v, err := s.askClassifier(p.ID+"/jev-privacy", []string{"debugging"}, before{}, true, query2); err != nil || v.Intent != "debugging" || v.Effort != "high" {
		t.Fatalf("classifier result %+v %v", v, err)
	}
	mu.Lock()
	sent = states[len(states)-1]
	mu.Unlock()
	if sent != query2 {
		t.Fatalf("Jev local exception sent %q", sent)
	}
	// An unmasked flag alone is not permission for a remote destination.
	p.Decide = "https://decision.fixture.invalid/v1"
	if sent, err := maskedQuery(query2, &p); err != nil || strings.Contains(sent, "local-decision.fixture@proton.me") || !strings.Contains(sent, "{{EMAIL_") {
		t.Fatalf("remote Jev exception leaked %q", sent)
	}
}

func TestWhaleBridgePrivacyOptionsAndExistingPlaceholders(t *testing.T) {
	fresh(t)
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", "")
	query := "reach 13512345678 privacy-options@proton.me project-custom-eight 12:34:56:78:9A:BC"
	if err := settings.Save(settings.Settings{RedactPersonal: true, RedactWords: []string{"project-custom-eight"}, RedactKinds: map[string]bool{"mac": false}}); err != nil {
		t.Fatal(err)
	}
	masked, err := maskedQuery(query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(masked, "12:34:56:78:9A:BC") || strings.Contains(masked, "13512345678") || strings.Contains(masked, "privacy-options@proton.me") || strings.Contains(masked, "project-custom-eight") {
		t.Fatalf("redaction settings not respected: %s", masked)
	}
	if again, err := maskedQuery(masked, nil); err != nil || again != masked {
		t.Fatalf("placeholder masked again: %s => %s", masked, again)
	}
	if err := settings.Save(settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	if got, err := maskedQuery(query, nil); err != nil || got != query {
		t.Fatalf("disabled masking changed query: %s", got)
	}
}
