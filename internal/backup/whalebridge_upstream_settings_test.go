package backup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/agentenv"
	"github.com/ChisaAlter/WhaleBridge/internal/lastgood"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.RunIn(m, func(string) {
		testenv.Offline()
		if err := os.Setenv("LAUNCHER_COMPONENT_DATA_DIR", ""); err != nil {
			panic(err)
		}
	}))
}

func upstreamSettingsHome(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, name := range agentenv.Vars {
		t.Setenv(name, "")
	}
	for _, name := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(name, root)
	}
	t.Setenv("LAUNCHER_COMPONENT_ID", "whalebridge")
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", filepath.Join(root, "component"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, ".cache"))
	t.Setenv("MAGPIE_NO_ACCOUNTS", "1")
	t.Setenv("PATH", "")
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestWhaleBridgeUpstreamCollectStopsOnUnknownSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		dir  bool
	}{
		{"cut short", []byte(`{"theme":"dark","lang":`), false},
		{"zeroed", make([]byte, 64), false},
		{"wrong field type", []byte(`{"theme":123}`), false},
		{"unreadable directory", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstreamSettingsHome(t)
			if tc.dir {
				if err := os.Mkdir(settings.Path(), 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(settings.Path(), tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, keys := range []bool{false, true} {
				b, err := Collect(keys, "test")
				if err == nil || !strings.Contains(err.Error(), "settings") || b.Settings != nil {
					t.Fatalf("unknown settings were collected as defaults: keys=%v settings=%+v err=%v", keys, b.Settings, err)
				}
			}
			if !tc.dir {
				if got, err := os.ReadFile(settings.Path()); err != nil || !bytes.Equal(got, tc.data) {
					t.Fatalf("Collect changed corrupt local settings: %v", err)
				}
			}
		})
	}
}

func TestWhaleBridgeUpstreamCollectMissingEmptyAndRecoveredSettings(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		upstreamSettingsHome(t)
		if b, err := Collect(true, "test"); err != nil || b.Settings != nil {
			t.Fatalf("missing settings must stay absent: %+v (%v)", b.Settings, err)
		}
	})
	t.Run("empty", func(t *testing.T) {
		upstreamSettingsHome(t)
		if err := os.WriteFile(settings.Path(), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if b, err := Collect(true, "test"); err != nil || b.Settings == nil {
			t.Fatalf("empty settings must retain their default behavior: %+v (%v)", b.Settings, err)
		}
	})
	t.Run("last good generation", func(t *testing.T) {
		upstreamSettingsHome(t)
		s := settings.Settings{Theme: "dark", Lang: "ja", TextSize: 125, GitHubToken: "private-fixture-token"}
		good, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lastgood.Bak(settings.Path()), good, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(settings.Path(), make([]byte, 64), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, keys := range []bool{false, true} {
			b, err := Collect(keys, "test")
			if err != nil || b.Settings == nil || b.Settings.Theme != "dark" || b.Settings.Lang != "ja" || b.Settings.TextSize != 125 {
				t.Fatalf("valid last-good settings were not collected: %+v (%v)", b.Settings, err)
			}
			if (b.Settings.GitHubToken != "") != keys {
				t.Fatal("recovered settings bypassed backup credential redaction")
			}
			if b.Library != nil {
				t.Fatal("WhaleBridge backup unexpectedly carries the library")
			}
		}
	})
}
