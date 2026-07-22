package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ronaknnathani/relay/internal/agentsmd"
)

// copilot is the adapter for the GitHub Copilot CLI. Copilot auto-invokes
// skills weakly, so it launches with a prose prompt that names the skill, with
// project context delivered through an AGENTS.md file (Copilot has no
// system-prompt flag).
type copilot struct{}

func (copilot) Name() string { return "copilot" }

func (copilot) Lookup() (string, error) {
	path, err := exec.LookPath("copilot")
	if err != nil {
		return "", fmt.Errorf("copilot not found in PATH: %w", err)
	}
	return path, nil
}

// Prepare writes the project context into <worktree>/AGENTS.md, which Copilot
// auto-loads every session. AGENTS.md is added to the worktree's
// .git/info/exclude so it does not dirty the user's git status.
func (copilot) Prepare(o LaunchOptions) error {
	return prepareAgentsMD(o)
}

func prepareAgentsMD(o LaunchOptions) error {
	if err := agentsmd.Apply(o.Worktree, o.ProjectDir, o.SystemPrompt); err != nil {
		return err
	}
	if err := gitExclude(o.Worktree, "AGENTS.md"); err != nil {
		return fmt.Errorf("exclude AGENTS.md: %w", err)
	}
	return nil
}

// gitExclude appends pattern to the worktree's .git/info/exclude if not already
// present. A missing .git/info directory (non-git dir) is treated as success.
func gitExclude(worktree, pattern string) error {
	excludePath := filepath.Join(worktree, ".git", "info", "exclude")
	data, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		// A worktree's .git is a file pointing at the real gitdir; if the
		// info/exclude path does not resolve, skip silently rather than fail.
		return nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil
		}
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil
	}
	defer f.Close()
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = f.WriteString(pattern + "\n")
	return err
}

// Capabilities reports Copilot's real values: task-based subagents, a
// long-context tier, prose (not deterministic-slash) invocation, no lifecycle
// hook, context delivered via AGENTS.md, and the Claude→Copilot lowercase
// tool-name map. (Permission handling is mode-driven; see PermissionModes.)
func (copilot) Capabilities() Capabilities {
	return Capabilities{
		Subagents:          SubagentTask,
		LargeContext:       true,
		DeterministicSlash: false,
		LifecycleHook:      HookNone,
		ContextInjection:   ContextFile,
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
	args = append(args, "-i", relaySkillPrompt(o))
	return args
}

func relaySkillPrompt(o LaunchOptions) string {
	prompt := fmt.Sprintf("Run the relay %q skill", o.Command)
	if o.CommandArgs != "" {
		prompt += " for slug " + o.CommandArgs
	}
	return prompt + "."
}

// PermissionModes lists Copilot's permission modes; "allow-all" (the default)
// grants all permissions without prompting, "prompt" leaves Copilot's normal
// asks on.
func (copilot) PermissionModes() []string { return []string{"allow-all", "prompt"} }
