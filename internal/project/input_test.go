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
}
