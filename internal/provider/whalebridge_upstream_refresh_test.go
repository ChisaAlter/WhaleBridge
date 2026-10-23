package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/netproxy"
	"github.com/ChisaAlter/WhaleBridge/internal/steady"
)

type upstreamRefreshTransport func(*http.Request) (*http.Response, error)

func (f upstreamRefreshTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func upstreamObserveRefresh(t *testing.T, observe func(*http.Request)) {
	t.Helper()
	old := http.DefaultClient
	client := *old
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = upstreamRefreshTransport(func(r *http.Request) (*http.Response, error) {
		observe(r)
		return base.RoundTrip(r)
	})
	http.DefaultClient = &client
	t.Cleanup(func() { http.DefaultClient = old })
}

// A component-owned account must keep the rotated tokens even if the
// request that started its refresh is cancelled. Native credentials stay
// byte-identical, and an unrelated caller value survives the detached context.
func TestWhaleBridgeUpstreamCodexRefreshOutlivesCaller(t *testing.T) {
	isolate(t)
	const who = "owned@example.test"
	native := parityAuth(t, "native@example.test", "native")
	nativePath := codexAuthPath()
	writeFile(t, nativePath, native)
	saveLogins(t, savedLogin{Agent: "codex", User: who, Owned: true, On: true, Auth: parityAuth(t, who, "owned")})

	arrived, release := make(chan struct{}), make(chan struct{})
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		close(arrived)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": body["refresh_token"] + "-rotated",
		})
	}))
	t.Cleanup(fake.Close)
	var released bool
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})
	t.Cleanup(CodexTokenURLForTest(fake.URL))
	type traceKey struct{}
	var requests atomic.Int32
	upstreamObserveRefresh(t, func(r *http.Request) {
		requests.Add(1)
		if r.Context().Value(traceKey{}) != "keep-me" {
			t.Error("refresh lost its caller's context values")
		}
		deadline, ok := r.Context().Deadline()
		if left := time.Until(deadline); !ok || left <= 25*time.Second || left > 30*time.Second {
			t.Errorf("refresh deadline is not its own 30-second bound: %v (%v)", deadline, ok)
		}
	})
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), traceKey{}, "keep-me"), time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := renewSavedLogin(ctx, "codex", who, true)
		done <- err
	}()
	select {
	case <-arrived:
	case err := <-done:
		t.Fatalf("refresh ended before reaching the fixture: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not reach the fixture")
	}
	cancel()
	close(release)
	released = true
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not finish after the fixture answered")
	}
	ls := readLogins()
	if len(ls) != 1 || !ls[0].Owned || ls[0].Lapsed != "" || ls[0].Renewed.IsZero() {
		t.Fatalf("owned account changed ownership or was not renewed: %+v", ls)
	}
	var saved codexAuth
	if err := json.Unmarshal(ls[0].Auth, &saved); err != nil || saved.Tokens.RefreshToken != "refresh-owned-rotated" {
		t.Fatalf("rotated refresh token did not reach the saved account: %v", err)
	}
	if got, err := os.ReadFile(nativePath); err != nil || !bytes.Equal(bytes.TrimSpace(got), native) {
		t.Fatalf("renewing the owned account changed native credentials: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("sent %d refreshes, want one", requests.Load())
	}
}

func TestWhaleBridgeUpstreamCodexRefreshAlreadyCancelledSendsNothing(t *testing.T) {
	isolate(t)
	var requests atomic.Int32
	upstreamObserveRefresh(t, func(*http.Request) { requests.Add(1) })
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "deadline exceeded"}[expired], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if expired {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
				want = context.DeadlineExceeded
			}
			cancel()
			raw := map[string]any{"tokens": map[string]any{"access_token": "old", "refresh_token": "unspent"}, "keep": true}
			before, _ := json.Marshal(raw)
			if _, err := codexRefresh(ctx, raw); !errors.Is(err, want) {
				t.Fatalf("refresh returned %v, want %v", err, want)
			}
			after, _ := json.Marshal(raw)
			if !bytes.Equal(before, after) {
				t.Fatal("cancelled refresh modified its credential blob")
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("an already-cancelled caller reached the HTTP transport %d times", requests.Load())
	}
}

func TestWhaleBridgeUpstreamCodexRefreshKeepsProxyValues(t *testing.T) {
	isolate(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"rotated"}`))
	}))
	t.Cleanup(fake.Close)
	t.Cleanup(CodexTokenURLForTest(fake.URL))
	upstreamObserveRefresh(t, func(r *http.Request) {
		if got := netproxy.Choice(r.Context()); got != "direct" {
			t.Errorf("refresh lost its provider proxy choice: %q", got)
		}
	})
	raw := map[string]any{"tokens": map[string]any{"refresh_token": "old"}}
	if _, err := codexRefresh(netproxy.With(t.Context(), "direct"), raw); err != nil {
		t.Fatal(err)
	}
}

func upstreamDueCodexAuth(t *testing.T) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".codex", "auth.json")
	writeFile(t, path, map[string]any{
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token": "private-old-id", "access_token": fakeJWT(map[string]any{"exp": time.Now().Add(time.Minute).Unix()}),
			"refresh_token": "private-old-refresh", "account_id": "acct-fixture",
		},
		"last_refresh": "previous", "other": map[string]any{"unchanged": true},
	})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, before
}

func upstreamNoCredentialTemps(t *testing.T, path string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".*"))
	if err != nil || len(left) != 0 {
		t.Fatalf("temporary credential files left behind: %v (%v)", left, err)
	}
	fi, err := os.Stat(path)
	if err != nil || os.PathSeparator == '/' && fi.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode is not private: %v (%v)", fi, err)
	}
}

func TestWhaleBridgeUpstreamCodexCredentialWriteIsDurable(t *testing.T) {
	isolate(t)
	freshAccess := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": freshAccess, "refresh_token": "private-fresh-refresh", "id_token": "private-fresh-id",
		})
	}))
	t.Cleanup(fake.Close)
	t.Cleanup(CodexTokenURLForTest(fake.URL))
	oldSync := steady.Sync
	t.Cleanup(func() { steady.Sync = oldSync })
	var logged bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	path, before := upstreamDueCodexAuth(t)
	steady.Sync = func(*os.File) error { return errors.New("fixture disk offline") }
	if tok, id, err := codexToken(t.Context(), path); err != nil || tok != freshAccess || id != "acct-fixture" {
		t.Fatalf("fresh token cannot serve after persistence failed: id=%q err=%v", id, err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, before) {
		t.Fatalf("failed flush damaged the old sign-in: %v", err)
	}
	upstreamNoCredentialTemps(t, path)
	if !strings.Contains(logged.String(), path) || !strings.Contains(logged.String(), "fixture disk offline") {
		t.Fatal("Codex persistence failure was not reported")
	}
	var old codexAuth
	_ = json.Unmarshal(before, &old)
	for _, secret := range []string{old.Tokens.AccessToken, old.Tokens.IDToken, old.Tokens.RefreshToken, freshAccess, "private-fresh-refresh", "private-fresh-id"} {
		if strings.Contains(logged.String(), secret) {
			t.Fatal("Codex persistence log leaked a credential")
		}
	}

	expectedDir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	flushed := false
	steady.Sync = func(f *os.File) error {
		if filepath.Dir(f.Name()) != expectedDir || filepath.Base(f.Name()) == filepath.Base(path) {
			t.Error("credential was not flushed in a temporary file beside its destination")
		}
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, before) {
			t.Error("old credentials changed before the replacement was flushed")
		}
		flushed = true
		return f.Sync()
	}
	if _, _, err := codexToken(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	var saved map[string]any
	if err != nil || json.Unmarshal(got, &saved) != nil || !flushed {
		t.Fatalf("credential replacement was not flushed and readable: %v", err)
	}
	tokens, _ := saved["tokens"].(map[string]any)
	other, _ := saved["other"].(map[string]any)
	if tokens["refresh_token"] != "private-fresh-refresh" || tokens["account_id"] != "acct-fixture" || other["unchanged"] != true || saved["last_refresh"] == "previous" {
		t.Fatal("credential replacement lost rotated tokens or unrelated fields")
	}
	if value, exists := saved["OPENAI_API_KEY"]; !exists || value != nil {
		t.Fatal("credential replacement dropped an unrelated null field")
	}
	upstreamNoCredentialTemps(t, path)
}

func TestWhaleBridgeUpstreamClaudeCredentialWriteIsDurable(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), ".claude", ".credentials.json")
	writeFile(t, path, map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "old-access", "refreshToken": "old-refresh"},
		"mcpOAuth":      map[string]any{"unrelated": "keep-me"},
	})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := parseClaudeCredentials(before)
	if !ok {
		t.Fatal("fixture credentials cannot be parsed")
	}
	c.OAuth.AccessToken, c.OAuth.RefreshToken = "fresh-access", "fresh-refresh"
	loc := claudeCredentialLocation{path: path}
	oldSync := steady.Sync
	t.Cleanup(func() { steady.Sync = oldSync })
	steady.Sync = func(*os.File) error { return errors.New("fixture disk offline") }
	if err := writeClaudeCredential(loc, c); err == nil {
		t.Fatal("Claude credential flush failure was reported as saved")
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, before) {
		t.Fatalf("failed flush damaged Claude's old sign-in: %v", err)
	}
	upstreamNoCredentialTemps(t, path)
	expectedDir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	flushed := false
	steady.Sync = func(f *os.File) error {
		if filepath.Dir(f.Name()) != expectedDir || filepath.Base(f.Name()) == filepath.Base(path) {
			t.Error("Claude credentials were not flushed beside their destination")
		}
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, before) {
			t.Error("Claude credentials changed before the replacement was flushed")
		}
		flushed = true
		return f.Sync()
	}
	if err := writeClaudeCredential(loc, c); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	var saved map[string]any
	if err != nil || json.Unmarshal(got, &saved) != nil || !flushed {
		t.Fatalf("Claude credential replacement was not flushed and readable: %v", err)
	}
	oauth, _ := saved["claudeAiOauth"].(map[string]any)
	mcp, _ := saved["mcpOAuth"].(map[string]any)
	if oauth["refreshToken"] != "fresh-refresh" || mcp["unrelated"] != "keep-me" {
		t.Fatal("Claude credential replacement lost new tokens or unrelated OAuth state")
	}
	upstreamNoCredentialTemps(t, path)
}
