package davsync

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/backup"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

func TestS3BlindReMergesDuringSameSyncNow(t *testing.T) {
	newComputer(t).use(t)
	f, _ := newFakeS3(t)
	f.noCond = true
	const object = "team x+y/magpie/magpie.magpie-backup"
	var replacement []byte
	armed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.ServeHTTP(w, r)
		f.mu.Lock()
		defer f.mu.Unlock()
		if armed && r.Method == http.MethodPut && r.Header.Get("If-Match") == "" && r.Header.Get("If-None-Match") == "" {
			// Another computer's blind write arrives after ours, before its
			// confirming read. This must be re-merged by this SyncNow call.
			f.store(object, replacement)
			armed = false
		}
	}))
	t.Cleanup(srv.Close)
	c := f.config(srv)
	if err := provider.Save(provider.Provider{ID: "first", Name: "First", Chat: srv.URL + "/v1", Key: "first-fixture-key"}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(c); err != nil {
		t.Fatal(err)
	}
	if err := SyncNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	other, err := backup.Open(f.objects[object], c.Passphrase)
	if err != nil {
		t.Fatal(err)
	}
	other.Settings.Currency = "cny"
	other.Created = time.Now()
	replacement, err = backup.Seal(other, c.Passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "second", Name: "Second", Chat: srv.URL + "/v1", Key: "second-fixture-key"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.log, armed = nil, true
	f.mu.Unlock()
	if err := SyncNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	stored, calls := bytes.Clone(f.objects[object]), slices.Clone(f.log)
	f.mu.Unlock()
	merged, err := backup.Open(stored, c.Passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Providers) != 2 || merged.Settings.Currency != "cny" || settings.Load().Currency != "cny" || !slices.Equal(ids(), []string{"first=first-fixture-key", "second=second-fixture-key"}) {
		t.Fatalf("same-sync merge lost the local provider or competing settings: %v currency %q, local %v / %q", merged.Providers, merged.Settings.Currency, ids(), settings.Load().Currency)
	}
	puts, gets := 0, 0
	for _, call := range calls {
		if call == "PUT" {
			puts++
		}
		if call == "GET" || len(call) > 4 && call[:4] == "GET " {
			gets++
		}
	}
	if puts != 2 || gets != 2 {
		t.Fatalf("must read the competitor and write the merge within one SyncNow: %v", calls)
	}
}

func TestGitHubExplicitUploadKeepsReplacedFile(t *testing.T) {
	newComputer(t).use(t)
	f := newGithubFixture(t)
	c := githubConfig()
	original := []byte(`{"format":"other-app","valuable":"keep me"}`)
	f.objects["main|team/magpie/"+file] = original
	if err := Configure(c); err != nil {
		t.Fatal(err)
	}
	if err := SyncNow(context.Background()); err == nil {
		t.Fatal("ordinary sync must not replace another app's file")
	}
	if f.writes != 0 || !bytes.Equal(f.objects["main|team/magpie/"+file], original) {
		t.Fatal("ordinary sync changed the target")
	}
	if err := Upload(context.Background()); err != nil {
		t.Fatal(err)
	}
	kept, err := filepath.Glob(filepath.Join(settings.Dir(), "sync", "*-server-replaced"+backup.Ext))
	if err != nil || len(kept) != 1 {
		t.Fatalf("replacement copy: %v %v", kept, err)
	}
	data, err := os.ReadFile(kept[0])
	if err != nil || !bytes.Equal(data, original) {
		t.Fatalf("original file was not preserved: %q %v", data, err)
	}
	if _, err := backup.Open(f.objects["main|team/magpie/"+file], c.Passphrase); err != nil {
		t.Fatal(err)
	}
	before := f.writes
	if err := Upload(context.Background()); err == nil || f.writes != before {
		t.Fatal("upload must not replace a valid backup")
	}
}

func TestSyncOwnsOnlyDSHModelSettings(t *testing.T) {
	newComputer(t).use(t)
	s := settings.Load()
	s.Visible = map[string][]string{"dsh": {"local"}, "codex": {"private-codex"}}
	s.HiddenModels = map[string][]string{"dsh": {"local/m"}, "claude": {"private/m"}}
	s.OrderedModels = map[string][]string{"dsh": {"local/m"}, "codex": {"private/m"}}
	s.PickedModels = map[string][]string{"dsh": {}, "codex": {}}
	s.FastPicks = map[string][]string{"dsh": {"local/m"}, "codex": {"private/m"}}
	s.AgentEfforts = map[string]string{"dsh": "low", "codex": "high"}
	s.OwnModelsHidden = []string{"codex", "dsh"}
	s.UIFont = json.RawMessage(`{"family":"Local Font"}`)
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	local, err := collect(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(local.Settings.Visible) != 1 || local.Settings.Visible["dsh"][0] != "local" || len(local.Settings.HiddenModels) != 1 || len(local.Settings.OrderedModels) != 1 || len(local.Settings.PickedModels) != 1 || len(local.Settings.FastPicks) != 1 || len(local.Settings.AgentEfforts) != 1 || !slices.Equal(local.Settings.OwnModelsHidden, []string{"dsh"}) || local.Settings.UIFont != nil {
		t.Fatal("first upload must contain dsh preferences only and no local fonts")
	}
	remote := backup.Bundle{Settings: &settings.Settings{Currency: "usd", Visible: map[string][]string{"dsh": {"old"}, "codex": {"remote-private"}}, PickedModels: map[string][]string{"codex": {}}, AgentEfforts: map[string]string{"codex": "xhigh"}, OwnModelsHidden: []string{"codex"}, UIFont: json.RawMessage(`{"family":"Remote Font"}`)}}
	before := hashes(remote)["settings"]
	remote.Settings.Visible["codex"] = []string{"remote-private-changed"}
	if hashes(remote)["settings"] != before {
		t.Fatal("external model preferences must not count as component settings conflicts")
	}
	take(&remote, local, "settings")
	if !slices.Equal(remote.Settings.Visible["codex"], []string{"remote-private-changed"}) || remote.Settings.AgentEfforts["codex"] != "xhigh" || !slices.Equal(remote.Settings.Visible["dsh"], []string{"local"}) || string(remote.Settings.UIFont) != `{"family":"Remote Font"}` {
		t.Fatalf("upload changed opaque remote preferences: %+v", remote.Settings)
	}
	if values, exists := remote.Settings.PickedModels["codex"]; !exists || len(values) != 0 {
		t.Fatal("explicit empty external picked-model list was erased")
	}
}
