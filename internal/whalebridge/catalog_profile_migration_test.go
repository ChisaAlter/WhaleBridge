package whalebridge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

func TestWhaleBridgeCatalogLegacyDSHDefaultAndCompat(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "retired", true: "custom_endpoint"}[custom], func(t *testing.T) {
			h := newProviderHTTP(t)
			if custom {
				if err := provider.Save(provider.Provider{ID: "zed-plugin", Name: "Custom endpoint", Chat: h.vendor + "/v1", Key: "fixture-key", Models: []string{"m"}}); err != nil {
					t.Fatal(err)
				}
			}
			home := os.Getenv("WHALEBRIDGE_DSH_HOME")
			initial := `- id: llm-pi-ai
  config:
    providers:
      whalebridge:
        apiKeyEnv: WHALEBRIDGE_GATEWAY_KEY
        api: openai-completions
        models:
          - id: zed-plugin/m
            compat:
              maxTokensField: max_tokens
          - id: zed/m
            compat:
              maxTokensField: max_completion_tokens
- id: agent-default-model
  config:
    provider: whalebridge
    model: zed-plugin/m
    reasoningEffort: high
`
			for _, p := range []string{profilePath(home), filepath.Join(home, "cordis.patch.yml")} {
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(initial), 0600); err != nil {
					t.Fatal(err)
				}
			}
			id := "zed/m"
			if custom {
				id = "zed-plugin/m"
			}
			route := map[string]any{"apiKeyEnv": keyName, "api": "openai-completions", "models": []map[string]any{{"id": id}}}
			if err := updateProfile(home, route, false); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{profilePath(home), filepath.Join(home, "cordis.patch.yml")} {
				doc, err := patchDocument(p)
				if err != nil {
					t.Fatal(err)
				}
				cfg := member(configRow(doc, "agent-default-model"), "config")
				if member(cfg, "provider").Value != "whalebridge" || member(cfg, "model").Value != id || member(cfg, "reasoningEffort").Value != "high" {
					t.Fatal("model spelling migration changed the chosen model or effort")
				}
				models := member(member(member(configRow(doc, "llm-pi-ai"), "config"), "providers"), "whalebridge")
				compat := member(member(models, "models").Content[0], "compat")
				want := "max_completion_tokens"
				if custom {
					want = "max_tokens"
				}
				if member(compat, "maxTokensField").Value != want {
					t.Fatal("canonical compat lost or custom endpoint stolen")
				}
			}
		})
	}
}
