package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/access"
	"github.com/ChisaAlter/WhaleBridge/internal/gateway"
)

// An agent in a WSL distro under NAT reaches the gateway at Windows'
// address, where gateway.Token is turned away (whqtian on Discord: OpenCode
// in WSL was written "apiKey": "magpie" and refused). Every agent's config
// carries the LAN sharing key there while sharing is on, and gateway.Token on
// loopback, or while nothing is shared.
func TestGatewayKeyBeyondLoopback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	wsl := "http://172.23.80.1:3425"
	local := "http://127.0.0.1:3425"

	keys := func(gw string) map[string]string {
		oc := magpieProviderJSONAt("opencode", "opencode", gw).(map[string]any)["options"].(map[string]any)
		cr := magpieProviderJSONAt("crush", "crush", gw).(map[string]any)
		pi := magpieProviderJSONAt("pi", "pi", gw).(map[string]any)
		out := map[string]string{
			"opencode": oc["apiKey"].(string),
			"crush":    cr["api_key"].(string),
			"pi":       pi["apiKey"].(string),
			"hermes":   hermesProviderAt(gw).APIKey,
		}
		for _, e := range droidEntriesAt(gw) {
			out["droid"] = e.APIKey
		}
		return out
	}
	want := func(gw, key string) {
		t.Helper()
		for agent, k := range keys(gw) {
			if k != key {
				t.Errorf("%s at %s: key %q, want %q", agent, gw, k, key)
			}
		}
	}

	want(wsl, gateway.Token) // nothing shared: nothing else to give
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	lan := access.LANSecret()
	if !strings.HasPrefix(lan, access.Prefix) {
		t.Fatalf("LAN key %q", lan)
	}
	want(wsl, lan)
	want(local, gateway.Token)
	want("http://localhost:3425", gateway.Token)
	if k := qoderProviderAt("qoder", "", wsl+"/v1")["apiKey"]; k != lan {
		t.Errorf("Qoder in WSL: key %q, want the LAN key", k)
	}
	if k := qoderProviderAt("qoder", "", local+"/v1")["apiKey"]; k != gateway.TokenFor("qoder") {
		t.Errorf("Qoder on loopback: key %q", k)
	}
	if !ourKey(lan) || !ourKey(gateway.Token) || ourKey("sk-someone-else") || ourKey("") {
		t.Error("ourKey tells magpie's keys from others wrongly")
	}
	if !qoderKeyed(lan, "qoder") {
		t.Error("a Qoder given the LAN key isn't seen as on magpie")
	}

	if err := access.ConfigureLAN(false, false); err != nil {
		t.Fatal(err)
	}
	want(wsl, gateway.Token)
}
