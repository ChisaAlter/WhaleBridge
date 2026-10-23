package provider

import (
	"context"
	"maps"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
)

func whaleCatalogModels(ids ...string) []plugin.Model {
	var models []plugin.Model
	for _, id := range ids {
		models = append(models, plugin.Model{ID: id, Name: id, NPM: "@ai-sdk/openai-compatible"})
	}
	return models
}

// The public provider catalog is the union of all enabled accounts, in
// catalog order. Routing remains limited by each individual account's list.
func TestWhaleBridgeCatalogPluginOnAccountsUnionAndTiers(t *testing.T) {
	whaleCatalogHome(t)
	pp := plugin.Provider{ID: "catalog-acme", Name: "Catalog Acme", Spec: "@fixture/catalog-acme",
		Models: whaleCatalogModels("a", "b", "c", "messages"),
		Accounts: []plugin.Account{
			{Key: "catalog-acme", Models: []string{"a"}},
			{Key: "catalog-acme#2", Models: []string{"b", "a"}},
			{Key: "catalog-acme#3", Models: []string{"c"}},
		},
	}
	pp.Models[0].ServiceTiers = []string{"priority", "ultrafast"}
	pp.Models[1].NPM, pp.Models[1].ServiceTiers = "@ai-sdk/openai", []string{"ultrafast"}
	pp.Models[3].NPM, pp.Models[3].ServiceTiers = "@ai-sdk/anthropic", []string{"priority"}
	whaleCatalogPlugin(t, pp, map[string]any{
		"catalog-acme":   map[string]any{"type": "api", "key": "synthetic-one"},
		"catalog-acme#2": map[string]any{"type": "api", "key": "synthetic-two"},
		"catalog-acme#3": map[string]any{"type": "api", "key": "synthetic-three"},
	})
	saveLogins(t,
		savedLogin{Agent: "plugin:catalog-acme", User: "one", Home: "catalog-acme", On: true},
		savedLogin{Agent: "plugin:catalog-acme", User: "two", Home: "catalog-acme#2", On: true},
		savedLogin{Agent: "plugin:catalog-acme", User: "three", Home: "catalog-acme#3"},
	)
	p := whaleCatalogProvider(t, pp.ID)
	if got := modelIDs(p.Available()); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("enabled union %v", got)
	}
	if !p.Account.Lists("a") || p.Account.Lists("b") {
		t.Fatal("the first account routes past its own models")
	}
	current, ok := PluginOf(pp.ID)
	if !ok {
		t.Fatal("fixture provider was not cached")
	}
	per := map[string][]string{}
	for _, l := range pluginLogins(current) {
		account := pluginProvider(current, l)
		per[l.acct.Key] = modelIDs(account.Available())
		tiers, known := account.Account.Tiers("a")
		if l.acct.Key == "catalog-acme#3" {
			if !known || len(tiers) != 0 {
				t.Fatal("an account lacking the model claimed its tiers")
			}
		} else if !known || !slices.Equal(tiers, []string{"priority", "ultrafast"}) {
			t.Fatalf("%s account tiers %v (%v)", l.acct.Key, tiers, known)
		}
		if _, known := account.Account.Tiers("c"); l.acct.Key == "catalog-acme#3" && known {
			t.Fatal("an undeclared service tier became known")
		}
	}
	if !slices.Equal(per["catalog-acme"], []string{"a"}) || !slices.Equal(per["catalog-acme#2"], []string{"a", "b"}) || !slices.Equal(per["catalog-acme#3"], []string{"c"}) {
		t.Fatalf("per-account catalogs %v", per)
	}
	if got := PluginTiers(p, "a"); !slices.Equal(got, []string{"priority", "ultrafast"}) {
		t.Fatalf("chat tiers %v", got)
	}
	if got := PluginTiers(p, "messages"); len(got) != 0 || CanFast(p, "messages") {
		t.Fatalf("Messages was offered service_tier fast: %v", got)
	}
	if !CanFast(p, "a") || CanFast(p, "b") {
		t.Fatal("Fast did not require the priority tier")
	}
	for _, e := range Served() {
		if e.Provider.ID != pp.ID {
			continue
		}
		m := catalog.Model{}
		CodexTiers(&m, e, func(string) []Member { return nil })
		want := pp.Models[slices.IndexFunc(pp.Models, func(m plugin.Model) bool { return m.ID == e.Model })].ServiceTiers
		if !slices.Equal(m.Tiers, want) {
			t.Errorf("%s Codex tiers %v, want %v", e.ID, m.Tiers, want)
		}
	}
	// nil is unknown (all models), while [] is an explicitly empty list.
	for _, c := range []struct {
		models []string
		want   []string
	}{
		{nil, []string{"a", "b", "c", "messages"}},
		{[]string{}, []string{"a"}},
	} {
		pp.Accounts[1].Models = c.models
		plugin.UseCached([]plugin.Provider{pp})
		Changed()
		p = whaleCatalogProvider(t, pp.ID)
		if got := modelIDs(p.Available()); !slices.Equal(got, c.want) {
			t.Fatalf("second account %v: union %v, want %v", c.models, got, c.want)
		}
	}
}

// The real relist loop retries soon after shortening a selected list and
// restores its picks. A cached fake replaces the plugin host completely.
func TestWhaleBridgeCatalogPluginRelistRestoresSoon(t *testing.T) {
	whaleCatalogHome(t)
	full := plugin.Provider{ID: "catalog-pool", Name: "Catalog Pool", Spec: "@fixture/catalog-pool", Models: whaleCatalogModels("pool", "channel/a", "channel/unpicked")}
	whaleCatalogPlugin(t, full, map[string]any{"catalog-pool": map[string]any{"type": "api", "key": "managed"}})
	if err := Save(Provider{ID: full.ID, Models: []string{"channel/a"}}); err != nil {
		t.Fatal(err)
	}
	answers, asked := make(chan []plugin.Provider), make(chan struct{})
	oldList, oldFirst, oldEvery, oldSoon := relistPlugins, relistFirst, relistEvery, relistSoon
	oldChanged := catalog.Changed
	notified := make(chan struct{}, 16)
	catalog.Changed = func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	}
	relistPlugins = func(ctx context.Context) ([]plugin.Provider, error) {
		select {
		case asked <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case ps := <-answers:
			plugin.UseCached(ps)
			Changed()
			return ps, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	relistFirst, relistSoon, relistEvery = time.Millisecond, time.Millisecond, time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done
		relistPlugins, relistFirst, relistEvery, relistSoon = oldList, oldFirst, oldEvery, oldSoon
		catalog.Changed = oldChanged
	})
	go func() { defer close(done); KeepPluginsListed(ctx) }()
	wait := func(ch <-chan struct{}, message string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatal(message)
		}
	}
	wait(asked, "initial relist did not run")
	short := full
	short.Models = whaleCatalogModels("pool")
	answers <- []plugin.Provider{short}
	wait(asked, "a shortened selection was not retried soon")
	p := whaleCatalogProvider(t, full.ID)
	if len(p.Exposed()) != 0 || !slices.Equal(p.Delisted, []string{"channel/a"}) {
		t.Fatalf("short list: picks %v, aside %v, exposed %v", p.Models, p.Delisted, modelIDs(p.Exposed()))
	}
	// Keep the previous PickedFrom when an editor saves a new selection.
	p.Models, p.Delisted = []string{"pool"}, nil
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	for len(notified) > 0 {
		<-notified
	}
	answers <- []plugin.Provider{full}
	wait(notified, "restored picks did not notify the catalog watcher")
	p = whaleCatalogProvider(t, full.ID)
	if !slices.Equal(p.Models, []string{"pool", "channel/a"}) || len(p.Delisted) != 0 || slices.Contains(modelIDs(p.Exposed()), "channel/unpicked") {
		t.Fatalf("full list again: picks %v, aside %v, exposed %v", p.Models, p.Delisted, modelIDs(p.Exposed()))
	}
}

// Even with no picked-model record to write, a background plugin model
// change must notify catalog consumers that maintain DSH's model files.
func TestWhaleBridgeCatalogPluginRelistNotifiesWithoutPicks(t *testing.T) {
	whaleCatalogHome(t)
	pp := plugin.Provider{ID: "catalog-watch", Name: "Catalog Watch", Spec: "@fixture/catalog-watch", Models: whaleCatalogModels("a")}
	whaleCatalogPlugin(t, pp, map[string]any{"catalog-watch": map[string]any{"type": "api", "key": "synthetic"}})
	_ = whaleCatalogProvider(t, pp.ID)
	oldList, oldFirst, oldEvery := relistPlugins, relistFirst, relistEvery
	oldChanged := catalog.Changed
	var calls atomic.Int32
	notified := make(chan struct{}, 1)
	catalog.Changed = func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	}
	relistPlugins = func(context.Context) ([]plugin.Provider, error) {
		calls.Add(1)
		pp.Models = whaleCatalogModels("a", "new")
		plugin.UseCached([]plugin.Provider{pp})
		Changed()
		return []plugin.Provider{pp}, nil
	}
	relistFirst, relistEvery = time.Millisecond, time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done
		relistPlugins, relistFirst, relistEvery = oldList, oldFirst, oldEvery
		catalog.Changed = oldChanged
	})
	go func() { defer close(done); KeepPluginsListed(ctx) }()
	select {
	case <-notified:
	case <-time.After(3 * time.Second):
		t.Fatal("a plugin model change with no picks never notified catalog.Changed")
	}
	if calls.Load() != 1 || len(load().Providers) != 0 {
		t.Fatal("the test required a relist without any provider record saved")
	}
}

func TestWhaleBridgeCatalogLegacyPluginAliasesAndRecords(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(map[bool]string{false: "old-only", true: "canonical-wins"}[collision], func(t *testing.T) {
			whaleCatalogHome(t)
			pp := plugin.Provider{ID: "zed", Spec: "@fixture/catalog-zed", Models: whaleCatalogModels("legacy", "canonical")}
			whaleCatalogPlugin(t, pp, map[string]any{"zed": map[string]any{"type": "api", "key": "synthetic"}})
			legacy := Provider{ID: "zed-plugin", Models: []string{"legacy"}, Delisted: []string{"pending"}, Contexts: map[string]int{"*": 42000}}
			f := file{Providers: []Provider{legacy}, Order: []string{"zed-plugin", "unrelated", "zed"}}
			if collision {
				f.Providers = append(f.Providers, Provider{ID: "zed", Models: []string{"canonical"}, Contexts: map[string]int{"*": 84000}})
			}
			if err := store(f); err != nil {
				t.Fatal(err)
			}
			want := "legacy"
			if collision {
				want = "canonical"
			}
			p := whaleCatalogProvider(t, "zed-plugin")
			if p.ID != "zed" || !slices.Equal(p.Models, []string{want}) || !slices.Contains(p.Was, "zed-plugin") {
				t.Fatalf("read compatibility %+v", p)
			}
			if p.Account.moved {
				t.Fatal("retirement was confused with an actual credential move")
			}
			if resolved, m, ok := Resolve("zed-plugin/" + want); !ok || resolved.ID != "zed" || m != want {
				t.Fatalf("old reference resolved %s/%s (%v)", resolved.ID, m, ok)
			}
			if err := MigrateLegacyPluginRecords(); err != nil {
				t.Fatal(err)
			}
			f = load()
			if len(f.Providers) != 1 || f.Providers[0].ID != "zed" || !slices.Equal(f.Providers[0].Models, []string{want}) || !slices.Contains(f.Providers[0].Was, "zed-plugin") || !slices.Equal(f.Order, []string{"zed", "unrelated"}) {
				t.Fatalf("migrated %+v, order %v", f.Providers, f.Order)
			}
			before, err := os.ReadFile(Path())
			if err != nil {
				t.Fatal(err)
			}
			if err := MigrateLegacyPluginRecords(); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(Path())
			if !slices.Equal(before, after) {
				t.Fatal("an idempotent migration rewrote the provider file")
			}
		})
	}
}

func TestWhaleBridgeCatalogLegacyAliasesRespectCustomProviders(t *testing.T) {
	for _, customID := range []string{"zed", "zed-plugin"} {
		t.Run(customID, func(t *testing.T) {
			whaleCatalogHome(t)
			pp := plugin.Provider{ID: "zed", Spec: "@fixture/catalog-zed", Models: whaleCatalogModels("plugin-model")}
			whaleCatalogPlugin(t, pp, map[string]any{"zed": map[string]any{"type": "api", "key": "synthetic"}})
			custom := Provider{ID: customID, Name: "My custom", Key: "synthetic", Chat: "http://127.0.0.1:1/v1", Models: []string{"custom-model"}}
			if err := store(file{Providers: []Provider{custom}, Order: []string{customID}}); err != nil {
				t.Fatal(err)
			}
			if _, ok := LegacyPluginAliases()["zed-plugin"]; ok || CanonicalProviderID("zed-plugin") != "zed-plugin" {
				t.Fatal("a custom provider did not block the alias")
			}
			p := whaleCatalogProvider(t, customID)
			if p.Account != nil || p.Name != "My custom" {
				t.Fatal("an alias stole a custom provider")
			}
			pluginID := "zed"
			if customID == "zed" {
				pluginID = "zed-plugin"
			}
			p = whaleCatalogProvider(t, pluginID)
			if !p.IsPlugin() || PluginID("zed") != pluginID {
				t.Fatalf("plugin collided with custom provider: %s", p.ID)
			}
			if err := MigrateLegacyPluginRecords(); err != nil {
				t.Fatal(err)
			}
			if got := load().Providers; len(got) != 1 || got[0].ID != customID || !maps.Equal(got[0].Contexts, custom.Contexts) {
				t.Fatalf("custom record migrated: %+v", got)
			}
		})
	}
}
