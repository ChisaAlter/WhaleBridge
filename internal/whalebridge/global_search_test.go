package whalebridge

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// The component's search editor saves each key, but its ordinary state
// masks each separately. Only the explicit key endpoint reveals the list.
func TestGlobalSearchKeysAndOptionalYou(t *testing.T) {
	a := newProviderHTTP(t)
	a.call("POST", "search/save", map[string]string{"vendor": "tavily", "key": "tvly-first-secret, tvly-second-secret\ntvly-first-secret", "url": a.vendor}, 200)
	a.call("POST", "search/save", map[string]string{"vendor": "you"}, 200)
	raw := a.call("GET", "global", nil, 200)
	for _, key := range []string{"tvly-first-secret", "tvly-second-secret"} {
		if bytes.Contains(raw, []byte(key)) {
			t.Fatal("ordinary global state disclosed a search credential")
		}
	}
	var state struct {
		Searches []struct {
			Vendor, KeyMasked string
			KeySet, Ready     bool
		}
		SearchVendors []struct {
			ID          string
			KeyOptional bool
		}
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Searches) != 2 || state.Searches[0].KeyMasked != "tvly-…cret, tvly-…cret" || !state.Searches[0].KeySet || !state.Searches[0].Ready {
		t.Fatalf("multi-key search state was lost: %+v", state.Searches)
	}
	if state.Searches[1].Vendor != "you" || state.Searches[1].KeySet || !state.Searches[1].Ready {
		t.Fatalf("You.com needs no key: %+v", state.Searches[1])
	}
	found := false
	for _, v := range state.SearchVendors {
		if v.ID == "you" {
			found = v.KeyOptional
		}
	}
	if !found {
		t.Fatal("search editor did not receive You.com's optional-key contract")
	}
	a.call("POST", "search/save", map[string]string{"vendor": "tavily", "url": a.vendor + "/new-base"}, 200)
	var revealed struct{ Key string }
	if err := json.Unmarshal(a.call("GET", "search/key?vendor=tavily", nil, 200), &revealed); err != nil {
		t.Fatal(err)
	}
	if revealed.Key != "tvly-first-secret,tvly-second-secret" {
		t.Fatal("blank-key editing failed to preserve the normalized key list")
	}
	a.call("POST", "search/save", map[string]string{"vendor": "brave"}, 400)
	a.call("POST", "search/save", map[string]string{"vendor": "tavily", "url": a.vendor + "?key=unrelated"}, 400)
	if saved := provider.StoredSearchAPIs(); saved[0].URL != a.vendor+"/new-base" {
		t.Fatal("a rejected base URL replaced the saved search endpoint")
	}
	a.call("POST", "search/delete", map[string]string{"vendor": "you"}, 200)
	a.call("GET", "search/key?vendor=you", nil, 404)
}
