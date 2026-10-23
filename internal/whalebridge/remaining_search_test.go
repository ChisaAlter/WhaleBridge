package whalebridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

func TestWhaleBridgeRemainingSearchManagementHTTP(t *testing.T) {
	h := newProviderHTTP(t)
	var reads atomic.Int32
	const first, second = "tvly-fixture-first-secret", "tvly-fixture-second-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usage" {
			t.Errorf("unexpected remote request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer "+second {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]any{"error": first + " and " + second})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"key": map[string]any{"usage": 25, "limit": 100}, "account": map[string]any{"current_plan": "fixture " + first + " and " + second}})
	}))
	t.Cleanup(srv.Close)
	h.call("POST", "search/save", map[string]any{"vendor": "tavily", "key": first + "," + second, "url": srv.URL, "pick": "order"}, 200)
	h.call("POST", "search/save", map[string]any{"vendor": "deepseek", "key": "fixture-deepseek-key", "url": h.vendor}, 200)
	h.call("POST", "search/save", map[string]any{"vendor": "you"}, 200)
	raw := h.call("GET", "global", nil, 200)
	assertSecretsAbsent(t, raw, first, second, "fixture-deepseek-key")
	if reads.Load() != 0 {
		t.Fatal("ordinary settings state queried usage")
	}
	var state struct {
		Searches []struct {
			Vendor, Pick  string
			Off, CanQuota bool
		}
		SearchVendors []struct{ ID, Base string }
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Searches) != 3 || state.Searches[0].Pick != "order" || !state.Searches[0].CanQuota || state.Searches[1].CanQuota {
		t.Fatal("safe search management DTO lost mode/quota capabilities")
	}
	found := false
	for _, v := range state.SearchVendors {
		if v.ID == "deepseek" && v.Base == "https://api.deepseek.com" {
			found = true
		}
	}
	if !found {
		t.Fatal("DeepSeek missing from editor")
	}
	h.call("POST", "search/switch", map[string]any{"vendor": "tavily", "on": false}, 200)
	h.call("POST", "search/save", map[string]any{"vendor": "tavily", "url": srv.URL}, 200)
	if a := provider.StoredSearchAPIs()[0]; !a.Off || a.Pick != "order" || a.Key != first+","+second {
		t.Fatal("editing disabled service lost key/order/off state")
	}
	for _, a := range provider.SearchAPIs() {
		if a.Vendor == "tavily" {
			t.Fatal("disabled search still selected")
		}
	}
	h.call("POST", "search/order", map[string]any{"vendors": []string{"deepseek", "tavily", "you"}}, 200)
	stored := provider.StoredSearchAPIs()
	if !slices.Equal([]string{stored[0].Vendor, stored[1].Vendor, stored[2].Vendor}, []string{"deepseek", "tavily", "you"}) {
		t.Fatal("service order not persisted")
	}
	before, _ := os.ReadFile(provider.Path())
	h.call("POST", "search/order", map[string]any{"vendors": []string{"you", "you"}}, 400)
	after, _ := os.ReadFile(provider.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("invalid order partly saved")
	}
	h.call("POST", "search/switch", map[string]any{"vendor": "missing", "on": false}, 400)
	h.call("POST", "search/save", map[string]any{"vendor": "tavily", "url": srv.URL, "pick": "turns"}, 200)
	h.call("POST", "search/switch", map[string]any{"vendor": "tavily", "on": true}, 200)
	if a := provider.StoredSearchAPIs()[1]; a.Off || a.Pick != "" || a.Key != first+","+second {
		t.Fatal("explicit re-enable or turns failed")
	}
	quota := h.call("POST", "search/quota", map[string]any{"vendor": "tavily"}, 200)
	assertSecretsAbsent(t, quota, first, second)
	var reply struct {
		Keys []struct {
			Known          bool
			Used, Limit    int64
			Err, Plan, Key string
		}
	}
	if err := json.Unmarshal(quota, &reply); err != nil || len(reply.Keys) != 2 {
		t.Fatal("quota reply unreadable")
	}
	if !reply.Keys[0].Known || reply.Keys[0].Used != 25 || reply.Keys[0].Limit != 100 || reply.Keys[1].Known || reply.Keys[1].Err == "" {
		t.Fatal("unknown quota became zero-use success")
	}
	if reads.Load() != 2 {
		t.Fatalf("quota calls=%d", reads.Load())
	}
	h.call("GET", "search/quota?vendor=tavily", nil, 200)
	if reads.Load() != 2 {
		t.Fatal("recent quota not cached")
	}
	h.call("POST", "search/quota", map[string]any{"vendor": "deepseek"}, 400)
	h.call("GET", "search/quota?vendor=missing", nil, 404)
	if reads.Load() != 2 {
		t.Fatal("unsupported quota issued network call")
	}
}
