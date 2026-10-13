package agent

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// The stash remembers what an agent's config said before magpie pointed it
// at the gateway, so switching back restores it instead of guessing.

func stashPath() string { return filepath.Join(filepath.Dir(provider.Path()), "stash.json") }

func stashLoad() map[string]string {
	out := map[string]string{}
	if b, err := os.ReadFile(stashPath()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

func stash(kv map[string]string) {
	m := stashLoad()
	for k, v := range kv {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	os.MkdirAll(filepath.Dir(stashPath()), 0o755)
	os.WriteFile(stashPath(), b, 0o600)
}

// forget drops stashed values without restoring them.
func forget(keys ...string) {
	m := stashLoad()
	for _, k := range keys {
		delete(m, k)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(stashPath(), b, 0o600)
}

func unstash(key string) string {
	m := stashLoad()
	v := m[key]
	if v != "" {
		delete(m, key)
		b, _ := json.MarshalIndent(m, "", "  ")
		os.WriteFile(stashPath(), b, 0o600)
	}
	return v
}
