package program

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverMissingRoot(t *testing.T) {
	programs, diagnostics, err := Discover(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(programs) != 0 || len(diagnostics) != 0 {
		t.Fatalf("Discover = (%+v, %+v), want empty results", programs, diagnostics)
	}
}

func TestDiscoverMixedPrograms(t *testing.T) {
	setTestHome(t)
	root := ActiveDir()
	for _, slug := range []string{"zulu", "alpha"} {
		p := newTestProgram(t)
		p.Slug = slug
		p.Title = slug
		if err := Create(p); err != nil {
			t.Fatalf("Create(%s): %v", slug, err)
		}
	}

	malformedDir := ProgramDir(root, "malformed")
	if err := os.MkdirAll(malformedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformedDir, "program.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	mismatch := newTestProgram(t)
	mismatch.Slug = "manifest-slug"
	mismatch.Title = "mismatch"
	mismatchDir := ProgramDir(root, "directory-slug")
	if err := os.MkdirAll(mismatchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := encodeProgram(mismatch)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mismatchDir, "program.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	programs, diagnostics, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := []string{programs[0].Slug, programs[1].Slug}; got[0] != "alpha" || got[1] != "zulu" {
		t.Fatalf("program slugs = %v, want [alpha zulu]", got)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v, want 2", diagnostics)
	}
	if diagnostics[0].Directory != "directory-slug" ||
		!strings.Contains(diagnostics[0].Err.Error(), "manifest slug") {
		t.Fatalf("first diagnostic = %+v", diagnostics[0])
	}
	if diagnostics[1].Directory != "malformed" ||
		!strings.Contains(diagnostics[1].Err.Error(), "parse program") {
		t.Fatalf("second diagnostic = %+v", diagnostics[1])
	}
}

func TestDiscoverUnreadableRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read permissionless directories")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(root, 0o755); err != nil {
			t.Errorf("restore root permissions: %v", err)
		}
	})

	_, _, err := Discover(root)
	if err == nil || !strings.Contains(err.Error(), "read programs directory") {
		t.Fatalf("Discover error = %v, want unreadable-root error", err)
	}
}
