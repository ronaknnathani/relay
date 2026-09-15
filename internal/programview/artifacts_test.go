package programview

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

func TestReadArtifactRejectsEscapesAndSymlinksOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readArtifact(root, "../secret.md", 1024, true); err == nil {
		t.Fatal("relative path escape was accepted")
	}
	link := filepath.Join(root, "context.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readArtifact(root, "context.md", 1024, true); err == nil {
		t.Fatal("symlink escape was accepted")
	}
}

func TestLoadArtifactStatesAndSelectors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	at := "2026-09-14T20:00:00Z"
	p := program.Program{
		Revision: 1, Slug: "artifact-states", Title: "Artifact states",
		Repo: repo, State: program.StateActive, Agent: "copilot", MaxOpenPRs: 1,
		CreatedAt: at, UpdatedAt: at, ApprovalRequestedAt: at, ApprovedAt: at, ApprovedBy: "test",
		Items: []program.WorkItem{{
			ID: "w1", Kind: program.ItemKindChange, Title: "task",
			Priority: program.PriorityP1, Status: program.ItemDispatched,
			Dependencies: []string{}, ContractRefs: []string{"api@v1"},
			Repo: repo, ProjectSlug: "artifact-child", Notes: []string{},
			CreatedAt: at, UpdatedAt: at, DispatchedAt: at,
		}},
		Contracts: []program.Contract{{
			Name: "api", Version: 1, Ref: "api@v1", Path: "contracts/api/v1.md",
			SHA256: "abc", Status: program.ContractApproved, PublishedAt: at,
			ApprovedAt: at, ApprovedBy: "test",
		}},
		Decisions: []program.Decision{},
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	childDir := filepath.Join(project.ActiveDir(), "artifact-child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(repo, ".worktrees", "artifact-child")
	if err := project.Save(project.ManifestPath(project.ActiveDir(), "artifact-child"), project.Manifest{
		Slug: "artifact-child", Title: "Artifact child", Repo: repo,
		Branch: "feature", BaseBranch: "main", Worktree: &worktree,
		Status: "active", Workflow: "deliver-pr", Program: p.Slug, ProgramItem: "w1",
		Phase: "implement", Created: at, Updated: at,
		PhasesCompleted: []string{}, PhasesRemaining: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, "assignment.md"), []byte("loaded"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, "task.md"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, "plan.md"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(childDir, "context.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(program.ProgramDir(program.ActiveDir(), p.Slug), "contracts", "api", "v1.md")
	if err := os.MkdirAll(filepath.Dir(contractPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contractPath, []byte("contract"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		selector ArtifactSelector
		state    ArtifactState
		text     string
		size     int64
		limit    int64
		wantErr  bool
	}{
		{name: "loaded task", selector: ArtifactSelector{Kind: ArtifactKindTask, Item: "w1", Name: "assignment.md"}, state: ArtifactStateLoaded, text: "loaded", size: 6, limit: 128 * 1024},
		{name: "empty task", selector: ArtifactSelector{Kind: ArtifactKindTask, Item: "w1", Name: "task.md"}, state: ArtifactStateEmpty, text: "", size: 0, limit: 128 * 1024},
		{name: "missing task", selector: ArtifactSelector{Kind: ArtifactKindTask, Item: "w1", Name: "notes.md"}, state: ArtifactStateMissing, limit: 128 * 1024},
		{name: "truncated task", selector: ArtifactSelector{Kind: ArtifactKindTask, Item: "w1", Name: "plan.md"}, state: ArtifactStateTruncated, text: "1234", size: 5, limit: 4},
		{name: "read error", selector: ArtifactSelector{Kind: ArtifactKindTask, Item: "w1", Name: "context.md"}, state: ArtifactStateError, limit: 128 * 1024, wantErr: true},
		{name: "loaded contract", selector: ArtifactSelector{Kind: ArtifactKindContract, Ref: "api@v1"}, state: ArtifactStateLoaded, text: "contract", size: 8, limit: 128 * 1024},
		{name: "invalid task name", selector: ArtifactSelector{Kind: ArtifactKindTask, Item: "w1", Name: "../goal.md"}, wantErr: true},
		{name: "unknown task", selector: ArtifactSelector{Kind: ArtifactKindTask, Item: "w9", Name: "task.md"}, wantErr: true},
		{name: "unknown contract", selector: ArtifactSelector{Kind: ArtifactKindContract, Ref: "missing@v1"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := LoadArtifact(p.Slug, test.selector, test.limit)
			if (err != nil) != test.wantErr {
				t.Fatalf("LoadArtifact error = %v, wantErr %v", err, test.wantErr)
			}
			if got.State != test.state {
				t.Fatalf("state = %q, want %q", got.State, test.state)
			}
			if artifactText(got.Artifact) != test.text || got.Artifact.Size != test.size {
				t.Fatalf("artifact = %+v, want text %q size %d", got.Artifact, test.text, test.size)
			}
		})
	}
}
