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
