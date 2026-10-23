package routes

import (
	"slices"
	"sync"
)

// All provider and group weighted picks use these same smooth rounds. A
// registration lifecycle does not reset them; Forget explicitly starts over.
var rounds = struct {
	sync.Mutex
	m map[string]*round
}{m: map[string]*round{}}

type round struct {
	cur      map[string]int
	keys     []string
	weights  []int
	revision uint64
	last     *PickReceipt
}

// PickReceipt identifies one charged pick. Its fields are private, and moving
// it is single-use. A later concurrent pick does not invalidate it; replacing
// the roster/weights or forgetting the round does.
type PickReceipt struct {
	pool     string
	round    *round
	revision uint64
	picked   string
	total    int
	moved    bool // guarded by rounds
}

// Pick chooses a ready route, charges its total, and returns its receipt.
// Invalid lengths or duplicate keys leave the round untouched.
func Pick(pool string, keys []string, weights []int, rests []bool) (int, *PickReceipt) {
	if len(keys) == 0 || len(weights) != len(keys) || len(rests) != len(keys) {
		return -1, nil
	}
	seen := make(map[string]bool, len(keys))
	normal := make([]int, len(keys))
	for i, k := range keys {
		if seen[k] {
			return -1, nil
		}
		seen[k] = true
		normal[i] = max(weights[i], 1)
	}
	rounds.Lock()
	defer rounds.Unlock()
	r := rounds.m[pool]
	if r == nil {
		r = &round{cur: map[string]int{}}
		rounds.m[pool] = r
	}
	if !slices.Equal(r.keys, keys) || !slices.Equal(r.weights, normal) {
		r.revision++
		r.keys, r.weights = slices.Clone(keys), normal
		for k := range r.cur {
			if !seen[k] {
				delete(r.cur, k)
			}
		}
	}
	best, total := -1, 0
	for i, k := range keys {
		if rests[i] {
			continue
		}
		r.cur[k] += normal[i]
		total += normal[i]
		if best < 0 || r.cur[k] > r.cur[keys[best]] {
			best = i
		}
	}
	if best < 0 {
		return -1, nil
	}
	r.cur[keys[best]] -= total
	receipt := &PickReceipt{pool: pool, round: r, revision: r.revision, picked: keys[best], total: total}
	r.last = receipt
	return best, receipt
}

// MovePick charges this request's actual recipient instead of the route picked
// for it. It never amends another request's receipt, even when both picked the
// same key. The gateway must carry the receipt rather than infer a global last.
func MovePick(receipt *PickReceipt, picked, kept string) bool {
	rounds.Lock()
	defer rounds.Unlock()
	return movePick(receipt, picked, kept)
}
func movePick(receipt *PickReceipt, picked, kept string) bool {
	if receipt == nil || receipt.moved || picked == kept || receipt.picked != picked {
		return false
	}
	r := rounds.m[receipt.pool]
	if r != receipt.round || r.revision != receipt.revision {
		return false
	}
	if _, ok := r.cur[picked]; !ok {
		return false
	}
	if _, ok := r.cur[kept]; !ok {
		return false
	}
	r.cur[picked] += receipt.total
	r.cur[kept] -= receipt.total
	receipt.moved = true
	return true
}

// Round and MoveRound preserve the simple synchronous API for strategy code.
// Concurrent gateway affinity uses Pick and MovePick, not the latest pick.
func Round(pool string, keys []string, weights []int, rests []bool) int {
	best, _ := Pick(pool, keys, weights, rests)
	return best
}
func MoveRound(pool, picked, kept string) {
	rounds.Lock()
	defer rounds.Unlock()
	if r := rounds.m[pool]; r != nil {
		movePick(r.last, picked, kept)
	}
}

// Forget drops selected pools, or all pools when none are named. It also
// invalidates every outstanding receipt from the forgotten round.
func Forget(pools ...string) {
	rounds.Lock()
	defer rounds.Unlock()
	turns.Lock()
	defer turns.Unlock()
	if len(pools) == 0 {
		rounds.m = map[string]*round{}
		turns.m = map[string]uint64{}
		return
	}
	for _, p := range pools {
		delete(rounds.m, p)
		delete(turns.m, p)
	}
}
