// Package routes holds the gateway's candidate ordering strategies. Admission,
// protocol fit, cooldowns, affinity and failover remain the gateway's concern.
package routes

import (
	"log"
	"regexp"
	"slices"
	"sync"
	"time"
)

// Seat is one account, key, or group head in a pool. It contains no credential.
type Seat struct {
	Key     string
	Account bool
	Known   bool
	Learns  bool
	Used    float64
	Soon    []time.Time
	Pace    float64
	Weight  int
	Resting bool
	Tokens  float64
	Free    bool
}

// Strategy returns a permutation of the seats. Exact preserves that order over
// protocol fit; ByUse and ByFree request the corresponding trace facts. Calls
// must be bounded by the implementation (the JS adapter includes its wait).
type Strategy struct {
	Order  func(pool string, seats []Seat) []int
	Exact  bool
	ByUse  bool
	ByFree bool
	kind   strategyKind
}

type strategyKind uint8

const (
	custom strategyKind = iota
	smart
	ordered
	rotate
	leastUsed
	pace
	weighted
	freeFirst
)

// Rotates and Weighted identify the built-in round semantics, not merely a
// configured name. Reserved names cannot be overridden with another algorithm.
func (s Strategy) Rotates() bool  { return s.kind == rotate }
func (s Strategy) Weighted() bool { return s.kind == weighted }

var reg = struct {
	sync.RWMutex
	m map[string][]*Strategy
}{m: map[string][]*Strategy{}}

// Register stacks a strategy until the returned idempotent disposer removes
// it. Captured values remain usable after removal. Built-in names activate the
// canonical built-in only: replacing a weighted algorithm while group planning
// still treats it as weighted would split ordering from affinity accounting.
func Register(name string, s Strategy) func() {
	if canonical, reserved := builtins[name]; reserved {
		if s.kind != canonical.kind {
			log.Printf("routes: %q is reserved for its built-in strategy", name)
			return func() {}
		}
		s = canonical
	} else if name == "" {
		return func() {}
	}
	e := &s
	reg.Lock()
	reg.m[name] = append(reg.m[name], e)
	reg.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			reg.Lock()
			defer reg.Unlock()
			stack := slices.DeleteFunc(slices.Clone(reg.m[name]), func(x *Strategy) bool { return x == e })
			if len(stack) == 0 {
				delete(reg.m, name)
			} else {
				reg.m[name] = stack
			}
		})
	}
}

// Find takes a value snapshot. Smart is always available; every other policy
// must be explicitly registered by the application's assembly.
func Find(name string) (Strategy, bool) {
	if name == "" {
		return Smart, true
	}
	reg.RLock()
	defer reg.RUnlock()
	s := reg.m[name]
	if len(s) == 0 {
		return Strategy{}, false
	}
	return *s[len(s)-1], true
}
func Served(name string) bool { _, ok := Find(name); return ok }

var pluginName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

// FromPlugin accepts a persistent plugin/name, independently of whether the
// plugin is loaded now. It cannot collide with a built-in policy name.
func FromPlugin(name string) bool { return pluginName.MatchString(name) }

// RegisterBuiltins is explicit bootstrap for a finite application assembly or
// test binary. Constructing a gateway does not call it. Disposing registrations
// does not erase their round state; Forget is the deliberate reset operation.
func RegisterBuiltins() func() {
	var off []func()
	for _, name := range []string{"order", "rotate", "usage", "pace", "weight", "free"} {
		off = append(off, Register(name, builtins[name]))
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(off) - 1; i >= 0; i-- {
				off[i]()
			}
		})
	}
}

// Apply checks a strategy's output. A panic, nil callback or invalid order
// keeps every candidate in listed order. The input facts cannot be mutated.
func Apply(s Strategy, pool string, seats []Seat) []int {
	order, _ := ApplyPick(s, pool, seats)
	return order
}

// ApplyPick also returns the receipt of a built-in weighted pick. The gateway
// carries it with that request so affinity can compensate this exact pick even
// after another request has advanced the same round.
func ApplyPick(s Strategy, pool string, seats []Seat) (order []int, receipt *PickReceipt) {
	n := len(seats)
	if n < 2 {
		return identity(n), nil
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("routes: %s's strategy panicked: %v", pool, r)
			order, receipt = identity(n), nil
		}
	}()
	copySeats := slices.Clone(seats)
	for i := range copySeats {
		copySeats[i].Soon = slices.Clone(seats[i].Soon)
	}
	if s.Weighted() {
		keys, weights, rests := make([]string, n), make([]int, n), make([]bool, n)
		for i, seat := range copySeats {
			keys[i], weights[i], rests[i] = seat.Key, max(seat.Weight, 1), seat.Resting
		}
		var best int
		best, receipt = Pick(pool, keys, weights, rests)
		return First(best, n), receipt
	}
	if s.Order == nil {
		return identity(n), nil
	}
	order = s.Order(pool, copySeats)
	seen := make([]bool, n)
	valid := len(order) == n
	for _, i := range order {
		if !valid || i < 0 || i >= n || seen[i] {
			valid = false
			break
		}
		seen[i] = true
	}
	if !valid {
		log.Printf("routes: %s's strategy returned an invalid order; trying every route in listed order", pool)
		return identity(n), nil
	}
	return slices.Clone(order), nil
}
