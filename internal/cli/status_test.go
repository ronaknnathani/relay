package cli

import (
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
