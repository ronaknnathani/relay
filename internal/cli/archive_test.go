package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/agentsmd"
	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/project"
)

const relayGeneratedAgentsMD = "# relay\n\nActive relay project: archive-test. Workflow: deliver-pr. Mode: full.\n"

func TestArchiveAllowsOnlyRelayGeneratedAgentsMDWithoutForce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "safe-agents"
	branch := "user/safe-agents"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	writeArchiveFile(t, worktree, "AGENTS.md", relayGeneratedAgentsMD)

	out, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	})
	if err != nil {
		t.Fatalf("runArchive: %v", err)
	}

	if strings.Contains(out, "--force") {
		t.Fatalf("archive output = %q, want no --force hint", out)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatalf("active project dir still exists")
	}
	if pathExists(worktree) {
		t.Fatalf("worktree dir still exists")
	}
	if gitx.BranchExists(repo, branch) {
		t.Fatalf("branch %q still exists", branch)
	}
	archivedManifest := loadArchivedManifest(t, slug)
	if archivedManifest.Status != "archived" {
		t.Fatalf("archived status = %q, want archived", archivedManifest.Status)
	}
	if archivedManifest.Archived == nil || *archivedManifest.Archived == "" {
		t.Fatalf("archived timestamp was not set")
	}
}

func TestArchiveCleansManagedAgentsMDOnTrackedExistingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	writeArchiveFile(t, repo, "AGENTS.md", "# project\n")
	runArchiveGit(t, repo, "add", "AGENTS.md")
	runArchiveGit(t, repo, "commit", "-q", "-m", "add agents")
	slug := "managed-existing-agents"
	branch := "user/managed-existing-agents"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	applyManagedArchiveAgentsMD(t, slug, worktree, "Active relay project: archive-test.")

	if _, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	}); err != nil {
		t.Fatalf("runArchive: %v", err)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatalf("active project dir still exists")
	}
	if pathExists(worktree) {
		t.Fatalf("worktree dir still exists")
	}
	if archivedManifest := loadArchivedManifest(t, slug); archivedManifest.AgentsMD != nil {
		t.Fatalf("archived manifest AgentsMD = %+v, want nil", archivedManifest.AgentsMD)
	}
}

func TestArchiveRemovesManagedRelayCreatedAgentsMD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "managed-created-agents"
	branch := "user/managed-created-agents"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	applyManagedArchiveAgentsMD(t, slug, worktree, "Active relay project: archive-test.")

	if _, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	}); err != nil {
		t.Fatalf("runArchive: %v", err)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatalf("active project dir still exists")
	}
	if pathExists(worktree) {
		t.Fatalf("worktree dir still exists")
	}
}

func TestArchivePreservesSeparableManagedAgentsMDEdits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	writeArchiveFile(t, repo, "AGENTS.md", "# project\n")
	runArchiveGit(t, repo, "add", "AGENTS.md")
	runArchiveGit(t, repo, "commit", "-q", "-m", "add agents")
	slug := "managed-edited-agents"
	branch := "user/managed-edited-agents"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	applyManagedArchiveAgentsMD(t, slug, worktree, "Active relay project: archive-test.")
	appendArchiveFile(t, worktree, "AGENTS.md", "Session note\n")

	_, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	})
	if err == nil {
		t.Fatalf("runArchive succeeded, want dirty worktree after preserving AGENTS.md edits")
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
	data := readArchiveFile(t, worktree, "AGENTS.md")
	if data != "# project\nSession note\n" {
		t.Fatalf("AGENTS.md after archive cleanup = %q, want preserved session note", data)
	}
}

func TestArchiveConflictsOnEditedManagedAgentsMDBlock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "managed-conflict-agents"
	branch := "user/managed-conflict-agents"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	applyManagedArchiveAgentsMD(t, slug, worktree, "Active relay project: archive-test.")
	before := readArchiveFile(t, worktree, "AGENTS.md")
	edited := strings.Replace(before, "archive-test", "edited", 1)
	writeArchiveFile(t, worktree, "AGENTS.md", edited)

	_, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	})
	if err == nil || !strings.Contains(err.Error(), "AGENTS.md") {
		t.Fatalf("runArchive error = %v, want AGENTS.md conflict", err)
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
	if got := readArchiveFile(t, worktree, "AGENTS.md"); got != edited {
		t.Fatalf("AGENTS.md changed on conflict = %q, want %q", got, edited)
	}
}

func TestArchiveRejectsRelayGeneratedAgentsMDWithOtherDirtyFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "dirty-agents"
	branch := "user/dirty-agents"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	writeArchiveFile(t, worktree, "AGENTS.md", relayGeneratedAgentsMD)
	writeArchiveFile(t, worktree, "notes.txt", "keep me\n")

	_, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	})
	if err == nil {
		t.Fatalf("runArchive succeeded, want dirty worktree error")
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
}

func TestArchiveRejectsNonGeneratedAgentsMDWithoutForce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "user-agents"
	branch := "user/user-agents"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	writeArchiveFile(t, worktree, "AGENTS.md", "# project\n\nPlease keep this.\n")

	_, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	})
	if err == nil {
		t.Fatalf("runArchive succeeded, want non-generated AGENTS.md to be preserved")
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
}

func TestArchiveRejectsUnmergedBranchBeforeGeneratedAgentsMD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "unmerged-agents"
	branch := "user/unmerged-agents"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "unique\n", "unique work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	writeArchiveFile(t, worktree, "AGENTS.md", relayGeneratedAgentsMD)

	_, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	})
	if err == nil || !strings.Contains(err.Error(), "unmerged work") {
		t.Fatalf("runArchive error = %v, want unmerged branch protection", err)
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
}

func TestArchiveForceKeepsDirtyUnmergedBehavior(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "force-dirty"
	branch := "user/force-dirty"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "unique\n", "unique work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	writeArchiveFile(t, worktree, "AGENTS.md", "# project\n\nPlease keep this.\n")

	if _, err := captureStdout(t, func() error {
		return runArchive(slug, true)
	}); err != nil {
		t.Fatalf("runArchive --force: %v", err)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatalf("active project dir still exists")
	}
	if pathExists(worktree) {
		t.Fatalf("worktree dir still exists")
	}
	if gitx.BranchExists(repo, branch) {
		t.Fatalf("branch %q still exists", branch)
	}
	archivedManifest := loadArchivedManifest(t, slug)
	if archivedManifest.Status != "archived" {
		t.Fatalf("archived status = %q, want archived", archivedManifest.Status)
	}
	if archivedManifest.Archived == nil || *archivedManifest.Archived == "" {
		t.Fatalf("archived timestamp was not set")
	}
}

func addArchiveWorktree(t *testing.T, repo, slug, branch string) string {
	t.Helper()
	worktree := filepath.Join(repo, ".worktrees", slug)
	runArchiveGit(t, repo, "worktree", "add", "-q", worktree, "-b", branch, "HEAD")
	return worktree
}

func writeArchiveManifest(t *testing.T, slug, repo, branch, worktree string) {
	t.Helper()
	projDir := filepath.Join(project.ActiveDir(), slug)
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	m := project.Manifest{
		Slug:       slug,
		Title:      slug,
		Repo:       repo,
		Branch:     branch,
		BaseBranch: "main",
		Worktree:   &worktree,
		Status:     "active",
		Created:    now,
		Updated:    now,
	}
	if err := project.Save(filepath.Join(projDir, "manifest.json"), m); err != nil {
		t.Fatalf("save archive manifest: %v", err)
	}
}

func writeArchiveFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create parent dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func appendArchiveFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

func readArchiveFile(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func applyManagedArchiveAgentsMD(t *testing.T, slug, worktree, prompt string) {
	t.Helper()
	if err := agentsmd.Apply(worktree, filepath.Join(project.ActiveDir(), slug), prompt); err != nil {
		t.Fatalf("apply managed AGENTS.md: %v", err)
	}
}

func commitArchiveFile(t *testing.T, worktree, name, content, message string) {
	t.Helper()
	writeArchiveFile(t, worktree, name, content)
	runArchiveGit(t, worktree, "add", name)
	runArchiveGit(t, worktree, "commit", "-q", "-m", message)
}

func loadArchivedManifest(t *testing.T, slug string) project.Manifest {
	t.Helper()
	m, err := project.Load(filepath.Join(project.ArchivedDir(), slug, "manifest.json"))
	if err != nil {
		t.Fatalf("load archived manifest: %v", err)
	}
	return m
}

func assertArchivePreserved(t *testing.T, repo, slug, branch, worktree string) {
	t.Helper()
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatalf("active project dir was removed")
	}
	if pathExists(filepath.Join(project.ArchivedDir(), slug)) {
		t.Fatalf("archived project dir exists")
	}
	if !pathExists(worktree) {
		t.Fatalf("worktree dir was removed")
	}
	if !gitx.BranchExists(repo, branch) {
		t.Fatalf("branch %q was removed", branch)
	}
}

func runArchiveGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
		"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
}
