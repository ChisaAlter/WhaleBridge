package whalebridge

import (
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/ChisaAlter/WhaleBridge/internal/filememo"
	"github.com/ChisaAlter/WhaleBridge/internal/gateway"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

const keyName = "WHALEBRIDGE_GATEWAY_KEY"

var dshWrites sync.Mutex

type routeModel struct {
	ID                  string            `yaml:"id"`
	Name                string            `yaml:"name"`
	ContextWindow       int               `yaml:"contextWindow,omitempty"`
	MaxTokens           int               `yaml:"maxTokens,omitempty"`
	CompactionThreshold int               `yaml:"compactionThreshold,omitempty"`
	Input               []string          `yaml:"input,omitempty"`
	ReasoningEfforts    map[string]string `yaml:"reasoningEfforts,omitempty"`
}

// DSH reserves maxTokens plus 65536 before it compacts. A catalog default
// leaves half the window for messages, or caps output at a quarter where
// that gives more room. An explicit output setting keeps the user's value.
func dshOutput(m provider.Entry) int {
	n := m.PublishedOutput()
	if m.OutputSet || m.Context <= 0 || n <= 0 {
		return n
	}
	return min(n, max(1, max(m.Context/4, m.Context/2-65536)))
}

func syncDSH() error {
	home := os.Getenv("WHALEBRIDGE_DSH_HOME")
	if home == "" {
		return fmt.Errorf("缺少 DSH 数据目录")
	}
	dshWrites.Lock()
	defer dshWrites.Unlock()
	defer provider.Hold()()
	defer filememo.Hold()()
	models, _ := provider.CatalogFor("dsh")
	labels := provider.Labels(models)
	modelSettings := provider.HeldSettings()
	findGroup := provider.GroupFinder()
	compactAt := modelSettings.Compact()
	out := make([]routeModel, 0, len(models))
	for i, m := range models {
		row := routeModel{ID: m.ID, Name: labels[i], ContextWindow: m.Context, MaxTokens: dshOutput(m)}
		row.CompactionThreshold = provider.CompactFor(modelSettings, m, findGroup)
		if row.CompactionThreshold == 0 && compactAt > 0 && row.ContextWindow > compactAt {
			row.CompactionThreshold = compactAt
		}
		if row.ContextWindow > 0 && row.MaxTokens > row.ContextWindow {
			row.MaxTokens = row.ContextWindow
		}
		if m.Images {
			row.Input = []string{"text", "image"}
		} else if m.ImageInput != nil {
			row.Input = []string{"text"}
		}
		levels := map[string]string{}
		for _, level := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
			effort := level
			if level == "off" {
				effort = "none"
			}
			if slices.Contains(m.Efforts, effort) {
				levels[level] = effort
			}
		}
		if len(levels) > 1 || len(levels) == 1 && levels["off"] == "" {
			row.ReasoningEfforts = levels
		}
		out = append(out, row)
	}
	route := struct {
		DisplayName string       `yaml:"displayName"`
		APIKeyEnv   string       `yaml:"apiKeyEnv"`
		API         string       `yaml:"api"`
		BaseURL     string       `yaml:"baseURL"`
		Models      []routeModel `yaml:"models"`
	}{"鲸Bridge", keyName, "openai-completions", gateway.URL() + "/v1", out}
	return updateProfile(home, route, false)
}

func Disconnect(home string) error {
	if home == "" {
		return fmt.Errorf("缺少 DSH 数据目录")
	}
	mutations.Lock()
	defer mutations.Unlock()
	if err := disconnectManagedAgents(); err != nil {
		return err
	}
	dshWrites.Lock()
	defer dshWrites.Unlock()
	return updateProfile(home, nil, true)
}
func isDefault() bool { return profileDefault(os.Getenv("WHALEBRIDGE_DSH_HOME")) }
