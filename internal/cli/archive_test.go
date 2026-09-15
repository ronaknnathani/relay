package cli

import (
	"errors"
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

func TestArchiveDoesNotRecordMergedPullRequestWhenTheLocalBranchIsGone(t *testing.T) {
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
	if archived.Merged {
		t.Fatal("archive recorded an unverifiable pull request as merged")
	}
	if archived.Status != "archived" {
		t.Fatalf("archived status = %q, want archived", archived.Status)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("an orphan active project directory was left behind")
	}
}

func TestResolveRecordedPullRequestMergeReturnsStateReadError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	slug := "malformed-state"
	statePath := project.StatePath(slug)
	if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}

	merged, err := resolveRecordedPullRequestMerge(project.Manifest{Slug: slug}, slug)
	if err == nil {
		t.Fatal("resolveRecordedPullRequestMerge error = nil")
	}
	if merged {
		t.Fatal("resolveRecordedPullRequestMerge reported malformed state as merged")
	}
	if !strings.Contains(err.Error(), statePath) {
		t.Fatalf("resolveRecordedPullRequestMerge error = %q, want state path", err)
	}
	if recordedPullRequestMerged(project.Manifest{Slug: slug}, slug) {
		t.Fatal("recordedPullRequestMerged should remain conservative on state errors")
	}
}

func TestResolveRecordedPullRequestMergeRequiresMatchingRepositoryAndHead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "verified-pr"
	branch := "user/verified-pr"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "verified\n", "verified work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	recordArchiveManifestPR(t, slug, 407)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	branchTip := gitx.RevParse(repo, "refs/heads/"+branch)

	tests := []struct {
		name       string
		repository string
		headSHA    string
		wantMerged bool
		wantError  string
	}{
		{
			name:       "matching proof",
			repository: "acme/widgets",
			headSHA:    branchTip,
			wantMerged: true,
		},
		{
			name:       "different repository",
			repository: "acme/other",
			headSHA:    branchTip,
			wantError:  "repository",
		},
		{
			name:       "different head",
			repository: "acme/widgets",
			headSHA:    manifest.StartSHA,
			wantError:  "head",
		},
		{
			name:       "missing head",
			repository: "acme/widgets",
			wantError:  "head",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previousProof := loadArchivePullRequestProof
			previousRepository := loadArchiveRepository
			loadArchivePullRequestProof = func(string, string) (programview.PullRequestProof, error) {
				return programview.PullRequestProof{
					State:      programview.PRStateMerged,
					Repository: test.repository,
					HeadSHA:    test.headSHA,
				}, nil
			}
			loadArchiveRepository = func(string) (string, error) {
				return "acme/widgets", nil
			}
			t.Cleanup(func() {
				loadArchivePullRequestProof = previousProof
				loadArchiveRepository = previousRepository
			})

			merged, err := resolveRecordedPullRequestMerge(manifest, slug)
			if merged != test.wantMerged {
				t.Fatalf("merged = %t, want %t", merged, test.wantMerged)
			}
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("resolveRecordedPullRequestMerge: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q diagnostic", err, test.wantError)
			}
		})
	}
}

func TestArchiveForceDeletesBranchMergedOnlyIntoRemoteBase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	runArchiveGit(t, filepath.Dir(remote), "init", "-q", "--bare", "--initial-branch=main", remote)
	runArchiveGit(t, repo, "remote", "add", "origin", remote)
	runArchiveGit(t, repo, "push", "-q", "-u", "origin", "main")

	slug := "remote-only-merge"
	branch := "user/remote-only-merge"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "merged upstream\n", "merged upstream")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	runArchiveGit(t, repo, "push", "-q", "origin", branch+":main")

	result, err := archiveProject(slug, true)
	if err != nil {
		t.Fatalf("archiveProject --force: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("archive warnings = %v, want none", result.Warnings)
	}
	if !result.BranchDeleted || gitx.BranchExists(repo, branch) {
		t.Fatalf("branch %q survived forced archive", branch)
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
	proofs := make(map[string]programview.PullRequestProof, len(states))
	manifests, err := project.LoadAll(project.ActiveDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, manifest := range manifests {
		hasPR, ref, err := programview.RecordedPR(manifest, project.StatePath(manifest.Slug))
		if err != nil {
			t.Fatal(err)
		}
		state, found := states[ref]
		if !hasPR || !found {
			continue
		}
		proofs[ref] = programview.PullRequestProof{
			State:      state,
			Repository: "acme/widgets",
			HeadSHA:    gitx.RevParse(manifest.Repo, "refs/heads/"+manifest.Branch),
		}
	}
	previousProof := loadArchivePullRequestProof
	previousRepository := loadArchiveRepository
	loadArchivePullRequestProof = func(_, ref string) (programview.PullRequestProof, error) {
		proof, found := proofs[ref]
		if !found {
			return programview.PullRequestProof{}, errors.New("pull request not found")
		}
		return proof, nil
	}
	loadArchiveRepository = func(string) (string, error) {
		return "acme/widgets", nil
	}
	t.Cleanup(func() {
		loadArchivePullRequestProof = previousProof
		loadArchiveRepository = previousRepository
	})
}

func installArchivePRLookupError(t *testing.T, lookupErr error) {
	t.Helper()
	previous := loadArchivePullRequestProof
	loadArchivePullRequestProof = func(string, string) (programview.PullRequestProof, error) {
		return programview.PullRequestProof{}, lookupErr
	}
	t.Cleanup(func() { loadArchivePullRequestProof = previous })
}

func TestArchiveProjectReturnsAResultWithoutWritingToStdout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "result-archive"
	branch := "user/result-archive"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)

	var result archiveResult
	out, err := captureStdout(t, func() error {
		var archiveErr error
		result, archiveErr = archiveProject(slug, false)
		return archiveErr
	})
	if err != nil {
		t.Fatalf("archiveProject: %v", err)
	}
	if out != "" {
		t.Fatalf("archiveProject wrote %q to stdout", out)
	}
	if result.Slug != slug || result.Branch != branch {
		t.Fatalf("result identity = %q/%q", result.Slug, result.Branch)
	}
	if !result.WorktreeRemoved || result.Worktree != worktree {
		t.Fatalf("worktree result = %t/%q", result.WorktreeRemoved, result.Worktree)
	}
	if !result.BranchDeleted {
		t.Fatalf("branch %q was not deleted: %v", branch, result.Warnings)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", result.Warnings)
	}
	if result.ArchivedPath != filepath.Join(project.ArchivedDir(), slug) {
		t.Fatalf("archived path = %q", result.ArchivedPath)
	}
	if pathExists(worktree) || pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("archiveProject left the worktree or active project behind")
	}
}

func TestArchiveForceDiscardsDirtyAndUntrackedWork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "force-discard"
	branch := "user/force-discard"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "unique\n", "unique work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	writeArchiveFile(t, worktree, "feature.txt", "dirty edit\n")
	writeArchiveFile(t, worktree, "scratch/notes.txt", "untracked\n")

	result, err := archiveProject(slug, true)
	if err != nil {
		t.Fatalf("archiveProject --force: %v", err)
	}
	if !result.WorktreeRemoved {
		t.Fatal("the dirty worktree was not removed")
	}
	if pathExists(worktree) {
		t.Fatalf("worktree %s survived a forced archive", worktree)
	}
	if gitx.BranchExists(repo, branch) {
		t.Fatalf("unmerged branch %s survived a forced archive", branch)
	}
	if !pathExists(filepath.Join(project.ArchivedDir(), slug)) {
		t.Fatal("the project was not moved to archived")
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("the project is still active after archiving")
	}
}

func TestRunArchiveKeepsItsTextOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "text-archive"
	branch := "user/text-archive"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)

	out, err := captureStdout(t, func() error { return runArchive(slug, false) })
	if err != nil {
		t.Fatalf("runArchive: %v", err)
	}
	for _, want := range []string{"Archived:", slug, "Worktree removed:", worktree} {
		if !strings.Contains(out, want) {
			t.Fatalf("archive output %q is missing %q", out, want)
		}
	}
}
