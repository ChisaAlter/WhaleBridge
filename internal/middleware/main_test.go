package middleware

import (
	"os"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
)

// The tests run in a home of their own, never the user's real files.
func TestMain(m *testing.M) {
	os.Exit(testenv.RunIn(m, func(string) {
		testenv.Offline()
		os.Unsetenv("LAUNCHER_COMPONENT_DATA_DIR")
	}))
}
