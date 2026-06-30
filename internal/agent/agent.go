// Package agent abstracts the coding-agent CLI that a relay session launches.
// Each adapter knows how to resolve its binary, prepare any per-worktree
// context files, and build the launch argv. The launcher and CLI route every
// launch through this interface so new agents can be added without touching
// the launch path.
package agent

// LaunchOptions carries everything an adapter needs to launch a session.
type LaunchOptions struct {
	Worktree        string
	ProjectDir      string // ~/.relay/projects/active/<slug> — project metadata dir, outside the worktree
	SystemPrompt    string // project context line (e.g. "Active relay project: x. Phase: plan.")
	SessionName     string // e.g. "relay:<slug>"
	Command         string // command/skill name, e.g. "plan"
	CommandArgs     string // args appended to the command, e.g. the slug
	SkipPermissions bool
}

// Agent abstracts one coding-agent CLI.
type Agent interface {
	Name() string
	Lookup() (string, error)             // resolve the binary in PATH
	Prepare(o LaunchOptions) error       // write any context files into the worktree; no-op when the agent has a system-prompt flag
	LaunchArgs(o LaunchOptions) []string // argv AFTER argv[0]
	Capabilities() Capabilities          // what this agent supports; drives package generation
}
