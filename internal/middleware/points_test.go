package middleware

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/gwmw"
	"github.com/ChisaAlter/WhaleBridge/internal/lastgood"
	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/routes"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
	"github.com/ChisaAlter/WhaleBridge/internal/usage"
)

func pointSeats() []routes.Seat {
	return []routes.Seat{{Key: "a", Weight: 1, Soon: []time.Time{time.UnixMilli(1700000000000)}}, {Key: "b", Weight: 2, Free: true}, {Key: "c", Weight: 3}}
}
func pointOrder(t *testing.T, name string) []int {
	t.Helper()
	s, ok := routes.Find(name)
	if !ok {
		t.Fatalf("strategy %s not served", name)
	}
	return routes.Apply(s, "fixture", pointSeats())
}
func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(end) {
			t.Fatal("condition never became true")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPointsRegistrationReloadAndCapturedRun(t *testing.T) {
	e := file(t, `export function onRequest(b){b.generation="old";return b} export const strategies={reverse:(p,s,o)=>{if(p!=="fixture"||s[1].free!==true||s[0].soon[0]!==1700000000000||o.first!==2)throw Error("bad input");return [2,0,1]}}`)
	e.Options = map[string]any{"first": 2}
	use(t, e)
	if routes.Served("x/reverse") {
		t.Fatal("strategy served before points start")
	}
	stop := StartPoints()
	defer stop()
	if !slices.Equal(pointOrder(t, "x/reverse"), []int{2, 0, 1}) {
		t.Fatal("wrong JS order")
	}
	captured, _ := routes.Find("x/reverse")
	old := Chain{}.Begin(gwmw.Info{Model: "m", Protocol: "chat"})
	if old == nil {
		t.Fatal("hook not captured")
	}
	defer old.End()
	if err := os.WriteFile(e.Spec, []byte(`export function onRequest(b){b.generation="new";return b} export const strategies={newer:(p,s)=>[1,2,0]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Refresh(); err != nil {
		t.Fatal(err)
	}
	if routes.Served("x/reverse") || !routes.Served("x/newer") {
		t.Fatal("reload did not replace strategy names")
	}
	if got := routes.Apply(captured, "fixture", pointSeats()); !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatal("stopped captured strategy did not fail safely")
	}
	if b, _ := old.Request([]byte(`{}`)); !bytes.Contains(b, []byte(`"generation":"old"`)) {
		t.Fatalf("old captured hook lost runtime: %s", b)
	}
	fresh := Begin(info())
	defer fresh.End()
	if b, _ := fresh.Request([]byte(`{}`)); !bytes.Contains(b, []byte(`"generation":"new"`)) {
		t.Fatal(string(b))
	}
	stop()
	stop()
	if routes.Served("x/newer") || len(Routings()) != 0 {
		t.Fatal("stopped points left strategies registered")
	}
}

func TestPointsInvalidOrdersAndLimits(t *testing.T) {
	e := file(t, `export const strategies={thrower:()=>{throw Error("fixture")},slow:()=>{for(;;){}},duplicate:()=>[0,0,2],short:()=>[0],fraction:()=>[0.5,1,2],nulls:()=>[null,1,2],pending:()=>new Promise(()=>{})}`)
	use(t, e)
	stop := StartPoints()
	defer stop()
	for _, name := range []string{"thrower", "slow", "duplicate", "short", "fraction", "nulls", "pending"} {
		start := time.Now()
		if !slices.Equal(pointOrder(t, "x/"+name), []int{0, 1, 2}) {
			t.Fatal(name)
		}
		if time.Since(start) > time.Second {
			t.Fatal("hook blocked", name)
		}
	}
	m := current().list[0]
	if !m.pt.take(time.Second) {
		t.Fatal("cannot hold runtime")
	}
	start := time.Now()
	got := pointOrder(t, "x/duplicate")
	m.pt.give()
	if !slices.Equal(got, []int{0, 1, 2}) || time.Since(start) > time.Second {
		t.Fatal("busy runtime escaped limit")
	}
	if st := States()[e.Spec]; st.Failures != 8 {
		t.Fatalf("failures not reported %+v", st)
	}
}

func TestPointsUsageAllowListQueueAndStopJoin(t *testing.T) {
	oldQueue, oldLimit := usageQueue, usageLimit
	usageQueue, usageLimit = 2, 30*time.Millisecond
	defer func() { usageQueue, usageLimit = oldQueue, oldLimit }()
	e := file(t, `let rows=[];export function onUsage(row){if(row.input===999)for(;;){};rows.push(row)} export const strategies={seen:()=>{throw Error(JSON.stringify(rows))}}`)
	use(t, e)
	o := Points().(*observer)
	remove := usage.Observe(o)
	stopServing := usage.ServeObservers()
	defer remove()
	defer stopServing()
	cost := 0.5
	r := usage.Record{Time: time.Now(), Provider: "fixture", Model: "m", Input: 10, Output: 2, OwnCost: &cost, BodyIn: "SECRET_BODY", BodyOut: "SECRET_REPLY", Error: "SECRET_ERROR", Session: "SECRET_SESSION", RequestID: "SECRET_ID", ProviderAccount: "SECRET_ACCOUNT", CallerKeyName: "SECRET_KEY_NAME"}
	usage.Append(r)
	eventually(t, func() bool { return States()[e.Spec].Rows == 1 })
	_ = pointOrder(t, "x/seen")
	last := States()[e.Spec].Last
	if !strings.Contains(last, `"cost":0.5`) || !strings.Contains(last, `"input":10`) {
		t.Fatalf("onUsage did not see metadata %s", last)
	}
	for _, secret := range []string{"SECRET_BODY", "SECRET_REPLY", "SECRET_ERROR", "SECRET_SESSION", "SECRET_ID", "SECRET_ACCOUNT", "SECRET_KEY_NAME"} {
		if strings.Contains(last, secret) {
			t.Fatalf("usage plugin saw %s", secret)
		}
	}
	m := current().list[0]
	// This row spins inside the JS callback; its timeout must release the
	// runtime so a subsequent strategy and shutdown can complete.
	o.Ledger(usage.Record{Time: time.Now(), Input: 999})
	eventually(t, func() bool { return m.rows.Load() == 2 && m.failures.Load() >= 2 })
	if !m.pt.take(time.Second) {
		t.Fatal("cannot hold runtime")
	}
	start := time.Now()
	for i := 0; i < 1000; i++ {
		o.Ledger(r)
	}
	elapsed := time.Since(start)
	m.pt.give()
	if elapsed > time.Second || m.dropped.Load() == 0 {
		t.Fatal("queue blocked or did not count overflow", elapsed, m.dropped.Load())
	}
	o.Ledger(usage.Record{Time: time.Now(), Input: 999})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			o.Ledger(r)
		}
	}()
	remove()
	remove()
	wg.Wait()
	stopServing()
	stopServing()
	if routes.Served("x/seen") || o.offered.Load() != nil {
		t.Fatal("stop did not join and unregister")
	}
	before := m.rows.Load()
	o.Ledger(r)
	if m.rows.Load() != before {
		t.Fatal("stopped observer accepted another call")
	}
}

func TestPointsCheckedDiscoveryPreservesAndRepairs(t *testing.T) {
	e := file(t, `export const strategies={a:(p,s)=>[0,1,2]}`)
	use(t, e)
	stop := StartPoints()
	defer stop()
	listPath := filepath.Join(settings.Dir(), "plugins.json")
	valid, err := os.ReadFile(listPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(lastgood.Bak(listPath))
	for _, bad := range []string{`{"plugins":`, `{"plugins":[{"spec":123}]}`, `null`, `[]`} {
		if err := os.WriteFile(listPath, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Refresh(); err == nil {
			t.Fatal("broken list read as empty")
		}
		if _, err := On(); err == nil || !routes.Served("x/a") {
			t.Fatal("read failure unregistered valid strategies")
		}
		if States()["$discovery"].Error == "" {
			t.Fatal("read error absent from status")
		}
	}
	if err := os.WriteFile(listPath, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(listPath, []byte(`{"plugins":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Refresh(); err != nil {
		t.Fatal(err)
	}
	if routes.Served("x/a") {
		t.Fatal("intentional empty list did not unregister")
	}
}

func TestPointsManagedRefreshOnlySchedulesAndHashesImports(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "module.gateway.ts")
	dep := filepath.Join(dir, "dep.js")
	if err := os.WriteFile(dep, []byte(`export const v=1;`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte(`import {v} from "./dep.js";export const strategies={version:()=>v===1?[0,1,2]:[2,1,0]};export function onRequest(b){b.v=v;return b}`), 0600); err != nil {
		t.Fatal(err)
	}
	use(t, plugin.Entry{Spec: entry})
	stop := StartPoints()
	defer stop()
	before, err := Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	unmanage := ManagedRefresh(func() { requests.Add(1) })
	defer unmanage()
	if err := os.WriteFile(dep, []byte(`export const v=2;`), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := Fingerprint()
	if err != nil || before == after {
		t.Fatal("same-size import content did not change pure fingerprint", err)
	}
	nextCheck.Store(0)
	run := Begin(info())
	if run == nil {
		t.Fatal("missing managed run")
	}
	b, _ := run.Request([]byte(`{}`))
	run.End()
	if !bytes.Contains(b, []byte(`"v":1`)) || !slices.Equal(pointOrder(t, "module/version"), []int{0, 1, 2}) {
		t.Fatal("managed read committed before host drain")
	}
	Reload()
	eventually(t, func() bool { return requests.Load() >= 2 })
	if cur.Load().stamp != before {
		t.Fatal("managed reload changed generation")
	}
	if err := Refresh(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pointOrder(t, "module/version"), []int{2, 1, 0}) {
		t.Fatal("host commit did not publish new imports")
	}
	// An older Host's disposer must not clear its replacement scheduler.
	var newer atomic.Int64
	unmanage2 := ManagedRefresh(func() { newer.Add(1) })
	defer unmanage2()
	unmanage()
	Reload()
	if newer.Load() != 1 {
		t.Fatal("old scheduler stop removed newer Host")
	}
}

func TestGatewayDiscoveryAliasesAndSavedRoutings(t *testing.T) {
	for _, ext := range []string{".gateway.js", ".gateway.mjs", ".gateway.ts"} {
		p := filepath.Join(t.TempDir(), "alias"+ext)
		if err := os.WriteFile(p, []byte(`export const strategies={a:(p,s)=>s.map((_,i)=>i)}`), 0600); err != nil {
			t.Fatal(err)
		}
		got, only, err := plugin.MiddlewareChecked(p)
		if err != nil || got != p || !only {
			t.Fatal(got, only, err)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"magpie":{"gateway":"index.js"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if f, only, err := plugin.MiddlewareChecked(dir); err != nil || f != filepath.Join(dir, "index.js") || !only {
		t.Fatal(f, only, err)
	}
	stored := map[string]any{"providers": []any{map[string]any{"id": "hidden", "hidden": true, "routing": "fixture/custom"}, map[string]any{"id": "off", "off": true, "routing": "rotate"}}, "groups": []any{map[string]any{"id": "hidden-group", "hidden": true, "routing": "fixture/group"}}}
	b, _ := json.Marshal(stored)
	if err := os.WriteFile(provider.Path(), b, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(provider.Path())
	r, err := provider.Routings()
	if err != nil || !slices.Equal(r, []string{"fixture/custom", "rotate", "fixture/group"}) {
		t.Fatal(r, err)
	}
	if err := provider.Save(provider.Provider{ID: "custom", Name: "Fixture", Key: "synthetic-fixture-key", Chat: "https://fixture.invalid/v1", Models: []string{"m"}, Routing: "fixture/custom"}); err != nil {
		t.Fatal(err)
	}
	if p, err := provider.Find("custom"); err != nil || p.Routing != "fixture/custom" {
		t.Fatal("custom routing did not persist", err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "custom", Name: "Fixture", Members: []string{"custom/m"}, Routing: "fixture/group"}); err != nil {
		t.Fatal(err)
	}
	var storedAgain struct {
		Groups []provider.Group `json:"groups"`
	}
	saved, _ := os.ReadFile(provider.Path())
	if err := json.Unmarshal(saved, &storedAgain); err != nil || !slices.ContainsFunc(storedAgain.Groups, func(g provider.Group) bool { return g.ID == "custom" && g.Routing == "fixture/group" }) {
		t.Fatal("custom group routing did not persist", err)
	}
	_ = os.Remove(lastgood.Bak(provider.Path()))
	if err := os.WriteFile(provider.Path(), []byte(`{"providers":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Routings(); err == nil {
		t.Fatal("damaged saved routings read as empty")
	}
	for _, name := range []string{"fixture/custom", "scope-plugin/reverse_v2"} {
		if !provider.PluginRouting(name) {
			t.Fatal("valid custom route discarded")
		}
	}
	for _, name := range []string{"free", "plugin/../escape", "/name", "name/", "a/b/c", "a/with space"} {
		if provider.PluginRouting(name) {
			t.Fatal("invalid custom route allowed", name)
		}
	}
}
