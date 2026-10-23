package pluginrt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/appdir"
)

func home(t *testing.T) {
	t.Helper()
	appdir.UseExecutable("")
	t.Cleanup(func() { appdir.UseExecutable("") })
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}
func put(t *testing.T, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}
func stop(t *testing.T, rt *Runtime) {
	t.Helper()
	rt.Stop()
	done := make(chan struct{})
	go func() { rt.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime Stop/Wait did not join")
	}
}
func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for lifecycle event")
	}
}

func TestDependencyOrderingRollbackAndReverseStop(t *testing.T) {
	var events []string
	note := func(v string) func() { return func() { events = append(events, v) } }
	rt := New([]Manifest{
		{ID: "a", Requires: []string{"data"}, Apply: func(c *Ctx) error {
			if c.Service("data") != "ready" {
				t.Error("dependency not available in Apply")
			}
			c.OnRetire(func() {
				if c.Service("data") != "ready" {
					t.Error("dependency withdrawn before its consumer retired")
				}
				note("a-retire")()
			})
			c.OnDispose(note("a-1"))
			c.OnDispose(note("a-2"))
			return nil
		}},
		{ID: "b", Provides: []string{"data"}, Apply: func(c *Ctx) error { c.Provide("data", "ready"); c.OnDispose(note("b")); return nil }},
		{ID: "bad", Apply: func(c *Ctx) error {
			c.Provide("partial", "must not leak")
			c.OnDispose(note("bad"))
			return errors.New("apply failed")
		}},
		{ID: "boom", Apply: func(c *Ctx) error { c.OnDispose(note("boom")); panic("apply panicked") }},
		{ID: "wait", Requires: []string{"partial"}}, {ID: "old", API: "^2"},
	})
	on := map[string]bool{"a": true, "b": true, "bad": true, "boom": true, "wait": true, "old": true}
	if err := rt.Reconcile(on, nil); err == nil {
		t.Fatal("Apply errors and missing dependencies were swallowed")
	}
	want := map[string]State{"a": Active, "b": Active, "bad": Failed, "boom": Failed, "wait": Pending, "old": Failed}
	for _, s := range rt.Status() {
		if s.State != want[s.ID] {
			t.Fatalf("%s: %+v", s.ID, s)
		}
	}
	if !slices.Equal(events, []string{"bad", "boom"}) {
		t.Fatalf("partial Apply did not roll back: %v", events)
	}
	events = nil
	stop(t, rt)
	if !slices.Equal(events, []string{"a-retire", "a-2", "a-1", "b"}) {
		t.Fatalf("reverse dependency/hook order: %v", events)
	}
	rt.Stop()
	rt.Wait()
	if err := rt.Reconcile(on, nil); !errors.Is(err, ErrStopped) {
		t.Fatalf("stopped runtime restarted: %v", err)
	}
}

func TestConfigChangesReapplyDependentsAndDoNotShareMaps(t *testing.T) {
	defaults := map[string]any{"nested": map[string]any{"default": 1}, "list": []any{map[string]any{"x": 1}}}
	var events []string
	var contexts []*Ctx
	rt := New([]Manifest{
		{ID: "a", Requires: []string{"data"}, Apply: func(c *Ctx) error {
			events = append(events, "apply-a")
			c.OnDispose(func() { events = append(events, "off-a") })
			return nil
		}},
		{ID: "b", Defaults: defaults, Provides: []string{"data"}, Apply: func(c *Ctx) error {
			contexts = append(contexts, c)
			c.Provide("data", c.Config)
			events = append(events, "apply-b")
			c.OnDispose(func() { events = append(events, "off-b") })
			return nil
		}},
	})
	on := map[string]bool{"a": true, "b": true}
	cfg := map[string]map[string]any{"b": {"nested": map[string]any{"user": 2}}}
	if err := rt.Reconcile(on, cfg); err != nil {
		t.Fatal(err)
	}
	if err := rt.Reconcile(on, cfg); err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 1 {
		t.Fatal("unchanged config reapplied")
	}
	cfg["b"]["nested"].(map[string]any)["user"] = 3
	if contexts[0].Config["nested"].(map[string]any)["user"] != 2 {
		t.Fatal("active config aliases caller")
	}
	contexts[0].Config["list"].([]any)[0].(map[string]any)["x"] = 99
	if defaults["list"].([]any)[0].(map[string]any)["x"] != 1 {
		t.Fatal("active config aliases defaults")
	}
	events = nil
	if err := rt.Reconcile(on, cfg); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(events, []string{"off-a", "off-b", "apply-b", "apply-a"}) {
		t.Fatalf("config refresh failed to reapply dependents: %v", events)
	}
	if len(contexts) != 2 || contexts[1].Config["nested"].(map[string]any)["user"] != 3 {
		t.Fatal("new config was ignored")
	}
	if contexts[0].Config["nested"].(map[string]any)["user"] != 2 {
		t.Fatal("old captured context mutated on refresh")
	}
	stop(t, rt)
}

func TestFailedConfigReapplyRestoresPreviousActiveDependencyChain(t *testing.T) {
	for _, failing := range []string{"provider", "dependent"} {
		t.Run(failing, func(t *testing.T) {
			newFailure := errors.New("new configuration rejected")
			var events []string
			var providerConfig, dependentService string
			rt := New([]Manifest{
				{ID: "a-consumer", Requires: []string{"data"}, Apply: func(c *Ctx) error {
					value, _ := c.Service("data").(string)
					events = append(events, "apply-consumer-"+value)
					c.OnDispose(func() { events = append(events, "dispose-consumer-"+value) })
					if value == "bad" && failing == "dependent" {
						return newFailure
					}
					dependentService = value
					return nil
				}},
				{ID: "b-provider", Defaults: map[string]any{"value": "old"}, Provides: []string{"data"}, Apply: func(c *Ctx) error {
					value, _ := c.Config["value"].(string)
					events = append(events, "apply-provider-"+value)
					c.OnDispose(func() { events = append(events, "dispose-provider-"+value) })
					if value == "bad" && failing == "provider" {
						return newFailure
					}
					providerConfig = value
					c.Provide("data", value)
					return nil
				}},
			})
			on := map[string]bool{"a-consumer": true, "b-provider": true}
			if err := rt.Reconcile(on, nil); err != nil {
				t.Fatal(err)
			}
			events = nil
			err := rt.Reconcile(on, map[string]map[string]any{"b-provider": {"value": "bad"}})
			if err == nil || !strings.Contains(err.Error(), newFailure.Error()) {
				t.Fatalf("new configuration failure was hidden: %v", err)
			}
			if !rt.Active("a-consumer") || !rt.Active("b-provider") || providerConfig != "old" || dependentService != "old" {
				t.Fatalf("previous service chain not restored: %+v provider=%s consumer=%s events=%v", rt.Status(), providerConfig, dependentService, events)
			}
			want := []string{"dispose-consumer-old", "dispose-provider-old", "apply-provider-bad"}
			if failing == "dependent" {
				want = append(want, "apply-consumer-bad", "dispose-consumer-bad")
			}
			want = append(want, "dispose-provider-bad", "apply-provider-old", "apply-consumer-old")
			if !slices.Equal(events, want) {
				t.Fatalf("rollback dependency order: %v, want %v", events, want)
			}
			if err := rt.Reconcile(on, map[string]map[string]any{"b-provider": {"value": "fixed"}}); err != nil {
				t.Fatal(err)
			}
			if providerConfig != "fixed" || dependentService != "fixed" {
				t.Fatal("fixed new configuration could not replace restored chain")
			}
			stop(t, rt)
		})
	}
}

func TestFailedConfigRestorationReportsFailureWithoutLooping(t *testing.T) {
	var attempts int
	canRestore := true
	rt := New([]Manifest{{ID: "fixture", Defaults: map[string]any{"value": "old"}, Apply: func(c *Ctx) error {
		attempts++
		if c.Config["value"] == "bad" {
			return errors.New("new failed")
		}
		if !canRestore {
			return errors.New("old external service disappeared")
		}
		return nil
	}}})
	on := map[string]bool{"fixture": true}
	if err := rt.Reconcile(on, nil); err != nil {
		t.Fatal(err)
	}
	canRestore = false
	err := rt.Reconcile(on, map[string]map[string]any{"fixture": {"value": "bad"}})
	if err == nil || !strings.Contains(err.Error(), "new failed") || !strings.Contains(err.Error(), "old external service disappeared") || attempts != 3 {
		t.Fatalf("best-effort failure was hidden or retried forever: %v attempts=%d", err, attempts)
	}
	if rt.Active("fixture") {
		t.Fatal("failed old external dependency was reported active")
	}
	stop(t, rt)
}

func TestCallbacksCanQueryRefreshAndStopWithoutDeadlock(t *testing.T) {
	home(t)
	var rt *Runtime
	var queried atomic.Int32
	rt = New([]Manifest{{ID: "callback", Apply: func(c *Ctx) error {
		_ = rt.Status()
		if _, err := rt.Refresh(); !errors.Is(err, ErrBusy) {
			t.Errorf("Apply recursive Refresh: %v", err)
		}
		c.OnDispose(func() {
			_ = rt.Status()
			queried.Add(1)
			if _, err := rt.Refresh(); !errors.Is(err, ErrStopped) {
				t.Errorf("Stop recursive Refresh: %v", err)
			}
			rt.Stop()
		})
		return nil
	}}})
	if _, err := rt.Refresh(); err != nil {
		t.Fatal(err)
	}
	stop(t, rt)
	if queried.Load() != 1 {
		t.Fatal("disposer did not execute")
	}
}

func TestWorkersCancelAndJoinBeforeResourceDisposal(t *testing.T) {
	started, allowExit, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var retired, disposed atomic.Bool
	var c *Ctx
	rt := New([]Manifest{{ID: "worker", Apply: func(ctx *Ctx) error {
		c = ctx
		ctx.OnRetire(func() { retired.Store(true) })
		ctx.OnDispose(func() {
			select {
			case <-exited:
			default:
				t.Error("resource closed before worker join")
			}
			disposed.Store(true)
		})
		ctx.Go(func(ctx context.Context) { close(started); <-ctx.Done(); <-allowExit; close(exited) })
		return nil
	}}})
	if err := rt.Reconcile(map[string]bool{"worker": true}, nil); err != nil {
		t.Fatal(err)
	}
	await(t, started)
	rt.Stop()
	done := make(chan struct{})
	go func() { rt.Wait(); close(done) }()
	select {
	case <-done:
		t.Fatal("Wait did not join worker")
	case <-time.After(15 * time.Millisecond):
	}
	if disposed.Load() {
		t.Fatal("resource closed while worker still running")
	}
	close(allowExit)
	await(t, done)
	if !retired.Load() || !disposed.Load() {
		t.Fatal("missing retire/dispose")
	}
	if c.Go(func(context.Context) { t.Error("late worker executed") }) {
		t.Fatal("retired context accepted late worker")
	}
}

func TestStopDuringApplyRollsBackAndCannotReRegister(t *testing.T) {
	entered, leave, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var alive atomic.Int32
	rt := New([]Manifest{{ID: "slow", Apply: func(c *Ctx) error {
		alive.Add(1)
		c.OnDispose(func() { alive.Add(-1) })
		close(entered)
		<-leave
		return nil
	}}})
	go func() { defer close(finished); _ = rt.Reconcile(map[string]bool{"slow": true}, nil) }()
	await(t, entered)
	rt.Stop()
	close(leave)
	await(t, finished)
	rt.Wait()
	if rt.Active("slow") || alive.Load() != 0 {
		t.Fatalf("late Apply survived shutdown: %+v alive=%d", rt.Status(), alive.Load())
	}
	if _, err := rt.Refresh(); !errors.Is(err, ErrStopped) {
		t.Fatal("late refresh was not refused")
	}
}

func TestComposeLayersAndConservativeInUse(t *testing.T) {
	no := false
	ms := []Manifest{
		{ID: "essential", Essential: true, Defaults: map[string]any{"nested": map[string]any{"a": 1, "b": 2}}},
		{ID: "unused"},
		{ID: "used", InUse: func() (int, error) { return 2, nil }},
		{ID: "unknown", InUse: func() (int, error) { return 0, errors.New("unreadable") }},
		{ID: "panic", InUse: func() (int, error) { panic("unreadable") }},
	}
	f := File{Preset: PresetEssentials, Plugins: []Row{{ID: "essential", Config: map[string]any{"nested": map[string]any{"b": 3}}}, {ID: "used", Enabled: &no}}}
	cs := Compose(ms, f)
	if !cs[0].Enabled || cs[1].Enabled || !cs[2].Enabled || cs[2].Chosen || cs[2].Source != FromInUse || !cs[3].Enabled || !cs[4].Enabled {
		t.Fatalf("layers/in-use: %+v", cs)
	}
	if !reflect.DeepEqual(cs[0].Config["nested"], map[string]any{"a": 1, "b": 3}) {
		t.Fatal("nested user config did not merge")
	}
	cs[0].Config["nested"].(map[string]any)["a"] = 9
	if ms[0].Defaults["nested"].(map[string]any)["a"] != 1 {
		t.Fatal("composed defaults alias manifest")
	}
}

func TestRefreshCorruptPreservesGoodAndFirstUsesDefaults(t *testing.T) {
	home(t)
	var count atomic.Int32
	m := Manifest{ID: "fixture", Defaults: map[string]any{"value": "default"}, Apply: func(c *Ctx) error { count.Add(1); return nil }}
	put(t, "plugins: [")
	rt := New([]Manifest{m})
	if _, err := rt.Refresh(); err == nil || !rt.Active("fixture") {
		t.Fatalf("first corrupt must return error and retain safe defaults: %v %+v", err, rt.Status())
	}
	put(t, "plugins:\n  - id: fixture\n    enabled: false\n")
	if _, err := rt.Refresh(); err != nil || rt.Active("fixture") {
		t.Fatalf("valid disable: %v", err)
	}
	put(t, "plugins: [")
	if _, err := rt.Refresh(); err == nil || rt.Active("fixture") {
		t.Fatalf("bad config overwrote previously good disabled state: %v", err)
	}
	if err := Set("fixture", true); err == nil {
		t.Fatal("mutator overwrote corrupt user configuration")
	}
	b, _ := os.ReadFile(Path())
	if string(b) != "plugins: [" {
		t.Fatal("corrupt bytes overwritten")
	}
	stop(t, rt)
}

func TestLoadRejectsInvalidRowsAndDocuments(t *testing.T) {
	home(t)
	for _, text := range []string{"preset: bogus\n", "plugins: [{id: x}, {id: x}]\n", "plugins: [{id: ''}]\n", "plugins: []\n---\nplugins: []\n", "plugins: [{id: x, enabled: maybe}]\n"} {
		put(t, text)
		if _, _, err := Load(); err == nil {
			t.Fatalf("accepted corrupt/ambiguous configuration %q", text)
		}
	}
	put(t, "# intentionally empty\n")
	if _, there, err := Load(); err != nil || !there {
		t.Fatalf("empty file: %v %v", there, err)
	}
}

func TestWatcherDetectsContentAndCancellationKeepsRegistrations(t *testing.T) {
	home(t)
	var applies atomic.Int32
	changed := make(chan struct{}, 4)
	rt := New([]Manifest{{ID: "fixture", Apply: func(c *Ctx) error { applies.Add(1); changed <- struct{}{}; return nil }}})
	put(t, "plugins:\n- id: fixture\n  config: {value: a}\n")
	ctx, cancel := context.WithCancel(context.Background())
	if err := rt.Start(ctx, 5*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	await(t, changed)
	stamp, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	// Same size and mtime: content hashing, not a stat-only heuristic.
	put(t, "plugins:\n- id: fixture\n  config: {value: b}\n")
	if err := os.Chtimes(Path(), stamp.ModTime(), stamp.ModTime()); err != nil {
		t.Fatal(err)
	}
	await(t, changed)
	cancel()
	rt.StopWatching()
	if !rt.Active("fixture") {
		t.Fatal("watch cancellation unregistered before host drained requests")
	}
	put(t, "plugins:\n- id: fixture\n  config: {value: c}\n")
	if applies.Load() != 2 {
		t.Fatalf("unexpected applications: %d", applies.Load())
	}
	stop(t, rt)
	if _, err := rt.Refresh(); !errors.Is(err, ErrStopped) {
		t.Fatalf("refresh after stopped watcher: %v", err)
	}
}

func TestWatcherStopAndRefreshContentionCannotApplyLate(t *testing.T) {
	home(t)
	entered, leave := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	rt := New([]Manifest{{ID: "fixture", Apply: func(c *Ctx) error {
		if count.Add(1) == 2 {
			close(entered)
			<-leave
		}
		return nil
	}}})
	put(t, "plugins:\n- id: fixture\n  config: {value: a}\n")
	if err := rt.Start(context.Background(), 5*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	put(t, "plugins:\n- id: fixture\n  config: {value: b}\n")
	await(t, entered)
	if _, err := rt.Refresh(); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent Refresh did not reject contention: %v", err)
	}
	rt.Stop()
	close(leave)
	rt.Wait()
	if count.Load() != 2 || rt.Active("fixture") {
		t.Fatalf("watcher/Apply survived Stop: %d %+v", count.Load(), rt.Status())
	}
}

func TestConcurrentReadsReconcileAndStop(t *testing.T) {
	rt := New([]Manifest{{ID: "fixture", Apply: func(c *Ctx) error { c.OnDispose(func() { _ = rtStatus(c.rt) }); return nil }}})
	var wg sync.WaitGroup
	for n := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 40 {
				_ = rt.Reconcile(map[string]bool{"fixture": true}, map[string]map[string]any{"fixture": {"v": fmt.Sprint(n, i)}})
				_ = rt.Status()
				_ = rt.Active("fixture")
			}
		}()
	}
	rt.Stop()
	wg.Wait()
	rt.Wait()
	if rt.Active("fixture") {
		t.Fatal("concurrent shutdown left runtime active")
	}
}
func rtStatus(rt *Runtime) []Status { return rt.Status() }

func TestTransientApplyFailureRetriesOnUnchangedRefresh(t *testing.T) {
	home(t)
	var attempts int
	rt := New([]Manifest{{ID: "transient", Apply: func(c *Ctx) error {
		attempts++
		if attempts == 1 {
			return errors.New("module input temporarily unreadable")
		}
		return nil
	}}})
	if _, err := rt.Refresh(); err == nil || attempts != 1 {
		t.Fatalf("first Apply must fail exactly once: %v attempts=%d", err, attempts)
	}
	if _, err := rt.Refresh(); err != nil || attempts != 2 || !rt.Active("transient") {
		t.Fatalf("unchanged YAML prevented retry: %v attempts=%d", err, attempts)
	}
	stop(t, rt)
}

func TestCompatibleHostAPIRanges(t *testing.T) {
	for want, ok := range map[string]bool{"": true, "^1": true, "^1.0": true, "^1.1": false, "^2": false, "1": false, "^x": false, "^-1": false, "^1.-1": false} {
		if Compatible(want) != ok {
			t.Fatalf("Compatible(%q) expected %v", want, ok)
		}
	}
}
