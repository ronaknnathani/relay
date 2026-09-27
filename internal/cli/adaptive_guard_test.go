package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/project"
)

func TestAdaptiveManifestVersionZeroStateCannotUseLegacyCommands(t *testing.T) {
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
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Version = 0
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}

	if _, err := runStateRaw(
		t, "set", "demo", "open-pr", "done",
	); err == nil || !strings.Contains(err.Error(), "adaptive delivery cannot use state set") {
		t.Fatalf("version-zero adaptive state used legacy transition: %v", err)
	}
	loaded, _, err := loadStateAt("demo")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != project.WorkflowStateVersion || !loaded.UsesAdaptiveDelivery() {
		t.Fatalf("adaptive state was not upgraded before guard checks: %+v", loaded)
	}
}
