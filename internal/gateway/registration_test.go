package gateway

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/routes"
)

func TestRegistrationInactiveRoutingIsOrderedEverywhere(t *testing.T) {
	fresh(t)
	forgetRouting()
	withoutBuiltinRoutes(t)
	_ = New() // construction must not silently re-register disabled policies
	for _, name := range []string{provider.Rotate, provider.Weighted, provider.FreeFirst, "fixture/missing"} {
		if routes.Served(name) || routingOf(name) != provider.Ordered || goesRound(name) || rotates(name) || weighsByWeight(name) || !sinks(true, name) {
			t.Fatalf("inactive routing kept active semantics %s", name)
		}
	}
	p := provider.Provider{ID: "inactive", Name: "inactive", Key: "fixture", Chat: "http://127.0.0.1:1/v1", Routing: provider.Rotate}
	cs := []candidate{{p: p, rest: "a", model: "m"}, {p: p, rest: "b", model: "m"}}
	for range 3 {
		out, _ := weighRouted(p, cs, "m", provider.Chat)
		if out[0].rest != "a" {
			t.Fatal("unregistered rotation advanced")
		}
	}
	freeFirstCatalog(t, p, catalog.Model{ID: "m", Free: true})
	p.Routing = provider.FreeFirst
	if _, wg := weighRouted(p, cs[:1], "m", provider.Chat); wg.free != nil {
		t.Fatal("inactive policy read free facts")
	}
	// A weighted group has reverted to listed member order, and no longer
	// creates group seats, turn markers or weighted affinity compensation.
	g := provider.Group{ID: "inactive-group", Routing: provider.Weighted, Weights: map[string]int{"a/m": 1, "b/m": 10}}
	a, b := p, p
	a.ID, a.Routing = "a", ""
	b.ID, b.Routing = "b", ""
	ms := []provider.Member{{Provider: a, Model: "m", Path: []string{"a/m"}}, {Provider: b, Model: "m", Path: []string{"b/m"}}}
	out, pl := (&Server{}).planGroup(g, ms, provider.Chat)
	if len(out) != 2 || out[0].p.ID != "a" || len(out[0].seats) != 0 || slices.ContainsFunc(pl.order, func(w Weighed) bool { return w.Turn }) {
		t.Fatalf("inactive group round still active %+v %+v", out, pl.order)
	}
	// Session affinity must ignore rotation's new-turn branch after removal.
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"first"},{"role":"assistant","content":"done"},{"role":"user","content":"next"}]}`)
	in := http.Header{"X-Session-Id": []string{"inactive-fixture"}}
	scope := "inactive-scope"
	sticks.Lock()
	sticks.m[scope+"|"+conversationID(in, body)] = stick{rest: out[1].rest, who: out[1].who(), model: "m", at: time.Now()}
	sticks.Unlock()
	kept, _, aff, _ := affine(scope, provider.AffinitySession, rotates(provider.Rotate), in, provider.Chat, body, out, pl)
	if !aff.Kept || aff.Why != "session" || kept[0].p.ID != "b" {
		t.Fatalf("disabled rotate affected affinity %+v", aff)
	}
}

func TestRegistrationCustomOrderingAndCapturedRemoval(t *testing.T) {
	fresh(t)
	forgetRouting()
	name := "fixture/reverse"
	p := provider.Provider{ID: "custom-strategy", Routing: name, Key: "fixture", Chat: "http://127.0.0.1:1/v1"}
	cs := []candidate{{p: p, rest: "a", model: "m"}, {p: p, rest: "b", model: "m"}}
	remove := routes.Register(name, routes.Strategy{Exact: true, Order: func(pool string, seats []routes.Seat) []int {
		if pool != p.ID || seats[0].Key != "a" {
			t.Error("wrong strategy input")
		}
		return []int{1, 0}
	}})
	captured, _ := routes.Find(name)
	out, _ := weighRouted(p, cs, "m", provider.Chat)
	if out[0].rest != "b" || goesRound(name) {
		t.Fatalf("custom strategy not consumed %s", restsOf(out))
	}
	remove()
	remove()
	out, _ = weighRouted(p, cs, "m", provider.Chat)
	if out[0].rest != "a" || routingOf(name) != provider.Ordered {
		t.Fatal("removed custom order stayed active")
	}
	if !slices.Equal(routes.Apply(captured, "custom-strategy", []routes.Seat{{Key: "a"}, {Key: "b"}}), []int{1, 0}) {
		t.Fatal("captured request lost strategy")
	}
	for _, fn := range []func(string, []routes.Seat) []int{func(string, []routes.Seat) []int { panic("fixture") }, func(string, []routes.Seat) []int { return []int{0, 0} }} {
		off := routes.Register(name, routes.Strategy{Order: fn})
		out, _ = weighRouted(p, cs, "m", provider.Chat)
		off()
		if len(out) != 2 || out[0].rest != "a" || out[1].rest != "b" {
			t.Fatal("invalid custom strategy lost candidates")
		}
	}
}

func TestRegistrationProviderAndGroupShareRounds(t *testing.T) {
	fresh(t)
	forgetRouting()
	p := provider.Provider{ID: "shared-provider", Routing: provider.Weighted, Key: "fixture", Chat: "http://127.0.0.1:1/v1"}
	a, b := p, p
	a.KeyWeight = 3
	b.KeyWeight = 1
	cs := []candidate{{p: a, rest: "a", model: "m"}, {p: b, rest: "b", model: "m"}}
	one, _ := weighRouted(p, cs, "m", provider.Chat)
	two, _ := weighRouted(p, cs, "m", provider.Chat)
	if one[0].rest != "a" || two[0].rest != "a" {
		t.Fatal("expected two overlapping a picks")
	}
	weightedKept(one[0], one[1])
	weightedKept(one[0], one[1]) // duplicate settlement is harmless
	three, _ := weighRouted(p, cs, "m", provider.Chat)
	if three[0].rest != "a" {
		t.Fatal("old request charged later pick or moved twice")
	}
	weightedKept(two[0], two[1])
	// Cross the API boundary: a gateway pick and routes.Round must advance
	// the same state, rather than maintaining provider and group copies.
	routes.Forget(p.ID)
	one, _ = weighRouted(p, cs, "m", provider.Chat)
	if routes.Round(p.ID, []string{"a", "b"}, []int{3, 1}, []bool{false, false}) != 0 {
		t.Fatal("shared round's second pick")
	}
	three, _ = weighRouted(p, cs, "m", provider.Chat)
	if three[0].rest != "b" {
		t.Fatal("gateway weighted state not shared with registry round")
	}
}

func TestRegistrationNestedWeightedReceipts(t *testing.T) {
	fresh(t)
	forgetRouting()
	url := "http://127.0.0.1:1/v1"
	mk := func(id string) provider.Provider { return provider.Provider{ID: id, Key: "fixture-" + id, Chat: url} }
	child := provider.Group{ID: "receipt-child", Routing: provider.Weighted, Weights: map[string]int{"a/m": 3}}
	parent := provider.Group{ID: "receipt-parent", Routing: provider.Weighted, Weights: map[string]int{"group/receipt-child": 3}}
	ms := []provider.Member{
		{Provider: mk("a"), Model: "m", Path: []string{"group/receipt-child", "a/m"}, Via: []provider.Group{child}},
		{Provider: mk("b"), Model: "m", Path: []string{"group/receipt-child", "b/m"}, Via: []provider.Group{child}},
		{Provider: mk("c"), Model: "m", Path: []string{"c/m"}},
	}
	one, _ := (&Server{}).planGroup(parent, ms, provider.Chat)
	_, _ = (&Server{}).planGroup(parent, ms, provider.Chat) // another request advances both rounds
	if len(one[0].seats) != 2 {
		t.Fatalf("nested receipt missing %+v", one[0].seats)
	}
	at := slices.IndexFunc(one, func(c candidate) bool { return c.p.ID == "b" })
	weightedKept(one[0], one[at])
	// This request changed only the child recipient; the parent already
	// charged that same child and must retain its original group share.
	if best := routes.Round(provider.GroupPrefix+child.ID, []string{"a/m", "b/m"}, []int{3, 1}, []bool{false, false}); best != 0 {
		t.Fatalf("nested affinity did not amend its own earlier receipt: %d", best)
	}
	if best := routes.Round(provider.GroupPrefix+parent.ID, []string{"group/receipt-child", "c/m"}, []int{3, 1}, []bool{false, false}); best != 1 {
		t.Fatalf("nested child affinity changed parent charge: %d", best)
	}
}
