package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/edit"
	"github.com/ChisaAlter/WhaleBridge/internal/gateway"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// desktopWindowSandbox is a HOME with provider v's models: gpt 272K and
// grok 256K (reasoning, so Desktop lists them by mythos-magpie ids), glm
// 1M, plain of no known window, and a Claude-named one Claude Code knows.
func desktopWindowSandbox(t *testing.T) (home, path string) {
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "http://127.0.0.1:1/v1", Key: "k",
		Models: []string{"gpt", "grok", "glm", "plain", "claude-opus-4-6"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "http://127.0.0.1:1/v1", []catalog.Model{
		{ID: "gpt", Context: 272000}, {ID: "grok", Context: 256000}, {ID: "glm", Context: 1000000},
		{ID: "plain"}, {ID: "claude-opus-4-6", Context: 128000},
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"v/gpt", "v/grok", "v/claude-opus-4-6"} {
		if err := provider.SetModelEfforts(id, []string{"low", "medium", "high"}); err != nil {
			t.Fatal(err)
		}
	}
	path = filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"theme":"dark"}`)
	return home, path
}

// TestClaudeDesktopContextWindow (#1458): Claude Desktop's Code tab runs
// Claude Code on ~/.claude/settings.json with the id Desktop lists a model
// by, and hands it nothing of the model's window; Claude Code 2.1.293 takes
// a mythos-magpie id it doesn't know for 200K unless
// CLAUDE_CODE_MAX_CONTEXT_TOKENS says otherwise, so GPT 272K and Grok 256K
// showed "/ 200K". While Desktop is on magpie, magpie tells Claude Code the
// least window of Desktop's models (it takes one value for all of them),
// whatever model Claude Code itself is on, and never more than one of them
// takes; a model Claude Code knows, and one listed by its 1M id, are left
// out; the user's own value is theirs.
func TestClaudeDesktopContextWindow(t *testing.T) {
	home, path := desktopWindowSandbox(t)
	a := claude(home)
	window := func() string { v, _ := edit.GetJSON(path, "env."+claudeContextEnv); return v }
	set := func(key, v string) {
		t.Helper()
		if err := a.Field(key).Set(v); err != nil {
			t.Fatal(err)
		}
	}
	desktop := func(v string) {
		t.Helper()
		if err := claudeDesktop(home).Field("provider").Set(v); err != nil {
			t.Fatal(err)
		}
	}

	// the ids Desktop's Code tab hands Claude Code are ones it doesn't know
	for _, e := range provider.Catalog() {
		if e.ID == "v/gpt" || e.ID == "v/grok" {
			if id := gateway.DesktopID(e); !strings.HasPrefix(id, "mythos-magpie-") || claudeName(id) != "" {
				t.Fatalf("%s is listed as %s", e.ID, id)
			}
		}
	}

	// Claude Code on magpie on a model of no known window: nothing to
	// tell it until Desktop is on magpie
	set("model", "v/plain")
	if window() != "" {
		t.Fatalf("plain, Desktop off: %q", window())
	}
	desktop("magpie")
	if window() != "256000" {
		t.Fatalf("Desktop on magpie, Claude Code on plain: %q, want Grok's 256000", window())
	}
	// Claude Code's own model of 272K is told no more than Desktop's 256K
	set("model", "v/gpt")
	if window() != "256000" {
		t.Fatalf("main gpt beside Desktop's grok: %q", window())
	}
	// a [1m] main model, which before told nothing at all
	set("model", "v/glm[1m]")
	if window() != "256000" {
		t.Fatalf("[1m] main model: %q", window())
	}
	// a model added later reaches it with the rest of the catalog
	if err := provider.Save(provider.Provider{ID: "w", Name: "W", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"small"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("w", "http://127.0.0.1:1/v1", []catalog.Model{{ID: "small", Context: 128000}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil || window() != "128000" {
		t.Fatalf("after w/small was added: %q %v", window(), err)
	}
	if err := provider.Delete("w"); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil || window() != "256000" {
		t.Fatalf("after w/small went: %q %v", window(), err)
	}
	// Desktop off: Claude Code's own model's window again, or none
	desktop("")
	set("model", "v/gpt")
	if window() != "272000" {
		t.Fatalf("Desktop off, main gpt: %q", window())
	}
	set("model", "claude-opus-4-8")
	if window() != "" {
		t.Fatalf("Desktop off, Claude's own model: %q", window())
	}

	// the user's own value is theirs, through Desktop, a switch and a Sync
	if err := edit.SetJSON(path, edit.KV{Path: "env." + claudeContextEnv, Value: "64000"}); err != nil {
		t.Fatal(err)
	}
	desktop("magpie")
	set("model", "v/gpt")
	if err := a.Sync(); err != nil || window() != "64000" {
		t.Fatalf("the user's value was overwritten: %q %v", window(), err)
	}
	set("model", "")
	if window() != "64000" {
		t.Fatalf("the user's value was taken away: %q", window())
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Fatal("theme lost")
	}
}

// TestClaudeDesktopContextWindow1M: a model Desktop lists by its 1M id
// (settings.DesktopLongest) runs at 1M whatever the value says, so it adds
// nothing to it; listed plain too, it runs at the value, its own 1M when it
// is the only one.
func TestClaudeDesktopContextWindow1M(t *testing.T) {
	home, path := desktopWindowSandbox(t)
	// Desktop is shown glm alone (the Agents page's model list)
	if err := provider.SetHiddenModels("claude-desktop", []string{"v/gpt", "v/grok", "v/plain", "v/claude-opus-4-6"}); err != nil {
		t.Fatal(err)
	}
	a := claude(home)
	window := func() string { v, _ := edit.GetJSON(path, "env."+claudeContextEnv); return v }
	if err := a.Field("model").Set("v/plain"); err != nil {
		t.Fatal(err)
	}
	if err := claudeDesktop(home).Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if window() != "1000000" {
		t.Fatalf("glm listed plain: %q", window())
	}
	s := settings.Load()
	s.DesktopLongest = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil || window() != "" {
		t.Fatalf("glm listed by its 1M id alone: %q %v", window(), err)
	}
}

// TestClaudeDesktopAloneContextWindow (#1458, marsxxl on v0.1.1154): with
// Claude Desktop on magpie and Claude Code itself left on its own setup,
// Desktop's Code tab still runs Claude Code on ~/.claude/settings.json, so
// it is told Desktop's window (and what Desktop's models can do) all the
// same. Only wiring Claude Code in wrote them before, and its Sync did
// nothing while it wasn't, so such a user saw 200K for every model. The
// user's own Claude Code setup and settings are left as they are, and
// Desktop off takes magpie's values out again.
func TestClaudeDesktopAloneContextWindow(t *testing.T) {
	home, path := desktopWindowSandbox(t)
	writeFile(t, path, `{"theme":"dark","model":"opus","autoCompactWindow":300000}`)
	a := claude(home)
	get := func(key string) string { v, _ := edit.GetJSON(path, key); return v }
	window := func() string { return get("env." + claudeContextEnv) }
	desktop := func(v string) {
		t.Helper()
		if err := claudeDesktop(home).Field("provider").Set(v); err != nil {
			t.Fatal(err)
		}
	}

	desktop("magpie")
	if window() != "256000" {
		t.Fatalf("Desktop alone on magpie: window %q, want Grok's 256000", window())
	}
	if caps := get("env." + claudeCapsEnv); !strings.Contains(caps, "mythos-magpie-") {
		t.Fatalf("Desktop alone on magpie: capabilities %q name none of Desktop's ids", caps)
	}
	if get("env.ANTHROPIC_BASE_URL") != "" || get("model") != "opus" || get("autoCompactWindow") != "300000" || get("theme") != "dark" {
		t.Fatalf("the user's own Claude Code setup changed: %s", readFile(path))
	}
	// the catalog's round keeps it
	if err := a.Sync(); err != nil || window() != "256000" {
		t.Fatalf("after Sync: %q %v", window(), err)
	}
	// Claude Code wired in and out again while Desktop stays on
	if err := a.Field("model").Set("v/gpt"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if get("env.ANTHROPIC_BASE_URL") != "" {
		t.Fatalf("Claude Code still on magpie: %s", readFile(path))
	}
	if window() != "256000" {
		t.Fatalf("Claude Code taken off magpie, Desktop still on: %q", window())
	}
	// Desktop off: magpie's values go
	desktop("")
	if window() != "" || get("env."+claudeCapsEnv) != "" {
		t.Fatalf("Desktop off: %s", readFile(path))
	}
	if get("theme") != "dark" || get("autoCompactWindow") != "300000" {
		t.Fatalf("the user's settings changed: %s", readFile(path))
	}
}
