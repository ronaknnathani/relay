// Package agent abstracts the coding-agent CLI that a relay session launches.
// Each adapter knows how to resolve its binary, prepare any per-worktree
// context files, and build the launch argv. The launcher and CLI route every
// launch through this interface so new agents can be added without touching
// the launch path.
package agent

// LaunchOptions carries everything an adapter needs to launch a session.
type LaunchOptions struct {
	Worktree     string
	ProjectDir   string // ~/.relay/projects/active/<slug> — project metadata dir, outside the worktree
	SystemPrompt string // project context line (e.g. "Active relay project: x. Phase: plan.")
	SessionName  string // e.g. "relay:<slug>"
	Command      string // command/skill name, e.g. "plan"
	CommandArgs  string // args appended to the command, e.g. the slug
	// PermissionMode selects how the agent handles permission prompts. Valid
	// values are agent-specific (see Agent.PermissionModes); an empty or
	// unrecognized value resolves to the agent's default mode.
	PermissionMode string
}

// Agent abstracts one coding-agent CLI.
type Agent interface {
	Name() string
	Lookup() (string, error)             // resolve the binary in PATH
	Prepare(o LaunchOptions) error       // write any context files into the worktree; no-op when the agent has a system-prompt flag
	LaunchArgs(o LaunchOptions) []string // argv AFTER argv[0]
	Capabilities() Capabilities          // what this agent supports; drives package generation
	// PermissionModes lists the permission modes this agent supports; the first
	// is the default used when none is configured.
	PermissionModes() []string
}

// resolvePermissionMode returns mode when the agent supports it, otherwise the
// agent's default (the first of PermissionModes). This lets an empty config
// value, or one written for a different agent, fall back sanely.
func resolvePermissionMode(a Agent, mode string) string {
	modes := a.PermissionModes()
	for _, m := range modes {
		if m == mode {
			return mode
		}
	}
	if len(modes) > 0 {
		return modes[0]
	}
	return ""
}
