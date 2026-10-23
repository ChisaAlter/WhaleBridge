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

func TestWhaleBridgeSearchQuotaParser(t *testing.T) {
	for _, tc := range []struct {
		body             string
		used, limit      int64
		of               string
		unlimited, spent bool
	}{
		{`{"key":{"usage":100,"limit":100},"account":{"plan_limit":1000,"plan_usage":500,"paygo_limit":100,"paygo_usage":0}}`, 100, 100, "key", false, true},
		{`{"key":{"usage":100,"limit":null},"account":{"plan_limit":1000,"plan_usage":900,"current_plan":"Free"}}`, 900, 1000, "plan", false, false},
		{`{"key":{"usage":100,"limit":null},"account":{"plan_limit":1000,"plan_usage":1000,"paygo_limit":20,"paygo_usage":10}}`, 1000, 1000, "plan", false, false},
		{`{"key":{"usage":100,"limit":null},"account":{"plan_limit":1000,"plan_usage":1000}}`, 1000, 1000, "plan", false, true},
		{`{"key":{"usage":23,"limit":null},"account":{"plan_limit":null}}`, 23, 0, "", true, false},
	} {
		q, err := readTavilyUsage([]byte(tc.body))
		if err != nil || !q.Known || q.Used != tc.used || q.Limit != tc.limit || q.Of != tc.of || q.Unlimited != tc.unlimited || q.UsedUp != tc.spent {
			t.Errorf("%s => %+v %v", tc.body, q, err)
		}
	}
	for _, body := range []string{`{}`, `{"key":{"limit":null}}`, `{"key":{"limit":3}}`, `{"key":{"limit":null,"usage":2},"account":{"plan_limit":5}}`, `{"key":{"limit":-1,"usage":2}}`, `{"key":{"limit":3.5,"usage":2}}`, `{"key":{"limit":3,"usage":"2"}}`, `{"key":{"limit":null},"account":{"plan_limit":null}}`, `{"key":{"limit":3,"usage":null}}`} {
		if q, err := readTavilyUsage([]byte(body)); err == nil || q.Known {
			t.Errorf("malformed quota became known: %s %+v %v", body, q, err)
		}
	}
}

func TestWhaleBridgeSearchQuotasHTTPAndMasks(t *testing.T) {
	fresh(t)
	forgetSearchKeys()
	searchQuotas.Clear()
	t.Cleanup(func() { forgetSearchKeys(); searchQuotas.Clear() })
	keys := []string{"tvly-quota-aaaaaaaa1111", "tvly-quota-bbbbbbbb2222", "tvly-quota-cccccccc3333"}
	var mu sync.Mutex
	calls := 0
	credit := false
	errorMode := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		calls++
		hasCredit, bad := credit, errorMode
		mu.Unlock()
		if r.Method != http.MethodGet || r.URL.Path != "/custom/usage" {
			t.Errorf("quota path/method %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if key == keys[2] || bad {
			w.WriteHeader(429)
			json.NewEncoder(w).Encode(map[string]string{"message": "echo " + strings.Join(keys, " ")})
			return
		}
		used := 100
		if hasCredit || key == keys[1] {
			used = 20
		}
		json.NewEncoder(w).Encode(map[string]any{"key": map[string]any{"usage": used, "limit": 100}, "account": map[string]any{"current_plan": "plan " + strings.Join(keys, " "), "plan_limit": 1000, "plan_usage": 500}})
	}))
	t.Cleanup(up.Close)
	a := provider.SearchAPI{Vendor: "tavily", Key: strings.Join(keys, ","), URL: up.URL + "/custom"}
	q := SearchQuotas(context.Background(), a, true)
	if len(q) != 3 || !q[0].Known || !q[0].UsedUp || q[0].Resting != "used up" || !q[1].Known || q[1].Used != 20 || q[2].Known || q[2].Err == "" {
		t.Fatalf("quotas %+v", q)
	}
	b, _ := json.Marshal(q)
	for _, key := range keys {
		if strings.Contains(string(b), key) {
			t.Fatalf("quota JSON leaked key %s", b)
		}
	}
	if !strings.Contains(q[0].Plan, "tvly-…1111") || !strings.Contains(q[2].Err, "tvly-…2222") {
		t.Fatalf("all echoes not masked %+v", q)
	}
	if order := searchKeyOrder(a, keys, time.Now()); order[0] == 0 {
		t.Fatalf("quota used-up key still first %v", order)
	}
	_ = SearchQuotas(context.Background(), a, false)
	mu.Lock()
	n := calls
	credit = true
	mu.Unlock()
	if n != 3 {
		t.Fatalf("cache miss: calls %d", n)
	}
	q = SearchQuotas(context.Background(), a, true)
	if q[0].UsedUp || q[0].Resting != "" || q[0].Used != 20 {
		t.Fatalf("credit did not wake %+v", q[0])
	}
	if _, ok := searchKeyRest(a.Vendor, keys[0], time.Now()); ok {
		t.Fatal("used-up rest kept after top-up")
	}
	mu.Lock()
	errorMode = true
	mu.Unlock()
	q = SearchQuotas(context.Background(), a, true)
	for _, v := range q {
		if v.Known || v.Err == "" {
			t.Fatalf("failed read reported zero/known %+v", q)
		}
	}
	searchQuotas.Range(func(k, v any) bool {
		b, _ := json.Marshal([]any{k, v.(keptQuota).q})
		for _, key := range keys {
			if strings.Contains(string(b), key) {
				t.Fatalf("cache leaked key %s", b)
			}
		}
		return true
	})
	// A changed service address must not reuse another host/base's quota.
	otherCalls := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		otherCalls++
		mu.Unlock()
		io.WriteString(w, `{"key":{"usage":7,"limit":100}}`)
	}))
	t.Cleanup(other.Close)
	a.URL = other.URL
	q = SearchQuotas(context.Background(), a, false)
	mu.Lock()
	n = otherCalls
	mu.Unlock()
	if n != 3 || len(q) != 3 || q[0].Used != 7 {
		t.Fatalf("host cache mixed %d %+v", n, q)
	}
	if CanReadSearchQuota(provider.SearchAPI{Vendor: "deepseek", Key: "key"}) || SearchQuotas(context.Background(), provider.SearchAPI{Vendor: "deepseek", Key: "key"}, true) != nil {
		t.Fatal("unsupported service read quota")
	}
}

func TestWhaleBridgeSearchQuotaRedirectKeepsKey(t *testing.T) {
	fresh(t)
	searchQuotas.Clear()
	t.Cleanup(searchQuotas.Clear)
	var mu sync.Mutex
	var leaked []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		leaked = append(leaked, r.Header.Get("Authorization"))
		mu.Unlock()
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(other.Close)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/usage", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(up.Close)
	q := SearchQuotas(context.Background(), provider.SearchAPI{Vendor: "tavily", Key: "tvly-redirect-aaaa1111", URL: up.URL}, true)
	if len(q) != 1 || q[0].Known || q[0].Err == "" {
		t.Fatalf("redirect quota %+v", q)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(leaked, []string(nil)) {
		t.Fatalf("redirect got keys %v", leaked)
	}
}
