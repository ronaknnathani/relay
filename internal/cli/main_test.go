package cli

import (
	"os"
	"testing"
)

// TestMain isolates the suite from the developer's own Herdr session so managed
// program and managed child tests exercise the readiness gate deterministically
// instead of inheriting a live workspace.
func TestMain(m *testing.M) {
	for _, name := range []string{"HERDR_ENV", "HERDR_WORKSPACE_ID"} {
		if err := os.Unsetenv(name); err != nil {
			panic(err)
		}
	}
	herdrAvailable = func() bool { return false }
	newHerdrClient = func() herdrRuntimeClient {
		panic("test used the real Herdr client; install managed Herdr fakes first")
	}
	os.Exit(m.Run())
}
