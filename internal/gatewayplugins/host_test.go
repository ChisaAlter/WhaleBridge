package gatewayplugins

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/pluginrt"
)

func testHost(t *testing.T, manifests []pluginrt.Manifest) (*Host, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", dir)
	path := pluginrt.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	h, err := start(manifests, []string{path}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	return h, path
}
func waitPending(t *testing.T, h *Host) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		if h.Status().Pending {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("refresh did not close admission")
		case <-time.After(time.Millisecond):
		}
	}
}
func waitResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("control operation did not finish")
		return nil
	}
}

func TestHostRefreshDrainsWhileHelperCanFinishAcceptedTurn(t *testing.T) {
	var active atomic.Bool
	manifest := pluginrt.Manifest{ID: "fixture", API: "^1", Apply: func(c *pluginrt.Ctx) error {
		active.Store(true)
		c.OnRetire(func() { active.Store(false) })
		return nil
	}}
	h, path := testHost(t, []pluginrt.Manifest{manifest})
	release, err := h.enter(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("plugins:\n- id: fixture\n  enabled: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	refreshed := make(chan error, 1)
	go func() { refreshed <- h.Refresh(context.Background()) }()
	waitPending(t, h)
	if !active.Load() {
		t.Fatal("retired before accepted turn finished")
	}
	queuedCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queued := make(chan error, 1)
	go func() {
		done, err := h.enter(queuedCtx, false)
		if err == nil {
			done()
		}
		queued <- err
	}()
	select {
	case <-queued:
		t.Fatal("normal admission bypassed pending refresh")
	default:
	}
	// A helper must pass the gate while its existing parent call is waiting
	// for it. The private handler after this wrapper still checks its origin.
	entered := false
	wrapped := h.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered = true
		if !active.Load() {
			t.Error("helper lost registered plugin")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, httptest.NewRequest("POST", "http://127.0.0.1/_magpie/claude-mcp/fixture", nil))
	if w.Code != 204 || !entered {
		t.Fatal("helper blocked behind the refresh waiting for its parent")
	}
	release()
	if err := waitResult(t, refreshed); err != nil {
		t.Fatal(err)
	}
	if active.Load() {
		t.Fatal("refresh did not retire the now-unused plugin")
	}
	if err := waitResult(t, queued); err != nil {
		t.Fatal(err)
	}
}

func TestHostQuiesceJoinsPendingRefreshWithoutRetiringRequest(t *testing.T) {
	var retired atomic.Bool
	h, _ := testHost(t, []pluginrt.Manifest{{ID: "fixture", API: "^1", Apply: func(c *pluginrt.Ctx) error { c.OnRetire(func() { retired.Store(true) }); return nil }}})
	release, err := h.enter(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	refreshed := make(chan error, 1)
	go func() { refreshed <- h.Refresh(context.Background()) }()
	waitPending(t, h)
	quiesced := make(chan error, 1)
	go func() { h.Quiesce(); quiesced <- nil }()
	if err := waitResult(t, quiesced); err != nil {
		t.Fatal(err)
	}
	if err := waitResult(t, refreshed); !errors.Is(err, ErrStopping) {
		t.Fatalf("pending refresh after quiesce: %v", err)
	}
	if retired.Load() {
		t.Fatal("quiesce disposed before response finished")
	}
	if _, err := h.enter(context.Background(), false); !errors.Is(err, ErrStopping) {
		t.Fatalf("new work admitted during shutdown: %v", err)
	}
	helperDone, err := h.enter(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	helperDone()
	stopped := make(chan error, 1)
	go func() { h.Stop(); stopped <- nil }()
	select {
	case <-stopped:
		t.Fatal("stop did not wait for accepted response")
	default:
	}
	release()
	if err := waitResult(t, stopped); err != nil {
		t.Fatal(err)
	}
	if !retired.Load() {
		t.Fatal("stop did not retire")
	}
	if err := h.Refresh(context.Background()); !errors.Is(err, ErrStopping) {
		t.Fatalf("late refresh resurrected stopped host: %v", err)
	}
	if _, err := h.enter(context.Background(), true); !errors.Is(err, ErrStopping) {
		t.Fatalf("helper admitted after final seal: %v", err)
	}
}

func TestHostFingerprintReadsContentsAndCanceledWaitReopensAdmission(t *testing.T) {
	var applies atomic.Int32
	h, path := testHost(t, []pluginrt.Manifest{{ID: "fixture", API: "^1", Apply: func(c *pluginrt.Ctx) error { applies.Add(1); return nil }}})
	const on = "plugins:\n- id: fixture\n  enabled: true \n"
	const off = "plugins:\n- id: fixture\n  enabled: false\n"
	if err := os.WriteFile(path, []byte(on), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before := h.fingerprint()
	if err := os.WriteFile(path, []byte(off), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if h.fingerprint() == before {
		t.Fatal("same-length same-time content edit was missed")
	}
	release, err := h.enter(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	refreshed := make(chan error, 1)
	go func() { refreshed <- h.Refresh(ctx) }()
	waitPending(t, h)
	cancel()
	if err := waitResult(t, refreshed); !errors.Is(err, context.Canceled) {
		t.Fatalf("uncancelable refresh wait: %v", err)
	}
	if h.Status().Pending {
		t.Fatal("canceled refresh kept normal admission closed")
	}
	release()
	var calls int
	wrapped := h.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "http://127.0.0.1/v1/chat/completions", nil))
	if calls != 1 || h.rt.Active("fixture") {
		t.Fatal("next request did not synchronously apply edited plugin configuration")
	}
	if applies.Load() != 1 {
		t.Fatal("unrelated refresh reapplied unchanged manifest")
	}
}

func TestHostCanceledRequestCannotEnterAfterRefreshWait(t *testing.T) {
	h, path := testHost(t, []pluginrt.Manifest{{ID: "fixture", API: "^1"}})
	release, err := h.enter(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("plugins:\n- id: fixture\n  enabled: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	wrapped := h.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	done := make(chan error, 1)
	go func() {
		wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "http://127.0.0.1/v1/chat/completions", nil).WithContext(ctx))
		done <- nil
	}()
	waitPending(t, h)
	cancel()
	if err := waitResult(t, done); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || h.Status().Active != 1 {
		t.Fatal("canceled refresh waiter was admitted downstream")
	}
	release()
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "http://127.0.0.1/v1/chat/completions", nil))
	if calls.Load() != 1 || h.rt.Active("fixture") {
		t.Fatal("fresh request did not reconcile after canceled waiter")
	}
}

func TestHostAlreadyCanceledControlDoesNotCommit(t *testing.T) {
	h, path := testHost(t, []pluginrt.Manifest{{ID: "fixture", API: "^1"}})
	if err := os.WriteFile(path, []byte("plugins:\n- id: fixture\n  enabled: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled control refresh committed: %v", err)
	}
	if !h.rt.Active("fixture") {
		t.Fatal("canceled control changed active plugin")
	}
	if err := h.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.rt.Active("fixture") {
		t.Fatal("fresh control refresh failed to retire plugin")
	}
}

func TestHostFailedInitialApplyRollsBackBeforeReadiness(t *testing.T) {
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", t.TempDir())
	var cleanup atomic.Bool
	h, err := start([]pluginrt.Manifest{{ID: "bad", API: "^1", Apply: func(c *pluginrt.Ctx) error {
		c.OnRetire(func() { cleanup.Store(true) })
		return errors.New("fixture apply failure")
	}}}, nil, time.Hour)
	if err == nil || h != nil || !cleanup.Load() {
		t.Fatalf("startup advertised partial registration or did not rollback: host=%v error=%v cleanup=%v", h, err, cleanup.Load())
	}
}
