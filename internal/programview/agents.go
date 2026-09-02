package programview

import (
	"context"
	"os/exec"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
)

// NewHerdrAgentLister returns the current Herdr client when Herdr is installed.
func NewHerdrAgentLister() AgentLister {
	return NewHerdrAgentListerWithCommandTimeout(context.Background(), 0)
}

// NewHerdrAgentListerWithCommandTimeout returns a context-bound Herdr client
// when Herdr is installed. A positive timeout bounds each Herdr command.
func NewHerdrAgentListerWithCommandTimeout(
	ctx context.Context,
	timeout time.Duration,
) AgentLister {
	if _, err := exec.LookPath("herdr"); err != nil {
		return nil
	}
	return herdr.NewClientWithCommandTimeout(ctx, timeout)
}
