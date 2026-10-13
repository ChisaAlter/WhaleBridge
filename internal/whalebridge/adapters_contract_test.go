package whalebridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/middleware"
	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// The test executable stands in only for the plugin host's process protocol.
// It cannot download Bun, load user plugins, or send a vendor request.
func init() {
	if os.Getenv("WHALEBRIDGE_CONTRACT_FAKE_PLUGIN_HOST") != "1" {
		return
	}
	for _, arg := range os.Args[1:] {
		if arg == "--version" {
			io.WriteString(os.Stdout, "1.3.0\n")
			os.Exit(0)
		}
	}
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	var specs []string
	var by string
	for {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if dec.Decode(&req) != nil {
			os.Exit(0)
		}
		var result any
		switch req.Method {
		case "init":
			var in struct {
				Plugins []struct{ Spec string }
				Prefer  map[string]string
			}
			if json.Unmarshal(req.Params, &in) != nil {
				os.Exit(2)
			}
			for _, p := range in.Plugins {
				specs = append(specs, p.Spec)
			}
			if len(specs) == 0 {
				os.Exit(3)
			}
			by = in.Prefer["fixtureco"]
			if by == "" {
				by = specs[len(specs)-1]
			}
			loaded := []plugin.Loaded{}
			for i, spec := range specs {
				p := plugin.Loaded{Spec: spec, Provides: []string{"fixtureco"}}
				if spec != by {
					p.ServedBy = map[string]string{"fixtureco": by}
				}
				if i == len(specs)-1 {
					p.Provides = append(p.Provides, "otherco")
				}
				loaded = append(loaded, p)
			}
			result = map[string]any{"plugins": loaded}
		case "providers":
			result = []plugin.Provider{{ID: "fixtureco", Name: "FixtureCo", Spec: by}, {ID: "otherco", Name: "OtherCo", Spec: specs[len(specs)-1]}}
		default:
			_ = enc.Encode(map[string]any{"id": req.ID, "error": "unexpected fixture method: " + req.Method})
			continue
		}
		if enc.Encode(map[string]any{"id": req.ID, "result": result}) != nil {
			os.Exit(4)
		}
	}
}

func TestAdapterMiddlewareDiscoveryAndStatsHTTP(t *testing.T) {
	h := newProviderHTTP(t)
	t.Cleanup(plugin.Settle)
	root := filepath.Join(h.data, "fixtures")
	good, bad := filepath.Join(root, "good.middleware.js"), filepath.Join(root, "broken.middleware.js")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte(`export function onRequest(body) { if (body.fail) throw new Error("fixture middleware failure"); body.changed = true; return body; }`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(`export function onRequest( {`), 0600); err != nil {
		t.Fatal(err)
	}
	componentContractWriteJSON(t, filepath.Join(settings.Dir(), "plugins.json"), plugin.List{Plugins: []plugin.Entry{{Spec: good}, {Spec: bad}}})
	plugin.UseCached([]plugin.Provider{})
	middleware.Reload()
	run := middleware.Begin(middleware.Info{Protocol: "chat", Model: "fixture-model", Path: "/v1/chat/completions"})
	if run == nil {
		t.Fatal("the installed middleware did not run")
	}
	out, rejected := run.Request([]byte(`{"model":"fixture-model"}`))
	run.End()
	if rejected != nil || !bytes.Contains(out, []byte(`"changed":true`)) {
		t.Fatal("the real middleware engine did not apply the fixture")
	}
	run = middleware.Begin(middleware.Info{Protocol: "chat", Model: "fixture-model"})
	run.Request([]byte(`{"model":"fixture-model","fail":true}`))
	run.End()
	market := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"plugins":[{"package":"fixture-provider","name":"Provider","community":true,"providers":["fixtureco"]},{"package":"fixture-middleware","name":"Middleware","community":true,"kind":"middleware"},{"package":"fixture-agent","name":"Agent","community":true,"kind":"agent"}]}`)
	}))
	t.Cleanup(market.Close)
	t.Setenv("MAGPIE_PLUGIN_MARKET", market.URL)
	plugin.RefreshMarket()
	var dto struct {
		Installed []struct {
			Package, Kind                string
			IsMiddleware, MiddlewareOnly bool
			Middleware                   *middleware.State
		}
		Available []plugin.Listing
	}
	if err := json.Unmarshal(h.call("GET", "subscription/adapters", nil, 200), &dto); err != nil {
		t.Fatal(err)
	}
	if len(dto.Installed) != 2 {
		t.Fatal("installed middleware was filtered from discovery")
	}
	for _, row := range dto.Installed {
		if row.Kind != "middleware" || !row.IsMiddleware || !row.MiddlewareOnly || row.Middleware == nil {
			t.Fatal("middleware classification/state is missing")
		}
		if row.Package == good && (row.Middleware.Calls != 2 || row.Middleware.Failures != 1 || !strings.Contains(row.Middleware.Last, "fixture middleware failure") || len(row.Middleware.Hooks) != 1 || row.Middleware.Hooks[0] != "onRequest") {
			t.Fatalf("middleware DTO lost real call/failure statistics: %+v", row.Middleware)
		}
		if row.Package == bad && row.Middleware.Error == "" {
			t.Fatal("invalid middleware was listed without its load error")
		}
	}
	kinds := map[string]bool{}
	for _, item := range dto.Available {
		kinds[item.Kind] = true
	}
	if !kinds["middleware"] || !kinds["agent"] {
		t.Fatal("the recommended directory still filters non-provider kinds")
	}
	if plugin.HasBun() || plugin.Running() {
		t.Fatal("middleware discovery started or downloaded the provider host runtime")
	}
}

func TestAdapterPreferClashContractHTTP(t *testing.T) {
	h := newProviderHTTP(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WHALEBRIDGE_CONTRACT_FAKE_PLUGIN_HOST", "1")
	t.Setenv("MAGPIE_BUN", exe)
	t.Cleanup(plugin.Settle)
	root := filepath.Join(h.data, "fixture-provider-plugins")
	mine, theirs := filepath.Join(root, "mine", "index.js"), filepath.Join(root, "theirs", "index.js")
	for _, path := range []string{mine, theirs} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`export default async function () { return {}; }`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	componentContractWriteJSON(t, filepath.Join(settings.Dir(), "plugins.json"), plugin.List{Plugins: []plugin.Entry{{Spec: mine}, {Spec: theirs}}})
	if _, err := plugin.Providers(context.Background()); err != nil {
		t.Fatal(err)
	}
	type row struct {
		Package   string
		Providers []string
		Clashes   []struct {
			ID, Name, By string
			With         []string
		}
	}
	read := func() map[string]row {
		t.Helper()
		var dto struct {
			Installed []row
			Prefer    map[string]string
		}
		if err := json.Unmarshal(h.call("GET", "subscription/adapters", nil, 200), &dto); err != nil {
			t.Fatal(err)
		}
		out := map[string]row{}
		for _, r := range dto.Installed {
			out[r.Package] = r
		}
		return out
	}
	check := func(rows map[string]row, owner string) {
		t.Helper()
		for spec, other := range map[string]string{mine: theirs, theirs: mine} {
			r := rows[spec]
			if len(r.Clashes) != 1 || r.Clashes[0].ID != "fixtureco" || r.Clashes[0].Name != "FixtureCo" || r.Clashes[0].By != owner || len(r.Clashes[0].With) != 1 || r.Clashes[0].With[0] != other {
				t.Fatalf("clash DTO did not identify both owners: %+v", r)
			}
		}
		if !containsContractID(rows[owner].Providers, "fixtureco") || !containsContractID(rows[theirs].Providers, "otherco") {
			t.Fatal("the selected provider or the other plugin's remaining provider was lost")
		}
	}
	check(read(), theirs)
	h.call("POST", "subscription/adapter", map[string]any{"action": "prefer", "package": mine, "provider": "fixtureco"}, 200)
	if plugin.Load().Prefer["fixtureco"] != mine {
		t.Fatal("the component prefer route did not persist its owner")
	}
	check(read(), mine)
}
