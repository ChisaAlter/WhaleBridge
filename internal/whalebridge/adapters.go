package whalebridge

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/middleware"
	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

func latestWarm(times map[string]time.Time) *time.Time {
	var newest time.Time
	for _, at := range times {
		if at.After(newest) {
			newest = at
		}
	}
	if newest.IsZero() {
		return nil
	}
	return &newest
}

func setAdapterMirror(on bool) error {
	s := settings.Load()
	changed := s.ChinaMirror != on
	s.ChinaMirror = on
	if err := settings.Save(s); err != nil {
		return err
	}
	if changed {
		plugin.RefreshMarket()
	}
	return nil
}

func subscriptionAdapters(ctx context.Context) map[string]any {
	installed, available, moves := []map[string]any{}, []plugin.Listing{}, []map[string]any{}
	list := plugin.Load()
	es := list.Plugins
	ps := plugin.Cached()
	var providerErr error
	errs := map[string]string{}
	clashes := map[string][]plugin.Clash{}
	if len(es) > 0 && (plugin.Running() || plugin.HasBun()) {
		loaded, err := plugin.Plugins(ctx)
		if err != nil {
			providerErr = err
		}
		for _, p := range loaded {
			errs[p.Spec] = p.Error
		}
		clashes = plugin.Clashes(loaded)
	}
	names := []string{}
	for _, e := range es {
		if !plugin.IsPath(e.Spec) && !plugin.IsGit(e.Spec) {
			names = append(names, plugin.Name(e.Spec))
		}
	}
	known := plugin.InfoCached(names)
	middlewares := middleware.States()
	providerNames := map[string]string{}
	for _, p := range ps {
		providerNames[p.ID] = p.Name
	}
	for _, e := range es {
		ids := []string{}
		for _, p := range ps {
			if p.Spec == e.Spec {
				ids = append(ids, p.ID)
			}
		}
		target := plugin.Target(e.Spec)
		mwFile, mwOnly := plugin.Middleware(target)
		agentFile, agentOnly := plugin.Agent(target)
		kind := "provider"
		if len(ids) == 0 {
			switch {
			case mwFile != "":
				kind = "middleware"
			case agentFile != "":
				kind = "agent"
			}
		}
		row := map[string]any{"id": plugin.Name(e.Spec), "name": plugin.Name(e.Spec), "package": e.Spec, "version": plugin.Installed(e.Spec), "enabled": !e.Off, "providers": ids, "named": plugin.Answered(), "error": errs[e.Spec], "latest": known[plugin.Name(e.Spec)].Version, "moved": provider.MovedOnto(e.Spec), "optionsExample": plugin.OptionsExample(target), "kind": kind, "isMiddleware": mwFile != "", "middlewareOnly": mwOnly, "isAgent": agentFile != "", "agentOnly": agentOnly, "inMagpieOnly": plugin.InMagpieOnly(target)}
		if state, ok := middlewares[e.Spec]; ok {
			row["middleware"] = state
		}
		conflicts := []map[string]any{}
		for _, clash := range clashes[e.Spec] {
			name := providerNames[clash.ID]
			if name == "" {
				name = clash.ID
			}
			conflicts = append(conflicts, map[string]any{"id": clash.ID, "name": name, "by": clash.By, "with": clash.With})
		}
		row["clashes"] = conflicts
		if u, ok := plugin.LastUpdated(plugin.Name(e.Spec), time.Now().Add(-3*24*time.Hour)); ok && !plugin.IsPath(e.Spec) {
			row["autoUpdated"] = u
		}
		installed = append(installed, row)
	}
	for _, e := range plugin.Market(ctx) {
		available = append(available, e)
	}
	for _, id := range provider.MovableIDs() {
		m, _ := provider.MigrationOf(id)
		p, _ := provider.Find(id)
		moves = append(moves, map[string]any{"id": id, "package": provider.MovePackage(id), "moved": provider.Moved(id), "signedIn": p != nil && p.Account != nil, "state": m.State, "error": m.Err})
	}
	out := map[string]any{"installed": installed, "available": available, "moves": moves, "updates": plugin.PendingUpdates(), "bun": plugin.HasBun(), "bunVer": plugin.BunInUse(), "mirror": settings.Load().ChinaMirror, "prefer": list.Prefer}
	if providerErr != nil {
		out["error"] = providerErr.Error()
	}
	return out
}

func adapterDiscoveryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/subscription/adapters/search", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		hits, err := plugin.Search(ctx, r.URL.Query().Get("q"))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]any{"hits": hits})
	})
	mux.HandleFunc("GET /api/subscription/adapters/page", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		p, err := plugin.Readme(ctx, r.URL.Query().Get("name"))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, p)
	})
	mux.HandleFunc("GET /api/subscription/adapters/github", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"repos": plugin.TaggedRepos(r.Context()), "topic": plugin.Topic})
	})
	mux.HandleFunc("GET /api/subscription/adapters/npm", func(w http.ResponseWriter, r *http.Request) {
		names := []string{}
		for _, n := range strings.Split(r.URL.Query().Get("names"), ",") {
			if n = strings.TrimSpace(n); n != "" && len(names) < 100 {
				names = append(names, n)
			}
		}
		writeJSON(w, map[string]any{"npm": plugin.Info(r.Context(), names)})
	})
	mux.HandleFunc("POST /api/subscription/adapters/check", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		writeJSON(w, plugin.CheckNow(ctx))
	})
}
