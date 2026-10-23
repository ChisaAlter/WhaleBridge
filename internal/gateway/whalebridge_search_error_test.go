package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// The whole key crosses APIError's 300-rune fallback boundary. Masking only
// its formatted error cannot match that partial echo, nor hide the rest of
// a longer overlapping key if a shorter one was masked first.
func TestWhaleBridgeSearchErrorMasksBeforeTruncation(t *testing.T) {
	fresh(t)
	forgetSearchKeys()
	searchQuotas.Clear()
	t.Cleanup(func() { forgetSearchKeys(); searchQuotas.Clear() })
	short := "tvly-boundary-short-secret-1111"
	long := short + "-overlap-tail-2222"
	sibling := "tvly-boundary-sibling-secret-3333"
	keys := []string{short, long, sibling}
	for _, tc := range []struct {
		name, opening, closing, first string
	}{
		{"plain-short", "", "", short},
		{"plain-overlap", "", "", long},
		{"plain-sibling", "", "", sibling},
		{"unknown-json", `{"unrecognized":"`, `"}`, long},
		{"known-json", `{"error":{"message":"`, `"}}`, long},
		{"you-message", `{"message":"`, `"}`, long},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forgetSearchKeys()
			searchQuotas.Clear()
			body := tc.opening + strings.Repeat("鲸", 290-utf8.RuneCountInString(tc.opening)) + tc.first + " " + strings.Join(keys, " ") + tc.closing
			if !strings.Contains(provider.APIError([]byte(body), "401 Unauthorized"), "tvly-bound") {
				t.Fatal("fixture did not expose a key prefix before masking")
			}
			assertSafe := func(text string) {
				t.Helper()
				for _, fragment := range []string{short, long, sibling, "tvly-bound", "-overlap-tail-"} {
					if strings.Contains(text, fragment) {
						t.Fatalf("error exposed key fragment %q: %s", fragment, text)
					}
				}
				if !strings.Contains(text, "tvly-") {
					t.Fatalf("key-boundary fixture no longer reaches formatted error: %s", text)
				}
			}
			var reads atomic.Int32
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/usage" {
					reads.Add(1)
				}
				if strings.Contains(tc.name, "json") || tc.name == "you-message" {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, body)
			}))
			t.Cleanup(remote.Close)
			a := provider.SearchAPI{Vendor: "tavily", URL: remote.URL, Key: strings.Join(keys, ",")}
			searchAPI := a
			if tc.name == "you-message" {
				searchAPI.Vendor = "you"
			}
			_, err := New().askSearchAPIKeys(context.Background(), searchAPI, "q")
			if err == nil {
				t.Fatal("refused search returned success")
			}
			assertSafe(err.Error())
			// This HTTP wrapper serializes the production quota DTO on both
			// fresh and cached reads, as the settings endpoint does.
			quota := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"vendor": a.Vendor, "keys": SearchQuotas(r.Context(), a, r.URL.Query().Get("fresh") == "1")})
			}))
			t.Cleanup(quota.Close)
			for _, query := range []string{"?fresh=1", ""} {
				res, err := http.Get(quota.URL + query)
				if err != nil {
					t.Fatal(err)
				}
				b, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				assertSafe(string(b))
				var dto struct{ Keys []SearchKeyQuota }
				if err := json.Unmarshal(b, &dto); err != nil || len(dto.Keys) != len(keys) {
					t.Fatalf("quota JSON %s: %v", b, err)
				}
				for _, q := range dto.Keys {
					if q.Known || q.Err == "" {
						t.Fatalf("refusal became known quota %+v", q)
					}
					assertSafe(q.Err)
				}
			}
			if n := reads.Load(); n != int32(len(keys)) {
				t.Fatalf("cached quota made additional HTTP requests: %d", n)
			}
			searchQuotas.Range(func(_, v any) bool { assertSafe(v.(keptQuota).q.Err); return true })
		})
	}
}
