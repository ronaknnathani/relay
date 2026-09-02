package cli

import (
	"fmt"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/herdr"
)

// Managed Relay programs and managed child sessions run only under Herdr.
// requireHerdrRuntime is the single readiness gate every managed entry point
// calls before it creates, resumes, dispatches, or observes managed work.
func requireHerdrRuntime(command string, topology bool) (herdr.Readiness, error) {
	return herdr.Require(herdr.Requirement{
		Command:   command,
		Topology:  topology,
		Available: herdrAvailable,
		Agents:    func() ([]herdr.Agent, error) { return newHerdrClient().Agents() },
	})
}

// requireManagedAgent verifies the agent has an approved Herdr integration that
// carries Relay's session identity, which managed sessions depend on for owner
// discovery and tech lead doorbells.
func requireManagedAgent(agentName, subject string) error {
	a, err := agent.Get(agentName)
	if err != nil {
		return fmt.Errorf("validate Herdr integration for %s: %w", subject, err)
	}
	if a.Capabilities().NamedSessions {
		return nil
	}
	return fmt.Errorf(
		"%s uses %s, whose launch adapter cannot carry named sessions; "+
			"managed Relay sessions require an approved Herdr integration—use copilot or claude",
		subject, a.Name(),
	)
}

// requireHerdrPane verifies the Herdr binary and the owning Herdr pane without
// contacting the Herdr server, so commands can fail early before taking locks.
func requireHerdrPane(command string) (string, error) {
	return herdr.RequireContext(herdr.Requirement{
		Command:   command,
		Topology:  true,
		Available: herdrAvailable,
	})
}

// requireManagedHerdr runs both managed-session gates in one place.
func requireManagedHerdr(command, agentName, subject string, topology bool) (herdr.Readiness, error) {
	if err := requireManagedAgent(agentName, subject); err != nil {
		return herdr.Readiness{}, err
	}
	return requireHerdrRuntime(command, topology)
}
