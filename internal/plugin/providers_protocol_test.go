package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
)

func TestListedKeepsUnknownSeparateFromEmpty(t *testing.T) {
	storeSandbox(t)
	UseCached(nil)
	t.Cleanup(func() { UseCached(nil) })
	t.Setenv("MAGPIE_BUN", filepath.Join(t.TempDir(), "must-not-run.exe"))
	if ps, known := Listed(); known || ps != nil {
		t.Fatalf("missing listing = %+v, %v", ps, known)
	}
	for _, body := range []string{"[", "{\"id\":\"wrong-shape\"}"} {
		if err := os.WriteFile(providersPath(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if ps, known := Listed(); known || ps != nil {
			t.Fatalf("unreadable listing %q = %+v, %v", body, ps, known)
		}
	}
	if err := os.WriteFile(providersPath(), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ps, known := Listed(); !known || ps == nil || len(ps) != 0 {
		t.Fatalf("known empty listing = %+v, %v", ps, known)
	}
	writeList(t, `{"plugins":[{"spec":"fixture","off":true}]}`)
	want := []Provider{{ID: "fixture", Spec: "fixture", Accounts: []Account{{Key: "fixture"}}}}
	UseCached(want)
	ps, known := Listed()
	if !known || !reflect.DeepEqual(ps, want) {
		t.Fatalf("off plugin listing = %+v, %v", ps, known)
	}
	ps[0].ID = "changed"
	if again, _ := Listed(); again[0].ID != "fixture" {
		t.Fatal("Listed returned its shared provider slice")
	}
	if Running() {
		t.Fatal("reading a listing started a plugin host")
	}
}

func TestAccountModelsCacheRoundTrip(t *testing.T) {
	storeSandbox(t)
	UseCached(nil)
	t.Cleanup(func() { UseCached(nil) })
	writeList(t, `{"plugins":[{"spec":"fixture"}]}`)
	if err := os.WriteFile(AuthPath(), []byte(`{"fixture":{"type":"api","key":"base"},"fixture#empty":{"type":"api","key":"empty"},"fixture#some":{"type":"api","key":"some"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []Provider{{ID: "fixture", Spec: "fixture", Models: []Model{{ID: "one", ServiceTiers: []string{"priority", "ultrafast"}}}, Accounts: []Account{
		{Key: "fixture", Models: nil},
		{Key: "fixture#empty", Models: []string{}},
		{Key: "fixture#some", Models: []string{"one"}},
	}}}
	commitProviders(want, nil)
	UseCached(nil)
	// Do not refresh while testing the disk-to-cache protocol.
	provMu.Lock()
	provGood, provTried = true, true
	provMu.Unlock()
	ps := Cached()
	if len(ps) != 1 || len(ps[0].Accounts) != 3 {
		t.Fatalf("cached listing = %+v", ps)
	}
	for i, a := range ps[0].Accounts {
		if !reflect.DeepEqual(a.Models, want[0].Accounts[i].Models) {
			t.Fatalf("account %s: models %#v, want %#v", a.Key, a.Models, want[0].Accounts[i].Models)
		}
	}
	if !slices.Equal(ps[0].Models[0].ServiceTiers, []string{"priority", "ultrafast"}) {
		t.Fatalf("service tiers lost on disk read: %+v", ps[0].Models)
	}
}

func protocolFixture(t *testing.T, single bool) (context.Context, string, string) {
	t.Helper()
	sandbox(t)
	storeSandbox(t)
	// A local empty models.dev file keeps startup from asking the network.
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	identity := filepath.Join(t.TempDir(), "identity.json")
	catalogFile := filepath.Join(t.TempDir(), "catalog.json")
	putFixtureJSON(t, identity, map[string]any{"key": "native-fixture"})
	putFixtureJSON(t, catalogFile, map[string]any{})
	abs, err := filepath.Abs("testdata/protocol/index.js")
	if err != nil {
		t.Fatal(err)
	}
	var downloaded int
	if _, err := AddWithProgress(ctx, abs, func() { downloaded++ }); err != nil {
		t.Fatal(err)
	}
	if downloaded != 1 || Running() {
		t.Fatalf("local add progress=%d, host running=%v", downloaded, Running())
	}
	if err := SetOptions(abs, map[string]any{"single": single, "identity": identity, "catalog": catalogFile}); err != nil {
		t.Fatal(err)
	}
	return ctx, identity, catalogFile
}

func putFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHostKeylessNativeCredentialsAndSingleton(t *testing.T) {
	ctx, identity, _ := protocolFixture(t, true)
	ps, err := Providers(ctx)
	if err != nil || len(ps) != 1 || len(ps[0].Methods) != 4 {
		t.Fatalf("providers = %+v, %v", ps, err)
	}
	if !ps[0].Single || !ps[0].Methods[0].Keyless {
		t.Fatalf("native method flags lost: %+v", ps[0])
	}
	for _, i := range []int{1, 2, 3} {
		if ps[0].Methods[i].Keyless {
			t.Fatalf("non-keyless method %d advertised as keyless", i)
		}
	}
	if _, err := Authorize(ctx, "fixture", 0, nil, NewAccount); err == nil {
		t.Fatal("API keyless method was incorrectly dispatched as OAuth")
	}
	first, err := APIKey(ctx, "fixture", 0, nil, "", NewAccount)
	if err != nil || first.Account != "fixture" {
		t.Fatalf("keyless API result = %+v, %v", first, err)
	}
	// Native methods can return OAuth credentials directly from authorize.
	putFixtureJSON(t, identity, map[string]any{"refresh": "fixture-rotated", "access": "fixture-access", "expires": 0, "accountId": "other@fixture"})
	second, err := APIKey(ctx, "fixture", 0, nil, "", NewAccount)
	if err != nil || second.Account != first.Account {
		t.Fatalf("singleton keyless result = %+v, %v", second, err)
	}
	auths := Auths("fixture")
	if len(auths) != 1 || auths[first.Account]["type"] != "oauth" || auths[first.Account]["refresh"] != "fixture-rotated" {
		t.Fatalf("native credentials not read back as one OAuth account: %+v", auths)
	}
	if _, err := APIKey(ctx, "fixture", 0, map[string]string{"fail": "yes"}, "", NewAccount); !errors.Is(err, ErrFailed) {
		t.Fatalf("keyless failure = %v", err)
	}
	if got := Auths("fixture"); !reflect.DeepEqual(got, auths) {
		t.Fatalf("failed sign-in changed the saved identity: %+v", got)
	}
	a, err := Authorize(ctx, "fixture", 3, nil, NewAccount)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Finish(ctx, a.Session, "good"); err != nil {
		t.Fatal(err)
	}
	if got := Auths("fixture"); len(got) != 1 || got[first.Account]["refresh"] != "fixture-refresh" {
		t.Fatalf("OAuth singleton sign-in created an extra account: %+v", got)
	}
}

func TestHostModelServiceTiersNormalized(t *testing.T) {
	ctx, _, catalogFile := protocolFixture(t, false)
	ps, err := Providers(ctx)
	if err != nil || len(ps) != 1 {
		t.Fatalf("providers = %+v, %v", ps, err)
	}
	if !slices.Equal(ps[0].Models[0].ServiceTiers, []string{"priority", "ultrafast"}) || len(ps[0].Models[1].ServiceTiers) != 0 {
		t.Fatalf("config service tiers = %+v", ps[0].Models)
	}
	if _, err := APIKey(ctx, "fixture", 1, nil, "account-a", NewAccount); err != nil {
		t.Fatal(err)
	}
	putFixtureJSON(t, catalogFile, map[string]any{"account-a": map[string]any{"models": []string{"live"}, "tiers": []any{" priority ", map[string]any{"id": "ultrafast"}, "priority", 9, nil}}})
	ps, err = Providers(ctx)
	if err != nil || len(ps[0].Models) != 1 || !slices.Equal(ps[0].Models[0].ServiceTiers, []string{"priority", "ultrafast"}) {
		t.Fatalf("models hook service tiers = %+v, %v", ps, err)
	}
}

func TestHostAccountCatalogUnionAndFallback(t *testing.T) {
	for _, failed := range []string{"account-a", "account-b"} {
		t.Run(failed, func(t *testing.T) {
			ctx, _, catalogFile := protocolFixture(t, false)
			for _, key := range []string{"account-a", "account-b", "account-empty"} {
				if _, err := APIKey(ctx, "fixture", 1, nil, key, NewAccount); err != nil {
					t.Fatal(err)
				}
			}
			state := map[string]any{
				"account-a":     map[string]any{"models": []string{"only-a"}},
				"account-b":     map[string]any{"models": []string{"only-b"}},
				"account-empty": map[string]any{"models": []string{}},
			}
			putFixtureJSON(t, catalogFile, state)
			ps, err := Providers(ctx)
			if err != nil || len(ps) != 1 || !slices.Equal(modelIDs(ps[0].Models), []string{"only-a", "only-b"}) {
				t.Fatalf("account union = %+v, %v", ps, err)
			}
			checkLists := func(p Provider) {
				t.Helper()
				for _, account := range p.Accounts {
					key := Auths("fixture")[account.Key]["key"].(string)
					want := map[string][]string{"account-a": {"only-a"}, "account-b": {"only-b"}, "account-empty": {}}[key]
					if account.Models == nil || !slices.Equal(account.Models, want) || account.FellBack {
						t.Fatalf("%s models=%#v fallback=%v, want %#v", key, account.Models, account.FellBack, want)
					}
				}
			}
			checkLists(ps[0])
			// One vendor list fails while another adds a model. Preserve the
			// failed account's old model details and the successful new list.
			state[failed] = map[string]any{"mode": "fallback"}
			other := "account-b"
			if failed == other {
				other = "account-a"
			}
			state[other] = map[string]any{"models": []string{map[string]string{"account-a": "only-a", "account-b": "only-b"}[other], "new"}}
			putFixtureJSON(t, catalogFile, state)
			ps, err = Providers(ctx)
			if err != nil || len(ps[0].Models) != 3 {
				t.Fatalf("fallback union = %+v, %v", ps, err)
			}
			for _, id := range []string{"only-a", "only-b", "new"} {
				if !slices.ContainsFunc(ps[0].Models, func(m Model) bool {
					return m.ID == id && m.Name == id && slices.Equal(m.ServiceTiers, []string{"priority", "ultrafast"})
				}) {
					t.Fatalf("model %s details lost after fallback: %+v", id, ps[0].Models)
				}
			}
			// Re-read the real persisted accounts rather than UseCached fixtures.
			UseCached(nil)
			provMu.Lock()
			provGood, provTried = true, true
			provMu.Unlock()
			cached := Cached()
			if len(cached) != 1 || !reflect.DeepEqual(cached[0].Models, ps[0].Models) || !reflect.DeepEqual(cached[0].Accounts, ps[0].Accounts) {
				t.Fatalf("disk read changed account catalogs: %+v, want %+v", cached, ps)
			}
			// A thrown error follows the same retention path.
			state[failed] = map[string]any{"mode": "throw"}
			putFixtureJSON(t, catalogFile, state)
			if got, err := Providers(ctx); err != nil || !reflect.DeepEqual(got[0].Models, ps[0].Models) {
				t.Fatalf("thrown list lost the union: %+v, %v", got, err)
			}
		})
	}
}

func TestKeepListedRestoresKnownEmptyAccount(t *testing.T) {
	was := []Provider{{ID: "fixture", Spec: "fixture", Models: []Model{}, Accounts: []Account{{Key: "fixture", Models: []string{}}}}}
	now := []Provider{{ID: "fixture", Spec: "fixture", Models: []Model{{ID: "default"}}, FellBack: true, Accounts: []Account{{Key: "fixture", FellBack: true}}}}
	got := keepListed(now, was)[0]
	if got.FellBack || got.Accounts[0].FellBack || got.Models == nil || len(got.Models) != 0 || got.Accounts[0].Models == nil || len(got.Accounts[0].Models) != 0 {
		t.Fatalf("known empty list became a default: %+v", got)
	}
}
