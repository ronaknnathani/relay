package project

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		Workflow:        "deliver-pr",
		Program:         "relay-v1",
		ProgramItem:     "w1",
		Merged:          true,
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

func TestLoadAllResultsReportsEveryProjectDirectory(t *testing.T) {
	dir := t.TempDir()
	validDir := filepath.Join(dir, "a-valid")
	malformedDir := filepath.Join(dir, "b-malformed")
	missingDir := filepath.Join(dir, "c-missing")
	for _, path := range []string{validDir, malformedDir, missingDir} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	valid := Manifest{Slug: "a-valid", Repo: "/repo", Branch: "user/a-valid"}
	if err := Save(filepath.Join(validDir, "manifest.json"), valid); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformedDir, "manifest.json"), []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}

	results, err := LoadAllResults(dir)
	if err != nil {
		t.Fatalf("LoadAllResults: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}
	if results[0].Name != "a-valid" ||
		results[0].Path != filepath.Join(validDir, "manifest.json") ||
		results[0].Manifest.Slug != "a-valid" ||
		results[0].Err != nil {
		t.Fatalf("valid result = %+v", results[0])
	}
	if results[1].Name != "b-malformed" ||
		results[1].Path != filepath.Join(malformedDir, "manifest.json") ||
		results[1].Err == nil ||
		!strings.Contains(results[1].Err.Error(), "parse manifest") {
		t.Fatalf("malformed result = %+v", results[1])
	}
	if results[2].Name != "c-missing" ||
		results[2].Path != filepath.Join(missingDir, "manifest.json") ||
		results[2].Err == nil ||
		!strings.Contains(results[2].Err.Error(), "read manifest") {
		t.Fatalf("missing result = %+v", results[2])
	}
}

func TestLoadAllResultsMissingDirectory(t *testing.T) {
	results, err := LoadAllResults(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("LoadAllResults: %v", err)
	}
	if results != nil {
		t.Fatalf("results = %#v, want nil", results)
	}
}

func TestLoadAllStillFiltersInvalidManifests(t *testing.T) {
	dir := t.TempDir()
	validDir := filepath.Join(dir, "valid")
	malformedDir := filepath.Join(dir, "malformed")
	for _, path := range []string{validDir, malformedDir} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	valid := Manifest{Slug: "valid", Repo: "/repo", Branch: "user/valid"}
	if err := Save(filepath.Join(validDir, "manifest.json"), valid); err != nil {
		t.Fatal(err)
	}
	valid, err := Load(filepath.Join(validDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformedDir, "manifest.json"), []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}

	manifests, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if !reflect.DeepEqual(manifests, []Manifest{valid}) {
		t.Fatalf("manifests = %+v, want %+v", manifests, []Manifest{valid})
	}
}
