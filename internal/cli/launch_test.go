package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/project"
)

func TestWorkflowGoal(t *testing.T) {
	tests := []struct {
		workflow string
		task     string
		want     string
	}{
		{
			workflow: "deliver-pr",
			task:     "Add native goals to Copilot launches",
			want:     "Add native goals to Copilot launches",
		},
		{
			workflow: "stack-ship",
			task:     "  Ship the new search experience  ",
			want:     "Ship the new search experience",
		},
		{workflow: "deliver-pr"},
		{workflow: "clarify", task: "Clarify the task"},
		{workflow: "plan", task: "Plan the task"},
		{workflow: "review", task: "Review the task"},
		{workflow: "validate", task: "Validate the task"},
		{workflow: "ship", task: "Ship the task"},
		{task: "Complete the task"},
	}

	for _, tt := range tests {
		t.Run(tt.workflow, func(t *testing.T) {
			if got := workflowGoal(tt.workflow, tt.task); got != tt.want {
				t.Errorf("workflowGoal(%q, %q) = %q, want %q", tt.workflow, tt.task, got, tt.want)
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
		"Add native goals to Copilot launches",
		"allow-all",
	)
	want := agent.LaunchOptions{
		Worktree:       "/repo/.worktrees/demo",
		ProjectDir:     "/home/.relay/projects/active/demo",
		SystemPrompt:   "Active relay project: demo. Workflow: deliver-pr. Mode: full.",
		SessionName:    "relay:demo",
		Command:        "deliver-pr",
		CommandArgs:    "demo",
		WorkflowGoal:   "Add native goals to Copilot launches",
		PermissionMode: "allow-all",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("relayLaunchOptions mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestRunNewLaunchesWorkflowGoal(t *testing.T) {
	repo := newTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	if err := config.Save(config.Config{
		BranchPrefix: "test/",
		DefaultAgent: "copilot",
		PermissionModes: map[string]string{
			"copilot": "allow-all",
		},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	var got agent.LaunchOptions
	previousLaunch := launchAgent
	launchAgent = func(_ agent.Agent, o agent.LaunchOptions) error {
		got = o
		return nil
	}
	t.Cleanup(func() { launchAgent = previousLaunch })

	if err := runNew(newOpts{
		task:     "demo task",
		name:     "demo",
		agent:    "copilot",
		workflow: "stack-ship",
	}); err != nil {
		t.Fatalf("runNew: %v", err)
	}
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve repo path: %v", err)
	}

	want := agent.LaunchOptions{
		Worktree:       filepath.Join(repoRoot, ".worktrees", "test_demo"),
		ProjectDir:     filepath.Join(home, ".relay", "projects", "active", "demo"),
		SystemPrompt:   "Active relay project: demo. Workflow: stack-ship. Mode: full.",
		SessionName:    "relay:demo",
		Command:        "stack-ship",
		CommandArgs:    "demo",
		WorkflowGoal:   "demo task",
		PermissionMode: "allow-all",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("launched options mismatch:\n got: %#v\nwant: %#v", got, want)
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
			m:    project.Manifest{Slug: "demo", Title: "Add native goals", Workflow: "deliver-pr", Phase: "validate", Worktree: &worktree},
			want: "deliver-pr",
			goal: "Add native goals",
		},
		{
			name: "persisted stack-ship workflow",
			m:    project.Manifest{Slug: "demo", Title: "Ship search", Workflow: "stack-ship", Phase: "implement", Worktree: &worktree},
			want: "stack-ship",
			goal: "Ship search",
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
				tt.m.Title,
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

func TestRunResumeLaunchesWorkflowGoal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := config.Save(config.Config{
		BranchPrefix: "test/",
		DefaultAgent: "copilot",
		PermissionModes: map[string]string{
			"copilot": "allow-all",
		},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	worktree := t.TempDir()
	projectDir := filepath.Join(project.ActiveDir(), "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := project.Save(filepath.Join(projectDir, "manifest.json"), project.Manifest{
		Slug:     "demo",
		Title:    "Add native goals",
		Agent:    "copilot",
		Workflow: "deliver-pr",
		Phase:    "validate",
		Worktree: &worktree,
	}); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	var got agent.LaunchOptions
	previousLaunch := launchAgent
	launchAgent = func(_ agent.Agent, o agent.LaunchOptions) error {
		got = o
		return nil
	}
	t.Cleanup(func() { launchAgent = previousLaunch })

	if err := runResume("demo"); err != nil {
		t.Fatalf("runResume: %v", err)
	}

	want := agent.LaunchOptions{
		Worktree:       worktree,
		ProjectDir:     projectDir,
		SystemPrompt:   "Active relay project: demo. Workflow: deliver-pr.",
		SessionName:    "relay:demo",
		Command:        "deliver-pr",
		CommandArgs:    "demo",
		WorkflowGoal:   "Add native goals",
		PermissionMode: "allow-all",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("launched options mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}
