package generate

import (
	"regexp"

	"github.com/ronaknnathani/relay/internal/agent"
)

// subagentDirective matches the neutral subagent directive in a workflow body:
//
//	{{subagent:large_context}}  or  {{subagent:fast}}  or  {{subagent}}
//
// It carries intent plus a capability requirement (a model tier), not
// weakest-agent mechanics. The generator rewrites it to the best mechanism the
// target agent supports.
var subagentDirective = regexp.MustCompile(`\{\{subagent(?::(large_context|fast))?\}\}`)

// renderBody rewrites the agent-neutral directives in a workflow body for the
// given capabilities. Bodies with no directives are returned unchanged.
func renderBody(body []byte, caps agent.Capabilities) []byte {
	return subagentDirective.ReplaceAllFunc(body, func(m []byte) []byte {
		tier := string(subagentDirective.FindSubmatch(m)[1])
		return []byte(renderSubagent(caps, tier))
	})
}

// renderSubagent renders the subagent directive into the mechanism the agent
// supports. The inline fallback is used ONLY for agents that genuinely lack a
// subagent mechanism — never as a blanket downgrade of a capable agent.
//
// tier is "large_context" (1M-context model), "fast" (a cheap/fast model), or
// "" (the agent's default model).
func renderSubagent(caps agent.Capabilities, tier string) string {
	if caps.Subagents != agent.SubagentTask {
		return "Run the following inline (no subagent available)"
	}
	// The Agent-tool name distinguishes Claude (unmapped → "Agent", per-subagent
	// model) from a task-style agent like Copilot ("task"). Task-style agents have
	// no per-subagent model knob; a large-context tier is engaged at the session
	// level by the adapter's launch flags (e.g. Copilot's `--context long_context`),
	// not on the in-session task tool — so the directive renders just the mechanism.
	if toolName := caps.ToolNames.Name("Agent"); toolName != "Agent" {
		return "Launch a subagent (" + toolName + " tool)"
	}
	// Claude: Task tool with an explicit per-subagent model. A large-context
	// request maps to opus (1M) when the agent advertises LargeContext; a fast
	// request maps to haiku.
	model := "default"
	switch tier {
	case "large_context":
		if caps.LargeContext {
			model = "opus"
		}
	case "fast":
		model = "haiku"
	}
	return `Launch a subagent (Agent tool, model: "` + model + `")`
}
