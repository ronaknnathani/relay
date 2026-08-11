package cli

import (
	"strings"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/launcher"
	"github.com/ronaknnathani/relay/internal/project"
)

var launchAgent = launcher.Launch

func workflowGoal(workflow, task string) string {
	switch workflow {
	case "deliver-pr", "stack-ship":
		return strings.TrimSpace(task)
	default:
		return ""
	}
}

func resumeCommand(m project.Manifest) string {
	if m.Workflow != "" {
		return m.Workflow
	}
	return project.PhaseToBatch(m.Phase)
}

func relayLaunchOptions(worktree, projectDir, systemPrompt, slug, command, task, permissionMode string) agent.LaunchOptions {
	return agent.LaunchOptions{
		Worktree:       worktree,
		ProjectDir:     projectDir,
		SystemPrompt:   systemPrompt,
		SessionName:    "relay:" + slug,
		Command:        command,
		CommandArgs:    slug,
		WorkflowGoal:   workflowGoal(command, task),
		PermissionMode: permissionMode,
	}
}
