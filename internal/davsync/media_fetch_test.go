package davsync

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/backup"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

func TestMediaFetchHashIgnoresOnlyLocalGrant(t *testing.T) {
	base := settings.Settings{Theme: "dark", Currency: "cny"}
	want := hashes(backup.Bundle{Version: backup.BundleVersion, Settings: &base})
	for _, grant := range [][]string{nil, {}, {"one.internal.test"}, {"two.internal.test:8443", "10.51.0.0/16"}} {
		s := base
		s.FetchPrivate = slices.Clone(grant)
		got := hashes(backup.Bundle{Version: backup.BundleVersion, Settings: &s})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("local network authorization changed a sync hash: %v", grant)
		}
		if !reflect.DeepEqual(s.FetchPrivate, grant) {
			t.Fatal("hashing mutated the source grant list")
		}
		for _, changed := range []settings.Settings{
			{Theme: "light", Currency: base.Currency, FetchPrivate: grant},
			{Theme: base.Theme, Currency: "usd", FetchPrivate: grant},
		} {
			if h := hashes(backup.Bundle{Version: backup.BundleVersion, Settings: &changed}); h["settings"] == want["settings"] {
				t.Fatal("ordinary theme or currency changes stopped contributing to the settings hash")
			}
		}
	}
}

// Each computer has a separate component directory. Only this loopback
// fixture receives the sealed WebDAV object, and every attempted PUT counts.
func TestMediaFetchWebDAVMachinesKeepLocalGrant(t *testing.T) {
	for _, secondGrant := range [][]string{nil, {"b-media.internal.test:9443", "192.168.72.0/24"}} {
		name := "fresh-second-machine"
		if secondGrant != nil {
			name = "authorized-second-machine"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var remote []byte
			var etag string
			puts := 0
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				user, password, ok := r.BasicAuth()
				if !ok || user != "media-fixture-user" || password != "media-fixture-password" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if r.URL.Path != "/dav/magpie/"+file {
					t.Errorf("unexpected fixture path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				switch r.Method {
				case http.MethodGet, http.MethodHead:
					if remote == nil {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					w.Header().Set("ETag", etag)
					if r.Method == http.MethodGet && r.Header.Get("If-None-Match") == etag {
						w.WriteHeader(http.StatusNotModified)
						return
					}
					w.Header().Set("Content-Length", strconv.Itoa(len(remote)))
					if r.Method == http.MethodGet {
						_, _ = w.Write(remote)
					}
				case http.MethodPut:
					puts++
					if match := r.Header.Get("If-Match"); match != "" && match != etag {
						w.WriteHeader(http.StatusPreconditionFailed)
						return
					}
					b, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					remote = b
					etag = `"` + sum(b) + `"`
					w.Header().Set("ETag", etag)
					w.WriteHeader(http.StatusCreated)
				default:
					t.Errorf("unexpected fixture method: %s", r.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
			}))
			t.Cleanup(fake.Close)
			cfg := Config{URL: fake.URL + "/dav/", User: "media-fixture-user", Password: "media-fixture-password", Passphrase: "synthetic-media-sync-passphrase", Keys: true}
			a, b := newComputer(t), newComputer(t)
			firstGrant := []string{"a-media.internal.test:8443", "10.71.0.0/16"}
			b.use(t)
			second := settings.Load()
			second.Theme, second.Currency, second.FetchPrivate = "light", "usd", slices.Clone(secondGrant)
			if err := settings.Save(second); err != nil {
				t.Fatal(err)
			}
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			a.use(t)
			first := settings.Load()
			first.Theme, first.Currency, first.FetchPrivate = "dark", "cny", slices.Clone(firstGrant)
			if err := settings.Save(first); err != nil {
				t.Fatal(err)
			}
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			if err := Now(t.Context()); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			initial := bytes.Clone(remote)
			initialPuts := puts
			mu.Unlock()
			if initialPuts != 1 {
				t.Fatalf("initial sync PUTs: %d", initialPuts)
			}
			opened, err := backup.Open(initial, cfg.Passphrase)
			if err != nil || opened.Settings == nil || !slices.Equal(opened.Settings.FetchPrivate, firstGrant) {
				t.Fatalf("sealed remote did not carry source configuration: %v", err)
			}
			b.use(t)
			if err := Now(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := settings.Load(); got.Theme != "dark" || got.Currency != "cny" || !slices.Equal(got.FetchPrivate, secondGrant) {
				t.Fatalf("join changed B's local grant or lost shared settings: theme=%s currency=%s grant=%v", got.Theme, got.Currency, got.FetchPrivate)
			}
			assertUnchanged := func(want []byte, wantPuts int) {
				t.Helper()
				mu.Lock()
				defer mu.Unlock()
				if puts != wantPuts || !bytes.Equal(remote, want) {
					t.Fatalf("local grant change rewrote remote: PUTs=%d want=%d", puts, wantPuts)
				}
			}
			assertUnchanged(initial, initialPuts)
			second = settings.Load()
			second.FetchPrivate = []string{"b-new-media.internal.test:9553", "172.29.0.0/16"}
			if err := settings.Save(second); err != nil {
				t.Fatal(err)
			}
			if err := Now(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertUnchanged(initial, initialPuts)
			second = settings.Load()
			second.Theme, second.Currency = "light", "usd"
			if err := settings.Save(second); err != nil {
				t.Fatal(err)
			}
			if err := Now(t.Context()); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			updated, updatedPuts := bytes.Clone(remote), puts
			mu.Unlock()
			if updatedPuts != initialPuts+1 || bytes.Equal(updated, initial) {
				t.Fatal("ordinary settings change did not produce exactly one remote update")
			}
			a.use(t)
			if err := Now(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := settings.Load(); got.Theme != "light" || got.Currency != "usd" || !slices.Equal(got.FetchPrivate, firstGrant) {
				t.Fatalf("pull changed A's local grant or lost normal changes: theme=%s currency=%s grant=%v", got.Theme, got.Currency, got.FetchPrivate)
			}
			first = settings.Load()
			first.FetchPrivate = nil
			if err := settings.Save(first); err != nil {
				t.Fatal(err)
			}
			if err := Now(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertUnchanged(updated, updatedPuts)
			b.use(t)
			if err := Now(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := settings.Load(); !slices.Equal(got.FetchPrivate, second.FetchPrivate) {
				t.Fatalf("A revoking its grant changed B's grant: %v", got.FetchPrivate)
			}
			assertUnchanged(updated, updatedPuts)
		})
	}
}
