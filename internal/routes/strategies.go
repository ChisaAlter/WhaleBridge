package routes

import (
	"sort"
	"sync"
	"time"
)

// Keep these core thresholds independent of provider so provider settings can
// validate names through this package without a dependency cycle.
const (
	LowShare  = 90
	UsedShare = 98
)

func identity(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}
func tier(share float64) int {
	if share >= UsedShare {
		return 2
	}
	if share >= LowShare {
		return 1
	}
	return 0
}

// Smart preserves cache-friendly order within a tier, preferring unknown
// subscriptions that learn their allowance, then the soonest renewal. Low and
// spent allowances follow in ascending use.
var Smart = Strategy{kind: smart, Order: func(_ string, seats []Seat) []int {
	var fine, low, spent []int
	for i, s := range seats {
		switch tier(s.Used) {
		case 2:
			spent = append(spent, i)
		case 1:
			low = append(low, i)
		default:
			fine = append(fine, i)
		}
	}
	sort.SliceStable(fine, func(i, j int) bool {
		a, b := seats[fine[i]], seats[fine[j]]
		if a.Learns != b.Learns {
			return a.Learns
		}
		for k := 0; k < len(a.Soon) || k < len(b.Soon); k++ {
			var x, y time.Time
			if k < len(a.Soon) {
				x = a.Soon[k].Truncate(time.Hour)
			}
			if k < len(b.Soon) {
				y = b.Soon[k].Truncate(time.Hour)
			}
			if x.Equal(y) {
				continue
			}
			if x.IsZero() || y.IsZero() {
				return y.IsZero()
			}
			return x.Before(y)
		}
		return false
	})
	for _, bucket := range [][]int{low, spent} {
		sort.SliceStable(bucket, func(i, j int) bool { return seats[bucket[i]].Used < seats[bucket[j]].Used })
	}
	return append(append(fine, low...), spent...)
}}
var Ordered = Strategy{kind: ordered, Exact: true, Order: func(_ string, seats []Seat) []int { return identity(len(seats)) }}

var turns = struct {
	sync.Mutex
	m map[string]uint64
}{m: map[string]uint64{}}
var Rotate = Strategy{kind: rotate, Order: func(pool string, seats []Seat) []int {
	if len(seats) == 0 {
		return nil
	}
	turns.Lock()
	n := int(turns.m[pool] % uint64(len(seats)))
	turns.m[pool]++
	turns.Unlock()
	return append(identity(len(seats))[n:], identity(n)...)
}}
var LeastUsed = Strategy{kind: leastUsed, ByUse: true, Order: func(_ string, seats []Seat) []int {
	idx := identity(len(seats))
	sort.SliceStable(idx, func(i, j int) bool {
		a, b := seats[idx[i]], seats[idx[j]]
		if a.Used != b.Used {
			return a.Used < b.Used
		}
		return a.Tokens < b.Tokens
	})
	return idx
}}
var Pace = Strategy{kind: pace, ByUse: true, Order: func(_ string, seats []Seat) []int {
	var eligible []int
	for i, s := range seats {
		if tier(s.Used) == 0 && !s.Learns {
			eligible = append(eligible, i)
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool { return seats[eligible[i]].Pace > seats[eligible[j]].Pace })
	band, top := make([]int, len(seats)), 0.0
	for k, i := range eligible {
		if p := seats[i].Pace; k == 0 || p < 0.9*top {
			top = p
			if k > 0 {
				band[i] = band[eligible[k-1]] + 1
				continue
			}
		} else {
			band[i] = band[eligible[k-1]]
		}
	}
	idx := identity(len(seats))
	sort.SliceStable(idx, func(i, j int) bool {
		a, b := seats[idx[i]], seats[idx[j]]
		if ta, tb := tier(a.Used), tier(b.Used); ta != tb {
			return ta < tb
		} else if ta > 0 {
			if a.Used != b.Used {
				return a.Used < b.Used
			}
		} else if a.Learns != b.Learns {
			return a.Learns
		} else if band[idx[i]] != band[idx[j]] {
			return band[idx[i]] < band[idx[j]]
		}
		return a.Tokens < b.Tokens
	})
	return idx
}}
var Weighted = Strategy{kind: weighted, Order: func(pool string, seats []Seat) []int {
	keys, weights, rests := make([]string, len(seats)), make([]int, len(seats)), make([]bool, len(seats))
	for i, s := range seats {
		keys[i], weights[i], rests[i] = s.Key, max(s.Weight, 1), s.Resting
	}
	return First(Round(pool, keys, weights, rests), len(seats))
}}
var FreeFirst = Strategy{kind: freeFirst, ByFree: true, Order: func(_ string, seats []Seat) []int {
	var free, paid []int
	for i, s := range seats {
		if s.Free {
			free = append(free, i)
		} else {
			paid = append(paid, i)
		}
	}
	return append(free, paid...)
}}

func First(best, n int) []int {
	idx := identity(n)
	if best <= 0 || best >= n {
		return idx
	}
	return append(append([]int{best}, idx[:best]...), idx[best+1:]...)
}

var builtins = map[string]Strategy{"order": Ordered, "rotate": Rotate, "usage": LeastUsed, "pace": Pace, "weight": Weighted, "free": FreeFirst}

// Builtin is a copy for explicit finite assembly. Mutating this exported map
// cannot change which algorithms are allowed under the reserved core names.
var Builtin = func() map[string]Strategy {
	out := make(map[string]Strategy, len(builtins))
	for name, s := range builtins {
		out[name] = s
	}
	return out
}()
