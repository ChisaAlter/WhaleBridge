package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/agentenv"
	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/sessions"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

func sessionHome(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, name := range agentenv.Vars {
		t.Setenv(name, "")
	}
	for _, name := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(name, root)
	}
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", filepath.Join(root, "component"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, ".cache"))
	t.Setenv("APPDATA", filepath.Join(root, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "AppData", "Local"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))
	t.Setenv("MAGPIE_NO_ACCOUNTS", "1")
	t.Setenv("PATH", "")
	s := settings.Load()
	s.NoWSLAgents = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

func pageHome(t *testing.T) {
	t.Helper()
	sessionHome(t)
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"id":"openai","models":{"m":{"id":"m","cost":{"input":2,"output":8,"cache_read":0.5,"cache_write":2.5}}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
}

func holdClock(t *testing.T, at time.Time) time.Time {
	t.Helper()
	old := Clock
	Clock = func() time.Time { return at }
	t.Cleanup(func() { Clock = old })
	return at
}
