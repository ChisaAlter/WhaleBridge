package agent

import (
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/edit"
	"github.com/ChisaAlter/WhaleBridge/internal/gateway"
)

func TestWhaleBridgeClaudeSignInKeepsProxyAndWSLAddress(t *testing.T) {
	const token = "11111111111111111111111111111111"
	t.Setenv("WHALEBRIDGE_GATEWAY_TOKEN", token)
	t.Setenv("MAGPIE_ADDR", "")
	was := gateway.Token
	gateway.Token = token
	t.Cleanup(func() { gateway.Token = was })
	home, path := claudeSettings(t, `{"model":"sonnet","theme":"dark"}`)
	saveDeepseek(t)
	a := claude(home)
	if err := a.Apply("model", "deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	const proxy = "http://127.0.0.1:8788/fixture-proxy"
	fakeMagpieAt(t, map[string]bool{proxy: true})
	if err := edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: proxy}); err != nil {
		t.Fatal(err)
	}
	check := func(url, key string) {
		t.Helper()
		if u, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); u != url {
			t.Fatal("login conversion lost the user's proxy base")
		}
		if k, _ := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN"); k != key {
			t.Fatal("login conversion wrote the wrong credential mode")
		}
		if off := a.Check(); off != "" {
			t.Fatal("converted proxy no longer passes ownership check")
		}
	}
	if err := a.Apply("login", "claudeai"); err != nil {
		t.Fatal(err)
	}
	private := strings.TrimPrefix(gateway.WhaleBridgeAnthropicURL(), gateway.URL())
	check(proxy+private, "")
	if err := a.Apply("model", "deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	check(proxy+private, "")
	if err := a.Apply("login", ""); err != nil {
		t.Fatal(err)
	}
	check(proxy, token)
	if err := a.Apply("login", "claudeai"); err != nil {
		t.Fatal(err)
	}
	check(proxy+private, "")
	// WSL changes only the host of the explicitly connected gateway, while
	// the private purpose/key suffix stays the same.
	base := "http://172.23.80.1:3425"
	at := place{home: home, base: func() string { return base }}
	if at.claudeURL() != base+private {
		t.Fatal("WSL private Claude URL lost its gateway host or authenticated suffix")
	}
}
