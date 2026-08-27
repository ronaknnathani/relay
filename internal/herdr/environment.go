package herdr

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

var lookPath = exec.LookPath

// Available reports whether the Herdr CLI is installed.
func Available() bool {
	_, err := lookPath("herdr")
	return err == nil
}

// RequireEnvironment verifies Relay is running inside a Herdr-managed pane and
// returns the current workspace ID. Callers guarding managed sessions use
// Require instead, which also verifies the binary and the running server.
func RequireEnvironment() (string, error) {
	if os.Getenv("HERDR_ENV") != "1" {
		return "", errors.New("managed Relay commands require HERDR_ENV=1; run this command from a Herdr-managed pane")
	}
	workspaceID := strings.TrimSpace(os.Getenv("HERDR_WORKSPACE_ID"))
	if workspaceID == "" {
		return "", errors.New("managed Relay commands require HERDR_WORKSPACE_ID; run this command from a Herdr-managed pane with caller context")
	}
	return workspaceID, nil
}
