package agent

import (
	"fmt"
	"os/exec"
	"strings"
)

// copilot is the adapter for the GitHub Copilot CLI. Copilot auto-invokes
// skills weakly, so it launches with a prose prompt that names the skill and
// carries project context.
type copilot struct{}

func (copilot) Name() string { return "copilot" }

func (copilot) Lookup() (string, error) {
	path, err := exec.LookPath("copilot")
	if err != nil {
		return "", fmt.Errorf("copilot not found in PATH: %w", err)
	}
	return path, nil
}

// Prepare is a no-op: Copilot receives relay context in the initial -i prompt.
func (copilot) Prepare(LaunchOptions) error { return nil }

// Capabilities reports Copilot's real values: task-based subagents, a
// long-context tier, bounded noninteractive turns, prose (not
// deterministic-slash) invocation, no lifecycle hook, context delivered in the
// initial prompt, and the Claude→Copilot lowercase tool-name map. (Permission
// handling is mode-driven; see PermissionModes.)
func (copilot) Capabilities() Capabilities {
	return Capabilities{
		Subagents:          SubagentTask,
		LargeContext:       true,
		NamedSessions:      true,
		HeadlessTurn:       true,
		DeterministicSlash: false,
		LifecycleHook:      HookNone,
		ContextInjection:   ContextPrompt,
		ToolNames: ToolNameMap{
			"Bash":            "bash",
			"Read":            "view",
			"Write":           "create",
			"Edit":            "edit",
			"Glob":            "glob",
			"Grep":            "grep",
			"Agent":           "task",
			"WebFetch":        "web_fetch",
			"AskUserQuestion": "ask_user",
		},
	}
}

func (copilot) LaunchArgs(o LaunchOptions) []string {
	args := []string{
		"-C", o.Worktree,
		"-n", o.SessionName,
	}
	// Grant the file tools access to the project metadata dir, which lives
	// outside the worktree (Copilot's view/edit are sandboxed to -C otherwise).
	if o.ProjectDir != "" {
		args = append(args, "--add-dir", o.ProjectDir)
	}
	args = append(args, "--context", "long_context")
	if resolvePermissionMode(copilot{}, o.PermissionMode) == "allow-all" {
		args = append(args, "--allow-all")
	}
	// "prompt" mode: omit the allow-all flags so Copilot asks before acting.
	prompt := promptWithGoal(o.WorkflowGoal, relaySkillPrompt(o))
	args = append(args, "-i", prompt)
	return args
}

func relaySkillPrompt(o LaunchOptions) string {
	prompt := fmt.Sprintf("Run the relay %q skill", o.Command)
	if o.CommandArgs != "" {
		prompt += " for slug " + o.CommandArgs
	}
	prompt += "."
	if strings.TrimSpace(o.SystemPrompt) != "" {
		prompt += "\n\nContext:\n" + o.SystemPrompt
	}
	return prompt
}

// PermissionModes lists Copilot's permission modes; "allow-all" (the default)
// grants all permissions without prompting, "prompt" leaves Copilot's normal
// asks on.
func (copilot) PermissionModes() []string { return []string{"allow-all", "prompt"} }

// HeadlessTurnArgs builds one bounded noninteractive Copilot turn in a fresh
// session. The configured permission mode is deliberately not consulted: a
// headless turn cannot answer a permission ask, so it always runs --allow-all
// in the managed program directories Relay already trusts.
func (copilot) HeadlessTurnArgs(o HeadlessTurnOptions) []string {
	args := []string{"-C", o.Repo}
	if o.ProgramDir != "" {
		args = append(args, "--add-dir", o.ProgramDir)
	}
	args = append(args,
		"--context", "long_context",
		"--allow-all",
		"--session-id", o.SessionID,
		"-p", o.Prompt,
		"--silent",
	)
	return args
}
