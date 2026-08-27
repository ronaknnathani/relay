package programview

import (
	"os/exec"

	"github.com/ronaknnathani/relay/internal/herdr"
)

// NewHerdrAgentLister returns the current Herdr client when Herdr is installed.
func NewHerdrAgentLister() AgentLister {
	if _, err := exec.LookPath("herdr"); err != nil {
		return nil
	}
	return herdr.NewClient()
}
