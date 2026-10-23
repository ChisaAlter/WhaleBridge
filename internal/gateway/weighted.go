package gateway

// Weighted providers and groups use the same routes round. Each request carries
// the receipt of its pick so affinity compensation cannot charge a later one.

import (
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/routes"
)

func keyWeight(c candidate) int { return max(c.p.KeyWeight, 1) }

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

// weightedKept moves this request's pick to the route actually kept. Receipts
// survive unregistration, so a request already planned can still settle its
// own charge; unregistration prevents new picks rather than erasing history.
func weightedKept(picked, kept candidate) {
	if picked.p.ID == kept.p.ID && picked.rest != kept.rest {
		routes.MovePick(picked.roundPick, picked.rest, kept.rest)
	}
	for _, s := range picked.seats {
		for _, t := range kept.seats {
			if s.round == t.round && s.member != t.member {
				routes.MovePick(s.pick, s.member, t.member)
			}
		}
	}
}

// wrrSeat is this request's place in each weighted group it belongs to. Nested
// groups retain their own receipts alongside the enclosing group's receipt.
type wrrSeat struct {
	round, member string
	pick          *routes.PickReceipt
}

func weightedUnits(g provider.Group, members []string, heads []candidate) ([]int, *routes.PickReceipt) {
	weights, rests := make([]int, len(members)), make([]bool, len(members))
	for i, m := range members {
		weights[i], rests[i] = g.MemberWeight(m), resting(heads[i])
	}
	best, receipt := routes.Pick(provider.GroupPrefix+g.ID, members, weights, rests)
	return routes.First(best, len(members)), receipt
}
