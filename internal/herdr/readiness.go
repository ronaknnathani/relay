package herdr

import (
	"fmt"
	"strings"
)

// Requirement describes what one managed Relay command needs from Herdr.
// Managed programs and managed child sessions have no non-Herdr fallback, so
// every unmet requirement fails closed with setup or start instructions.
type Requirement struct {
	// Command names the Relay command being guarded, for actionable errors.
	Command string
	// Topology requires an owning Herdr workspace because the command creates
	// or controls tabs and panes.
	Topology bool
	// Available reports whether the herdr binary is installed. It defaults to
	// the installed-binary lookup.
	Available func() bool
	// Agents lists live Herdr agents. It defaults to the installed CLI client.
	Agents func() ([]Agent, error)
}

// Readiness is the verified Herdr context for one managed Relay command.
type Readiness struct {
	WorkspaceID string
	// Agents is the agent list observed while verifying the server, so callers
	// reuse it instead of running `herdr agent list` twice.
	Agents []Agent
}

// RequireContext verifies the Herdr binary and workspace topology without
// contacting the Herdr server, so callers can fail early before taking locks.
func RequireContext(requirement Requirement) (string, error) {
	command := requirementCommand(requirement)
	available := requirement.Available
	if available == nil {
		available = Available
	}
	if !available() {
		return "", fmt.Errorf(
			"%s requires Herdr, but the herdr binary is not on PATH; "+
				"install Herdr, approve its Copilot or Claude integration, start the Herdr server, then retry",
			command,
		)
	}
	if !requirement.Topology {
		return "", nil
	}
	workspaceID, err := RequireEnvironment()
	if err != nil {
		return "", fmt.Errorf(
			"%s requires a Herdr-managed pane: %w; start or attach Herdr and run %s from a Herdr pane",
			command, err, command,
		)
	}
	return workspaceID, nil
}

func requirementCommand(requirement Requirement) string {
	command := strings.TrimSpace(requirement.Command)
	if command == "" {
		return "this managed Relay command"
	}
	return command
}

// Require verifies the Herdr binary, workspace topology, and running server
// before a managed Relay session starts or resumes.
func Require(requirement Requirement) (Readiness, error) {
	command := requirementCommand(requirement)
	workspaceID, err := RequireContext(requirement)
	if err != nil {
		return Readiness{}, err
	}
	readiness := Readiness{WorkspaceID: workspaceID}
	agents := requirement.Agents
	if agents == nil {
		agents = NewClient().Agents
	}
	live, err := agents()
	if err != nil {
		return Readiness{}, fmt.Errorf(
			"%s requires a running Herdr server, but listing agents failed: %w; "+
				"start or attach Herdr, verify it with `herdr agent list`, then retry",
			command, err,
		)
	}
	readiness.Agents = live
	return readiness, nil
}
