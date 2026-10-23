// Package gatewayplugins owns the finite, headless gateway plugin assembly.
// Reconfiguration closes normal admission, drains accepted calls, reconciles,
// then reopens admission. It deliberately does not promise generation snapshots.
package gatewayplugins

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/gwmw"
	"github.com/ChisaAlter/WhaleBridge/internal/middleware"
	"github.com/ChisaAlter/WhaleBridge/internal/pluginrt"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

var ErrStopping = errors.New("gateway plugins are stopping")

// Host is owned by one serving process, never by gateway.New or package init.
// A control operation must not be called from inside a wrapped gateway request.
// Essential activation there is scheduled instead; the request fails closed.
type Host struct {
	rt                       *pluginrt.Runtime
	control                  chan struct{}
	mu                       sync.Mutex
	changed                  chan struct{}
	active                   int
	pending, closing, sealed bool
	applying                 bool
	stamp, lastError         string
	paths                    []string
	wake                     chan struct{}
	cancel                   context.CancelFunc
	done                     chan struct{}
	removeRefresh            func()
	removeManaged            func()
	quiesce                  sync.Once
	stop                     sync.Once
}

type Status struct {
	Plugins []pluginrt.Status `json:"plugins"`
	Pending bool              `json:"pending"`
	Active  int               `json:"active"`
	Error   string            `json:"error,omitempty"`
}

// Start applies the complete finite assembly before the caller publishes
// readiness or starts either HTTP listener. No failed first apply is advertised.
func Start() (*Host, error) {
	return start(Manifests(), []string{pluginrt.Path(), provider.Path(), settings.Path(), settings.Dir() + string(os.PathSeparator) + "plugins.json"}, time.Second)
}

func start(ms []pluginrt.Manifest, paths []string, every time.Duration) (*Host, error) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Host{rt: pluginrt.New(ms), control: make(chan struct{}, 1), changed: make(chan struct{}), paths: paths, wake: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{})}
	h.removeManaged = middleware.ManagedRefresh(h.ScheduleRefresh)
	if err := h.Refresh(context.Background()); err != nil {
		cancel()
		h.rt.Stop()
		h.rt.Wait()
		h.removeManaged()
		return nil, err
	}
	h.removeRefresh = gwmw.Refresh(h.ScheduleRefresh)
	go h.watch(ctx, every)
	return h, nil
}

func (h *Host) signalLocked() { close(h.changed); h.changed = make(chan struct{}) }

func (h *Host) fingerprint() string {
	digest := sha256.New()
	for _, path := range h.paths {
		fmt.Fprint(digest, path, "\x00")
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(digest, "error:%v\x00", err)
			continue
		}
		_, err = io.Copy(digest, f)
		_ = f.Close()
		fmt.Fprintf(digest, "\x00%v\x00", err)
	}
	stamp, err := middleware.Fingerprint()
	fmt.Fprintf(digest, "modules:%s\x00%v", stamp, err)
	return fmt.Sprintf("%x", digest.Sum(nil))
}

// Refresh is a control-plane operation. Its wait is cancelable, and no mutex is
// held across a request, an Apply, a disposer, or a worker join. Trusted Claude
// helper callbacks remain admitted while normal calls drain.
func (h *Host) Refresh(ctx context.Context) error {
	select {
	case h.control <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-h.control }()
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return ErrStopping
	}
	h.pending = true
	h.signalLocked()
	for h.active > 0 && !h.closing {
		ch := h.changed
		h.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			h.mu.Lock()
			h.pending = false
			h.signalLocked()
			h.mu.Unlock()
			return ctx.Err()
		}
		h.mu.Lock()
	}
	if h.closing {
		h.pending = false
		h.signalLocked()
		h.mu.Unlock()
		return ErrStopping
	}
	if err := ctx.Err(); err != nil {
		h.pending = false
		h.signalLocked()
		h.mu.Unlock()
		return err
	}
	h.applying = true
	h.mu.Unlock()
	stamp := h.fingerprint()
	_, err := h.rt.Refresh()
	// Active modules read their own files dynamically. A control refresh
	// still reloads them now, even when their manifest/config did not change.
	if err == nil && h.rt.Active("middleware") {
		err = middleware.Refresh()
	}
	h.mu.Lock()
	h.applying = false
	h.stamp = stamp
	h.lastError = ""
	if err != nil {
		h.lastError = err.Error()
	}
	h.pending = false
	h.signalLocked()
	h.mu.Unlock()
	return err
}

func (h *Host) ScheduleRefresh() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}
func (h *Host) watch(ctx context.Context, every time.Duration) {
	defer close(h.done)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-h.wake:
		}
		h.mu.Lock()
		old, retry, closing := h.stamp, h.lastError != "", h.closing
		h.mu.Unlock()
		if closing {
			return
		}
		if retry || h.fingerprint() != old {
			_ = h.Refresh(ctx)
		}
	}
}

// Quiesce prevents further control-plane commits and joins the watcher. It
// leaves registrations alive while the service drains. Helpers for an already
// accepted Claude turn still enter until WaitIdle atomically seals admission.
func (h *Host) Quiesce() {
	h.quiesce.Do(func() {
		h.mu.Lock()
		h.closing = true
		h.signalLocked()
		h.mu.Unlock()
		h.cancel()
		<-h.done
		h.control <- struct{}{}
		<-h.control
	})
}

// WaitIdle seals all admission when the accepted handlers, including helper
// callbacks and response Finish/usage bookkeeping, have returned.
func (h *Host) WaitIdle(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for h.active > 0 {
		ch := h.changed
		h.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			h.mu.Lock()
			return ctx.Err()
		}
		h.mu.Lock()
	}
	h.sealed = true
	h.signalLocked()
	return nil
}

// Stop is terminal and idempotent. The normal service owner drains HTTP before
// calling it; this extra wait also makes error paths safe.
func (h *Host) Stop() {
	h.stop.Do(func() {
		h.Quiesce()
		_ = h.WaitIdle(context.Background())
		h.rt.Stop()
		h.rt.Wait()
		if h.removeRefresh != nil {
			h.removeRefresh()
		}
		if h.removeManaged != nil {
			h.removeManaged()
		}
	})
}

func (h *Host) Status() Status {
	h.mu.Lock()
	s := Status{Pending: h.pending, Active: h.active, Error: h.lastError}
	h.mu.Unlock()
	s.Plugins = h.rt.Statuses()
	return s
}

func helper(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/_magpie/claude-mcp/")
}

func (h *Host) enter(ctx context.Context, isHelper bool) (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if h.sealed || h.closing && !isHelper {
			return nil, ErrStopping
		}
		if !h.pending || isHelper && !h.applying {
			h.active++
			return func() { h.mu.Lock(); h.active--; h.signalLocked(); h.mu.Unlock() }, nil
		}
		ch := h.changed
		h.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			h.mu.Lock()
			return nil, ctx.Err()
		}
		h.mu.Lock()
	}
}

// Handler leaves the original writer intact, preserving streaming/Flusher and
// ResponseController. The private gateway handler still authenticates helpers.
func (h *Host) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isHelper := helper(r)
		if !isHelper {
			h.mu.Lock()
			old, closing := h.stamp, h.closing
			h.mu.Unlock()
			if !closing && h.fingerprint() != old {
				_ = h.Refresh(r.Context())
			}
		}
		release, err := h.enter(r.Context(), isHelper)
		if err != nil {
			http.Error(w, "鲸Bridge正在重新配置或停止，请稍后再试", http.StatusServiceUnavailable)
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}
