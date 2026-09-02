package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/program"
)

func runProgramCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newCmdProgram()
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

func saveProgramTestConfig(t *testing.T) {
	t.Helper()
	if err := config.Save(config.Config{
		BranchPrefix: "test/",
		DefaultAgent: "copilot",
		PermissionModes: map[string]string{
			"copilot": "allow-all",
		},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

func TestProgramNewCreatesFilesWithoutLaunch(t *testing.T) {
	repo := newTestRepo(t)
	t.Setenv("HOME", t.TempDir())
	installManagedHerdrFakes(t, nil)
	saveProgramTestConfig(t)

	launched := false
	previousLaunch := launchAgent
	launchAgent = func(agent.Agent, agent.LaunchOptions) error {
		launched = true
		return nil
	}
	t.Cleanup(func() { launchAgent = previousLaunch })

	out, err := runProgramCommand(t, "new", "Ship Relay governance", "--name", "governance", "--repo", repo, "--no-launch")
	if err != nil {
		t.Fatalf("program new: %v", err)
	}
	if launched {
		t.Fatal("program new --no-launch launched an agent")
	}
	if !strings.Contains(out, "governance") {
		t.Fatalf("output = %q", out)
	}

	dir := program.ProgramDir(program.ActiveDir(), "governance")
	p, err := program.Load(program.ManifestPath(program.ActiveDir(), "governance"))
	if err != nil {
		t.Fatalf("load program: %v", err)
	}
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Ship Relay governance" || p.Repo != repoRoot || p.Agent != "copilot" || p.MaxOpenPRs != 3 {
		t.Fatalf("program = %+v", p)
	}
	for name, contains := range map[string][]string{
		"goal.md":      {"# Ship Relay governance", "Approved outcome", "Priorities", "Architecture", "Guardrails"},
		"decisions.md": {"# Decisions"},
		"progress.md":  {"# Progress", "Program created"},
	} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, want := range contains {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s = %q, missing %q", name, data, want)
			}
		}
	}
}

func TestProgramNewLaunchesTL(t *testing.T) {
	repo := newTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	installManagedHerdrFakes(t, nil)
	saveProgramTestConfig(t)

	var got agent.LaunchOptions
	previousLaunch := launchAgent
	launchAgent = func(_ agent.Agent, options agent.LaunchOptions) error {
		got = options
		return nil
	}
	t.Cleanup(func() { launchAgent = previousLaunch })

	if _, err := runProgramCommand(t, "new", "Ship Relay governance", "-n", "governance", "--repo", repo); err != nil {
		t.Fatalf("program new: %v", err)
	}
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	want := agent.LaunchOptions{
		Worktree:   repoRoot,
		ProjectDir: filepath.Join(home, ".relay", "programs", "active", "governance"),
		SystemPrompt: "Active relay program: governance. Role: tech lead. Run the tl skill only. Never invoke stack-ship; " +
			"decompose into program work items and dispatch each item through deliver-pr. " +
			"Reconstruct governance state from the program directory before acting.",
		SessionName:    "relay:program:governance",
		Command:        "tl",
		CommandArgs:    "governance",
		WorkflowGoal:   "Ship Relay governance",
		PermissionMode: "allow-all",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("launch options:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestProgramResumeLaunchesFreshTLReentry(t *testing.T) {
	repo := newTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	installManagedHerdrFakes(t, nil)
	saveProgramTestConfig(t)
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	p, err := program.New("governance", "Ship Relay governance", repoRoot, "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.OpenDecision(program.Decision{
		Kind:     program.DecisionQuestion,
		RaisedBy: program.RaisedByTL,
		Question: "Which rollout?",
	}); err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}

	var got agent.LaunchOptions
	previousLaunch := launchAgent
	launchAgent = func(_ agent.Agent, options agent.LaunchOptions) error {
		got = options
		return nil
	}
	t.Cleanup(func() { launchAgent = previousLaunch })

	out, err := runProgramCommand(t, "resume", "governance")
	if err != nil {
		t.Fatalf("program resume: %v", err)
	}
	for _, want := range []string{"Program: governance", "State: draft", "Open decisions: 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("resume output %q missing %q", out, want)
		}
	}
	want := agent.LaunchOptions{
		Worktree:   repoRoot,
		ProjectDir: filepath.Join(home, ".relay", "programs", "active", "governance"),
		SystemPrompt: "Active relay program: governance. Role: tech lead. Run the tl skill only. Never invoke stack-ship; " +
			"decompose into program work items and dispatch each item through deliver-pr. " +
			"Reconstruct governance state from the program directory before acting.",
		SessionName:    "relay:program:governance",
		Command:        "tl",
		CommandArgs:    "governance",
		WorkflowGoal:   "Ship Relay governance",
		PermissionMode: "allow-all",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("launch options:\n got: %#v\nwant: %#v", got, want)
	}

	if err := program.Archive("governance"); err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCommand(t, "resume", "governance"); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("resume archived error = %v", err)
	}
}

// One program has exactly one CEO-facing tech lead. A second resume must send
// the CEO to the live pane instead of launching a rival tech lead beside it.
func TestProgramResumeRefusesToLaunchASecondTL(t *testing.T) {
	for _, test := range []struct {
		name   string
		agents []herdr.Agent
		want   []string
	}{
		{
			name: "one live owner",
			agents: []herdr.Agent{
				{PaneID: "near", TerminalTitle: "relay:program:governance-other"},
				{PaneID: "tl-1", TerminalTitle: "relay:program:governance - GitHub Copilot"},
			},
			want: []string{`program "governance" already has a live tech lead session in pane tl-1`,
				"herdr agent focus tl-1"},
		},
		{
			name: "ambiguous owners",
			agents: []herdr.Agent{
				{PaneID: "tl-1", TerminalTitle: "relay:program:governance"},
				{PaneID: "tl-2", TerminalTitle: "relay:program:governance - GitHub Copilot"},
			},
			want: []string{"2 live tech lead sessions (panes tl-1, tl-2)", "herdr agent focus <pane>"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newTestRepo(t)
			t.Setenv("HOME", t.TempDir())
			installManagedHerdrFakes(t, &fakeHerdrClient{agentResponses: [][]herdr.Agent{test.agents}})
			saveProgramTestConfig(t)
			repoRoot, err := filepath.EvalSymlinks(repo)
			if err != nil {
				t.Fatal(err)
			}
			p, err := program.New("governance", "Ship Relay governance", repoRoot, "copilot", 3)
			if err != nil {
				t.Fatal(err)
			}
			if err := program.Create(p); err != nil {
				t.Fatal(err)
			}
			previousLaunch := launchAgent
			launched := 0
			launchAgent = func(agent.Agent, agent.LaunchOptions) error {
				launched++
				return nil
			}
			t.Cleanup(func() { launchAgent = previousLaunch })

			_, err = runProgramCommand(t, "resume", "governance")
			if err == nil {
				t.Fatal("program resume launched a second tech lead")
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("resume error %q is missing %q", err, want)
				}
			}
			if launched != 0 {
				t.Fatalf("agent launches = %d, want 0", launched)
			}
		})
	}
}

func TestProgramResumeUsesProgramAgentConfiguration(t *testing.T) {
	repo := newTestRepo(t)
	t.Setenv("HOME", t.TempDir())
	installManagedHerdrFakes(t, nil)
	if err := config.Save(config.Config{
		BranchPrefix: "test/",
		DefaultAgent: "copilot",
		PermissionModes: map[string]string{
			"copilot": "allow-all",
			"claude":  "prompt",
		},
	}); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	p, err := program.New("governance", "Ship Relay governance", repoRoot, "claude", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}

	var got agent.LaunchOptions
	previousLaunch := launchAgent
	launchAgent = func(_ agent.Agent, options agent.LaunchOptions) error {
		got = options
		return nil
	}
	t.Cleanup(func() { launchAgent = previousLaunch })

	if _, err := runProgramCommand(t, "resume", p.Slug); err != nil {
		t.Fatal(err)
	}
	if got.PermissionMode != "prompt" {
		t.Fatalf("permission mode = %q, want prompt", got.PermissionMode)
	}
}

func TestProgramQueueIsReadOnlyAndStatusDetailIncludesPlan(t *testing.T) {
	repo := newTestRepo(t)
	t.Setenv("HOME", t.TempDir())
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	p, err := program.New("governance", "Ship Relay governance", repoRoot, "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "Build CLI", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	progressPath := program.ProgressPath(program.ProgramDir(program.ActiveDir(), p.Slug))
	if err := os.WriteFile(progressPath, []byte("# Progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := program.ManifestPath(program.ActiveDir(), p.Slug)
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	progressBefore, err := os.ReadFile(progressPath)
	if err != nil {
		t.Fatal(err)
	}

	first, err := runProgramCommand(t, "queue", p.Slug, "--json")
	if err != nil {
		t.Fatalf("program queue: %v", err)
	}
	second, err := runProgramCommand(t, "queue", p.Slug, "--json")
	if err != nil {
		t.Fatalf("second program queue: %v", err)
	}
	if first != second {
		t.Fatalf("queue output changed:\nfirst: %s\nsecond: %s", first, second)
	}
	var queue struct {
		Program     string       `json:"program"`
		View        program.View `json:"view"`
		NextCommand string       `json:"next_command"`
	}
	if err := json.Unmarshal([]byte(first), &queue); err != nil {
		t.Fatalf("decode queue: %v\n%s", err, first)
	}
	if queue.Program != p.Slug || len(queue.View.Ready) != 1 || queue.View.Ready[0].ID != item.ID ||
		queue.NextCommand != "relay program dispatch governance "+item.ID {
		t.Fatalf("queue = %+v", queue)
	}
	manifestAfter, _ := os.ReadFile(manifestPath)
	progressAfter, _ := os.ReadFile(progressPath)
	if !bytes.Equal(manifestBefore, manifestAfter) || !bytes.Equal(progressBefore, progressAfter) {
		t.Fatal("queue modified program files")
	}

	detail, err := runProgramCommand(t, "status", p.Slug, "--json")
	if err != nil {
		t.Fatalf("program status: %v", err)
	}
	var status struct {
		Program program.Program `json:"program"`
		Plan    program.View    `json:"plan"`
	}
	if err := json.Unmarshal([]byte(detail), &status); err != nil {
		t.Fatalf("decode status: %v\n%s", err, detail)
	}
	if status.Program.Slug != p.Slug || len(status.Plan.Ready) != 1 {
		t.Fatalf("status = %+v", status)
	}
}

func TestProgramLifecycleCommandsAndErrors(t *testing.T) {
	repo := newTestRepo(t)
	t.Setenv("HOME", t.TempDir())
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	p, err := program.New("governance", "Ship Relay governance", repoRoot, "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "Governed change", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	goalPath := filepath.Join(program.ProgramDir(program.ActiveDir(), p.Slug), "goal.md")
	if err := os.WriteFile(goalPath, []byte("# Goal\n\nComplete goal, priorities, architecture, and guardrails.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runProgramCommand(t, "release", p.Slug); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("release draft error = %v", err)
	}
	for _, command := range [][]string{
		{"submit", p.Slug},
		{"approve", p.Slug, "--by", "board"},
		{"hold", p.Slug, "--reason", "Revisit architecture"},
		{"release", p.Slug},
	} {
		if _, err := runProgramCommand(t, command...); err != nil {
			t.Fatalf("program %s: %v", command[0], err)
		}
	}
	active, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if active.State != program.StateActive || active.ApprovedBy != "board" {
		t.Fatalf("active program = %+v", active)
	}
	progress, err := os.ReadFile(program.ProgressPath(program.ProgramDir(program.ActiveDir(), p.Slug)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(progress), "Revisit architecture") {
		t.Fatalf("progress = %q", progress)
	}

	if _, err := runProgramCommand(t, "item", "cancel", p.Slug, item.ID, "--reason", "Done outside the program"); err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCommand(t, "finish", p.Slug); err != nil {
		t.Fatalf("program finish: %v", err)
	}
	finished, err := program.Load(program.ManifestPath(program.ArchivedDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != program.StateCompleted {
		t.Fatalf("finished state = %s", finished.State)
	}

	unfinished, err := program.New("unfinished", "Keep working", repoRoot, "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := unfinished.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := unfinished.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	if _, err := unfinished.AddItem(program.WorkItem{Title: "Pending work", Priority: program.PriorityP0}); err != nil {
		t.Fatal(err)
	}
	if err := program.Create(unfinished); err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCommand(t, "finish", unfinished.Slug); err == nil ||
		!strings.Contains(err.Error(), "want merged or "+string(program.ItemCancelled)) {
		t.Fatalf("finish unfinished error = %v", err)
	}

	abandoned, err := program.New("abandoned", "Stop this", repoRoot, "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Create(abandoned); err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCommand(t, "abandon", abandoned.Slug, "--reason", "No longer strategic"); err != nil {
		t.Fatalf("program abandon: %v", err)
	}
	abandoned, err = program.Load(program.ManifestPath(program.ArchivedDir(), abandoned.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if abandoned.State != program.StateAbandoned {
		t.Fatalf("abandoned state = %s", abandoned.State)
	}
}

func TestProgramApproveRequiresCompletedGoalAndWork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := createCLIProgram(t, "governance")
	goalPath := filepath.Join(program.ProgramDir(program.ActiveDir(), p.Slug), "goal.md")
	if err := os.WriteFile(goalPath, []byte("# Goal\n\n_TBD_\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCommand(t, "submit", p.Slug); err != nil {
		t.Fatal(err)
	}

	if _, err := runProgramCommand(t, "approve", p.Slug); err == nil ||
		!strings.Contains(err.Error(), "goal.md still contains _TBD_") {
		t.Fatalf("approve incomplete goal error = %v", err)
	}

	if err := os.WriteFile(goalPath, []byte("# Goal\n\nComplete goal, priorities, architecture, and guardrails.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCommand(t, "approve", p.Slug); err == nil ||
		!strings.Contains(err.Error(), "at least one work item") {
		t.Fatalf("approve empty program error = %v", err)
	}
}

func TestProgramSetMaxOpenPRs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := createCLIProgram(t, "governance")
	out, err := runProgramCommand(t, "set-max-open-prs", p.Slug, "5", "--by", "ceo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from 3 to 5 by ceo") {
		t.Fatalf("output = %q", out)
	}
	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MaxOpenPRs != 5 {
		t.Fatalf("MaxOpenPRs = %d, want 5", loaded.MaxOpenPRs)
	}
}

func TestProgramContractAndDecisionCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := createCLIProgram(t, "governance")
	if _, err := runProgramCommand(t, "item", "add", p.Slug, "Build API"); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(p.Repo, "api-contract.md")
	if err := os.WriteFile(source, []byte("# API contract\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "contract", "publish", p.Slug, "Public API", "--file", source)
	if err != nil {
		t.Fatalf("contract publish: %v", err)
	}
	fields := strings.Fields(out)
	if len(fields) != 2 || fields[0] != "public-api@v1" || len(fields[1]) != 64 {
		t.Fatalf("publish output = %q", out)
	}
	queueJSON, err := runProgramCommand(t, "queue", p.Slug, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var queue programQueueOutput
	if err := json.Unmarshal([]byte(queueJSON), &queue); err != nil {
		t.Fatal(err)
	}
	if queue.NextCommand != "relay program contract approve governance "+fields[0]+" --by ceo" {
		t.Fatalf("contract next command = %q", queue.NextCommand)
	}
	if _, err := runProgramCommand(t, "item", "update", p.Slug, "w1", "--add-contract", fields[0]); err != nil {
		t.Fatalf("add contract to item: %v", err)
	}
	if _, err := runProgramCommand(t, "contract", "approve", p.Slug, fields[0]); err != nil {
		t.Fatalf("contract approve: %v", err)
	}

	out, err = runProgramCommand(t, "decision", "open", p.Slug,
		"--question", "Which rollout?", "--item", "w1", "--options", "canary|all-at-once")
	if err != nil {
		t.Fatalf("decision open: %v", err)
	}
	decisionID := strings.TrimSpace(out)
	if decisionID != "d2" {
		t.Fatalf("decision ID = %q", decisionID)
	}
	if _, err := runProgramCommand(t, "decision", "resolve", p.Slug, decisionID,
		"--answer", "canary", "--by", "ceo"); err != nil {
		t.Fatalf("decision resolve: %v", err)
	}

	out, err = runProgramCommand(t, "contract", "list", p.Slug, "--json")
	if err != nil {
		t.Fatalf("contract list: %v", err)
	}
	var contracts []program.Contract
	if err := json.Unmarshal([]byte(out), &contracts); err != nil {
		t.Fatalf("decode contracts: %v\n%s", err, out)
	}
	if len(contracts) != 1 || contracts[0].Status != program.ContractApproved || contracts[0].ApprovedBy != "ceo" {
		t.Fatalf("contracts = %+v", contracts)
	}
	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.OpenDecisions()) != 0 || loaded.Decisions[1].Answer != "canary" {
		t.Fatalf("decisions = %+v", loaded.Decisions)
	}
	log, err := os.ReadFile(program.DecisionLogPath(program.ProgramDir(program.ActiveDir(), p.Slug)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{fields[0], "approved", "Which rollout?", "canary"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("decision log %q missing %q", log, want)
		}
	}

}

func TestProgramContractRejectCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := createCLIProgram(t, "governance")
	source := filepath.Join(p.Repo, "rejected-contract.md")
	if err := os.WriteFile(source, []byte("reject me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runProgramCommand(t, "contract", "publish", p.Slug, "api", "--file", source)
	if err != nil {
		t.Fatal(err)
	}
	ref := strings.Fields(out)[0]
	if _, err := runProgramCommand(t, "contract", "reject", p.Slug, ref); err == nil ||
		!strings.Contains(err.Error(), "--reason is required") {
		t.Fatalf("missing reason error = %v", err)
	}
	if _, err := runProgramCommand(t, "decision", "resolve", p.Slug, "d1", "--answer", "reject"); err == nil ||
		!strings.Contains(err.Error(), "contract reject") {
		t.Fatalf("generic contract resolution error = %v", err)
	}
	if _, err := runProgramCommand(t, "contract", "reject", p.Slug, ref,
		"--by", "ceo", "--reason", "missing rollback constraints"); err != nil {
		t.Fatalf("contract reject: %v", err)
	}
	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Contracts[0].Status != program.ContractRejected ||
		loaded.Contracts[0].RejectionReason != "missing rollback constraints" ||
		len(loaded.OpenDecisions()) != 0 {
		t.Fatalf("rejected program state = %+v", loaded)
	}
	log, err := os.ReadFile(program.DecisionLogPath(program.ProgramDir(program.ActiveDir(), p.Slug)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "rejected by ceo") ||
		!strings.Contains(string(log), "missing rollback constraints") {
		t.Fatalf("decision log = %q", log)
	}
}

func TestRootRegistersProgramCommand(t *testing.T) {
	command, _, err := newRootCmd().Find([]string{"program"})
	if err != nil {
		t.Fatalf("find program command: %v", err)
	}
	if command == nil || command.Name() != "program" {
		t.Fatalf("program command = %#v", command)
	}
}

func TestManagedProgramCommandsRequireHerdr(t *testing.T) {
	tests := []struct {
		name      string
		available bool
		herdrEnv  string
		agentErr  error
		want      string
	}{
		{name: "missing binary", want: "herdr binary is not on PATH"},
		{name: "outside a Herdr pane", available: true, want: "HERDR_ENV=1"},
		{
			name: "unreachable server", available: true, herdrEnv: "1",
			agentErr: errors.New("connection refused"), want: "running Herdr server",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newTestRepo(t)
			t.Setenv("HOME", t.TempDir())
			client := installManagedHerdrFakes(t, &fakeHerdrClient{agentErr: tt.agentErr})
			t.Setenv("HERDR_ENV", tt.herdrEnv)
			herdrAvailable = func() bool { return tt.available }
			saveProgramTestConfig(t)
			launched := false
			previousLaunch := launchAgent
			launchAgent = func(agent.Agent, agent.LaunchOptions) error {
				launched = true
				return nil
			}
			t.Cleanup(func() { launchAgent = previousLaunch })

			_, err := runProgramCommand(t, "new", "Ship Relay governance",
				"--name", "governance", "--repo", repo, "--no-launch")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("program new error = %v, want %q", err, tt.want)
			}
			if _, statErr := os.Stat(program.ManifestPath(program.ActiveDir(), "governance")); !os.IsNotExist(statErr) {
				t.Fatalf("program new created a program without Herdr: %v", statErr)
			}

			p, newErr := program.New("governance", "Ship Relay governance", repo, "copilot", 3)
			if newErr != nil {
				t.Fatal(newErr)
			}
			if createErr := program.Create(p); createErr != nil {
				t.Fatal(createErr)
			}
			if _, err := runProgramCommand(t, "resume", "governance"); err == nil ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("program resume error = %v, want %q", err, tt.want)
			}
			if launched {
				t.Fatal("managed program launched a tech lead without Herdr")
			}
			if len(client.created) != 0 {
				t.Fatalf("managed program created Herdr tabs: %#v", client.created)
			}
		})
	}
}

func TestManagedProgramCommandsRejectAgentsWithoutHerdrIntegration(t *testing.T) {
	repo := newTestRepo(t)
	t.Setenv("HOME", t.TempDir())
	installManagedHerdrFakes(t, nil)
	if err := config.Save(config.Config{
		BranchPrefix:    "test/",
		DefaultAgent:    "copilot",
		PermissionModes: map[string]string{"copilot": "allow-all", "codex": "allow-all"},
	}); err != nil {
		t.Fatal(err)
	}

	_, err := runProgramCommand(t, "new", "Ship Relay governance",
		"--name", "governance", "--repo", repo, "--agent", "codex", "--no-launch")
	if err == nil || !strings.Contains(err.Error(), "codex") ||
		!strings.Contains(err.Error(), "named sessions") ||
		!strings.Contains(err.Error(), "copilot") || !strings.Contains(err.Error(), "claude") {
		t.Fatalf("program new with codex error = %v", err)
	}
}
