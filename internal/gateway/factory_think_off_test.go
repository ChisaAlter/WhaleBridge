package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// A retired Factory subscription is served through its plugin. The host
// lists whether a model supports "none" or starts at "low"; all client
// protocols must preserve that distinction when translated to Chat.
func TestFactoryThinkingOff(t *testing.T) {
	// besideFake resets HOME; the explicit catalog cache survives that
	// reset and keeps host startup entirely local.
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_OFF", "1")
	f := &fake{t: t, reply: sse(`data: {"id":"x","choices":[{"index":0,"delta":{"content":"ok"}}]}`, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`)}
	id := besideFake(t, "factory", f)
	p, err := provider.Find(id)
	if err != nil || !p.IsPlugin() || !provider.BuiltinRetired("factory") || id != "factory" {
		t.Fatalf("Factory is not served by its retired plugin id: %+v %v", p, err)
	}

	for _, tt := range []struct {
		name, path, body, want string
	}{
		{"chat none", "/v1/chat/completions", `{"model":"factory/fake-off","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`, "none"},
		{"chat low", "/v1/chat/completions", `{"model":"factory/fake-off","reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`, "low"},
		{"responses none", "/v1/responses", `{"model":"factory/fake-off","reasoning":{"effort":"none"},"input":[{"role":"user","content":"hi"}]}`, "none"},
		{"messages disabled", "/v1/messages", `{"model":"factory/fake-off","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}],"max_tokens":8}`, "none"},
		{"mandatory chat none", "/v1/chat/completions", `{"model":"factory/fake-on","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`, "low"},
		{"mandatory responses none", "/v1/responses", `{"model":"factory/fake-on","reasoning":{"effort":"none"},"input":[{"role":"user","content":"hi"}]}`, "low"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f.got, f.path = nil, ""
			code, out := post(t, tt.path, tt.body)
			if code != http.StatusOK {
				t.Fatalf("%d %s", code, out)
			}
			if f.path != "/v1/chat/completions" || f.head.Get("Authorization") != "Bearer k1" {
				t.Fatalf("plugin fetch was not used: %s %v", f.path, f.head)
			}
			var m map[string]any
			if json.Unmarshal(f.got, &m) != nil {
				t.Fatalf("upstream %s", f.got)
			}
			if m["reasoning_effort"] != tt.want {
				t.Errorf("reasoning_effort %v, want %s", m["reasoning_effort"], tt.want)
			}
		})
	}
}
