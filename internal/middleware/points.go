package middleware

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Calcium-Ion/moejs"
	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/routes"
	"github.com/ChisaAlter/WhaleBridge/internal/usage"
)

var strategyLimit = 20 * time.Millisecond
var usageLimit = 250 * time.Millisecond
var usageQueue = 1024
var strategyName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var badPrefix = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
var errBusy = errors.New("its runtime was busy past the time a strategy has")

type strat struct {
	name string
	h    moejs.Hook
}
type points struct {
	in  *inst
	sem chan struct{}
}

func (m *mw) discover(in *inst) error {
	m.onUse, m.hasUse = in.hook("onUsage")
	names, err := in.keys("strategies")
	if err != nil {
		return err
	}
	for _, name := range names {
		if !strategyName.MatchString(name) {
			return fmt.Errorf("strategy %q has an invalid name", name)
		}
		h, ok := in.member(name)
		if !ok {
			return fmt.Errorf("strategy %q is not a function or an order function", name)
		}
		m.strats = append(m.strats, strat{name, h})
	}
	if len(m.strats) == 0 && !m.hasUse {
		return nil
	}
	pt, err := m.newInst()
	if err != nil {
		return err
	}
	m.pt = &points{in: pt, sem: make(chan struct{}, 1)}
	return nil
}
func (in *inst) keys(name string) ([]string, error) {
	v, ok := in.rt.Export(name)
	if !ok || v.IsUndefined() {
		d, ok := in.rt.Export("default")
		if !ok || !d.IsObject() {
			return nil, nil
		}
		v, _ = in.rt.Get(d, name)
		if v.IsUndefined() {
			return nil, nil
		}
	}
	if !v.IsObject() {
		return nil, fmt.Errorf("%s is not an object", name)
	}
	g, err := in.rt.ToGo(v)
	if err != nil {
		return nil, err
	}
	obj, ok := g.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an object", name)
	}
	names := make([]string, 0, len(obj))
	for n := range obj {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}
func (in *inst) member(name string) (moejs.Hook, bool) {
	for _, path := range [][]string{{"strategies", name}, {"strategies", name, "order"}} {
		for _, prefix := range []string{"", "default"} {
			p := path
			if prefix != "" {
				p = append([]string{prefix}, path...)
			}
			h, err := in.m.mod.Hook(p[0], p[1:]...)
			if err == nil {
				if ok, _ := in.rt.Has(h); ok {
					return h, true
				}
			}
		}
	}
	return moejs.Hook{}, false
}
func (p *points) take(wait time.Duration) bool {
	if wait <= 0 {
		return false
	}
	select {
	case p.sem <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case p.sem <- struct{}{}:
		return true
	case <-t.C:
		return false
	}
}
func (p *points) give() { p.in.rt.ReleaseCallData(); <-p.sem }
func (m *mw) routing(name string) string {
	prefix := plugin.ShortName(m.name)
	if plugin.IsPath(m.spec) {
		prefix = filepath.Base(m.spec)
		for _, ext := range []string{".js", ".mjs", ".ts", ".cjs", ".middleware", ".gateway"} {
			prefix = strings.TrimSuffix(prefix, ext)
		}
	}
	prefix = strings.Trim(badPrefix.ReplaceAllString(prefix, "-"), "-")
	if prefix == "" {
		prefix = "plugin"
	}
	return prefix + "/" + name
}

type seatJSON struct {
	Key     string  `json:"key"`
	Account bool    `json:"account"`
	Known   bool    `json:"known"`
	Learns  bool    `json:"learns"`
	Used    float64 `json:"used"`
	Soon    []int64 `json:"soon"`
	Pace    float64 `json:"pace"`
	Weight  int     `json:"weight"`
	Resting bool    `json:"resting"`
	Tokens  float64 `json:"tokens"`
	Free    bool    `json:"free"`
}

func identity(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}
func (m *mw) strategy(st strat, closed *atomic.Bool) routes.Strategy {
	return routes.Strategy{Order: func(pool string, seats []routes.Seat) []int {
		if closed.Load() {
			return identity(len(seats))
		}
		out, err := m.order(st.h, pool, seats)
		if err != nil {
			m.fail("strategies."+st.name, err)
			return identity(len(seats))
		}
		return out
	}}
}
func (m *mw) order(h moejs.Hook, pool string, seats []routes.Seat) ([]int, error) {
	start := time.Now()
	rows := make([]seatJSON, len(seats))
	for i, s := range seats {
		soon := make([]int64, len(s.Soon))
		for j, t := range s.Soon {
			soon[j] = t.UnixMilli()
		}
		rows[i] = seatJSON{s.Key, s.Account, s.Known, s.Learns, s.Used, soon, s.Pace, s.Weight, s.Resting, s.Tokens, s.Free}
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	p := m.pt
	if !p.take(strategyLimit - time.Since(start)) {
		return nil, errBusy
	}
	defer p.give()
	rt := p.in.rt
	poolV, err := rt.FromGo(pool)
	if err != nil {
		return nil, err
	}
	seatsV, err := rt.ParseJSON(b)
	if err != nil {
		return nil, err
	}
	opts, err := rt.ParseJSON(m.options)
	if err != nil {
		return nil, err
	}
	left := strategyLimit - time.Since(start)
	if left <= 0 {
		return nil, errBusy
	}
	v, err := p.in.call(h, left, poolV, seatsV, opts)
	m.orders.Add(1)
	if err != nil {
		return nil, err
	}
	out, err := rt.AppendJSON(nil, v)
	var indexes []*float64
	if err != nil || json.Unmarshal(out, &indexes) != nil || len(indexes) != len(seats) {
		return nil, errors.New("returned an invalid seat order")
	}
	order, seen := make([]int, len(seats)), make([]bool, len(seats))
	for i, x := range indexes {
		if x == nil || *x != math.Trunc(*x) || *x < 0 || *x >= float64(len(seats)) || seen[int(*x)] {
			return nil, errors.New("returned an invalid seat order")
		}
		order[i], seen[int(*x)] = int(*x), true
	}
	return order, nil
}

// Explicit metadata allow-list: bodies, error text, IDs and sessions never enter
// the asynchronous plugin queue. The existing OTel path retains ownership.
type usageRow struct {
	Time       int64    `json:"time"`
	Operation  string   `json:"operation,omitempty"`
	Agent      string   `json:"agent"`
	Provider   string   `json:"provider"`
	Model      string   `json:"model"`
	Requested  string   `json:"requested,omitempty"`
	Served     string   `json:"served,omitempty"`
	Input      int      `json:"input"`
	Output     int      `json:"output"`
	CacheRead  int      `json:"cacheRead"`
	CacheWrite int      `json:"cacheWrite"`
	Reasoning  int      `json:"reasoning"`
	Ms         int64    `json:"ms"`
	TTFTMs     int64    `json:"ttftMs"`
	Status     int      `json:"status"`
	ErrType    string   `json:"errType,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Rejected   bool     `json:"rejected"`
	Tries      int      `json:"tries,omitempty"`
	Cost       *float64 `json:"cost"`
}

func rowOf(r usage.Record) usageRow {
	row := usageRow{Time: r.Time.UnixMilli(), Operation: r.Operation, Agent: r.Agent, Provider: r.Provider, Model: r.Model, Requested: r.Requested, Served: r.Served, Input: r.Input, Output: r.Output, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite, Reasoning: r.Reasoning, Ms: r.Millis, TTFTMs: r.TTFT, Status: r.Status, ErrType: r.ErrType, Kind: r.Kind, Rejected: r.Rejected, Tries: r.Tries}
	if r.OwnCost != nil {
		cost := *r.OwnCost
		row.Cost = &cost
	}
	return row
}
func (m *mw) use(b []byte, closed *atomic.Bool) {
	if closed.Load() {
		return
	}
	p := m.pt
	if !p.take(usageLimit) {
		m.fail("onUsage", errBusy)
		return
	}
	defer p.give()
	if closed.Load() {
		return
	}
	row, err := p.in.rt.ParseJSON(b)
	if err == nil {
		opts, e := p.in.rt.ParseJSON(m.options)
		err = e
		if e == nil {
			_, err = p.in.call(m.onUse, usageLimit, row, opts)
		}
	}
	m.rows.Add(1)
	if err != nil {
		m.fail("onUsage", err)
	}
}

type generation struct {
	set     *set
	queue   chan []byte
	done    chan struct{}
	wg      sync.WaitGroup
	closed  atomic.Bool
	removes []func()
}

func (g *generation) stop() {
	g.closed.Store(true)
	for _, remove := range g.removes {
		remove()
	}
	close(g.done)
	g.wg.Wait()
}
func newGeneration(s *set) *generation {
	g := &generation{set: s, queue: make(chan []byte, usageQueue), done: make(chan struct{})}
	for _, m := range s.list {
		for _, st := range m.strats {
			g.removes = append(g.removes, routes.Register(m.routing(st.name), m.strategy(st, &g.closed)))
		}
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		for {
			select {
			case <-g.done:
				return
			case b := <-g.queue:
				if g.closed.Load() {
					return
				}
				for _, m := range s.list {
					if m.hasUse {
						m.use(b, &g.closed)
					}
				}
			}
		}
	}()
	return g
}

type observer struct {
	mu      sync.Mutex
	current *generation
	offered atomic.Pointer[generation]
	stopped bool
	done    chan struct{}
	wg      sync.WaitGroup
}

var livePoints = struct {
	sync.Mutex
	items map[*observer]bool
}{items: map[*observer]bool{}}

func Points() usage.Observer { return &observer{} }

// StartPoints is convenient for direct consumers; assembly normally registers
// Points with usage.Observe and calls usage.ServeObservers before accepting.
func StartPoints() func() {
	o := Points()
	remove := usage.Observe(o)
	stop := usage.ServeObservers()
	var once sync.Once
	return func() { once.Do(func() { remove(); stop() }) }
}
func syncPoints(s *set) {
	livePoints.Lock()
	list := make([]*observer, 0, len(livePoints.items))
	for o := range livePoints.items {
		list = append(list, o)
	}
	livePoints.Unlock()
	for _, o := range list {
		o.swap(s)
	}
}
func (o *observer) swap(s *set) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stopped || o.current != nil && o.current.set == s {
		return
	}
	if o.current != nil {
		o.offered.Store(nil)
		o.current.stop()
	}
	o.current = newGeneration(s)
	o.offered.Store(o.current)
}
func (o *observer) Serve() func() {
	o.mu.Lock()
	o.stopped = false
	o.done = make(chan struct{})
	done := o.done
	o.mu.Unlock()
	livePoints.Lock()
	livePoints.items[o] = true
	livePoints.Unlock()
	refreshRequested()
	if s := cur.Load(); s != nil {
		o.swap(s)
	}
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				refreshRequested()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			livePoints.Lock()
			delete(livePoints.items, o)
			livePoints.Unlock()
			o.mu.Lock()
			o.stopped = true
			o.offered.Store(nil)
			if o.current != nil {
				o.current.stop()
				o.current = nil
			}
			close(done)
			o.mu.Unlock()
			o.wg.Wait()
		})
	}
}
func (o *observer) Ledger(r usage.Record) {
	g := o.offered.Load()
	if g == nil || g.closed.Load() {
		return
	}
	b, err := json.Marshal(rowOf(r))
	if err != nil {
		return
	}
	select {
	case g.queue <- b:
	default:
		for _, m := range g.set.list {
			if m.hasUse {
				m.dropped.Add(1)
			}
		}
	}
}

type Routing struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Plugin string `json:"plugin"`
}

func Routings() []Routing {
	_ = current()
	livePoints.Lock()
	list := make([]*observer, 0, len(livePoints.items))
	for o := range livePoints.items {
		list = append(list, o)
	}
	livePoints.Unlock()
	out := []Routing{}
	seen := map[string]bool{}
	for _, o := range list {
		o.mu.Lock()
		if g := o.current; g != nil && !g.closed.Load() {
			for _, m := range g.set.list {
				for _, st := range m.strats {
					id := m.routing(st.name)
					if !seen[id] && routes.Served(id) {
						out = append(out, Routing{id, st.name, m.name})
						seen[id] = true
					}
				}
			}
		}
		o.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
