package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/project"
)

func TestAdaptivePRRecordingRejectsCleanSnapshotMismatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state := openPRReadyState(t, "demo")
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	token := dispatchPhase(t, "demo", "open-pr")
	if err := os.WriteFile(repo+"/committed-after-review.txt", []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, repo, "add", "committed-after-review.txt")
	gitOutput(t, repo, "commit", "-q", "-m", "change after review")

	if _, err := runState(
		t,
		"pr", "demo",
		"--number", "42",
		"--url", "https://github.com/example/test/pull/42",
		"--dispatch-token", token,
	); err == nil || !strings.Contains(err.Error(), "snapshot is stale") {
		t.Fatalf("clean snapshot mismatch error = %v", err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.PR.Number != 0 || got.FinalResult != nil ||
		got.PendingPR.Number != 42 ||
		got.Phases["open-pr"].Status != project.PhaseEscalated {
		t.Fatalf("snapshot mismatch state = %+v", got)
	}
}

func TestResumeAtomicallyUpgradesVersionZeroAdaptiveState(t *testing.T) {
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
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Version = 0
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
	if token == "" || got.Version != project.WorkflowStateVersion ||
		got.CoordinatorHash == "" {
		t.Fatalf("upgraded adaptive state = %+v, token=%q", got, token)
	}
}

func TestStateInitCannotBypassAdaptiveManifestWithCustomWorkflow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir := project.ActiveDir() + "/demo"
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := project.Save(project.ManifestPath(project.ActiveDir(), "demo"), project.Manifest{
		Slug: "demo", Workflow: "deliver-pr", DeliveryMode: project.DeliveryModeAdaptive,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runStateRaw(
		t,
		"init", "demo",
		"--workflow", "custom",
		"--phases", "open-pr",
	); err == nil || !strings.Contains(err.Error(), "does not match project workflow") {
		t.Fatalf("adaptive manifest workflow bypass error = %v", err)
	}
	if _, err := os.Stat(project.StatePath("demo")); !os.IsNotExist(err) {
		t.Fatalf("adaptive manifest bypass created state: %v", err)
	}
}

func TestHistoricalVersionOneNoncanonicalDeliveryStateRemainsLegacy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir := project.ActiveDir() + "/demo"
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), "demo"), project.Manifest{
		Slug: "demo", Workflow: "deliver-pr",
	}); err != nil {
		t.Fatal(err)
	}
	state, err := project.NewState(
		"demo", "deliver-pr",
		[]string{"clarify", "plan", "implement", "simplify", "review", "validate", "open-pr"},
	)
	if err != nil {
		t.Fatal(err)
	}
	state.Version = project.WorkflowStateVersion
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runStateRaw(t, "set", "demo", "clarify", "done"); err != nil {
		t.Fatalf("historical legacy state was treated as adaptive: %v", err)
	}
}
