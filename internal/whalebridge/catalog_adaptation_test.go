package whalebridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/backup"
	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

func TestWhaleBridgeCatalogHTTPProtocolMetadata(t *testing.T) {
	h := newProviderHTTP(t)
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"claude-only","supported_endpoints":["/v1/messages"]},{"id":"chat-only","supported_endpoints":["/v1/chat/completions"]}]}`)
	}))
	t.Cleanup(vendor.Close)
	var listed struct {
		Models []struct {
			ID   string
			Auto []string
		}
	}
	if err := json.Unmarshal(h.call("POST", "provider/list", map[string]any{"chat": vendor.URL + "/v1", "key": "fixture-key"}, 200), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 2 || !slices.Equal(listed.Models[0].Auto, []string{"anthropic"}) {
		t.Fatalf("draft list lost API metadata: %+v", listed)
	}
	id := h.save(map[string]any{"new": true, "id": "relay", "name": "Relay", "chat": vendor.URL + "/v1", "key": "fixture-key", "models": []string{"claude-only", "chat-only"}})
	var state struct {
		Providers []struct {
			ID                    string
			Speaks                []string
			Models, DefaultModels []string
			Available             []struct {
				ID    string
				NoAPI bool
			}
		}
	}
	if err := json.Unmarshal(h.call("GET", "state", nil, 200), &state); err != nil {
		t.Fatal(err)
	}
	for _, p := range state.Providers {
		if p.ID == id {
			if !slices.Contains(p.Models, "claude-only") || slices.Contains(p.DefaultModels, "claude-only") || !slices.Equal(p.Speaks, []string{"chat"}) {
				t.Fatalf("saved picks/defaults/apis disagree: %+v", p)
			}
			for _, m := range p.Available {
				if m.ID == "claude-only" && !m.NoAPI {
					t.Fatal("unusable old pick has no reason in editor")
				}
			}
			h.save(map[string]any{"id": id, "modelPrefs": map[string]any{"claude-only": map[string]any{"api": "chat"}}})
			saved := h.provider(id)
			if saved.NoAPIFor("claude-only") || len(saved.Exposed()) != 2 {
				t.Fatal("single-endpoint API override was saved but ignored")
			}
			return
		}
	}
	t.Fatal("relay absent from state")
}

func TestWhaleBridgeCatalogHTTPDelistedAndOldLimits(t *testing.T) {
	h := newProviderHTTP(t)
	var short atomic.Bool
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if short.Load() {
			io.WriteString(w, `{"data":[{"id":"a"}]}`)
		} else {
			io.WriteString(w, `{"data":[{"id":"a"},{"id":"b"}]}`)
		}
	}))
	t.Cleanup(vendor.Close)
	id := h.save(map[string]any{"new": true, "id": "relay", "name": "Relay", "chat": vendor.URL + "/v1", "key": "fixture-key", "models": []string{"a", "b"}, "outputs": map[string]int{"b": 2048}, "compacts": map[string]int{"b": 64000}})
	short.Store(true)
	h.call("POST", "provider/models", map[string]any{"id": id}, 200)
	p := h.provider(id)
	if !slices.Equal(p.Models, []string{"a"}) || !slices.Equal(p.Delisted, []string{"b"}) {
		t.Fatal("short list did not preserve the pending pick")
	}
	// Unticking the last available choice still waits for the absent one.
	h.save(map[string]any{"id": id, "models": []string{}, "clearDelisted": false})
	p = h.provider(id)
	if len(p.Models) != 0 || !slices.Equal(p.Delisted, []string{"b"}) || len(p.Exposed()) != 0 {
		t.Fatalf("all picks absent became defaults: %+v", p.Delisted)
	}
	dto := supplierInfo(*p)
	if len(dto["defaultModels"].([]string)) != 1 {
		t.Fatal("waiting state contaminated default-model preview")
	}
	h.save(map[string]any{"id": id, "name": "Still waiting", "models": []string{}, "outputs": map[string]int{"b": 2048}, "compacts": map[string]int{"b": 64000}})
	p = h.provider(id)
	if !slices.Equal(p.Delisted, []string{"b"}) {
		t.Fatal("unrelated editor save dropped waiting picks")
	}
	before, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	h.call("POST", "provider/save", map[string]any{"id": id, "name": "Must not persist", "outputs": map[string]int{"b": 4096}}, 400)
	after, _ := os.ReadFile(provider.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("invalid changed output partially saved supplier")
	}
	h.call("POST", "provider/save", map[string]any{"id": id, "name": "Must not persist either", "compacts": map[string]int{"typo": 64000}}, 400)
	after, _ = os.ReadFile(provider.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("invalid compact partially saved supplier")
	}
	short.Store(false)
	h.call("POST", "provider/models", map[string]any{"id": id}, 200)
	p = h.provider(id)
	if !slices.Equal(p.Models, []string{"b"}) || len(p.Delisted) != 0 || settings.Load().ModelOutputs[id+"/b"] != 2048 {
		t.Fatal("relisting did not restore original choice and limits")
	}
	short.Store(true)
	h.call("POST", "provider/models", map[string]any{"id": id}, 200)
	h.save(map[string]any{"id": id, "models": []string{}, "clearDelisted": true, "noModels": false})
	p = h.provider(id)
	if len(p.Delisted) != 0 || len(p.Exposed()) != 1 {
		t.Fatal("explicit defaults did not clear waiting state")
	}
	// A refresh can discover multiple missing picks after the editor has
	// already unticked one. Remove only that one, keeping the other pending.
	p.Models, p.Delisted = []string{"a"}, []string{"b", "c"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	h.save(map[string]any{"id": id, "models": []string{"a"}, "removeDelisted": []string{"b"}})
	p = h.provider(id)
	if !slices.Equal(p.Delisted, []string{"c"}) || !slices.Equal(p.Models, []string{"a"}) {
		t.Fatalf("targeted waiting removal lost another choice: %+v", p)
	}
}

// A refresh reports the rule-selected additions separately from relisted picks,
// so an open editor can retain its draft without undoing the new rules' choices.
func TestWhaleBridgeCatalogHTTPRefreshReportsRuleAdditions(t *testing.T) {
	h := newProviderHTTP(t)
	var expanded atomic.Bool
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if expanded.Load() {
			io.WriteString(w, `{"data":[{"id":"a"},{"id":"b"},{"id":"new-matched"},{"id":"other-new"}]}`)
		} else {
			io.WriteString(w, `{"data":[{"id":"a"},{"id":"b"}]}`)
		}
	}))
	t.Cleanup(vendor.Close)
	id := h.save(map[string]any{"new": true, "id": "rules", "name": "Rules", "chat": vendor.URL + "/v1", "key": "fixture-rules-key", "models": []string{"a", "b"}})
	h.save(map[string]any{"id": id, "models": []string{"a"}, "pickedFrom": []string{"a", "b"}, "autoInclude": []string{"new-*", "b*"}})
	// A model already matched when the rules were set can still be unticked.
	h.save(map[string]any{"id": id, "models": []string{"a"}, "pickedFrom": []string{"a", "b"}})
	expanded.Store(true)
	var response struct {
		Taken, Dropped []string
		Provider       struct{ Chosen []string }
	}
	if err := json.Unmarshal(h.call("POST", "provider/models", map[string]any{"id": id}, 200), &response); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(response.Taken, []string{"new-matched"}) || len(response.Dropped) != 0 || !slices.Contains(response.Provider.Chosen, "new-matched") || slices.Contains(response.Provider.Chosen, "b") || slices.Contains(response.Provider.Chosen, "other-new") {
		t.Fatalf("refresh lost new matches or revived existing unchecked models: %+v", response)
	}
	if got := h.provider(id); !slices.Contains(got.Models, "new-matched") || slices.Contains(got.Models, "b") {
		t.Fatalf("rule additions did not reach saved picks: %v", got.Models)
	}
}

func TestWhaleBridgeCatalogHTTPReasonlessBackupAndRename(t *testing.T) {
	h := newProviderHTTP(t)
	id := h.save(map[string]any{"new": true, "id": "relay", "name": "Relay", "chat": h.vendor + "/v1", "key": "fixture-key", "models": []string{"test-model"}, "modelPrefs": map[string]any{"test-model": map[string]any{"reasonless": true}}})
	dto := supplierInfo(*h.provider(id))
	prefs := dto["modelPrefs"].(map[string]provider.ModelPref)
	if prefs["test-model"].Reasonless == nil || !*prefs["test-model"].Reasonless {
		t.Fatal("editor cannot reopen reasonless state")
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
	if err = models.Decode(&rows); err != nil {
		t.Fatal(err)
	}
	for _, m := range rows {
		if m.ID == id+"/test-model" && len(m.ReasoningEfforts) > 0 {
			t.Fatal("DSH still offers reasoning")
		}
	}
	b, err := backup.Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Settings == nil || !b.Settings.ModelReasonless[id+"/test-model"] {
		t.Fatal("backup lost model reasonless setting")
	}
	h.save(map[string]any{"from": id, "id": "renamed", "name": "Renamed"})
	if !provider.Reasonless("renamed", "test-model") || settings.Load().ModelReasonless[id+"/test-model"] {
		t.Fatal("rename lost or duplicated reasonless key")
	}
	h.call("POST", "provider/reset", map[string]any{"id": "renamed", "model": "test-model"}, 200)
	if provider.Reasonless("renamed", "test-model") {
		t.Fatal("reset kept reasonless switch")
	}
}

func TestWhaleBridgeCatalogHTTPRefreshNormalizedCount(t *testing.T) {
	h := newProviderHTTP(t)
	// Antigravity raw variant ids collapse to one model in the editor.
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"claude-sonnet-5-5"},{"id":"claude-sonnet-5-5-thinking"}]}`)
	}))
	t.Cleanup(vendor.Close)
	id := h.save(map[string]any{"new": true, "id": "relay", "name": "Relay", "chat": vendor.URL + "/v1", "key": "fixture-key"})
	var result struct {
		Count    int
		Provider struct {
			Available    []json.RawMessage
			CatalogCount int
		}
	}
	if err := json.Unmarshal(h.call("POST", "provider/models", map[string]any{"id": id}, 200), &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != len(h.provider(id).Available()) || result.Count != result.Provider.CatalogCount {
		t.Fatalf("refresh count does not match catalog: %+v", result)
	}
}

func TestWhaleBridgeCatalogLegacyReferencesBeforeSync(t *testing.T) {
	h := newProviderHTTP(t)
	t.Cleanup(plugin.Settle)
	componentContractWriteJSON(t, provider.Path(), map[string]any{"providers": []provider.Provider{{ID: "zed-plugin", Models: []string{"test-model"}, Contexts: map[string]int{"*": 128000}}}, "groups": []provider.Group{{ID: "legacy", Name: "Legacy", Members: []string{"zed-plugin/test-model"}}}})
	s := settings.Load()
	s.ModelNames = map[string]string{"zed-plugin/test-model": "Old name"}
	s.ModelReasonless = map[string]bool{"zed-plugin/test-model": true}
	s.PickedModels = map[string][]string{"dsh": {"zed-plugin/test-model"}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := migratePluginReferences(); err != nil {
		t.Fatal(err)
	}
	stored, err := provider.Stored()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].ID != "zed" || !slices.Contains(stored[0].Was, "zed-plugin") {
		t.Fatalf("provider references not migrated: %+v", stored)
	}
	s = settings.Load()
	if s.ModelNames["zed/test-model"] != "Old name" || !s.ModelReasonless["zed/test-model"] || !slices.Equal(s.PickedModels["dsh"], []string{"zed/test-model"}) {
		t.Fatal("model settings or DSH picks lost during migration")
	}
	if err := migratePluginReferences(); err != nil {
		t.Fatal(err)
	}
	for _, g := range provider.Groups() {
		if g.ID == "legacy" && !slices.Equal(g.Members, []string{"zed/test-model"}) {
			t.Fatal("routing group kept retired spelling")
		}
	}
	if _, err := os.Stat(filepath.Join(h.data, "config", "state.json")); err == nil {
		t.Fatal("migration must not start component service")
	}
}

func TestWhaleBridgeCatalogBackgroundChangeReachesDSH(t *testing.T) {
	h := newProviderHTTP(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: h.vendor + "/v1", Key: "fixture-key"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restore := watchDSHCatalog(ctx)
	defer restore()
	if err := catalog.SaveLive("relay", h.vendor+"/v1", []catalog.Model{{ID: "background-model", Name: "Background", Context: 128000}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(profilePath(os.Getenv("WHALEBRIDGE_DSH_HOME")))
		if strings.Contains(string(b), "relay/background-model") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("background catalog did not reach DSH: %s", dshSyncError())
}
