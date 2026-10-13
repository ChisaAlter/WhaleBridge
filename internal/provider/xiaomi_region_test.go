package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
)

func useXiaomiCatalog(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	b, err := os.ReadFile(filepath.Join("testdata", "xiaomi_catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["freebie"] = map[string]any{"id": "freebie", "models": map[string]any{
		"mimo-v2.6-flash": map[string]any{"id": "mimo-v2.6-flash", "cost": map[string]any{"input": 0, "output": 0}}}}
	b, _ = json.Marshal(m)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

func xiaomiAt(t *testing.T, region, cat string) Provider {
	t.Helper()
	pr := Preset("xiaomi")
	for _, r := range pr.Regions {
		if r.ID == region {
			return normalize(Provider{ID: "xiaomi", Name: "Xiaomi MiMo", Preset: "xiaomi", Key: "k",
				Chat: r.Chat, Responses: r.Responses, Anthropic: r.Anthropic, Catalog: cat})
		}
	}
	t.Fatalf("xiaomi has no region %q", region)
	return Provider{}
}

func TestXiaomiRegionCatalogs(t *testing.T) {
	useXiaomiCatalog(t)
	want := map[string][]string{
		"plan-cn":  {"xiaomi-token-plan-cn", "xiaomi"},
		"plan-sgp": {"xiaomi-token-plan-sgp", "xiaomi"},
		"plan-ams": {"xiaomi-token-plan-ams", "xiaomi"},
		"api":      {"xiaomi"},
	}
	pr := Preset("xiaomi")
	if len(pr.Regions) != len(want) {
		t.Fatalf("xiaomi has %d regions, the test knows %d", len(pr.Regions), len(want))
	}
	for region, cats := range want {
		// a new provider, the old default saved before this, and one
		// moved from another region in the editor
		for _, from := range []string{pr.Catalog, "xiaomi", "xiaomi-token-plan-sgp, xiaomi"} {
			if got := xiaomiAt(t, region, from).Catalogs(); !slices.Equal(got, cats) {
				t.Errorf("%s saved with %q: catalogs %v, want %v", region, from, got, cats)
			}
		}
		// added at that region from a link
		p, err := ParseImport("magpie://import?preset=xiaomi&region=" + region)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Catalogs(); !slices.Equal(got, cats) {
			t.Errorf("import at %s: catalogs %v, want %v", region, got, cats)
		}
	}
	// the preset's own endpoints are plan-cn's, and so is its catalog
	if p, _ := FromPreset("xiaomi"); !slices.Equal(p.Catalogs(), want["plan-cn"]) {
		t.Errorf("FromPreset(xiaomi) catalogs %v", p.Catalogs())
	}
	// a catalog the user named is theirs, at any region
	for _, region := range []string{"api", "plan-sgp"} {
		if got := xiaomiAt(t, region, "openrouter, xiaomi").Catalog; got != "openrouter, xiaomi" {
			t.Errorf("%s: the user's catalog became %q", region, got)
		}
	}
}
