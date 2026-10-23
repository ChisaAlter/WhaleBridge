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
	"unicode/utf8"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

func TestWhaleBridgeSearchKeyPoolAndRefusals(t *testing.T) {
	fresh(t)
	forgetSearchKeys()
	t.Cleanup(forgetSearchKeys)
	keys := []string{"tvly-pool-aaaaaaaa1111", "tvly-pool-bbbbbbbb2222", "tvly-pool-cccccccc3333"}
	var mu sync.Mutex
	var asked []string
	denied := map[string]int{}
	retry := "120"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		asked = append(asked, key)
		status := denied[key]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.Header().Set("Retry-After", retry)
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "refused " + strings.Join(keys, " ")}})
			return
		}
		io.WriteString(w, `{"results":[{"title":"Found","url":"https://example.test/","content":"search works"}]}`)
	}))
	t.Cleanup(up.Close)
	a := provider.SearchAPI{Vendor: "tavily", Key: strings.Join(keys, ","), URL: up.URL}
	s := New()
	for range 4 {
		if _, err := s.askSearchAPIKeys(context.Background(), a, "q"); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	got := slices.Clone(asked)
	asked = nil
	mu.Unlock()
	if !slices.Equal(got, []string{keys[0], keys[1], keys[2], keys[0]}) {
		t.Fatalf("round robin %v", got)
	}
	forgetSearchKeys()
	a.Pick = provider.SearchPickOrder
	for range 2 {
		if _, err := s.askSearchAPIKeys(context.Background(), a, "q"); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	got = slices.Clone(asked)
	asked = nil
	denied[keys[0]] = 429
	mu.Unlock()
	if !slices.Equal(got, []string{keys[0], keys[0]}) {
		t.Fatalf("ordered %v", got)
	}
	if _, err := s.askSearchAPIKeys(context.Background(), a, "q"); err != nil {
		t.Fatal(err)
	}
	if r, ok := searchKeyRest(a.Vendor, keys[0], time.Now()); !ok || r.Why != "rate limited" || time.Until(r.Until) < 110*time.Second {
		t.Fatalf("RetryAfter rest %+v %v", r, ok)
	}
	mu.Lock()
	asked = nil
	mu.Unlock()
	if _, err := s.askSearchAPIKeys(context.Background(), a, "q"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got = slices.Clone(asked)
	asked = nil
	denied[keys[1]] = 500
	mu.Unlock()
	if !slices.Equal(got, []string{keys[1]}) {
		t.Fatalf("rest key was asked first %v", got)
	}
	if _, err := s.askSearchAPIKeys(context.Background(), a, "q"); err == nil {
		t.Fatal("non-key failure not returned")
	} else {
		for _, key := range keys {
			if strings.Contains(err.Error(), key) {
				t.Fatalf("error leaked sibling key: %v", err)
			}
		}
	}
	mu.Lock()
	got = slices.Clone(asked)
	asked = nil
	denied[keys[0]] = 401
	denied[keys[1]] = 402
	denied[keys[2]] = 433
	mu.Unlock()
	if !slices.Equal(got, []string{keys[1]}) {
		t.Fatalf("500 spent more keys: %v", got)
	}
	if _, err := s.askSearchAPIKeys(context.Background(), a, "q"); err == nil {
		t.Fatal("all keys refused but no error")
	} else {
		for _, key := range keys {
			if strings.Contains(err.Error(), key) {
				t.Fatalf("refusal leaked secret: %v", err)
			}
		}
	}
	// State maps use hashes, including the stored rest reason.
	searchRests.Range(func(k, v any) bool {
		b, _ := json.Marshal([]any{k, v})
		for _, key := range keys {
			if strings.Contains(string(b), key) {
				t.Fatalf("rest stores raw key %s", b)
			}
		}
		return true
	})
	for _, key := range keys {
		wakeSearchKey(a.Vendor, key)
	}
	for i, key := range keys {
		restSearchKey(a.Vendor, key, time.Duration(3-i)*time.Minute, "rate limited", time.Now())
	}
	if order := searchKeyOrder(a, keys, time.Now()); !slices.Equal(order, []int{2, 1, 0}) {
		t.Fatalf("resting recovery order %v", order)
	}
	mu.Lock()
	clear(denied)
	mu.Unlock()
	if _, err := s.askSearchAPIKeys(context.Background(), a, "q"); err != nil {
		t.Fatal(err)
	}
	if _, ok := searchKeyRest(a.Vendor, keys[2], time.Now()); ok {
		t.Fatal("success did not wake resting key")
	}
}

func TestWhaleBridgeSearchOverlappingKeyEchoes(t *testing.T) {
	fresh(t)
	forgetSearchKeys()
	t.Cleanup(forgetSearchKeys)
	short := "tvly-overlap-aaaa1111"
	long := short + "-tail-2222"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]string{"message": "rejected " + short + " " + long})
	}))
	t.Cleanup(up.Close)
	_, err := New().askSearchAPIKeys(context.Background(), provider.SearchAPI{Vendor: "tavily", URL: up.URL, Key: short + "," + long}, "q")
	if err == nil || strings.Contains(err.Error(), short) || strings.Contains(err.Error(), long) || strings.Contains(err.Error(), "-tail-") {
		t.Fatalf("overlapping key echo leaked: %v", err)
	}
}

func TestWhaleBridgeSearchRestBounds(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		vendor string
		status int
		after  string
		d      time.Duration
		why    string
	}{
		{"tavily", 401, "", turnedAwayRest, "turned away"}, {"tavily", 432, "", outOfCreditRest, "out of credit"}, {"deepseek", 402, "", outOfCreditRest, "out of credit"},
		{"deepseek", 429, "1", 30 * time.Second, "rate limited"}, {"brave", 433, "", time.Minute, "rate limited"}, {"brave", 429, "999999999999999999", 10 * time.Minute, "rate limited"},
		{"brave", 429, now.Add(5 * time.Minute).UTC().Format(http.TimeFormat), 5 * time.Minute, "rate limited"},
	} {
		d, why := searchRestFor(tc.vendor, tc.status, tc.after, now)
		if d < tc.d-time.Second || d > tc.d+time.Second || why != tc.why {
			t.Errorf("%+v => %v %s", tc, d, why)
		}
	}
}

func TestWhaleBridgeSearchOffAndActualOrder(t *testing.T) {
	fresh(t)
	forgetSearchKeys()
	t.Cleanup(forgetSearchKeys)
	if err := settings.Save(settings.Settings{Searcher: "off"}); err != nil {
		t.Fatal(err)
	}
	var asked []string
	var mu sync.Mutex
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vendor := strings.TrimPrefix(r.URL.Path, "/")
		mu.Lock()
		asked = append(asked, vendor)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(vendor, "tavily") {
			w.WriteHeader(500)
			io.WriteString(w, `{"error":"down"}`)
			return
		}
		io.WriteString(w, `{"results":[{"title":"B","url":"https://example.test/","content":"ok"}],"web":{"results":[{"title":"B","url":"https://example.test/","description":"ok"}]}}`)
	}))
	t.Cleanup(up.Close)
	for _, v := range []string{"tavily", "brave"} {
		if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: v, Key: "fixture-" + v, URL: up.URL + "/" + v}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := New().webSearch(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := slices.Clone(asked)
	asked = nil
	mu.Unlock()
	if len(got) != 2 || !strings.HasPrefix(got[0], "tavily/") || !strings.HasPrefix(got[1], "brave/") {
		t.Fatalf("initial search order %v", got)
	}
	if err := provider.ArrangeSearchAPIs([]string{"brave", "tavily"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New().webSearch(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got = slices.Clone(asked)
	asked = nil
	mu.Unlock()
	if len(got) != 1 || !strings.HasPrefix(got[0], "brave/") {
		t.Fatalf("arranged search order %v", got)
	}
	for _, v := range []string{"brave", "tavily"} {
		if err := provider.SwitchSearchAPI(v, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := New().webSearch(context.Background(), "q"); err == nil {
		t.Fatal("off searchers answered")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 0 {
		t.Fatalf("off services were asked %v", asked)
	}
}

func TestWhaleBridgeSearchDeepSeekHTTP(t *testing.T) {
	fresh(t)
	var gotQuery, gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Query       string
			Count       int
			NeedContent bool `json:"need_content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Error(err)
		}
		gotQuery, gotPath = q.Query, r.URL.Path
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer ds-fixture-key" || q.Count != searchHits || q.NeedContent {
			t.Errorf("DeepSeek request %s %s %+v", r.Method, r.Header.Get("Authorization"), q)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[{"title":"一","url":"https://example.test/one","summary":"Summary","snippet":"Snippet","content":"","date":"2026-10-17T18:14:52Z"},{"title":"二","url":"https://example.test/two","summary":"","snippet":"Fallback","content":"","date":null},{"title":"三","url":"https://example.test/three","summary":"Summary","content":"Body"}]}`)
	}))
	t.Cleanup(up.Close)
	query := strings.Repeat("鲸🌊", 100)
	pages, err := New().askSearchAPI(context.Background(), provider.SearchAPI{Vendor: "deepseek", Key: "ds-fixture-key", URL: up.URL + "/custom"}, query)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/custom/v1/tools/search" || !utf8.ValidString(gotQuery) || utf8.RuneCountInString(gotQuery) != 128 || gotQuery != string([]rune(query)[:128]) {
		t.Fatalf("path/query %s %q", gotPath, gotQuery)
	}
	if len(pages) != 3 || pages[0].Text != "Summary" || pages[0].Age != "2026-10-17T18:14:52Z" || pages[1].Text != "Fallback" || pages[1].Age != "" || pages[2].Text != "Body" {
		t.Fatalf("DeepSeek pages %+v", pages)
	}
	_, hits := pagesSaid(pages)
	if hits[0].PageAge != pages[0].Age {
		t.Fatalf("hit age %+v", hits)
	}
}
