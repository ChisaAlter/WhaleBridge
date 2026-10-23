package provider

import (
	"context"
	"fmt"
	"log"
	"maps"
	"strings"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// whaleBridgeMoveAllowed checks the entire batch. An agent's own sign-in
// or a borrowed credential cannot be claimed by unattended maintenance.
// The explicit Move API remains the user's choice to hand one over.
func whaleBridgeMoveAllowed(id string, accounts []Moving) error {
	var native []string
	for _, a := range accounts {
		if a.Own || WhaleBridgeBorrowing(id, a.User) {
			native = append(native, a.User)
		}
	}
	if len(native) == 0 {
		return nil
	}
	users := strings.Join(native, ", ")
	return &moveError{
		why: MoveWhy{Code: "native", Args: map[string]string{"name": nameOf(id), "users": users}},
		msg: fmt.Sprintf("%s has native or borrowed accounts (%s); automatic migration skipped the entire batch. Choose Move explicitly to hand them to the plugin.", nameOf(id), users),
	}
}

// whaleBridgeOwnAccounts is kept as a small predicate for callers which
// only need to inspect eligibility; execution uses the diagnostic gate.
func whaleBridgeOwnAccounts(id string, accounts []Moving) bool {
	return whaleBridgeMoveAllowed(id, accounts) == nil
}

// WhaleBridgeHandOver adopts an installed provider plugin and transfers
// component-owned accounts. A mixed batch stays entirely at its source.
func WhaleBridgeHandOver(ctx context.Context) map[string]error {
	return handOverAccounts(ctx, true, whaleBridgeMoveAllowed)
}

func KeepWhaleBridgeRetiringMoved(ctx context.Context) {
	t := time.NewTimer(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if settings.AccountDiscoveryOff() {
			t.Reset(time.Hour)
			continue
		}
		keepMovedCurrent(ctx)
		moves := moveRetiring(ctx, whaleBridgeMoveAllowed)
		maps.Copy(moves, WhaleBridgeHandOver(ctx))
		for id, err := range moves {
			if err != nil {
				log.Printf("moving %s to its plugin: %s", id, err)
			} else {
				log.Printf("moved %s to its plugin, %s", id, MovePackage(id))
			}
		}
		t.Reset(nextRetiringRun())
	}
}
