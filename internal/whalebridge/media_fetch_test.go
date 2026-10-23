package whalebridge

import (
	"bytes"
	"encoding/base64"
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
	"github.com/ChisaAlter/WhaleBridge/internal/gateway"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// A local control-plane save can authorize a private media origin. A media URL
// itself cannot, and an imported bundle must preserve this computer's grants.
// Exercise the real HTTP consumer as well as the management settings handlers.
func TestWhaleBridgeMediaFetchAuthorizationHTTP(t *testing.T) {
	a := newProviderHTTP(t)
	var hits atomic.Int32
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG\r\n\x1a\nfixture-private-image"))
	}))
	t.Cleanup(inside.Close)
	target := inside.URL + "/image.png"
	fetch := func(allowed bool) {
		t.Helper()
		before := hits.Load()
		_, _, err := gateway.FetchImage(t.Context(), gateway.PublicClient(), target)
		if allowed {
			if err != nil || hits.Load() != before+1 {
				t.Fatalf("explicit local grant did not reach the media server: %v", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "fetchPrivate") || hits.Load() != before {
			t.Fatalf("ungranted private media reached the server: %v, hits=%d", err, hits.Load())
		}
	}
	fetch(false)

	grant := strings.TrimPrefix(inside.URL, "http://")
	want := []string{grant}
	a.call("POST", "global/settings", map[string]any{"currency": "cny", "fetchPrivate": want}, 200)
	if !slices.Equal(settings.Load().FetchPrivate, want) {
		t.Fatal("explicit local settings save did not create the media grant")
	}
	fetch(true)

	// An unrelated page save preserves the grant; malformed input is rejected
	// before any settings are written, rather than granting a partial list.
	a.call("POST", "global/settings", map[string]any{"currency": "usd"}, 200)
	for _, patch := range []any{"invalid-type", []string{"http://127.0.0.1"}, []string{"127.0.0.1:0"}} {
		a.call("POST", "global/settings", map[string]any{"currency": "cny", "fetchPrivate": patch}, 400)
		if got := settings.Load(); got.Currency != "usd" || !slices.Equal(got.FetchPrivate, want) {
			t.Fatal("a refused media grant partially changed local settings")
		}
	}
	var global struct {
		Settings struct {
			FetchPrivate []string `json:"fetchPrivate"`
		}
	}
	if err := json.Unmarshal(a.call("GET", "global", nil, 200), &global); err != nil || !slices.Equal(global.Settings.FetchPrivate, want) {
		t.Fatalf("the local editor did not read back its grant: %v", err)
	}
	if raw := a.call("GET", "state", nil, 200); bytes.Contains(raw, []byte("fetchPrivate")) || bytes.Contains(raw, []byte(grant)) {
		t.Fatal("general provider state exposed unrelated local network grants")
	}

	// A decryptable backup is data to restore, not authorization for a new
	// network. It keeps this computer's explicit grant and ordinary settings.
	incoming := settings.Settings{Currency: "cny", FetchPrivate: []string{"169.254.169.254", "10.0.0.0/8"}}
	sealed, err := backup.Seal(backup.Bundle{Version: backup.BundleVersion, Settings: &incoming}, "fixture-media-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	restore := func() {
		t.Helper()
		a.call("POST", "backup/import", map[string]any{"pass": "fixture-media-passphrase", "data": base64.StdEncoding.EncodeToString(sealed), "settings": true}, 200)
	}
	restore()
	if got := settings.Load(); got.Currency != "cny" || !slices.Equal(got.FetchPrivate, want) {
		t.Fatal("import changed the local media grant or lost ordinary settings")
	}
	fetch(true)

	for _, cleared := range []any{[]string{}, nil} {
		a.call("POST", "global/settings", map[string]any{"fetchPrivate": cleared}, 200)
		if len(settings.Load().FetchPrivate) != 0 {
			t.Fatal("explicit local revocation did not remove the grant")
		}
		fetch(false)
	}
	restore()
	if len(settings.Load().FetchPrivate) != 0 {
		t.Fatal("restoring a backup authorized private media on an ungranted computer")
	}
	fetch(false)
}

// The real component wraps these handlers in its launcher authentication. Only
// the local management origin with that cookie can make an explicit grant; the
// provider gateway key and a cross-origin page do not grant management access.
func TestWhaleBridgeMediaFetchManagementBoundary(t *testing.T) {
	a := newProviderHTTP(t)
	p := startRuntimeComponent(t, a.vendor, a.vendor)
	body := []byte(`{"fetchPrivate":["127.0.0.1"]}`)
	path := filepath.Join(p.data, "settings.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	withoutCookie := &http.Client{Timeout: 15 * time.Second}
	for _, tc := range []struct {
		name, url, origin, contentType, host string
		client                               *http.Client
		status                               int
	}{
		{name: "no launcher cookie", url: p.origin, origin: p.origin, contentType: "application/json", client: withoutCookie, status: http.StatusUnauthorized},
		{name: "foreign page origin", url: p.origin, origin: "https://foreign.invalid", contentType: "application/json", client: p.client, status: http.StatusForbidden},
		{name: "foreign management host", url: p.origin, origin: p.origin, host: "foreign.invalid", contentType: "application/json", client: p.client, status: http.StatusForbidden},
		{name: "plain form submission", url: p.origin, origin: p.origin, contentType: "text/plain", client: p.client, status: http.StatusUnsupportedMediaType},
		{name: "gateway bearer key", url: p.gateway, origin: p.origin, contentType: "application/json", client: withoutCookie, status: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest(http.MethodPost, tc.url+"/api/global/settings", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			r.Host = tc.host
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", tc.contentType)
			r.Header.Set("Authorization", "Bearer "+runtimePrivateKey)
			response, err := tc.client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			text, _ := io.ReadAll(response.Body)
			if response.StatusCode != tc.status {
				t.Fatalf("HTTP %d, want %d: %s", response.StatusCode, tc.status, text)
			}
			if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected management request changed local settings")
			}
		})
	}
	p.request(http.MethodPost, p.origin+"/api/global/settings", body, false).requireOK(t)
	var saved settings.Settings
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &saved) != nil || !slices.Equal(saved.FetchPrivate, []string{"127.0.0.1"}) {
		t.Fatalf("authorized local management request did not save the grant: %v", err)
	}
	p.request(http.MethodPost, p.origin+"/api/global/settings", []byte(`{"fetchPrivate":[]}`), false).requireOK(t)
	p.requestStop(t)
	p.wait(t)
}
