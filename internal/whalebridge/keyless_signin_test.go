package whalebridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
)

func TestWhaleBridgeKeylessSignInDispatchHTTP(t *testing.T) {
	bun := os.Getenv("MAGPIE_BUN")
	if bun == "" {
		t.Skip("local Bun fixture runtime not configured")
	}
	h := newProviderHTTP(t)
	t.Setenv("MAGPIE_BUN", bun)
	plugin.Settle()
	t.Cleanup(plugin.Settle)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs("../plugin/testdata/protocol/index.js")
	if err != nil {
		t.Fatal(err)
	}
	h.call("POST", "subscription/adapter", map[string]any{"action": "install", "package": fixture}, 200)
	identity := filepath.Join(h.data, "fixture-identity.json")
	componentContractWriteJSON(t, identity, map[string]string{"key": "fixture-native-key"})
	if err := plugin.SetOptions(fixture, map[string]any{"single": true, "identity": identity}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	var result struct{ State string }
	if err := json.Unmarshal(h.call("POST", "signin", map[string]any{"agent": "fixture", "plugin": true, "method": 0}, 200), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "done" || plugin.Auths("fixture")["fixture"]["key"] != "fixture-native-key" {
		t.Fatal("keyless API login did not run its authorize method")
	}
	// The server trusts the installed method, not presence of a key. Invalid
	// method indexes and ordinary API methods with no key do not write auth.
	before, _ := os.ReadFile(plugin.AuthPath())
	for _, method := range []int{-1, 1, 99} {
		h.call("POST", "signin", map[string]any{"agent": "fixture", "plugin": true, "method": method}, 400)
	}
	after, _ := os.ReadFile(plugin.AuthPath())
	if string(before) != string(after) {
		t.Fatal("bad method or missing key changed credentials")
	}
	var oauth struct{ ID, State string }
	if err := json.Unmarshal(h.call("POST", "signin", map[string]any{"agent": "fixture", "plugin": true, "method": 3, "key": "must-not-dispatch-as-api"}, 200), &oauth); err != nil {
		t.Fatal(err)
	}
	if oauth.ID == "" || oauth.State != "waiting" {
		t.Fatalf("OAuth method mis-dispatched: %+v", oauth)
	}
	h.call("POST", "signin/"+oauth.ID+"/cancel", map[string]any{}, 200)
}
