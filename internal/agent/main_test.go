package agent

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
)

// TestMain gives the package a home of its own (testenv). A home an agent is
// found through can sit outside HOME — DSH_HOME does — and the package's
// tests sandbox HOME alone: left as the developer has it, a test that picks
// a model writes it into the real ~/.dsh/profiles, and dsh then refuses
// every turn with a model its provider does not list. internal/gateway,
// internal/provider and internal/usage isolate themselves the same way.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == DryRunArg {
		if err := DryRun(os.Args[2]); err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		os.Exit(0)
	}
	// whether Codex's ChatGPT account is out of its allowance is asked of
	// OpenAI; never from here
	codexUsedUp = func() bool { return false }
	// Sandboxed config writes must never write into the real OS keychain.
	zedCredential = func(string) error { return nil }
	// Pi's and omp's model registries are read from their installs; never
	// this machine's
	nodeModulesOf = func(string, []string) []string { return nil }
	// Aside's default is changed through the aside binary, which answers for
	// the account the developer is actually running: a test that picks a
	// model would change the real Aside's default, and the running one would
	// keep the old one, so the test would read back a file nothing had
	// applied. The Aside tests stand in for it themselves.
	asideSet = func(string, string) error { return errors.New("aside: no Aside in a test") }
	asideRead = func() (map[string]json.RawMessage, error) { return nil, errors.New("aside: no Aside in a test") }
	// an address an agent is pointed at is tried over the network (Drift,
	// #1013); never from a test, whose reach tests stand in for it
	reachProbe = func(string) bool { return true }
	os.Exit(testenv.Run(m))
}
