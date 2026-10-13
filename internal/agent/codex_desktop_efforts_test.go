package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// #310: the Codex app's model picker offers only the efforts in its
// [desktop] enabled-reasoning-efforts (unset: low … xhigh, ultra,
// persistent), so a magpie model's "max" was never offered. Routed to one,
// magpie adds what its models take and the app knows; the user's entries,
// order and comments stay, and nothing is written when nothing is missing.
func TestCodexDesktopEfforts(t *testing.T) {
	setup := func(t *testing.T, config string) (string, func() string) {
		home, read := codexHome(t, "", config)
		os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
		os.WriteFile(catalog.CachePath(), []byte(`{
		  "zai":{"models":{"glm-5.3-flash":{"id":"glm-5.3-flash","reasoning_options":[{"type":"effort","values":["low","high","max","turbo"]}]}}}}`), 0o644)
		catalog.Reset()
		t.Cleanup(catalog.Reset)
		if err := provider.Save(provider.Provider{ID: "volc", Name: "Volc", Key: "k", Chat: "http://127.0.0.1:1/api/v3",
			Models: []string{"glm-5.3-flash"}}); err != nil {
			t.Fatal(err)
		}
		return home, read
	}

	t.Run("unset", func(t *testing.T) {
		home, read := setup(t, "# mine\n")
		if err := codex(home).Fields[0].Set("volc/glm-5.3-flash"); err != nil {
			t.Fatal(err)
		}
		cfg := read()
		if !strings.Contains(cfg, "[desktop]\nenabled-reasoning-efforts = [\"low\", \"medium\", \"high\", \"xhigh\", \"ultra\", \"persistent\", \"max\"]\n") ||
			!strings.Contains(cfg, "# mine") || strings.Contains(cfg, "turbo") {
			t.Fatalf("config:\n%s", cfg)
		}
	})

	t.Run("user's own", func(t *testing.T) {
		home, read := setup(t, "[desktop]\n# the ones I use\nenabled-reasoning-efforts = [\"high\", \"low\"] # mine\nother = 1\n")
		if err := codex(home).Fields[0].Set("volc/glm-5.3-flash"); err != nil {
			t.Fatal(err)
		}
		cfg := read()
		if !strings.Contains(cfg, "# the ones I use\nenabled-reasoning-efforts = [\"high\", \"low\", \"max\"] # mine\nother = 1\n") {
			t.Fatalf("config:\n%s", cfg)
		}
		// again: nothing missing, nothing written
		before, _ := os.Stat(filepath.Join(home, ".codex", "config.toml"))
		if err := codexEnableEfforts(filepath.Join(home, ".codex", "config.toml"), magpieModels("codex")); err != nil {
			t.Fatal(err)
		}
		if after, _ := os.Stat(filepath.Join(home, ".codex", "config.toml")); !after.ModTime().Equal(before.ModTime()) || read() != cfg {
			t.Fatalf("rewritten:\n%s", read())
		}
	})

	t.Run("not routed", func(t *testing.T) {
		home, read := setup(t, "")
		if err := codex(home).Fields[0].Set("gpt-5.5"); err != nil {
			t.Fatal(err)
		}
		if cfg := read(); strings.Contains(cfg, "desktop") {
			t.Fatalf("config:\n%s", cfg)
		}
	})

	t.Run("inline desktop", func(t *testing.T) {
		home, read := setup(t, "desktop = { theme = \"dark\" }\n")
		if err := codex(home).Fields[0].Set("volc/glm-5.3-flash"); err != nil {
			t.Fatal(err)
		}
		if cfg := read(); strings.Contains(cfg, "[desktop]") || !strings.Contains(cfg, "desktop = { theme = \"dark\" }") {
			t.Fatalf("config:\n%s", cfg)
		}
	})
}
