package provider

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

func freeFirstCatalog(t *testing.T, prices map[string]*catalog.Price) {
	t.Helper()
	models := map[string]any{}
	for id, price := range prices {
		model := map[string]any{"id": id, "name": id}
		if price != nil {
			model["cost"] = price
		}
		models[id] = model
	}
	writeFile(t, catalog.CachePath(), map[string]any{"free-fixture": map[string]any{"models": models}})
	catalog.Reset()
	Changed()
}

func TestFreeFirstAllBillingDimensions(t *testing.T) {
	whaleCatalogHome(t)
	prices := map[string]*catalog.Price{
		"known-zero": {},
		"unknown":    nil,
		"input":      {Input: 1}, "output": {Output: 1}, "cache-read": {CacheRead: 1},
		"cache-write": {CacheWrite: 1}, "cache-write-hour": {CacheWrite1h: 1},
		"zero-tier":             {Tiers: []catalog.Tier{{Above: 1}}},
		"tier-input":            {Tiers: []catalog.Tier{{Above: 1, Input: 1}}},
		"tier-output":           {Tiers: []catalog.Tier{{Above: 1, Output: 1}}},
		"tier-cache-read":       {Tiers: []catalog.Tier{{Above: 1, CacheRead: 1}}},
		"tier-cache-write":      {Tiers: []catalog.Tier{{Above: 1, CacheWrite: 1}}},
		"tier-cache-write-hour": {Tiers: []catalog.Tier{{Above: 1, CacheWrite1h: 1}}},
		"late-paid-tier":        {Tiers: []catalog.Tier{{Above: 1}, {Above: 100, CacheWrite1h: 2}}},
		"paid-base-free-tier":   {Input: 1, Tiers: []catalog.Tier{{Above: 100}}},
		"negative-is-not-free":  {Input: -1},
	}
	freeFirstCatalog(t, prices)
	p := Provider{ID: "free-billing", Catalog: "free-fixture"}
	for id := range prices {
		t.Run(id, func(t *testing.T) {
			want := id == "known-zero" || id == "zero-tier"
			if got := p.FreeModel(id); got != want {
				t.Fatalf("FreeModel=%v, want %v", got, want)
			}
		})
	}
	if p.FreeModel("nobody-prices-this-model") {
		t.Fatal("an unknown model became free")
	}
}

func TestFreeFirstVendorFlagAndOverrides(t *testing.T) {
	whaleCatalogHome(t)
	freeFirstCatalog(t, map[string]*catalog.Price{"flagged": {Input: 3, Output: 15}, "paid": {Input: 3, Output: 15}, "zero": {}})
	p := Provider{ID: "free-vendor", Name: "Free Vendor", Chat: "http://127.0.0.1:9/v1", Key: "synthetic", Catalog: "free-fixture", Models: []string{"flagged", "paid", "zero"}, PriceRate: 0.001}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	writeFile(t, catalog.LivePath(p.ID), map[string]any{"models": []catalog.Model{{ID: "flagged", Free: true}, {ID: "paid"}, {ID: "zero"}}})
	Changed()
	if !p.FreeModel("FLAGGED") || p.FreeModel("paid") || !p.FreeModel("zero") {
		t.Fatal("vendor free flag or known zero price was misclassified")
	}
	// Only the provider carrying the flag can establish allowance-free use.
	other := p
	other.ID = "other-vendor"
	if other.FreeModel("flagged") {
		t.Fatal("a different provider inherited another vendor's free flag")
	}
	for _, rate := range []float64{0, 0.001, 1, 1000} {
		p.PriceRate = rate
		if p.FreeModel("paid") {
			t.Fatalf("price rate %v made paid model free", rate)
		}
	}
	for _, ref := range []string{p.ID + "/paid", p.ID + "/*", AnyPriceKey("paid")} {
		st := settings.Load()
		st.ModelPrices = map[string]settings.ModelPrice{ref: settings.StatedPrice(catalog.Price{})}
		if err := settings.Save(st); err != nil {
			t.Fatal(err)
		}
		if effective, ok := EffectivePrice(p.ID, "paid"); !ok || effective.Input != 0 || effective.Output != 0 {
			t.Fatal("zero display-price fixture did not apply")
		}
		if p.FreeModel("paid") {
			t.Fatalf("user override %s made paid model free", ref)
		}
	}
	st := settings.Load()
	st.ModelPrices = map[string]settings.ModelPrice{p.ID + "/zero": settings.StatedPrice(catalog.Price{Input: 99})}
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if !p.FreeModel("zero") {
		t.Fatal("a display-price override hid a genuinely zero list price")
	}
}

// Ordinary fetched price metadata is not a ListPrice source; a remote
// gateway's declared price is. Preserve that distinction instead of treating
// an unpriced fetched model's zero-value struct as a known free tariff.
func TestFreeFirstFetchedPriceUsesExistingListPriceContract(t *testing.T) {
	whaleCatalogHome(t)
	models := []catalog.Model{
		{ID: "fetched-zero", Price: &catalog.Price{}},
		{ID: "fetched-unknown"},
		{ID: "fetched-hour", Price: &catalog.Price{CacheWrite1h: 1}},
		{ID: "fetched-tier", Price: &catalog.Price{Tiers: []catalog.Tier{{Above: 10, Output: 1}}}},
	}
	ordinary := Provider{ID: "fetched-plain", Chat: "http://127.0.0.1:9/v1"}
	if err := catalog.SaveLive(ordinary.ID, ordinary.Chat, models); err != nil {
		t.Fatal(err)
	}
	if ordinary.FreeModel("fetched-zero") {
		t.Fatal("ordinary fetched price metadata invented a known list price")
	}
	remote := Provider{ID: "fetched-remote", Preset: RemoteMagpiePreset, Chat: ordinary.Chat}
	if err := catalog.SaveLive(remote.ID, remote.Chat, models); err != nil {
		t.Fatal(err)
	}
	if !remote.FreeModel("fetched-zero") || remote.FreeModel("fetched-unknown") || remote.FreeModel("fetched-hour") || remote.FreeModel("fetched-tier") {
		t.Fatal("remote list price lost known zero/unknown/one-hour/tier distinctions")
	}
}

func TestFreeFirstActualAccountFlagsAreIndependent(t *testing.T) {
	whaleCatalogHome(t)
	var fetches atomic.Int64
	account := func(user string, free bool) Provider {
		return Provider{ID: "account-free", Account: &Account{Agent: "fixture", User: user, magpieList: true,
			models: func() []catalog.Model { return []catalog.Model{{ID: "same-model", Free: free}} },
			fetch: func(context.Context) ([]catalog.Model, error) {
				fetches.Add(1)
				t.Error("free classification refreshed account models")
				return nil, nil
			}}}
	}
	a, b := account("free-plan", true), account("paid-plan", false)
	if !a.FreeModel("same-model") || b.FreeModel("same-model") || a.FreeModel("missing-model") {
		t.Fatal("free status leaked between accounts or models")
	}
	if fetches.Load() != 0 {
		t.Fatal("classification fetched models")
	}
}

func TestFreeFirstPluginAccountDoesNotUseCatalogUnion(t *testing.T) {
	whaleCatalogHome(t)
	pp := plugin.Provider{ID: "free-accounts", Spec: "@fixture/free-accounts", Models: whaleCatalogModels("first-paid", "second-free"), Accounts: []plugin.Account{{Key: "free-accounts", Models: []string{"first-paid"}, FreeModels: []string{}}, {Key: "free-accounts#2", Models: []string{"second-free"}, FreeModels: []string{"second-free"}}}}
	pp.Models[1].Free = true
	whaleCatalogPlugin(t, pp, map[string]any{"free-accounts": map[string]any{"type": "api", "key": "synthetic-one"}, "free-accounts#2": map[string]any{"type": "api", "key": "synthetic-two"}})
	saveLogins(t, savedLogin{Agent: "plugin:free-accounts", User: "one", Home: "free-accounts", On: true}, savedLogin{Agent: "plugin:free-accounts", User: "two", Home: "free-accounts#2", On: true})
	primary := whaleCatalogProvider(t, pp.ID)
	if !slices.Equal(modelIDs(primary.Available()), []string{"first-paid", "second-free"}) {
		t.Fatal("public account union changed")
	}
	if primary.Account.Lists("second-free") || primary.FreeModel("second-free") {
		t.Fatal("primary routing account borrowed free status from the second account's catalog")
	}
	current, _ := PluginOf(pp.ID)
	for _, login := range pluginLogins(current) {
		p := pluginProvider(current, login)
		want := login.acct.Key == "free-accounts#2"
		if p.FreeModel("second-free") != want {
			t.Fatalf("%s free status escaped its own models", login.acct.Key)
		}
	}
	// Unknown availability still uses the whole provider list, but free
	// status needs an independent, explicit fact for this account.
	pp.Accounts[0].Models = nil
	pp.Accounts[0].FreeModels = []string{"second-free"}
	plugin.UseCached([]plugin.Provider{pp})
	Changed()
	if !primary.FreeModel("second-free") {
		t.Fatal("unknown account list stopped using the provider catalog")
	}
	pp.Accounts[0].Models = []string{}
	plugin.UseCached([]plugin.Provider{pp})
	Changed()
	if primary.FreeModel("second-free") {
		t.Fatal("an explicitly empty account used another account's free model")
	}
}

func TestFreeFirstPluginSameModelFactsIgnoreUnionOrder(t *testing.T) {
	for _, freeFirst := range []bool{true, false} {
		name := "paid-account-first"
		if freeFirst {
			name = "free-account-first"
		}
		t.Run(name, func(t *testing.T) {
			whaleCatalogHome(t)
			pp := plugin.Provider{ID: "same-free", Spec: "@fixture/same-free", Models: whaleCatalogModels("same-model", "only-free-account"), Accounts: []plugin.Account{
				{Key: "same-free", Models: []string{"same-model", "only-free-account"}, FreeModels: []string{"same-model", "only-free-account"}},
				{Key: "same-free#2", Models: []string{"same-model"}, FreeModels: []string{}},
			}}
			if !freeFirst {
				pp.Accounts[0], pp.Accounts[1] = pp.Accounts[1], pp.Accounts[0]
			}
			// The provider-wide first-wins flag can be either value. Only
			// each account's explicit set establishes its free status.
			pp.Models[0].Free = freeFirst
			pp.Models[1].Free = freeFirst
			whaleCatalogPlugin(t, pp, map[string]any{"same-free": map[string]any{"type": "api", "key": "synthetic-free"}, "same-free#2": map[string]any{"type": "api", "key": "synthetic-paid"}})
			saveLogins(t, savedLogin{Agent: "plugin:same-free", User: "free", Home: "same-free", On: true}, savedLogin{Agent: "plugin:same-free", User: "paid", Home: "same-free#2", On: true})
			for _, login := range pluginLogins(pp) {
				p := pluginProvider(pp, login)
				want := login.acct.Key == "same-free"
				if p.FreeModel("SAME-MODEL") != want || p.FreeModel("only-free-account") != want {
					t.Fatalf("%s used union free=%v instead of own fact", login.acct.Key, freeFirst)
				}
			}
		})
	}
}

func TestFreeFirstPluginLegacyUnknownSurvivesAccountRemoval(t *testing.T) {
	whaleCatalogHome(t)
	freeFirstCatalog(t, map[string]*catalog.Price{"known-zero": {}})
	pp := plugin.Provider{ID: "legacy-free", Spec: "@fixture/legacy-free", Models: whaleCatalogModels("same-model", "known-zero"), Accounts: []plugin.Account{
		{Key: "legacy-free", Models: []string{"same-model", "known-zero"}},
		{Key: "legacy-free#2", Models: []string{"same-model", "known-zero"}},
	}}
	pp.Models[0].Free = true // legacy union retained from the former free account
	whaleCatalogPlugin(t, pp, map[string]any{"legacy-free": map[string]any{"type": "api", "key": "old-free"}, "legacy-free#2": map[string]any{"type": "api", "key": "still-paid"}})
	saveLogins(t, savedLogin{Agent: "plugin:legacy-free", User: "one", Home: "legacy-free", On: true}, savedLogin{Agent: "plugin:legacy-free", User: "two", Home: "legacy-free#2", On: true})
	var survivor Provider
	for _, login := range pluginLogins(pp) {
		p := pluginProvider(pp, login)
		p.Catalog = "free-fixture"
		if p.FreeModel("same-model") || !p.FreeModel("known-zero") {
			t.Fatal("legacy unknown used union flag or lost known-zero fallback")
		}
		if login.acct.Key == "legacy-free#2" {
			survivor = p
		}
	}
	// Removing the old first account cannot turn its leftover union flag
	// into proof that the now-single surviving account serves it free.
	pp.Accounts = pp.Accounts[1:]
	plugin.UseCached([]plugin.Provider{pp})
	Changed()
	if survivor.FreeModel("same-model") || !survivor.FreeModel("known-zero") {
		t.Fatal("legacy single survivor inherited the removed account's free flag")
	}
	// A successful refresh writes an explicit table, so free status is
	// recovered without changing the public union catalog.
	pp.Accounts[0].FreeModels = []string{"same-model"}
	plugin.UseCached([]plugin.Provider{pp})
	Changed()
	if !survivor.FreeModel("same-model") {
		t.Fatal("refreshed account did not use its new explicit free fact")
	}
	pp.Accounts[0].FreeModels = []string{}
	plugin.UseCached([]plugin.Provider{pp})
	Changed()
	if survivor.FreeModel("same-model") || !survivor.FreeModel("known-zero") {
		t.Fatal("explicit no-free set failed to replace old union fact")
	}
	pp.Accounts[0].FreeModels = []string{"same-model", "unlisted-model"}
	pp.Accounts[0].Models = []string{}
	plugin.UseCached([]plugin.Provider{pp})
	Changed()
	if survivor.FreeModel("same-model") || survivor.FreeModel("unlisted-model") || survivor.FreeModel("known-zero") {
		t.Fatal("free facts or known prices bypassed an explicitly empty model list")
	}
}

func TestFreeFirstRoutingPersistsAndInvalidStillNormalizes(t *testing.T) {
	whaleCatalogHome(t)
	p := Provider{ID: "free-routing", Name: "Free Routing", Chat: "http://127.0.0.1:9/v1", Key: "synthetic", Models: []string{"model"}, Routing: FreeFirst}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	found, err := Find(p.ID)
	if err != nil || found.Routing != FreeFirst {
		t.Fatalf("provider routing lost: %+v %v", found, err)
	}
	g := Group{ID: "free-routing-group", Name: "Free routing group", Members: []string{p.ID + "/model"}, Routing: FreeFirst}
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	stored := load()
	if len(stored.Groups) != 1 || stored.Groups[0].Routing != FreeFirst {
		t.Fatalf("group routing lost: %+v", stored.Groups)
	}
	for _, routing := range []string{Ordered, Rotate, LeastUsed, Pace, Weighted, FreeFirst, "invalid-policy"} {
		if err := SetRouting(p.ID, routing); err != nil {
			t.Fatal(err)
		}
		want := routing
		if routing == "invalid-policy" {
			want = ""
		}
		if got, err := Find(p.ID); err != nil || got.Routing != want {
			t.Fatalf("SetRouting %q -> %+v %v", routing, got, err)
		}
		g.Routing = routing
		if err := SaveGroup(g); err != nil {
			t.Fatal(err)
		}
		if got := load().Groups[0].Routing; got != want {
			t.Fatalf("SaveGroup %q -> %q", routing, got)
		}
	}
	if err := SetRouting(p.ID, FreeFirst); err != nil {
		t.Fatal(err)
	}
	g.Routing = FreeFirst
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	// Check the serialized boundary as well as normalized objects.
	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var disk struct {
		Providers []Provider `json:"providers"`
		Groups    []Group    `json:"groups"`
	}
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Providers) != 1 || disk.Providers[0].Routing != "free" || len(disk.Groups) != 1 || disk.Groups[0].Routing != "free" {
		t.Fatalf("serialized routing lost: %s", data)
	}
}
