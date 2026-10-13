package backup

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/agentenv"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

func TestRestorePreservesOtherAgentPreferencesAndLocalConsent(t *testing.T) {
	root := t.TempDir()
	for _, name := range agentenv.Vars {
		t.Setenv(name, "")
	}
	for _, name := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(name, root)
	}
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", filepath.Join(root, "component"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, ".cache"))
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, ".claude"))
	t.Setenv("MAGPIE_NO_ACCOUNTS", "1")
	t.Setenv("PATH", "")
	s := settings.Load()
	s.Visible = map[string][]string{"codex": {"own-provider"}, "dsh": {"old"}}
	s.HiddenModels = map[string][]string{"codex": {"own/hidden"}}
	s.OrderedModels = map[string][]string{"claude": {"own/second", "own/first"}}
	s.PickedModels = map[string][]string{"codex": {}}
	s.FastPicks = map[string][]string{"claude": {"own/fast"}}
	s.AgentEfforts = map[string]string{"codex": "xhigh"}
	s.OwnModelsHidden = []string{"codex"}
	s.NoAccountDiscovery, s.QuotaReads = true, "asked"
	s.LANKey = "local-private-key"
	s.UIFont = json.RawMessage(`{"family":"Own Font"}`)
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetGatewayConversations(true); err != nil {
		t.Fatal(err)
	}
	before := settings.Load()
	in := settings.Settings{Currency: "cny", Visible: map[string][]string{"codex": {"foreign"}, "dsh": {"new-dsh"}}, HiddenModels: map[string][]string{"codex": {"foreign/m"}}, OrderedModels: map[string][]string{"claude": {"foreign/m"}}, PickedModels: map[string][]string{"codex": {"foreign/m"}, "dsh": {}}, FastPicks: map[string][]string{"claude": {"foreign/m"}}, OwnModelsHidden: []string{"claude", "dsh"}, AgentEfforts: map[string]string{"codex": "low", "dsh": "medium"}, UIFont: json.RawMessage(`{"family":"Foreign Font"}`)}
	result, err := Restore(Bundle{Version: BundleVersion, Keys: true, Settings: &in}, Parts{Settings: true})
	if err != nil || !result.Settings {
		t.Fatalf("restore: %+v %v", result, err)
	}
	got := settings.Load()
	if got.Currency != "cny" || !slices.Equal(got.Visible["dsh"], []string{"new-dsh"}) || got.AgentEfforts["dsh"] != "medium" {
		t.Fatal("global/dsh settings were not restored")
	}
	if !reflect.DeepEqual(got.Visible["codex"], before.Visible["codex"]) || !reflect.DeepEqual(got.HiddenModels, before.HiddenModels) || !reflect.DeepEqual(got.OrderedModels, before.OrderedModels) || !reflect.DeepEqual(got.FastPicks, before.FastPicks) || got.AgentEfforts["codex"] != before.AgentEfforts["codex"] || !slices.Equal(got.OwnModelsHidden, []string{"codex", "dsh"}) {
		t.Fatalf("restored external agent preferences: %+v", got)
	}
	if picks, exists := got.PickedModels["codex"]; !exists || len(picks) != 0 {
		t.Fatal("empty external only-picked list lost its meaning")
	}
	if !got.NoAccountDiscovery || got.QuotaReads != "asked" || !got.GatewayConversations || got.LANKey != before.LANKey || !reflect.DeepEqual(got.UIFont, before.UIFont) {
		t.Fatal("restore changed local privacy, private access, recording consent or font")
	}
}
