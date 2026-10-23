package gateway

import (
	"context"
	"sync"

	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// StartWhaleBridgeSubscriptions runs the gateway's subscription maintenance.
// User-selected warmups, resets and check-ins use this component's settings;
// native client account switching and client integrations are not started.
func StartWhaleBridgeSubscriptions(parent context.Context) func() {
	ctx, cancel := context.WithCancel(parent)
	var workers sync.WaitGroup
	for _, run := range []func(context.Context){
		provider.KeepLoginsAlive, provider.KeepCodexWindowsWarm,
		func(ctx context.Context) { provider.KeepClaudeWindowsWarm(ctx, warmClaude) },
		provider.KeepResetsFromRunningOut, provider.KeepWorkBuddyCheckedIn,
		provider.KeepTraeCheckedIn, provider.KeepMiniMaxCheckedIn,
		provider.KeepQoderCheckedIn, provider.KeepPluginsCheckedIn,
		provider.KeepWhaleBridgeRetiringMoved, provider.KeepPluginsListed,
		func(ctx context.Context) {
			provider.WatchQuotas(ctx, whaleBridgeQuotaWake, recordWhaleBridgeQuotaAlert)
		},
		plugin.KeepUpdated, plugin.KeepBunUpdated,
	} {
		workers.Add(1)
		go func() { defer workers.Done(); run(ctx) }()
	}
	var once sync.Once
	return func() { once.Do(func() { cancel(); workers.Wait() }) }
}

// CloseWhaleBridgeSessions releases the component's parked/idle Claude workers
// only after accepted HTTP requests have drained. Their output readers and
// process reapers finish before plugin resources are withdrawn.
func (s *Server) CloseWhaleBridgeSessions() {
	b := s.subscription
	if b == nil {
		return
	}
	b.mu.Lock()
	runs := make([]*subscriptionRun, 0, len(b.runs))
	for _, r := range b.runs {
		runs = append(runs, r)
	}
	shelf := make([]*savedSession, 0, len(b.shelf))
	for _, item := range b.shelf {
		shelf = append(shelf, item)
	}
	b.shelf = make(map[string]*savedSession)
	b.mu.Unlock()
	b.abortAll()
	for _, r := range runs {
		r.mu.Lock()
		done := r.exited
		r.mu.Unlock()
		if done != nil {
			<-done
		}
	}
	b.reapers.Wait()
	for _, item := range shelf {
		item.discard()
	}
}
