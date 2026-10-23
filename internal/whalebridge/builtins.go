package whalebridge

import (
	"github.com/ChisaAlter/WhaleBridge/internal/budget"
	"github.com/ChisaAlter/WhaleBridge/internal/gwbudget"
	"github.com/ChisaAlter/WhaleBridge/internal/gwmw"
	"github.com/ChisaAlter/WhaleBridge/internal/gwtool"
	"github.com/ChisaAlter/WhaleBridge/internal/masking"
	"github.com/ChisaAlter/WhaleBridge/internal/middleware"
	"github.com/ChisaAlter/WhaleBridge/internal/otlp"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/routes"
	"github.com/ChisaAlter/WhaleBridge/internal/usage"
)

// serveBuiltins turns on the gateway capabilities upstream applies through its
// built-in plugins (plugins/*, pluginrt). The component has no plugin page, so
// each is always on, as it was while it belonged to the gateway itself. Each
// one still acts only on what Settings and the providers ask for.
func serveBuiltins() (stop func()) {
	var stops []func()
	keep := func(remove func()) { stops = append(stops, remove) }
	// plugins/groups: routing groups
	keep(provider.ServeGroups())
	// plugins/rotation: In order, In turn, Least used first, Weekly pace, By weight
	for name, s := range routes.Builtin {
		keep(routes.Register(name, s))
	}
	// plugins/redact: Settings › Privacy masking
	keep(gwmw.Mask(masking.Masker{}))
	// plugins/search: web search for models that cannot search
	keep(gwtool.Serve(gwtool.WebSearch))
	// plugins/tools: the tools installed plugins give
	keep(gwtool.Serve(gwtool.PluginTools))
	// plugins/middleware: JS gateway middleware, routings and usage observers
	keep(gwmw.Use(middleware.Chain{}))
	keep(usage.Observe(middleware.Points()))
	// plugins/budget: limits on gateway keys
	keep(gwbudget.Limit(budget.Limiter{}))
	// plugins/otel: OTLP export, sent only while Settings enable it
	keep(usage.Observe(otlp.New()))
	// the observers serve with this process's gateway (Server.ListenAndServe
	// upstream; the component serves its own listener)
	keep(usage.ServeObservers())
	return func() {
		for i := len(stops) - 1; i >= 0; i-- {
			stops[i]()
		}
	}
}
