package davsync

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/backup"
	"github.com/ChisaAlter/WhaleBridge/internal/lastgood"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.RunIn(m, func(string) {
		testenv.Offline()
		if err := os.Setenv("LAUNCHER_COMPONENT_DATA_DIR", ""); err != nil {
			panic(err)
		}
	}))
}

// This fixture keeps the encrypted WebDAV object and counts all attempted
// PUTs. A failed local settings read must leave it byte-identical, even
// when the remote object is unchanged and the next computer syncs again.
func TestWhaleBridgeUpstreamSyncStopsOnUnknownSettings(t *testing.T) {
	var mu sync.Mutex
	var remote []byte
	var puts int
	var etag string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		user, password, ok := r.BasicAuth()
		if !ok || user != "fixture-user" || password != "fixture-password" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/dav/magpie/"+file {
			t.Errorf("unexpected WebDAV fixture path: %s", r.URL.Path)
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
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			remote = data
			etag = `"` + sum(data) + `"`
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected WebDAV fixture method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(fake.Close)
	cfg := Config{URL: fake.URL + "/dav/", User: "fixture-user", Password: "fixture-password", Passphrase: "fixture passphrase", Keys: true}
	a, b := newComputer(t), newComputer(t)
	b.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	a.use(t)
	if err := settings.Save(settings.Settings{Theme: "dark", Lang: "ja", TextSize: 125}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.use(t)
	if err := Now(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); s.Theme != "dark" || s.Lang != "ja" || s.TextSize != 125 {
		t.Fatal("second computer did not receive the initial settings")
	}
	a.use(t)
	broken := []byte(`{"theme":"dark","lang":`)
	if err := os.WriteFile(settings.Path(), broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lastgood.Bak(settings.Path())); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	mu.Lock()
	before, beforePuts := bytes.Clone(remote), puts
	mu.Unlock()
	if err := Now(t.Context()); err == nil || !strings.Contains(err.Error(), "settings") {
		t.Fatalf("sync did not stop on corrupt settings: %v", err)
	}
	if st := Status(); !strings.Contains(st.Error, "settings") {
		t.Fatalf("sync status does not show the failed settings read: %+v", st)
	}
	if got, err := os.ReadFile(settings.Path()); err != nil || !bytes.Equal(got, broken) {
		t.Fatalf("failed sync overwrote the broken local settings: %v", err)
	}
	local, err := collect(cfg)
	if err == nil || local.Settings != nil {
		t.Fatal("collect filled fallback defaults after a failed backup collection")
	}
	mu.Lock()
	after, afterPuts := bytes.Clone(remote), puts
	mu.Unlock()
	if afterPuts != beforePuts || !bytes.Equal(after, before) {
		t.Fatal("a failed local settings read changed the remote backup")
	}
	bundle, err := backup.Open(after, cfg.Passphrase)
	if err != nil || bundle.Settings == nil || bundle.Settings.Theme != "dark" || bundle.Settings.Lang != "ja" {
		t.Fatalf("remote lost the original settings: %v", err)
	}
	b.use(t)
	if err := Now(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); s.Theme != "dark" || s.Lang != "ja" || s.TextSize != 125 {
		t.Fatal("second computer lost its settings to the broken first computer")
	}
}

func TestWhaleBridgeUpstreamCollectFallbackOnlyAfterSuccessfulBackup(t *testing.T) {
	t.Run("missing settings retain sync defaults", func(t *testing.T) {
		newComputer(t).use(t)
		b, err := collect(Config{})
		if err != nil || b.Settings == nil || b.Settings.UIFont != nil || b.Settings.CodeFont != nil {
			t.Fatalf("missing settings fallback changed: settings=%+v err=%v", b.Settings, err)
		}
	})
	t.Run("providers error returns before settings fallback", func(t *testing.T) {
		newComputer(t).use(t)
		if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(provider.Path(), []byte(`{"providers":`), 0o600); err != nil {
			t.Fatal(err)
		}
		b, err := collect(Config{})
		if err == nil || b.Settings != nil {
			t.Fatalf("failed backup collection acquired default settings: settings=%+v err=%v", b.Settings, err)
		}
	})
}
