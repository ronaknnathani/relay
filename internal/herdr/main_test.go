package herdr

import (
	"os"
	"testing"
)

// Tests must not see the Herdr frontend the developer runs them from.
func TestMain(m *testing.M) {
	for _, name := range []string{"HERDR_ENV", "HERDR_WORKSPACE_ID", "HC_TERMINAL_ID"} {
		if err := os.Unsetenv(name); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}
