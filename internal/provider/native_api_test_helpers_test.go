package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/agentenv"
	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
)

// TestMain uses the existing upstream sandbox before Accounts can discover
// anything, and allows only loopback HTTP fixtures. Component storage also
// stays in this home rather than an inherited launcher data directory.
func TestMain(m *testing.M) {
	os.Exit(testenv.RunIn(m, func(home string) {
		testenv.Offline()
		for name, value := range map[string]string{
			"LAUNCHER_COMPONENT_ID":          "whalebridge",
			"LAUNCHER_COMPONENT_DATA_DIR":    "",
			"GOOGLE_APPLICATION_CREDENTIALS": "",
			"CLOUDSDK_CONFIG":                "",
		} {
			if err := os.Setenv(name, value); err != nil {
				panic(err)
			}
		}
		kiroCLIDB = func() string { return filepath.Join(home, "kiro-test", "kiro-cli", "data.sqlite3") }
		kiroIDEDir = func() string { return filepath.Join(home, "kiro-test", "sso") }
		askKiroIdentity = func(string, string) (string, string) { return "", "" }
		copilotCLISecret = func(string) string { return "" }
	}))
}

// isolate is the focused tests' upstream account fixture, adapted to the
// caches available in this component. No real keychain or agent CLI is used.
func isolate(t *testing.T) {
	t.Helper()
	home := parityHome(t)
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", filepath.Join(home, "component"))
	for _, name := range agentenv.Vars {
		t.Setenv(name, "")
	}
	oldKeychain, oldCursor := claudeKeychain, cursorKeychain
	claudeKeychain, cursorKeychain = false, false
	forgetDevinStatus()
	copilotMu.Lock()
	copilotSessions = map[string]copilotSession{}
	copilotMu.Unlock()
	forgetPlanQuotas()
	ForgetBalances()
	t.Cleanup(func() {
		claudeKeychain, cursorKeychain = oldKeychain, oldCursor
		forgetDevinStatus()
		ForgetAccounts()
	})
}

func writeFile(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func modelIDs(ms []catalog.Model) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

// rewrite sends every request to srv, keeping its path, as if srv were
// the vendor's host. This is the upstream plan-quota fixture.
type rewrite struct{ srv *httptest.Server }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	u := *req.URL
	u.Scheme, u.Host = "http", strings.TrimPrefix(r.srv.URL, "http://")
	req = req.Clone(req.Context())
	req.Header.Set("X-Host", req.URL.Host)
	req.URL, req.Host = &u, ""
	return http.DefaultTransport.RoundTrip(req)
}
