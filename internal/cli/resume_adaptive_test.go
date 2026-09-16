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
	worktree := t.TempDir()
	projectDir := filepath.Join(project.ActiveDir(), "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(filepath.Join(projectDir, "manifest.json"), project.Manifest{
		Slug: "demo", Title: "Adaptive flow", Agent: "copilot",
		Workflow: "deliver-pr", Phase: "plan", Worktree: &worktree,
	}); err != nil {
		t.Fatal(err)
	}
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	for _, phase := range state.Order {
		if slices.Contains(state.Route.SelectedPhases, phase) {
			state.Phases[phase] = project.PhaseState{Status: project.PhaseDone}
		} else {
			state.Phases[phase] = project.PhaseState{
				Status: project.PhaseSkipped, Reason: "not selected",
				Outcome: project.PhaseOutcomeNoOp,
			}
		}
		state.Evidence.Review = &project.EvidenceRecord{
			Snapshot: state.Route.Snapshot, RouteRevision: state.Route.Revision,
			RouteDigest: state.Route.Digest, DispatchID: "implement-dispatch",
			Result: project.EvidencePassed, Owner: project.EvidenceOwnerImplement,
			CompletedAt: "2026-09-15T00:01:00Z",
			Roles:       append([]string(nil), state.Route.ReviewRoles...),
		}
		state.Evidence.Validation = &project.EvidenceRecord{
			Snapshot: state.Route.Snapshot, RouteRevision: state.Route.Revision,
			RouteDigest: state.Route.Digest, DispatchID: "implement-dispatch",
			Result: project.EvidencePassed, Owner: project.EvidenceOwnerImplement,
			CompletedAt: "2026-09-15T00:02:00Z",
			Commands: []project.CommandEvidence{{
				GateID: "test", Display: "go <redacted-args>",
				Digest: commandDigestForTest("go test ./..."),
			}},
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
	}
	if err := project.SaveState(filepath.Join(projectDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
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

func TestResumeRotatesCoordinatorCapabilityForAdaptiveState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir := filepath.Join(project.ActiveDir(), "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(projectDir, "manifest.json")
	if err := project.Save(manifestPath, project.Manifest{
		Slug: "demo", Workflow: "deliver-pr",
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

func TestResumeKeepsCoordinatorCapabilityOutOfAgentArguments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	worktree := t.TempDir()
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
