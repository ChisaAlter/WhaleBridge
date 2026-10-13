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

	"github.com/tidwall/gjson"
	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// pickEfforts saves the levels picked in Settings for Image recognition's
// model and the searcher's.
func pickEfforts(t *testing.T, vision, searcher string) {
	t.Helper()
	st := settings.Load()
	st.VisionEffort, st.SearcherEffort = vision, searcher
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
}

// The reasoning level picked for Image recognition (MOMO on Discord) is
// what its model is asked at, fitted to the levels it has, with room to
// think in; Default is magpie's own, the least it has, as before. The
// model's own requests, from an agent, are sent as they came.

// Image recognition on a Claude is asked to think with a budget under its
// reply limit, which the gateway fits from the level picked.

// searchUp is a vendor's Messages API that answers a search, or its
// Responses API that turns it away, keeping each body it is sent.
type searchUp struct {
	mu     sync.Mutex
	bodies []string
}

func (u *searchUp) last() gjson.Result {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) == 0 {
		return gjson.Result{}
	}
	return gjson.Parse(u.bodies[len(u.bodies)-1])
}

func (u *searchUp) serve(models ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			var ms []string
			for _, m := range models {
				ms = append(ms, `{"id":"`+m+`"}`)
			}
			io.WriteString(w, `{"data":[`+strings.Join(ms, ",")+`]}`)
			return
		}
		u.mu.Lock()
		u.bodies = append(u.bodies, string(b))
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, `{"error":{"message":"only kept"}}`, 400)
			return
		}
		io.WriteString(w, `{"id":"s","type":"message","role":"assistant","model":"m","content":[
			{"type":"server_tool_use","id":"srv","name":"web_search","input":{"query":"go"}},
			{"type":"web_search_tool_result","tool_use_id":"srv","content":[{"type":"web_search_result","title":"Go downloads","url":"https://go.dev/dl/"}]},
			{"type":"text","text":"The latest Go is 1.27.1."}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":5}}`)
	}
}

// The reasoning level picked for the searcher (MOMO on Discord) is asked
// of its model in its vendor's words: a Claude that thinks only
// adaptively is asked output_config.effort, an older Claude a budget
// alone, OpenAI's Responses reasoning.effort; "none" turns thinking off,
// and Default sends none, as before.
func TestSearcherEffortPicked(t *testing.T) {
	fresh(t)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	newClaude, oldClaude, oai := &searchUp{}, &searchUp{}, &searchUp{}
	nc := httptest.NewServer(newClaude.serve("claude-sonnet-5-5"))
	oc := httptest.NewServer(oldClaude.serve("claude-haiku-4-5"))
	oa := httptest.NewServer(oai.serve("gpt-5-mini"))
	for _, srv := range []*httptest.Server{nc, oc, oa} {
		t.Cleanup(srv.Close)
	}
	for proto, urls := range map[provider.Protocol][]string{provider.Anthropic: {nc.URL, oc.URL}, provider.Responses: {oa.URL}} {
		hosts := searchHosts[proto]
		add := slices.Clone(hosts)
		for _, u := range urls {
			add = append(add, provider.HostOf(u))
		}
		searchHosts[proto] = add
		t.Cleanup(func() { searchHosts[proto] = hosts })
	}
	levels := []string{"none", "low", "medium", "high", "xhigh", "max"}
	for _, c := range []struct {
		p     provider.Provider
		base  string
		model string
	}{
		{provider.Provider{ID: "new", Name: "New", Key: "k", Anthropic: nc.URL}, nc.URL, "claude-sonnet-5-5"},
		{provider.Provider{ID: "old", Name: "Old", Key: "k", Anthropic: oc.URL}, oc.URL, "claude-haiku-4-5"},
		{provider.Provider{ID: "oai", Name: "OpenAI", Key: "k", Responses: oa.URL + "/v1"}, oa.URL + "/v1", "gpt-5-mini"},
	} {
		if err := provider.Save(c.p); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive(c.p.ID, c.base, []catalog.Model{{ID: c.model, Efforts: levels}}); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	search := func(named, effort string) {
		t.Helper()
		st := settings.Load()
		st.Searcher, st.SearcherEffort = named, effort
		if err := settings.Save(st); err != nil {
			t.Fatal(err)
		}
		if p, m, ok := searcher(); !ok || p.ID+"/"+m != named {
			t.Fatalf("searcher = %s/%s, want %s", p.ID, m, named)
		}
		// a Responses reply is streamed, which this one isn't: only what it
		// was sent is looked at
		if _, _, err := s.modelSearch(context.Background(), "latest go"); err != nil && !strings.HasPrefix(named, "oai/") {
			t.Fatalf("%s at %q: %v", named, effort, err)
		}
	}

	search("new/claude-sonnet-5-5", "")
	if q := newClaude.last(); q.Get("thinking").Exists() && q.Get("thinking.type").String() != "disabled" || q.Get("output_config.effort").Exists() {
		t.Fatalf("default sent %s", q.Raw)
	}
	if !slices.Equal(SearchEfforts(), levels) || SearchEffortNow() != "" {
		t.Fatalf("the row is offered %q at %q", SearchEfforts(), SearchEffortNow())
	}
	search("new/claude-sonnet-5-5", "xhigh")
	if q := newClaude.last(); q.Get("thinking.type").String() != "adaptive" || q.Get("output_config.effort").String() != "xhigh" {
		t.Fatalf("a Claude that thinks adaptively was sent %s", q.Raw)
	}
	if SearchEffortNow() != "xhigh" {
		t.Fatalf("the row shows %q", SearchEffortNow())
	}
	search("new/claude-sonnet-5-5", "none")
	// off as this Claude takes it: between_tools on Sonnet 5.5 (#1454)
	if q := newClaude.last(); q.Get("thinking.type").String() != "between_tools" || q.Get("output_config.effort").Exists() {
		t.Fatalf("none was sent %s", q.Raw)
	}
	search("old/claude-haiku-4-5", "high")
	if q := oldClaude.last(); q.Get("thinking.type").String() != "enabled" || q.Get("thinking.budget_tokens").Int() != 24000 ||
		q.Get("max_tokens").Int() <= 24000 || q.Get("output_config").Exists() {
		t.Fatalf("an older Claude was sent %s", q.Raw)
	}
	search("oai/gpt-5-mini", "medium")
	if q := oai.last(); q.Get("reasoning.effort").String() != "medium" {
		t.Fatalf("Responses was sent %s", q.Raw)
	}
	search("oai/gpt-5-mini", "")
	if q := oai.last(); q.Get("reasoning.effort").Exists() {
		t.Fatalf("Responses at the default was sent %s", q.Raw)
	}
}

// A level that isn't one is refused, so a typo doesn't go to a vendor.
func TestSideEffortChecked(t *testing.T) {
	fresh(t)
	st := settings.Load()
	st.SearcherEffort = "hard"
	if err := settings.Save(st); err == nil {
		t.Fatal("a level that isn't one was saved")
	}
	st.SearcherEffort, st.VisionEffort = " High ", "max"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load(); got.SearcherEffort != "high" || got.VisionEffort != "max" {
		t.Fatalf("saved %+v", got)
	}
}

// The reporter's searcher, Gemini 3 Flash on Antigravity, is asked the
// level picked as its thinkingLevel; at the default it is sent none.
func TestSearcherEffortOnGemini(t *testing.T) {
	ag := provider.Provider{ID: "antigravity", Account: &provider.Account{Agent: "antigravity", User: "u"}}
	level := func(effort string) string {
		t.Helper()
		b, _ := json.Marshal(searchBody(ag, "gemini-3-flash", "latest go", effort))
		r, err := parseAnthropic(b)
		if err != nil {
			t.Fatal(err)
		}
		return gjson.GetBytes(buildCodeAssist(r, "gemini-3-flash", "antigravity"), "request.generationConfig.thinkingConfig.thinkingLevel").String()
	}
	for effort, want := range map[string]string{"": "", "low": "low", "medium": "medium", "high": "high"} {
		if got := level(effort); got != want {
			t.Errorf("%q: thinkingLevel %q, want %q", effort, got, want)
		}
	}
}
