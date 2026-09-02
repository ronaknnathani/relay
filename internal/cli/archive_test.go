package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/project"
)

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

func TestArchiveRejectsUnmergedBranchBeforeDirtyWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "unmerged-dirty"
	branch := "user/unmerged-dirty"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "unique\n", "unique work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	writeArchiveFile(t, worktree, "notes.txt", "dirty\n")

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
	if archivedManifest.Merged {
		t.Fatal("force-archived unmerged work was recorded as merged")
	}
}

func TestArchiveRecordsVerifiedMergedBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "merged-work"
	branch := "user/merged-work"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "merged\n", "merged work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	runArchiveGit(t, repo, "merge", "-q", "--ff-only", branch)

	if _, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	}); err != nil {
		t.Fatalf("runArchive merged: %v", err)
	}
	if archived := loadArchivedManifest(t, slug); !archived.Merged {
		t.Fatalf("archived manifest merged = false, want true")
	}
}

func TestArchiveDoesNotMarkEmptyReachableBranchMerged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "empty-work"
	branch := "user/empty-work"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)

	if _, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	}); err != nil {
		t.Fatalf("runArchive empty branch: %v", err)
	}
	if archived := loadArchivedManifest(t, slug); archived.Merged {
		t.Fatal("empty branch was recorded as merged work")
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
		StartSHA:   gitOutput(t, repo, "rev-parse", "main"),
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

func TestArchiveRecordsSquashMergedPullRequestFromGitHub(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "squashed-work"
	branch := "user/squashed-work"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "squashed\n", "squashed work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	recordArchiveManifestPR(t, slug, 404)
	installArchivePRIndex(t, map[string]programview.PRState{"#404": programview.PRStateMerged})

	if _, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	}); err != nil {
		t.Fatalf("runArchive squash-merged pull request: %v", err)
	}
	archived := loadArchivedManifest(t, slug)
	if !archived.Merged {
		t.Fatal("squash-merged pull request was not recorded as merged")
	}
	if gitx.BranchExists(repo, branch) {
		t.Fatal("squash-merged branch was left behind")
	}
}

func TestArchiveStillProtectsUnmergedPullRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "closed-work"
	branch := "user/closed-work"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "closed\n", "closed work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	recordArchiveManifestPR(t, slug, 405)
	installArchivePRIndex(t, map[string]programview.PRState{"#405": programview.PRStateClosed})

	_, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	})
	if err == nil || !strings.Contains(err.Error(), "unmerged work") {
		t.Fatalf("runArchive error = %v, want unmerged branch protection", err)
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
}

// A local branch that is already deleted is not evidence of abandoned work: the
// branch is usually gone precisely because its pull request merged.
func TestArchiveRecordsMergedPullRequestWhenTheLocalBranchIsGone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "deleted-branch-work"
	branch := "user/deleted-branch-work"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "merged\n", "merged work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	recordArchiveManifestPR(t, slug, 406)
	installArchivePRIndex(t, map[string]programview.PRState{"#406": programview.PRStateMerged})
	runArchiveGit(t, repo, "worktree", "remove", "--force", worktree)
	runArchiveGit(t, repo, "branch", "-D", branch)

	if _, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	}); err != nil {
		t.Fatalf("runArchive with a deleted local branch: %v", err)
	}
	archived := loadArchivedManifest(t, slug)
	if !archived.Merged {
		t.Fatal("merged pull request with a deleted local branch was not recorded as merged")
	}
	if archived.Status != "archived" {
		t.Fatalf("archived status = %q, want archived", archived.Status)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("an orphan active project directory was left behind")
	}
}

func recordArchiveManifestPR(t *testing.T, slug string, number int) {
	t.Helper()
	path := project.ManifestPath(project.ActiveDir(), slug)
	manifest, err := project.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest.PR = project.PRInfo{Number: &number}
	if err := project.Save(path, manifest); err != nil {
		t.Fatal(err)
	}
}

func installArchivePRIndex(t *testing.T, states map[string]programview.PRState) {
	t.Helper()
	previous := loadArchivePRIndex
	loadArchivePRIndex = func(string, []string) programview.PRIndex {
		return prIndexStub(func(ref string) (programview.PRState, bool) {
			state, found := states[ref]
			return state, found
		})
	}
	t.Cleanup(func() { loadArchivePRIndex = previous })
}
