package gatewayplugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/gwmw"
	"github.com/ChisaAlter/WhaleBridge/internal/middleware"
	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/pluginrt"
	"github.com/ChisaAlter/WhaleBridge/internal/routes"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
	"github.com/ChisaAlter/WhaleBridge/internal/usage"
)

func TestManagedJSStrategyRefreshDrainsPinnedRunAndStops(t *testing.T) {
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", t.TempDir())
	module := filepath.Join(t.TempDir(), "managed.gateway.js")
	const oldSource = `export function onRequest(b){b.generation="old";return b} export const strategies={oldrev:()=>[1,0]}`
	const newSource = `export function onRequest(b){b.generation="new";return b} export const strategies={newrev:()=>[0,1]}`
	if len(oldSource) != len(newSource) {
		t.Fatal("fixture must have equal-length generations")
	}
	if err := os.WriteFile(module, []byte(oldSource), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(settings.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	listPath := filepath.Join(settings.Dir(), "plugins.json")
	writeList := func(off bool) {
		t.Helper()
		b, err := json.Marshal(plugin.List{Plugins: []plugin.Entry{{Spec: module, Off: off}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(listPath, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeList(false)
	// Use the real finite assembly's middleware manifest, without starting
	// unrelated OTel or masking modules. Observers run exactly as in Run.
	var manifests []pluginrt.Manifest
	for _, m := range Manifests() {
		if m.ID == "middleware" {
			manifests = append(manifests, m)
		}
	}
	stopObservers := usage.ServeObservers()
	t.Cleanup(stopObservers)
	h, err := start(manifests, []string{pluginrt.Path(), settings.Path(), listPath}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pinned, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); h.Stop() })
	if !routes.Served("managed/oldrev") || len(gwmw.Agents()) != 1 || len(usage.Observers()) != 1 {
		t.Fatal("first Apply did not synchronously register hooks, strategies and observer")
	}
	var ended atomic.Bool
	var calls atomic.Int32
	wrapped := h.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		run := gwmw.Begin(gwmw.Info{Protocol: "chat", Model: "fixture", Path: r.URL.Path})
		if run == nil {
			http.Error(w, "missing managed hook", 500)
			return
		}
		isOld := r.URL.Path == "/old"
		defer func() {
			run.End()
			if isOld {
				ended.Store(true)
			}
		}()
		body, reject := run.Request([]byte(`{}`))
		if reject != nil {
			http.Error(w, "hook rejected fixture", 500)
			return
		}
		generation, name := "old", "managed/oldrev"
		if r.URL.Path == "/new" {
			generation, name = "new", "managed/newrev"
			if !ended.Load() {
				t.Error("new generation entered before old Run.End")
			}
		}
		if !strings.Contains(string(body), `"generation":"`+generation+`"`) {
			t.Errorf("wrong captured hook %s", body)
		}
		strategy, ok := routes.Find(name)
		if !ok {
			http.Error(w, "missing strategy "+name, 500)
			return
		}
		order := routes.Apply(strategy, "fixture", []routes.Seat{{Key: "a"}, {Key: "b"}})
		if isOld {
			close(pinned)
			<-release
			body, _ = run.Request([]byte(`{}`))
			if !strings.Contains(string(body), `"generation":"old"`) || !routes.Served(name) || routes.Served("managed/newrev") {
				t.Error("in-flight hook/strategy replaced before End")
			}
			if again := routes.Apply(strategy, "fixture", []routes.Seat{{Key: "a"}, {Key: "b"}}); again[0] != 1 {
				t.Error("pinned strategy lost old generation")
			}
		}
		fmt.Fprintf(w, "%s/%d", generation, order[0])
	}))
	call := func(path string, done chan<- *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		method := http.MethodGet
		if strings.HasPrefix(path, "/_magpie/claude-mcp/") {
			method = http.MethodPost
		}
		wrapped.ServeHTTP(rec, httptest.NewRequest(method, "http://127.0.0.1"+path, nil))
		done <- rec
	}
	wait := func(done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		t.Helper()
		select {
		case rec := <-done:
			return rec
		case <-time.After(3 * time.Second):
			t.Fatal("managed request did not complete")
			return nil
		}
	}
	oldDone := make(chan *httptest.ResponseRecorder, 1)
	go call("/old", oldDone)
	select {
	case <-pinned:
	case <-time.After(3 * time.Second):
		t.Fatal("old Run did not pin generation")
	}
	stat, err := os.Stat(module)
	if err != nil {
		t.Fatal(err)
	}
	before := h.fingerprint()
	if err := os.WriteFile(module, []byte(newSource), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(module, stat.ModTime(), stat.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(module)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != stat.Size() || !after.ModTime().Equal(stat.ModTime()) || before == h.fingerprint() {
		t.Fatal("same-size same-mtime module content change was missed")
	}
	newDone := make(chan *httptest.ResponseRecorder, 1)
	go call("/new", newDone)
	waitPending(t, h)
	if !routes.Served("managed/oldrev") || routes.Served("managed/newrev") || ended.Load() {
		t.Fatal("pending refresh retired pinned old generation")
	}
	select {
	case <-newDone:
		t.Fatal("new request bypassed drain")
	default:
	}
	helperDone := make(chan *httptest.ResponseRecorder, 1)
	go call("/_magpie/claude-mcp/fixture", helperDone)
	if rec := wait(helperDone); rec.Code != 200 || rec.Body.String() != "old/1" {
		t.Fatalf("helper lost old generation: %d %s", rec.Code, rec.Body)
	}
	unblock()
	if rec := wait(oldDone); rec.Code != 200 || rec.Body.String() != "old/1" {
		t.Fatalf("old Run lost generation: %d %s", rec.Code, rec.Body)
	}
	if rec := wait(newDone); rec.Code != 200 || rec.Body.String() != "new/0" {
		t.Fatalf("new Run did not use replacement: %d %s", rec.Code, rec.Body)
	}
	if !ended.Load() || routes.Served("managed/oldrev") || !routes.Served("managed/newrev") {
		t.Fatal("refresh did not replace strategy after old End")
	}
	// Disabling the local module retires its strategy before downstream is
	// admitted; the finite middleware feature itself may remain enabled.
	writeList(true)
	probe := h.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if routes.Served("managed/newrev") || gwmw.Begin(gwmw.Info{Model: "fixture", Protocol: "chat"}) != nil {
			t.Error("disabled module still served hooks or strategy")
		}
		w.WriteHeader(204)
	}))
	rec := httptest.NewRecorder()
	probe.ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1/probe", nil))
	if rec.Code != 204 || len(middleware.Routings()) != 0 {
		t.Fatal("module disable did not reconcile before admission")
	}
	h.Stop()
	h.Stop()
	if routes.Served("managed/oldrev") || routes.Served("managed/newrev") || len(gwmw.Agents()) != 0 || len(usage.Observers()) != 0 {
		t.Fatal("Stop left a registration active")
	}
	writeList(false)
	middleware.Reload()
	h.ScheduleRefresh()
	if err := h.Refresh(context.Background()); !errors.Is(err, ErrStopping) {
		t.Fatalf("stopped Host accepted refresh: %v", err)
	}
	beforeCalls := calls.Load()
	rec = httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1/new", nil))
	if rec.Code != 503 || calls.Load() != beforeCalls || routes.Served("managed/newrev") || len(gwmw.Agents()) != 0 || len(usage.Observers()) != 0 {
		t.Fatal("late reload resurrected stopped registrations or admitted work")
	}
}
