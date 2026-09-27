package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectInputRevisionTracksNormalizedDeliveryInputs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte("task\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := ProjectInputRevision(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte("task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	normalized, err := ProjectInputRevision(dir)
	if err != nil {
		t.Fatal(err)
	}
	if normalized != first {
		t.Fatal("line-ending-only input change altered the normalized revision")
	}
	if err := os.WriteFile(filepath.Join(dir, "requirements.md"), []byte("new requirement\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := ProjectInputRevision(dir)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("requirements change did not alter the input revision")
	}
	if err := os.WriteFile(filepath.Join(dir, "assignment.md"), []byte("new assignment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assignmentChanged, err := ProjectInputRevision(dir)
	if err != nil {
		t.Fatal(err)
	}
	if assignmentChanged == changed {
		t.Fatal("assignment change did not alter the input revision")
	}
}

func TestProjectInputRevisionFramesNULContainingInputsUnambiguously(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	separator := "\x00requirements.md\x00present\x00"
	if err := os.WriteFile(filepath.Join(first, "task.md"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(first, "requirements.md"),
		[]byte("x\n"+separator+"y\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(second, "task.md"),
		[]byte("a\n"+separator+"x\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "requirements.md"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	firstRevision, err := ProjectInputRevision(first)
	if err != nil {
		t.Fatal(err)
	}
	secondRevision, err := ProjectInputRevision(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstRevision == secondRevision {
		t.Fatal("distinct NUL-containing project inputs produced the same revision")
	}
}
