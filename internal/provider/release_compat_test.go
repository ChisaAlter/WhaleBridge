package provider

import (
	"reflect"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// Published provider controls and the later delisted selection must survive
// the same save. This catches merges which preserve either schema alone.
func TestReleaseProviderControlsAndDelistedRoundTrip(t *testing.T) {
	whaleCatalogHome(t)
	p := Provider{ID: "release-fixture", Name: "Release", Chat: "https://release.fixture.invalid/v1", Key: "synthetic", Models: []string{"model"}, Delisted: []string{"temporarily-gone"}, PickedFrom: []string{"model", "temporarily-gone"}, Routing: FreeFirst,
		QuotaResetAt: "08:00+08:00", RestCap: RestCapOff, BalanceURL: "https://release.fixture.invalid/balance", BalanceMethod: "POST", InsecureTLS: true, NativeCompaction: true, RefreshEvery: "6h", AutoInclude: []string{"gpt-*"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	before := whaleCatalogProvider(t, p.ID)
	before.Name = "Renamed display"
	if err := Save(before); err != nil {
		t.Fatal(err)
	}
	got := whaleCatalogProvider(t, p.ID)
	if got.QuotaResetAt != p.QuotaResetAt || got.RestCap != p.RestCap || got.BalanceMethod != p.BalanceMethod || !got.InsecureTLS || !got.NativeCompaction || got.RefreshEvery != p.RefreshEvery || got.Routing != FreeFirst || !reflect.DeepEqual(got.AutoInclude, p.AutoInclude) || !reflect.DeepEqual(got.Delisted, p.Delisted) {
		t.Fatalf("mixed provider controls lost: %+v", got)
	}
}
func TestReleaseLegacyAliasesPreserveNewModelChoices(t *testing.T) {
	s := settings.Settings{UltraPicks: map[string][]string{"codex": {"zed-plugin/model"}}, FastPicks: map[string][]string{"codex": {"zed-plugin/model"}}, DesktopSubagentModel: "zed-plugin/model", DesktopSubagentEffort: "high", PluginOptions: map[string]map[string]bool{"zed": {"allow": true}}, CodexAutoResetOnly: map[string]string{"fixture@example.test": "week"}}
	got, changed := remapModelSettings(s, map[string]string{"zed-plugin": "zed"})
	if !changed || got.UltraPicks["codex"][0] != "zed/model" || got.FastPicks["codex"][0] != "zed/model" || got.DesktopSubagentModel != "zed/model" || got.DesktopSubagentEffort != "high" {
		t.Fatalf("new choices were not mapped: %+v", got)
	}
	if !got.PluginOptions["zed"]["allow"] || got.CodexAutoResetOnly["fixture@example.test"] != "week" {
		t.Fatal("unrelated published choices changed during alias migration")
	}
	if s.UltraPicks["codex"][0] != "zed-plugin/model" || s.DesktopSubagentModel != "zed-plugin/model" {
		t.Fatal("read-only alias mapping mutated its input")
	}
}
