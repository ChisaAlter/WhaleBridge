package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
)

func freeFirstFixture(t *testing.T) (context.Context, string) {
	t.Helper()
	sandbox(t)
	storeSandbox(t)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	state := filepath.Join(t.TempDir(), "catalog.json")
	putFixtureJSON(t, state, map[string]any{})
	abs, err := filepath.Abs("testdata/freefirst/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if err := SetOptions(abs, map[string]any{"statePath": state}); err != nil {
		t.Fatal(err)
	}
	return ctx, state
}

func freeTablesByAccount(t *testing.T, p Provider) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, a := range p.Accounts {
		if a.FellBack {
			t.Fatalf("account %s unexpectedly fell back: %s", a.Key, a.ListError)
		}
		key, _ := Auths("freefixture")[a.Key]["key"].(string)
		out[key] = a.FreeModels
		if slices.Contains(a.FreeModels, "retired") || slices.Contains(a.Models, "retired") {
			t.Fatal("deprecated model entered account metadata")
		}
	}
	return out
}

func TestHostFreeModelsBelongToEachActualAccount(t *testing.T) {
	ctx, state := freeFirstFixture(t)
	putFixtureJSON(t, state, map[string]any{"a": map[string]any{"free": []string{"shared"}}, "b": map[string]any{"free": []string{}}})
	if _, err := APIKey(ctx, "freefixture", 0, nil, "a", NewAccount); err != nil {
		t.Fatal(err)
	}
	// A single account must also have an explicit complete table.
	ps, err := Providers(ctx)
	if err != nil || len(ps) != 1 || len(ps[0].Accounts) != 1 || !slices.Equal(ps[0].Accounts[0].FreeModels, []string{"shared"}) {
		t.Fatalf("single account table: %+v, %v", ps, err)
	}
	if _, err := APIKey(ctx, "freefixture", 0, nil, "b", NewAccount); err != nil {
		t.Fatal(err)
	}
	ps, err = Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"a": {"shared"}, "b": {}}
	if got := freeTablesByAccount(t, ps[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("same id inherited the other account's free status: %#v, want %#v", got, want)
	}
	// The union deliberately remains first-wins. Account facts must survive
	// that representation, including after reversing which account is free.
	if !ps[0].Models[0].Free {
		t.Fatal("fixture did not exercise the free-first union")
	}
	putFixtureJSON(t, state, map[string]any{"a": map[string]any{"free": []string{}}, "b": map[string]any{"free": []string{"shared"}}})
	ps, err = Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want = map[string][]string{"a": {}, "b": {"shared"}}
	if got := freeTablesByAccount(t, ps[0]); !reflect.DeepEqual(got, want) || ps[0].Models[0].Free {
		t.Fatalf("reversed account facts were lost: %#v / union=%+v", got, ps[0].Models)
	}
	// Cached rebuilds accounts from credentials; it must copy the facts
	// back, both from the in-memory provider cache and its real disk file.
	if got := freeTablesByAccount(t, Cached()[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("in-memory Cached lost free tables: %#v", got)
	}
	UseCached(nil)
	provMu.Lock()
	provGood, provTried = true, true
	provMu.Unlock()
	if got := freeTablesByAccount(t, Cached()[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("disk Cached lost explicit free/paid tables: %#v", got)
	}
}

func TestHostFreeModelsFallbackKeepsOnlyThatAccountsLastFact(t *testing.T) {
	for _, mode := range []string{"fallback", "throw"} {
		t.Run(mode, func(t *testing.T) {
			ctx, state := freeFirstFixture(t)
			for _, key := range []string{"a", "b"} {
				if _, err := APIKey(ctx, "freefixture", 0, nil, key, NewAccount); err != nil {
					t.Fatal(err)
				}
			}
			putFixtureJSON(t, state, map[string]any{"a": map[string]any{"free": []string{"shared"}}, "b": map[string]any{"free": []string{}}})
			if _, err := Providers(ctx); err != nil {
				t.Fatal(err)
			}
			putFixtureJSON(t, state, map[string]any{"a": map[string]any{"mode": mode}, "b": map[string]any{"free": []string{"shared"}}})
			ps, err := Providers(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string][]string{"a": {"shared"}, "b": {"shared"}}
			if got := freeTablesByAccount(t, ps[0]); !reflect.DeepEqual(got, want) {
				t.Fatalf("primary fallback did not preserve only its own old table: %#v", got)
			}
			// Switch the failing account. Its old free status is retained;
			// the newly successful account's explicit [] must win immediately.
			putFixtureJSON(t, state, map[string]any{"a": map[string]any{"free": []string{}}, "b": map[string]any{"mode": mode}})
			ps, err = Providers(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want = map[string][]string{"a": {}, "b": {"shared"}}
			if got := freeTablesByAccount(t, ps[0]); !reflect.DeepEqual(got, want) {
				t.Fatalf("secondary fallback borrowed new primary facts: %#v", got)
			}
		})
	}
}

func TestHostFreeModelsUnreadListStaysUnknownThenKeepsKnownEmpty(t *testing.T) {
	ctx, state := freeFirstFixture(t)
	if _, err := APIKey(ctx, "freefixture", 0, nil, "a", NewAccount); err != nil {
		t.Fatal(err)
	}
	putFixtureJSON(t, state, map[string]any{"a": map[string]any{"mode": "fallback"}})
	ps, err := Providers(ctx)
	if err != nil || len(ps) != 1 || len(ps[0].Accounts) != 1 || ps[0].Accounts[0].FreeModels != nil {
		t.Fatalf("unread list invented free facts: %+v, %v", ps, err)
	}
	putFixtureJSON(t, state, map[string]any{"a": map[string]any{"free": []string{}}})
	ps, err = Providers(ctx)
	if err != nil || ps[0].Accounts[0].FreeModels == nil || len(ps[0].Accounts[0].FreeModels) != 0 {
		t.Fatalf("known paid account did not state an empty free table: %+v, %v", ps, err)
	}
	putFixtureJSON(t, state, map[string]any{"a": map[string]any{"mode": "fallback"}})
	ps, err = Providers(ctx)
	if err != nil || ps[0].Accounts[0].FellBack || ps[0].Accounts[0].FreeModels == nil || len(ps[0].Accounts[0].FreeModels) != 0 {
		t.Fatalf("fallback lost the same account's known empty table: %+v, %v", ps, err)
	}
}

func TestFreeModelsUnknownEmptyAndDiskRoundTrip(t *testing.T) {
	storeSandbox(t)
	UseCached(nil)
	t.Cleanup(func() { UseCached(nil) })
	writeList(t, `{"plugins":[{"spec":"freefixture"}]}`)
	if err := os.WriteFile(AuthPath(), []byte(`{"freefixture":{"type":"api","key":"a"},"freefixture#2":{"type":"api","key":"b"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []Provider{{ID: "freefixture", Spec: "freefixture", Models: []Model{{ID: "shared", Free: true}}, Accounts: []Account{
		{Key: "freefixture", FreeModels: nil},
		{Key: "freefixture#2", FreeModels: []string{}},
	}}}
	commitProviders(want, nil)
	UseCached(nil)
	provMu.Lock()
	provGood, provTried = true, true
	provMu.Unlock()
	got := Cached()
	if len(got) != 1 || got[0].Accounts[0].FreeModels != nil || got[0].Accounts[1].FreeModels == nil || len(got[0].Accounts[1].FreeModels) != 0 {
		t.Fatalf("disk erased unknown versus known-empty: %+v", got)
	}
	// Missing old JSON metadata stays unknown even when its provider union
	// has Free=true. No inference is made while reading an old cache.
	var old []Provider
	if err := json.Unmarshal([]byte(`[{"id":"freefixture","spec":"freefixture","models":[{"id":"shared","free":true}],"accounts":[{"key":"freefixture"}]}]`), &old); err != nil {
		t.Fatal(err)
	}
	if old[0].Accounts[0].FreeModels != nil {
		t.Fatal("old cache invented per-account facts")
	}
	now := []Provider{{ID: "freefixture", Spec: "freefixture", Models: []Model{{ID: "default"}}, FellBack: true, Accounts: []Account{{Key: "freefixture", FellBack: true}}}}
	kept := keepListed(now, old)
	if kept[0].Accounts[0].FreeModels != nil {
		t.Fatal("fallback invented facts from an old global free model")
	}
}

func TestFreeModelsFallbackAndCachedDoNotAliasPriorSlices(t *testing.T) {
	storeSandbox(t)
	writeList(t, `{"plugins":[{"spec":"freefixture"}]}`)
	if err := os.WriteFile(AuthPath(), []byte(`{"freefixture":{"type":"api","key":"a"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	last := []Provider{{ID: "freefixture", Spec: "freefixture", Models: []Model{{ID: "shared"}}, Accounts: []Account{{Key: "freefixture", Models: []string{"shared"}, FreeModels: []string{"shared"}}}}}
	now := []Provider{{ID: "freefixture", Spec: "freefixture", FellBack: true, Accounts: []Account{{Key: "freefixture", FellBack: true}}}}
	got := keepListed(now, last)
	got[0].Accounts[0].FreeModels[0] = "changed"
	if last[0].Accounts[0].FreeModels[0] != "shared" {
		t.Fatal("fallback table aliased its old generation")
	}
	UseCached(last)
	t.Cleanup(func() { UseCached(nil) })
	got = Cached()
	got[0].Accounts[0].FreeModels[0] = "changed"
	if last[0].Accounts[0].FreeModels[0] != "shared" {
		t.Fatal("Cached table aliased shared provider metadata")
	}
}
