package usage

import (
	"net/http"
	"slices"
	"sync"
)

// Observer receives completed ledger rows without altering accounting. Ledger
// must return promptly; request-aware observers may also implement RequestObserver.
type Observer interface {
	Serve() (stop func())
	Ledger(Record)
}

// RequestObserver watches attempts separately from their eventual ledger row.
// Legacy metadata observers need only Serve and Ledger.
type RequestObserver interface {
	Observer
	Bodies() Bodies
	Begin(Request) Watch
}

type Watch interface {
	Attempt(Record)
	End(Record)
}

type Request struct {
	Header         http.Header
	Agent, Session string
	Kind           string
	Plain          bool
}

type Bodies int

const (
	NoBodies Bodies = iota
	CapturedBodies
	WholeBodies
)

const BodyCut = "\n… (cut here by magpie)"

func RequestObservers() []RequestObserver {
	var out []RequestObserver
	for _, observer := range Observers() {
		if request, ok := observer.(RequestObserver); ok {
			out = append(out, request)
		}
	}
	return out
}

func ObservedBodies() Bodies {
	bodies := NoBodies
	for _, observer := range RequestObservers() {
		func() { defer func() { _ = recover() }(); bodies = max(bodies, observer.Bodies()) }()
	}
	return bodies
}

type observing struct {
	o    Observer
	stop func()
}

var obs = struct {
	sync.Mutex
	items   []*observing
	serving int
}{}

// obsLife serializes starts/stops without holding the snapshot lock across a
// worker join. A Ledger callback may read Observers while its worker stops.
var obsLife sync.Mutex

// Observe registers an observer, starting it now when the gateway is served.
func Observe(o Observer) func() {
	if o == nil {
		return func() {}
	}
	obsLife.Lock()
	defer obsLife.Unlock()
	ob := &observing{o: o}
	obs.Lock()
	serving := obs.serving > 0
	obs.Unlock()
	if serving {
		ob.stop = o.Serve()
	}
	obs.Lock()
	obs.items = append(obs.items, ob)
	obs.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			obsLife.Lock()
			defer obsLife.Unlock()
			obs.Lock()
			obs.items = slices.DeleteFunc(slices.Clone(obs.items), func(x *observing) bool { return x == ob })
			stop := ob.stop
			ob.stop = nil
			obs.Unlock()
			if stop != nil {
				stop()
			}
		})
	}
}

// ServeObservers starts registered observers before accepting gateway calls.
// Stop after handlers drain; concurrent owners share one set of workers.
func ServeObservers() func() {
	obsLife.Lock()
	obs.Lock()
	obs.serving++
	first := obs.serving == 1
	items := slices.Clone(obs.items)
	obs.Unlock()
	if first {
		for _, ob := range items {
			ob.stop = ob.o.Serve()
		}
	}
	obsLife.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			obsLife.Lock()
			defer obsLife.Unlock()
			obs.Lock()
			obs.serving--
			var stops []func()
			if obs.serving == 0 {
				for _, ob := range obs.items {
					if ob.stop != nil {
						stops = append(stops, ob.stop)
						ob.stop = nil
					}
				}
			}
			obs.Unlock()
			for i := len(stops) - 1; i >= 0; i-- {
				stops[i]()
			}
		})
	}
}

func Observers() []Observer {
	obs.Lock()
	defer obs.Unlock()
	out := make([]Observer, len(obs.items))
	for i, ob := range obs.items {
		out[i] = ob.o
	}
	return out
}

func observeLedger(r Record) {
	for _, o := range Observers() {
		copy := r
		// Request-aware exporters own their opt-in body capture. Metadata-only
		// plugins never receive prompts, replies or shared mutable cost pointers.
		allowed := false
		if request, ok := o.(RequestObserver); ok {
			func() { defer func() { _ = recover() }(); allowed = request.Bodies() > NoBodies }()
		}
		if !allowed {
			copy.BodyIn, copy.BodyOut = "", ""
		}
		if r.OwnCost != nil {
			cost := *r.OwnCost
			copy.OwnCost = &cost
		}
		func() { defer func() { _ = recover() }(); o.Ledger(copy) }()
	}
}
