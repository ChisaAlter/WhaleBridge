package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/agentenv"
	"github.com/ChisaAlter/WhaleBridge/internal/gwmw"
	"github.com/ChisaAlter/WhaleBridge/internal/masking"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/routes"
	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
)

// standInsEnv names the folder of a test binary's stand-in CLIs, for a
// test binary it starts again.
const standInsEnv = "MAGPIE_GATEWAY_TEST_STAND_INS"

// Upstream routing_test.go's isolated protocol-fixture contract: a fake
// inference server does not implement a vendor's allowance APIs. Tests of
// quota admission install the allowance reader they need themselves.
func init() {
	allowances = func(string) map[string]provider.Allowance { return nil }
	keyAllowance = func(provider.Provider) (provider.Allowance, bool) { return nil, false }
}

func TestMain(m *testing.M) {
	code, err := isolatedTests(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

// Isolate every process, including the cold-start redaction tests. HOME
// alone does not isolate the macOS Keychain, and account caches eventually
// expire: discovery must keep finding only the test's sign-ins and CLIs.
var builtinMaskerRemove func()
var routeBuiltinStop func()

func withoutBuiltinRoutes(t *testing.T) {
	t.Helper()
	routeBuiltinStop()
	t.Cleanup(func() { routeBuiltinStop = routes.RegisterBuiltins() })
}

func withoutBuiltinMasker(t *testing.T) {
	t.Helper()
	builtinMaskerRemove()
	t.Cleanup(func() { builtinMaskerRemove = gwmw.Mask(masking.Masker{}) })
}

func isolatedTests(m *testing.M) (int, error) {
	routeBuiltinStop = routes.RegisterBuiltins()
	defer func() { routeBuiltinStop() }()
	builtinMaskerRemove = gwmw.Mask(masking.Masker{})
	defer func() { builtinMaskerRemove() }()
	home, err := os.MkdirTemp("", "magpie-gateway-test-")
	if err != nil {
		return 1, err
	}
	defer os.RemoveAll(home)
	for name, value := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
		"APPDATA":         filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA":    filepath.Join(home, "AppData", "Local"),
	} {
		if err := os.Setenv(name, value); err != nil {
			return 1, err
		}
	}
	// The package isolates itself rather than through testenv.Run, so it
	// says so as testenv does: what reads the marker (a CLI identity kept
	// on disk, provider.keepsIdentities; the machine's own folders,
	// proc.SystemDirs) then acts as in any isolated test binary. Without it
	// an ask still going when its test ended wrote cli-identity.json into
	// the next test's folder as that was removed (#1524, again after
	// 9c377231 dropped this package's own switch).
	if err := os.Setenv(testenv.Marker, home); err != nil {
		return 1, err
	}
	for _, name := range agentenv.Vars {
		if err := os.Unsetenv(name); err != nil {
			return 1, err
		}
	}
	// The machine's proxy variables too: net/http freezes them into its
	// first routed request, so a test that sets its own afterwards (search's
	// Auto, by ALL_PROXY alone) reads the machine's instead, and its
	// searcher goes out through a proxy that cannot reach the test's
	// servers.
	testenv.ClearProxy()
	// The fakes of ChatGPT's backend read turns plain; codex_zstd_test's
	// send them zstd, as the real one is sent.
	provider.CodexZstdForTests(false)
	// Finding an inert CLI also prevents discovery from falling back to
	// one installed at an absolute system path. Tests can prepend their
	// own fakes, as fakeClaude does, and still use ordinary shell tools.
	// A test binary this one starts again (the cold-start redaction tests)
	// runs the stand-ins its parent made: on macOS a newly written program
	// waits for the system's first-run check, and under a full suite's load
	// a child's account discovery spent its whole 20s on new stand-ins.
	bin := os.Getenv(standInsEnv)
	if bin == "" {
		bin = filepath.Join(home, "bin")
		if err := os.Mkdir(bin, 0o755); err != nil {
			return 1, err
		}
		if err := testenv.StandIns(bin, []string{"security", "secret-tool", "claude", "codex", "cursor-agent", "devin", "grok", "kiro-cli"}); err != nil {
			return 1, err
		}
		defer testenv.RemovePrograms()
		if err := os.Setenv(standInsEnv, bin); err != nil {
			return 1, err
		}
	}
	if err := os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return 1, err
	}
	// no route is written to disk behind a test's back; the history's own
	// tests call saveRoute themselves
	keepRoutes = false
	// no test asks a vendor: a provider with a real base URL and a made-up
	// key would, in the background (a plan key's windows, #1016), and a
	// plugin host's first start fetched models.dev
	testenv.Offline()
	return m.Run(), nil
}

// The package's tests run as an isolated test binary: nothing the marker
// turns off (a CLI identity kept on disk, #1524) runs behind a test's back.
func TestRunsAsAnIsolatedTestBinary(t *testing.T) {
	if os.Getenv(testenv.Marker) == "" {
		t.Fatalf("TestMain left %s unset", testenv.Marker)
	}
}
