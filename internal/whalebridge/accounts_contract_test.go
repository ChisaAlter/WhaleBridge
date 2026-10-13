package whalebridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

func componentContractWriteJSON(t *testing.T, path string, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return b
}

func componentClaudeFixture(t *testing.T, h providerHTTP) (string, string, []byte) {
	t.Helper()
	home, _ := os.UserHomeDir()
	current, saved := "current@example.invalid", "saved@example.invalid"
	auth := func(token string) map[string]any {
		return map[string]any{"claudeAiOauth": map[string]any{"accessToken": token, "refreshToken": token + "-refresh", "expiresAt": time.Now().Add(8 * time.Hour).UnixMilli(), "subscriptionType": "max", "scopes": []string{"user:inference", "user:profile"}}}
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	before := componentContractWriteJSON(t, path, auth("fixture-current-token"))
	componentContractWriteJSON(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": current}})
	componentContractWriteJSON(t, filepath.Join(filepath.Dir(provider.Path()), "logins.json"), []map[string]any{{"agent": "claude", "user": saved, "plan": "max", "on": true, "owned": true, "auth": auth("fixture-saved-token"), "profile": map[string]any{"emailAddress": saved}}})
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	return saved, path, before
}

func TestAccountAliasAndWindowCapsContractHTTP(t *testing.T) {
	h := newProviderHTTP(t)
	saved, native, before := componentClaudeFixture(t, h)
	h.call("POST", "accounts/name", map[string]any{"id": "claude", "user": saved, "alias": "  Research   seat  "}, 200)
	h.call("POST", "accounts/cap", map[string]any{"id": "claude", "user": saved, "cap": 70}, 200)
	h.call("POST", "accounts/windowcap", map[string]any{"id": "claude", "user": saved, "window": "  Weekly  ", "cap": 40}, 200)
	h.call("POST", "accounts/windowcap", map[string]any{"id": "claude", "user": saved, "window": "5 hours", "cap": 100}, 200)
	if provider.AccountNameOf("claude", saved) != "Research seat" {
		t.Fatal("account alias did not persist under the real account")
	}
	caps := provider.AccountCapsOf("claude", saved)
	if c, _ := caps.Of("weekly"); c != 40 {
		t.Fatal("the named window did not override the account cap")
	}
	if c, _ := caps.Of("5 hours"); c != 0 {
		t.Fatal("100 did not remove a cap on only that window")
	}
	var rows []accountInfo
	if err := json.Unmarshal(h.call("GET", "accounts/claude", nil, 200), &rows); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.User == saved {
			found = row.Alias == "Research seat"
		}
	}
	if !found {
		t.Fatal("the safe account list omitted its alias")
	}
	h.call("POST", "accounts/windowcap", map[string]any{"id": "claude", "user": saved, "window": "weekly", "cap": 0}, 200)
	if c, _ := provider.AccountCapsOf("claude", saved).Of("weekly"); c != 70 {
		t.Fatal("resetting a window did not follow the retained account cap")
	}
	h.call("POST", "accounts/windowcap", map[string]any{"id": "claude", "user": saved, "window": "weekly", "cap": 101}, 400)
	h.call("POST", "accounts/name", map[string]any{"id": "claude", "user": saved, "alias": strings.Repeat("x", provider.MaxAccountName+1)}, 400)
	if provider.AccountNameOf("claude", saved) != "Research seat" {
		t.Fatal("an invalid alias replaced the saved value")
	}
	if got, err := os.ReadFile(native); err != nil || !bytes.Equal(got, before) {
		t.Fatal("component account policies changed the native sign-in")
	}
	assertSecretsAbsent(t, h.call("GET", "state", nil, 200), "fixture-saved-token", "fixture-current-token")
}

func TestAccountSavedClaudeExplicitRefreshHTTP(t *testing.T) {
	h := newProviderHTTP(t)
	saved, native, before := componentClaudeFixture(t, h)
	h.call("POST", "accounts/name", map[string]any{"id": "claude", "user": saved, "alias": "Refresh seat"}, 200)
	served := time.Now().UTC().Add(-time.Minute)
	provider.NoteServed(provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: saved}}, served)
	var savedRuns atomic.Int32
	root := filepath.Join(filepath.Dir(provider.Path()), "claude-accounts")
	provider.UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		if dir != "" {
			if !strings.HasPrefix(dir, root+string(filepath.Separator)) {
				return "", fmt.Errorf("saved usage ran outside the component account directory")
			}
			b, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
			if err != nil || !bytes.Contains(b, []byte("fixture-saved-token")) {
				return "", fmt.Errorf("saved usage received a different account")
			}
			savedRuns.Add(1)
		}
		return "Current session: 23% used\nCurrent week (all models): 37% used", nil
	})
	t.Cleanup(func() { provider.UsageClaudeVia(nil) })
	h.call("GET", "accounts/claude/usage", nil, 200)
	if savedRuns.Load() != 0 {
		t.Fatal("ordinary account usage read started a saved Claude account")
	}
	var cards map[string]provider.SubscriptionQuota
	response := h.call("GET", "accounts/claude/usage?refresh=true", nil, 200)
	if err := json.Unmarshal(response, &cards); err != nil {
		t.Fatal(err)
	}
	q := cards[saved]
	if savedRuns.Load() != 1 || q.Error != "" || len(q.Windows) != 2 || q.Windows[0].Used != 23 || q.Windows[1].Used != 37 || q.ReadAt == nil {
		t.Fatalf("explicit refresh did not read the saved account: runs=%d, windows=%d, error=%s", savedRuns.Load(), len(q.Windows), q.Error)
	}
	if q.Alias != "Refresh seat" || q.LastServedAt == nil || !q.LastServedAt.Equal(served) {
		t.Fatal("account usage lost the saved alias or last service time")
	}
	h.call("GET", "accounts/claude/usage", nil, 200)
	if savedRuns.Load() != 1 {
		t.Fatal("ordinary polling ran the saved account again")
	}
	if got, err := os.ReadFile(native); err != nil || !bytes.Equal(got, before) {
		t.Fatal("saved-account refresh replaced Claude Code's own credential")
	}
	assertSecretsAbsent(t, response, "fixture-saved-token", "fixture-current-token")
}
