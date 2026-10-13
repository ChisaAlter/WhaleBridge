package whalebridge

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
	"github.com/ChisaAlter/WhaleBridge/internal/usage"
)

func TestEmptyAdapterDiscoveryDoesNotStartRuntime(t *testing.T) {
	h := newProviderHTTP(t)
	if len(plugin.Load().Plugins) != 0 || plugin.Running() {
		t.Fatal("expected an empty, stopped adapter environment")
	}
	hadBun := plugin.HasBun()
	var result struct {
		Installed []json.RawMessage `json:"installed"`
		Available []json.RawMessage `json:"available"`
		Error     string            `json:"error"`
	}
	if err := json.Unmarshal(h.call("GET", "subscription/adapters", nil, 200), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Installed) != 0 || len(result.Available) == 0 || result.Error != "" {
		t.Fatalf("empty discovery lost its catalog or attempted runtime loading: %+v", result)
	}
	if plugin.Running() || plugin.HasBun() != hadBun {
		t.Fatal("browsing adapters started or downloaded a runtime")
	}
}

func TestSubscriptionMultipleWarmTimesContractHTTP(t *testing.T) {
	h := newProviderHTTP(t)
	h.call("POST", "subscription/settings", map[string]any{"codexWarmAts": []string{"17:30", "09:00", "09:00"}, "claudeWarmAts": []string{"08:20", "18:20"}}, 200)
	s := settings.Load()
	if !reflect.DeepEqual(s.CodexWarmAts, []string{"09:00", "17:30"}) || !reflect.DeepEqual(s.ClaudeWarmAts, []string{"08:20", "18:20"}) || s.CodexWarmAt != "09:00" {
		t.Fatal("multiple daily times were not normalized and persisted")
	}
	h.call("POST", "subscription/settings", map[string]any{"usageAlert": 20}, 200)
	if !reflect.DeepEqual(settings.Load().CodexWarmAts, s.CodexWarmAts) {
		t.Fatal("unrelated maintenance save lost daily times")
	}
	h.call("POST", "subscription/settings", map[string]any{"codexWarmAt": "09:00", "codexWarmAts": []string{}}, 200)
	if now := settings.Load(); len(now.CodexWarmAts) != 0 || now.CodexWarmAt != "" {
		t.Fatal("empty array did not clear the legacy time too")
	}
	h.call("POST", "subscription/settings", map[string]any{"claudeWarmAt": "11:25"}, 200)
	if !reflect.DeepEqual(settings.Load().ClaudeWarmAts, []string{"11:25"}) {
		t.Fatal("a legacy single-time edit retained the old array")
	}
	before := settings.Load()
	h.call("POST", "subscription/settings", map[string]any{"codexWarmAts": []string{"25:70"}}, 400)
	if !reflect.DeepEqual(before.CodexWarmAts, settings.Load().CodexWarmAts) {
		t.Fatal("invalid daily time changed the saved schedule")
	}
	var dto struct{ CodexWarmAts, ClaudeWarmAts []string }
	if err := json.Unmarshal(h.call("GET", "subscription/settings", nil, 200), &dto); err != nil || !reflect.DeepEqual(dto.ClaudeWarmAts, []string{"11:25"}) {
		t.Fatal("maintenance DTO lost the normalized array")
	}
}

func TestSubscriptionPerAccountCheckinContractHTTP(t *testing.T) {
	h := newProviderHTTP(t)
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".local", "share")
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support")
	case "windows":
		base = filepath.Join(home, "AppData", "Local")
	}
	native := filepath.Join(base, "CodeBuddyExtension", "Data", "Public", "auth", "workbuddy-desktop.info")
	before := componentContractWriteJSON(t, native, map[string]any{
		"account": map[string]any{"uid": "fixture", "nickname": "Fixture seat"},
		"auth":    map[string]any{"accessToken": "fixture-workbuddy-access", "refreshToken": "fixture-workbuddy-refresh", "expiresAt": time.Now().Add(8 * time.Hour).UnixMilli(), "domain": "www.codebuddy.cn"},
	})
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	s := settings.Load()
	s.WorkBuddyCheckin, s.CheckinOff = true, []string{"workbuddy|other"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		h.call("POST", "accounts/checkin", map[string]any{"key": "workbuddy|fixture", "on": false}, 200)
	}
	if now := settings.Load(); !reflect.DeepEqual(now.CheckinOff, []string{"workbuddy|other", "workbuddy|fixture"}) || !now.WorkBuddyCheckin {
		t.Fatal("one account's exclusion changed the vendor or another account")
	}
	var accounts []accountInfo
	response := h.call("GET", "accounts/workbuddy", nil, 200)
	if err := json.Unmarshal(response, &accounts); err != nil || len(accounts) != 1 || accounts[0].User != "Fixture seat" || accounts[0].CheckinKey != "workbuddy|fixture" || !accounts[0].CheckinOff {
		t.Fatal("the real account DTO lost its resolved per-account check-in switch")
	}
	assertSecretsAbsent(t, response, "fixture-workbuddy-access", "fixture-workbuddy-refresh")
	h.call("POST", "subscription/settings", map[string]any{"resetReminder": 10}, 200)
	if len(settings.Load().CheckinOff) != 2 {
		t.Fatal("an unrelated maintenance edit erased account exclusions")
	}
	h.call("POST", "accounts/checkin", map[string]any{"id": "workbuddy", "user": "Fixture seat", "on": true}, 200)
	if !reflect.DeepEqual(settings.Load().CheckinOff, []string{"workbuddy|other"}) {
		t.Fatal("re-enabling one account changed another exclusion")
	}
	h.call("POST", "accounts/checkin", map[string]any{"key": "workbuddy|fixture"}, 400)
	h.call("POST", "accounts/checkin", map[string]any{"on": false}, 400)
	if got, err := os.ReadFile(native); err != nil || !bytes.Equal(got, before) {
		t.Fatal("the per-account check-in switch changed the native sign-in")
	}
}

func TestSubscriptionQuotaMetadataAndWindowHoldsContract(t *testing.T) {
	newProviderHTTP(t)
	user := "metadata@example.invalid"
	componentContractWriteJSON(t, provider.Path(), map[string]any{"providers": []provider.Provider{{ID: "codex", AccountNames: map[string]string{user: "Research seat"}}}})
	at := time.Now().UTC()
	spent := provider.ResetUse{User: user, At: at.Add(-time.Hour), Why: provider.ResetUsedByHand, Windows: 1}
	componentContractWriteJSON(t, filepath.Join(settings.Dir(), "codex-resets-used.json"), []provider.ResetUse{spent})
	served := at.Add(-time.Minute)
	provider.NoteServed(provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: user}}, served)
	// A first call before this window begins establishes that the ledger
	// covers the whole period; only the later account call is counted.
	usage.Append(usage.Record{Time: at.Add(-6 * time.Hour), Agent: "codex", Provider: "codex", ProviderAccount: "other@example.invalid", Model: "gpt-fixture", Input: 1})
	usage.Append(usage.Record{Time: at.Add(-time.Hour), Agent: "codex", Provider: "codex", ProviderAccount: user, Model: "gpt-fixture", Input: 100, Output: 20})
	reset := at.Add(time.Hour)
	input := []provider.SubscriptionQuota{
		{Provider: "codex", User: user, NeedsAccessKey: true, Seat: "Existing seat", ReadAt: &at, Windows: []provider.QuotaWindow{{Name: "5 hours", Used: 50, ResetsAt: &reset, Span: 5 * time.Hour}}},
		{Provider: "codex", User: user, From: "Other bridge", Alias: "Remote alias", Seat: "Remote seat", Windows: []provider.QuotaWindow{}},
	}
	out := componentQuotaCards(input)
	q := out[0]
	if q.Alias != "Research seat" || q.Seat != "Existing seat" || !q.NeedsAccessKey || q.LastServedAt == nil || !q.LastServedAt.Equal(served) || len(q.ResetsUsed) != 1 || q.ResetsUsed[0].Why != provider.ResetUsedByHand {
		t.Fatal("the component lost live quota display metadata")
	}
	holds := q.Windows[0].Holds
	if holds == nil || holds.Tokens != 240 || holds.Routed.Tokens != 120 || holds.Routed.Calls != 1 {
		t.Fatalf("the component did not decorate windows from the actual local ledger: %+v", holds)
	}
	if input[0].Alias != "" || input[0].LastServedAt != nil || input[0].Windows[0].Holds != nil {
		t.Fatal("quota decoration changed the caller's cached reading")
	}
	if remote := out[1]; remote.Alias != "Remote alias" || remote.Seat != "Remote seat" || remote.LastServedAt != nil || len(remote.ResetsUsed) != 0 {
		t.Fatal("local display metadata replaced the remote account's values")
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"needsAccessKey", "resetsUsed", "alias", "seat", "lastServedAt", "holds"} {
		if !bytes.Contains(b, []byte(`"`+field+`"`)) {
			t.Fatalf("the quota response omitted %s", field)
		}
	}
}

func TestAdapterInstallReportsFailureInRequestedFormat(t *testing.T) {
	h := newProviderHTTP(t)
	for _, accept := range []string{"application/json", "application/x-ndjson"} {
		req, err := http.NewRequest("POST", h.url+"/api/subscription/adapter", strings.NewReader(`{"action":"install","package":"invalid package"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", accept)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if accept == "application/json" {
			if res.StatusCode != 400 || !strings.Contains(string(body), "isn't an npm package name") {
				t.Fatalf("JSON failure: %d %s", res.StatusCode, body)
			}
			continue
		}
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/x-ndjson" {
			t.Fatalf("stream headers: %d %v", res.StatusCode, res.Header)
		}
		dec := json.NewDecoder(strings.NewReader(string(body)))
		var phase, failure map[string]any
		if err := dec.Decode(&phase); err != nil || phase["phase"] != "downloading" {
			t.Fatalf("download phase: %v %v", phase, err)
		}
		if err := dec.Decode(&failure); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(failure["error"].(string), "isn't an npm package name") {
			t.Fatalf("failure: %v", failure)
		}
		var extra map[string]any
		if err := dec.Decode(&extra); err != io.EOF {
			t.Fatalf("unexpected completion after failure: %v %v", extra, err)
		}
	}
}
