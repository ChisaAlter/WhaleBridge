package agent

import (
	"path/filepath"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// A catalog model with a fast mode says, in an agent's picker, which agent
// it is switched fast for and whether it is now (#954); one without (a
// relay's) says nothing.
func TestFastPickOptions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "oa", Name: "OpenAI", Key: "k", Responses: "https://api.openai.com/v1", Models: []string{"gpt-6.1-sol"}},
		{ID: "rl", Name: "Relay", Key: "k", Chat: "https://relay.example/v1", Models: []string{"gpt-6.1-sol"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetFastPick("codex", "oa/gpt-6.1-sol", true); err != nil {
		t.Fatal(err)
	}
	of := func(agent string) map[string]Option {
		out := map[string]Option{}
		for _, o := range viaMagpie(agent, "magpie/") {
			out[o.Ref] = o
		}
		return out
	}
	codex, other := of("codex"), of("opencode")
	if o := codex["oa/gpt-6.1-sol"]; o.FastFor != "codex" || !o.Fast {
		t.Fatalf("codex's fast pick: %+v", o)
	}
	if o := other["oa/gpt-6.1-sol"]; o.FastFor != "opencode" || o.Fast {
		t.Fatalf("another agent's: %+v", o)
	}
	if o, ok := codex["rl/gpt-6.1-sol"]; !ok || o.FastFor != "" || o.Fast {
		t.Fatalf("a relay's model: %+v %v", o, ok)
	}
}
