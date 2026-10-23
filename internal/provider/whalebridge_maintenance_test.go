package provider

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
)

func maintenancePicks(t *testing.T, id string, off, hidden bool) {
	t.Helper()
	writeFile(t, Path(), map[string]any{"providers": []map[string]any{{"id": id, "off": off, "hidden": hidden}}})
	Changed()
}

func TestWhaleBridgeMaintenanceIdentityAndModelGates(t *testing.T) {
	isolate(t)
	for _, hidden := range []bool{false, true} {
		maintenancePicks(t, "cursor", !hidden, hidden)
		var asked atomic.Int32
		c := cliIdentity{name: "cursor", read: true, at: time.Now().Add(-time.Hour), user: "kept", plan: "pro", ok: true,
			ask: func() (string, string, bool, error) { asked.Add(1); return "new", "pro", true, nil }}
		if user, _, ok := c.get(); user != "kept" || !ok || asked.Load() != 0 {
			t.Fatal("provider left alone asked its CLI or dropped the last identity")
		}
		if usage, at := loginUsageAt(t.Context(), "cursor"); usage != nil || !at.IsZero() {
			t.Fatal("provider left alone entered the usage lookup")
		}
		if _, _, _, ok := NextLogin(t.Context(), "cursor"); ok {
			t.Fatal("provider left alone proposed a native account switch")
		}
	}
	maintenancePicks(t, "cursor", false, false)
	var asks atomic.Int32
	c := cliIdentity{name: "cursor", read: true, ask: func() (string, string, bool, error) {
		asks.Add(1)
		return "back-on", "pro", true, nil
	}}
	if user, _, ok := c.get(); user != "back-on" || !ok || asks.Load() != 1 {
		t.Fatal("re-enabled provider did not ask its CLI")
	}
	p := Provider{ID: "maintenance-models", Responses: "http://127.0.0.1/fixture", Account: &Account{Agent: "codex", User: "synthetic"}}
	for _, hidden := range []bool{false, true} {
		p.Off, p.Hidden = !hidden, hidden
		newFetches.Lock()
		due, skipped := newDue([]Provider{p})
		newFetches.Unlock()
		if len(due)+len(skipped) != 0 {
			t.Fatal("provider left alone scheduled a model fetch")
		}
	}
	p.Off, p.Hidden = false, false
	newFetches.Lock()
	due, _ := newDue([]Provider{p})
	newFetches.Unlock()
	if len(due) != 1 {
		t.Fatal("enabled account no longer schedules its first model fetch")
	}
}

func TestWhaleBridgeMaintenanceRenewalsLeaveOffProvidersAlone(t *testing.T) {
	isolate(t)
	const user = "owned@example.test"
	saveLogins(t, savedLogin{Agent: "codex", User: user, Owned: true, On: true, Auth: parityAuth(t, user, "owned")})
	var calls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"fresh-synthetic","refresh_token":"fresh-refresh"}`))
	}))
	t.Cleanup(fake.Close)
	t.Cleanup(CodexTokenURLForTest(fake.URL))
	for _, hidden := range []bool{false, true} {
		maintenancePicks(t, "codex", !hidden, hidden)
		if rs := RenewLogins(t.Context(), 0); len(rs) != 0 || calls.Load() != 0 {
			t.Fatalf("left-alone provider refreshed saved credentials: %+v", rs)
		}
	}
	maintenancePicks(t, "codex", false, false)
	if rs := RenewLogins(t.Context(), 0); len(rs) != 1 || !rs[0].Renewed || calls.Load() != 1 {
		t.Fatalf("enabled owned account did not renew: %+v", rs)
	}
	if ls := readLogins(); len(ls) != 1 || !ls[0].Owned {
		t.Fatal("maintenance changed credential ownership")
	}
}

func TestWhaleBridgeMaintenanceExpiringResetOffAndOn(t *testing.T) {
	upstreamResetHome(t)
	oldNext, oldUntil := expiring.next, expiring.until
	expiring.next, expiring.until = map[string]time.Time{}, map[string]time.Time{}
	t.Cleanup(func() { expiring.next, expiring.until = oldNext, oldUntil })
	consumed := upstreamUsedWeekServer(t)
	var calls atomic.Int32
	upstreamObserveRefresh(t, func(*http.Request) { calls.Add(1) })
	for _, hidden := range []bool{false, true} {
		maintenancePicks(t, "codex", !hidden, hidden)
		SpendExpiringCodexResets(t.Context())
		if consumed.Load() != 0 || calls.Load() != 0 {
			t.Fatal("left-alone Codex read usage or spent an expiring reset")
		}
	}
	maintenancePicks(t, "codex", false, false)
	SpendExpiringCodexResets(t.Context())
	if calls.Load() == 0 || consumed.Load() != 1 {
		t.Fatalf("enabled Codex did not spend its due reset: requests=%d spent=%d", calls.Load(), consumed.Load())
	}
}

// A kick exercises the production timer loop immediately. The preference
// callback acknowledges each processed kick, without sleeping for its timer.
func TestWhaleBridgeMaintenanceWarmLoopGate(t *testing.T) {
	isolate(t)
	const name = "maintenance-warmer"
	maintenancePicks(t, name, true, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready, processed, done, sent := make(chan struct{}), make(chan struct{}, 1), make(chan struct{}), make(chan struct{}, 1)
	var calls atomic.Int32
	w := codexWarmer{path: filepath.Join(t.TempDir(), "warm.json"), now: time.Now,
		usage: func(context.Context) map[string]SubscriptionQuota {
			calls.Add(1)
			return map[string]SubscriptionQuota{"synthetic": {Windows: []QuotaWindow{{Name: "week", Span: 7 * 24 * time.Hour}}}}
		}, send: func(context.Context, string) error { sent <- struct{}{}; return nil }}
	go func() {
		defer close(done)
		n := 0
		keepWarm(ctx, name, w, func() (string, []string) {
			n++
			if n == 1 {
				close(ready)
			}
			select {
			case processed <- struct{}{}:
			default:
			}
			return "all", nil
		})
	}()
	// Wait for registration, using the same hook mutex as production.
	var hook func(string)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for hook == nil {
		notStartedHooks.Lock()
		hook = notStartedHooks.m[name].f
		notStartedHooks.Unlock()
		if hook == nil {
			select {
			case <-deadline.C:
				t.Fatal("warmer did not register")
			default:
			}
			time.Sleep(time.Millisecond)
		}
	}
	hook("first")
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("warm kick not processed")
	}
	// Cancelling joins the loop before checking that it performed no work.
	cancel()
	<-done
	if calls.Load() != 0 {
		t.Fatal("left-alone provider read warm-up usage")
	}

	maintenancePicks(t, name, false, false)
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	done2 := make(chan struct{})
	go func() { defer close(done2); keepWarm(ctx2, name, w, func() (string, []string) { return "all", nil }) }()
	hook = nil
	deadline2 := time.NewTimer(5 * time.Second)
	defer deadline2.Stop()
	for hook == nil {
		notStartedHooks.Lock()
		hook = notStartedHooks.m[name].f
		notStartedHooks.Unlock()
		if hook == nil {
			select {
			case <-deadline2.C:
				t.Fatal("enabled warmer did not register")
			default:
			}
			time.Sleep(time.Millisecond)
		}
	}
	hook("second")
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("enabled provider did not warm")
	}
	cancel2()
	<-done2
}

func TestWhaleBridgeMaintenanceCheckinProviderSwitches(t *testing.T) {
	isolate(t)
	const card = "checkin-fixture"
	var mu sync.Mutex
	var asked int
	c := checkiner{path: filepath.Join(t.TempDir(), "checkin.json"), now: time.Now, mu: &mu, accounts: func() []checkinAcct {
		return []checkinAcct{{User: "synthetic", On: true, key: "fixture|one", provider: card, do: func(context.Context) WorkBuddyCheckin {
			asked++
			return WorkBuddyCheckin{Outcome: CheckinClaimed}
		}}}
	}}
	for _, hidden := range []bool{false, true} {
		maintenancePicks(t, card, !hidden, hidden)
		if got := c.checkinNow(t.Context(), false); len(got) != 0 || asked != 0 {
			t.Fatal("background check-in reached a left-alone provider")
		}
	}
	if got := c.checkinNow(t.Context(), true); len(got) != 1 || !got[0].Asked || asked != 1 {
		t.Fatal("explicit check-in did not retain its existing behavior")
	}
	for _, c := range []checkiner{
		newMiniMaxCheckiner(func() []miniMaxAccount { return []miniMaxAccount{{On: true, uid: "one", card: card}} }),
		newTraeCheckiner(func() []traeAccount { return []traeAccount{{On: true, uid: "one", card: card}} }),
		newQoderCheckiner(func() []qoderCheckinAcct {
			return []qoderCheckinAcct{{On: true, uid: "one", site: "qoder", card: card}}
		}, time.Now),
		(pluginCheckinProvider{ID: "fake", accounts: []pluginCheckinAcct{{On: true, key: "fake|one", card: card}}}).checkiner(time.Now),
	} {
		if as := c.accounts(); len(as) != 1 || as[0].provider != card {
			t.Fatal("vendor account lost its provider switch")
		}
	}
}

func TestWhaleBridgeMaintenanceUnreadableCheckin(t *testing.T) {
	isolate(t)
	for name, bad := range map[string][]byte{"cut": []byte(`{"key":`), "zeroed": make([]byte, 64), "empty": {}, "list": []byte(`[]`), "null": []byte(`null`), "type": []byte(`{"key":{"outcome":1}}`), "private time value": []byte(`{"key":{"at":"synthetic-private-ledger-value"}}`)} {
		t.Run(name, func(t *testing.T) {
			f := &fakeWBCheckin{active: true}
			srv := f.serve(t)
			now := time.Now()
			c := wbCheckinFixture(t, srv, &now)
			if err := os.WriteFile(c.path, bad, 0o600); err != nil {
				t.Fatal(err)
			}
			var logged bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logged)
			t.Cleanup(func() { log.SetOutput(old) })
			for _, soon := range []bool{false, true} {
				got := c.checkinNow(t.Context(), soon)
				if len(got) != 1 || got[0].Outcome != CheckinFailed || got[0].Asked || !strings.Contains(got[0].Msg, c.path) {
					t.Fatalf("bad ledger allowed a request or lost diagnostics: %+v", got)
				}
				if strings.Contains(got[0].Msg, "synthetic-private-ledger-value") {
					t.Fatal("parse diagnostic echoed a private ledger value")
				}
			}
			if strings.Contains(logged.String(), "synthetic-private-ledger-value") {
				t.Fatal("parse log echoed a private ledger value")
			}
			if strings.Count(logged.String(), "check-in:") != 1 {
				t.Fatal("unchanged ledger error was logged repeatedly")
			}
			if status, claims := f.counts(); status != 0 || claims != 0 {
				t.Fatal("unreadable ledger reached HTTP fixture")
			}
			if data, err := os.ReadFile(c.path); err != nil || !bytes.Equal(data, bad) {
				t.Fatal("unreadable ledger was overwritten")
			}
		})
	}
}

func TestWhaleBridgeMaintenanceCheckinLastGoodAndRepair(t *testing.T) {
	isolate(t)
	f := &fakeWBCheckin{active: true}
	srv := f.serve(t)
	now := time.Now()
	c := wbCheckinFixture(t, srv, &now)
	if got := c.checkinNow(t.Context(), false); len(got) != 1 || !got[0].Asked {
		t.Fatal("missing ledger did not check in")
	}
	good, err := os.ReadFile(c.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.path, []byte(`{"bad":`), 0o600); err != nil {
		t.Fatal(err)
	}
	shown := readCheckins(c.path)
	if shown["workbuddy|u1"].Outcome != CheckinClaimed {
		t.Fatal("display dropped last good result")
	}
	delete(shown, "workbuddy|u1")
	if readCheckins(c.path)["workbuddy|u1"].Outcome != CheckinClaimed {
		t.Fatal("display caller mutated cached results")
	}
	var loop wbCheckinLoop
	if got := loop.tick(t.Context(), c); len(got) != 1 || got[0].Outcome != CheckinFailed || loop.again.IsZero() {
		t.Fatal("unknown ledger did not schedule its retry")
	}
	if err := os.WriteFile(c.path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := c.checkinNow(t.Context(), false); len(got) != 1 || got[0].Asked || got[0].Outcome != CheckinClaimed {
		t.Fatal("repaired ledger repeated today's check-in")
	}
	if status, claims := f.counts(); status != 1 || claims != 1 {
		t.Fatalf("status=%d claims=%d", status, claims)
	}

	path := filepath.Join(t.TempDir(), "plugin-checkin.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	p := checkiner{path: path, now: time.Now, mu: &mu, by: "plugin:synthetic", accounts: func() []checkinAcct {
		return []checkinAcct{{User: "one", On: true, key: "plugin|one", do: func(context.Context) WorkBuddyCheckin {
			t.Error("unreadable directory ran plugin hook")
			return WorkBuddyCheckin{}
		}}}
	}}
	if got := p.checkinNow(t.Context(), true); len(got) != 1 || got[0].Outcome != CheckinFailed {
		t.Fatal("directory ledger did not stop plugin check-in")
	}
}

func TestWhaleBridgeMaintenanceRetiredQoderPluginCheckin(t *testing.T) {
	whaleCatalogHome(t)
	pp := plugin.Provider{ID: "qoder", Spec: "@fixture/qoder", Accounts: []plugin.Account{{Key: "qoder", AccountID: "synthetic"}}}
	whaleCatalogPlugin(t, pp, map[string]any{"qoder": map[string]any{"type": "oauth", "access": "synthetic", "uid": "u-fixture"}})
	if !PluginServes("qoder") || Moved("qoder") {
		t.Fatal("fixture must be retired but not migrated")
	}
	if as := qoderCheckinAccounts(); len(as) != 1 || as[0].uid != "u-fixture" || as[0].card != PluginID("qoder") {
		t.Fatalf("retired plugin account was lost: %+v", as)
	}
}
