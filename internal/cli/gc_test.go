package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
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
	runArchiveGit(t, fixture.repo, "push", "-q", "origin", branch)
	runArchiveGit(t, fixture.upstream, "fetch", "-q", "origin", branch)
	runArchiveGit(t, fixture.upstream, "merge", "-q", "--no-edit", "origin/"+branch)
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
	if archived := loadArchivedManifest(t, slug); !archived.Merged {
		t.Fatal("GC did not record the force-cleaned project as merged")
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

func TestGCContinuesAfterMalformedAndInvalidMetadata(t *testing.T) {
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
	if pathExists(filepath.Join(project.ActiveDir(), "z-eligible")) {
		t.Fatal("GC did not continue to archive an independent eligible project")
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

func TestGCRejectsManifestIdentityChangeAfterMergeProof(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "manifest-race"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)
	replacement := "user/replacement"
	runArchiveGit(t, fixture.repo, "branch", replacement, "refs/heads/"+branch)
	previous := gcArchiveProject
	gcArchiveProject = func(proof archiveProofSnapshot, force bool) (archiveResult, error) {
		updateGCManifest(t, proof.Slug, func(manifest *project.Manifest) {
			manifest.Branch = replacement
		})
		return archiveProjectWithProof(proof, force)
	}
	t.Cleanup(func() { gcArchiveProject = previous })

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "manifest changed") {
		t.Fatalf("stderr %q is missing stale manifest proof diagnostic", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) ||
		!gitx.BranchExists(fixture.repo, replacement) {
		t.Fatal("GC cleaned up after the manifest identity changed")
	}
}

func TestGCRejectsBranchAdvanceAfterMergeProof(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "branch-race"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)
	previous := gcArchiveProject
	gcArchiveProject = func(proof archiveProofSnapshot, force bool) (archiveResult, error) {
		commitArchiveFile(t, worktree, "later.txt", "later\n", "advance after proof")
		return archiveProjectWithProof(proof, force)
	}
	t.Cleanup(func() { gcArchiveProject = previous })

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "branch tip changed") {
		t.Fatalf("stderr %q is missing stale branch proof diagnostic", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
		t.Fatal("GC cleaned up after the branch advanced")
	}
}

func TestGCRejectsDetachedWorktreeAdvanceAfterMergeProof(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "worktree-race"
	branch, worktree := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)
	previous := gcArchiveProject
	gcArchiveProject = func(proof archiveProofSnapshot, force bool) (archiveResult, error) {
		runArchiveGit(t, worktree, "checkout", "-q", "--detach")
		commitArchiveFile(t, worktree, "later.txt", "later\n", "advance detached after proof")
		return archiveProjectWithProof(proof, force)
	}
	t.Cleanup(func() { gcArchiveProject = previous })

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "worktree tip changed") {
		t.Fatalf("stderr %q is missing stale worktree proof diagnostic", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(worktree) ||
		!gitx.BranchExists(fixture.repo, branch) {
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
	fixture := newGCRepoFixture(t, "main")
	type managedProject struct {
		slug     string
		branch   string
		worktree string
		before   []byte
	}
	var managed []managedProject
	for _, ownership := range []struct {
		slug    string
		program string
		item    string
	}{
		{slug: "program-only", program: "delivery"},
		{slug: "item-only", item: "worker-1"},
	} {
		branch, worktree := addGCProject(t, fixture, ownership.slug)
		mergeGCProjectUpstream(t, fixture, branch)
		updateGCManifest(t, ownership.slug, func(manifest *project.Manifest) {
			manifest.Program = ownership.program
			manifest.ProgramItem = ownership.item
		})
		manifestPath := project.ManifestPath(project.ActiveDir(), ownership.slug)
		before, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		managed = append(managed, managedProject{
			slug: ownership.slug, branch: branch, worktree: worktree, before: before,
		})
	}

	stdout, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
	}
	for _, want := range []string{
		"program-only",
		"relay program worker cleanup delivery <item>",
		"item-only",
		"relay program worker cleanup <program> worker-1",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout %q is missing %q", stdout, want)
		}
	}
	for _, item := range managed {
		assertGCProjectUnchanged(
			t,
			project.ManifestPath(project.ActiveDir(), item.slug),
			item.before,
			fixture.repo,
			item.branch,
			item.worktree,
		)
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
