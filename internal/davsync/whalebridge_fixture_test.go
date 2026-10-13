package davsync

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/agentenv"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// computer mirrors the fixed upstream test's isolated machines, including
// WhaleBridge's component data root rather than a real user's settings.
type computer string

func newComputer(t *testing.T) computer { return computer(t.TempDir()) }

func (c computer) use(t *testing.T) {
	t.Helper()
	for _, name := range agentenv.Vars {
		t.Setenv(name, "")
	}
	for _, name := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(name, string(c))
	}
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", filepath.Join(string(c), "component"))
	t.Setenv("WHALEBRIDGE_DSH_HOME", filepath.Join(string(c), "dsh"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(string(c), ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(string(c), ".cache"))
	t.Setenv("APPDATA", filepath.Join(string(c), "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(string(c), "AppData", "Local"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(string(c), ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(string(c), ".codex"))
	t.Setenv("MAGPIE_NO_ACCOUNTS", "1")
	t.Setenv("PATH", "")
}

func ids() []string {
	ps, _ := provider.Stored()
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID+"="+p.Key)
	}
	slices.Sort(out)
	return out
}
