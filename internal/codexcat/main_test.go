package codexcat

import (
	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
	"os"
	"testing"
)

// Every file and request belongs to the test process, never to a real account.
func TestMain(m *testing.M) {
	os.Exit(testenv.RunIn(m, func(string) { testenv.ClearProxy(); testenv.Offline() }))
}
