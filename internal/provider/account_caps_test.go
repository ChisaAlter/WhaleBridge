package provider

import (
	"testing"
	"time"
)

func TestCapHeld(t *testing.T) {
	now := time.Now()
	five, week := now.Add(3*time.Hour), now.Add(4*24*time.Hour)
	a := Allowance{
		{Used: 75, Resets: five, Span: 5 * time.Hour},
		{Used: 40, Resets: week, Span: 7 * 24 * time.Hour},
		{Used: 95, Resets: week, Span: 7 * 24 * time.Hour, Model: "opus"},
	}
	all := func(n int) WindowCaps { return WindowCaps{All: n} }
	if h := a.CapHeld("gpt-6", all(70), now); h == nil || h.Used != 75 || h.Cap != 70 || !h.Back.Equal(five) || h.Credits {
		t.Fatalf("at 75%% of 70%%: %+v", h)
	}
	if h := a.CapHeld("gpt-6", all(80), now); h != nil {
		t.Fatal("held below its cap")
	}
	if h := a.CapHeld("gpt-6", all(0), now); h != nil {
		t.Fatal("held with no cap")
	}
	// Opus's own window counts for Opus alone
	if h := a.CapHeld("claude-opus-5", all(90), now); h == nil || h.Used != 95 || !h.Back.Equal(week) {
		t.Fatalf("opus at 95%% of 90%%: %+v", h)
	}
	if h := a.CapHeld("claude-sonnet-5", all(90), now); h != nil {
		t.Fatal("Opus's window held another model")
	}
	// renewed since it was read: empty again
	if h := a.CapHeld("gpt-6", all(70), five.Add(time.Minute)); h != nil {
		t.Fatal("held by a window that has renewed")
	}
	// one not saying when it renews: back not known
	if h := (Allowance{{Used: 80}}).CapHeld("gpt-6", all(70), now); h == nil || !h.Back.IsZero() {
		t.Fatalf("no reset: %+v", h)
	}
}

func TestWindowCapHeld(t *testing.T) {
	now := time.Now()
	five, week := now.Add(3*time.Hour), now.Add(4*24*time.Hour)
	// the vendor's readings, as allowanceOf makes them: windows by name
	read := func(fiveUsed, weekUsed float64) Allowance {
		return allowanceOf([]QuotaWindow{
			{Name: "5 hours", Used: fiveUsed, ResetsAt: &five, Span: 5 * time.Hour},
			{Name: "Weekly", Used: weekUsed, ResetsAt: &week, Span: 7 * 24 * time.Hour},
		}, now)
	}
	friend := WindowCaps{Windows: map[string]int{"5 hours": 50, "weekly": 40}}
	// the five hours at 60%, past its 50%: held, at that window's cap
	if h := read(60, 10).CapHeld("gpt-6", friend, now); h == nil || h.Cap != 50 || h.Used != 60 || !h.Back.Equal(five) {
		t.Fatalf("5 hours at 60%% of 50%%: %+v", h)
	}
	// the week at 30% of its 40%, the five hours at 20% of 50%: used
	if h := read(20, 30).CapHeld("gpt-6", friend, now); h != nil {
		t.Fatalf("both under their own caps, held: %+v", h)
	}
	// the week at 45%, past its 40%, though under the five hours' 50%
	if h := read(20, 45).CapHeld("gpt-6", friend, now); h == nil || h.Cap != 40 || !h.Back.Equal(week) {
		t.Fatalf("week at 45%% of 40%%: %+v", h)
	}
	// one window's own beside the account's: the week follows the account
	mixed := WindowCaps{All: 10, Windows: map[string]int{"5 hours": 50}}
	if h := read(30, 5).CapHeld("gpt-6", mixed, now); h != nil {
		t.Fatalf("5 hours at 30%% of its own 50%%, held: %+v", h)
	}
	if h := read(30, 12).CapHeld("gpt-6", mixed, now); h == nil || h.Cap != 10 {
		t.Fatalf("week at 12%% of the account's 10%%: %+v", h)
	}
	// 100 on a window is none there, whatever the account's
	free := WindowCaps{All: 10, Windows: map[string]int{"weekly": 100}}
	if h := read(5, 95).CapHeld("gpt-6", free, now); h != nil {
		t.Fatalf("week with no cap of its own held: %+v", h)
	}
	// set not to spend its credits, that window is still held at 100%
	free.Credits = true
	if h := read(5, 100).CapHeld("gpt-6", free, now); h == nil || h.Cap != 100 || !h.Credits {
		t.Fatalf("credits off, week at 100%%: %+v", h)
	}
	// the window's name is matched in any case
	if c, _ := (WindowCaps{Windows: map[string]int{"weekly": 40}}).Of(" WEEKLY "); c != 40 {
		t.Fatalf("named in another case: %d", c)
	}
	// capReached, for the agent signed in, goes by each window's own too
	ws := []QuotaWindow{{Name: "5 hours", Used: 60, ResetsAt: &five}, {Name: "Weekly", Used: 30, ResetsAt: &week}}
	if !capReached(SubscriptionQuota{Windows: ws}, friend, now) {
		t.Fatal("5 hours at 60% of 50% not reached")
	}
	ws[0].Used = 20
	if capReached(SubscriptionQuota{Windows: ws}, friend, now) {
		t.Fatal("reached under both windows' own caps")
	}
}

func TestSetWindowCap(t *testing.T) {
	signIn(t) // me@example.com on codex
	if err := SetAccountCap("codex", "me@example.com", 70); err != nil {
		t.Fatal(err)
	}
	c := AccountCapsOf("codex", "me@example.com")
	if c.All != 70 || c.Windows != nil {
		t.Fatalf("an old cap read as %+v", c)
	}
	if s, _ := c.Of("Weekly"); s != 70 {
		t.Fatalf("the week with the account's cap: %d", s)
	}
	if err := SetWindowCap("codex", "ME@example.com", "Weekly", 40); err != nil {
		t.Fatal(err)
	}
	c = AccountCapsOf("codex", "me@example.com")
	if s, _ := c.Of("weekly"); s != 40 || c.All != 70 {
		t.Fatalf("the week's own: %+v", c)
	}
	if s, _ := c.Of("5 hours"); s != 70 {
		t.Fatalf("the five hours not following the account's cap: %d", s)
	}
	for _, bad := range []int{-1, 101} {
		if err := SetWindowCap("codex", "me@example.com", "Weekly", bad); err == nil {
			t.Errorf("%d taken", bad)
		}
	}
	if err := SetWindowCap("codex", "me@example.com", "", 40); err == nil {
		t.Error("a window with no name taken")
	}
	if err := SetWindowCap("codex", "me@example.com", "weekly", 0); err != nil {
		t.Fatal(err)
	}
	if c := AccountCapsOf("codex", "me@example.com"); c.Windows != nil {
		t.Fatalf("cleared, still %+v", c)
	}
	if p, _ := Find("codex"); p.AccountWindowCaps != nil || p.AccountCap("me@example.com") != 70 {
		t.Fatalf("saved as %+v", p.AccountWindowCaps)
	}
}

func TestParseCap(t *testing.T) {
	for in, want := range map[string]int{"70": 70, "70%": 70, " 5 % ": 5, "off": 0, "none": 0, "0": 0, "100": 0, "-": 0} {
		if got, err := ParseCap(in); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "abc", "150", "-5", "0.5"} {
		if _, err := ParseCap(in); err == nil {
			t.Errorf("%q taken", in)
		}
	}
}
