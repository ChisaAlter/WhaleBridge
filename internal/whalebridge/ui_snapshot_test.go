package whalebridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/edit"
	"github.com/ChisaAlter/WhaleBridge/internal/filememo"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

func seedUISnapshot(t *testing.T, h providerHTTP, providers, models int) string {
	t.Helper()
	st := settings.Load()
	st.NoAccountDiscovery = true
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAutoGroups(false); err != nil {
		t.Fatal(err)
	}
	for n := range providers {
		p := provider.Provider{ID: fmt.Sprintf("snapshot-%d", n), Name: fmt.Sprintf("Snapshot %d", n), Chat: h.vendor + "/v1", Key: "local-snapshot-key"}
		var live []catalog.Model
		for m := range models {
			id := fmt.Sprintf("snapshot-model-%03d", m)
			p.Models = append(p.Models, id)
			live = append(live, catalog.Model{ID: id, Name: "Native model " + id, Context: 131072, Output: 8192, Efforts: []string{"low", "high"}})
		}
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive(p.ID, p.Chat, live); err != nil {
			t.Fatal(err)
		}
	}
	provider.Changed()
	t.Cleanup(provider.Changed)
	return "snapshot-0/snapshot-model-000"
}

// Count real backend work rather than timing it: Node UI fixtures returning
// canned JSON cannot detect per-model settings parses or catalog rebuilds.
func TestUISnapshotManagementStateReusesAndReleasesCatalog(t *testing.T) {
	h := newProviderHTTP(t)
	seedUISnapshot(t, h, 3, 100)
	state := stateHandler("snapshot-test")
	var parses, builds int64
	handler := managementReads(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beforeParses, beforeBuilds := settings.Parses(), provider.AllBuilt.Load()
		state(w, r)
		for range 10 {
			_ = provider.All()
		}
		parses, builds = settings.Parses()-beforeParses, provider.AllBuilt.Load()-beforeBuilds
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("state: HTTP %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Providers []struct {
			Available []json.RawMessage `json:"available"`
		} `json:"providers"`
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Providers) != 3 || len(result.Models) != 300 {
		t.Fatalf("large real catalog was not served: providers=%d models=%d", len(result.Providers), len(result.Models))
	}
	for _, p := range result.Providers {
		if len(p.Available) != 100 {
			t.Fatalf("model metadata omitted instead of computed: %d", len(p.Available))
		}
	}
	t.Logf("300 model management GET: settings parses=%d provider builds=%d", parses, builds)
	if parses >= 100 || builds >= 10 {
		t.Fatalf("per-model rebuild returned: settings parses=%d provider builds=%d", parses, builds)
	}
	// The response must release both holds, not pin a process-wide cache.
	before := provider.AllBuilt.Load()
	_ = provider.All()
	_ = provider.All()
	if got := provider.AllBuilt.Load() - before; got != 2 {
		t.Fatalf("management GET leaked a catalog hold: subsequent builds=%d", got)
	}
}

func TestUISnapshotOnlyManagementGETIsHeld(t *testing.T) {
	h := newProviderHTTP(t)
	seedUISnapshot(t, h, 1, 1)
	for _, tc := range []struct {
		method, path string
		want         int64
	}{{http.MethodGet, "/api/keys/snapshot-0", 1}, {http.MethodPost, "/api/probe", 3}, {http.MethodHead, "/api/probe", 3}, {http.MethodGet, "/style.css", 3}, {http.MethodGet, "/v1/models", 3}} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			provider.Changed()
			var count int64
			handler := managementReads(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				before := provider.AllBuilt.Load()
				for range 3 {
					_ = provider.All()
				}
				count = provider.AllBuilt.Load() - before
			}))
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.path, nil))
			if count != tc.want {
				t.Fatalf("request scope builds=%d, want %d", count, tc.want)
			}
		})
	}
}

func TestUISnapshotNonGETInvalidatesOverlappingHeldSettings(t *testing.T) {
	h := newProviderHTTP(t)
	ref := seedUISnapshot(t, h, 1, 1)
	release := provider.Hold()
	defer release()
	files := filememo.Hold()
	defer files()
	_ = provider.HeldSettings()
	_ = provider.Catalog()
	const alias = "Alias saved by direct API handler"
	handler := managementReads(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := settings.Load()
		st.ModelNames = map[string]string{ref: alias}
		if err := settings.Save(st); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/direct-settings", nil))
	if got := provider.HeldSettings().ModelNames[ref]; got != alias {
		t.Fatalf("POST left overlapping GET's provider cache alive: %q", got)
	}
	if got := snapshotCatalogName(ref); got != alias {
		t.Fatalf("POST did not invalidate already built model entries: %q", got)
	}
}

func TestUISnapshotMutationInvalidatesBeforeDSHSync(t *testing.T) {
	h := newProviderHTTP(t)
	ref := seedUISnapshot(t, h, 1, 1)
	release := provider.Hold()
	defer release()
	_ = provider.HeldSettings()
	_ = provider.Catalog()
	const alias = "Fresh alias before synchronous DSH publication"
	// A plain settings save clears filememo but does not call catalog.Touched.
	// This isolates mutate's pre-sync invalidation from the model setter's.
	handler := mutate(func(w http.ResponseWriter, r *http.Request) error {
		st := settings.Load()
		st.ModelNames = map[string]string{ref: alias}
		return settings.Save(st)
	})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/settings-fixture", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("mutation: HTTP %d: %s", w.Code, w.Body.String())
	}
	if got := snapshotCatalogName(ref); got != alias {
		t.Fatalf("model alias was still stale after mutation: %q", got)
	}
	if got := snapshotDSHName(t, ref); !strings.Contains(got, alias) {
		t.Fatalf("response succeeded before new alias reached DSH: %q", got)
	}
}

func TestUISnapshotGlobalSettingsInvalidateBeforeReturn(t *testing.T) {
	h := newProviderHTTP(t)
	seedUISnapshot(t, h, 1, 1)
	release := provider.Hold()
	defer release()
	before := provider.HeldSettings()
	value := !before.ChineseUnits
	body, _ := json.Marshal(map[string]bool{"chineseUnits": value})
	if err := saveGlobalSettings(httptest.NewRequest(http.MethodPost, "/api/global/settings", bytes.NewReader(body))); err != nil {
		t.Fatal(err)
	}
	if got := provider.HeldSettings().ChineseUnits; got != value {
		t.Fatalf("global settings save did not invalidate existing held view: got %v want %v", got, value)
	}
}

func TestUISnapshotActualModelAliasHTTPPublishesDSH(t *testing.T) {
	h := newProviderHTTP(t)
	ref := seedUISnapshot(t, h, 1, 1)
	release := provider.Hold()
	defer release()
	_ = provider.Catalog()
	const alias = "HTTP model alias in current native catalog"
	h.call(http.MethodPost, "provider/name", map[string]any{"id": "snapshot-0", "model": "snapshot-model-000", "modelName": alias}, http.StatusOK)
	if got := snapshotCatalogName(ref); got != alias {
		t.Fatalf("model alias API kept old entry: %q", got)
	}
	if got := snapshotDSHName(t, ref); !strings.Contains(got, alias) {
		t.Fatalf("model alias API did not synchronously publish DSH: %q", got)
	}
}

// Connect is a POST with its own planning phase, so the management GET
// wrapper does not provide its hold. Exercise the real action and Codex files.
func TestUISnapshotCodexConnectReusesAndReleasesCatalog(t *testing.T) {
	var smallParses, largeParses int64
	for _, perProvider := range []int{1, 100} {
		t.Run(fmt.Sprintf("%d-models", 3*perProvider), func(t *testing.T) {
			h := newProviderHTTP(t)
			ref := seedUISnapshot(t, h, 3, perProvider)
			config := filepath.Join(os.Getenv("HOME"), ".codex", "config.toml")
			if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, []byte("model = \"native-before-connect\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			parsesBefore, buildsBefore := settings.Parses(), provider.AllBuilt.Load()
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/agent/connect", strings.NewReader(`{"id":"codex"}`))
			r.Header.Set("Content-Type", "application/json")
			agentAction("connect")(w, r)
			parses, builds := settings.Parses()-parsesBefore, provider.AllBuilt.Load()-buildsBefore
			if w.Code != http.StatusOK {
				t.Fatalf("real Codex connect: HTTP %d: %s", w.Code, w.Body.String())
			}
			if model, _ := edit.GetTOMLTop(config, "model"); model != ref {
				t.Fatalf("Codex did not select the first served model: %q", model)
			}
			if source, _ := edit.GetTOMLTop(config, "model_provider"); source != "magpie" {
				t.Fatalf("Codex provider was not connected: %q", source)
			}
			ids, err := managedAgents()
			if err != nil || !slices.Contains(ids, "codex") {
				t.Fatalf("successful connect did not persist component ownership: %v (%v)", ids, err)
			}
			data, err := os.ReadFile(filepath.Join(filepath.Dir(config), "magpie-models.json"))
			if err != nil {
				t.Fatal(err)
			}
			var list struct {
				Models []json.RawMessage `json:"models"`
			}
			if err := json.Unmarshal(data, &list); err != nil || len(list.Models) != 3*perProvider {
				t.Fatalf("Codex catalog omitted served models: count=%d want=%d err=%v", len(list.Models), 3*perProvider, err)
			}
			t.Logf("%d model Codex connect: settings parses=%d provider builds=%d", 3*perProvider, parses, builds)
			if parses >= 300 || builds >= 10 {
				t.Fatalf("Codex connect rebuilt per model: settings parses=%d provider builds=%d", parses, builds)
			}
			if perProvider == 1 {
				smallParses = parses
			} else {
				largeParses = parses
			}
			assertUISnapshotHoldReleased(t, "Codex connect")
		})
	}
	// Agent discovery and native-account decisions have fixed settings reads.
	// Growing the catalog 100-fold must not add one parse for each model.
	if largeParses > smallParses+25 {
		t.Fatalf("settings parses scaled with model count: 3 models=%d, 300 models=%d", smallParses, largeParses)
	}
}

func TestUISnapshotDirectDSHSyncReusesAndReleasesCatalog(t *testing.T) {
	h := newProviderHTTP(t)
	ref := seedUISnapshot(t, h, 3, 100)
	parsesBefore, buildsBefore := settings.Parses(), provider.AllBuilt.Load()
	if err := syncDSH(); err != nil {
		t.Fatal(err)
	}
	parses, builds := settings.Parses()-parsesBefore, provider.AllBuilt.Load()-buildsBefore
	doc, err := profileDocument(os.Getenv("WHALEBRIDGE_DSH_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := member(configRow(doc, "llm-pi-ai"), "config")
	models := member(member(member(cfg, "providers"), "whalebridge"), "models")
	if models == nil || len(models.Content) != 300 {
		t.Fatal("direct DSH sync omitted the large catalog")
	}
	if name := snapshotDSHName(t, ref); name == "" {
		t.Fatal("direct DSH sync did not publish the first model")
	}
	t.Logf("300 model direct DSH sync: settings parses=%d provider builds=%d", parses, builds)
	if parses >= 50 || builds >= 5 {
		t.Fatalf("direct DSH sync rebuilt per model: settings parses=%d provider builds=%d", parses, builds)
	}
	assertUISnapshotHoldReleased(t, "direct DSH sync")
}

func TestUISnapshotManagedAgentsSyncReusesAndReleasesCatalog(t *testing.T) {
	h := newProviderHTTP(t)
	ref := seedUISnapshot(t, h, 3, 100)
	config := filepath.Join(os.Getenv("HOME"), ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("model = \"native-before-connect\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/agent/connect", strings.NewReader(`{"id":"codex"}`))
	r.Header.Set("Content-Type", "application/json")
	agentAction("connect")(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("prepare managed Codex: HTTP %d: %s", w.Code, w.Body.String())
	}
	path := filepath.Join(filepath.Dir(config), "magpie-models.json")
	if _, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	// Require this direct sync to republish the model list, rather than pass
	// by inspecting an unchanged catalog left by connect's own held phase.
	if err := os.WriteFile(path, []byte(`{"models":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	provider.Changed()
	parsesBefore, buildsBefore := settings.Parses(), provider.AllBuilt.Load()
	syncManagedAgents()
	parses, builds := settings.Parses()-parsesBefore, provider.AllBuilt.Load()-buildsBefore
	if agentSyncErrors["*"] != "" || agentSyncErrors["codex"] != "" {
		t.Fatalf("managed Codex sync failed: global=%q codex=%q", agentSyncErrors["*"], agentSyncErrors["codex"])
	}
	ids, err := managedAgents()
	if err != nil || !slices.Contains(ids, "codex") {
		t.Fatalf("sync lost component ownership: %v (%v)", ids, err)
	}
	if model, _ := edit.GetTOMLTop(config, "model"); model != ref {
		t.Fatalf("sync changed the connected model: %q", model)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &list); err != nil || len(list.Models) != 300 {
		t.Fatalf("managed sync did not republish 300 models: count=%d err=%v", len(list.Models), err)
	}
	seen := map[string]bool{}
	for _, m := range list.Models {
		seen[m.Slug] = true
	}
	for p := range 3 {
		for m := range 100 {
			if id := fmt.Sprintf("snapshot-%d/snapshot-model-%03d", p, m); !seen[id] {
				t.Fatalf("managed sync lost model %q", id)
			}
		}
	}
	t.Logf("300 model managed Agent sync: settings parses=%d provider builds=%d", parses, builds)
	// Native-account checks and atomic file writes have fixed settings work;
	// the model catalog itself must remain shared throughout this sync.
	if parses >= 500 || builds >= 10 {
		t.Fatalf("managed sync rebuilt per model: settings parses=%d provider builds=%d", parses, builds)
	}
	assertUISnapshotHoldReleased(t, "managed Agent sync")
}

func assertUISnapshotHoldReleased(t *testing.T, operation string) {
	t.Helper()
	before := provider.AllBuilt.Load()
	_ = provider.All()
	_ = provider.All()
	if got := provider.AllBuilt.Load() - before; got != 2 {
		t.Fatalf("%s leaked a catalog hold: subsequent builds=%d", operation, got)
	}
}

func snapshotCatalogName(ref string) string {
	for _, entry := range provider.Catalog() {
		if entry.ID == ref {
			return entry.Name
		}
	}
	return ""
}

func snapshotDSHName(t *testing.T, ref string) string {
	t.Helper()
	doc, err := profileDocument(os.Getenv("WHALEBRIDGE_DSH_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := member(configRow(doc, "llm-pi-ai"), "config")
	route := member(member(cfg, "providers"), "whalebridge")
	models := member(route, "models")
	if models != nil {
		for _, model := range models.Content {
			if id := member(model, "id"); id != nil && id.Value == ref {
				if name := member(model, "name"); name != nil {
					return name.Value
				}
			}
		}
	}
	t.Fatalf("DSH route has no model %q", ref)
	return ""
}
