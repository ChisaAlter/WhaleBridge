package provider

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/edit"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// Rename gives a provider another id, the one its models are picked by
// (<id>/<model>). What magpie keeps that names it by id follows: the
// routing groups it is in and their rules and classifiers, the other
// providers' fallbacks, and which agents are shown it. The old id stays
// with it (Was), so an agent still running on a config that names it the
// old way reaches it, and its usage so far is counted with it; the agents'
// own files are the agent package's to rewrite (agent.RenameProvider).
// A signed-in account keeps the id of its agent.
func Rename(from, to string) error {
	from = strings.ToLower(strings.TrimSpace(from))
	to = strings.ToLower(strings.TrimSpace(to))
	if to == "" || to != Slug(to) {
		return fmt.Errorf("a provider's id must be lowercase letters, digits and dashes, not %q", to)
	}
	if to == from {
		return nil
	}
	f, err := read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == from })
	// one of the user's own saved on a subscription's id before that was one
	// (a "WorkBuddy" key before v0.1.261) can be moved off it; the
	// subscription itself can't
	custom := i >= 0 && hasEndpoint(f.Providers[i])
	_, signedIn := find(Accounts(), from)
	sub := subscriptionID(from)
	if (signedIn || sub) && !custom {
		return fmt.Errorf("%s is a subscription: its id is its agent's", from)
	}
	switch {
	case to == "magpie":
		return errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	case to == strings.TrimSuffix(GroupPrefix, "/"):
		return errors.New(`"group" starts the ids of routing groups; pick another id`)
	case subscriptionID(to):
		return fmt.Errorf("%q is the id of the %s subscription; pick another", to, to)
	}
	if i < 0 {
		return fmt.Errorf("no provider %q", from)
	}
	if slices.ContainsFunc(f.Providers, func(p Provider) bool { return p.ID == to }) {
		return fmt.Errorf("there is a provider %q already", to)
	}
	// an id another provider once had is this one's now
	for j := range f.Providers {
		f.Providers[j].Was = slices.DeleteFunc(f.Providers[j].Was, func(w string) bool { return w == to })
	}
	p := &f.Providers[i]
	p.ID = to
	// the subscription's id is the subscription's: an old ref to it isn't
	// taken for this one
	if !sub && !slices.Contains(p.Was, from) {
		p.Was = append(p.Was, from)
	}
	for j := range f.Providers {
		for k, m := range f.Providers[j].Fallback {
			f.Providers[j].Fallback[k] = renamedRef(m, from, to)
		}
	}
	for j, id := range f.Order {
		if id == from {
			f.Order[j] = to
		}
	}
	for j := range f.Groups {
		g := &f.Groups[j]
		for k, m := range g.Members {
			g.Members[k] = renamedRef(m, from, to)
		}
		for k, r := range g.Rules {
			g.Rules[k].Use = renamedRef(r.Use, from, to)
		}
		for k, m := range g.Fast {
			g.Fast[k] = renamedRef(m, from, to)
		}
		for k, m := range g.Off {
			g.Off[k] = renamedRef(m, from, to)
		}
		g.Weights = renamedWeights(g.Weights, func(m string) string { return renamedRef(m, from, to) })
		g.Classifier = renamedRef(g.Classifier, from, to)
		g.Pick = renamedRef(g.Pick, from, to)
	}
	// the vendor's list last fetched goes with it
	os.Rename(catalog.LivePath(from), catalog.LivePath(to))
	os.Rename(catalog.LivePath(decisionsID(from)), catalog.LivePath(decisionsID(to)))
	if err := store(f); err != nil {
		return err
	}
	s := settings.Load()
	changed := renameModelPrefs(&s, from, to)
	for a, names := range s.Visible {
		for k, n := range names {
			if strings.EqualFold(n, from) {
				s.Visible[a][k], changed = to, true
			}
		}
	}
	if changed {
		return settings.Save(s)
	}
	return nil
}

// renamedRef is a model id ("provider/model", "magpie/provider/model")
// with the provider from named to instead.
func renamedRef(ref, from, to string) string {
	pre, rest := "", ref
	if r, ok := strings.CutPrefix(ref, "magpie/"); ok {
		pre, rest = "magpie/", r
	}
	if m, ok := strings.CutPrefix(rest, from+"/"); ok {
		return pre + to + "/" + m
	}
	return ref
}

// canonicalModelSettings provides a read-only view of legacy plugin model
// preferences. It copies only changed maps and lists; canonical keys win.
func canonicalModelSettings(s settings.Settings) settings.Settings {
	out, _ := remapModelSettings(s, heldOf("legacy-plugin-aliases", LegacyPluginAliases))
	return out
}

func remapModelSettings(s settings.Settings, aliases map[string]string) (settings.Settings, bool) {
	if len(aliases) == 0 {
		return s, false
	}
	changed := false
	ref := func(id string) string {
		for old, now := range aliases {
			if id == old {
				return now
			}
			if next := renamedRef(id, old, now); next != id {
				return next
			}
			if rest, ok := strings.CutPrefix(id, old+"|"); ok {
				return now + "|" + rest
			}
		}
		return id
	}
	settings.PerModelKeys(&s, func(_ string, m reflect.Value) {
		var copied reflect.Value
		for _, k := range m.MapKeys() {
			next := ref(k.String())
			if next == k.String() {
				continue
			}
			if !copied.IsValid() {
				copied = reflect.MakeMapWithSize(m.Type(), m.Len())
				iter := m.MapRange()
				for iter.Next() {
					copied.SetMapIndex(iter.Key(), iter.Value())
				}
			}
			nk := reflect.ValueOf(next).Convert(m.Type().Key())
			if !copied.MapIndex(nk).IsValid() {
				copied.SetMapIndex(nk, m.MapIndex(k))
			}
			copied.SetMapIndex(k, reflect.Value{})
		}
		if copied.IsValid() {
			m.Set(copied)
			changed = true
		}
	})
	list := func(in []string) []string {
		var out []string
		moved := false
		for _, id := range in {
			next := ref(id)
			moved = moved || next != id
			if !slices.Contains(out, next) {
				out = append(out, next)
			}
		}
		if moved {
			changed = true
			return out
		}
		return in
	}
	for _, field := range []*map[string][]string{&s.HiddenModels, &s.PickedModels, &s.OrderedModels, &s.FastPicks, &s.Visible} {
		var out map[string][]string
		for key, ids := range *field {
			next := list(ids)
			if !slices.Equal(ids, next) {
				if out == nil {
					out = make(map[string][]string, len(*field))
					for k, v := range *field {
						out[k] = v
					}
				}
				out[key] = next
			}
		}
		if out != nil {
			*field = out
		}
	}
	for _, field := range []*[]string{&s.DisabledModels, &s.UsageOrder, &s.PanelUsageHidden, &s.TrayUsages} {
		*field = list(*field)
	}
	for _, field := range []*string{&s.Vision, &s.ImageGen, &s.Searcher, &s.CodexTitles, &s.CodexDescriptions, &s.CodexPredictions, &s.CodexAutoReview, &s.CodexSubagentModel, &s.TrayUsage} {
		if next := ref(*field); next != *field {
			*field, changed = next, true
		}
	}
	return s, changed
}

// MigrateLegacyPluginSettings persists the safe alias supplied by
// LegacyPluginAliases. It never overwrites a canonical preference, and
// leaves actual custom providers using either id alone.
func MigrateLegacyPluginSettings(oldID, newID string) error {
	if LegacyPluginAliases()[oldID] != newID {
		return nil
	}
	s, err := settings.LoadChecked()
	if err != nil {
		return err
	}
	s, changed := remapModelSettings(s, map[string]string{oldID: newID})
	f, err := read()
	if err != nil {
		return err
	}
	refsChanged := false
	rename := func(id string) string {
		next := renamedRef(id, oldID, newID)
		refsChanged = refsChanged || next != id
		return next
	}
	for i := range f.Providers {
		for j, id := range f.Providers[i].Fallback {
			f.Providers[i].Fallback[j] = rename(id)
		}
	}
	for i := range f.Groups {
		g := &f.Groups[i]
		for _, ids := range [][]string{g.Members, g.Match, g.Fast, g.Off} {
			for j, id := range ids {
				ids[j] = rename(id)
			}
		}
		for j := range g.Rules {
			g.Rules[j].Use = rename(g.Rules[j].Use)
		}
		g.Pick, g.Classifier = rename(g.Pick), rename(g.Classifier)
		g.Weights = renamedWeights(g.Weights, rename)
	}
	if !changed && !refsChanged {
		return nil
	}
	return edit.Atomically(func() error {
		if changed {
			if err := settings.Save(s); err != nil {
				return err
			}
		}
		if refsChanged {
			return store(f)
		}
		catalog.Touched()
		return nil
	}, settings.Path(), Path())
}

// RenamedRef is ref with the provider it names by an id the provider had
// named by the id it has now.
func RenamedRef(ref string) string {
	for old, now := range Renamed() {
		if r := renamedRef(ref, old, now); r != ref {
			return r
		}
	}
	return ref
}

// Renamed maps the ids providers had to the ones they have now.
func Renamed() map[string]string {
	out := map[string]string{}
	for _, p := range load().Providers {
		for _, w := range p.Was {
			out[w] = p.ID
		}
	}
	return out
}

// OnAccountIDs are the user's own providers saved on a subscription's id,
// which hide that subscription once it is signed in (All lists the one
// saved): made before the id was a subscription's, as a WorkBuddy key was
// before WorkBuddy's plan was one. MoveOffAccountIDs moves them.
func OnAccountIDs() []string {
	var out []string
	for _, p := range load().Providers {
		if slices.Contains(accountIDs, p.ID) && hasEndpoint(p) {
			out = append(out, p.ID)
		}
	}
	return out
}

// FreeID is id, or id-2, id-3… whichever no provider or subscription has.
func FreeID(id string) string { return freeID(id) }

// hasEndpoint: a provider of the user's, not a subscription's model picks.
// Vertex AI's has none of the four: it is asked at its project's address.
func hasEndpoint(p Provider) bool {
	return p.Chat != "" || p.Responses != "" || p.Anthropic != "" || p.Gemini != "" || p.Decide != "" || p.IsVertex()
}
