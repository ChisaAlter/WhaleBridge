package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A profile distinguishes seats sharing an email; usage from the live CLI
// must never cross from its Team account into that email's personal account.
func TestClaudeReadingOwnership(t *testing.T) {
	for _, tc := range []struct{ before, after, user string }{
		{"max", "team", "me@x.com"},
		{"team", "max", "me@x.com · Acme"},
	} {
		t.Run(tc.before+"-to-"+tc.after, func(t *testing.T) {
			isolate(t)
			home, _ := os.UserHomeDir()
			setPlan := func(plan string) {
				writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{
					"emailAddress": "me@x.com", "organizationUuid": "org-" + plan, "organizationName": "Acme"}})
				writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{
					"accessToken": "tok-" + plan, "refreshToken": "r-" + plan,
					"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": plan}})
				forgetAccountCaches()
			}
			setPlan(tc.before)
			if ClaudeCodeMovedOff(tc.user) {
				t.Fatal("the complete profile did not identify its own account")
			}
			var text atomic.Value
			text.Store("Current session: 17% used")
			runs := fakeClaudeUsage(t, &text, nil)
			ws, err := claudeWindows(context.Background(), tc.user, true)
			if err != nil || len(ws) != 1 || ws[0].Used != 17 || runs.Load() != 1 {
				t.Fatalf("initial reading: %+v err=%v runs=%d", ws, err, runs.Load())
			}
			claudeUsage.Lock()
			key := strings.ToLower(tc.user)
			e := claudeUsage.m[key]
			e.tried = e.tried.Add(-time.Hour)
			claudeUsage.m[key] = e
			claudeUsage.Unlock()
			UsageClaudeVia(func(context.Context, string) (string, error) {
				runs.Add(1)
				setPlan(tc.after)
				return "Current session: 89% used", nil
			})
			AskClaudeUsage()
			ws, err = claudeWindows(Asked(context.Background()), tc.user, true)
			if err != nil || len(ws) != 1 || ws[0].Used != 17 || runs.Load() != 2 {
				t.Fatalf("switched seat's reading overwrote the original: %+v err=%v runs=%d", ws, err, runs.Load())
			}
			if !ClaudeCodeMovedOff(tc.user) {
				t.Fatal("a different profile with the same email was treated as the original account")
			}
			claudeUsage.Lock()
			e = claudeUsage.m[key]
			e.tried = e.tried.Add(-time.Hour)
			claudeUsage.m[key] = e
			claudeUsage.Unlock()
			AskClaudeUsage()
			ws, err = claudeWindows(Asked(context.Background()), tc.user, true)
			if err != nil || len(ws) != 1 || ws[0].Used != 17 || runs.Load() != 2 {
				t.Fatalf("reading ran after moving off: %+v err=%v runs=%d", ws, err, runs.Load())
			}
		})
	}
}
