// Package launcher hands off to a coding-agent CLI via syscall.Exec.
package launcher

import (
	"fmt"
	"os"
	"syscall"

	"github.com/ronaknnathani/relay/internal/agent"
)

// Launch replaces the current process with the agent's CLI running in the
// worktree. It resolves the binary, lets the agent write any per-worktree
// context files, changes into the worktree, then exec's. Returns only on
// failure (success exec's away). argv[0] is the agent name.
func Launch(a agent.Agent, o agent.LaunchOptions) error {
	bin, err := a.Lookup()
	if err != nil {
		return err
	}
	if err := a.Prepare(o); err != nil {
		return fmt.Errorf("prepare %s: %w", a.Name(), err)
	}
	if err := os.Chdir(o.Worktree); err != nil {
		return fmt.Errorf("chdir to worktree %s: %w", o.Worktree, err)
	}
	argv := append([]string{a.Name()}, a.LaunchArgs(o)...)
	if err := syscall.Exec(bin, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", a.Name(), err)
	}
	return nil
}
