package whalebridge

import (
	"os"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"gopkg.in/yaml.v3"
)

func TestWhaleBridgeDSHOutputLeavesCompactionRoom(t *testing.T) {
	for _, tc := range []struct {
		window, output int
		set            bool
		want           int
	}{
		{524288, 524288, false, 196608}, {524288, 1048576, false, 196608}, {500000, 393216, false, 184464},
		{131072, 65536, false, 32768}, {1000000, 393216, false, 393216}, {400000, 128000, false, 128000},
		{200000, 393216, false, 50000}, {128000, 393216, false, 32000}, {524288, 393216, true, 393216},
		{524288, 1048576, true, 524288}, {0, 128000, false, 128000}, {200000, 0, false, 0},
	} {
		m := provider.Entry{Context: tc.window, Output: tc.output, OutputSet: tc.set}
		if got := dshOutput(m); got != tc.want {
			t.Errorf("window %d output %d explicit %v: got %d want %d", tc.window, tc.output, tc.set, got, tc.want)
		}
	}
}

func TestWhaleBridgeDSHSyncBudgetAndCompat(t *testing.T) {
	a := newProviderHTTP(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: a.vendor + "/v1", Key: "fixture", Models: []string{"m", "n"}, Contexts: map[string]int{"*": 500000}}); err != nil {
		t.Fatal(err)
	}
	// A per-model output is explicit and must survive the budget cap.
	if err := provider.SetModelOutput("relay/m", 393216); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("relay/m", []string{"low", "high"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelReasonless("relay/m", true); err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("WHALEBRIDGE_DSH_HOME")
	if err := os.MkdirAll(home+"/profiles/web", 0700); err != nil {
		t.Fatal(err)
	}
	initial := `# user's patch layer
- id: llm-pi-ai
  config:
    providers:
      other:
        apiKeyEnv: USER_KEY
      whalebridge:
        apiKeyEnv: WHALEBRIDGE_GATEWAY_KEY
        api: openai-completions
        models:
          - id: relay/m
            name: stale
            contextWindow: 1
            compat:
              maxTokensField: max_tokens
              supportsStore: false
          - id: removed/m
            compat:
              stale: true
- id: unrelated
  config:
    user: stays
`
	if err := os.WriteFile(profilePath(home), []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := syncDSH(); err != nil {
			t.Fatal(err)
		}
		doc, err := profileDocument(home)
		if err != nil {
			t.Fatal(err)
		}
		ps := member(member(configRow(doc, "llm-pi-ai"), "config"), "providers")
		if member(ps, "other") == nil {
			t.Fatal("another provider was lost")
		}
		models := member(member(ps, "whalebridge"), "models")
		var rows []struct {
			ID, Name string
			Context  int               `yaml:"contextWindow"`
			Output   int               `yaml:"maxTokens"`
			Efforts  map[string]string `yaml:"reasoningEfforts"`
			Compat   map[string]any
		}
		if err := models.Decode(&rows); err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, m := range rows {
			if m.ID == "relay/m" {
				found = true
				if m.Name == "stale" || m.Context != 500000 || m.Output != 393216 || len(m.Efforts) != 0 || m.Compat["maxTokensField"] != "max_tokens" || m.Compat["supportsStore"] != false {
					t.Fatalf("sync %d row %+v", i, m)
				}
			}
			if m.ID == "relay/n" && len(m.Compat) > 0 {
				t.Fatalf("new model inherited compat: %+v", m)
			}
		}
		if !found {
			t.Fatalf("model missing: %+v", rows)
		}
	}
}

func TestWhaleBridgeDSHGroupPreservesExplicitOutput(t *testing.T) {
	h := newProviderHTTP(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: h.vendor + "/v1", Key: "fixture", Models: []string{"m", "n"}, Contexts: map[string]int{"*": 500000}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelOutput("relay/m", 393216); err != nil {
		t.Fatal(err)
	}
	for _, g := range []provider.Group{{ID: "single", Name: "Single", Members: []string{"relay/m"}}, {ID: "mixed", Name: "Mixed", Members: []string{"relay/n", "relay/m"}}} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	if err := syncDSH(); err != nil {
		t.Fatal(err)
	}
	doc, err := profileDocument(os.Getenv("WHALEBRIDGE_DSH_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	models := member(member(member(member(configRow(doc, "llm-pi-ai"), "config"), "providers"), "whalebridge"), "models")
	var rows []routeModel
	if err := models.Decode(&rows); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, row := range rows {
		if row.ID == "group/single" || row.ID == "group/mixed" {
			found[row.ID] = true
			if row.MaxTokens != 393216 {
				t.Fatalf("explicit member output reduced in %s: %d", row.ID, row.MaxTokens)
			}
		}
	}
	if len(found) != 2 {
		t.Fatal("routing group missing from DSH")
	}
}

func TestWhaleBridgeDSHCompatStaysOnProtocolAndOwnedKey(t *testing.T) {
	parse := func(text string) *yaml.Node {
		t.Helper()
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(text), &n); err != nil {
			t.Fatal(err)
		}
		return n.Content[0]
	}
	cur := parse("api: openai-completions\nmodels:\n- id: m\n  compat:\n    maxTokensField: max_tokens\n    forceAdaptiveThinking: false\n")
	next := parse("api: anthropic-messages\nmodels:\n- id: m\n")
	keepModelCompat(cur, next)
	if member(member(next, "models").Content[0], "compat") != nil {
		t.Fatal("compat crossed protocol change")
	}
	next = parse("api: openai-completions\nmodels:\n- id: m\n  compat:\n    forceAdaptiveThinking: true\n- id: n\n")
	keepModelCompat(cur, next)
	compat := member(member(next, "models").Content[0], "compat")
	if member(compat, "maxTokensField").Value != "max_tokens" || member(compat, "forceAdaptiveThinking").Value != "true" {
		t.Fatal("new managed switch failed to win")
	}
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{parse("- id: llm-pi-ai\n  config:\n    providers:\n      whalebridge:\n        apiKeyEnv: USERS_OWN_KEY\n")}}
	if _, err := editRoute(doc, map[string]any{"apiKeyEnv": keyName, "api": "openai-completions"}, false, true); err == nil {
		t.Fatal("overwrote unowned route")
	}
}
