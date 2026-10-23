package provider

import (
	"context"
	"fmt"
	"slices"

	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
)

// LeftRecord keeps a removed plugin's model picks without presenting them
// as a subscription which still needs signing in on this computer.
type LeftRecord struct {
	Package  string   `json:"package"`
	Provider Provider `json:"provider"`
}

// pluginRecordIDs reads the complete cached listing, including disabled
// plugins, without starting the host during removal.
func pluginRecordIDs(spec string) ([]string, bool) {
	listed, ok := plugin.Listed()
	if !ok {
		return nil, false
	}
	installed := map[string]bool{}
	for _, e := range plugin.Load().Plugins {
		installed[e.Spec] = true
	}
	others := map[string]bool{}
	for _, pp := range listed {
		if pp.Spec != spec && installed[pp.Spec] {
			others[PluginID(pp.ID)] = true
		}
	}
	var ids []string
	for _, pp := range listed {
		id := PluginID(pp.ID)
		if pp.Spec != spec || id == "" || id == "magpie" || slices.Contains(accountIDs, id) || others[id] || slices.Contains(ids, id) {
			continue
		}
		ids = append(ids, id)
	}
	return ids, true
}

func installedSpec(name string) string {
	es := plugin.Load().Plugins
	if i := slices.IndexFunc(es, func(e plugin.Entry) bool { return e.Spec == name }); i >= 0 {
		return es[i].Spec
	}
	if i := slices.IndexFunc(es, func(e plugin.Entry) bool { return plugin.Name(e.Spec) == name }); i >= 0 {
		return es[i].Spec
	}
	return ""
}

// setAside preserves only records with no endpoint or key of their own.
// User-created providers which reused a plugin's id stay in place.
func setAside(pkg string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	f, err := read()
	if err != nil {
		return err
	}
	keep := make([]Provider, 0, len(f.Providers))
	moved := false
	for _, p := range f.Providers {
		if slices.Contains(ids, p.ID) && OnlyPicks(p) {
			f.PluginLeft = slices.DeleteFunc(slices.Clone(f.PluginLeft), func(l LeftRecord) bool { return l.Provider.ID == p.ID })
			f.PluginLeft = append(f.PluginLeft, LeftRecord{Package: pkg, Provider: p})
			moved = true
			continue
		}
		keep = append(keep, p)
	}
	if !moved {
		return nil
	}
	f.Providers = keep
	return store(f)
}

func takeBackLeft(spec string) error {
	pkg := plugin.PackageName(spec)
	f, err := read()
	if err != nil {
		return err
	}
	left := make([]LeftRecord, 0, len(f.PluginLeft))
	changed := false
	for _, l := range f.PluginLeft {
		if l.Package == pkg && !slices.ContainsFunc(f.Providers, func(p Provider) bool { return p.ID == l.Provider.ID }) {
			f.Providers = append(slices.Clone(f.Providers), l.Provider)
			changed = true
			continue
		}
		left = append(left, l)
	}
	if !changed {
		return nil
	}
	f.PluginLeft = left
	return store(f)
}

// AddPlugin installs the adapter and restores model picks from its last
// removal. Credential ownership is handled separately by HandOver.
func AddPlugin(ctx context.Context, spec string) (plugin.Entry, error) {
	e, err := addPlugin(ctx, spec)
	return finishAddPlugin(e, err)
}

// AddPluginWithProgress retains the component's download progress callback.
func AddPluginWithProgress(ctx context.Context, spec string, downloaded func()) (plugin.Entry, error) {
	e, err := addPluginWithProgress(ctx, spec, downloaded)
	return finishAddPlugin(e, err)
}

var addPluginWithProgress = plugin.AddWithProgress

func finishAddPlugin(e plugin.Entry, err error) (plugin.Entry, error) {
	if err != nil {
		return e, err
	}
	if err := takeBackLeft(e.Spec); err != nil {
		return e, fmt.Errorf("%s is added; its model picks from before weren't put back: %w", e.Spec, err)
	}
	return e, nil
}
