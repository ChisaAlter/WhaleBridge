package whalebridge

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// The provider controls upstream added after the component's last sync reach
// its editor: saved, shown, kept by unrelated edits, and refused when invalid
// without saving anything.
func TestProviderUpstreamControlsHTTP(t *testing.T) {
	a := newProviderHTTP(t)
	id := a.save(map[string]any{"new": true, "id": "own", "name": "Own relay", "chat": a.vendor + "/v1", "key": "fixture-own-key",
		"models": []string{"test-model"}, "quotaResetAt": "08:00", "restCap": -1, "balanceMethod": "post", "insecureTLS": true,
		"refreshEvery": "6h", "autoInclude": []string{"gpt-*", " gpt-* "}})
	check := func(when string) {
		t.Helper()
		p := a.provider(id)
		if p.QuotaResetAt != "08:00" || p.RestCap != provider.RestCapOff || p.BalanceMethod != "POST" || !p.InsecureTLS ||
			p.RefreshEvery == "" || !slices.Equal(p.AutoInclude, []string{"gpt-*"}) {
			t.Fatalf("%s: %+v", when, p)
		}
	}
	check("saved")
	a.save(map[string]any{"id": id, "name": "Renamed relay"})
	check("after an unrelated edit")

	var state struct{ Providers []map[string]any }
	if err := json.Unmarshal(a.call("GET", "state", nil, 200), &state); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(state.Providers, func(p map[string]any) bool { return p["id"] == id })
	if i < 0 || state.Providers[i]["takesInsecureTLS"] != true || state.Providers[i]["ownList"] != true ||
		state.Providers[i]["carriesCompaction"] != false || state.Providers[i]["quotaResetAt"] != "08:00" {
		t.Fatalf("editor state lacks the controls: %v", state.Providers[i])
	}

	before, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range []map[string]any{
		{"quotaResetAt": "25:99"}, {"restCap": -2}, {"restCap": provider.MaxQuotaRest + 1}, {"balanceMethod": "PUT"},
		{"refreshEvery": "1m"}, {"autoInclude": []string{"plain-model-id"}}, {"nativeCompaction": true},
	} {
		patch["id"], patch["name"] = id, "Must not save"
		a.call("POST", "provider/save", patch, 400)
		after, err := os.ReadFile(provider.Path())
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("invalid %v partially saved the provider", patch)
		}
	}

	a.save(map[string]any{"id": id, "quotaResetAt": "", "restCap": nil, "balanceMethod": "", "insecureTLS": false, "refreshEvery": "", "autoInclude": []string{}})
	p := a.provider(id)
	if p.QuotaResetAt != "" || p.RestCap != 0 || p.BalanceMethod != "" || p.InsecureTLS || p.RefreshEvery != "" || len(p.AutoInclude) != 0 {
		t.Fatalf("explicit reset did not persist: %+v", p)
	}
}
