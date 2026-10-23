package provider

import (
	"bytes"
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

	"github.com/ChisaAlter/WhaleBridge/internal/settings"
	"github.com/ChisaAlter/WhaleBridge/internal/steady"
)

func upstreamUsedWeekServer(t *testing.T) *atomic.Int32 {
	t.Helper()
	var consumed atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"plan_type": "pro", "rate_limit": map[string]any{
					"primary_window":   map[string]any{"used_percent": 10, "limit_window_seconds": 18000},
					"secondary_window": map[string]any{"used_percent": 100, "limit_window_seconds": 604800, "reset_at": time.Now().Add(72 * time.Hour).Unix()},
				}, "rate_limit_reset_credits": map[string]any{"available_count": 2},
			})
		case "/backend-api/wham/rate-limit-reset-credits":
			_ = json.NewEncoder(w).Encode(map[string]any{"available_count": 2, "credits": []any{
				map[string]any{"id": "credit-fixture", "status": "available", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
			}})
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumed.Add(1)
			_, _ = w.Write([]byte(`{"code":"reset","windows_reset":2}`))
		default:
			t.Errorf("unexpected reset fixture request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fake.Close)
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	return &consumed
}

func upstreamResetHome(t *testing.T) {
	t.Helper()
	signIn(t)
	if err := SetCodexAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	autoReset.Lock()
	autoReset.path = ""
	autoReset.Unlock()
}

func upstreamCorruptResetLedger(t *testing.T, data []byte) string {
	t.Helper()
	path := autoReset.where()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func upstreamCheckBadLedger(t *testing.T, path string, want []byte) {
	t.Helper()
	aside, err := filepath.Glob(path + ".bad-*")
	if err != nil || len(aside) != 1 {
		t.Fatalf("corrupt reset ledger was not kept aside once: %v (%v)", aside, err)
	}
	if got, err := os.ReadFile(aside[0]); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("bad ledger copy did not keep the exact bytes: %v", err)
	}
}

// Reconstruct only weekly automatic use from the previous week. Missing
// history is known-empty, as readResetsUsed specifies; manual and expiring
// uses must not suppress a weekly automatic reset.
func TestWhaleBridgeUpstreamAutoResetRebuildsCorruptLedger(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name string
		uses []ResetUse
		want int32
	}{
		{"recent automatic use", []ResetUse{{User: "Me@example.com", At: now.Add(-time.Hour), Why: ResetUsedWeek, Windows: 2}}, 0},
		{"old automatic use", []ResetUse{{User: "me@example.com", At: now.Add(-8 * 24 * time.Hour), Why: ResetUsedWeek}}, 1},
		{"manual use", []ResetUse{{User: "me@example.com", At: now.Add(-time.Hour), Why: ResetUsedByHand}}, 1},
		{"expiring use", []ResetUse{{User: "me@example.com", At: now.Add(-time.Hour), Why: ResetUsedExpiring}}, 1},
		{"another account", []ResetUse{{User: "other@example.com", At: now.Add(-time.Hour), Why: ResetUsedWeek}}, 1},
		{"missing history", nil, 1},
		{"newest record wins", []ResetUse{
			{User: "me@example.com", At: now.Add(-time.Hour), Why: ResetUsedWeek, Windows: 2},
			{User: "ME@example.com", At: now.Add(-6 * 24 * time.Hour), Why: ResetUsedWeek, Windows: 1},
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstreamResetHome(t)
			if tc.uses != nil {
				writeFile(t, resetsUsedPath(), tc.uses)
			}
			bad := make([]byte, 64)
			path := upstreamCorruptResetLedger(t, bad)
			consumed := upstreamUsedWeekServer(t)
			for range 2 {
				out, err := AutoUseCodexReset(t.Context(), "me@example.com")
				if err != nil {
					t.Fatal(err)
				}
				if tc.want == 0 && out.Code != "" {
					t.Fatalf("another weekly reset was attempted: %+v", out)
				}
				autoReset.Lock()
				autoReset.path = "" // a new process reads the persisted repair
				autoReset.Unlock()
			}
			if got := consumed.Load(); got != tc.want {
				t.Fatalf("spent %d resets, want %d", got, tc.want)
			}
			upstreamCheckBadLedger(t, path, bad)
			b, err := os.ReadFile(path)
			var kept map[string]autoResetSpent
			if err != nil || json.Unmarshal(b, &kept) != nil {
				t.Fatalf("rebuilt ledger is unreadable: %v", err)
			}
			s, ok := kept["me@example.com"]
			if !ok || s.Out.Code != "reset" || !s.Until.After(time.Now()) {
				t.Fatal("the weekly spend was not kept past a restart")
			}
			if tc.name == "newest record wins" && (!s.At.Equal(tc.uses[0].At) || s.Out.Windows != 2 || !s.Until.Equal(tc.uses[0].At.Add(autoResetWeek))) {
				t.Fatal("reconstruction used an older entry over the latest weekly spend")
			}
		})
	}
}

// A field type error can leave newer-looking entries in the partially
// decoded map. None of them may override the known weekly spend history.
func TestWhaleBridgeUpstreamAutoResetRebuildDiscardsPartialDecode(t *testing.T) {
	upstreamResetHome(t)
	now := time.Now().UTC()
	use := ResetUse{User: "me@example.com", At: now.Add(-time.Hour), Why: ResetUsedWeek, Windows: 2}
	writeFile(t, resetsUsedPath(), []ResetUse{use})
	bad, err := json.Marshal(map[string]any{
		use.User: map[string]any{
			"at": now.Add(-time.Minute), "until": time.Time{},
			"outcome": map[string]any{"code": 123, "windows": 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := upstreamCorruptResetLedger(t, bad)
	consumed := upstreamUsedWeekServer(t)
	for range 2 {
		if out, err := AutoUseCodexReset(t.Context(), use.User); err != nil || out.Code != "" {
			t.Fatalf("partial decode displaced the real weekly spend: %+v (%v)", out, err)
		}
		autoReset.Lock()
		autoReset.path = "" // the repair must also survive a restart
		autoReset.Unlock()
	}
	if consumed.Load() != 0 {
		t.Fatalf("partial decode caused %d extra resets", consumed.Load())
	}
	upstreamCheckBadLedger(t, path, bad)
	data, err := os.ReadFile(path)
	var kept map[string]autoResetSpent
	if err != nil || json.Unmarshal(data, &kept) != nil {
		t.Fatalf("rebuilt ledger is unreadable: %v", err)
	}
	s := kept[use.User]
	if !s.At.Equal(use.At) || !s.Until.Equal(use.At.Add(autoResetWeek)) || s.Out.Code != "reset" || s.Out.Windows != use.Windows {
		t.Fatal("partial decode survived instead of the known weekly spend")
	}
}

func TestWhaleBridgeUpstreamAutoResetUnknownWeekHoldsAllAccounts(t *testing.T) {
	upstreamResetHome(t)
	if err := SetCodexAutoReset("other@example.com", true); err != nil {
		t.Fatal(err)
	}
	writeFile(t, resetsUsedPath(), "not a reset-use list")
	bad := []byte(`{"cut off":`)
	path := upstreamCorruptResetLedger(t, bad)
	consumed := upstreamUsedWeekServer(t)
	var until time.Time
	for _, user := range []string{"me@example.com", "other@example.com"} {
		out, err := AutoUseCodexReset(t.Context(), user)
		if err != nil || out.Code != "" {
			t.Fatalf("unknown week spent a reset for %s: %+v (%v)", user, out, err)
		}
		autoReset.Lock()
		s := autoReset.spent[autoResetAny]
		autoReset.path = ""
		autoReset.Unlock()
		if left := time.Until(s.Until); left < autoResetWeek-time.Minute || left > autoResetWeek {
			t.Fatalf("unknown week held until %v", s.Until)
		}
		if !until.IsZero() && !until.Equal(s.Until) {
			t.Fatal("restart extended the persisted week hold")
		}
		until = s.Until
	}
	if consumed.Load() != 0 {
		t.Fatal("unknown history spent a weekly automatic reset")
	}
	upstreamCheckBadLedger(t, path, bad)

	// A hold that has elapsed permits the normal weekly look again.
	a := autoResets{file: "expired-autoreset.json"}
	writeFile(t, a.where(), map[string]autoResetSpent{autoResetAny: {Until: time.Now().Add(-time.Minute)}})
	spent := false
	out, err := a.use("me@example.com", func(now time.Time) (*time.Time, bool, error) {
		end := now.Add(time.Hour)
		return &end, true, nil
	}, func() (ResetOutcome, error) { spent = true; return ResetOutcome{Code: "reset"}, nil })
	if err != nil || out.Code != "reset" || !spent {
		t.Fatalf("elapsed unknown-week hold still blocked auto-use: %+v (%v)", out, err)
	}
}

// No write or spend is allowed when evidence cannot be kept. A long valid
// ledger basename makes the .bad suffix exceed the filesystem's component
// limit without depending on root permissions or platform-specific locks.
func TestWhaleBridgeUpstreamAutoResetStopsIfCorruptionCannotBeKept(t *testing.T) {
	isolate(t)
	a := autoResets{file: strings.Repeat("a", 240) + ".json"}
	bad := []byte("broken ledger")
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.where(), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		looked, spent := false, false
		_, err := a.use("me@example.com", func(time.Time) (*time.Time, bool, error) {
			looked = true
			return nil, true, nil
		}, func() (ResetOutcome, error) { spent = true; return ResetOutcome{Code: "reset"}, nil })
		if err == nil || !strings.Contains(err.Error(), "keeping it aside") || looked || spent {
			t.Fatalf("unpreserved corruption did not stop auto-use: looked=%v spent=%v err=%v", looked, spent, err)
		}
		if got, err := os.ReadFile(a.where()); err != nil || !bytes.Equal(got, bad) {
			t.Fatalf("unpreserved corruption was overwritten: %v", err)
		}
	}
}

func TestWhaleBridgeUpstreamAutoResetStopsOnReadError(t *testing.T) {
	isolate(t)
	a := autoResets{file: "directory-ledger.json"}
	if err := os.MkdirAll(a.where(), 0o700); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, err := a.use("me@example.com", func(time.Time) (*time.Time, bool, error) {
			t.Error("looked at quota with an unreadable ledger")
			return nil, false, nil
		}, func() (ResetOutcome, error) {
			t.Error("spent a reset with an unreadable ledger")
			return ResetOutcome{}, nil
		})
		if err == nil {
			t.Fatal("unreadable ledger was treated as no spends")
		}
	}
}

func TestWhaleBridgeUpstreamAutoResetSaveFlushesBeforeReplace(t *testing.T) {
	isolate(t)
	a := autoResets{file: "durable-autoreset.json"}
	a.path = a.where()
	old := map[string]autoResetSpent{"old@example.com": {Until: time.Now().Add(time.Hour)}}
	writeFile(t, a.path, old)
	before, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	a.spent = map[string]autoResetSpent{"new@example.com": {Until: time.Now().Add(time.Hour)}}
	oldSync := steady.Sync
	t.Cleanup(func() { steady.Sync = oldSync })
	var logged bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(oldLog) })
	steady.Sync = func(*os.File) error { return errors.New("fixture flush failed") }
	a.save()
	if got, err := os.ReadFile(a.path); err != nil || !bytes.Equal(got, before) {
		t.Fatalf("failed flush damaged the ledger: %v", err)
	}
	if !strings.Contains(logged.String(), "fixture flush failed") {
		t.Fatal("ledger persistence failure was silently dropped")
	}
	flushed := false
	steady.Sync = func(f *os.File) error {
		if filepath.Dir(f.Name()) != filepath.Dir(a.path) || f.Name() == a.path {
			t.Error("ledger was not flushed in a file beside its destination")
		}
		if got, err := os.ReadFile(a.path); err != nil || !bytes.Equal(got, before) {
			t.Error("ledger changed before the replacement was flushed")
		}
		flushed = true
		return f.Sync()
	}
	a.save()
	var got map[string]autoResetSpent
	data, err := os.ReadFile(a.path)
	if err != nil || json.Unmarshal(data, &got) != nil || !flushed || got["new@example.com"].Until.IsZero() {
		t.Fatalf("durable ledger replacement failed: %v", err)
	}
	left, err := filepath.Glob(filepath.Join(filepath.Dir(a.path), "."+filepath.Base(a.path)+".*"))
	if err != nil || len(left) != 0 {
		t.Fatalf("ledger temporary files left behind: %v (%v)", left, err)
	}
}
