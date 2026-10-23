// Package pluginrt manages WhaleBridge's explicitly assembled, headless Go
// features. It is separate from the Bun supplier plugins in internal/plugin.
// Registrations captured by an in-flight request must remain callable after
// unregistering; this runtime does not promise an atomic snapshot across points.
package pluginrt

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const API = "1.0"

var (
	Core       = []string{"settings", "providers", "gateway"}
	ErrBusy    = errors.New("pluginrt: another operation is in progress")
	ErrStopped = errors.New("pluginrt: runtime is stopped")
	ErrStarted = errors.New("pluginrt: watcher already started")
)

// Manifest describes one deliberately assembled feature. Config is YAML data,
// composed from Defaults and the user's file. No GUI or settings schema is used.
type Manifest struct {
	ID, API            string
	Requires, Provides []string
	Essential          bool
	Name, Sub          string
	InUse              func() (int, error)
	Defaults           map[string]any
	Apply              func(*Ctx) error
}

type State string

const (
	Off      State = "off"
	Pending  State = "pending"
	Active   State = "active"
	Failed   State = "failed"
	Disposed State = "disposed"
)

type Status struct {
	ID    string `json:"id"`
	State State  `json:"state"`
	Why   string `json:"why,omitempty"`
}

// Ctx is one application of a manifest, not shared with its replacement.
// Config is an independent deep copy and must be treated as read-only.
// Hooks register synchronously in Apply. Workers should use Go and honor its
// cancellation; they must never Wait on their own runtime during disposal.
type Ctx struct {
	ID              string
	Config          map[string]any
	rt              *Runtime
	mu              sync.Mutex
	closed          bool
	retire, dispose []func()
	provided        map[string]any
	worker          context.Context
	cancel          context.CancelFunc
	workers         sync.WaitGroup
}

// OnRetire unregisters a point before workers stop. Old captured references
// must still be safe; OnRetire is not an in-flight request barrier.
func (c *Ctx) OnRetire(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		panic("pluginrt: registering on retired context")
	}
	c.retire = append(c.retire, f)
}

// OnDispose releases resources after OnRetire and all Ctx.Go workers exit.
// Both hook lists run newest first, outside all runtime state locks.
func (c *Ctx) OnDispose(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		panic("pluginrt: registering on disposed context")
	}
	c.dispose = append(c.dispose, f)
}

// Go starts a cancellable worker tracked by retirement and Stop/Wait.
func (c *Ctx) Go(f func(context.Context)) bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	c.workers.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.workers.Done()
		defer func() {
			if p := recover(); p != nil {
				log.Printf("plugins: %s worker panic: %v", c.ID, p)
			}
		}()
		f(c.worker)
	}()
	return true
}

// Provide stages a service until Apply succeeds. A failing Apply never
// publishes partial services to another plugin.
func (c *Ctx) Provide(name string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		panic("pluginrt: providing from retired context")
	}
	c.provided[name] = v
}
func (c *Ctx) Service(name string) any {
	c.mu.Lock()
	v, own := c.provided[name]
	c.mu.Unlock()
	if own {
		return v
	}
	c.rt.mu.Lock()
	defer c.rt.mu.Unlock()
	return c.rt.services[name].value
}

func (c *Ctx) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	retire, dispose := c.retire, c.dispose
	c.retire, c.dispose = nil, nil
	c.mu.Unlock()
	undo(c.ID, retire)
	c.cancel()
	c.workers.Wait()
	undo(c.ID, dispose)
}
func undo(id string, hooks []func()) {
	for i := len(hooks) - 1; i >= 0; i-- {
		func() {
			defer func() {
				if p := recover(); p != nil {
					log.Printf("plugins: %s cleanup panic: %v", id, p)
				}
			}()
			if hooks[i] != nil {
				hooks[i]()
			}
		}()
	}
}

type service struct {
	owner *Ctx
	value any
}
type running struct {
	m      Manifest
	state  State
	why    string
	wanted bool
	ctx    *Ctx
	config map[string]any
}

// Runtime serializes mutations with a non-waiting operation gate. External
// hooks hold neither its state lock nor any request-long read lock. Reentrant
// Refresh/Reconcile returns ErrBusy, including calls from Apply/disposers.
type Runtime struct {
	mu                           sync.Mutex
	op                           sync.Mutex
	plugins                      map[string]*running
	order                        []string
	services                     map[string]service
	reconciled, stopped, started bool
	watchCancel                  context.CancelFunc
	watchDone                    chan struct{}
	stopOnce                     sync.Once
	stopDone                     chan struct{}
}

func New(ms []Manifest) *Runtime {
	rt := &Runtime{plugins: map[string]*running{}, services: map[string]service{}, stopDone: make(chan struct{})}
	for _, name := range Core {
		rt.services[name] = service{value: struct{}{}}
	}
	for _, m := range ms {
		if m.ID == "" || rt.plugins[m.ID] != nil {
			panic("pluginrt: empty or duplicate manifest id: " + m.ID)
		}
		m.Defaults = cloneMap(m.Defaults)
		m.Requires, m.Provides = slices.Clone(m.Requires), slices.Clone(m.Provides)
		rt.plugins[m.ID] = &running{m: m, state: Off}
	}
	return rt
}

func (rt *Runtime) begin() error {
	rt.mu.Lock()
	stopped := rt.stopped
	rt.mu.Unlock()
	if stopped {
		return ErrStopped
	}
	if !rt.op.TryLock() {
		return ErrBusy
	}
	rt.mu.Lock()
	stopped = rt.stopped
	rt.mu.Unlock()
	if stopped {
		rt.op.Unlock()
		return ErrStopped
	}
	return nil
}

// Reconcile applies synchronously. Changing an active config retires that
// application and its dependents, then reapplies them with the new config.
// Unchanged active applications are retained. If a changed active application
// or an active dependent cannot restart, its previous configuration chain is
// restored best-effort. This is not an atomic generation swap: both the failed
// application and restoration may have external effects. The original error
// is returned even when restoring the previous active chain succeeds.
func (rt *Runtime) Reconcile(on map[string]bool, config map[string]map[string]any) error {
	if err := rt.begin(); err != nil {
		return err
	}
	defer rt.op.Unlock()
	return rt.reconcile(on, config)
}

func (rt *Runtime) reconcile(on map[string]bool, config map[string]map[string]any) error {
	// Keep only the previous active, still-wanted chain affected by a
	// configuration change. An explicit disable must not be rolled back.
	rt.mu.Lock()
	previous := map[string]map[string]any{}
	rollback := map[string]map[string]any{}
	for id, p := range rt.plugins {
		cfg, _ := merge(p.m.Defaults, config[id]).(map[string]any)
		rollback[id] = cfg
		if p.ctx != nil && on[id] && !reflect.DeepEqual(p.config, cfg) {
			previous[id] = cloneMap(p.config)
		}
	}
	for {
		more := false
		for _, id := range rt.order {
			if _, included := previous[id]; included || !on[id] {
				continue
			}
			p := rt.plugins[id]
			for name, svc := range rt.services {
				if svc.owner == nil {
					continue
				}
				if _, changed := previous[svc.owner.ID]; changed && slices.Contains(p.m.Requires, name) {
					previous[id], more = cloneMap(p.config), true
					break
				}
			}
		}
		if !more {
			break
		}
	}
	rt.mu.Unlock()
	err := rt.reconcileOnce(on, config)
	if err == nil || len(previous) == 0 {
		return err
	}
	rt.mu.Lock()
	failed := false
	for id, old := range previous {
		rollback[id] = old
		failed = failed || rt.plugins[id].state != Active
	}
	stopped := rt.stopped
	rt.mu.Unlock()
	if !failed || stopped {
		return err
	}
	// Only one restoration attempt: a previous external dependency may no
	// longer be usable. Report both errors instead of hiding either failure.
	if restoreErr := rt.reconcileOnce(on, rollback); restoreErr != nil {
		return errors.Join(err, fmt.Errorf("restoring previous plugin configuration: %w", restoreErr))
	}
	return err
}

func (rt *Runtime) reconcileOnce(on map[string]bool, config map[string]map[string]any) error {
	rt.mu.Lock()
	if rt.stopped {
		rt.mu.Unlock()
		return ErrStopped
	}
	rt.reconciled = true
	configs := map[string]map[string]any{}
	going := map[string]bool{}
	for id, p := range rt.plugins {
		cfg, _ := merge(p.m.Defaults, config[id]).(map[string]any)
		configs[id] = cfg
		changed := !reflect.DeepEqual(p.config, cfg)
		if p.ctx != nil && (!on[id] || changed) {
			going[id] = true
		}
		// A new explicit refresh retries a transient Apply failure even
		// if YAML is unchanged (the module or another input may be fixed).
		if p.state == Failed {
			p.state, p.why = Off, ""
		}
		p.wanted = on[id]
	}
	for {
		more := false
		for _, id := range rt.order {
			p := rt.plugins[id]
			if going[id] {
				continue
			}
			for name, svc := range rt.services {
				if svc.owner != nil && going[svc.owner.ID] && slices.Contains(p.m.Requires, name) {
					going[id], more = true, true
					break
				}
			}
		}
		if !more {
			break
		}
	}
	var retiring []string
	for i := len(rt.order) - 1; i >= 0; i-- {
		if id := rt.order[i]; going[id] {
			retiring = append(retiring, id)
		}
	}
	ids := make([]string, 0, len(rt.plugins))
	for id := range rt.plugins {
		ids = append(ids, id)
	}
	rt.mu.Unlock()
	for _, id := range retiring {
		rt.mu.Lock()
		c := rt.detach(rt.plugins[id])
		rt.mu.Unlock()
		c.close()
	}
	sort.Strings(ids)
	for {
		progress := false
		for _, id := range ids {
			rt.mu.Lock()
			if rt.stopped {
				rt.mu.Unlock()
				return ErrStopped
			}
			p := rt.plugins[id]
			if !p.wanted || p.state == Active || p.state == Failed {
				rt.mu.Unlock()
				continue
			}
			if !Compatible(p.m.API) {
				p.state, p.why, p.config = Failed, "incompatible host API "+p.m.API, cloneMap(configs[id])
				rt.mu.Unlock()
				continue
			}
			var missing []string
			for _, name := range p.m.Requires {
				if _, ok := rt.services[name]; !ok {
					missing = append(missing, name)
				}
			}
			if len(missing) > 0 {
				p.state, p.why = Pending, "waiting for "+strings.Join(missing, ", ")
				rt.mu.Unlock()
				continue
			}
			p.config = cloneMap(configs[id])
			rt.mu.Unlock()
			if rt.apply(p, configs[id]) {
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var errs []error
	for _, id := range ids {
		p := rt.plugins[id]
		if !p.wanted {
			if p.state != Disposed {
				p.state, p.why = Off, ""
			}
			continue
		}
		if p.state == Failed || p.state == Pending {
			errs = append(errs, fmt.Errorf("%s: %s", id, p.why))
		}
	}
	return errors.Join(errs...)
}

// detach updates only state, under rt.mu. No external callback is invoked.
func (rt *Runtime) detach(p *running) *Ctx {
	c := p.ctx
	for name, svc := range rt.services {
		if svc.owner == c {
			delete(rt.services, name)
		}
	}
	p.ctx, p.state, p.why = nil, Disposed, ""
	rt.order = slices.DeleteFunc(rt.order, func(id string) bool { return id == p.m.ID })
	return c
}
func (rt *Runtime) apply(p *running, config map[string]any) bool {
	worker, cancel := context.WithCancel(context.Background())
	c := &Ctx{ID: p.m.ID, Config: cloneMap(config), rt: rt, provided: map[string]any{}, worker: worker, cancel: cancel}
	err := func() (err error) {
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("panic: %v", v)
			}
		}()
		if p.m.Apply != nil {
			return p.m.Apply(c)
		}
		return nil
	}()
	rt.mu.Lock()
	if err == nil {
		if rt.stopped {
			err = ErrStopped
		}
		c.mu.Lock()
		for name := range c.provided {
			if _, exists := rt.services[name]; exists {
				err = fmt.Errorf("service already provided: %s", name)
				break
			}
		}
		if err == nil {
			for name, value := range c.provided {
				rt.services[name] = service{owner: c, value: value}
			}
			p.ctx, p.state, p.why = c, Active, ""
			rt.order = append(rt.order, p.m.ID)
		}
		c.mu.Unlock()
	}
	if err != nil {
		p.ctx, p.state, p.why = nil, Failed, err.Error()
	}
	rt.mu.Unlock()
	if err != nil {
		c.close()
		return false
	}
	return true
}

// Stop requests shutdown exactly once and returns without waiting. It can be
// called safely by a callback or worker. The owner must first drain requests,
// then call Stop and Wait. Wait must not be called inside a runtime callback.
func (rt *Runtime) Stop() {
	rt.stopOnce.Do(func() {
		rt.mu.Lock()
		rt.stopped = true
		cancel, watchDone := rt.watchCancel, rt.watchDone
		rt.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		go func() {
			defer close(rt.stopDone)
			if watchDone != nil {
				<-watchDone
			}
			rt.op.Lock()
			defer rt.op.Unlock()
			for {
				rt.mu.Lock()
				if len(rt.order) == 0 {
					rt.mu.Unlock()
					break
				}
				c := rt.detach(rt.plugins[rt.order[len(rt.order)-1]])
				rt.mu.Unlock()
				c.close()
			}
		}()
	})
}
func (rt *Runtime) Wait() { <-rt.stopDone }
func (rt *Runtime) Statuses() []Status {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make([]Status, 0, len(rt.plugins))
	for id, p := range rt.plugins {
		out = append(out, Status{ID: id, State: p.state, Why: p.why})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (rt *Runtime) Active(id string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	p := rt.plugins[id]
	return p != nil && p.state == Active
}

var regMu sync.Mutex
var registry = map[string]Manifest{}

func Register(m Manifest) {
	regMu.Lock()
	defer regMu.Unlock()
	if m.ID == "" {
		panic("pluginrt: empty manifest id")
	}
	if _, exists := registry[m.ID]; exists {
		panic("pluginrt: duplicate manifest " + m.ID)
	}
	registry[m.ID] = m
}
func Registered() []Manifest {
	regMu.Lock()
	defer regMu.Unlock()
	ms := make([]Manifest, 0, len(registry))
	for _, m := range registry {
		ms = append(ms, m)
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].ID < ms[j].ID })
	return ms
}
func Compatible(want string) bool {
	if want == "" {
		want = "^1"
	}
	v, ok := strings.CutPrefix(want, "^")
	if !ok {
		return false
	}
	a, b, ok := version(v)
	x, y, hostOK := version(API)
	return ok && hostOK && a == x && b <= y
}
func version(v string) (int, int, bool) {
	major, minor, _ := strings.Cut(v, ".")
	a, err := strconv.Atoi(major)
	if err != nil || a < 0 {
		return 0, 0, false
	}
	if minor == "" {
		return a, 0, true
	}
	minor, _, _ = strings.Cut(minor, ".")
	b, err := strconv.Atoi(minor)
	return a, b, err == nil && b >= 0
}
