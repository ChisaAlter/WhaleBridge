package whalebridge

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/agentenv"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/redact"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
	"github.com/ChisaAlter/WhaleBridge/internal/usage"
	"github.com/ChisaAlter/WhaleBridge/internal/usageapi"
)

func newGlobalParityHTTP(t *testing.T) providerHTTP {
	t.Helper()
	for _, name := range agentenv.Vars {
		t.Setenv(name, "")
	}
	t.Setenv("MAGPIE_NO_ACCOUNTS", "1")
	t.Setenv("PATH", "")
	a := newProviderHTTP(t)
	t.Setenv("CODEX_HOME", filepath.Join(os.Getenv("USERPROFILE"), ".codex-parity"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(os.Getenv("USERPROFILE"), ".claude-parity"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("USERPROFILE"), ".config-parity"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(os.Getenv("USERPROFILE"), ".cache-parity"))
	s := settings.Load()
	s.NoWSLAgents = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	return a
}

func TestGlobalPrivacyCategoriesAndExplicitConsent(t *testing.T) {
	a := newGlobalParityHTTP(t)
	var state struct{ RedactCategories []map[string]json.RawMessage }
	if err := json.Unmarshal(a.call("GET", "global", nil, 200), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.RedactCategories) != 10 {
		t.Fatalf("categories: %d", len(state.RedactCategories))
	}
	kinds := map[string]bool{}
	for _, row := range state.RedactCategories {
		var id string
		var on bool
		if len(row) != 2 || row["id"] == nil || row["on"] == nil || json.Unmarshal(row["id"], &id) != nil || json.Unmarshal(row["on"], &on) != nil || id == "" {
			t.Fatalf("renderer needs lowercase id/on, not Go acronym fields: %v", row)
		}
		if _, exists := kinds[id]; exists {
			t.Fatalf("duplicate category %q", id)
		}
		kinds[id] = on
	}
	if !kinds["ssn"] || kinds["home"] {
		t.Fatal("official category defaults missing")
	}
	kinds["ssn"], kinds["home"] = false, true
	a.call("POST", "global/settings", map[string]any{"redactPersonal": true, "redactKinds": kinds, "noAccountDiscovery": true, "quotaReads": "asked"}, 200)
	s := settings.Load()
	if !s.NoAccountDiscovery || s.QuotaReads != "asked" || !reflect.DeepEqual(s.RedactKinds, kinds) {
		t.Fatalf("explicit privacy choices were not saved: %+v", s.RedactKinds)
	}
	if text, _ := redact.Mask("SSN: 536-22-1847", redact.Options{Personal: s.RedactPersonal, Kinds: s.RedactKinds}); text != "SSN: 536-22-1847" {
		t.Fatal("disabled category still masked")
	}
	kinds["ssn"] = true
	a.call("POST", "global/settings", map[string]any{"redactKinds": kinds}, 200)
	s = settings.Load()
	if text, count := redact.Mask("SSN: 536-22-1847", redact.Options{Personal: s.RedactPersonal, Kinds: s.RedactKinds}); text == "SSN: 536-22-1847" || count != 1 {
		t.Fatal("enabled category was not consumed by masking")
	}
	a.call("POST", "global/settings", map[string]any{"quotaReads": "invalid"}, 400)
	if settings.Load().QuotaReads != "asked" {
		t.Fatal("invalid quota read strategy replaced saved consent")
	}
}

func TestGlobalAccountDiscoveryClearsCachedNativeSignIn(t *testing.T) {
	a := newGlobalParityHTTP(t)
	t.Setenv("MAGPIE_NO_ACCOUNTS", "")
	file := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	jwt := func(claims map[string]any) string {
		data, _ := json.Marshal(claims)
		return "h." + base64.RawURLEncoding.EncodeToString(data) + ".sig"
	}
	write := func(email string) {
		t.Helper()
		data, err := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"id_token": jwt(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "fixture-account"}}), "access_token": jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}), "refresh_token": "fixture-refresh", "account_id": "fixture-account"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("before@example.test")
	accounts := provider.Accounts()
	if !slices.ContainsFunc(accounts, func(p provider.Provider) bool { return p.Account != nil && p.Account.User == "before@example.test" }) {
		t.Fatal("isolated Codex login was not actually discovered")
	}
	a.call("POST", "global/settings", map[string]any{"noAccountDiscovery": true}, 200)
	if len(provider.Accounts()) != 0 {
		t.Fatal("discovered account stayed available after discovery was disabled")
	}
	write("after-privacy-choice@example.test")
	a.call("POST", "global/settings", map[string]any{"noAccountDiscovery": false}, 200)
	if !slices.ContainsFunc(provider.Accounts(), func(p provider.Provider) bool {
		return p.Account != nil && p.Account.User == "after-privacy-choice@example.test"
	}) {
		t.Fatal("re-enabling discovery reused the cached pre-consent login")
	}
}

func TestGlobalAuxiliaryEffortsOffAndRetrySettings(t *testing.T) {
	a := newGlobalParityHTTP(t)
	if err := provider.Save(provider.Provider{ID: "fixture", Name: "Fixture", Responses: a.vendor + "/v1/responses", Key: "fixture-private-key", Models: []string{"test-model"}, Searches: true}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("fixture/test-model", []string{"low", "high"}); err != nil {
		t.Fatal(err)
	}
	a.call("POST", "global/settings", map[string]any{"searcher": "fixture/test-model", "vision": "fixture/test-model", "searcherEffort": "high", "visionEffort": "low", "noLoopGuard": true, "refusalRetries": 0, "codexPredictions": "off"}, 200)
	var levels []string
	if err := json.Unmarshal(a.call("GET", "tools/efforts?model=fixture/test-model", nil, 200), &levels); err != nil || !slices.Equal(levels, []string{"low", "high"}) {
		t.Fatalf("selected model efforts: %v %v", levels, err)
	}
	var state struct {
		SearcherEffortNow, VisionEffortNow string
		SearcherFailed                     bool
		Settings                           settings.Settings
	}
	if err := json.Unmarshal(a.call("GET", "global", nil, 200), &state); err != nil {
		t.Fatal(err)
	}
	if state.SearcherEffortNow != "high" || state.VisionEffortNow != "low" || state.Settings.RefusalRetries == nil || *state.Settings.RefusalRetries != 0 || !state.Settings.NoLoopGuard || state.Settings.CodexPredictions != "off" || state.SearcherFailed {
		t.Fatalf("auxiliary settings did not reach runtime choices: %+v", state)
	}
	a.call("POST", "global/settings", map[string]any{"searcher": "off", "vision": "off", "refusalRetries": nil}, 200)
	if s := settings.Load(); s.Searcher != "off" || s.Vision != "off" || s.RefusalRetries != nil {
		t.Fatal("off/default-reset settings lost")
	}
	a.call("POST", "global/settings", map[string]any{"refusalRetries": -1}, 400)
	if settings.Load().RefusalRetries != nil {
		t.Fatal("negative retry limit changed saved policy")
	}
}

func TestGlobalLedgerAgentDatesRetryAndHeatmap(t *testing.T) {
	a := newGlobalParityHTTP(t)
	now := time.Now()
	day := now.Format(time.DateOnly)
	usage.Append(usage.Record{Time: now.Add(-time.Second), Agent: "dsh", Provider: "fixture", Model: "test-model", Input: 7, Status: 200, Tries: 2})
	usage.Append(usage.Record{Time: now.Add(-time.Second), Agent: "codex", Provider: "fixture", Model: "test-model", Input: 13, Status: 200})
	usage.Append(usage.Record{Time: now.Add(-time.Second), Agent: "dsh", Provider: "fixture", Model: "test-model", Input: 99, Status: 200, Source: usage.SourceReported})
	usage.Append(usage.Record{Time: now.AddDate(0, 0, -2), Agent: "dsh", Provider: "fixture", Model: "test-model", Input: 1000, Status: 200, Tries: 2})
	var page usageapi.Ledger
	if err := json.Unmarshal(a.call("GET", "requests?period="+day+".."+day+"&agent=dsh&retried=1", nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Calls != 1 || page.Retried != 1 || page.Input != 7 || page.Rows[0].AgentName != "鲸屿" {
		t.Fatalf("agent/date/retry/scope: %+v", page)
	}
	var heat usage.Heatmap
	if err := json.Unmarshal(a.call("GET", "requests/heatmap?agent=dsh&day="+day+"&retried=1", nil, 200), &heat); err != nil {
		t.Fatal(err)
	}
	if len(heat.Days) != 2 || heat.Days[0].Tokens != 1000 || heat.Days[1].Tokens != 7 {
		t.Fatalf("heatmap must keep other days and exclude external reported usage: %+v", heat)
	}
}
