package proc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Reading an installed npm shim must never start its CLI: Cline starts an
// updater even for --version unless explicitly told otherwise.
func TestVersionReadsNPMPackageWithoutStartingCLI(t *testing.T) {
	old := runVersion
	t.Cleanup(func() { runVersion = old })
	started := 0
	runVersion = func(string) (string, error) { started++; return "", nil }
	for _, tc := range []struct{ name, shim, pkg string }{
		{"cline.cmd", "@ECHO off\r\n\"%dp0%\\node_modules\\cline\\bin\\cline\" %*\r\n", "cline"},
		{"cline", "#!/bin/sh\nexec node \"$basedir/node_modules/cline/bin/cline\" \"$@\"\n", "cline"},
		{"codex.ps1", "& node \"$basedir/node_modules/@openai/codex/bin/codex.js\" $args\n", "@openai/codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pkg := filepath.Join(dir, "node_modules", filepath.FromSlash(tc.pkg))
			if err := os.MkdirAll(pkg, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"version":"3.0.71"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(dir, tc.name)
			if err := os.WriteFile(bin, []byte(tc.shim), 0o755); err != nil {
				t.Fatal(err)
			}
			if got := Version(bin); got != "3.0.71" || started != 0 {
				t.Fatalf("version %q, CLI started %d times", got, started)
			}
		})
	}
}

func TestVersionProbeSuppressesClineAutoUpdate(t *testing.T) {
	dir := t.TempDir()
	name, body := "cline", "#!/bin/sh\nprintf 'cline %s' \"$CLINE_NO_AUTO_UPDATE\"\n"
	if runtime.GOOS == "windows" {
		name, body = "cline.cmd", "@echo off\r\necho cline %CLINE_NO_AUTO_UPDATE%\r\n"
	}
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	// An existing contrary environment value must be replaced for the probe.
	t.Setenv("CLINE_NO_AUTO_UPDATE", "0")
	got, err := runVersion(bin)
	if err != nil || strings.TrimSpace(got) != "cline 1" {
		t.Fatalf("probe %q, %v", got, err)
	}
}
