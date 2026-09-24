package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/project"
)

func TestLegacySevenPhaseStateFlowActiveAndArchived(t *testing.T) {
	for _, location := range []string{"active", "archived"} {
		t.Run(location, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			if err := config.Save(config.Config{
				BranchPrefix:    "test/",
				DefaultAgent:    "copilot",
				PermissionModes: map[string]string{"copilot": "allow-all"},
			}); err != nil {
				t.Fatal(err)
			}
			worktree := t.TempDir()
			root := project.ActiveDir()
			if location == "archived" {
				root = project.ArchivedDir()
			}
			projectDir := filepath.Join(root, "demo")
			if err := os.MkdirAll(projectDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := project.Save(filepath.Join(projectDir, "manifest.json"), project.Manifest{
				Slug: "demo", Title: "Legacy flow", Agent: "copilot",
				Workflow: "deliver-pr", Phase: "clarify", Worktree: &worktree,
			}); err != nil {
				t.Fatal(err)
			}
			order := []string{"clarify", "plan", "implement", "simplify", "review", "validate", "open-pr"}
			phases := make(map[string]project.PhaseState, len(order))
			for _, phase := range order {
				phases[phase] = project.PhaseState{Status: project.PhasePending}
			}
			phases["clarify"] = project.PhaseState{
				Status: project.PhaseInProgress, Artifact: "requirements.md", Task: "1/7",
				StartedAt: "2026-09-14T00:00:00Z",
			}
			raw, err := json.MarshalIndent(project.WorkflowState{
				Slug: "demo", Workflow: "deliver-pr", Order: order, Phases: phases,
			}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			statePath := filepath.Join(projectDir, "state.json")
			if err := os.WriteFile(statePath, append(raw, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}

			var launched agent.LaunchOptions
			previousLaunch := launchAgent
			launchAgent = func(_ agent.Agent, options agent.LaunchOptions) error {
				launched = options
				return nil
			}
			t.Cleanup(func() { launchAgent = previousLaunch })
			if err := runResume("demo"); err != nil {
				t.Fatal(err)
			}
			if launched.ProjectDir != projectDir || launched.Command != "deliver-pr" {
				t.Fatalf("resume used wrong project: %+v", launched)
			}

			for index, phase := range order {
				out, err := runState(t, "next", "demo")
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(out) != phase {
					t.Fatalf("next at %d = %q, want %q", index, out, phase)
				}
				if phase == "clarify" {
					if _, err := runState(t, "set", "demo", phase, "done"); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := runState(t, "set", "demo", phase, "in-progress",
						"--artifact", phase+".md", "--task", strings.Join([]string{"step", phase}, " ")); err != nil {
						t.Fatal(err)
					}
					if _, err := runState(t, "advance", "demo"); err != nil {
						t.Fatal(err)
					}
				}

			}

			got, err := project.LoadState(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != 0 || got.Route != nil || !reflect.DeepEqual(got.Order, order) {
				t.Fatalf("legacy shape changed: %+v", got)
			}
			first := got.Phases["clarify"]
			if first.Artifact != "requirements.md" || first.Task != "1/7" ||
				first.StartedAt != "2026-09-14T00:00:00Z" {
				t.Fatalf("legacy pointers changed: %+v", first)
			}
			for _, phase := range order {
				if got.Phases[phase].Status != project.PhaseDone {
					t.Fatalf("%s status = %q, want done", phase, got.Phases[phase].Status)
				}
			}
			otherRoot := project.ArchivedDir()
			if location == "archived" {
				otherRoot = project.ActiveDir()
			}
			if _, err := os.Stat(filepath.Join(otherRoot, "demo", "state.json")); !os.IsNotExist(err) {
				t.Fatalf("state was written outside resolved %s project: %v", location, err)
			}
		})
	}
}

func TestStateLookupDoesNotCombineActiveManifestWithArchivedState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, root := range []string{project.ActiveDir(), project.ArchivedDir()} {
		dir := filepath.Join(root, "demo")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := project.Save(filepath.Join(dir, "manifest.json"), project.Manifest{
			Slug: "demo", Workflow: "deliver-pr",
		}); err != nil {
			t.Fatal(err)
		}
	}
	state, err := project.NewState("demo", "deliver-pr", []string{"clarify"})
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SaveState(
		filepath.Join(project.ArchivedDir(), "demo", "state.json"), state,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "next", "demo"); err == nil ||
		!strings.Contains(err.Error(), "relay state init demo") {
		t.Fatalf("active project incorrectly reused archived state: %v", err)
	}
}
