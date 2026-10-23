package whalebridge

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/backup"
	"github.com/ChisaAlter/WhaleBridge/internal/gateway"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// Local grants come from the settings file. Ordinary page patches and an
// imported settings section cannot turn a crafted private media URL into a
// download. Exercise the real HTTP consumers rather than only the whitelist.
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
				t.Fatalf("explicit file grant did not reach the media server: %v", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "fetchPrivate") || hits.Load() != before {
			t.Fatalf("a page patch granted a private download: %v, hits=%d", err, hits.Load())
		}
	}

	// JSON submitted by a page cannot add an allowance.
	a.call("POST", "global/settings", map[string]any{"currency": "cny", "fetchPrivate": []string{"127.0.0.1"}}, 200)
	if len(settings.Load().FetchPrivate) != 0 {
		t.Fatal("unrecognized settings patch created a media grant")
	}
	fetch(false)

	st := settings.Load()
	st.FetchPrivate = []string{strings.TrimPrefix(inside.URL, "http://")}
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(st.FetchPrivate)
	for _, patch := range []any{nil, []string{}, []string{"169.254.169.254"}, "invalid-type"} {
		a.call("POST", "global/settings", map[string]any{"currency": "usd", "fetchPrivate": patch}, 200)
		if !slices.Equal(settings.Load().FetchPrivate, want) {
			t.Fatal("ordinary page patch replaced the local file grant")
		}
	}
	for _, endpoint := range []string{"global", "state"} {
		raw := a.call("GET", endpoint, nil, 200)
		if bytes.Contains(raw, []byte("fetchPrivate")) || bytes.Contains(raw, []byte(want[0])) {
			t.Fatalf("%s exposed local network grants to the initial page", endpoint)
		}
	}
	fetch(true)

	// Even a decryptable imported bundle is data to restore, not a new
	// authorization for this computer's network.
	incoming := settings.Settings{Currency: "cny", FetchPrivate: []string{"169.254.169.254", "10.0.0.0/8"}}
	sealed, err := backup.Seal(backup.Bundle{Version: backup.BundleVersion, Settings: &incoming}, "fixture-media-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	a.call("POST", "backup/import", map[string]any{"pass": "fixture-media-passphrase", "data": base64.StdEncoding.EncodeToString(sealed), "settings": true}, 200)
	if got := settings.Load(); got.Currency != "cny" || !slices.Equal(got.FetchPrivate, want) {
		t.Fatal("import changed the local media grant or lost ordinary settings")
	}
	fetch(true)

	st = settings.Load()
	st.FetchPrivate = nil
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	fetch(false)
}
