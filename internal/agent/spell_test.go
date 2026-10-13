package agent

import "testing"

func TestSpell(t *testing.T) {
	opts := func(prefix string) func(map[string]string) []Option {
		return func(map[string]string) []Option {
			return []Option{
				{Value: "gpt-6"},
				{Value: prefix + "copilot/gpt-6-sol", Ref: "copilot/gpt-6-sol"},
			}
		}
	}
	get := func() string { return "" }
	bare := &Agent{Fields: []Field{{Key: "model", Get: get, Options: opts("")}}}
	prefixed := &Agent{Fields: []Field{{Key: "model", Get: get, Options: opts("magpie/")}}}
	for _, c := range []struct {
		a       *Agent
		in, out string
		err     bool
	}{
		{bare, "copilot/gpt-6-sol", "copilot/gpt-6-sol", false},
		{bare, "magpie/copilot/gpt-6-sol", "copilot/gpt-6-sol", false},
		{prefixed, "copilot/gpt-6-sol", "magpie/copilot/gpt-6-sol", false},
		{prefixed, "magpie/copilot/gpt-6-sol", "magpie/copilot/gpt-6-sol", false},
		{bare, "gpt-6", "gpt-6", false},
		{bare, "some-unlisted-model", "some-unlisted-model", false},
		{bare, "", "", false},
		{bare, "magpie/copilot/nope", "", true},
		{prefixed, "magpie/copilot/nope", "", true},
	} {
		got, err := c.a.Spell("model", c.in)
		if got != c.out || (err != nil) != c.err {
			t.Errorf("%q: got %q, %v; want %q, err %v", c.in, got, err, c.out, c.err)
		}
	}
}
