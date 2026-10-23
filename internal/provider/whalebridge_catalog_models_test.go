package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// All catalog tests use the package's offline, component-owned home. Plugin
// fixtures are cached data only: no host, public plugin or real sign-in runs.
func whaleCatalogHome(t *testing.T) {
	t.Helper()
	isolate(t)
	t.Setenv("MAGPIE_BUN", "")
	writeFile(t, catalog.CachePath(), map[string]any{"fixture": map[string]any{"models": map[string]any{}}})
	catalog.Reset()
	plugin.UseCached(nil)
	t.Cleanup(func() {
		plugin.UseCached(nil)
		catalog.Reset()
		Changed()
	})
}

func whaleCatalogPlugin(t *testing.T, pp plugin.Provider, auth map[string]any) {
	t.Helper()
	writeFile(t, filepath.Join(settings.Dir(), "plugins.json"), map[string]any{"plugins": []map[string]any{{"spec": pp.Spec}}})
	writeFile(t, filepath.Join(settings.Dir(), "plugin-auth.json"), auth)
	plugin.UseCached([]plugin.Provider{pp})
	Changed()
}

func whaleCatalogProvider(t *testing.T, id string) Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

// A real /models response drives API filtering, including the automatic
// first-24 slice and picks saved by Select all. The Claude entries remain in
// Available, and the user's API override can make one usable again.
func TestWhaleBridgeCatalogNoAPIAndNewlyListed(t *testing.T) {
	whaleCatalogHome(t)
	var newer atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		data := []map[string]any{{"id": "claude-only", "supported_endpoints": []string{"/v1/messages"}}}
		for i := range manyModels + 3 {
			data = append(data, map[string]any{"id": fmt.Sprintf("chat-%02d", i), "supported_endpoints": []string{"/v1/chat/completions", "/v1/responses"}})
		}
		data = append(data, map[string]any{"id": "api-unknown"})
		if newer.Load() {
			data = append(data, map[string]any{"id": "new-chat", "supported_endpoints": []string{"/v1/chat/completions"}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(server.Close)
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "synthetic", Chat: server.URL + "/v1", Responses: server.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	p := whaleCatalogProvider(t, "relay")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p = whaleCatalogProvider(t, "relay")
	if got := len(p.Available()); got != manyModels+5 {
		t.Fatalf("available %d, want the complete list", got)
	}
	if got := modelIDs(p.Exposed()); len(got) != manyModels || got[0] != "chat-00" || slices.Contains(got, "claude-only") {
		t.Fatalf("default slice %v", got)
	}
	if !p.NoAPIFor("claude-only") || p.NoAPIFor("api-unknown") {
		t.Fatal("listed and unknown APIs were not distinguished")
	}
	// Select all can pick every usable model while leaving Claude unticked.
	for _, m := range p.Available() {
		if !p.NoAPIFor(m.ID) {
			p.Models = append(p.Models, m.ID)
		}
	}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	newer.Store(true)
	p = whaleCatalogProvider(t, "relay")
	if _, _, err := p.Refetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p = whaleCatalogProvider(t, "relay")
	if got := modelIDs(p.Exposed()); !slices.Contains(got, "new-chat") || slices.Contains(got, "claude-only") {
		t.Fatalf("a complete usable selection did not take the new model: %v", got)
	}
	if !slices.Contains(p.Picks(), "new-chat") {
		t.Fatal("the editor would save without the new model")
	}
	// A previously picked, unavailable API stays saved, but is not served.
	p.Models = append(p.Models, "claude-only", "typed-by-hand")
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	p = whaleCatalogProvider(t, "relay")
	if got := modelIDs(p.Exposed()); slices.Contains(got, "claude-only") || !slices.Contains(got, "typed-by-hand") {
		t.Fatalf("picked APIs %v", got)
	}
	if err := SetModelAPI("relay/claude-only", string(Responses)); err != nil {
		t.Fatal(err)
	}
	p = whaleCatalogProvider(t, "relay")
	if p.NoAPIFor("claude-only") || !slices.Contains(modelIDs(p.Exposed()), "claude-only") {
		t.Fatal("the user's API did not override the list")
	}
	if (Provider{Account: &Account{}}).NoAPIFor("anything") {
		t.Fatal("a signed-in provider was filtered")
	}
}

// Lists may alternate their models. Restoring A in the same refresh that
// drops B must not erase A, and saving the shorter list must remember models
// the user saw and left unpicked before it shrank.
func TestWhaleBridgeCatalogDelistedRoundTripAndSwap(t *testing.T) {
	whaleCatalogHome(t)
	var stage atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		ids := [][]string{{"a", "b", "c", "unpicked"}, {"a", "c"}, {"b", "c", "unpicked"}}[stage.Load()]
		var data []map[string]string
		for _, id := range ids {
			data = append(data, map[string]string{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(server.Close)
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "synthetic", Chat: server.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	fetch := func() []string {
		t.Helper()
		p := whaleCatalogProvider(t, "relay")
		_, gone, err := p.Refetch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return gone
	}
	fetch()
	p := whaleCatalogProvider(t, "relay")
	p.Models = []string{"a", "b"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	stage.Store(1)
	if gone := fetch(); !slices.Equal(gone, []string{"b"}) {
		t.Fatalf("gone %v", gone)
	}
	p = whaleCatalogProvider(t, "relay")
	if !slices.Equal(p.Models, []string{"a"}) || !slices.Equal(p.Delisted, []string{"b"}) {
		t.Fatalf("short list: picks %v, aside %v", p.Models, p.Delisted)
	}
	// What an editor sends after selecting c, with no delisted field.
	p.Models, p.Delisted = []string{"a", "c"}, nil
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	stage.Store(2)
	if gone := fetch(); !slices.Equal(gone, []string{"a"}) {
		t.Fatalf("swap dropped %v", gone)
	}
	p = whaleCatalogProvider(t, "relay")
	if !slices.Equal(p.Models, []string{"c", "b"}) || !slices.Equal(p.Delisted, []string{"a"}) {
		t.Fatalf("same refresh lost a restored pick: %v, aside %v", p.Models, p.Delisted)
	}
	stage.Store(0)
	fetch()
	p = whaleCatalogProvider(t, "relay")
	if !slices.Equal(p.Models, []string{"c", "b", "a"}) || len(p.Delisted) != 0 {
		t.Fatalf("restored %v, aside %v", p.Models, p.Delisted)
	}
	if slices.Contains(modelIDs(p.Exposed()), "unpicked") {
		t.Fatal("a model seen and left unpicked was treated as new")
	}
}

func TestWhaleBridgeCatalogEveryPickDelistedAndEmptyLists(t *testing.T) {
	whaleCatalogHome(t)
	p := Provider{ID: "relay", Name: "Relay", Key: "synthetic", Chat: "http://127.0.0.1:1/v1", Models: []string{"b"}}
	if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{{ID: "a"}, {ID: "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := dropGonePicks(p.ID, map[string]bool{"a": true, "b": true}, map[string]bool{"a": true}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{{ID: "a"}}); err != nil {
		t.Fatal(err)
	}
	p = whaleCatalogProvider(t, p.ID)
	if len(p.Exposed()) != 0 || !p.waitsForPicks() {
		t.Fatalf("all picks aside exposed %v", modelIDs(p.Exposed()))
	}
	if _, _, ok := Resolve("relay/b"); ok {
		t.Fatal("a delisted explicit model still resolved")
	}
	if _, _, ok := Resolve("a"); ok {
		t.Fatal("the default list was served as a bare model")
	}
	if _, _, ok := Resolve("relay/hand-typed"); !ok {
		t.Fatal("an unknown hand-typed id was incorrectly rejected")
	}
	// Unrelated edits with no delisted field keep waiting.
	p.Delisted, p.MaxRPM = nil, 10
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	p = whaleCatalogProvider(t, p.ID)
	if len(p.Exposed()) != 0 || !slices.Equal(p.Delisted, []string{"b"}) || !slices.Equal(p.PickedFrom, []string{"a", "b"}) {
		t.Fatalf("editor lost set-aside picks: aside %v, picked-from %v", p.Delisted, p.PickedFrom)
	}
	before := []plugin.Provider{{ID: "relay", Models: []plugin.Model{{ID: "a"}, {ID: "b"}}}}
	for _, now := range [][]plugin.Provider{{{ID: "relay"}}, {{ID: "relay", FellBack: true, Models: []plugin.Model{{ID: "a"}}}}} {
		if got := dropGonePluginPicks(before, now); len(got) != 0 {
			t.Fatalf("an empty or failed list dropped %v", got)
		}
		if got := keptListed(before, now); len(got[0].Models) != 2 {
			t.Fatalf("an empty or failed list replaced the previous one: %v", got)
		}
	}
	// Select none clears pending picks. An explicit empty Delisted also
	// permits the editor's Vendor's list mode to clear them.
	p.NoModels = true
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	p = whaleCatalogProvider(t, p.ID)
	if len(p.Delisted) != 0 || !p.ServesNone() {
		t.Fatal("Select none kept pending picks")
	}
	p.NoModels, p.Delisted = false, []string{"b"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	p = whaleCatalogProvider(t, p.ID)
	p.Delisted = []string{}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	p = whaleCatalogProvider(t, p.ID)
	if len(p.Delisted) != 0 || len(p.Exposed()) != 1 {
		t.Fatal("Vendor's list did not clear pending picks")
	}
}

// A cache may be absent at startup while set-aside picks still live in
// providers.json. The first successful list restores them without needing
// an old live list against which to compare.
func TestWhaleBridgeCatalogFirstFetchRestoresDelisted(t *testing.T) {
	whaleCatalogHome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "back"}, {"id": "other"}}})
	}))
	t.Cleanup(server.Close)
	if err := Save(Provider{ID: "first", Name: "First", Key: "synthetic", Chat: server.URL + "/v1", Delisted: []string{"back"}}); err != nil {
		t.Fatal(err)
	}
	p := whaleCatalogProvider(t, "first")
	if len(liveIDs(p.ID)) != 0 || len(p.Exposed()) != 0 {
		t.Fatal("fixture must begin with pending picks and no live cache")
	}
	if _, gone, err := p.Refetch(context.Background()); err != nil || len(gone) != 0 {
		t.Fatalf("first fetch: dropped %v, err %v", gone, err)
	}
	p = whaleCatalogProvider(t, "first")
	if !slices.Equal(p.Models, []string{"back"}) || len(p.Delisted) != 0 {
		t.Fatalf("first fetch lost pending selection: picks %v, aside %v", p.Models, p.Delisted)
	}
	// A plugin can also have no trustworthy prior list.
	f := load()
	f.Providers = append(f.Providers, Provider{ID: "first-plugin", Delisted: []string{"back"}})
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	if gone := dropGonePluginPicks(nil, []plugin.Provider{{ID: "first-plugin", Models: whaleCatalogModels("back", "other")}}); len(gone) != 0 {
		t.Fatalf("first plugin list dropped %v", gone)
	}
	f = load()
	p, ok := find(f.Providers, "first-plugin")
	if !ok || !slices.Equal(p.Models, []string{"back"}) || len(p.Delisted) != 0 {
		t.Fatalf("first plugin list did not restore: %+v", p)
	}
}

func TestWhaleBridgeCatalogWindowsAndRetiredLimits(t *testing.T) {
	whaleCatalogHome(t)
	writeFile(t, catalog.CachePath(), map[string]any{"other": map[string]any{"models": map[string]any{
		"known": map[string]any{"id": "known", "name": "Known", "limit": map[string]int{"context": 200000, "output": 32000}},
	}}})
	catalog.Reset()
	p := Provider{ID: "relay", Name: "Relay", Key: "synthetic", Chat: "http://127.0.0.1:1/v1", Models: []string{"known", "retired"}, Contexts: map[string]int{"*": 1000000}}
	for _, c := range []struct {
		m    catalog.Model
		want int
	}{
		{catalog.Model{ID: "known"}, 1000000},
		{catalog.Model{ID: "known", Context: 262144}, 262144},
		{catalog.Model{ID: "known", Context: 272000, MaxContext: 872000}, 872000},
		{catalog.Model{ID: "known", Context: 512}, 1000000},
	} {
		if got := p.WindowOf(c.m); got != c.want {
			t.Errorf("%+v: window %d, want %d", c.m, got, c.want)
		}
	}
	p.Contexts["known"] = 1200000
	if got := p.WindowOf(catalog.Model{ID: "known", Context: 262144}); got != 1200000 {
		t.Fatalf("per-model window %d", got)
	}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if err := SetModelOutputs(p.ID, map[string]int{"retired": 65536}); err != nil {
		t.Fatal(err)
	}
	if err := SetModelCompacts(p.ID, map[string]int{"retired": 200000}); err != nil {
		t.Fatal(err)
	}
	p.Models = []string{"known"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		set  func(string, map[string]int) error
		of   func(string) map[string]int
	}{
		{"output", SetModelOutputs, OutputsOf},
		{"compact", SetModelCompacts, CompactsOf},
	} {
		t.Run(c.name, func(t *testing.T) {
			values := c.of(p.ID)
			old := values["retired"]
			values["known"] = 32000
			if err := c.set(p.ID, values); err != nil {
				t.Fatalf("unchanged retired value blocked save: %v", err)
			}
			if !maps.Equal(c.of(p.ID), map[string]int{"retired": old, "known": 32000}) {
				t.Fatalf("saved %v", c.of(p.ID))
			}
			// Sending exactly those values back is a true no-op: no file
			// replacement and no model-file rewrite notification.
			info, err := os.Stat(settings.Path())
			if err != nil {
				t.Fatal(err)
			}
			var touched atomic.Int32
			oldChanged := catalog.Changed
			catalog.Changed = func() { touched.Add(1) }
			err = c.set(p.ID, values)
			catalog.Changed = oldChanged
			after, statErr := os.Stat(settings.Path())
			if err != nil || statErr != nil || !after.ModTime().Equal(info.ModTime()) || touched.Load() != 0 {
				t.Fatalf("unchanged save was not a no-op: err=%v stat=%v notifications=%d", err, statErr, touched.Load())
			}
			for _, invalid := range []map[string]int{{"retired": old + 1}, {"retired": old, "typo": 5}} {
				if err := c.set(p.ID, invalid); err == nil {
					t.Fatalf("accepted new or changed unknown limit: %v", invalid)
				}
				if !maps.Equal(c.of(p.ID), values) {
					t.Fatal("a refused save changed stored limits")
				}
			}
			if err := c.set(p.ID, map[string]int{"known": 32000}); err != nil {
				t.Fatal(err)
			}
			if _, ok := c.of(p.ID)["retired"]; ok {
				t.Fatal("omitting a retired value did not clear it")
			}
		})
	}
}
