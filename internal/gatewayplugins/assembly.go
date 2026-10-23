package gatewayplugins

import (
	"path/filepath"

	"github.com/ChisaAlter/WhaleBridge/internal/gwmw"
	"github.com/ChisaAlter/WhaleBridge/internal/masking"
	"github.com/ChisaAlter/WhaleBridge/internal/middleware"
	"github.com/ChisaAlter/WhaleBridge/internal/pluginrt"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/routes"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
	"github.com/ChisaAlter/WhaleBridge/internal/usage"
)

// Manifests is the full producer list for this component. It intentionally has
// no upstream GUI, LAN listener, native account switcher or blanket plugins/all.
func Manifests() []pluginrt.Manifest {
	return []pluginrt.Manifest{
		{ID: "rotation", API: "^1", Requires: []string{"gateway", "providers"}, Essential: true,
			InUse: func() (int, error) {
				names, err := provider.Routings()
				if err != nil {
					return 0, err
				}
				n := 0
				for _, name := range names {
					if _, ok := routes.Builtin[name]; ok {
						n++
					}
				}
				return n, nil
			},
			Apply: func(c *pluginrt.Ctx) error { c.OnRetire(routes.RegisterBuiltins()); return nil }},
		{ID: "redact", API: "^1", Requires: []string{"gateway", "settings"}, Essential: true,
			InUse: func() (int, error) {
				s, err := settings.LoadChecked()
				if err != nil {
					return 0, err
				}
				if masking.Wanted(s) {
					return 1, nil
				}
				return 0, nil
			},
			Apply: func(c *pluginrt.Ctx) error {
				masking.SetKeyPath(filepath.Join(settings.Dir(), "redact.key"))
				c.OnRetire(gwmw.Mask(masking.Masker{}))
				return nil
			}},
		{ID: "middleware", API: "^1", Requires: []string{"gateway"}, Essential: true,
			InUse: middleware.On,
			Apply: func(c *pluginrt.Ctx) error {
				if err := middleware.Refresh(); err != nil {
					return err
				}
				c.OnRetire(gwmw.Use(middleware.Chain{}))
				c.OnRetire(usage.Observe(middleware.Points()))
				return nil
			}},
		{ID: "otel", API: "^1", Requires: []string{"gateway", "settings"}, Essential: true,
			InUse: func() (int, error) {
				if _, err := settings.LoadChecked(); err != nil {
					return 0, err
				}
				s, err := settings.OTelExport()
				if err != nil {
					return 0, err
				}
				if s.Enabled {
					return 1, nil
				}
				return 0, nil
			},
			Apply: func(c *pluginrt.Ctx) error { c.OnDispose(usage.StartOTel()); return nil }},
	}
}
