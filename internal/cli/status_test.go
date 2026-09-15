package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/project"
)

func TestStatusDetailDisplaysProgramAssociation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir := filepath.Join(project.ActiveDir(), "child")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := project.Save(filepath.Join(projectDir, "manifest.json"), project.Manifest{
		Slug:        "child",
		Program:     "relay-v1",
		ProgramItem: "w2",
	}); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return runStatus(statusOpts{slug: "child"})
	})
	if err != nil {
		t.Fatalf("status detail: %v", err)
	}
	for _, want := range []string{"Program", "relay-v1", "Program item", "w2"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output %q missing %q", out, want)
		}
	}
}

func TestStatusDetailDisplaysRoutedPhaseOutcomes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir := filepath.Join(project.ActiveDir(), "routed")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := project.Save(filepath.Join(projectDir, "manifest.json"), project.Manifest{Slug: "routed"}); err != nil {
		t.Fatal(err)
	}
	state, err := project.NewState("routed", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	state.Phases["route"] = project.PhaseState{
		Status: project.PhaseDone, Outcome: project.PhaseOutcomeMaterial,
	}
	state.Phases["clarify"] = project.PhaseState{
		Status: project.PhaseSkipped, Reason: "requirements are explicit",
		Outcome: project.PhaseOutcomeNoOp,
	}
	state.Phases["implement"] = project.PhaseState{
		Status: project.PhaseEscalated, Reason: "actual diff exceeded threshold",
	}
	if err := project.SaveState(filepath.Join(projectDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return runStatus(statusOpts{slug: "routed"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Status", "escalated", "Phase", "implement",
		"Route", "easy", "clarify", "skipped", "requirements are explicit",
		"implement", "escalated", "actual diff exceeded threshold",
		"Phases completed", "route, clarify",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output %q missing %q", out, want)
		}
	}

}

func TestStatusJSONUsesAdaptiveManifest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir := filepath.Join(project.ActiveDir(), "routed")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(filepath.Join(projectDir, "manifest.json"), project.Manifest{
		Slug: "routed", Status: "in-progress", Phase: "plan",
	}); err != nil {
		t.Fatal(err)
	}
	state, err := project.NewState("routed", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	state.Phases["route"] = project.PhaseState{Status: project.PhaseDone}
	state.Phases["clarify"] = project.PhaseState{
		Status: project.PhaseSkipped, Reason: "explicit", Outcome: project.PhaseOutcomeNoOp,
	}
	state.Phases["implement"] = project.PhaseState{
		Status: project.PhaseBlocked, Reason: "author decision required",
	}
	if err := project.SaveState(filepath.Join(projectDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return runStatus(statusOpts{slug: "routed", jsonOutput: true})
	})
	if err != nil {
		t.Fatal(err)
	}
	var got project.Manifest
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != project.PhaseBlocked || got.Phase != "implement" {
		t.Fatalf("adaptive JSON status = %+v", got)
	}
}

func TestStatusDetailDisplaysLegacyBlockedPhase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir := filepath.Join(project.ActiveDir(), "legacy")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(filepath.Join(projectDir, "manifest.json"), project.Manifest{Slug: "legacy"}); err != nil {
		t.Fatal(err)
	}
	state, err := project.NewState("legacy", "deliver-pr", []string{
		"clarify", "plan", "implement", "simplify", "review", "validate", "open-pr",
	})
	if err != nil {
		t.Fatal(err)
	}
	state.Version = 0
	state.Phases["implement"] = project.PhaseState{
		Status: project.PhaseBlocked, Reason: "author decision required",
	}
	if err := project.SaveState(filepath.Join(projectDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return runStatus(statusOpts{slug: "legacy"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"implement", "blocked", "author decision required"} {
		if !strings.Contains(out, want) {
			t.Errorf("legacy status output %q missing %q", out, want)
		}
	}
}
