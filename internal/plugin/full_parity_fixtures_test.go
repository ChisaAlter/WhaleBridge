package plugin

import (
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
	"os"
	"path/filepath"
	"testing"
)

func storeSandbox(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	testenv.SetHome(t, dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeList(t testing.TB, body string) {
	t.Helper()
	if err := os.WriteFile(listPath(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
