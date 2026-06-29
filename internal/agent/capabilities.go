package agent

// Capabilities describes what a coding agent supports. The generator reads it
// to render the best mechanism each agent has (progressive enhancement) rather
// than flattening every agent to the weakest one. See the harness design §D.
type Capabilities struct {
	Subagents          SubagentSupport // how subagents are spawned
	LargeContext       bool            // a long-context model/tier is available
	DeterministicSlash bool            // slash-with-args fires reliably (vs prose-named skill)
	LifecycleHook      HookKind        // how the per-session context line is injected at lifecycle events
	ContextInjection   ContextKind     // how project context reaches the agent
	ToolNames          ToolNameMap     // canonical tool name → this agent's name
	PermissionFlag     string          // flag that skips permission prompts
}

// SubagentSupport is the mechanism an agent uses to run work in a child context.
type SubagentSupport int

const (
	// SubagentNone means no subagent mechanism; work runs inline.
	SubagentNone SubagentSupport = iota
	// SubagentTask is Claude's Task tool with a per-subagent model.
	SubagentTask
	// SubagentToml is Codex's TOML-defined agents.
	SubagentToml
	// SubagentChildSession is opencode's mode:subagent child session.
	SubagentChildSession
	// SubagentFleet is Copilot's task/fleet parallelism.
	SubagentFleet
)

// HookKind is how an agent runs the per-session context line.
type HookKind int

const (
	// HookNone means no lifecycle hook is available.
	HookNone HookKind = iota
	// HookCommand runs a command at SessionStart.
	HookCommand
	// HookFile uses an always-on rule file instead of a command hook.
	HookFile
)

// ContextKind is how project context is delivered to an agent.
type ContextKind int

const (
	// ContextFlag passes context via a launch flag (Claude --append-system-prompt).
	ContextFlag ContextKind = iota
	// ContextFile writes context into the worktree (AGENTS.md, etc.).
	ContextFile
)

// ToolNameMap maps a canonical tool name to the agent's own name for it.
// An absent key means the canonical name is used unchanged.
type ToolNameMap map[string]string

// Name resolves a canonical tool name to this agent's name, falling back to the
// canonical name when unmapped.
func (m ToolNameMap) Name(canonical string) string {
	if got, ok := m[canonical]; ok {
		return got
	}
	return canonical
}
