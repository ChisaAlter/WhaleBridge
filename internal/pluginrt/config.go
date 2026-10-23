package pluginrt

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/appdir"
	"github.com/ChisaAlter/WhaleBridge/internal/filelock"
	"github.com/ChisaAlter/WhaleBridge/internal/steady"
	"gopkg.in/yaml.v3"
)

const (
	PresetAll        = "all"
	PresetEssentials = "essentials"
)

type File struct {
	Preset  string `yaml:"preset,omitempty"`
	Plugins []Row  `yaml:"plugins,omitempty"`
}
type Row struct {
	ID      string         `yaml:"id"`
	Enabled *bool          `yaml:"enabled,omitempty"`
	Config  map[string]any `yaml:"config,omitempty"`
}

// Path is the headless Go feature configuration, not the supplier plugins.json.
func Path() string { return filepath.Join(appdir.Config(), "plugins.yml") }

func Load() (File, bool, error) {
	b, err := steady.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return File{}, false, nil
	}
	if err != nil {
		return File{}, true, err
	}
	var f File
	decoder := yaml.NewDecoder(strings.NewReader(string(b)))
	if err := decoder.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return File{}, true, fmt.Errorf("%s: %w", Path(), err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple YAML documents are not supported")
		}
		return File{}, true, fmt.Errorf("%s: %w", Path(), err)
	}
	if f.Preset != "" && f.Preset != PresetAll && f.Preset != PresetEssentials {
		return File{}, true, fmt.Errorf("unknown plugin preset %q", f.Preset)
	}
	seen := map[string]bool{}
	for _, r := range f.Plugins {
		if r.ID == "" || seen[r.ID] {
			return File{}, true, fmt.Errorf("empty or duplicate plugin row %q", r.ID)
		}
		seen[r.ID] = true
	}
	return f, true, nil
}

type Source string

const (
	FromDefault Source = "default"
	FromPreset  Source = "preset"
	FromUser    Source = "user"
	FromInUse   Source = "in use"
)

type Composed struct {
	ID      string            `json:"id" yaml:"id"`
	Enabled bool              `json:"enabled" yaml:"enabled"`
	Source  Source            `json:"source" yaml:"source"`
	Chosen  bool              `json:"chosen" yaml:"chosen"`
	InUse   int               `json:"inUse" yaml:"inUse,omitempty"`
	Config  map[string]any    `json:"config,omitempty" yaml:"config,omitempty"`
	Sources map[string]Source `json:"configSources,omitempty" yaml:"configSources,omitempty"`
}

// Compose merges nested YAML maps without sharing them with defaults or users.
// InUse errors and panics are conservatively counted as use.
func Compose(ms []Manifest, f File) []Composed {
	rows := map[string]Row{}
	for _, r := range f.Plugins {
		rows[r.ID] = r
	}
	out := make([]Composed, 0, len(ms))
	for _, m := range ms {
		c := Composed{ID: m.ID, Enabled: true, Source: FromDefault, Config: cloneMap(m.Defaults), Sources: map[string]Source{}}
		for key := range c.Config {
			c.Sources[key] = FromDefault
		}
		if f.Preset == PresetEssentials && !m.Essential {
			c.Enabled, c.Source = false, FromPreset
		}
		if r, ok := rows[m.ID]; ok {
			if r.Enabled != nil {
				c.Enabled, c.Source = *r.Enabled, FromUser
			}
			for key, value := range r.Config {
				if c.Config == nil {
					c.Config = map[string]any{}
				}
				if value == nil {
					c.Config[key] = nil
				} else {
					c.Config[key] = merge(c.Config[key], value)
				}
				c.Sources[key] = FromUser
			}
		}
		c.Chosen = c.Enabled
		if m.InUse != nil {
			c.InUse = inUse(m.InUse)
			if c.InUse > 0 && !c.Enabled {
				c.Enabled, c.Source = true, FromInUse
			}
		}
		if len(c.Config) == 0 {
			c.Config, c.Sources = nil, nil
		}
		out = append(out, c)
	}
	return out
}
func inUse(f func() (int, error)) (n int) {
	defer func() {
		if recover() != nil {
			n = 1
		}
	}()
	n, err := f()
	if err != nil {
		return 1
	}
	if n < 0 {
		return 1
	}
	return n
}
func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for key, value := range m {
		out[key] = clone(value)
	}
	return out
}
func clone(v any) any {
	switch value := v.(type) {
	case map[string]any:
		return cloneMap(value)
	case []any:
		out := make([]any, len(value))
		for i, entry := range value {
			out[i] = clone(entry)
		}
		return out
	case []string:
		return append([]string{}, value...)
	default:
		return v
	}
}
func merge(under, over any) any {
	if over == nil {
		return clone(under)
	}
	u, ok1 := under.(map[string]any)
	o, ok2 := over.(map[string]any)
	if !ok1 || !ok2 {
		return clone(over)
	}
	out := cloneMap(u)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range o {
		if value == nil {
			out[key] = nil
		} else {
			out[key] = merge(out[key], value)
		}
	}
	return out
}

// Refresh reads and applies synchronously. A corrupt/unreadable file leaves an
// already reconciled runtime untouched; before the first good read it applies
// safe default-on manifests and still returns the read error. Hosts may fail
// startup on that error and Stop/Wait before exposing any listener.
func (rt *Runtime) Refresh() ([]Composed, error) {
	if err := rt.begin(); err != nil {
		return nil, err
	}
	defer rt.op.Unlock()
	rt.mu.Lock()
	ms := make([]Manifest, 0, len(rt.plugins))
	for _, p := range rt.plugins {
		ms = append(ms, p.m)
	}
	was := rt.reconciled
	rt.mu.Unlock()
	sortManifests(ms)
	f, _, loadErr := Load()
	if loadErr != nil && was {
		return nil, loadErr
	}
	cs := Compose(ms, f)
	if loadErr != nil {
		// No preset or user row is trusted from an unreadable file.
		cs = Compose(ms, File{})
	}
	on, config := map[string]bool{}, map[string]map[string]any{}
	for _, c := range cs {
		on[c.ID], config[c.ID] = c.Enabled, c.Config
	}
	return cs, errors.Join(loadErr, rt.reconcile(on, config))
}
func sortManifests(ms []Manifest) {
	sort.Slice(ms, func(i, j int) bool { return ms[i].ID < ms[j].ID })
}

// Start is optional for a host that does not have its own admission/drain
// watcher. First Apply is synchronous. A parent cancellation stops only the
// watcher; unregistering requires explicit Stop after request draining.
func (rt *Runtime) Start(parent context.Context, every time.Duration, also ...string) error {
	if every <= 0 {
		return errors.New("pluginrt: refresh interval must be positive")
	}
	if err := parent.Err(); err != nil {
		return err
	}
	rt.mu.Lock()
	if rt.started {
		rt.mu.Unlock()
		return ErrStarted
	}
	if rt.stopped {
		rt.mu.Unlock()
		return ErrStopped
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	rt.started, rt.watchCancel, rt.watchDone = true, cancel, done
	rt.mu.Unlock()
	initial, _ := fileStamp(append([]string{Path()}, also...))
	_, err := rt.Refresh()
	if err != nil {
		cancel()
		close(done)
		return err
	}
	go func() {
		defer close(done)
		timer := time.NewTicker(every)
		defer timer.Stop()
		files := append([]string{Path()}, also...)
		last := initial
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			stamp, readErr := fileStamp(files)
			if readErr != nil {
				log.Printf("plugins: watcher: %v", readErr)
				continue
			}
			if stamp == last {
				continue
			}
			if _, err := rt.Refresh(); err != nil {
				if !errors.Is(err, ErrStopped) {
					log.Printf("plugins: refresh: %v", err)
				}
				continue
			}
			last = stamp
		}
	}()
	return nil
}
func fileStamp(files []string) ([32]byte, error) {
	h := sha256.New()
	for _, path := range files {
		b, err := steady.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return [32]byte{}, err
		}
		fmt.Fprintf(h, "%d:%s/%t/%d:", len(path), path, err == nil, len(b))
		h.Write(b)
	}
	var stamp [32]byte
	copy(stamp[:], h.Sum(nil))
	return stamp, nil
}

// StopWatching cancels and joins the optional watcher while leaving registered
// features active for in-flight requests. Do not call it from an Apply hook.
func (rt *Runtime) StopWatching() {
	rt.mu.Lock()
	cancel, done := rt.watchCancel, rt.watchDone
	rt.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
func (rt *Runtime) Status() []Status { return rt.Statuses() }

var configMu sync.Mutex
var configLock = filelock.Mutex{Path: func() string { return Path() + ".lock" }}

func update(change func(*File)) error {
	configMu.Lock()
	defer configMu.Unlock()
	configLock.Lock()
	defer configLock.Unlock()
	f, _, err := Load()
	if err != nil {
		return err
	}
	change(&f)
	b, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		return err
	}
	return steady.WriteFile(Path(), b, 0o600)
}
func Set(id string, enabled bool) error {
	if id == "" {
		return errors.New("pluginrt: empty plugin id")
	}
	return update(func(f *File) {
		for i := range f.Plugins {
			if f.Plugins[i].ID == id {
				f.Plugins[i].Enabled = &enabled
				return
			}
		}
		f.Plugins = append(f.Plugins, Row{ID: id, Enabled: &enabled})
	})
}
func SetConfig(id string, config map[string]any) error {
	if id == "" {
		return errors.New("pluginrt: empty plugin id")
	}
	return update(func(f *File) {
		for i := range f.Plugins {
			if f.Plugins[i].ID == id {
				f.Plugins[i].Config, _ = merge(f.Plugins[i].Config, config).(map[string]any)
				return
			}
		}
		f.Plugins = append(f.Plugins, Row{ID: id, Config: cloneMap(config)})
	})
}
func SetPreset(preset string) error {
	if preset != PresetAll && preset != PresetEssentials {
		return fmt.Errorf("unknown plugin preset %q", preset)
	}
	return update(func(f *File) { f.Preset = preset })
}
