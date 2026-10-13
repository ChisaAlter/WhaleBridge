package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/ChisaAlter/WhaleBridge/internal/agentenv"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Fixtures migrated from the fixed upstream contracts; every path is isolated.
func fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func signIn(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows: os.UserHomeDir reads this, not HOME
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	exp := float64(time.Now().Add(time.Hour).Unix())
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-1"}}),
			"access_token":  fakeJWT(map[string]any{"exp": exp}),
			"refresh_token": "r", "account_id": "acct-1",
		},
	})
	// Codex lists its models in its own cache; magpie reads that, nothing else.
	writeFile(t, filepath.Join(home, ".codex", "models_cache.json"), map[string]any{
		"models": []any{map[string]any{"slug": "gpt-5.5", "display_name": "GPT-5.5", "visibility": "list", "priority": 1}},
	})
	writeFile(t, filepath.Join(home, ".config", "github-copilot", "apps.json"), map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "octocat", "oauth_token": "gho_x"},
	})
	return home
}

func azureHome(t *testing.T) { t.Helper(); isolate(t) }
func fakeClaudeUsage(t *testing.T, out *atomic.Value, fail *atomic.Bool) *atomic.Int32 {
	t.Helper()
	var runs atomic.Int32
	claudeCLIUsage.RLock()
	old := claudeCLIUsage.f
	claudeCLIUsage.RUnlock()
	oldAsked := claudeAsked.Load()
	UsageClaudeVia(func(context.Context, string) (string, error) {
		runs.Add(1)
		if fail != nil && fail.Load() {
			return "", errors.New("Claude Code: network is unreachable")
		}
		return out.Load().(string), nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("magpie asked Anthropic itself: %s", r.URL)
	}))
	oldBase, oldWait, oldUsed := claudeBase, claudeUsageWait, claudeUsedSince
	claudeBase = srv.URL
	claudeUsageWait = func() time.Duration { return usageTestWait }
	claudeUsedSince = func(time.Time) bool { return true }
	claudeAsked.Store(0)
	claudeUsage.Lock()
	claudeUsage.m = nil
	claudeUsage.Unlock()
	t.Cleanup(func() {
		UsageClaudeVia(old)
		claudeBase, claudeUsageWait, claudeUsedSince = oldBase, oldWait, oldUsed
		claudeAsked.Store(oldAsked)
		srv.Close()
		claudeUsage.Lock()
		claudeUsage.m = nil
		claudeUsage.Unlock()
		subscriptionUsageCache.Lock()
		subscriptionUsageCache.asked = false
		subscriptionUsageCache.Unlock()
	})
	return &runs
}

const usageTestWait = 7 * time.Minute

func claudeThree(t *testing.T) (prof func(email, org string), creds func(tok, plan string)) {
	t.Helper()
	isolate(t)
	home, _ := os.UserHomeDir()
	cred := filepath.Join(home, ".claude", ".credentials.json")
	profile := filepath.Join(home, ".claude.json")
	prof = func(email, org string) {
		writeFile(t, profile, map[string]any{"oauthAccount": map[string]any{"emailAddress": email, "organizationUuid": "org-" + email, "organizationName": org}})
	}
	creds = func(tok, plan string) {
		writeFile(t, cred, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "tok-" + tok, "refreshToken": "r-" + tok, "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": plan}})
	}
	for _, a := range [][3]string{{"team@x.com", "team", "Acme"}, {"max@x.com", "max", "max@x.com's Organization"}, {"pro@x.com", "pro", "pro@x.com's Organization"}} {
		prof(a[0], a[2])
		creds(a[0], a[1])
		forgetAccountCaches()
		rememberLogins(true)
	}
	return prof, creds
}

const fiveHours = 5 * time.Hour

func win(name string, span time.Duration, used float64, resets time.Time) QuotaWindow {
	w := QuotaWindow{Name: name, Used: used, Span: span}
	if !resets.IsZero() {
		w.ResetsAt = &resets
	}
	return w
}

type fakeWarm struct {
	now  time.Time
	ws   map[string][]QuotaWindow
	errs map[string]error
	sent []string
}

func (f *fakeWarm) warmer(path string) codexWarmer {
	return codexWarmer{path: path, now: func() time.Time { return f.now },
		usage: func(context.Context) map[string]SubscriptionQuota {
			out := map[string]SubscriptionQuota{}
			for u, ws := range f.ws {
				out[u] = SubscriptionQuota{Provider: "codex", User: u, Windows: ws}
			}
			return out
		},
		send: func(_ context.Context, user string) error {
			f.sent = append(f.sent, user)
			return f.errs[user]
		}}
}

var east8 = time.FixedZone("UTC+8", 8*60*60)

func at8(day, h, m int) time.Time { return time.Date(2026, 9, day, h, m, 0, 0, east8) }
func times(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
func (f *fakeWarm) runAt(t *testing.T, path, which, at string) []CodexWarm {
	t.Helper()
	return f.warmer(path).warmNow(t.Context(), which, times(at))
}

type fakeWBCheckin struct {
	mu        sync.Mutex
	active    bool
	checked   bool
	claimCode int
	down      bool // the network: every request fails
	broken    bool // every request is answered with a 500
	// refuse401 is the paths WorkBuddy's gateway answers with its bare
	// 401 page; "*" is every one (a token it refuses)
	refuse401 []string
	summaries int
	statuses  int
	claims    int
}

func (f *fakeWBCheckin) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			c.Close()
			return
		}
		if f.broken {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if slices.Contains(f.refuse401, "*") || slices.Contains(f.refuse401, r.URL.Path) {
			switch r.URL.Path {
			case "/v2/billing/meter/checkin-activity-status":
				f.statuses++
			case "/billing/meter/get-user-resource-summary":
				f.summaries++
			}
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, wbGateway401)
			return
		}
		if r.Header.Get("Authorization") != "Bearer wb-access" || r.Header.Get("X-User-Id") != "u1" ||
			!strings.HasPrefix(r.Header.Get("User-Agent"), "WorkBuddy/") {
			json.NewEncoder(w).Encode(map[string]any{"code": 10085, "msg": "请求不合法"})
			return
		}
		ok := func(data any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok", "data": data}) }
		switch r.URL.Path {
		case "/v2/billing/meter/checkin-activity-status":
			f.statuses++
			ok(map[string]any{"active": f.active, "today_checked_in": f.checked, "streak_days": 3,
				"daily_credit": 100, "today_credit": map[bool]int{true: 100}[f.checked], "total_credits": 300})
		case "/billing/meter/get-user-resource-summary":
			f.summaries++
			ok(map[string]any{"Packages": []any{map[string]any{"CycleTotalCapacity": "1500", "CycleUsedCapacity": "0"}}})
		case "/v2/billing/meter/daily-checkin":
			f.claims++
			if f.claimCode != 0 {
				json.NewEncoder(w).Encode(map[string]any{"code": f.claimCode, "msg": "今日已签到"})
				return
			}
			f.checked = true
			ok(map[string]any{"credit": 100, "streak_days": 4, "is_streak_day": true})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// wbGateway401 is what copilot.tencent.com's gateway (APISIX) answers a
// token it refuses with, as captured on 2026-10-10.
const wbGateway401 = `<html>
<head><title>401 Authorization Required</title></head>
<body>
<center><h1>401 Authorization Required</h1></center>
<hr><center>openresty</center>
<p><em>Powered by <a href="https://apisix.apache.org/">APISIX</a>.</em></p></body>
</html>
`

func (f *fakeWBCheckin) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statuses, f.claims
}

// wbCheckinFixture is a checkiner for one WorkBuddy account at srv, its
// clock at *now, its file in a temp folder.
func wbCheckinFixture(t *testing.T, srv *httptest.Server, now *time.Time) wbCheckiner {
	t.Helper()
	wbTokens.Lock()
	wbTokens.m = map[string]wbCreds{}
	wbTokens.Unlock()
	endpoint := srv.URL
	site := &wbSite{id: "workbuddy", name: "WorkBuddy", endpoint: &endpoint}
	future := time.Now().Add(24 * time.Hour).UnixMilli()
	a := wbAccount{Login: Login{User: "旅行者", On: true, Active: true}, site: site, own: true,
		creds: wbCreds{UID: "u1", Access: "wb-access", ExpiresAt: future, Domain: "www.codebuddy.cn"}}
	return wbCheckiner{
		path:     filepath.Join(t.TempDir(), "workbuddy-checkin.json"),
		now:      func() time.Time { return *now },
		accounts: func() []wbAccount { return []wbAccount{a} },
	}
}

func cardOf(cs []SubscriptionQuota, provider, user string) *SubscriptionQuota {
	for i := range cs {
		if cs[i].Provider == provider && cs[i].User == user {
			return &cs[i]
		}
	}
	return nil
}

func wbAuthFile(home string) string {
	base := filepath.Join(home, ".local", "share")
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support")
	case "windows":
		base = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(base, "CodeBuddyExtension", "Data", "Public", "auth", "workbuddy-desktop.info")
}

func detectHome(t *testing.T) {
	t.Helper()
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
}

func keyLimitsHome(t *testing.T) {
	t.Helper()
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	restart := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
		ForgetBalances()
		keyAllowances.Lock()
		keyAllowances.m = nil
		keyAllowances.Unlock()
	}
	restart()
	t.Cleanup(func() {
		// the reads behind the test, and the sign-ins they look up, end
		// before the next test's isolate changes what they call
		deadline := time.Now().Add(10 * time.Second)
		for busy := true; busy && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			busy = false
			keyAllowances.Lock()
			for _, e := range keyAllowances.m {
				busy = busy || e.loading
			}
			keyAllowances.Unlock()
			devinStatus.Lock()
			busy = busy || devinStatus.refreshing
			devinStatus.Unlock()
		}
		restart()
	})
}

func sub2apiServer(t *testing.T, body func() ([]byte, int)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" {
			t.Errorf("asked %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-limited" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"type":"authentication_error","message":"Invalid API key"}}`))
			return
		}
		asked.Add(1)
		b, status := body()
		w.WriteHeader(status)
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func sub2apiKey(t *testing.T, srv *httptest.Server) Provider {
	return Provider{ID: "sub2api-" + Slug(t.Name()), Name: "Sub2API", Chat: srv.URL + "/v1", Responses: srv.URL + "/v1",
		Key: "sk-limited", BalanceURL: srv.URL + "/v1/usage"}
}

func claudeHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Windows finds the home in USERPROFILE
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	forgetClaudeCredential()
	return home
}

func claudeSignIn(t *testing.T, home string, expiry time.Time) string {
	t.Helper()
	path := filepath.Join(home, ".claude", ".credentials.json")
	writeFile(t, path, map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": "sk-ant-oat01-old", "refreshToken": "sk-ant-ort01-old",
			"expiresAt": expiry.UnixMilli(), "subscriptionType": "max",
			"scopes": []string{"user:inference", "user:profile"},
		},
		// what magpie must not clobber when it writes a refreshed token back
		"mcpOAuth": map[string]any{"plugin:slack:slack|x": map[string]any{"serverName": "Slack"}},
	})
	return path
}

func saveLogins(t *testing.T, ls ...savedLogin) {
	t.Helper()
	loginsMu.Lock()
	defer loginsMu.Unlock()
	if err := writeLogins(ls); err != nil {
		t.Fatal(err)
	}
}
