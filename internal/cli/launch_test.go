package cli

import (
	"reflect"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/project"
)

func TestWorkflowGoal(t *testing.T) {
	tests := []struct {
		workflow string
		want     string
	}{
		{
			workflow: "deliver-pr",
			want:     "Deliver the Relay deliver-pr workflow for project \"demo\". Use Relay project artifacts and `relay state` as the durable source of truth. Stop when the pull request is open.",
		},
		{
			workflow: "stack-ship",
			want:     "Deliver the Relay stack-ship workflow for project \"demo\". Use Relay project artifacts and `relay state` as the durable source of truth. Stop only when all acceptance criteria are met and all pull requests are merged.",
		},
		{workflow: "clarify"},
		{workflow: "plan"},
		{workflow: "review"},
		{workflow: "validate"},
		{workflow: "ship"},
		{workflow: ""},
	}

	for _, tt := range tests {
		t.Run(tt.workflow, func(t *testing.T) {
			if got := workflowGoal(tt.workflow, "demo"); got != tt.want {
				t.Errorf("workflowGoal(%q, %q) = %q, want %q", tt.workflow, "demo", got, tt.want)
			}
		})
	}
}

func TestNewLaunchWorkflowGoal(t *testing.T) {
	got := relayLaunchOptions(
		"/repo/.worktrees/demo",
		"/home/.relay/projects/active/demo",
		"Active relay project: demo. Workflow: deliver-pr. Mode: full.",
		"demo",
		"deliver-pr",
		"allow-all",
	)
	want := agent.LaunchOptions{
		Worktree:       "/repo/.worktrees/demo",
		ProjectDir:     "/home/.relay/projects/active/demo",
		SystemPrompt:   "Active relay project: demo. Workflow: deliver-pr. Mode: full.",
		SessionName:    "relay:demo",
		Command:        "deliver-pr",
		CommandArgs:    "demo",
		WorkflowGoal:   workflowGoal("deliver-pr", "demo"),
		PermissionMode: "allow-all",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("relayLaunchOptions mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestResumeCommandAndWorkflowGoal(t *testing.T) {
	worktree := "/repo/.worktrees/demo"
	tests := []struct {
		name string
		m    project.Manifest
		want string
		goal string
	}{
		{
			name: "persisted deliver-pr workflow",
			m:    project.Manifest{Slug: "demo", Workflow: "deliver-pr", Phase: "validate", Worktree: &worktree},
			want: "deliver-pr",
			goal: workflowGoal("deliver-pr", "demo"),
		},
		{
			name: "persisted stack-ship workflow",
			m:    project.Manifest{Slug: "demo", Workflow: "stack-ship", Phase: "implement", Worktree: &worktree},
			want: "stack-ship",
			goal: workflowGoal("stack-ship", "demo"),
		},
		{
			name: "legacy phase fallback",
			m:    project.Manifest{Slug: "demo", Phase: "implement", Worktree: &worktree},
			want: "implement",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.m
			before.PhasesCompleted = append([]string(nil), tt.m.PhasesCompleted...)
			before.PhasesRemaining = append([]string(nil), tt.m.PhasesRemaining...)

			command := resumeCommand(tt.m)
			got := relayLaunchOptions(
				*tt.m.Worktree,
				"/home/.relay/projects/active/demo",
				"Active relay project: demo. Workflow: "+command+".",
				tt.m.Slug,
				command,
				"allow-all",
			)

			if command != tt.want {
				t.Errorf("resumeCommand() = %q, want %q", command, tt.want)
			}
			if got.WorkflowGoal != tt.goal {
				t.Errorf("WorkflowGoal = %q, want %q", got.WorkflowGoal, tt.goal)
			}
			if !reflect.DeepEqual(tt.m, before) {
				t.Errorf("resume launch mutated manifest:\n got: %#v\nwant: %#v", tt.m, before)
			}
		})
	}
}
