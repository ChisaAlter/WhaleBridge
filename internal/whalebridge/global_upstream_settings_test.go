package whalebridge

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// Settings upstream keeps outside its window (settings.json, CLI) or on pages
// the component lacks reach the component's own pages.
func TestUpstreamSettingsReachComponentPagesHTTP(t *testing.T) {
	a := newProviderHTTP(t)

	// Global › Connection: private hosts a URL the gateway didn't choose may
	// be downloaded from, checked as the gateway reads them.
	a.call("POST", "global/settings", map[string]any{"fetchPrivate": []string{" 192.168.1.20 ", "10.0.0.0/8", "NAS.local:8080", "nas.local:8080", "[::1]"}}, 200)
	if got := settings.Load().FetchPrivate; !slices.Equal(got, []string{"192.168.1.20", "10.0.0.0/8", "nas.local:8080", "[::1]"}) {
		t.Fatalf("fetchPrivate = %q", got)
	}
	var global struct{ Settings map[string]any }
	if err := json.Unmarshal(a.call("GET", "global", nil, 200), &global); err != nil {
		t.Fatal(err)
	}
	if list, _ := global.Settings["fetchPrivate"].([]any); len(list) != 4 {
		t.Fatalf("global state lacks fetchPrivate: %v", global.Settings["fetchPrivate"])
	}
	for _, bad := range []string{"http://nas.local", "nas.local:0", "nas local", "nas.local/share"} {
		a.call("POST", "global/settings", map[string]any{"fetchPrivate": []string{bad}}, 400)
	}
	if len(settings.Load().FetchPrivate) != 4 {
		t.Fatal("a refused fetchPrivate changed the saved list")
	}

	// Accounts: at which of the two times a Codex reset is spent by itself.
	when := func(v string, week, expiring bool) {
		t.Helper()
		a.call("POST", "accounts/settings", map[string]any{"id": "codex", "user": "a@example.invalid", "codexAutoResetWhen": v}, 200)
		if w, e := provider.CodexAutoResetWhen("a@example.invalid"); w != week || e != expiring {
			t.Fatalf("%s: week %v expiring %v", v, w, e)
		}
	}
	when("both", true, true)
	when("week", true, false)
	when("expiring", false, true)
	when("off", false, false)
	a.call("POST", "accounts/settings", map[string]any{"id": "codex", "user": "a@example.invalid", "codexAutoResetWhen": "sometimes"}, 400)

	// Provider editor: a model that takes no reasoning ask.
	id := a.save(map[string]any{"new": true, "id": "thinker", "name": "Thinker", "chat": a.vendor + "/v1", "key": "fixture-key", "models": []string{"test-model"},
		"modelPrefs": map[string]any{"test-model": map[string]any{"reasonless": true}}})
	if !provider.Reasonless(id, "test-model") {
		t.Fatal("reasonless pref did not persist")
	}
	a.save(map[string]any{"id": id, "modelPrefs": map[string]any{"test-model": map[string]any{"reasonless": false}}})
	if provider.Reasonless(id, "test-model") {
		t.Fatal("reasonless pref did not clear")
	}

	// Adapters: only an option a plugin declares is kept.
	a.call("POST", "subscription/adapter-option", map[string]any{"provider": "no-such-plugin", "key": "beta", "on": true}, 400)
	if len(settings.Load().PluginOptions) != 0 {
		t.Fatal("an undeclared plugin option was saved")
	}
}

// Agents: a fast pick's tier (Ultrafast where a Codex account offers it).
func TestAgentFastTierHTTP(t *testing.T) {
	a := newAgentHTTP(t)
	a.call("POST", "agent/connect", map[string]string{"id": "claude"}, http.StatusOK)
	if err := provider.Save(provider.Provider{ID: "fast", Name: "Fast relay", Key: "fixture", Chat: a.vendor + "/v1", Models: []string{"gpt-6.1-sol"}}); err != nil {
		t.Fatal(err)
	}
	tier := func(v string, status int) {
		t.Helper()
		a.call("POST", "agent-fast", map[string]any{"id": "claude", "for": "claude", "ref": "fast/gpt-6.1-sol", "tier": v}, status)
	}
	tier("priority", http.StatusOK)
	if !provider.IsFastPick("claude", "fast/gpt-6.1-sol") || provider.IsUltraPick("claude", "fast/gpt-6.1-sol") {
		t.Fatal("priority tier did not set a plain fast pick")
	}
	tier("ultrafast", http.StatusBadRequest) // no Codex account offers the tier here
	tier("turbo", http.StatusBadRequest)
	tier("", http.StatusOK)
	if provider.IsFastPick("claude", "fast/gpt-6.1-sol") {
		t.Fatal("the usual tier did not clear the fast pick")
	}

}
