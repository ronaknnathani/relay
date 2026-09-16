package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/project"
)

type gcRepoFixture struct {
	remote   string
	upstream string
	repo     string
	base     string
	startSHA string
}

func newGCRepoFixture(t *testing.T, base string) gcRepoFixture {
	t.Helper()
	root := t.TempDir()
	fixture := gcRepoFixture{
		remote:   filepath.Join(root, "origin.git"),
		upstream: filepath.Join(root, "upstream"),
		repo:     filepath.Join(root, "repo"),
		base:     base,
	}
	runArchiveGit(t, root, "init", "-q", "--bare", "--initial-branch="+base, fixture.remote)
	runArchiveGit(t, root, "--git-dir="+fixture.remote, "config", "receive.denyDeleteCurrent", "ignore")
	runArchiveGit(t, root, "init", "-q", "-b", base, fixture.upstream)
	writeArchiveFile(t, fixture.upstream, "README", "initial\n")
	runArchiveGit(t, fixture.upstream, "add", "README")
	runArchiveGit(t, fixture.upstream, "commit", "-q", "-m", "initial")
	runArchiveGit(t, fixture.upstream, "remote", "add", "origin", fixture.remote)
	runArchiveGit(t, fixture.upstream, "push", "-q", "-u", "origin", base)
	runArchiveGit(t, root, "clone", "-q", fixture.remote, fixture.repo)
	fixture.startSHA = gitOutput(t, fixture.repo, "rev-parse", base)
	return fixture
}

func addGCProject(t *testing.T, fixture gcRepoFixture, slug string) (branch, worktree string) {
	t.Helper()
	branch = "user/" + slug
	worktree = filepath.Join(fixture.repo, ".worktrees", slug)
	runArchiveGit(t, fixture.repo, "worktree", "add", "-q", worktree, "-b", branch, fixture.startSHA)
	commitArchiveFile(t, worktree, slug+".txt", slug+"\n", slug)
	writeGCManifest(t, slug, fixture, branch, worktree)
	return branch, worktree
}

func addGCBranch(t *testing.T, fixture gcRepoFixture, name string) (branch, worktree string) {
	t.Helper()
	branch = "user/" + name
	worktree = filepath.Join(fixture.repo, ".worktrees", name)
	runArchiveGit(t, fixture.repo, "worktree", "add", "-q", worktree, "-b", branch, fixture.startSHA)
	commitArchiveFile(t, worktree, name+".txt", name+"\n", name)
	return branch, worktree
}

func writeGCManifest(t *testing.T, slug string, fixture gcRepoFixture, branch, worktree string) {
	t.Helper()
	dir := filepath.Join(project.ActiveDir(), slug)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	manifest := project.Manifest{
		Slug:       slug,
		Title:      slug,
		Repo:       fixture.repo,
		Branch:     branch,
		BaseBranch: fixture.base,
		StartSHA:   fixture.startSHA,
		Worktree:   &worktree,
		Status:     "active",
		Created:    now,
		Updated:    now,
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), slug), manifest); err != nil {
		t.Fatal(err)
	}
}

func mergeGCProjectUpstream(t *testing.T, fixture gcRepoFixture, branch string) {
	t.Helper()
	mergeGCProjectsUpstream(t, fixture, branch)
}

func mergeGCProjectsUpstream(t *testing.T, fixture gcRepoFixture, branches ...string) {
	t.Helper()
	for _, branch := range branches {
		runArchiveGit(t, fixture.repo, "push", "-q", "origin", branch)
		runArchiveGit(t, fixture.upstream, "fetch", "-q", "origin", branch)
		runArchiveGit(t, fixture.upstream, "merge", "-q", "--no-edit", "origin/"+branch)
	}
	runArchiveGit(t, fixture.upstream, "push", "-q", "origin", fixture.base)
}

func captureGCOutput(t *testing.T, fn func() error) (stdout, stderr string, runErr error) {
	t.Helper()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = stdoutWriter, stderrWriter
	runErr = fn()
	stdoutWriter.Close()
	stderrWriter.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	stdoutBytes, _ := io.ReadAll(stdoutReader)
	stderrBytes, _ := io.ReadAll(stderrReader)
	return string(stdoutBytes), string(stderrBytes), runErr
}

func TestGCArchivesUpstreamMergedMainAndMaster(t *testing.T) {
	for _, base := range []string{"main", "master"} {
		t.Run(base, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fixture := newGCRepoFixture(t, base)
			slug := "upstream-" + base
			branch, worktree := addGCProject(t, fixture, slug)
			mergeGCProjectUpstream(t, fixture, branch)
			updateGCManifest(t, slug, func(manifest *project.Manifest) {
				manifest.BaseBranch = ""
			})
			runArchiveGit(t, fixture.repo, "checkout", "-q", "--detach", fixture.startSHA)
			runArchiveGit(t, fixture.repo, "branch", "-D", base)
			t.Setenv("GIT_TRACE", "1")

			stdout, stderr, err := captureGCOutput(t, runGC)
			if err != nil {
				t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
			}
			for _, want := range []string{slug, branch, "Archived:"} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout %q is missing %q", stdout, want)
				}
			}
			if pathExists(worktree) || gitx.BranchExists(fixture.repo, branch) {
				t.Fatal("GC left the merged worktree or branch behind")
			}
			if pathExists(filepath.Join(project.ActiveDir(), slug)) {
				t.Fatal("GC left the merged project active")
			}
			if archived := loadArchivedManifest(t, slug); !archived.Merged {
				t.Fatal("GC did not record the project as merged")
			}
		})
	}
}

func TestGCKeepsUnmergedBranchWhenTagShadowsRemoteBase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "shadowed-base"
	branch, worktree := addGCProject(t, fixture, slug)
	runArchiveGit(t, fixture.repo, "tag", "origin/main", "refs/heads/"+branch)

	_, _, err := captureGCOutput(t, runGC)
	if err != nil && !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC: %v", err)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC removed an unmerged project after a tag shadowed origin/main")
	}
}

func TestGCKeepsUpstreamMergedBranchWhenDetachedWorktreeHeadDiverges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "upstream-divergent-worktree"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)
	runArchiveGit(t, worktree, "checkout", "-q", "--detach")
	commitArchiveFile(t, worktree, "later.txt", "later\n", "later detached work")

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "worktree HEAD") || !strings.Contains(stderr, "does not match branch") {
		t.Fatalf("stderr %q is missing the divergent worktree diagnostic", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC changed an upstream-merged project whose detached worktree diverged")
	}
}

func TestGCKeepsUpstreamMergedBranchWhenWorktreeIsAttachedToAnotherBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "upstream-wrong-attached-branch"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)
	tip := gitx.RevParse(fixture.repo, "refs/heads/"+branch)
	replacement := "user/replacement-at-merged-tip"
	runArchiveGit(t, fixture.repo, "branch", replacement, tip)
	runArchiveGit(t, worktree, "checkout", "-q", replacement)

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "attached to") ||
		!strings.Contains(stderr, "refs/heads/"+branch) {
		t.Fatalf("stderr %q is missing the attached branch mismatch", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) || !pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) ||
		!gitx.BranchExists(fixture.repo, replacement) {
		t.Fatal("GC changed a project whose worktree was attached to another branch")
	}
}

func TestGCPreservesMergedBranchWithExistingUnregisteredWorktreeDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "upstream-leftover-worktree"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)
	runArchiveGit(t, fixture.repo, "worktree", "remove", "--force", worktree)
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(worktree, "keep.txt")
	if err := os.WriteFile(marker, []byte("do not delete\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manifestPath := project.ManifestPath(project.ActiveDir(), slug)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "exists but is not registered") {
		t.Fatalf("stderr %q is missing the unregistered worktree diagnostic", stderr)
	}
	assertGCProjectUnchanged(t, manifestPath, before, fixture.repo, branch, worktree)
	if data, err := os.ReadFile(marker); err != nil || string(data) != "do not delete\n" {
		t.Fatalf("unregistered worktree contents changed: data=%q err=%v", data, err)
	}
}

func TestGCRejectsDanglingWorktreeSymlink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "dangling-worktree"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)
	runArchiveGit(t, fixture.repo, "worktree", "remove", "--force", worktree)
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), worktree); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "symlink") {
		t.Fatalf("stderr %q is missing dangling symlink diagnostic", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC changed project after rejecting dangling symlink")
	}
	if _, statErr := os.Lstat(worktree); statErr != nil {
		t.Fatalf("GC removed dangling symlink: %v", statErr)
	}
}

func TestGCArchivesMergedPullRequestWithMatchingBranchTip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "pr-squash"
	_, _ = addGCProject(t, fixture, slug)
	recordArchiveManifestPR(t, slug, 701)
	installArchivePRIndex(t, map[string]programview.PRState{"#701": programview.PRStateMerged})
	t.Setenv("GIT_TRACE", "1")

	_, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("GC left the merged-PR project active")
	}
	if archived := loadArchivedManifest(t, slug); !archived.Merged {
		t.Fatal("GC did not record the merged pull request")
	}
}

func TestGCKeepsBranchMergedOnlyIntoLocalBase(t *testing.T) {
	for _, base := range []string{"main", "master"} {
		t.Run(base, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fixture := newGCRepoFixture(t, base)
			slug := "local-only-" + base
			branch, worktree := addGCProject(t, fixture, slug)
			runArchiveGit(t, fixture.repo, "merge", "-q", "--no-edit", "refs/heads/"+branch)

			_, stderr, err := captureGCOutput(t, runGC)
			if err != nil {
				t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
			}
			if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
				!pathExists(worktree) ||
				!gitx.BranchExists(fixture.repo, branch) {
				t.Fatal("GC removed a branch merged only into the local base")
			}
			if pathExists(filepath.Join(project.ArchivedDir(), slug)) {
				t.Fatal("GC archived a branch that is not merged upstream")
			}
		})
	}
}

func TestGCForceCleansDirtyUntrackedMergedWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "dirty-merged"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)
	writeArchiveFile(t, worktree, slug+".txt", "modified\n")
	writeArchiveFile(t, worktree, "untracked.txt", "untracked\n")

	_, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
	}
	if pathExists(worktree) || gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC left a proven-merged dirty worktree or branch behind")
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("GC left the proven-merged dirty project active")
	}
	archived := loadArchivedManifest(t, slug)
	if !archived.Merged {
		t.Fatal("GC did not record the force-cleaned project as merged")
	}
	if archived.ArchiveCleanup == nil || !archived.ArchiveCleanup.ForceAuthorized {
		t.Fatalf(
			"cleanup proof = %+v, want GC force authorization persisted",
			archived.ArchiveCleanup,
		)
	}
}

func TestGCArchivesMergedPullRequestWhenLocalBranchIsDeleted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "pr-deleted-branch"
	branch, worktree := addGCProject(t, fixture, slug)
	recordArchiveManifestPR(t, slug, 701)
	installArchivePRIndex(t, map[string]programview.PRState{"#701": programview.PRStateMerged})
	runArchiveGit(t, fixture.repo, "worktree", "remove", "--force", worktree)
	runArchiveGit(t, fixture.repo, "branch", "-D", branch)

	_, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("GC left the deleted-branch merged-PR project active")
	}
	if archived := loadArchivedManifest(t, slug); !archived.Merged {
		t.Fatal("GC did not record the deleted-branch project as merged")
	}
}

func TestGCArchivesMergedPullRequestWhenDeletedBranchWorktreeHeadMatches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "pr-deleted-branch-matching-worktree"
	branch, worktree := addGCProject(t, fixture, slug)
	recordArchiveManifestPR(t, slug, 711)
	installArchivePRIndex(t, map[string]programview.PRState{"#711": programview.PRStateMerged})
	runArchiveGit(t, worktree, "checkout", "-q", "--detach")
	runArchiveGit(t, fixture.repo, "branch", "-D", branch)

	_, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) || pathExists(worktree) {
		t.Fatal("GC left the matching detached-worktree project active")
	}
	if archived := loadArchivedManifest(t, slug); !archived.Merged {
		t.Fatal("GC did not record the matching detached-worktree project as merged")
	}
}

func TestGCKeepsMergedPullRequestWhenDeletedBranchWorktreeHeadDiverges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "pr-deleted-branch-divergent-worktree"
	branch, worktree := addGCProject(t, fixture, slug)
	recordArchiveManifestPR(t, slug, 711)
	installArchivePRIndex(t, map[string]programview.PRState{"#711": programview.PRStateMerged})
	runArchiveGit(t, worktree, "checkout", "-q", "--detach")
	commitArchiveFile(t, worktree, "later.txt", "later\n", "later detached work")
	runArchiveGit(t, fixture.repo, "branch", "-D", branch)

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "worktree HEAD") || !strings.Contains(stderr, "does not match") {
		t.Fatalf("stderr %q is missing the divergent worktree diagnostic", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("GC removed a project whose detached worktree diverged from the merged pull request")
	}
	if !pathExists(worktree) {
		t.Fatal("GC removed the divergent detached worktree")
	}
	if gitx.BranchExists(fixture.repo, branch) {
		t.Fatalf("deleted branch %q was unexpectedly restored", branch)
	}
}

func TestGCKeepsMergedPullRequestWhenExistingBranchDetachedWorktreeHeadDiverges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "pr-existing-branch-divergent-worktree"
	branch, worktree := addGCProject(t, fixture, slug)
	recordArchiveManifestPR(t, slug, 712)
	installArchivePRIndex(t, map[string]programview.PRState{"#712": programview.PRStateMerged})
	runArchiveGit(t, worktree, "checkout", "-q", "--detach")
	commitArchiveFile(t, worktree, "later.txt", "later\n", "later detached work")

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "worktree HEAD") || !strings.Contains(stderr, "does not match") {
		t.Fatalf("stderr %q is missing the divergent worktree diagnostic", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("GC removed a project whose existing branch worktree diverged from the merged pull request")
	}
	if !pathExists(worktree) {
		t.Fatal("GC removed the divergent worktree")
	}
	if !gitx.BranchExists(fixture.repo, branch) {
		t.Fatalf("GC removed existing branch %q after its worktree diverged", branch)
	}
}

func TestGCKeepsMergedPullRequestWhenDeletedBranchNameDoesNotMatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "pr-deleted-branch-mismatch"
	branch, worktree := addGCProject(t, fixture, slug)
	recordArchiveManifestPR(t, slug, 709)
	previousProof := loadArchivePullRequestProof
	previousRepository := loadArchiveRepository
	loadArchivePullRequestProof = func(string, string) (programview.PullRequestProof, error) {
		return programview.PullRequestProof{
			State:      programview.PRStateMerged,
			Repository: "github.com/acme/widgets",
			BaseBranch: "main",
			HeadBranch: "user/another-branch",
		}, nil
	}
	loadArchiveRepository = func(string) (string, error) {
		return "github.com/acme/widgets", nil
	}
	t.Cleanup(func() {
		loadArchivePullRequestProof = previousProof
		loadArchiveRepository = previousRepository
	})
	runArchiveGit(t, fixture.repo, "worktree", "remove", "--force", worktree)
	runArchiveGit(t, fixture.repo, "branch", "-D", branch)

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "does not match manifest branch") {
		t.Fatalf("stderr %q is missing the branch mismatch diagnostic", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("GC removed a project whose pull request head branch did not match")
	}
}

func TestGCKeepsMergedPullRequestWithMismatchedProof(t *testing.T) {
	tests := []struct {
		name            string
		repository      string
		localRepository string
		headSHA         func(gcRepoFixture, string) string
		wantError       string
	}{
		{
			name:            "different host",
			repository:      "github.example/acme/widgets",
			localRepository: "github.com/acme/widgets",
			headSHA: func(fixture gcRepoFixture, branch string) string {
				return gitx.RevParse(fixture.repo, "refs/heads/"+branch)
			},
			wantError: "repository",
		},
		{
			name:            "different repository",
			repository:      "github.com/acme/other",
			localRepository: "github.com/acme/widgets",
			headSHA: func(fixture gcRepoFixture, branch string) string {
				return gitx.RevParse(fixture.repo, "refs/heads/"+branch)
			},
			wantError: "repository",
		},
		{
			name:            "different branch head",
			repository:      "github.com/acme/widgets",
			localRepository: "github.com/acme/widgets",
			headSHA: func(fixture gcRepoFixture, _ string) string {
				return fixture.startSHA
			},
			wantError: "does not match",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fixture := newGCRepoFixture(t, "main")
			slug := "pr-mismatch"
			branch, worktree := addGCProject(t, fixture, slug)
			recordArchiveManifestPR(t, slug, 708)
			previousProof := loadArchivePullRequestProof
			previousRepository := loadArchiveRepository
			loadArchivePullRequestProof = func(string, string) (programview.PullRequestProof, error) {
				return programview.PullRequestProof{
					State:      programview.PRStateMerged,
					Repository: test.repository,
					BaseBranch: fixture.base,
					HeadSHA:    test.headSHA(fixture, branch),
				}, nil
			}
			loadArchiveRepository = func(string) (string, error) {
				return test.localRepository, nil
			}
			t.Cleanup(func() {
				loadArchivePullRequestProof = previousProof
				loadArchiveRepository = previousRepository
			})

			_, stderr, err := captureGCOutput(t, runGC)
			if !errors.Is(err, errGCCompletedWithErrors) {
				t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
			}
			if !strings.Contains(stderr, test.wantError) {
				t.Fatalf("stderr %q is missing %q", stderr, test.wantError)
			}
			if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
				!pathExists(worktree) ||
				!gitx.BranchExists(fixture.repo, branch) {
				t.Fatal("GC changed a project whose merged PR proof did not match")
			}
		})
	}
}

func TestGCKeepsUnmergedOpenAndClosedPullRequestProjects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	unmergedBranch, unmergedWorktree := addGCProject(t, fixture, "unmerged")
	closedBranch, closedWorktree := addGCProject(t, fixture, "closed-pr")
	openBranch, openWorktree := addGCProject(t, fixture, "open-pr")
	recordArchiveManifestPR(t, "closed-pr", 702)
	recordArchiveManifestPR(t, "open-pr", 705)
	installArchivePRIndex(t, map[string]programview.PRState{
		"#702": programview.PRStateClosed,
		"#705": programview.PRStateOpen,
	})

	stdout, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
	}
	if stdout != "" {
		t.Fatalf("runGC wrote unexpected output: %q", stdout)
	}
	for slug, state := range map[string]struct {
		branch   string
		worktree string
	}{
		"unmerged": {branch: unmergedBranch, worktree: unmergedWorktree},
		"closed-pr": {
			branch:   closedBranch,
			worktree: closedWorktree,
		},
		"open-pr": {
			branch:   openBranch,
			worktree: openWorktree,
		},
	} {
		if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
			!pathExists(state.worktree) ||
			!gitx.BranchExists(fixture.repo, state.branch) {
			t.Fatalf("GC changed unmerged project %s", slug)
		}
	}
}

func TestGCKeepsMissingBranchWithoutPullRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "missing-branch"
	branch, worktree := addGCProject(t, fixture, slug)
	runArchiveGit(t, fixture.repo, "worktree", "remove", "--force", worktree)
	runArchiveGit(t, fixture.repo, "branch", "-D", branch)

	stdout, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
	}
	if stdout != "" {
		t.Fatalf("runGC wrote unexpected output: %q", stdout)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatalf("GC removed unresolved project %s", slug)
	}
}

func TestGCPullRequestLookupFailureIsActionable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "lookup-failure"
	branch, worktree := addGCProject(t, fixture, slug)
	recordArchiveManifestPR(t, slug, 706)
	installArchivePRLookupError(t, errors.New("GitHub unavailable"))

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "GitHub unavailable") {
		t.Fatalf("stderr %q is missing the pull request lookup failure", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC changed a project after its pull request lookup failed")
	}
}

func TestGCKeepsDeletedBranchWhenPullRequestLookupFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "deleted-branch-lookup-failure"
	branch, worktree := addGCProject(t, fixture, slug)
	recordArchiveManifestPR(t, slug, 710)
	installArchivePRLookupError(t, errors.New("GitHub unavailable"))
	runArchiveGit(t, fixture.repo, "worktree", "remove", "--force", worktree)
	runArchiveGit(t, fixture.repo, "branch", "-D", branch)

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "GitHub unavailable") {
		t.Fatalf("stderr %q is missing the pull request lookup failure", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("GC removed a deleted-branch project after its pull request lookup failed")
	}
	if pathExists(filepath.Join(project.ArchivedDir(), slug)) {
		t.Fatal("GC archived a deleted-branch project after its pull request lookup failed")
	}
}

func TestGCMissingStartSHARequiresMergedPullRequest(t *testing.T) {
	for _, mergedPR := range []bool{false, true} {
		name := "unproven"
		if mergedPR {
			name = "merged-pr"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fixture := newGCRepoFixture(t, "main")
			slug := "missing-start-" + name
			_, _ = addGCProject(t, fixture, slug)
			updateGCManifest(t, slug, func(manifest *project.Manifest) {
				manifest.StartSHA = ""
				if mergedPR {
					number := 703
					manifest.PR.Number = &number
				}
			})
			if mergedPR {
				installArchivePRIndex(t, map[string]programview.PRState{"#703": programview.PRStateMerged})
			}

			_, stderr, err := captureGCOutput(t, runGC)
			if mergedPR {
				if err != nil {
					t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
				}
				if pathExists(filepath.Join(project.ActiveDir(), slug)) {
					t.Fatal("GC left merged-PR project active")
				}
				return
			}
			if !errors.Is(err, errGCCompletedWithErrors) {
				t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
			}
			if !strings.Contains(stderr, "no start_sha") {
				t.Fatalf("stderr %q does not explain missing start_sha", stderr)
			}
			if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
				t.Fatal("GC removed project without merge proof")
			}
		})
	}
}

func TestGCRejectsInvalidStartSHAEvenWithMergedPullRequestAndDirtyWorktree(t *testing.T) {
	for _, test := range []struct {
		name     string
		startSHA func(*testing.T, gcRepoFixture) string
		want     string
	}{
		{
			name: "invalid",
			startSHA: func(_ *testing.T, _ gcRepoFixture) string {
				return "not-a-commit"
			},
			want: "does not resolve to a commit",
		},
		{
			name: "non-commit",
			startSHA: func(t *testing.T, fixture gcRepoFixture) string {
				path := filepath.Join(fixture.repo, "blob")
				if err := os.WriteFile(path, []byte("not a commit\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return gitOutput(t, fixture.repo, "hash-object", "-w", path)
			},
			want: "does not resolve to a commit",
		},
		{
			name: "not branch ancestor",
			startSHA: func(t *testing.T, fixture gcRepoFixture) string {
				branch, worktree := addGCBranch(t, fixture, "unrelated-start")
				sha := gitx.RevParse(fixture.repo, "refs/heads/"+branch)
				runArchiveGit(t, fixture.repo, "worktree", "remove", "--force", worktree)
				return sha
			},
			want: "not an ancestor",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fixture := newGCRepoFixture(t, "main")
			slug := "invalid-start-" + strings.ReplaceAll(test.name, " ", "-")
			branch, worktree := addGCProject(t, fixture, slug)
			writeArchiveFile(t, worktree, "dirty.txt", "uncommitted work\n")
			recordArchiveManifestPR(t, slug, 714)
			installArchivePRIndex(t, map[string]programview.PRState{"#714": programview.PRStateMerged})
			updateGCManifest(t, slug, func(manifest *project.Manifest) {
				manifest.StartSHA = test.startSHA(t, fixture)
			})

			_, stderr, err := captureGCOutput(t, runGC)
			if !errors.Is(err, errGCCompletedWithErrors) {
				t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
			}
			if !strings.Contains(stderr, test.want) {
				t.Fatalf("stderr %q is missing %q", stderr, test.want)
			}
			if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
				!pathExists(worktree) ||
				!gitx.BranchExists(fixture.repo, branch) {
				t.Fatal("GC destructively cleaned a project with invalid start_sha")
			}
			if data, readErr := os.ReadFile(filepath.Join(worktree, "dirty.txt")); readErr != nil ||
				string(data) != "uncommitted work\n" {
				t.Fatalf("dirty worktree changed: data=%q err=%v", data, readErr)
			}
		})
	}
}

func TestGCSharedRefreshFailureWarnsOnceAndProtectsStaleRef(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	firstBranch, firstWorktree := addGCProject(t, fixture, "stale-first")
	secondBranch, secondWorktree := addGCProject(t, fixture, "stale-second")
	mergeGCProjectUpstream(t, fixture, firstBranch)
	mergeGCProjectUpstream(t, fixture, secondBranch)
	if _, err := gitx.Fetch(fixture.repo, fixture.base); err != nil {
		t.Fatal(err)
	}
	runArchiveGit(t, fixture.upstream, "push", "-q", "origin", ":main")
	fetchCount := installFetchCounter(t, fixture.repo)

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	warning := "refresh repository " + fixture.repo + " base main:"
	if count := strings.Count(stderr, warning); count != 1 {
		t.Fatalf("refresh warning count = %d, want 1\nstderr: %s", count, stderr)
	}
	if !strings.Contains(stderr, "couldn't find remote ref") {
		t.Fatalf("stderr %q is missing the Git diagnostic", stderr)
	}
	if attempts := fetchCount(); attempts != 1 {
		t.Fatalf("fetch attempts = %d, want 1", attempts)
	}
	for slug, state := range map[string]struct {
		branch   string
		worktree string
	}{
		"stale-first":  {branch: firstBranch, worktree: firstWorktree},
		"stale-second": {branch: secondBranch, worktree: secondWorktree},
	} {
		if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
			!pathExists(state.worktree) ||
			!gitx.BranchExists(fixture.repo, state.branch) {
			t.Fatalf("GC used stale origin/main to remove %s", slug)
		}
	}
}

func TestGCLinkedRepositoryPathsShareOneRefresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	firstBranch, firstWorktree := addGCProject(t, fixture, "linked-refresh-first")
	secondBranch, secondWorktree := addGCProject(t, fixture, "linked-refresh-second")
	mergeGCProjectUpstream(t, fixture, firstBranch)
	mergeGCProjectUpstream(t, fixture, secondBranch)

	linkedRepo := filepath.Join(t.TempDir(), "linked-repository")
	runArchiveGit(t, fixture.repo, "worktree", "add", "-q", "--detach", linkedRepo, fixture.startSHA)
	updateGCManifest(t, "linked-refresh-second", func(manifest *project.Manifest) {
		manifest.Repo = linkedRepo
	})
	fetchCount := installFetchCounter(t, fixture.repo)

	_, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
	}
	if attempts := fetchCount(); attempts != 1 {
		t.Fatalf("fetch attempts = %d, want 1 across linked repository paths", attempts)
	}
	for slug, state := range map[string]struct {
		repo     string
		branch   string
		worktree string
	}{
		"linked-refresh-first": {
			repo: fixture.repo, branch: firstBranch, worktree: firstWorktree,
		},
		"linked-refresh-second": {
			repo: linkedRepo, branch: secondBranch, worktree: secondWorktree,
		},
	} {
		if pathExists(filepath.Join(project.ActiveDir(), slug)) ||
			pathExists(state.worktree) ||
			gitx.BranchExists(state.repo, state.branch) {
			t.Fatalf("GC did not clean %s after the shared refresh", slug)
		}
	}
}

func TestGCIndependentRepositoriesRefreshSeparately(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := newGCRepoFixture(t, "main")
	firstBranch, firstWorktree := addGCProject(t, first, "independent-refresh-first")
	mergeGCProjectUpstream(t, first, firstBranch)
	firstFetchCount := installFetchCounter(t, first.repo)

	second := newGCRepoFixture(t, "main")
	secondBranch, secondWorktree := addGCProject(t, second, "independent-refresh-second")
	mergeGCProjectUpstream(t, second, secondBranch)
	secondFetchCount := installFetchCounter(t, second.repo)

	_, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
	}
	if attempts := firstFetchCount(); attempts != 1 {
		t.Fatalf("first repository fetch attempts = %d, want 1", attempts)
	}
	if attempts := secondFetchCount(); attempts != 1 {
		t.Fatalf("second repository fetch attempts = %d, want 1", attempts)
	}
	for slug, state := range map[string]struct {
		repo     string
		branch   string
		worktree string
	}{
		"independent-refresh-first": {
			repo: first.repo, branch: firstBranch, worktree: firstWorktree,
		},
		"independent-refresh-second": {
			repo: second.repo, branch: secondBranch, worktree: secondWorktree,
		},
	} {
		if pathExists(filepath.Join(project.ActiveDir(), slug)) ||
			pathExists(state.worktree) ||
			gitx.BranchExists(state.repo, state.branch) {
			t.Fatalf("GC did not clean %s after its repository refresh", slug)
		}
	}
}

func TestGCFetchWarningRedactsCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "credential-fetch-failure"
	branch, worktree := addGCProject(t, fixture, slug)
	scriptPath := filepath.Join(t.TempDir(), "failing-upload-pack")
	script := "#!/bin/sh\n" +
		"echo \"fatal: unable to access 'https://fetch-user:fetch-secret@git_alias_1/team/repo.git?access_token=query-secret#fragment-secret': denied\" >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	runArchiveGit(t, fixture.repo, "config", "remote.origin.uploadpack", scriptPath)

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, secret := range []string{
		"fetch-user", "fetch-secret", "access_token", "query-secret", "fragment-secret",
	} {
		if strings.Contains(stderr, secret) {
			t.Fatalf("stderr %q leaked %q", stderr, secret)
		}
	}
	if !strings.Contains(stderr, "https://[redacted]@git_alias_1/team/repo.git") {
		t.Fatalf("stderr %q is missing the sanitized fetch URL", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC changed a project after its credential-bearing fetch failure")
	}
}

func TestGCFetchWarningRedactsApostrophesInURLTokens(t *testing.T) {
	tests := []struct {
		name       string
		diagnostic string
		wantURL    string
		secrets    []string
		wantProse  string
	}{
		{
			name: "scheme URL",
			diagnostic: "fatal: unable to access " +
				"'https://fetch'user:fetch'secret@git_alias_1/team/it's/repo.git" +
				"?access_token=query'secret#fragment'secret': denied after request",
			wantURL: "https://[redacted]@git_alias_1/team/it's/repo.git",
			secrets: []string{
				"fetch'user", "fetch'secret", "access_token", "query'secret", "fragment'secret",
			},
			wantProse: "denied after request",
		},
		{
			name: "SCP URL",
			diagnostic: "fatal: repository " +
				"'de'ploy@git_alias_1:team/it's/repo.git?token=query'secret#fragment'secret'" +
				" is unavailable",
			wantURL: "[redacted]@git_alias_1:team/it's/repo.git",
			secrets: []string{
				"de'ploy", "token=", "query'secret", "fragment'secret",
			},
			wantProse: "is unavailable",
		},
		{
			name:       "SCP URL without userinfo",
			diagnostic: "fatal: repository 'git_alias_1:team/repo.git?token=query-secret#fragment-secret' is unavailable",
			wantURL:    "git_alias_1:team/repo.git",
			secrets:    []string{"token=", "query-secret", "fragment-secret"},
			wantProse:  "is unavailable",
		},
		{
			name:       "remote helper scheme URL",
			diagnostic: "fatal: repository 'git::https://fetch-user:fetch-secret@git_alias_1/team/repo.git?token=query-secret#fragment-secret' is unavailable",
			wantURL:    "git::https://[redacted]@git_alias_1/team/repo.git",
			secrets:    []string{"fetch-user", "fetch-secret", "token=", "query-secret", "fragment-secret"},
			wantProse:  "is unavailable",
		},
		{
			name:       "remote helper SCP URL",
			diagnostic: "fatal: repository 'cache::deploy-token@git_alias_1:team/repo.git?token=query-secret#fragment-secret' is unavailable",
			wantURL:    "cache::[redacted]@git_alias_1:team/repo.git",
			secrets:    []string{"deploy-token", "token=", "query-secret", "fragment-secret"},
			wantProse:  "is unavailable",
		},
		{
			name:       "underscore SCP URL",
			diagnostic: "fatal: repository '_deploy-token@git_alias_1:team/repo.git?token=query-secret#fragment-secret' is unavailable",
			wantURL:    "[redacted]@git_alias_1:team/repo.git",
			secrets:    []string{"_deploy-token", "token=", "query-secret", "fragment-secret"},
			wantProse:  "is unavailable",
		},
		{
			name:       "nested helper underscore SCP URL",
			diagnostic: "fatal: repository 'cache::_deploy-token@git_alias_1:team/repo.git?token=query-secret#fragment-secret' is unavailable",
			wantURL:    "cache::[redacted]@git_alias_1:team/repo.git",
			secrets:    []string{"_deploy-token", "token=", "query-secret", "fragment-secret"},
			wantProse:  "is unavailable",
		},
		{
			name:       "malformed userinfo host remote",
			diagnostic: "fatal: repository 'github-token@ghe.example?token=query-secret#fragment-secret' is invalid",
			wantURL:    "[redacted]@ghe.example",
			secrets:    []string{"github-token", "token=", "query-secret", "fragment-secret"},
			wantProse:  "is invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fixture := newGCRepoFixture(t, "main")
			slug := "apostrophe-fetch-failure"
			branch, worktree := addGCProject(t, fixture, slug)
			scriptPath := filepath.Join(t.TempDir(), "failing-upload-pack")
			script := "#!/bin/sh\n" +
				"printf '%s\\n' " + shellQuote(test.diagnostic) + " >&2\n" +
				"exit 1\n"
			if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			runArchiveGit(t, fixture.repo, "config", "remote.origin.uploadpack", scriptPath)

			_, stderr, err := captureGCOutput(t, runGC)
			if !errors.Is(err, errGCCompletedWithErrors) {
				t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
			}
			for _, secret := range test.secrets {
				if strings.Contains(stderr, secret) {
					t.Fatalf("stderr %q leaked %q", stderr, secret)
				}
			}
			for _, want := range []string{test.wantURL, test.wantProse} {
				if !strings.Contains(stderr, want) {
					t.Fatalf("stderr %q is missing %q", stderr, want)
				}
			}
			if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
				!pathExists(worktree) ||
				!gitx.BranchExists(fixture.repo, branch) {
				t.Fatal("GC changed a project after its credential-bearing fetch failure")
			}
		})
	}
}

func TestGCDeepRemoteHelperDiagnosticDoesNotBlockAnotherRepository(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	failingRepo := newGCRepoFixture(t, "main")
	failingBranch, failingWorktree := addGCProject(t, failingRepo, "a-deep-helper-failure")
	diagnostic := "fatal: repository '" + strings.Repeat("cache::", 10000) +
		"_deep-secret@git.example.com:team/repo.git?token=also-secret#fragment' is unavailable"
	scriptPath := filepath.Join(t.TempDir(), "failing-upload-pack")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' " + shellQuote(diagnostic) + " >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	runArchiveGit(t, failingRepo.repo, "config", "remote.origin.uploadpack", scriptPath)

	successRepo := newGCRepoFixture(t, "main")
	successBranch, successWorktree := addGCProject(t, successRepo, "z-deep-helper-success")
	mergeGCProjectUpstream(t, successRepo, successBranch)

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, secret := range []string{"deep-secret", "also-secret"} {
		if strings.Contains(stderr, secret) {
			t.Fatalf("stderr leaked %q in %q", secret, stderr)
		}
	}
	if !strings.Contains(stderr, "leading truncated token redacted") {
		t.Fatalf("stderr %q is missing the boundary redaction", stderr)
	}
	if !strings.Contains(stderr, "git diagnostic truncated") {
		t.Fatalf("stderr %q is missing the subprocess diagnostic truncation marker", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), "a-deep-helper-failure")) ||
		!pathExists(failingWorktree) ||
		!gitx.BranchExists(failingRepo.repo, failingBranch) {
		t.Fatal("GC changed the repository whose deep helper diagnostic failed")
	}
	if pathExists(filepath.Join(project.ActiveDir(), "z-deep-helper-success")) ||
		pathExists(successWorktree) ||
		gitx.BranchExists(successRepo.repo, successBranch) {
		t.Fatal("GC did not continue to archive the independent repository")
	}
}

func TestGCRefreshFailureDoesNotBlockAnotherRepository(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	failingRepo := newGCRepoFixture(t, "main")
	failingBranch, failingWorktree := addGCProject(t, failingRepo, "a-refresh-failure")
	runArchiveGit(t, failingRepo.upstream, "push", "-q", "origin", ":main")

	successRepo := newGCRepoFixture(t, "main")
	successBranch, _ := addGCProject(t, successRepo, "z-refresh-success")
	mergeGCProjectUpstream(t, successRepo, successBranch)

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "refresh repository "+failingRepo.repo+" base main:") {
		t.Fatalf("stderr %q is missing repository A's refresh failure", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), "a-refresh-failure")) ||
		!pathExists(failingWorktree) ||
		!gitx.BranchExists(failingRepo.repo, failingBranch) {
		t.Fatal("GC changed the project whose repository refresh failed")
	}
	if pathExists(filepath.Join(project.ActiveDir(), "z-refresh-success")) {
		t.Fatal("repository A's refresh failure blocked repository B cleanup")
	}
}

func TestGCRefreshFailureAllowsMergedPullRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "refresh-failure-pr"
	_, _ = addGCProject(t, fixture, slug)
	recordArchiveManifestPR(t, slug, 704)
	installArchivePRIndex(t, map[string]programview.PRState{"#704": programview.PRStateMerged})
	runArchiveGit(t, fixture.upstream, "push", "-q", "origin", ":main")

	_, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stderr, "refresh repository "+fixture.repo+" base main:") {
		t.Fatalf("stderr %q is missing refresh context", stderr)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("GC left merged-PR project active after refresh failure")
	}
}

func TestGCKeepsMergedPullRequestWhenBaseCannotBeVerified(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "base-discovery-failure-pr"
	_, _ = addGCProject(t, fixture, slug)
	recordArchiveManifestPR(t, slug, 713)
	installArchivePRIndex(t, map[string]programview.PRState{"#713": programview.PRStateMerged})
	updateGCManifest(t, slug, func(manifest *project.Manifest) {
		manifest.BaseBranch = ""
	})
	runArchiveGit(t, fixture.repo, "remote", "set-head", "origin", "-d")
	runArchiveGit(t, fixture.repo, "checkout", "-q", "--detach", fixture.startSHA)
	runArchiveGit(t, fixture.repo, "branch", "-D", "main")

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{"cannot determine default branch", "resolve base branch", "symbolic-ref"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("GC removed merged-PR project without verifying its base branch")
	}
}

func TestGCFailsClosedAfterMalformedAndInvalidCompetingMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	malformedDir := filepath.Join(project.ActiveDir(), "a-malformed")
	if err := os.MkdirAll(malformedDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformedDir, "manifest.json"), []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	invalidDir := filepath.Join(project.ActiveDir(), "b-invalid")
	if err := os.MkdirAll(invalidDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(
		filepath.Join(invalidDir, "manifest.json"),
		project.Manifest{Slug: "b-invalid", Branch: "user/b-invalid"},
	); err != nil {
		t.Fatal(err)
	}
	fixture := newGCRepoFixture(t, "main")
	branch, _ := addGCProject(t, fixture, "z-eligible")
	mergeGCProjectUpstream(t, fixture, branch)

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{
		filepath.Join(malformedDir, "manifest.json"),
		"parse manifest",
		filepath.Join(invalidDir, "manifest.json"),
		"repository is empty",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if !pathExists(filepath.Join(project.ActiveDir(), "z-eligible")) {
		t.Fatal("GC archived an eligible project while competing ownership metadata was unreadable")
	}
}

func TestGCContinuesForRepositoryIndependentOfMalformedOwnership(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	blockedRepo := newGCRepoFixture(t, "main")
	blockedSlug := "blocked-owner"
	blockedBranch, blockedWorktree := addGCProject(t, blockedRepo, blockedSlug)
	mergeGCProjectUpstream(t, blockedRepo, blockedBranch)

	malformedSlug := "malformed-owner"
	malformedDir := filepath.Join(project.ActiveDir(), malformedSlug)
	if err := os.MkdirAll(malformedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	malformed := `{
  "slug": "` + malformedSlug + `",
  "repo": "` + blockedRepo.repo + `",
  "branch": "` + blockedBranch + `",
  "worktree": "` + blockedWorktree + `",
  "status": 42
}`
	malformedPath := project.ManifestPath(project.ActiveDir(), malformedSlug)
	if err := os.WriteFile(malformedPath, []byte(malformed), 0o644); err != nil {
		t.Fatal(err)
	}

	eligibleRepo := newGCRepoFixture(t, "main")
	eligibleSlug := "independent-owner"
	eligibleBranch, eligibleWorktree := addGCProject(t, eligibleRepo, eligibleSlug)
	mergeGCProjectUpstream(t, eligibleRepo, eligibleBranch)

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{malformedPath, "cannot unmarshal", blockedSlug} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if !pathExists(filepath.Join(project.ActiveDir(), blockedSlug)) ||
		!pathExists(blockedWorktree) ||
		!gitx.BranchExists(blockedRepo.repo, blockedBranch) {
		t.Fatal("GC changed resources that malformed metadata could claim")
	}
	if pathExists(filepath.Join(project.ActiveDir(), eligibleSlug)) ||
		pathExists(eligibleWorktree) ||
		gitx.BranchExists(eligibleRepo.repo, eligibleBranch) {
		t.Fatal("GC did not clean an independent repository")
	}
}

func TestGCContinuesForRepositoryIndependentOfCanonicalizationFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	blockedRepo := newGCRepoFixture(t, "main")
	blockedSlug := "canonical-blocked-owner"
	blockedBranch, blockedWorktree := addGCProject(t, blockedRepo, blockedSlug)
	mergeGCProjectUpstream(t, blockedRepo, blockedBranch)

	invalidSlug := "invalid-repository-owner"
	invalidRepo := filepath.Join(blockedRepo.repo, "nested")
	if err := os.MkdirAll(invalidRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	invalid := project.Manifest{
		Slug: invalidSlug, Repo: invalidRepo, Branch: blockedBranch, Worktree: &blockedWorktree,
	}
	invalidPath := project.ManifestPath(project.ActiveDir(), invalidSlug)
	if err := os.MkdirAll(filepath.Dir(invalidPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(invalidPath, invalid); err != nil {
		t.Fatal(err)
	}

	eligibleRepo := newGCRepoFixture(t, "main")
	eligibleSlug := "canonical-independent-owner"
	eligibleBranch, eligibleWorktree := addGCProject(t, eligibleRepo, eligibleSlug)
	mergeGCProjectUpstream(t, eligibleRepo, eligibleBranch)

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{invalidPath, "resolves inside repository root", blockedSlug} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if !pathExists(filepath.Join(project.ActiveDir(), blockedSlug)) ||
		!pathExists(blockedWorktree) ||
		!gitx.BranchExists(blockedRepo.repo, blockedBranch) {
		t.Fatal("GC changed resources that uncanonicalizable metadata could claim")
	}
	if pathExists(filepath.Join(project.ActiveDir(), eligibleSlug)) ||
		pathExists(eligibleWorktree) ||
		gitx.BranchExists(eligibleRepo.repo, eligibleBranch) {
		t.Fatal("GC did not clean a repository independent of canonicalization failure")
	}
}

func TestGCRejectsNonNilEmptyWorktreeMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "empty-worktree"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)
	updateGCManifest(t, slug, func(manifest *project.Manifest) {
		empty := " \t "
		manifest.Worktree = &empty
	})

	_, stderr, err := captureGCOutput(t, runGC)

	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{"worktree is present but empty", "preserving project resources"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC changed resources for malformed worktree metadata")
	}
}

func TestGCRejectsPathLikeManifestSlugWithoutChangingVictim(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	victimSlug := "nested/victim"
	victimBranch, victimWorktree := addGCProject(t, fixture, victimSlug)
	proofBranch, proofWorktree := addGCBranch(t, fixture, "path-proof")
	mergeGCProjectUpstream(t, fixture, proofBranch)
	writeGCManifest(t, "a-attacker", fixture, proofBranch, proofWorktree)
	updateGCManifest(t, "a-attacker", func(manifest *project.Manifest) {
		manifest.Slug = victimSlug
	})
	victimPath := project.ManifestPath(project.ActiveDir(), victimSlug)
	before, err := os.ReadFile(victimPath)
	if err != nil {
		t.Fatal(err)
	}

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "invalid slug") {
		t.Fatalf("stderr %q is missing the invalid slug diagnostic", stderr)
	}
	assertGCProjectUnchanged(t, victimPath, before, fixture.repo, victimBranch, victimWorktree)
}

func TestGCRejectsDirectoryManifestSlugMismatchWithoutChangingVictim(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	victimSlug := "victim"
	victimBranch, victimWorktree := addGCProject(t, fixture, victimSlug)
	proofBranch, proofWorktree := addGCBranch(t, fixture, "mismatch-proof")
	mergeGCProjectUpstream(t, fixture, proofBranch)
	writeGCManifest(t, "a-attacker", fixture, proofBranch, proofWorktree)
	updateGCManifest(t, "a-attacker", func(manifest *project.Manifest) {
		manifest.Slug = victimSlug
	})
	victimPath := project.ManifestPath(project.ActiveDir(), victimSlug)
	before, err := os.ReadFile(victimPath)
	if err != nil {
		t.Fatal(err)
	}

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, `slug "victim" does not match directory "a-attacker"`) {
		t.Fatalf("stderr %q is missing the slug mismatch diagnostic", stderr)
	}
	assertGCProjectUnchanged(t, victimPath, before, fixture.repo, victimBranch, victimWorktree)
}

func TestGCSkipsManifestlessDirectories(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	manifestlessDir := filepath.Join(project.ActiveDir(), "empty")
	if err := os.MkdirAll(manifestlessDir, 0755); err != nil {
		t.Fatal(err)
	}
	fixture := newGCRepoFixture(t, "main")
	branch, _ := addGCProject(t, fixture, "eligible")
	mergeGCProjectUpstream(t, fixture, branch)

	_, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
	}
	if !pathExists(manifestlessDir) {
		t.Fatal("GC removed the manifest-less directory")
	}
	if pathExists(filepath.Join(project.ActiveDir(), "eligible")) {
		t.Fatal("GC did not archive the independent eligible project")
	}
}

func TestGCContinuesAfterArchiveFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	failingRepo := newGCRepoFixture(t, "main")
	failingSlug := "a-archive-failure"
	failingBranch, failingWorktree := addGCProject(t, failingRepo, failingSlug)
	mergeGCProjectUpstream(t, failingRepo, failingBranch)
	collisionDir := filepath.Join(project.ArchivedDir(), failingSlug)
	if err := os.MkdirAll(collisionDir, 0755); err != nil {
		t.Fatal(err)
	}
	collisionMarker := filepath.Join(collisionDir, "existing")
	if err := os.WriteFile(collisionMarker, []byte("keep\n"), 0644); err != nil {
		t.Fatal(err)
	}
	successRepo := newGCRepoFixture(t, "main")
	successBranch, _ := addGCProject(t, successRepo, "z-archive-success")
	mergeGCProjectUpstream(t, successRepo, successBranch)

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "archive a-archive-failure: move project to archived") {
		t.Fatalf("stderr %q is missing archive failure", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), failingSlug)) {
		t.Fatal("failed archive project was removed")
	}
	if !pathExists(failingWorktree) {
		t.Fatal("failed archive worktree was removed")
	}
	if !gitx.BranchExists(failingRepo.repo, failingBranch) {
		t.Fatal("failed archive branch was removed")
	}
	if !pathExists(collisionMarker) {
		t.Fatal("archive destination collision was overwritten")
	}
	if pathExists(filepath.Join(project.ActiveDir(), "z-archive-success")) {
		t.Fatal("GC did not continue after archive failure")
	}
}

func TestGCBranchProbeFailureReturnsIncomplete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "branch-probe-failure"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)
	previous := archiveBranchExists
	archiveBranchExists = func(string, string) (bool, error) {
		return false, errors.New("git branch probe failed")
	}
	t.Cleanup(func() { archiveBranchExists = previous })

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "git branch probe failed") {
		t.Fatalf("stderr %q is missing branch probe failure", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC reported clean after branch probe failure")
	}
}

func TestGCRejectsIdentityChangesAfterMergeProof(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	manifestSlug := "manifest-race"
	manifestBranch, manifestWorktree := addGCProject(t, fixture, manifestSlug)
	replacement := "user/replacement"
	runArchiveGit(t, fixture.repo, "branch", replacement, "refs/heads/"+manifestBranch)
	branchSlug := "branch-race"
	branch, branchWorktree := addGCProject(t, fixture, branchSlug)
	worktreeSlug := "worktree-race"
	worktreeBranch, worktree := addGCProject(t, fixture, worktreeSlug)
	mergeGCProjectsUpstream(t, fixture, manifestBranch, branch, worktreeBranch)
	previous := gcArchiveProject
	gcArchiveProject = func(proof archiveProofSnapshot, force bool) (archiveResult, error) {
		switch proof.Slug {
		case manifestSlug:
			updateGCManifest(t, proof.Slug, func(manifest *project.Manifest) {
				manifest.Branch = replacement
			})
		case branchSlug:
			commitArchiveFile(t, branchWorktree, "later.txt", "later\n", "advance after proof")
		case worktreeSlug:
			runArchiveGit(t, worktree, "checkout", "-q", "--detach")
			commitArchiveFile(t, worktree, "later.txt", "later\n", "advance detached after proof")
		}
		return archiveProjectWithProof(proof, force)
	}
	t.Cleanup(func() { gcArchiveProject = previous })

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{"manifest changed", "branch tip changed", "worktree tip changed"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if !pathExists(filepath.Join(project.ActiveDir(), manifestSlug)) ||
		!pathExists(manifestWorktree) ||
		!gitx.BranchExists(fixture.repo, manifestBranch) ||
		!gitx.BranchExists(fixture.repo, replacement) {
		t.Fatal("GC cleaned up after the manifest identity changed")
	}
	if !pathExists(filepath.Join(project.ActiveDir(), branchSlug)) ||
		!pathExists(branchWorktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC cleaned up after the branch advanced")
	}
	if !pathExists(filepath.Join(project.ActiveDir(), worktreeSlug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, worktreeBranch) {
		t.Fatal("GC cleaned up after the worktree advanced")
	}
}

func TestGCArchiveWarningReturnsFailureAfterArchiving(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "archive-warning"
	branch, _ := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)

	previous := gcArchiveProject
	gcArchiveProject = func(proof archiveProofSnapshot, force bool) (archiveResult, error) {
		result, err := archiveProjectWithProof(proof, force)
		result.BranchDeletionWarning = "injected branch deletion warning"
		return result, err
	}
	t.Cleanup(func() { gcArchiveProject = previous })

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "injected branch deletion warning") {
		t.Fatalf("stderr %q is missing branch deletion warning", stderr)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(filepath.Join(project.ArchivedDir(), slug)) {
		t.Fatal("archive warning prevented metadata archival")
	}
	if archived := loadArchivedManifest(t, slug); archived.Status != "archived" || !archived.Merged {
		t.Fatalf("archived manifest = %+v, want archived merged metadata", archived)
	}
}

func TestGCPostStagingFailureRendersArchivedRecovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "post-staging-recovery"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)

	previousSave := saveArchiveManifest
	saveArchiveManifest = func(path string, manifest project.Manifest) error {
		if manifest.ArchiveCleanup != nil &&
			manifest.ArchiveCleanup.BranchState == project.ArchiveCleanupClaimed {
			return errors.New("injected branch claim persistence failure")
		}
		return project.Save(path, manifest)
	}
	t.Cleanup(func() { saveArchiveManifest = previousSave })

	stdout, stderr, err := captureGCOutput(t, runGC)
	saveArchiveManifest = previousSave
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{
		"Archived cleanup incomplete:",
		slug,
		"Worktree removed:",
		"injected branch claim persistence failure",
		"relay archive " + slug,
	} {
		if !strings.Contains(stdout+"\n"+stderr, want) {
			t.Fatalf("GC output %q / %q is missing %q", stdout, stderr, want)
		}
	}
	if strings.Contains(stderr, "relay archive "+slug+" --force") {
		t.Fatalf("stderr %q adds unnecessary --force recovery", stderr)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(filepath.Join(project.ArchivedDir(), slug)) ||
		pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC post-staging result does not match durable archived state")
	}

	if err := runArchive(slug, false); err != nil {
		t.Fatalf("runArchive recovery: %v", err)
	}
	if gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("relay archive recovery left the branch behind")
	}
}

func TestGCRecordedStateFailureIsActionable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "bad-state"
	_, _ = addGCProject(t, fixture, slug)
	statePath := project.StatePath(slug)
	if err := os.WriteFile(statePath, []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, statePath) || !strings.Contains(stderr, "parse state") {
		t.Fatalf("stderr %q is missing recorded-state diagnostic", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("GC removed project after recorded-state failure")
	}
}

func TestGCSkipsProgramManagedProjects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "program-managed"
	dir := filepath.Join(project.ActiveDir(), slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := project.Manifest{
		Slug: slug, Repo: repo, Branch: "unused/branch",
		Program: "delivery", ProgramItem: "worker-1",
	}
	path := project.ManifestPath(project.ActiveDir(), slug)
	if err := project.Save(path, manifest); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
	}
	for _, want := range []string{
		slug,
		"relay program worker cleanup delivery worker-1",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout %q is missing %q", stdout, want)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("GC changed complete program ownership metadata")
	}
}

func TestGCPreservesResourcesClaimedByStandaloneAndManagedManifests(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "duplicate-owner"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)

	managedSlug := "managed-duplicate-owner"
	managed := project.Manifest{
		Slug: managedSlug, Repo: fixture.repo, Branch: branch, Worktree: &worktree,
		Program: "delivery", ProgramItem: "worker-1",
	}
	if err := os.MkdirAll(filepath.Join(project.ActiveDir(), managedSlug), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(
		project.ManifestPath(project.ActiveDir(), managedSlug), managed,
	); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{slug, managedSlug, "branch", "worktree", "also claimed"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(filepath.Join(project.ActiveDir(), managedSlug)) ||
		pathExists(filepath.Join(project.ArchivedDir(), slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC changed resources with duplicate standalone and managed ownership")
	}
}

func TestGCPreservesResourcesClaimedByDuplicateStandaloneManifest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "standalone-owner"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)

	duplicate := project.Manifest{
		Slug: "standalone-duplicate", Repo: fixture.repo, Branch: branch, Worktree: &worktree,
	}
	duplicatePath := project.ManifestPath(project.ActiveDir(), duplicate.Slug)
	if err := os.MkdirAll(filepath.Dir(duplicatePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(duplicatePath, duplicate); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{slug, duplicate.Slug, "also claimed"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(filepath.Join(project.ActiveDir(), duplicate.Slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC changed resources with duplicate standalone ownership")
	}
}

func TestGCPreservesDuplicateBranchClaimsAcrossLinkedWorktrees(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "linked-worktree-owner"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)

	linkedRepo := filepath.Join(t.TempDir(), "linked-repository")
	runArchiveGit(t, fixture.repo, "worktree", "add", "-q", "--detach", linkedRepo, fixture.startSHA)
	duplicate := project.Manifest{
		Slug: "linked-worktree-duplicate", Repo: linkedRepo, Branch: branch,
	}
	duplicatePath := project.ManifestPath(project.ActiveDir(), duplicate.Slug)
	if err := os.MkdirAll(filepath.Dir(duplicatePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(duplicatePath, duplicate); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{slug, duplicate.Slug, "branch", "also claimed"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(filepath.Join(project.ActiveDir(), duplicate.Slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC changed a branch claimed through linked worktree repository paths")
	}
}

func TestGCPreservesResourcesClaimedByArchivedPendingCleanup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "active-owner"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)
	branchTip, found, err := gitx.LocalBranchTip(fixture.repo, branch)
	if err != nil || !found {
		t.Fatalf("branch tip = %q, %t, %v", branchTip, found, err)
	}
	worktreeState, found, err := gitx.RegisteredWorktreeState(fixture.repo, worktree)
	if err != nil || !found {
		t.Fatalf("worktree state = %+v, %t, %v", worktreeState, found, err)
	}

	archivedSlug := "archived-owner"
	archived := project.Manifest{
		Slug: archivedSlug, Repo: fixture.repo, Branch: branch, Worktree: &worktree,
		Status: "archived", Merged: true,
		ArchiveCleanup: &project.ArchiveCleanupProof{
			Repository:             fixture.repo,
			Branch:                 branch,
			Worktree:               worktree,
			BranchPresent:          true,
			ExpectedBranchTip:      branchTip,
			BranchState:            project.ArchiveCleanupPending,
			WorktreePresent:        true,
			ExpectedWorktreeTip:    worktreeState.Head,
			ExpectedWorktreeBranch: worktreeState.Branch,
			WorktreeState:          project.ArchiveCleanupPending,
			AuthoritativeCommit:    branchTip,
		},
	}
	archivedPath := project.ManifestPath(project.ArchivedDir(), archivedSlug)
	if err := os.MkdirAll(filepath.Dir(archivedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(archivedPath, archived); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{slug, archivedSlug, "also claimed"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC changed resources claimed by archived pending cleanup")
	}
}

func TestGCCorruptArchivedBranchBindingBecomesOwnershipClaim(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "active-corrupt-proof-owner"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)
	branchTip := gitx.RevParse(fixture.repo, "refs/heads/"+branch)
	worktreeState, found, err := gitx.RegisteredWorktreeState(fixture.repo, worktree)
	if err != nil || !found {
		t.Fatalf("worktree state = %+v, %t, %v", worktreeState, found, err)
	}

	archived := project.Manifest{
		Slug: "corrupt-proof-owner", Repo: fixture.repo, Branch: branch, Worktree: &worktree,
		Status: "archived", Merged: true,
		ArchiveCleanup: &project.ArchiveCleanupProof{
			Repository:             fixture.repo,
			Branch:                 branch,
			Worktree:               worktree,
			BranchPresent:          true,
			ExpectedBranchTip:      branchTip,
			BranchState:            project.ArchiveCleanupPending,
			WorktreePresent:        true,
			ExpectedWorktreeTip:    worktreeState.Head,
			ExpectedWorktreeBranch: worktreeState.Branch,
			WorktreeState:          project.ArchiveCleanupPending,
		},
	}
	archivedPath := project.ManifestPath(project.ArchivedDir(), archived.Slug)
	if err := os.MkdirAll(filepath.Dir(archivedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(archivedPath, archived); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{archived.Slug, slug, "does not bind branch", "could conflict"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if !pathExists(project.ManifestPath(project.ActiveDir(), slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC used a corrupt archived proof as deletion authority")
	}
}

func TestGCInvalidArchivedProofPreservesManifestAndProofResourceClaims(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	manifestRepo := newGCRepoFixture(t, "main")
	manifestSlug := "manifest-resource-owner"
	manifestBranch, manifestWorktree := addGCProject(t, manifestRepo, manifestSlug)
	mergeGCProjectUpstream(t, manifestRepo, manifestBranch)

	proofRepo := newGCRepoFixture(t, "main")
	proofSlug := "proof-resource-owner"
	proofBranch, proofWorktree := addGCProject(t, proofRepo, proofSlug)
	mergeGCProjectUpstream(t, proofRepo, proofBranch)
	proofBranchTip, found, err := gitx.LocalBranchTip(proofRepo.repo, proofBranch)
	if err != nil || !found {
		t.Fatalf("proof branch tip = %q, %t, %v", proofBranchTip, found, err)
	}
	proofWorktreeState, found, err := gitx.RegisteredWorktreeState(proofRepo.repo, proofWorktree)
	if err != nil || !found {
		t.Fatalf("proof worktree state = %+v, %t, %v", proofWorktreeState, found, err)
	}

	independentRepo := newGCRepoFixture(t, "main")
	independentSlug := "independent-resource-owner"
	independentBranch, independentWorktree := addGCProject(t, independentRepo, independentSlug)
	mergeGCProjectUpstream(t, independentRepo, independentBranch)

	archived := project.Manifest{
		Slug: "mismatched-archived-owner", Repo: manifestRepo.repo,
		Branch: manifestBranch, Worktree: &manifestWorktree, Status: "archived", Merged: true,
		ArchiveCleanup: &project.ArchiveCleanupProof{
			Repository:             proofRepo.repo,
			Branch:                 proofBranch,
			Worktree:               proofWorktree,
			BranchPresent:          true,
			ExpectedBranchTip:      proofBranchTip,
			BranchState:            project.ArchiveCleanupPending,
			WorktreePresent:        true,
			ExpectedWorktreeTip:    proofWorktreeState.Head,
			ExpectedWorktreeBranch: proofWorktreeState.Branch,
			WorktreeState:          project.ArchiveCleanupPending,
			AuthoritativeCommit:    proofBranchTip,
		},
	}
	archivedPath := project.ManifestPath(project.ArchivedDir(), archived.Slug)
	if err := os.MkdirAll(filepath.Dir(archivedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(archivedPath, archived); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{archivedPath, manifestSlug, proofSlug, "does not match"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	for _, resource := range []struct {
		slug, repo, branch, worktree string
	}{
		{manifestSlug, manifestRepo.repo, manifestBranch, manifestWorktree},
		{proofSlug, proofRepo.repo, proofBranch, proofWorktree},
	} {
		if !pathExists(filepath.Join(project.ActiveDir(), resource.slug)) ||
			!pathExists(resource.worktree) ||
			!gitx.BranchExists(resource.repo, resource.branch) {
			t.Fatalf("GC changed resources claimed by invalid archived proof: %+v", resource)
		}
	}
	if pathExists(filepath.Join(project.ActiveDir(), independentSlug)) ||
		pathExists(independentWorktree) ||
		gitx.BranchExists(independentRepo.repo, independentBranch) {
		t.Fatal("GC did not clean a repository independent of both invalid archived identities")
	}
}

func TestGCInvalidArchivedProofWithDeletedLinkedRepositoryFailsClosedGlobally(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	blockedRepo := newGCRepoFixture(t, "main")
	blockedSlug := "deleted-linked-owner"
	blockedBranch, blockedWorktree := addGCProject(t, blockedRepo, blockedSlug)
	mergeGCProjectUpstream(t, blockedRepo, blockedBranch)
	blockedTip, found, err := gitx.LocalBranchTip(blockedRepo.repo, blockedBranch)
	if err != nil || !found {
		t.Fatalf("blocked branch tip = %q, %t, %v", blockedTip, found, err)
	}
	blockedState, found, err := gitx.RegisteredWorktreeState(blockedRepo.repo, blockedWorktree)
	if err != nil || !found {
		t.Fatalf("blocked worktree state = %+v, %t, %v", blockedState, found, err)
	}

	deletedLinkedRepo := filepath.Join(t.TempDir(), "deleted-linked-repository")
	runArchiveGit(
		t, blockedRepo.repo, "worktree", "add", "-q", "--detach",
		deletedLinkedRepo, blockedRepo.startSHA,
	)
	runArchiveGit(t, blockedRepo.repo, "worktree", "remove", deletedLinkedRepo)

	independentRepo := newGCRepoFixture(t, "main")
	independentSlug := "globally-preserved-owner"
	independentBranch, independentWorktree := addGCProject(t, independentRepo, independentSlug)
	mergeGCProjectUpstream(t, independentRepo, independentBranch)

	archived := project.Manifest{
		Slug: "deleted-linked-archived-owner", Repo: blockedRepo.repo,
		Branch: blockedBranch, Worktree: &blockedWorktree, Status: "archived", Merged: true,
		ArchiveCleanup: &project.ArchiveCleanupProof{
			Repository:             deletedLinkedRepo,
			Branch:                 blockedBranch,
			Worktree:               blockedWorktree,
			BranchPresent:          true,
			ExpectedBranchTip:      blockedTip,
			BranchState:            project.ArchiveCleanupPending,
			WorktreePresent:        true,
			ExpectedWorktreeTip:    blockedState.Head,
			ExpectedWorktreeBranch: blockedState.Branch,
			WorktreeState:          project.ArchiveCleanupPending,
			AuthoritativeCommit:    blockedTip,
		},
	}
	archivedPath := project.ManifestPath(project.ArchivedDir(), archived.Slug)
	if err := os.MkdirAll(filepath.Dir(archivedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(archivedPath, archived); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{archivedPath, deletedLinkedRepo, blockedSlug, independentSlug} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	for _, resource := range []struct {
		slug, repo, branch, worktree string
	}{
		{blockedSlug, blockedRepo.repo, blockedBranch, blockedWorktree},
		{independentSlug, independentRepo.repo, independentBranch, independentWorktree},
	} {
		if !pathExists(filepath.Join(project.ActiveDir(), resource.slug)) ||
			!pathExists(resource.worktree) ||
			!gitx.BranchExists(resource.repo, resource.branch) {
			t.Fatalf("GC changed resources despite global invalid-proof conflict: %+v", resource)
		}
	}
}

func TestGCPreservesResourcesClaimedByLegacyArchivedManifest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "legacy-active-owner"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)

	archivedSlug := "legacy-archived-owner"
	archived := project.Manifest{
		Slug: archivedSlug, Repo: fixture.repo, Branch: branch, Worktree: &worktree,
		Status: "archived", Merged: true,
	}
	archivedPath := project.ManifestPath(project.ArchivedDir(), archivedSlug)
	if err := os.MkdirAll(filepath.Dir(archivedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(archivedPath, archived); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{slug, archivedSlug, "also claimed"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC changed resources claimed by a legacy archived manifest")
	}
}

func TestGCLegacyArchivedProbeFailureBlocksOnlyConflictingRepository(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	blockedRepo := newGCRepoFixture(t, "main")
	blockedSlug := "legacy-probe-active"
	blockedBranch, blockedWorktree := addGCProject(t, blockedRepo, blockedSlug)
	mergeGCProjectUpstream(t, blockedRepo, blockedBranch)

	archivedSlug := "legacy-probe-archived"
	archived := project.Manifest{
		Slug: archivedSlug, Repo: blockedRepo.repo, Branch: blockedBranch,
		Worktree: &blockedWorktree, Status: "archived", Merged: true,
	}
	archivedPath := project.ManifestPath(project.ArchivedDir(), archivedSlug)
	if err := os.MkdirAll(filepath.Dir(archivedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(archivedPath, archived); err != nil {
		t.Fatal(err)
	}

	eligibleRepo := newGCRepoFixture(t, "main")
	eligibleSlug := "legacy-probe-independent"
	eligibleBranch, eligibleWorktree := addGCProject(t, eligibleRepo, eligibleSlug)
	mergeGCProjectUpstream(t, eligibleRepo, eligibleBranch)

	previous := archiveBranchExists
	archiveBranchExists = func(repo, branch string) (bool, error) {
		if repo == blockedRepo.repo && branch == blockedBranch {
			return false, errors.New("injected legacy branch probe failure")
		}
		return previous(repo, branch)
	}
	t.Cleanup(func() { archiveBranchExists = previous })

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{
		archivedPath, blockedSlug, "injected legacy branch probe failure",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if !pathExists(filepath.Join(project.ActiveDir(), blockedSlug)) ||
		!pathExists(blockedWorktree) ||
		!gitx.BranchExists(blockedRepo.repo, blockedBranch) {
		t.Fatal("GC changed resources after a legacy archived ownership probe failure")
	}
	if pathExists(filepath.Join(project.ActiveDir(), eligibleSlug)) ||
		pathExists(eligibleWorktree) ||
		gitx.BranchExists(eligibleRepo.repo, eligibleBranch) {
		t.Fatal("GC did not clean a repository independent of a legacy ownership probe failure")
	}
}

func TestGCPreservesAndReportsPartialProgramOwnership(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	before := make(map[string][]byte)
	for _, ownership := range []struct {
		slug    string
		program string
		item    string
	}{
		{slug: "program-only", program: "delivery"},
		{slug: "item-only", item: "worker-1"},
	} {
		dir := filepath.Join(project.ActiveDir(), ownership.slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := project.Manifest{
			Slug: ownership.slug, Repo: "/unused/repo", Branch: "unused/branch",
			Program: ownership.program, ProgramItem: ownership.item,
		}
		manifestPath := project.ManifestPath(project.ActiveDir(), ownership.slug)
		if err := project.Save(manifestPath, manifest); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		before[ownership.slug] = content
	}

	stdout, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	for _, want := range []string{
		"program-only",
		"item-only",
		"program ownership requires both program and program_item",
		"preserving project",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if strings.Contains(stdout, "relay program worker cleanup") {
		t.Fatalf("stdout %q gives worker guidance for malformed ownership", stdout)
	}
	for slug, expected := range before {
		path := project.ManifestPath(project.ActiveDir(), slug)
		after, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !reflect.DeepEqual(after, expected) ||
			pathExists(filepath.Join(project.ArchivedDir(), slug)) {
			t.Fatalf("GC changed partially-owned project %s", slug)
		}
	}
}

func TestGCEmptyStoreSucceedsWithoutCreatingProjectData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	stdout, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("runGC output = %q / %q, want empty", stdout, stderr)
	}
	if pathExists(project.ActiveDir()) || pathExists(project.ArchivedDir()) {
		t.Fatal("GC created project directories for an empty store")
	}
}

func installFetchCounter(t *testing.T, repo string) func() int {
	t.Helper()
	dir := t.TempDir()
	countPath := filepath.Join(dir, "fetch-count")
	scriptPath := filepath.Join(dir, "upload-pack")
	script := "#!/bin/sh\nprintf 'fetch\\n' >> '" + countPath + "'\nexec git-upload-pack \"$@\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	runArchiveGit(t, repo, "config", "remote.origin.uploadpack", scriptPath)
	return func() int {
		data, err := os.ReadFile(countPath)
		if err != nil {
			if os.IsNotExist(err) {
				return 0
			}
			t.Fatal(err)
		}
		return strings.Count(string(data), "fetch\n")
	}
}

func updateGCManifest(t *testing.T, slug string, update func(*project.Manifest)) {
	t.Helper()
	path := project.ManifestPath(project.ActiveDir(), slug)
	manifest, err := project.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	update(&manifest)
	if err := project.Save(path, manifest); err != nil {
		t.Fatal(err)
	}
}

func assertGCProjectUnchanged(
	t *testing.T,
	manifestPath string,
	before []byte,
	repo, branch, worktree string,
) {
	t.Helper()
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read preserved manifest: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("GC changed potential victim metadata")
	}
	if !pathExists(worktree) {
		t.Fatal("GC removed potential victim worktree")
	}
	if !gitx.BranchExists(repo, branch) {
		t.Fatal("GC removed potential victim branch")
	}
}
