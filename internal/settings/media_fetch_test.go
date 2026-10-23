package settings

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
)

func TestMediaFetchDefaultsAndJSON(t *testing.T) {
	for _, data := range []string{`{}`, `{"theme":"dark"}`, `{"fetchPrivate":null}`} {
		var s Settings
		if err := json.Unmarshal([]byte(data), &s); err != nil {
			t.Fatal(err)
		}
		if got := s.normal().FetchPrivate; got != nil {
			t.Fatalf("settings without a local grant enabled one: %v", got)
		}
	}
	var empty Settings
	if err := json.Unmarshal([]byte(`{"fetchPrivate":[]}`), &empty); err != nil || empty.FetchPrivate == nil || len(empty.FetchPrivate) != 0 {
		t.Fatalf("explicit empty JSON list changed meaning: %v (%v)", empty.FetchPrivate, err)
	}
	want := []string{"media.internal.test", "media.internal.test:8443", "10.24.0.9", "10.24.0.0/16", "[fd00::9]:8443", "fd00::/64"}
	data, err := json.Marshal(Settings{Theme: "dark", FetchPrivate: want})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields["fetchPrivate"]) == 0 {
		t.Fatalf("local configuration did not serialize fetchPrivate: %v", err)
	}
	var back Settings
	if err := json.Unmarshal(data, &back); err != nil || !slices.Equal(back.FetchPrivate, want) || back.Theme != "dark" {
		t.Fatalf("grant JSON roundtrip lost values or unrelated settings: %v", err)
	}
}

func TestMediaFetchKeepOwnLocalGrant(t *testing.T) {
	locals := []struct {
		name  string
		grant []string
	}{
		{"nil", nil},
		{"empty", []string{}},
		{"values", []string{"machine.internal.test:8443", "10.42.0.0/16"}},
	}
	remotes := [][]string{nil, {}, {"foreign.internal.test", "192.168.99.0/24"}}
	for _, local := range locals {
		t.Run(local.name, func(t *testing.T) {
			for _, grant := range remotes {
				cur := Settings{Theme: "light", FetchPrivate: slices.Clone(local.grant)}
				incoming := Settings{Theme: "dark", FetchPrivate: slices.Clone(grant)}
				incoming.KeepOwn(cur)
				if !reflect.DeepEqual(incoming.FetchPrivate, local.grant) || incoming.Theme != "dark" {
					t.Fatalf("KeepOwn replaced local grants or blocked normal preferences: local=%v got=%v", local.grant, incoming.FetchPrivate)
				}
				if len(cur.FetchPrivate) > 0 {
					incoming.FetchPrivate[0] = "mutated-restored-value"
					if !reflect.DeepEqual(cur.FetchPrivate, local.grant) {
						t.Fatal("restored settings alias the current machine's grant slice")
					}
					incoming.KeepOwn(cur)
					cur.FetchPrivate[0] = "mutated-current-value"
					if !reflect.DeepEqual(incoming.FetchPrivate, local.grant) {
						t.Fatal("later current-settings edits mutate the kept grant slice")
					}
				}
			}
		})
	}
}

func TestMediaFetchSaveLoadPrivateGrant(t *testing.T) {
	root := t.TempDir()
	testenv.SetHome(t, root)
	for name, value := range map[string]string{
		"LAUNCHER_COMPONENT_DATA_DIR": filepath.Join(root, "component"),
		"XDG_CONFIG_HOME":             filepath.Join(root, "config"),
		"XDG_CACHE_HOME":              filepath.Join(root, "cache"),
		"APPDATA":                     filepath.Join(root, "roaming"),
		"LOCALAPPDATA":                filepath.Join(root, "local"),
	} {
		t.Setenv(name, value)
	}
	if s, err := LoadChecked(); err != nil || len(s.FetchPrivate) != 0 {
		t.Fatalf("fresh machine has a media grant: %v (%v)", s.FetchPrivate, err)
	}
	want := []string{"media.internal.test:8443", "10.50.0.0/16"}
	s := Load()
	s.FetchPrivate = slices.Clone(want)
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if s, err := LoadChecked(); err != nil || !slices.Equal(s.FetchPrivate, want) {
		t.Fatalf("saved local grants did not load: %v (%v)", s.FetchPrivate, err)
	}
}
