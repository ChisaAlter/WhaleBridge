package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeOnlyPluginsStayOutOfProviderHost(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	for _, name := range []string{"quota.middleware.js", "editor.agent.js"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(`throw new Error("runtime-only module entered provider host");`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Add(ctx, path); err != nil {
			t.Fatal(err)
		}
	}
	both := filepath.Join(dir, "both")
	if err := os.MkdirAll(both, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(both, "package.json"), []byte(`{"name":"both","magpie":{"agent":"editor.js","middleware":"quota.js"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(ctx, both); err != nil {
		t.Fatal(err)
	}
	ordinary, err := filepath.Abs("testdata/fake/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Add(ctx, ordinary); err != nil {
		t.Fatal(err)
	}
	ps, err := Providers(ctx)
	if err != nil || len(ps) != 1 || ps[0].ID != "fakeco" {
		t.Fatalf("provider host did not retain only the ordinary plugin: %+v %v", ps, err)
	}
	ls, err := Plugins(ctx)
	if err != nil || len(ls) != 1 || ls[0].Spec != ordinary || ls[0].Error != "" {
		t.Fatalf("runtime-only plugin was loaded by provider host: %+v %v", ls, err)
	}
	if es := Load().Plugins; len(es) != 4 {
		t.Fatalf("runtime-only plugins were removed from their saved configuration: %+v", es)
	}
}
