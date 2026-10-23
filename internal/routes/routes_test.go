package routes

import (
	"slices"
	"sync"
	"testing"
	"time"
)

func TestRegisterStackSnapshotAndReservedMetadata(t *testing.T) {
	if !Served("") || Served("fixture/reverse") {
		t.Fatal("unexpected initial registry")
	}
	first := Register("fixture/reverse", Strategy{Order: func(string, []Seat) []int { return []int{1, 0} }})
	captured, _ := Find("fixture/reverse")
	second := Register("fixture/reverse", Ordered)
	if s, _ := Find("fixture/reverse"); !s.Exact {
		t.Fatal("newest registration not active")
	}
	first()
	first()
	if s, _ := Find("fixture/reverse"); !s.Exact {
		t.Fatal("removing older registration removed newer one")
	}
	second()
	second()
	if Served("fixture/reverse") {
		t.Fatal("removed strategy still served")
	}
	if got := Apply(captured, "fixture", make([]Seat, 2)); !slices.Equal(got, []int{1, 0}) {
		t.Fatal("captured strategy lost after unregister")
	}
	bad := Register("weight", Strategy{Order: func(string, []Seat) []int { return []int{1, 0} }})
	defer bad()
	if Served("weight") {
		t.Fatal("arbitrary algorithm impersonated reserved weighted policy")
	}
	off := RegisterBuiltins()
	defer off()
	st, ok := Find("weight")
	if !ok || !st.Weighted() || st.Rotates() {
		t.Fatal("weighted metadata mismatch")
	}
	changed := Weighted
	changed.Order = func(string, []Seat) []int { return []int{1, 0} }
	changed.Exact = true
	restore := Register("weight", changed)
	defer restore()
	st, _ = Find("weight")
	if st.Exact {
		t.Fatal("builtin override changed reserved semantics")
	}
	for _, name := range []string{"mod/by-latency", "a.b/c_d", "2/name"} {
		if !FromPlugin(name) {
			t.Errorf("valid plugin name rejected %s", name)
		}
	}
	for _, name := range []string{"weight", "/bad", "bad/", "a/b/c", "a b/c"} {
		if FromPlugin(name) {
			t.Errorf("invalid plugin name accepted %s", name)
		}
	}
}

func TestApplyKeepsEveryRouteAndFacts(t *testing.T) {
	seats := []Seat{{Key: "a", Soon: []time.Time{time.Unix(1, 0)}}, {Key: "b"}, {Key: "c"}}
	for name, order := range map[string]func(string, []Seat) []int{
		"panic": func(string, []Seat) []int { panic("fixture") }, "nil": nil,
		"short": func(string, []Seat) []int { return []int{1, 0} }, "duplicate": func(string, []Seat) []int { return []int{1, 1, 0} },
		"negative": func(string, []Seat) []int { return []int{-1, 1, 0} }, "outside": func(string, []Seat) []int { return []int{0, 1, 3} },
	} {
		t.Run(name, func(t *testing.T) {
			if got := Apply(Strategy{Order: order}, "pool", seats); !slices.Equal(got, []int{0, 1, 2}) {
				t.Fatalf("candidate lost %v", got)
			}
		})
	}
	out := Apply(Strategy{Order: func(_ string, s []Seat) []int {
		s[0].Key = "changed"
		s[0].Soon[0] = time.Time{}
		return []int{2, 0, 1}
	}}, "pool", seats)
	if !slices.Equal(out, []int{2, 0, 1}) || seats[0].Key != "a" || seats[0].Soon[0].IsZero() {
		t.Fatal("strategy mutated request facts")
	}
}

func TestBuiltinPoliciesAndWarmRoundLifecycle(t *testing.T) {
	Forget()
	defer Forget()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	ss := []Seat{{Key: "spent", Used: 99}, {Key: "late", Used: 5, Soon: []time.Time{now.Add(48 * time.Hour)}}, {Key: "low", Used: 92}, {Key: "unknown"}, {Key: "soon", Used: 50, Soon: []time.Time{now.Add(time.Hour)}}, {Key: "learn", Learns: true}}
	if got := Apply(Smart, "p", ss); !slices.Equal(got, []int{5, 4, 1, 3, 2, 0}) {
		t.Fatalf("smart %v", got)
	}
	if got := Apply(LeastUsed, "p", []Seat{{Used: 50}, {Used: 10, Tokens: 9}, {Used: 10, Tokens: 1}}); !slices.Equal(got, []int{2, 1, 0}) {
		t.Fatalf("usage %v", got)
	}
	if got := Apply(Pace, "p", []Seat{{Pace: 1}, {Used: 95, Pace: 50}, {Pace: 10}}); !slices.Equal(got, []int{2, 0, 1}) {
		t.Fatalf("pace %v", got)
	}
	if got := Apply(FreeFirst, "p", []Seat{{}, {Free: true, Used: 100}, {}, {Free: true}}); !slices.Equal(got, []int{1, 3, 0, 2}) {
		t.Fatalf("free partition %v", got)
	}
	seats := []Seat{{Key: "a", Weight: 3}, {Key: "b", Weight: 1}}
	off := RegisterBuiltins()
	st, _ := Find("weight")
	if Apply(st, "warm", seats)[0] != 0 {
		t.Fatal("first weighted pick")
	}
	off()
	if Served("weight") {
		t.Fatal("builtins survived dispose")
	}
	off = RegisterBuiltins()
	defer off()
	st, _ = Find("weight")
	if Apply(st, "warm", seats)[0] != 0 || Apply(st, "warm", seats)[0] != 1 {
		t.Fatal("remove/readd reset weighted round")
	}
	st, _ = Find("rotate")
	if Apply(st, "turn", seats)[0] != 0 {
		t.Fatal("initial rotate")
	}
	off()
	off = RegisterBuiltins()
	defer off()
	st, _ = Find("rotate")
	if Apply(st, "turn", seats)[0] != 1 {
		t.Fatal("remove/readd reset turn")
	}
	Forget("warm", "turn")
	if Apply(Weighted, "warm", seats)[0] != 0 || Apply(Rotate, "turn", seats)[0] != 0 {
		t.Fatal("explicit forget did not reset shared state")
	}
}

func TestRoundReceiptsConcurrentPicksAndInvalidation(t *testing.T) {
	Forget()
	defer Forget()
	keys, weights, rests := []string{"a", "b"}, []int{3, 1}, []bool{false, false}
	_, first := Pick("p", keys, weights, rests)
	_, second := Pick("p", keys, weights, rests) // same key; old last-based code charges the wrong request
	if first.picked != "a" || second.picked != "a" {
		t.Fatal("fixture expected successive a picks")
	}
	if !MovePick(first, "a", "b") || MovePick(first, "a", "b") {
		t.Fatal("receipt did not move exactly once")
	}
	if rounds.m["p"].cur["a"] != 2 || rounds.m["p"].cur["b"] != -2 {
		t.Fatalf("wrong concurrent charge %+v", rounds.m["p"].cur)
	}
	if !MovePick(second, "a", "b") {
		t.Fatal("later request's receipt was consumed by earlier affinity")
	}
	_, third := Pick("p", keys, weights, rests)
	Forget("p")
	Pick("p", keys, weights, rests)
	if MovePick(third, third.picked, "b") {
		t.Fatal("forgotten round receipt affected replacement")
	}
	_, stale := Pick("p", keys, weights, rests)
	Pick("p", keys, []int{1, 1}, rests)
	if MovePick(stale, stale.picked, "b") {
		t.Fatal("old roster/weight receipt affected new round")
	}
}

func TestRoundConcurrentReceiptSingleUse(t *testing.T) {
	Forget()
	defer Forget()
	_, receipt := Pick("p", []string{"a", "b"}, []int{1, 1}, []bool{false, false})
	var wg sync.WaitGroup
	var mu sync.Mutex
	moved := 0
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if MovePick(receipt, "a", "b") {
				mu.Lock()
				moved++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if moved != 1 {
		t.Fatalf("moved %d times", moved)
	}
}
