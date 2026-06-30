package agent

import (
	"fmt"
	"os/exec"
)

// claude is the reference adapter for the Claude Code CLI. It launches with a
// bare slash invocation (e.g. "/plan <slug>") matching the loose-skills install.
type claude struct{}

func (claude) Name() string { return "claude" }

func (claude) Lookup() (string, error) {
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude not found in PATH: %w", err)
	}
	return path, nil
}

// Prepare is a no-op: Claude receives context via --append-system-prompt.
func (claude) Prepare(LaunchOptions) error { return nil }

// Capabilities reports Claude's real values: Task-tool subagents with a 1M
// long-context model, deterministic slash invocation, no lifecycle hook,
// context via --append-system-prompt, and the native Claude tool names.
func (claude) Capabilities() Capabilities {
	return Capabilities{
		Subagents:          SubagentTask,
		LargeContext:       true,
		DeterministicSlash: true,
		LifecycleHook:      HookNone,
		ContextInjection:   ContextFlag,
		ToolNames:          nil, // canonical names are Claude's names
		PermissionFlag:     "--dangerously-skip-permissions",
	}
}

func (claude) LaunchArgs(o LaunchOptions) []string {
	var args []string
	if o.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	invocation := "/" + o.Command
	if o.CommandArgs != "" {
		invocation += " " + o.CommandArgs
	}
	args = append(args,
		"--append-system-prompt", o.SystemPrompt,
		"-n", o.SessionName,
		invocation,
	)
	return args
}
