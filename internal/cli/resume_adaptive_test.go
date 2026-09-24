package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/project"
)

func TestResumeUsesAdaptiveCompletionInsteadOfStaleManifestPhase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir, _ := saveCompletedAdaptiveResumeProject(t, "demo")
	if err := runResume("demo"); err == nil || !strings.Contains(err.Error(), "is complete") {
		t.Fatalf("resume adaptive completion error = %v", err)
	}
	manifest, err := project.Load(filepath.Join(projectDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Phase != "plan" {
		t.Fatalf("resume rewrote historical manifest phase to %q", manifest.Phase)
	}
}

func TestResumeReopensStaleTerminalAdaptiveState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir, repo := saveCompletedAdaptiveResumeProject(t, "demo")
	if err := config.Save(config.Config{
		BranchPrefix: "test/", DefaultAgent: "copilot",
		PermissionModes: map[string]string{"copilot": "allow-all"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "post-delivery.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var launched agent.LaunchOptions
	previous := launchAgent
	launchAgent = func(_ agent.Agent, options agent.LaunchOptions) error {
		launched = options
		return nil
	}
	t.Cleanup(func() { launchAgent = previous })

	if err := runResume("demo"); err != nil {
		t.Fatal(err)
	}
	if launched.Worktree != repo || !strings.Contains(launched.SystemPrompt, "Workflow: deliver-pr") {
		t.Fatalf("stale adaptive resume launch = %+v", launched)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".coordinator-capability")); err != nil {
		t.Fatalf("stale adaptive resume did not rotate coordinator capability: %v", err)
	}
	state, err := project.LoadState(filepath.Join(projectDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if state.FinalResult != nil || state.Next() != "route" ||
		state.Phases["route"].Status != project.PhaseEscalated {
		t.Fatalf("stale terminal resume state = %+v", state)
	}
}

func saveCompletedAdaptiveResumeProject(t *testing.T, slug string) (string, string) {
	t.Helper()
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, slug, repo)
	projectDir := filepath.Join(project.ActiveDir(), slug)
	manifestPath := filepath.Join(projectDir, "manifest.json")
	manifest, err := project.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Title = "Adaptive flow"
	manifest.Agent = "copilot"
	manifest.Workflow = "deliver-pr"
	manifest.Phase = "plan"
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	state := openPRReadyState(t, slug)
	for _, phase := range state.Order {
		if slices.Contains(state.Route.SelectedPhases, phase) {
			state.Phases[phase] = project.PhaseState{Status: project.PhaseDone}
		} else {
			state.Phases[phase] = project.PhaseState{
				Status: project.PhaseSkipped, Reason: "not selected",
				Outcome: project.PhaseOutcomeNoOp,
			}
		}
	}
	state.DispatchCount = 2
	state.HandoffCount = 1
	state.LastDispatch = "open-pr"
	state.LastDispatchID = "open-pr-dispatch"
	state.PR = project.PRRef{Number: 42, URL: "https://example.test/pull/42"}
	state.FinalResult = &project.FinalResult{
		Status: "opened", PRNumber: 42, PRURL: state.PR.URL,
		RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
		Snapshot: state.Route.Snapshot, DispatchID: state.LastDispatchID,
	}
	if err := project.SaveState(filepath.Join(projectDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	return projectDir, repo
}

func TestResumeRotatesCoordinatorCapabilityForAdaptiveState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	projectDir := filepath.Join(project.ActiveDir(), "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(projectDir, "manifest.json")
	worktree := repo
	if err := project.Save(manifestPath, project.Manifest{
		Slug: "demo", Workflow: "deliver-pr", Worktree: &worktree,
		BaseBranch: "main", StartSHA: gitRevParse(t, repo, "HEAD"),
	}); err != nil {
		t.Fatal(err)
	}
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SaveState(filepath.Join(projectDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	token, err := rotateCoordinatorForResume(manifestPath, "deliver-pr")
	if err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(filepath.Join(projectDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || got.CoordinatorHash == "" {
		t.Fatalf("resume capability = %q, state = %+v", token, got)
	}
	if err := validateCoordinatorToken(got, token); err != nil {
		t.Fatalf("rotated coordinator capability rejected: %v", err)
	}
	handoffPath, err := writeCoordinatorHandoff(projectDir, token)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(handoffPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("coordinator handoff mode = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(handoffPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != token {
		t.Fatal("coordinator handoff did not contain the rotated capability")
	}
	if err := validateCoordinatorToken(got, "wrong-token"); err == nil {
		t.Fatal("invalid coordinator capability was accepted")
	}
	if _, err := os.Stat(handoffPath); err != nil {
		t.Fatalf("invalid capability removed recoverable handoff: %v", err)
	}
	if err := validateCoordinatorToken(got, token); err != nil {
		t.Fatalf("consume coordinator handoff: %v", err)
	}
	if _, err := os.Stat(handoffPath); !os.IsNotExist(err) {
		t.Fatalf("consumed coordinator handoff still exists: %v", err)
	}
}

func TestResumeSupersedesInterruptedActiveDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	manifestPath := project.ManifestPath(project.ActiveDir(), "demo")
	manifest, err := project.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Workflow = "deliver-pr"
	manifest.DeliveryMode = project.DeliveryModeAdaptive
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	state := openPRReadyState(t, "demo")
	state.Phases["implement"] = project.PhaseState{
		Status: project.PhaseInProgress,
		Dispatch: &project.PhaseDispatch{
			ID: "lost-dispatch",
			TokenHashes: map[string]string{
				dispatchScopeFinish: strings.Repeat("a", 64),
			},
			RouteRevision: state.Route.Revision,
			RouteDigest:   state.Route.Digest,
		},
	}
	state.Phases["open-pr"] = project.PhaseState{Status: project.PhasePending}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}

	token, err := rotateCoordinatorForResume(manifestPath, "deliver-pr")
	if err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || got.Phases["implement"].Status != project.PhaseEscalated ||
		got.Phases["implement"].Dispatch != nil || got.Next() != "implement" {
		t.Fatalf("interrupted resume state = %+v, token=%q", got, token)
	}
}

func TestResumeKeepsCoordinatorCapabilityOutOfAgentArguments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	worktree := initCLIGitRepo(t)
	projectDir := filepath.Join(project.ActiveDir(), "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(config.Config{
		BranchPrefix: "test/", DefaultAgent: "copilot",
		PermissionModes: map[string]string{"copilot": "allow-all"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(filepath.Join(projectDir, "manifest.json"), project.Manifest{
		Slug: "demo", Title: "Resume", Agent: "copilot", Workflow: "deliver-pr",
		DeliveryMode: project.DeliveryModeAdaptive, Phase: "route", Worktree: &worktree,
	}); err != nil {
		t.Fatal(err)
	}
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SaveState(filepath.Join(projectDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	var launched agent.LaunchOptions
	previous := launchAgent
	launchAgent = func(_ agent.Agent, options agent.LaunchOptions) error {
		launched = options
		return nil
	}
	t.Cleanup(func() { launchAgent = previous })
	if err := runResume("demo"); err != nil {
		t.Fatal(err)
	}
	handoffPath := filepath.Join(projectDir, ".coordinator-capability")
	data, err := os.ReadFile(handoffPath)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		t.Fatal("resume wrote an empty coordinator capability")
	}
	if strings.Contains(launched.SystemPrompt, token) {
		t.Fatal("resume exposed coordinator capability in agent arguments")
	}
	if !strings.Contains(launched.SystemPrompt, handoffPath) {
		t.Fatalf("resume prompt does not point to capability handoff: %q", launched.SystemPrompt)
	}
}
