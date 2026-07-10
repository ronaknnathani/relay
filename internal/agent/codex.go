package agent

import (
	"fmt"
	"os/exec"
)

// codex is the adapter for the OpenAI Codex CLI. Codex loads skills from
// ~/.codex/skills and project context from AGENTS.md, so launch uses a prose
// prompt that names the relay skill and lets Codex trigger it.
type codex struct{}

func (codex) Name() string { return "codex" }

func (codex) Lookup() (string, error) {
	path, err := exec.LookPath("codex")
	if err != nil {
		return "", fmt.Errorf("codex not found in PATH: %w", err)
	}
	return path, nil
}

// Prepare writes the project context into <worktree>/AGENTS.md, which Codex
// loads as repository guidance.
func (codex) Prepare(o LaunchOptions) error {
	return prepareAgentsMD(o)
}

// Capabilities reports Codex's values: Codex-native skills, subagent-capable
// delegation, prose skill invocation, context delivered via AGENTS.md, and
// neutralized names for compound Claude-only tools that appear in prose.
func (codex) Capabilities() Capabilities {
	return Capabilities{
		Subagents:          SubagentToml,
		LargeContext:       true,
		DeterministicSlash: false,
		LifecycleHook:      HookNone,
		ContextInjection:   ContextFile,
		ToolNames: ToolNameMap{
			"WebFetch":        "web search",
			"AskUserQuestion": "ask the user",
		},
	}
}

func (codex) LaunchArgs(o LaunchOptions) []string {
	args := []string{
		"-C", o.Worktree,
	}
	if o.ProjectDir != "" {
		args = append(args, "--add-dir", o.ProjectDir)
	}
	switch resolvePermissionMode(codex{}, o.PermissionMode) {
	case "auto":
		args = append(args, "--sandbox", "workspace-write", "--ask-for-approval", "never")
	case "prompt":
		args = append(args, "--sandbox", "workspace-write", "--ask-for-approval", "on-request")
	case "bypass":
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	args = append(args, relaySkillPrompt(o))
	return args
}

// PermissionModes lists Codex permission modes; "auto" (the default) grants
// workspace writes without asking, "prompt" asks on request, and "bypass" runs
// without Codex sandboxing or approvals.
func (codex) PermissionModes() []string { return []string{"auto", "prompt", "bypass"} }
