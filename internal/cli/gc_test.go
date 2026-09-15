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
			if base == "master" {
				updateGCManifest(t, slug, func(manifest *project.Manifest) {
					manifest.BaseBranch = ""
				})
				runArchiveGit(t, fixture.repo, "checkout", "-q", "--detach", fixture.startSHA)
				runArchiveGit(t, fixture.repo, "branch", "-D", base)
			}

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

func TestGCArchivesMergedPullRequestWithSquashOrDeletedBranch(t *testing.T) {
	for _, deleteBranch := range []bool{false, true} {
		name := "squash"
		if deleteBranch {
			name = "deleted-branch"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fixture := newGCRepoFixture(t, "main")
			slug := "pr-" + name
			branch, worktree := addGCProject(t, fixture, slug)
			recordArchiveManifestPR(t, slug, 701)
			installArchivePRIndex(t, map[string]programview.PRState{"#701": programview.PRStateMerged})
			if deleteBranch {
				runArchiveGit(t, fixture.repo, "worktree", "remove", "--force", worktree)
				runArchiveGit(t, fixture.repo, "branch", "-D", branch)
			}

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

func TestGCKeepsMissingBranchAndUnknownPullRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	for _, slug := range []string{"missing-branch", "unknown-pr"} {
		branch, worktree := addGCProject(t, fixture, slug)
		runArchiveGit(t, fixture.repo, "worktree", "remove", "--force", worktree)
		runArchiveGit(t, fixture.repo, "branch", "-D", branch)
	}
	recordArchiveManifestPR(t, "unknown-pr", 706)
	previous := loadArchivePRIndex
	loadArchivePRIndex = func(string, []string) programview.PRIndex { return nil }
	t.Cleanup(func() { loadArchivePRIndex = previous })

	stdout, stderr, err := captureGCOutput(t, runGC)
	if err != nil {
		t.Fatalf("runGC: %v\nstderr: %s", err, stderr)
	}
	if stdout != "" {
		t.Fatalf("runGC wrote unexpected output: %q", stdout)
	}
	for _, slug := range []string{"missing-branch", "unknown-pr"} {
		if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
			t.Fatalf("GC removed unresolved project %s", slug)
		}
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

func TestGCContinuesAfterArchiveFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	failingRepo := newGCRepoFixture(t, "main")
	failingBranch, _ := addGCProject(t, failingRepo, "a-archive-failure")
	mergeGCProjectUpstream(t, failingRepo, failingBranch)
	successRepo := newGCRepoFixture(t, "main")
	successBranch, _ := addGCProject(t, successRepo, "z-archive-success")
	mergeGCProjectUpstream(t, successRepo, successBranch)

	previous := gcArchiveProject
	gcArchiveProject = func(slug string, force bool) (archiveResult, error) {
		if slug == "a-archive-failure" {
			return archiveResult{}, errors.New("injected cleanup failure")
		}
		return archiveProject(slug, force)
	}
	t.Cleanup(func() { gcArchiveProject = previous })

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "archive a-archive-failure: injected cleanup failure") {
		t.Fatalf("stderr %q is missing archive failure", stderr)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), "a-archive-failure")) {
		t.Fatal("failed archive project was removed")
	}
	if pathExists(filepath.Join(project.ActiveDir(), "z-archive-success")) {
		t.Fatal("GC did not continue after archive failure")
	}
}

func TestGCArchiveWarningReturnsFailureAfterArchiving(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newGCRepoFixture(t, "main")
	slug := "archive-warning"
	branch, _ := addGCProject(t, fixture, slug)
	mergeGCProjectUpstream(t, fixture, branch)

	previous := gcArchiveProject
	gcArchiveProject = func(slug string, force bool) (archiveResult, error) {
		result, err := archiveProject(slug, force)
		result.Warnings = append(result.Warnings, "injected branch cleanup warning")
		return result, err
	}
	t.Cleanup(func() { gcArchiveProject = previous })

	_, stderr, err := captureGCOutput(t, runGC)
	if !errors.Is(err, errGCCompletedWithErrors) {
		t.Fatalf("runGC error = %v, want %v", err, errGCCompletedWithErrors)
	}
	if !strings.Contains(stderr, "injected branch cleanup warning") {
		t.Fatalf("stderr %q is missing archive warning", stderr)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(filepath.Join(project.ArchivedDir(), slug)) {
		t.Fatal("archive warning prevented metadata archival")
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
	for _, manifest := range []project.Manifest{
		{Slug: "program-only", Program: "delivery"},
		{Slug: "item-only", ProgramItem: "worker-1"},
	} {
		dir := filepath.Join(project.ActiveDir(), manifest.Slug)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := project.Save(filepath.Join(dir, "manifest.json"), manifest); err != nil {
			t.Fatal(err)
		}
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
	for _, slug := range []string{"program-only", "item-only"} {
		if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
			t.Fatalf("GC removed program-managed project %s", slug)
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
