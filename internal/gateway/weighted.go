package gateway

// Weighted routing (#841): a provider's requests spread over its keys by
// each key's weight, as NGINX's smooth weighted round robin does — keys
// weighing 3 and 1 go a, a, b, a, a, a, b, a…, the ratio held over a
// handful of requests rather than only on average. A key resting after a
// failure is left out of the round while it rests, so the others share
// its requests by their own weights and it takes up its share when it is
// back. A conversation still stays with the key that answered it while
// its affinity says (affinity.go); a request kept so counts against the
// key that took it, not the one the round picked (weightedKept), so new
// conversations make up the share.
//
// A group routed by weight (#1910) spreads its requests over its members
// the same way, by each member's weight (provider.Group.Weights): a
// member's own keys are ordered by its provider's routing, and a group in
// it is one member, planned by its own.

import (
	"sync"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// wrr is each weighted provider's round: every key's current weight, and
// the total the last pick was charged.
var wrr = struct {
	sync.Mutex
	m map[string]*wrrRound // provider id → its round
}{m: map[string]*wrrRound{}}

type wrrRound struct {
	cur   map[string]int // candidate rest, or a group's member → current weight
	total int
	last  string // the one the last pick was charged to
}

// keyWeight is a candidate's weight: its key's, 1 when none.
func keyWeight(c candidate) int { return max(c.p.KeyWeight, 1) }

// resting says whether c rests now, as restLast will find.
func resting(c candidate) bool {
	if _, ok := restOf(c.restKey()); ok {
		return true
	}
	if c.restID() != c.restKey() {
		_, ok := restOf(c.restID())
		return ok
	}
	return false
}

// weightedFirst puts first the key the provider id's round picks now, the
// others after it in their order, for failover.
func weightedFirst(id string, cs []candidate) []candidate {
	keys, weights, rests := make([]string, len(cs)), make([]int, len(cs)), make([]bool, len(cs))
	for i, c := range cs {
		keys[i], weights[i], rests[i] = c.rest, keyWeight(c), resting(c)
	}
	best := wrrPick(id, keys, weights, rests)
	if best <= 0 {
		return cs // first already, or all resting: restLast orders them
	}
	return append(append([]candidate{cs[best]}, cs[:best]...), cs[best+1:]...)
}

// wrrPick is which of keys round id picks now, each weighing its weight,
// those resting left out of the round; -1 when every one rests. The
// pick is charged the round's total.
func wrrPick(id string, keys []string, weights []int, rests []bool) int {
	wrr.Lock()
	defer wrr.Unlock()
	r := wrr.m[id]
	if r == nil {
		r = &wrrRound{cur: map[string]int{}}
		wrr.m[id] = r
	}
	// keys no longer on are dropped from the round
	on := make(map[string]bool, len(keys))
	for _, k := range keys {
		on[k] = true
	}
	for k := range r.cur {
		if !on[k] {
			delete(r.cur, k)
		}
	}
	best, total := -1, 0
	for i, k := range keys {
		if rests[i] {
			continue
		}
		r.cur[k] += weights[i]
		total += weights[i]
		if best < 0 || r.cur[k] > r.cur[keys[best]] {
			best = i
		}
	}
	if best < 0 {
		return -1
	}
	r.cur[keys[best]] -= total
	r.total, r.last = total, keys[best]
	return best
}

// weightedKept moves the pick of picked's round to kept, when affinity
// keeps a conversation with kept, another key of the same weighted
// provider, or another member of a group routed by weight that both are
// in: the round counts the requests each key, and each member, really
// took.
func weightedKept(picked, kept candidate) {
	if picked.p.Routing == provider.Weighted && picked.p.ID == kept.p.ID && picked.rest != kept.rest {
		wrrMove(picked.p.ID, picked.rest, kept.rest)
	}
	for _, s := range picked.seats {
		for _, t := range kept.seats {
			if s.round == t.round && s.member != t.member {
				wrrMove(s.round, s.member, t.member)
			}
		}
	}
}

// wrrMove charges round id's pick to kept rather than picked.
func wrrMove(id, picked, kept string) {
	wrr.Lock()
	defer wrr.Unlock()
	r := wrr.m[id]
	if r == nil || r.last != picked {
		return
	}
	if _, ok := r.cur[kept]; !ok {
		return
	}
	r.cur[picked] += r.total
	r.cur[kept] -= r.total
	r.last = kept
}

// wrrSeat is a candidate's place in the round of a group routed by
// weight it is in: the round (the group's ref) and the member it is of.
type wrrSeat struct{ round, member string }

// weightedUnits orders the units of a group g routed by weight — each
// member, a group in it as one — by g's round over their members'
// weights: the one it picks first, the others after it in their order,
// for failover. heads are what each is weighed by: one resting is left
// out of the round.
func weightedUnits(g provider.Group, members []string, heads []candidate) []int {
	weights, rests := make([]int, len(members)), make([]bool, len(members))
	for i, m := range members {
		weights[i], rests[i] = g.MemberWeight(m), resting(heads[i])
	}
	order := make([]int, 0, len(members))
	best := wrrPick(provider.GroupPrefix+g.ID, members, weights, rests)
	if best > 0 {
		order = append(order, best)
	}
	for i := range members {
		if i != best || best <= 0 {
			order = append(order, i)
		}
	}
	return order
}
