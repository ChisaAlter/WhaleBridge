package whalebridge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/backup"
	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
)

// Run owns the actual group route registration. A separate test process
// exercises that handler and the launcher's cookie exchange without leaving
// component background workers running after this test's temporary HOME ends.
func TestWhaleBridgeFreeFirstComponentServer(t *testing.T) {
	if os.Getenv("WHALEBRIDGE_FREE_FIRST_HTTP_CHILD") != "1" {
		return
	}
	testenv.Offline()
	if err := Run("free-first-http-test"); err != nil {
		t.Fatal(err)
	}
}

func TestWhaleBridgeFreeFirstHTTP(t *testing.T) {
	a := newProviderHTTP(t)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:0")
	t.Setenv("MAGPIE_KEY", "")
	t.Setenv(testenv.Marker, os.Getenv("HOME"))
	t.Setenv("PATH", os.Getenv("HOME"))
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		t.Setenv(key, "")
	}
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "component.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestWhaleBridgeFreeFirstComponentServer$", "-test.timeout=90s")
	cmd.Env = append(os.Environ(), "WHALEBRIDGE_FREE_FIRST_HTTP_CHILD=1")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
		_ = log.Close()
	})
	var running struct{ URL string }
	for until := time.Now().Add(15 * time.Second); ; {
		if b, err := os.ReadFile(filepath.Join(a.data, "state.json")); err == nil && json.Unmarshal(b, &running) == nil && running.URL != "" {
			break
		}
		if time.Now().After(until) {
			b, _ := os.ReadFile(logPath)
			t.Fatalf("component did not become ready: %s", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	res, err := client.Get(running.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("launcher cookie exchange failed: %d", res.StatusCode)
	}
	origin := res.Request.URL.Scheme + "://" + res.Request.URL.Host
	call := func(method, path string, body any) []byte {
		t.Helper()
		var input io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			input = bytes.NewReader(b)
		}
		r, err := http.NewRequest(method, origin+"/api/"+path, input)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		b, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: HTTP %d: %s (%v)", method, path, response.StatusCode, b, err)
		}
		return b
	}
	const secret = "fixture-free-first-key-do-not-return"
	const headerSecret = "fixture-free-first-header-do-not-return"
	call("POST", "provider/save", map[string]any{"new": true, "id": "free-relay", "name": "Free routing fixture", "chat": a.vendor + "/v1", "key": secret, "headers": map[string]string{"X-Fixture-Secret": headerSecret}, "models": []string{"test-model"}, "routing": provider.FreeFirst, "affinity": "session", "sink": true, "maxRPM": 19, "quotaRest": 29})
	read := func() (provider.Provider, provider.Group) {
		t.Helper()
		b := call("GET", "state", nil)
		assertSecretsAbsent(t, b, secret, headerSecret)
		var state struct {
			Providers []provider.Provider
			Groups    []provider.Group
		}
		if err := json.Unmarshal(b, &state); err != nil {
			t.Fatal(err)
		}
		var p provider.Provider
		var g provider.Group
		for _, row := range state.Providers {
			if row.ID == "free-relay" {
				p = row
			}
		}
		for _, row := range state.Groups {
			if row.ID == "daily-free" {
				g = row
			}
		}
		return p, g
	}
	p, _ := read()
	if p.Routing != provider.FreeFirst || p.Affinity != "session" || !p.Sink || p.MaxRPM != 19 || p.QuotaRest != 29 {
		t.Fatal("provider Free first editor state lost request controls")
	}
	call("POST", "provider/save", map[string]any{"id": p.ID, "name": "Reopened provider", "key": ""})
	p, _ = read()
	if p.Routing != provider.FreeFirst || p.Affinity != "session" || !p.Sink {
		t.Fatal("unrelated edit erased provider Free first")
	}
	for _, mode := range []string{provider.Ordered, "", provider.FreeFirst} {
		call("POST", "provider/route", map[string]any{"id": p.ID, "routing": mode})
		p, _ = read()
		if p.Routing != mode {
			t.Fatalf("direct route mutation did not persist %q", mode)
		}
	}
	g := provider.Group{ID: "daily-free", Name: "Daily Free", Members: []string{"free-relay/test-model"}, Routing: provider.FreeFirst, Affinity: "session", Sink: true, FirstToken: 11, Patience: 37, Pick: "free-relay/test-model", Weights: map[string]int{"free-relay/test-model": 3}, Context: 96000, Levels: []string{"low", "high"}, Family: "daily-fixture"}
	call("POST", "group", g)
	_, reopened := read()
	if !reflect.DeepEqual(reopened, g) {
		t.Fatalf("group Free first did not preserve other editor fields: %#v", reopened)
	}
	for _, mode := range []string{provider.Ordered, "", provider.FreeFirst} {
		payload, err := json.Marshal(g)
		if err != nil {
			t.Fatal(err)
		}
		var input map[string]any
		if err := json.Unmarshal(payload, &input); err != nil {
			t.Fatal(err)
		}
		input["from"], input["routing"] = g.ID, mode
		call("POST", "group", input)
		_, reopened = read()
		want := g
		want.Routing = mode
		if !reflect.DeepEqual(reopened, want) {
			t.Fatalf("switching group strategy to %q erased another setting: %#v", mode, reopened)
		}
	}
	assertSecretsAbsent(t, call("POST", "provider/export", map[string]any{"id": p.ID}), secret, headerSecret)
	var saved struct{ Providers []provider.Provider }
	stored, err := os.ReadFile(provider.Path())
	if err != nil || json.Unmarshal(stored, &saved) != nil || len(saved.Providers) != 1 || saved.Providers[0].Key != secret || saved.Providers[0].Headers["X-Fixture-Secret"] != headerSecret {
		t.Fatal("routing edits replaced blank-preserved credentials")
	}
	const pass = "fixture-free-first-backup-pass"
	sealed := call("POST", "backup/export", map[string]any{"pass": pass, "keys": false})
	assertSecretsAbsent(t, sealed, secret, headerSecret)
	bundle, err := backup.Open(sealed, pass)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Providers) != 1 || bundle.Providers[0].Routing != provider.FreeFirst || len(bundle.Groups) != 1 || bundle.Groups[0].Routing != provider.FreeFirst || bundle.Providers[0].Key != "" || len(bundle.Providers[0].Headers) != 0 {
		t.Fatal("keyless backup lost routing or exported provider credentials")
	}
	call("POST", "provider/route", map[string]any{"id": p.ID, "routing": "order"})
	call("POST", "backup/import", map[string]any{"data": base64.StdEncoding.EncodeToString(sealed), "pass": pass, "providers": true, "settings": false})
	p, reopened = read()
	if p.Routing != provider.FreeFirst || !reflect.DeepEqual(reopened, g) {
		t.Fatal("backup restore did not retain provider/group Free first and other group settings")
	}
}

// The page may display a fetched price without that price establishing a
// known tariff. Its free filter must not mistake this DTO for a list price.
func TestWhaleBridgeFreeFirstFetchedZeroPriceDTO(t *testing.T) {
	a := newProviderHTTP(t)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	const model = "ff-unknown-display-zero-model"
	const secret = "fixture-price-provenance-secret"
	id := a.save(map[string]any{"new": true, "id": "display-zero", "name": "Display zero", "chat": a.vendor + "/v1", "key": secret, "models": []string{model}, "routing": provider.FreeFirst})
	p := a.provider(id)
	if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{{ID: model, Price: &catalog.Price{}}}); err != nil {
		t.Fatal(err)
	}
	provider.Changed()
	if p.FreeModel(model) {
		t.Fatal("ordinary fetched zero price became a known free tariff")
	}
	b := a.call("GET", "state", nil, http.StatusOK)
	assertSecretsAbsent(t, b, secret)
	var state struct {
		Providers []struct {
			ID        string `json:"id"`
			Available []struct {
				ID    string         `json:"id"`
				Free  bool           `json:"free"`
				Price *catalog.Price `json:"price"`
				List  *catalog.Price `json:"list"`
			} `json:"available"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	for _, entry := range state.Providers {
		if entry.ID != id {
			continue
		}
		for _, row := range entry.Available {
			if row.ID == model {
				if row.Price == nil || row.List != nil || row.Free || row.Price.Input != 0 || row.Price.Output != 0 {
					t.Fatalf("fetched display-price provenance changed: %+v", row)
				}
				return
			}
		}
	}
	t.Fatal("saved provider's display-price model was absent from the management DTO")
}
