package cli

import (
	"fmt"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/project"
)

func workflowGoal(workflow, slug string) string {
	switch workflow {
	case "deliver-pr":
		return fmt.Sprintf("Deliver the Relay deliver-pr workflow for project %q. Use Relay project artifacts and `relay state` as the durable source of truth. Stop when the pull request is open.", slug)
	case "stack-ship":
		return fmt.Sprintf("Deliver the Relay stack-ship workflow for project %q. Use Relay project artifacts and `relay state` as the durable source of truth. Stop only when all acceptance criteria are met and all pull requests are merged.", slug)
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

func relayLaunchOptions(worktree, projectDir, systemPrompt, slug, command, permissionMode string) agent.LaunchOptions {
	return agent.LaunchOptions{
		Worktree:       worktree,
		ProjectDir:     projectDir,
		SystemPrompt:   systemPrompt,
		SessionName:    "relay:" + slug,
		Command:        command,
		CommandArgs:    slug,
		WorkflowGoal:   workflowGoal(command, slug),
		PermissionMode: permissionMode,
	}
}
