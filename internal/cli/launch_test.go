package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/herdr"
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

func TestRunResumeRefusesDuplicateManagedHerdrOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "")
	saveProgramTestConfig(t)
	repo := t.TempDir()
	worktree := filepath.Join(repo, ".worktrees", "child")
	pluginDir := filepath.Join(worktree, "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(project.ActiveDir(), "child")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(filepath.Join(projectDir, "manifest.json"), project.Manifest{
		Slug: "child", Title: "Managed child", Agent: "copilot", Workflow: "deliver-pr",
		Phase: "implement", Repo: repo, Worktree: &worktree, Program: "governance", ProgramItem: "w1",
	}); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{{
		Status: herdr.StatusWorking, PaneID: "w7:p4",
		TerminalTitle: "relay:child - GitHub Copilot", CWD: repo, ForegroundCWD: pluginDir,
	}}}}
	previousClient := newHerdrClient
	previousAvailable := herdrAvailable
	newHerdrClient = func() herdrRuntimeClient { return client }
	herdrAvailable = func() bool { return true }
	t.Cleanup(func() {
		newHerdrClient = previousClient
		herdrAvailable = previousAvailable
	})

	launched := false
	previousLaunch := launchAgent
	launchAgent = func(agent.Agent, agent.LaunchOptions) error {
		launched = true
		return nil
	}
	t.Cleanup(func() { launchAgent = previousLaunch })

	err := runResume("child")
	if err == nil || !strings.Contains(err.Error(), "another live Herdr owner") ||
		!strings.Contains(err.Error(), "herdr agent focus w7:p4") {
		t.Fatalf("runResume error = %v", err)
	}
	if launched {
		t.Fatal("duplicate managed resume launched an agent")
	}
}

func TestRunResumeFailsClosedWhenHerdrDiscoveryFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveProgramTestConfig(t)
	repo := t.TempDir()
	worktree := filepath.Join(repo, ".worktrees", "child")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(project.ActiveDir(), "child")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(filepath.Join(projectDir, "manifest.json"), project.Manifest{
		Slug: "child", Title: "Managed child", Agent: "copilot", Workflow: "deliver-pr",
		Phase: "implement", Repo: repo, Worktree: &worktree, Program: "governance", ProgramItem: "w1",
	}); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{agentErr: errors.New("Herdr service unavailable")}
	previousClient := newHerdrClient
	previousAvailable := herdrAvailable
	newHerdrClient = func() herdrRuntimeClient { return client }
	herdrAvailable = func() bool { return true }
	t.Cleanup(func() {
		newHerdrClient = previousClient
		herdrAvailable = previousAvailable
	})

	launched := false
	previousLaunch := launchAgent
	launchAgent = func(agent.Agent, agent.LaunchOptions) error {
		launched = true
		return nil
	}
	t.Cleanup(func() { launchAgent = previousLaunch })

	err := runResume("child")
	if err == nil || !strings.Contains(err.Error(), "running Herdr server") ||
		!strings.Contains(err.Error(), "herdr agent list") ||
		!strings.Contains(err.Error(), "relay resume child") {
		t.Fatalf("runResume error = %v", err)
	}
	if launched {
		t.Fatal("managed resume launched after failed Herdr discovery")
	}
}

func TestRunResumeRequiresHerdrOnlyForManagedChildren(t *testing.T) {
	tests := []struct {
		name        string
		program     string
		programItem string
		available   bool
		agents      []herdr.Agent
		wantCalls   int
		wantErr     string
	}{
		{
			name:      "standalone project ignores Herdr entirely",
			available: false,
			agents: []herdr.Agent{{
				Status: herdr.StatusWorking, PaneID: "w7:p4",
				TerminalTitle: "relay:child",
			}},
		},
		{
			name:    "managed child without Herdr installed fails closed",
			program: "governance", programItem: "w1", available: false,
			wantErr: "herdr binary is not on PATH",
		},
		{
			name:    "managed child with healthy Herdr and no owner",
			program: "governance", programItem: "w1", available: true, wantCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("HERDR_ENV", "")
			saveProgramTestConfig(t)
			worktree := t.TempDir()
			projectDir := filepath.Join(project.ActiveDir(), "child")
			if err := os.MkdirAll(projectDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := project.Save(filepath.Join(projectDir, "manifest.json"), project.Manifest{
				Slug: "child", Title: "Child", Agent: "copilot", Workflow: "deliver-pr",
				Phase: "implement", Worktree: &worktree, Program: tt.program, ProgramItem: tt.programItem,
			}); err != nil {
				t.Fatal(err)
			}
			for index := range tt.agents {
				tt.agents[index].ForegroundCWD = worktree
			}
			client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{tt.agents}}
			previousClient := newHerdrClient
			previousAvailable := herdrAvailable
			newHerdrClient = func() herdrRuntimeClient { return client }
			herdrAvailable = func() bool { return tt.available }
			t.Cleanup(func() {
				newHerdrClient = previousClient
				herdrAvailable = previousAvailable
			})

			launched := false
			previousLaunch := launchAgent
			launchAgent = func(agent.Agent, agent.LaunchOptions) error {
				launched = true
				return nil
			}
			t.Cleanup(func() { launchAgent = previousLaunch })

			err := runResume("child")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("runResume error = %v, want %q", err, tt.wantErr)
				}
				if launched {
					t.Fatal("managed resume launched without Herdr")
				}
				return
			}
			if err != nil {
				t.Fatalf("runResume: %v", err)
			}
			if !launched {
				t.Fatal("compatible resume did not launch")
			}
			if client.agentCalls != tt.wantCalls {
				t.Fatalf("Herdr agent list called %d times, want %d", client.agentCalls, tt.wantCalls)
			}
		})
	}
}
