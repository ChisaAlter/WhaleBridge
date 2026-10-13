package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// A custom provider serving a model models.dev knows from others (a
// Volcengine endpoint's glm-5.3-flash) hands Codex the reasoning levels
// those give it: the catalog entry had none, so the Codex app offered no
// effort and magpie's own control showed a level Codex never used.
func TestCodexCustomProviderModelHasEfforts(t *testing.T) {
	home, read := codexHome(t, "", "model_reasoning_effort = \"none\"\n")
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "zai":{"models":{"glm-5.3-flash":{"id":"glm-5.3-flash","reasoning_options":[{"type":"effort","values":["low","high","max"]}]},
	                   "glm-4.5-air":{"id":"glm-4.5-air"}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "volc", Name: "Volc", Key: "k", Chat: "http://127.0.0.1:1/api/v3",
		Models: []string{"glm-5.3-flash", "glm-4.5-air"}}); err != nil {
		t.Fatal(err)
	}
	cx := codex(home)
	if err := cx.Fields[0].Set("volc/glm-5.3-flash"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_reasoning_effort = "high"`) {
		t.Fatalf("effort not settled on the model's default:\n%s", cfg)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "magpie-models.json"))
	var cat struct {
		Models []struct {
			Slug    string  `json:"slug"`
			Default *string `json:"default_reasoning_level"`
			Levels  []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &cat); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range cat.Models {
		var ls []string
		for _, l := range m.Levels {
			ls = append(ls, l.Effort)
		}
		d := ""
		if m.Default != nil {
			d = *m.Default
		}
		got[m.Slug] = strings.Join(ls, ",") + " / " + d
	}
	if got["volc/glm-5.3-flash"] != "low,high,max / high" {
		t.Errorf("glm-5.3-flash: %q", got["volc/glm-5.3-flash"])
	}
	// a model no one says reasons keeps none
	if got["volc/glm-4.5-air"] != " / " {
		t.Errorf("glm-4.5-air: %q", got["volc/glm-4.5-air"])
	}
	effort := cx.Fields[1]
	var opts []string
	for _, o := range effort.Options(map[string]string{"model": "volc/glm-5.3-flash"}) {
		opts = append(opts, o.Value)
	}
	if strings.Join(opts, ",") != "low,high,max" {
		t.Errorf("effort options: %v", opts)
	}

	// unset, the control shows what Codex takes: the catalog's default
	if err := effort.Set(""); err != nil {
		t.Fatal(err)
	}
	if v := effort.Get(); v != "high" {
		t.Errorf("unset effort shows %q", v)
	}

	// a level the model doesn't take, left from before, is settled when
	// the catalog is synced
	if err := effort.Set("none"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_reasoning_effort = "high"`) {
		t.Errorf("sync left the effort:\n%s", cfg)
	}
}
