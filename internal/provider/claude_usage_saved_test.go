package provider

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshSavedClaudeUsage(t *testing.T) {
	claudeThree(t)
	var text atomic.Value
	text.Store("Current session: 17% used\nCurrent week (all models): 38% used")
	runs := fakeClaudeUsage(t, &text, nil)
	var expectSaved atomic.Bool
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		runs.Add(1)
		if expectSaved.Load() {
			if dir != claudeAccountDir("max@x.com") {
				return "", errors.New("/usage ran in another account's directory")
			}
			c, ok := readClaudeDir(dir)
			if !ok || c.OAuth.AccessToken != "tok-max@x.com" {
				return "", errors.New("/usage received another account's sign-in")
			}
		}
		return text.Load().(string), nil
	})
	ctx := context.Background()
	before := LoginUsage(ctx, "claude")
	if before["max@x.com"].Error != errClaudeSaved.Error() {
		t.Fatalf("unasked saved account: %+v", before["max@x.com"])
	}
	// the Usage page opened, `magpie quota`: the user asking, but not for
	// this account, and each saved account would start a Claude Code
	beforeRuns := runs.Load()
	AskClaudeUsage()
	if q := LoginUsage(Asked(ctx), "claude")["max@x.com"]; q.Error != errClaudeSaved.Error() || runs.Load() != beforeRuns {
		t.Fatalf("an ask for every card ran a saved account: %+v, %d runs", q, runs.Load()-beforeRuns)
	}
	beforeRuns = runs.Load()
	expectSaved.Store(true)
	RefreshUsage(ctx, "claude", "max@x.com")
	after := LoginUsage(ctx, "claude")
	q := after["max@x.com"]
	if q.Error != "" || len(q.Windows) != 2 || q.Windows[0].Used != 17 || q.Windows[1].Used != 38 || q.ReadAt == nil {
		t.Fatalf("saved account's refresh read no allowance: %+v", q)
	}
	if runs.Load() != beforeRuns+1 {
		t.Fatalf("refresh ran Claude Code %d times, want once", runs.Load()-beforeRuns)
	}
	if after["team@x.com · Acme"].Error != before["team@x.com · Acme"].Error {
		t.Fatal("refresh changed another saved account's card")
	}
	active := ""
	for _, l := range Logins("claude") {
		if l.Active {
			active = l.User
		}
	}
	if active != "pro@x.com" {
		t.Fatalf("refresh changed Claude Code's own account to %q", active)
	}

	// Rapid clicks still respect the per-account floor. A later explicit
	// refresh reads anew, while a background read keeps what was read.
	RefreshUsage(ctx, "claude", "max@x.com")
	if runs.Load() != beforeRuns+1 {
		t.Fatal("rapid refresh bypassed the per-account floor")
	}
	claudeUsage.Lock()
	e := claudeUsage.m["max@x.com"]
	e.tried = e.tried.Add(-time.Hour)
	claudeUsage.m["max@x.com"] = e
	claudeUsage.Unlock()
	if ws, err := claudeWindows(ctx, "max@x.com", false); err != nil || len(ws) != 2 || runs.Load() != beforeRuns+1 {
		t.Fatalf("background read ran a saved account: %v %+v", err, ws)
	}
	text.Store("Current session: 21% used\nCurrent week (all models): 40% used")
	RefreshUsage(ctx, "claude", "max@x.com")
	q = LoginUsage(ctx, "claude")["max@x.com"]
	if q.Error != "" || len(q.Windows) != 2 || q.Windows[0].Used != 21 || runs.Load() != beforeRuns+2 {
		t.Fatalf("later refresh kept an old reading: %+v", q)
	}
}

func TestSavedClaudeUsageSavesRotatedSignIn(t *testing.T) {
	claudeThree(t)
	var text atomic.Value
	text.Store("Current session: 17% used")
	fakeClaudeUsage(t, &text, nil)
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		if dir != "" {
			writeFile(t, filepath.Join(dir, ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{
				"accessToken": "tok-rotated", "refreshToken": "r-rotated",
				"expiresAt": time.Now().Add(8 * time.Hour).UnixMilli(), "subscriptionType": "max"}})
		}
		return text.Load().(string), nil
	})
	RefreshUsage(context.Background(), "claude", "max@x.com")
	loginsMu.Lock()
	ls := readLogins()
	loginsMu.Unlock()
	for _, l := range ls {
		if l.Agent == "claude" && l.User == "max@x.com" {
			if c, ok := parseClaudeCredentials(l.Auth); !ok || c.OAuth.RefreshToken != "r-rotated" {
				t.Fatalf("logins.json kept the spent sign-in: %s", l.Auth)
			}
			return
		}
	}
	t.Fatal("max@x.com isn't saved")
}
