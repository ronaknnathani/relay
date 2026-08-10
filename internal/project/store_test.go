package project

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	worktree := "/tmp/wt"
	prNum := 42
	prURL := "https://example/pr/42"
	original := Manifest{
		Slug:            "demo",
		Title:           "demo project",
		Repo:            "/repo",
		Branch:          "ronaknnathani/demo",
		Agent:           "claude",
		BaseBranch:      "main",
		StartSHA:        "abc123",
		Worktree:        &worktree,
		Status:          "initialized",
		Phase:           "plan",
		Created:         "2026-05-12T00:00:00Z",
		PR:              PRInfo{Number: &prNum, URL: &prURL},
		PhasesCompleted: []string{"init"},
		PhasesRemaining: []string{"plan", "implement"},
	}
	if err := Save(path, original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Updated is rewritten on Save; clear before compare.
	loaded.Updated = ""
	original.Updated = ""
	if !reflect.DeepEqual(original, loaded) {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", loaded, original)
	}
}

func TestFindNotFound(t *testing.T) {
	if _, err := Find("definitely-not-a-real-slug-xyz123"); err == nil {
		t.Error("expected error, got nil")
	}
}
