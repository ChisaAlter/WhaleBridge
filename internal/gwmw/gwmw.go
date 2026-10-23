// Package gwmw holds gateway middleware registrations. Agent middleware may
// leave a request unchanged; required masking always fails closed.
package gwmw

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
)

type Info struct {
	Protocol string `json:"protocol"`
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Path     string `json:"path"`
	Agent    string `json:"agent,omitempty"`
}
type Reject struct {
	Status  int
	Message string
}
type Agent interface{ Begin(Info) Run }
type Run interface {
	Request([]byte) ([]byte, *Reject)
	Wrap(http.ResponseWriter) (http.ResponseWriter, func())
	End()
}
type To struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Account string `json:"account,omitempty"`
	Model   string `json:"model"`
}
type Tried interface{ Tried([]byte, To) []byte }
type Masker interface {
	JSON([]byte) ([]byte, int)
	Text(string) (string, int)
	Restore(string, bool) string
	Writer(http.ResponseWriter) (http.ResponseWriter, func())
}

var registry struct {
	sync.Mutex
	agents  []*Agent
	maskers []*Masker
	refresh *func()
}

func add[T any](list *[]*T, value T) func() {
	entry := &value
	registry.Lock()
	*list = append(*list, entry)
	registry.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			registry.Lock()
			defer registry.Unlock()
			*list = slices.DeleteFunc(slices.Clone(*list), func(v *T) bool { return v == entry })
		})
	}
}

// Use registers an agent for future requests. Removing it never changes a
// Chain already captured by an in-flight request.
func Use(a Agent) func() { return add(&registry.agents, a) }
func Agents() []Agent {
	registry.Lock()
	defer registry.Unlock()
	result := make([]Agent, 0, len(registry.agents))
	for _, a := range registry.agents {
		if *a != nil {
			result = append(result, *a)
		}
	}
	return result
}

// Mask stacks a masker until removed, restoring the previous generation.
func Mask(m Masker) func() { return add(&registry.maskers, m) }

// Refresh installs an on-demand reconcile callback. It is invoked outside
// registry locks. An older stop cannot remove a newer callback.
func Refresh(f func()) func() {
	entry := &f
	registry.Lock()
	registry.refresh = entry
	registry.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			registry.Lock()
			defer registry.Unlock()
			if registry.refresh == entry {
				registry.refresh = nil
			}
		})
	}
}

var refreshing atomic.Bool

func reconcile(want func() bool) {
	registry.Lock()
	entry := registry.refresh
	registry.Unlock()
	if entry == nil || !refreshing.CompareAndSwap(false, true) {
		return
	}
	defer refreshing.Store(false)
	defer func() { _ = recover() }()
	if want == nil || want() {
		(*entry)()
	}
}
func AgentsWanted(want func() bool) []Agent {
	a := Agents()
	if len(a) == 0 {
		reconcile(want)
		a = Agents()
	}
	return a
}
func Served() (Masker, bool) {
	latest := func() Masker {
		registry.Lock()
		defer registry.Unlock()
		if len(registry.maskers) > 0 {
			return *registry.maskers[len(registry.maskers)-1]
		}
		return nil
	}
	m := latest()
	if m == nil {
		reconcile(nil)
		m = latest()
	}
	return m, m != nil
}

var ErrUnmasked = errors.New("masking is required but no masker is serving; nothing was sent")

func Masking(wanted bool) (Masker, error) {
	if !wanted {
		return nil, nil
	}
	if m, ok := Served(); ok {
		return m, nil
	}
	return nil, ErrUnmasked
}

// Safely converts a faulty masker's panic into a blocked request. Do not put
// the panic's value in the error: it may itself contain the original secret.
func Safely[T any](f func() T) (out T, err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("masking failed; nothing was sent")
		}
	}()
	return f(), nil
}

// Chain owns the exact runs captured at Begin, including reply wrappers.
// Runtime swaps do not replace any of these references.
type Chain struct{ runs []Run }

func Begin(info Info) *Chain { return BeginWith(Agents(), info) }
func BeginWith(agents []Agent, info Info) *Chain {
	c := &Chain{}
	for _, a := range agents {
		if a != nil {
			if run := a.Begin(info); run != nil {
				c.runs = append(c.runs, run)
			}
		}
	}
	if len(c.runs) == 0 {
		return nil
	}
	return c
}
func (c *Chain) Request(body []byte) ([]byte, *Reject) {
	for _, r := range c.runs {
		var reject *Reject
		body, reject = r.Request(body)
		if reject != nil {
			return body, reject
		}
	}
	return body, nil
}
func (c *Chain) Wrap(w http.ResponseWriter) (http.ResponseWriter, func()) {
	var ends []func()
	for _, r := range c.runs {
		var done func()
		w, done = r.Wrap(w)
		if done != nil {
			ends = append(ends, done)
		}
	}
	return w, func() {
		for i := len(ends) - 1; i >= 0; i-- {
			ends[i]()
		}
	}
}
func (c *Chain) End() {
	for i := len(c.runs) - 1; i >= 0; i-- {
		c.runs[i].End()
	}
}
func (c *Chain) Tried(body []byte, to To) []byte {
	for _, r := range c.runs {
		if t, ok := r.(Tried); ok {
			body = t.Tried(body, to)
		}
	}
	return body
}
