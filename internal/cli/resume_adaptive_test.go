package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
