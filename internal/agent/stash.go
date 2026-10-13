package agent

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/edit"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// The stash remembers what an agent's config said before magpie pointed it
// at the gateway, so switching back restores it instead of guessing.
//
// stash.json holds every agent's originals, so a change to it is read,
// changed and written under stashMu, and written whole (edit.WriteAtomic):
// two changes at once, or one cut short, never leave it half there, which
// the next change read as no stash and wrote back over every agent's. A
// file that doesn't parse (edited by hand, or half written by an older
// magpie) is copied aside before it is written over, so what it held can
// still be got back.
var stashMu sync.Mutex

func stashPath() string { return filepath.Join(filepath.Dir(provider.Path()), "stash.json") }

func stashLoad() map[string]string {
	out := map[string]string{}
	if b, err := os.ReadFile(stashPath()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// stashWrite writes m as the stash, a file there that doesn't parse kept
// aside first. The caller holds stashMu.
func stashWrite(m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	p := stashPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	keepUnreadStash(p)
	// made 0600 first: WriteAtomic keeps a file's mode, and the stash holds
	// keys
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		f.Close()
	} else if !errors.Is(err, fs.ErrExist) {
		return err
	}
	return edit.WriteAtomic(p, b)
}

// keepUnreadStash copies a stash.json that doesn't parse aside before it is
// written over.
func keepUnreadStash(path string) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || json.Valid(b) {
		return
	}
	bad := path + ".bad-" + time.Now().Format("20060102-150405.000000000")
	if err := os.WriteFile(bad, b, 0o600); err != nil {
		log.Printf("stash.json doesn't parse and couldn't be kept: %v", err)
		return
	}
	log.Printf("stash.json didn't parse; it is kept as %s", filepath.Base(bad))
}

func stash(kv map[string]string) {
	stashMu.Lock()
	defer stashMu.Unlock()
	m := stashLoad()
	for k, v := range kv {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	stashWrite(m)
}

// forget drops stashed values without restoring them.
func forget(keys ...string) {
	stashMu.Lock()
	defer stashMu.Unlock()
	m := stashLoad()
	for _, k := range keys {
		delete(m, k)
	}
	stashWrite(m)
}

func unstash(key string) string {
	stashMu.Lock()
	defer stashMu.Unlock()
	m := stashLoad()
	v := m[key]
	if v != "" {
		delete(m, key)
		stashWrite(m)
	}
	return v
}
