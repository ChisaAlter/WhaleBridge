package provider

import (
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

func TestWhaleBridgeReasonlessPreferenceRoundTrip(t *testing.T) {
	isolate(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://fixture.invalid/v1", Key: "fixture", Models: []string{"m", "n"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if err := SetModelEfforts("relay/m", []string{"low", "high"}); err != nil {
		t.Fatal(err)
	}
	if err := SetModelPrefs("relay", map[string]ModelPref{"m": {Reasonless: new(true)}}); err != nil {
		t.Fatal(err)
	}
	if !Reasonless("relay", "m") || Reasonless("relay", "n") || len(p.ModelEfforts()["m"]) != 0 {
		t.Fatalf("reasonless preference: %+v", settings.Load())
	}
	if e := entryFor(p, catalog.Model{ID: "m", Reasoning: true, Efforts: []string{"low", "high"}}, settings.Load()); !e.Quiet() || !e.Reasonless || len(e.Efforts) > 0 || e.Reasoning || e.AgentsV2 {
		t.Fatalf("entry still reasons: %+v", e)
	}
	if err := SetModelPrefs("relay", map[string]ModelPref{"m": {Name: new("Mine")}}); err != nil {
		t.Fatal(err)
	}
	if !Reasonless("relay", "m") {
		t.Fatal("an absent reasonless field cleared the preference")
	}
	if err := SetModelPrefs("relay", map[string]ModelPref{"m": {Reasonless: new(false)}}); err != nil {
		t.Fatal(err)
	}
	if Reasonless("relay", "m") {
		t.Fatal("false did not restore reasoning")
	}
	for _, levels := range [][]string{{"low", "high"}, nil} {
		if err := SetModelReasonless("relay/m", true); err != nil {
			t.Fatal(err)
		}
		if err := SetModelEfforts("relay/m", levels); err != nil {
			t.Fatal(err)
		}
		if Reasonless("relay", "m") {
			t.Fatalf("levels %v did not clear reasonless", levels)
		}
	}
	if err := SetModelReasonless("relay/m", true); err != nil {
		t.Fatal(err)
	}
	if err := Rename("relay", "renamed"); err != nil {
		t.Fatal(err)
	}
	if !Reasonless("renamed", "m") || settings.Load().ModelReasonless["relay/m"] {
		t.Fatal("provider rename lost or duplicated reasonless")
	}
}

func TestWhaleBridgeReasonlessGroupKeepsOtherLevels(t *testing.T) {
	isolate(t)
	if err := store(file{Groups: []Group{{ID: "mix", Name: "Mix", Members: []string{"a/m:high", "b/m"}}}, NoAutoGroups: true}); err != nil {
		t.Fatal(err)
	}
	levels := []string{"low", "high"}
	a := Entry{ID: "a/m", Model: "m", Name: "A", Provider: Provider{ID: "a"}, Reasonless: true}
	b := Entry{ID: "b/m", Model: "m", Name: "B", Provider: Provider{ID: "b"}, Efforts: levels, Reasoning: true}
	for _, entries := range [][]Entry{{a, b}, {b, a}} {
		groups := groupEntries(entries)
		if len(groups) != 1 || !groups[0].Reasoning || !slices.Equal(groups[0].Efforts, levels) {
			t.Fatalf("group lost reasoning levels: %+v", groups)
		}
	}
}

func TestWhaleBridgeLegacyModelSettingsReadAndMigration(t *testing.T) {
	isolate(t)
	old, now := "cursor-plugin", "cursor"
	s := settings.Settings{
		ModelNames:      map[string]string{old + "/m": "Legacy", now + "/m": "Canonical"},
		ModelEfforts:    map[string][]string{old + "/m": {"low", "high"}},
		ModelReasonless: map[string]bool{old + "/n": true},
		ModelImages:     map[string]bool{old + "/m": false}, ModelAPIs: map[string]string{old + "/m": "chat"},
		ModelOutputs: map[string]int{old + "/*": 32000}, ModelCompacts: map[string]int{old + "/m": 64000},
		ModelWires:   map[string]string{old + "/m": "upstream/*"},
		HiddenModels: map[string][]string{"dsh": {old + "/m"}}, PickedModels: map[string][]string{"dsh": {old + "/m", now + "/m"}},
		FastPicks: map[string][]string{"dsh": {old + "/m"}}, OrderedModels: map[string][]string{"dsh": {old + "/n", old + "/m"}},
		Visible: map[string][]string{"dsh": {old}}, DisabledModels: []string{old + "/disabled"}, Searcher: old,
	}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := store(file{Groups: []Group{{ID: "mix", Name: "Mix", Members: []string{old + "/m:high"}, Fast: []string{old + "/m:high"}, Off: []string{old + "/m:high"}, Pick: old + "/m:high", Classifier: old + "/n", Weights: map[string]int{old + "/m:high": 3}}}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(settings.Path())
	if err != nil {
		t.Fatal(err)
	}
	view := canonicalModelSettings(settings.Load())
	beforeGroup := Group{ID: "auto-m", Members: []string{old + "/m", "relay/m"}}
	if got := withoutRemoved(beforeGroup, map[string]bool{now: true, "relay": true}); !slices.Equal(got.Members, beforeGroup.Members) {
		t.Fatalf("read removed safe legacy alias before migration: %+v", got)
	}
	if view.ModelNames[now+"/m"] != "Canonical" || view.ModelReasonless[now+"/n"] != true || view.ModelOutputs[now+"/*"] != 32000 || view.ModelImages[now+"/m"] != false || !slices.Equal(view.PickedModels["dsh"], []string{now + "/m"}) || view.Visible["dsh"][0] != now {
		t.Fatalf("canonical read: %+v", view)
	}
	if !reflect.DeepEqual(settings.Load().ModelNames, s.ModelNames) || settings.Load().ModelReasonless[now+"/n"] {
		t.Fatal("read fallback mutated original maps")
	}
	after, _ := os.ReadFile(settings.Path())
	if string(after) != string(before) {
		t.Fatal("read fallback rewrote settings")
	}
	if err := MigrateLegacyPluginSettings(old, now); err != nil {
		t.Fatal(err)
	}
	s = settings.Load()
	if s.ModelNames[now+"/m"] != "Canonical" || s.ModelNames[old+"/m"] != "" || !s.ModelReasonless[now+"/n"] || s.ModelOutputs[now+"/*"] != 32000 || s.Searcher != now {
		t.Fatalf("persisted settings: %+v", s)
	}
	g := load().Groups[0]
	if g.Members[0] != now+"/m:high" || g.Fast[0] != now+"/m:high" || g.Off[0] != now+"/m:high" || g.Pick != now+"/m:high" || g.Classifier != now+"/n" || g.Weights[now+"/m:high"] != 3 {
		t.Fatalf("legacy group refs: %+v", g)
	}
	if err := MigrateLegacyPluginSettings(old, now); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: old, Name: "Actual custom", Chat: "https://fixture.invalid/v1", Key: "fixture", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	s = settings.Load()
	s.ModelReasonless[old+"/m"] = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyPluginSettings(old, now); err != nil {
		t.Fatal(err)
	}
	if !settings.Load().ModelReasonless[old+"/m"] || canonicalModelSettings(settings.Load()).ModelReasonless[now+"/m"] {
		t.Fatal("custom endpoint was rekeyed")
	}
}
