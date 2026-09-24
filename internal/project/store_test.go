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

func TestLoadAllResultsReportsReadableManifestPaths(t *testing.T) {
	dir := t.TempDir()
	validDir := filepath.Join(dir, "a-valid")
	malformedDir := filepath.Join(dir, "b-malformed")
	missingDir := filepath.Join(dir, "c-missing")
	unreadableDir := filepath.Join(dir, "d-unreadable")
	danglingDir := filepath.Join(dir, "e-dangling")
	for _, path := range []string{validDir, malformedDir, missingDir, unreadableDir, danglingDir} {
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
	if err := os.Mkdir(filepath.Join(unreadableDir, "manifest.json"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(danglingDir, "missing-target"),
		filepath.Join(danglingDir, "manifest.json"),
	); err != nil {
		t.Fatal(err)
	}

	results, err := LoadAllResults(dir)
	if err != nil {
		t.Fatalf("LoadAllResults: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("len(results) = %d, want 4", len(results))
	}
	for _, result := range results {
		if result.Name == "c-missing" {
			t.Fatal("directory without manifest.json was reported as a project")
		}
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
	if results[2].Name != "d-unreadable" ||
		results[2].Path != filepath.Join(unreadableDir, "manifest.json") ||
		results[2].Err == nil ||
		!strings.Contains(results[2].Err.Error(), "not a regular file") {
		t.Fatalf("unreadable result = %+v", results[2])
	}
	if results[3].Name != "e-dangling" ||
		results[3].Path != filepath.Join(danglingDir, "manifest.json") ||
		results[3].Err == nil ||
		!strings.Contains(results[3].Err.Error(), "not a regular file") {
		t.Fatalf("dangling result = %+v", results[3])
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

func TestLoadAllEffectiveUsesDirectoryIdentityAndIsolatesInvalidProjects(t *testing.T) {
	dir := t.TempDir()
	for _, slug := range []string{"healthy", "mismatch", "invalid-state", "wrong-state"} {
		if err := os.MkdirAll(filepath.Join(dir, slug), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(ManifestPath(dir, "healthy"), Manifest{Slug: "healthy", Title: "Healthy"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(ManifestPath(dir, "mismatch"), Manifest{Slug: "healthy", Title: "Wrong"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(ManifestPath(dir, "invalid-state"), Manifest{Slug: "invalid-state", Title: "Broken"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(ManifestPath(dir, "wrong-state"), Manifest{Slug: "wrong-state", Title: "Wrong state"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "invalid-state", "state.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := NewState("another-project", "custom", []string{"work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveState(filepath.Join(dir, "wrong-state", "state.json"), state); err != nil {
		t.Fatal(err)
	}

	manifests, warnings, err := LoadAllEffective(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].Slug != "healthy" {
		t.Fatalf("isolated manifests = %+v", manifests)
	}
	if len(warnings) != 3 {
		t.Fatalf("warnings = %v, want manifest, parse, and state slug warnings", warnings)
	}
}

func TestLoadEffectivePreservesHistoricalStackState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "stack")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	manifest := Manifest{
		Slug: "stack", Title: "Historical stack", Workflow: "stack-ship", Phase: "monitor",
	}
	if err := Save(path, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "state.json"),
		[]byte(`{"goalSlug":"stack","frontPr":42,"prs":[{"number":42}]}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	got, err := LoadEffective(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != manifest.Slug || got.Workflow != manifest.Workflow || got.Phase != manifest.Phase {
		t.Fatalf("historical stack manifest changed: %+v", got)
	}
}
