package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fetchSnapshotResult struct {
	commit string
	err    error
}

// initRepo creates a throwaway git repo with one commit and returns its root.
func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
			"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README")
	run("commit", "-q", "-m", "init")
	return repo
}

func TestWorktreeRemoveRegistered(t *testing.T) {
	repo := initRepo(t)
	dir := filepath.Join(repo, ".worktrees", "wt")
	cmd := exec.Command("git", "-C", repo, "worktree", "add", "-q", dir, "-b", "feature", "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	if registered, err := IsWorktree(repo, dir); err != nil || !registered {
		t.Fatalf("IsWorktree = %v, %v; want true, nil", registered, err)
	}
	if err := WorktreeRemove(repo, dir, false); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present: %v", err)
	}
}

// TestWorktreeRemoveMissingWorktree reproduces the interrupted-setup case: the
// manifest points at a path that git does not consider a working tree.
func TestWorktreeRemoveMissingWorktree(t *testing.T) {
	repo := initRepo(t)
	dir := filepath.Join(repo, ".worktrees", "never-registered")

	if err := WorktreeRemove(repo, dir, false); err != nil {
		t.Fatalf("WorktreeRemove (absent dir): %v", err)
	}
}

func TestWorktreeRemovePreservesExistingUnregisteredDirectory(t *testing.T) {
	repo := initRepo(t)
	dir := filepath.Join(repo, ".worktrees", "never-registered")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir leftover: %v", err)
	}
	marker := filepath.Join(dir, "stale")
	if err := os.WriteFile(marker, []byte("x"), 0644); err != nil {
		t.Fatalf("write leftover: %v", err)
	}
	err := WorktreeRemove(repo, dir, false)
	if err == nil || !strings.Contains(err.Error(), "exists but is not registered") {
		t.Fatalf("WorktreeRemove error = %v, want unregistered path diagnostic", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "x" {
		t.Fatalf("unregistered directory changed: data=%q err=%v", data, err)
	}
}

func TestWorktreeRemoveRejectsDanglingSymlink(t *testing.T) {
	repo := initRepo(t)
	dir := filepath.Join(repo, ".worktrees", "dangling")
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), dir); err != nil {
		t.Fatal(err)
	}

	err := WorktreeRemove(repo, dir, true)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("WorktreeRemove error = %v, want symlink rejection", err)
	}
	if _, statErr := os.Lstat(dir); statErr != nil {
		t.Fatalf("dangling symlink was removed: %v", statErr)
	}
}

func TestWorktreeReclaimRestrictsUnregisteredPathsToRelayWorktreeRoot(t *testing.T) {
	repo := initRepo(t)
	owned := filepath.Join(repo, ".worktrees", "interrupted")
	if err := os.MkdirAll(owned, 0755); err != nil {
		t.Fatal(err)
	}
	if err := WorktreeReclaim(repo, owned, true); err != nil {
		t.Fatalf("WorktreeReclaim owned path: %v", err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned path still exists: %v", err)
	}

	outside := filepath.Join(repo, "unrelated")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	err := WorktreeReclaim(repo, outside, true)
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("WorktreeReclaim outside error = %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside path was changed: %v", err)
	}
}

func TestWorktreeReclaimRejectsTargetsOutsideRelayWorktreeRoot(t *testing.T) {
	repo := initRepo(t)
	externalWorktree := filepath.Join(t.TempDir(), "registered")
	runGit(t, repo, "worktree", "add", "-q", externalWorktree, "-b", "external", "HEAD")

	tests := []struct {
		name string
		dir  string
	}{
		{name: "repository root", dir: repo},
		{name: "parent traversal", dir: filepath.Join(repo, "..")},
		{name: "absolute external worktree", dir: externalWorktree},
		{name: "escaping relative path", dir: filepath.Join(repo, ".worktrees", "..", "outside")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := WorktreeReclaim(repo, test.dir, true)
			if err == nil || !strings.Contains(err.Error(), "refuse to reclaim") {
				t.Fatalf("WorktreeReclaim(%q) error = %v, want containment rejection", test.dir, err)
			}
		})
	}
	if _, err := os.Stat(repo); err != nil {
		t.Fatalf("repository root was changed: %v", err)
	}
	if _, err := os.Stat(externalWorktree); err != nil {
		t.Fatalf("external worktree was changed: %v", err)
	}
}

func TestWorktreeReclaimRejectsSymlinkedRelayWorktreeRoot(t *testing.T) {
	repo := initRepo(t)
	externalRoot := t.TempDir()
	target := filepath.Join(externalRoot, "interrupted")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "keep")
	if err := os.WriteFile(marker, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalRoot, filepath.Join(repo, ".worktrees")); err != nil {
		t.Fatal(err)
	}

	err := WorktreeReclaim(repo, filepath.Join(repo, ".worktrees", "interrupted"), true)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("WorktreeReclaim error = %v, want symlinked root rejection", err)
	}
	if data, readErr := os.ReadFile(marker); readErr != nil || string(data) != "keep\n" {
		t.Fatalf("external target changed: data=%q err=%v", data, readErr)
	}
}

func TestWorktreeReclaimRemovesOnlySymlinkTarget(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(repo, ".worktrees")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	marker := filepath.Join(external, "keep")
	if err := os.WriteFile(marker, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "escape")
	if err := os.Symlink(external, target); err != nil {
		t.Fatal(err)
	}

	if err := WorktreeReclaim(repo, target, true); err != nil {
		t.Fatalf("WorktreeReclaim symlink: %v", err)
	}
	if data, readErr := os.ReadFile(marker); readErr != nil || string(data) != "keep\n" {
		t.Fatalf("external target changed: data=%q err=%v", data, readErr)
	}
	if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		t.Fatalf("symlink still exists: %v", statErr)
	}
}

func TestWorktreeHeadRejectsExistingUnregisteredDirectory(t *testing.T) {
	repo := initRepo(t)
	dir := filepath.Join(repo, ".worktrees", "leftover")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	sha, found, err := WorktreeHead(repo, dir)
	if err == nil || !strings.Contains(err.Error(), "exists but is not registered") {
		t.Fatalf("WorktreeHead error = %v, want unregistered path diagnostic", err)
	}
	if found || sha != "" {
		t.Fatalf("WorktreeHead = (%q, %t), want empty, false", sha, found)
	}
}

func TestWorktreeHeadRejectsDanglingSymlink(t *testing.T) {
	repo := initRepo(t)
	dir := filepath.Join(repo, ".worktrees", "dangling")
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), dir); err != nil {
		t.Fatal(err)
	}

	sha, found, err := WorktreeHead(repo, dir)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("WorktreeHead error = %v, want symlink rejection", err)
	}
	if found || sha != "" {
		t.Fatalf("WorktreeHead = (%q, %t), want empty, false", sha, found)
	}
}

func TestRegisteredWorktreeStateFindsAndRemovesMissingPath(t *testing.T) {
	repo := initRepo(t)
	dir := filepath.Join(repo, ".worktrees", "missing")
	runGit(t, repo, "worktree", "add", "-q", dir, "-b", "missing", "HEAD")
	want := WorktreeState{
		Head:   gitOutput(t, repo, "rev-parse", "refs/heads/missing"),
		Branch: "refs/heads/missing",
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	state, found, err := RegisteredWorktreeState(repo, dir)
	if err != nil || !found || state != want {
		t.Fatalf("RegisteredWorktreeState = (%+v, %t, %v), want (%+v, true, nil)", state, found, err, want)
	}
	if err := WorktreeRemoveAt(repo, dir, want, true); err != nil {
		t.Fatalf("WorktreeRemoveAt: %v", err)
	}
	if registered, err := IsWorktree(repo, dir); err != nil || registered {
		t.Fatalf("IsWorktree after removal = (%t, %v), want (false, nil)", registered, err)
	}
}

func TestGitValueHelpersIgnoreSuccessfulStderr(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	runGit(t, repo, "remote", "add", "origin", "https://example.com/acme/widgets.git")
	runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+base)
	worktree := filepath.Join(repo, ".worktrees", "traced")
	runGit(t, repo, "worktree", "add", "-q", worktree, "-b", "traced", "HEAD")
	wantSHA := gitOutput(t, repo, "rev-parse", "refs/heads/traced")
	t.Setenv("GIT_TRACE", "1")

	branchTip, found, err := LocalBranchTip(repo, "traced")
	if err != nil || !found || branchTip != wantSHA {
		t.Fatalf("LocalBranchTip = (%q, %t, %v), want (%q, true, nil)", branchTip, found, err, wantSHA)
	}
	worktreeHead, found, err := WorktreeHead(repo, worktree)
	if err != nil || !found || worktreeHead != wantSHA {
		t.Fatalf("WorktreeHead = (%q, %t, %v), want (%q, true, nil)", worktreeHead, found, err, wantSHA)
	}
	origin, err := OriginURL(repo)
	if err != nil || origin != "https://example.com/acme/widgets.git" {
		t.Fatalf("OriginURL = (%q, %v), want exact URL", origin, err)
	}
	detected, err := DetectDefaultBranchWithError(repo)
	if err != nil || detected != base {
		t.Fatalf("DetectDefaultBranchWithError = (%q, %v), want (%q, nil)", detected, err, base)
	}
	registered, err := IsWorktree(repo, worktree)
	if err != nil || !registered {
		t.Fatalf("IsWorktree = (%t, %v), want (true, nil)", registered, err)
	}
	clean, err := WorktreeClean(worktree)
	if err != nil || !clean {
		t.Fatalf("WorktreeClean = (%t, %v), want (true, nil)", clean, err)
	}
}

func TestFetchUpdatesRemoteTrackingBase(t *testing.T) {
	for _, base := range []string{"main", "master"} {
		t.Run(base, func(t *testing.T) {
			_, source, repo := initRemoteRepo(t, base)
			initial := gitOutput(t, repo, "rev-parse", "origin/"+base)
			advanceRepo(t, source, "upstream.txt", "upstream\n", "advance upstream")
			upstream := gitOutput(t, source, "rev-parse", base)
			if initial == upstream {
				t.Fatal("upstream did not advance")
			}

			if _, err := Fetch(repo, base); err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if got := gitOutput(t, repo, "rev-parse", "origin/"+base); got != upstream {
				t.Fatalf("origin/%s = %s, want %s", base, got, upstream)
			}
		})
	}
}

func TestFetchReplacesStaleRemoteTrackingRefWithoutLocalBase(t *testing.T) {
	_, source, repo := initRemoteRepo(t, "main")
	initial := gitOutput(t, source, "rev-parse", "main")
	advanceRepo(t, source, "upstream.txt", "upstream\n", "advance upstream")
	if _, err := Fetch(repo, "main"); err != nil {
		t.Fatalf("initial Fetch: %v", err)
	}
	runGit(t, repo, "checkout", "-q", "--detach", "origin/main")
	runGit(t, repo, "branch", "-D", "main")
	runGit(t, source, "reset", "--hard", initial)
	runGit(t, source, "push", "--force", "origin", "main")

	if _, err := Fetch(repo, "main"); err != nil {
		t.Fatalf("Fetch after force-push: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", "origin/main"); got != initial {
		t.Fatalf("origin/main = %s, want force-updated %s", got, initial)
	}
	if BranchExists(repo, "main") {
		t.Fatal("Fetch recreated the absent local main branch")
	}
}

func TestFetchBaseSnapshotRetainsFetchedCommitAfterTrackingRefChanges(t *testing.T) {
	_, source, repo := initRemoteRepo(t, "main")
	initial := gitOutput(t, repo, "rev-parse", "origin/main")
	advanceRepo(t, source, "upstream.txt", "upstream\n", "advance upstream")
	upstream := gitOutput(t, source, "rev-parse", "main")

	_, snapshot, err := FetchBaseSnapshot(repo, "main")
	if err != nil {
		t.Fatalf("FetchBaseSnapshot: %v", err)
	}
	if snapshot != upstream {
		t.Fatalf("FetchBaseSnapshot commit = %s, want %s", snapshot, upstream)
	}
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", initial)
	if got := gitOutput(t, repo, "rev-parse", "origin/main"); got != initial {
		t.Fatalf("origin/main = %s, want overwritten %s", got, initial)
	}
	reachable, err := CommitReachable(repo, upstream, snapshot)
	if err != nil || !reachable {
		t.Fatalf("CommitReachable(snapshot) = (%t, %v), want (true, nil)", reachable, err)
	}
}

func TestFetchBaseSnapshotConcurrentFetchesResolveIsolatedCommits(t *testing.T) {
	_, source, repo := initRemoteRepo(t, "main")
	mainCommit := gitOutput(t, source, "rev-parse", "main")
	runGit(t, source, "checkout", "-q", "-b", "topic")
	advanceRepo(t, source, "topic.txt", "topic\n", "advance topic")
	topicCommit := gitOutput(t, source, "rev-parse", "topic")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapperDir := t.TempDir()
	gateDir := filepath.Join(wrapperDir, "gate")
	if err := os.Mkdir(gateDir, 0755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$3\" = rev-parse ] && [ \"$4\" = --verify ]; then\n" +
		"  case \"$5\" in\n" +
		"    'FETCH_HEAD^{commit}'|refs/relay/fetch/*'^{commit}')\n" +
		"      if mkdir '" + gateDir + "/first' 2>/dev/null; then\n" +
		"        printf '%s\\n' \"$5\" > '" + gateDir + "/first-ref'\n" +
		"        : > '" + gateDir + "/first-ready'\n" +
		"        while [ ! -f '" + gateDir + "/release' ]; do sleep 0.01; done\n" +
		"      else\n" +
		"        printf '%s\\n' \"$5\" > '" + gateDir + "/second-ref'\n" +
		"      fi\n" +
		"      ;;\n" +
		"  esac\n" +
		"fi\n" +
		"exec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapperDir, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	release := filepath.Join(gateDir, "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0644) })

	fetch := func(branch string) <-chan fetchSnapshotResult {
		result := make(chan fetchSnapshotResult, 1)
		go func() {
			_, commit, err := FetchBaseSnapshot(repo, branch)
			result <- fetchSnapshotResult{commit: commit, err: err}
		}()
		return result
	}
	first := fetch("main")
	waitForFile(t, filepath.Join(gateDir, "first-ready"))
	second := fetch("topic")

	secondResult := awaitFetchResult(t, second)
	if err := os.WriteFile(release, nil, 0644); err != nil {
		t.Fatal(err)
	}
	firstResult := awaitFetchResult(t, first)
	if firstResult.err != nil || firstResult.commit != mainCommit {
		t.Fatalf(
			"concurrent main snapshot = (%q, %v), want (%q, nil)",
			firstResult.commit, firstResult.err, mainCommit,
		)
	}
	if secondResult.err != nil || secondResult.commit != topicCommit {
		t.Fatalf(
			"concurrent topic snapshot = (%q, %v), want (%q, nil)",
			secondResult.commit, secondResult.err, topicCommit,
		)
	}

	firstRef := strings.TrimSpace(readTestFile(t, filepath.Join(gateDir, "first-ref")))
	secondRef := strings.TrimSpace(readTestFile(t, filepath.Join(gateDir, "second-ref")))
	for _, ref := range []string{firstRef, secondRef} {
		if !strings.HasPrefix(ref, "refs/relay/fetch/") ||
			!strings.HasSuffix(ref, "^{commit}") {
			t.Fatalf("snapshot resolved through unsafe ref %q", ref)
		}
	}
	if firstRef == secondRef {
		t.Fatalf("concurrent snapshots shared temporary ref %q", firstRef)
	}
	if refs := gitOutput(t, repo, "for-each-ref", "--format=%(refname)", "refs/relay/fetch"); refs != "" {
		t.Fatalf("temporary fetch refs remain after cleanup: %s", refs)
	}
}

func TestOriginURLPreservesGitDiagnostic(t *testing.T) {
	repo := initRepo(t)

	_, err := OriginURL(repo)
	if err == nil {
		t.Fatal("OriginURL error = nil")
	}
	for _, want := range []string{"git remote get-url origin", "No such remote"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("OriginURL error %q is missing %q", err, want)
		}
	}
}

func TestLocalBranchExistsReturnsGitFailure(t *testing.T) {
	repo := t.TempDir()

	exists, err := LocalBranchExists(repo, "feature")
	if err == nil {
		t.Fatal("LocalBranchExists error = nil")
	}
	if exists {
		t.Fatal("LocalBranchExists reported a branch after git failed")
	}
	for _, want := range []string{"git show-ref --verify refs/heads/feature", "not a git repository"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("LocalBranchExists error %q is missing %q", err, want)
		}
	}
}

func TestForceDeleteBranchAtRejectsAdvancedBranch(t *testing.T) {
	repo := initRepo(t)
	oldTip := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "branch", "feature", oldTip)
	if err := os.WriteFile(filepath.Join(repo, "later"), []byte("later\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "later")
	runGit(t, repo, "commit", "-q", "-m", "advance")
	newTip := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "branch", "-f", "feature", newTip)

	err := ForceDeleteBranchAt(repo, "feature", oldTip)
	if err == nil || !strings.Contains(err.Error(), "changed from") ||
		!strings.Contains(err.Error(), "git update-ref -d refs/heads/feature") {
		t.Fatalf("ForceDeleteBranchAt error = %v, want atomic changed-tip rejection", err)
	}
	tip, found, tipErr := LocalBranchTip(repo, "feature")
	if tipErr != nil || !found || tip != newTip {
		t.Fatalf("feature tip = (%q, %t, %v), want (%q, true, nil)", tip, found, tipErr, newTip)
	}
}

func TestForceDeleteBranchAtRejectsBranchCheckedOutInLinkedWorktree(t *testing.T) {
	repo := initRepo(t)
	tip := gitOutput(t, repo, "rev-parse", "HEAD")
	worktree := filepath.Join(t.TempDir(), "feature-worktree")
	runGit(t, repo, "worktree", "add", "-q", "-b", "feature", worktree, tip)

	err := ForceDeleteBranchAt(repo, "feature", tip)
	if err == nil || !strings.Contains(err.Error(), "checked out") ||
		!strings.Contains(err.Error(), filepath.Base(worktree)) {
		t.Fatalf("ForceDeleteBranchAt error = %v, want checked-out worktree rejection", err)
	}
	var checkedOutErr *BranchCheckedOutError
	if !errors.As(err, &checkedOutErr) {
		t.Fatalf("ForceDeleteBranchAt error = %T, want *BranchCheckedOutError", err)
	}
	canonicalWorktree, canonicalErr := CanonicalPath(worktree)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	if checkedOutErr.Branch != "feature" || checkedOutErr.Worktree != canonicalWorktree {
		t.Fatalf("checked-out error = %+v, want feature in %s", checkedOutErr, canonicalWorktree)
	}
	got, found, tipErr := LocalBranchTip(repo, "feature")
	if tipErr != nil || !found || got != tip {
		t.Fatalf("feature tip = (%q, %t, %v), want (%q, true, nil)", got, found, tipErr, tip)
	}
}

func TestForceDeleteBranchAtRemovesBranchConfig(t *testing.T) {
	repo := initRepo(t)
	tip := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "branch", "feature/config", tip)
	runGit(t, repo, "config", "--local", "branch.feature/config.remote", "origin")
	runGit(t, repo, "config", "--local", "branch.feature/config.merge", "refs/heads/feature/config")

	if err := ForceDeleteBranchAt(repo, "feature/config", tip); err != nil {
		t.Fatalf("ForceDeleteBranchAt: %v", err)
	}
	if BranchExists(repo, "feature/config") {
		t.Fatal("branch ref survived deletion")
	}
	cmd := exec.Command(
		"git", "-C", repo, "config", "--local", "--get", "branch.feature/config.remote",
	)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("branch config survived deletion: %s", out)
	}
}

func TestForceDeleteBranchAtDoesNotRecreateRefWhenConfigCleanupFails(t *testing.T) {
	repo := initRepo(t)
	tip := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "branch", "feature", tip)
	previous := removeBranchConfig
	removeBranchConfig = func(string, string) error {
		return errors.New("injected branch config cleanup failure")
	}
	t.Cleanup(func() { removeBranchConfig = previous })

	err := ForceDeleteBranchAt(repo, "feature", tip)
	if err == nil || !strings.Contains(err.Error(), "injected branch config cleanup failure") ||
		!strings.Contains(err.Error(), "was deleted") {
		t.Fatalf("ForceDeleteBranchAt error = %v, want partial config cleanup failure", err)
	}
	if BranchExists(repo, "feature") {
		t.Fatal("branch ref was recreated after config cleanup failed")
	}
}

func TestRemoveBranchConfigTreatsDottedSiblingAsDistinctSection(t *testing.T) {
	repo := initRepo(t)
	runGit(t, repo, "config", "--local", "branch.api.v2.remote", "origin")
	t.Setenv("GIT_TRACE", "1")

	if err := RemoveBranchConfig(repo, "api"); err != nil {
		t.Fatalf("RemoveBranchConfig(api): %v", err)
	}
	t.Setenv("GIT_TRACE", "0")
	if got := gitOutput(t, repo, "config", "--local", "--get", "branch.api.v2.remote"); got != "origin" {
		t.Fatalf("branch.api.v2.remote = %q, want preserved sibling value", got)
	}
}

func TestRemoveBranchConfigRemovesExactSectionAndPreservesDottedSibling(t *testing.T) {
	repo := initRepo(t)
	runGit(t, repo, "config", "--local", "branch.api.remote", "origin")
	runGit(t, repo, "config", "--local", "branch.api.merge", "refs/heads/api")
	runGit(t, repo, "config", "--local", "branch.api.v2.remote", "upstream")

	if err := RemoveBranchConfig(repo, "api"); err != nil {
		t.Fatalf("RemoveBranchConfig(api): %v", err)
	}
	cmd := exec.Command("git", "-C", repo, "config", "--local", "--get", "branch.api.remote")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("branch.api.remote survived cleanup: %s", out)
	}
	if got := gitOutput(t, repo, "config", "--local", "--get", "branch.api.v2.remote"); got != "upstream" {
		t.Fatalf("branch.api.v2.remote = %q, want preserved sibling value", got)
	}
}

func TestIsWorktreePreservesGitDiagnostic(t *testing.T) {
	repo := t.TempDir()

	registered, err := IsWorktree(repo, filepath.Join(repo, "worktree"))
	if err == nil {
		t.Fatal("IsWorktree error = nil")
	}
	if registered {
		t.Fatal("IsWorktree reported a worktree after git failed")
	}
	for _, want := range []string{"git worktree list", "not a git repository"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("IsWorktree error %q is missing %q", err, want)
		}
	}
}

func TestSanitizeDiagnosticRedactsGitURLUserinfo(t *testing.T) {
	input := strings.Join([]string{
		"fatal: unable to access 'https://relay:secret@example.com/repo.git/': denied",
		"remote ftp://ftp-token@example.org/team/repo.git",
		"remote ssh://ssh-token@git.example.net/team/repo.git",
		"remote scp-token@git.example.io:team/repo.git",
		"remote deploy-token@git.example:repo",
	}, "\n")
	got := SanitizeDiagnostic(input)
	for _, secret := range []string{"relay", "secret", "ftp-token", "ssh-token", "scp-token", "deploy-token"} {
		if strings.Contains(got, secret) {
			t.Fatalf("SanitizeDiagnostic(%q) leaked %q in %q", input, secret, got)
		}
	}
	for _, want := range []string{
		"https://[redacted]@example.com/repo.git",
		"ftp://[redacted]@example.org/team/repo.git",
		"ssh://[redacted]@git.example.net/team/repo.git",
		"[redacted]@git.example.io:team/repo.git",
		"[redacted]@git.example:repo",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("SanitizeDiagnostic(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSanitizeDiagnosticRedactsGitURLQueryAndFragment(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		secrets []string
	}{
		{
			name:  "https",
			input: "fatal: unable to access 'https://example.com/repo.git?access_token=secret#scope': denied",
			want:  "fatal: unable to access 'https://example.com/repo.git': denied",
		},
		{
			name:  "http",
			input: "remote: http://example.com/team/repo.git?token=secret#scope",
			want:  "remote: http://example.com/team/repo.git",
		},
		{
			name:  "ftp",
			input: "remote: ftp://example.org/team/repo.git?password=secret#scope",
			want:  "remote: ftp://example.org/team/repo.git",
		},
		{
			name:  "ftps",
			input: "remote: ftps://example.org/team/repo.git?password=secret#scope",
			want:  "remote: ftps://example.org/team/repo.git",
		},
		{
			name:  "git",
			input: "remote: git://git.example.net/team/repo.git?token=secret#scope",
			want:  "remote: git://git.example.net/team/repo.git",
		},
		{
			name:  "ssh",
			input: "remote: ssh://git.example.net/team/repo.git?identity=secret#scope",
			want:  "remote: ssh://git.example.net/team/repo.git",
		},
		{
			name:  "bracketed IPv6 scheme URL",
			input: "remote: https://ipv6-token@[2001:db8::1]/team/repo.git?identity=secret#scope",
			want:  "remote: https://[redacted]@[2001:db8::1]/team/repo.git",
		},
		{
			name:  "file",
			input: "remote: file:///tmp/repo.git?credential=secret#scope",
			want:  "remote: file:///tmp/repo.git",
		},
		{
			name:  "scheme URL with numeric port",
			input: "remote: https://example.com:443/team/repo.git",
			want:  "remote: https://example.com:443/team/repo.git",
		},
		{
			name:    "truncated scheme authority with credential pair",
			input:   "remote: https://x-access-token:ghp_SECRET",
			want:    "remote: [redacted-remote]",
			secrets: []string{"x-access-token", "ghp_SECRET"},
		},
		{
			name:  "scp-like ssh",
			input: "remote: git@git.example.io:team/repo.git?identity=secret#scope",
			want:  "remote: [redacted]@git.example.io:team/repo.git",
		},
		{
			name:  "scp-like ssh alias",
			input: "remote: deploy-token@git_alias_1:team/repo.git?identity=secret#scope",
			want:  "remote: [redacted]@git_alias_1:team/repo.git",
		},
		{
			name:  "scp-like underscore userinfo",
			input: "remote: _deploy-token@git.example.com:team/repo.git?identity=secret#scope",
			want:  "remote: [redacted]@git.example.com:team/repo.git",
		},
		{
			name:  "scp-like punctuation userinfo",
			input: "remote: +deploy-token@git.example.com:team/repo.git?identity=secret#scope",
			want:  "remote: [redacted]@git.example.com:team/repo.git",
		},
		{
			name:  "scp-like bracketed path",
			input: "remote: _deploy-token@git.example.com:team/[private].git?secret=x#scope",
			want:  "remote: [redacted]@git.example.com:team/[private].git",
		},
		{
			name:  "bracketed IPv6 scp-like URL",
			input: "remote: ipv6-token@[2001:db8::1]:team/repo.git?identity=secret#scope",
			want:  "remote: [redacted]@[2001:db8::1]:team/repo.git",
		},
		{
			name:  "scp-like without userinfo",
			input: "remote: git.example.io:team/repo.git?token=secret#scope",
			want:  "remote: git.example.io:team/repo.git",
		},
		{
			name:  "scp-like fragment without userinfo",
			input: "remote: git_alias_1:team/repo.git#secret",
			want:  "remote: git_alias_1:team/repo.git",
		},
		{
			name:  "single-component scp path",
			input: "status ops@example.com:ready",
			want:  "status [redacted]@example.com:ready",
		},
		{
			name:  "malformed remote with userinfo and host only",
			input: "remote: github-token@ghe.example?token=secret#scope",
			want:  "remote: [redacted]@ghe.example",
		},
		{
			name:  "scheme-less remote with userinfo and slash path",
			input: "remote: token@host.example/team/repo.git?secret=x#scope",
			want:  "remote: [redacted]@host.example/team/repo.git",
		},
		{
			name:    "scheme-less remote with colon in userinfo",
			input:   "remote: x-access-token:ghp_SECRET@github.com/o/r.git?access_token=query-secret#fragment-secret",
			want:    "remote: [redacted]@github.com/o/r.git",
			secrets: []string{"x-access-token", "ghp_SECRET", "access_token", "query-secret", "fragment-secret"},
		},
		{
			name:    "truncated scheme-less remote after userinfo",
			input:   "remote: x-access-token:SECRET@",
			want:    "remote: [redacted-remote]",
			secrets: []string{"x-access-token", "SECRET"},
		},
		{
			name:    "truncated scheme-less remote after host separator",
			input:   "remote: x-access-token:SECRET@github.com:",
			want:    "remote: [redacted-remote]",
			secrets: []string{"x-access-token", "SECRET"},
		},
		{
			name:    "scheme-less bracketed IPv6 remote",
			input:   "remote: x-access-token:SECRET@[2001:db8::1]",
			want:    "remote: [redacted]@[2001:db8::1]",
			secrets: []string{"x-access-token", "SECRET"},
		},
		{
			name:    "malformed scheme-less bracketed IPv6 remote",
			input:   "remote: x-access-token:SECRET@[2001:db8::1",
			want:    "remote: [redacted-remote]",
			secrets: []string{"x-access-token", "SECRET"},
		},
		{
			name:    "SCP remote with colon in userinfo",
			input:   "remote: x-access-token:ghp_SECRET@git.example.com:o/r.git?access_token=query-secret#fragment-secret",
			want:    "remote: [redacted]@git.example.com:o/r.git",
			secrets: []string{"x-access-token", "ghp_SECRET", "access_token", "query-secret", "fragment-secret"},
		},
		{
			name:  "scp path contains at sign",
			input: "remote: token@host:repo@mirror",
			want:  "remote: [redacted-remote]",
		},
		{
			name:  "remote helper wrapping scheme URL",
			input: "remote: git::https://token@github.com/o/r.git?secret=x#fragment",
			want:  "remote: git::https://[redacted]@github.com/o/r.git",
		},
		{
			name:  "remote helper wrapping bracketed IPv6 scheme URL",
			input: "remote: cache::https://ipv6-token@[2001:db8::1]/team/repo.git?secret=x#fragment",
			want:  "remote: cache::https://[redacted]@[2001:db8::1]/team/repo.git",
		},
		{
			name:  "remote helper wrapping scp URL",
			input: "remote: cache::deploy-token@git.example.com:team/repo.git?secret=x#fragment",
			want:  "remote: cache::[redacted]@git.example.com:team/repo.git",
		},
		{
			name:  "remote helper wrapping bracketed IPv6 scp URL",
			input: "remote: cache::ipv6-token@[2001:db8::1]:team/repo.git?secret=x#fragment",
			want:  "remote: cache::[redacted]@[2001:db8::1]:team/repo.git",
		},
		{
			name:  "remote helper wrapping underscore scp userinfo",
			input: "remote: cache::_deploy-token@git.example.com:team/repo.git?secret=x#fragment",
			want:  "remote: cache::[redacted]@git.example.com:team/repo.git",
		},
		{
			name:  "nested remote helpers",
			input: "remote: trace::cache::https://token@git.example.com/team/repo.git?secret=x#fragment",
			want:  "remote: trace::cache::https://[redacted]@git.example.com/team/repo.git",
		},
		{
			name:  "nested remote helpers wrapping scheme-less URL",
			input: "remote: trace::cache::token@host.example/team/repo.git?secret=x#fragment",
			want:  "remote: trace::cache::[redacted]@host.example/team/repo.git",
		},
		{
			name:    "nested remote helpers wrapping colon userinfo",
			input:   "remote: trace::cache::x-access-token:ghp_SECRET@github.com/o/r.git?access_token=query-secret#fragment-secret",
			want:    "remote: trace::cache::[redacted]@github.com/o/r.git",
			secrets: []string{"x-access-token", "ghp_SECRET", "access_token", "query-secret", "fragment-secret"},
		},
		{
			name:    "nested remote helpers wrapping truncated userinfo",
			input:   "remote: trace::cache::x-access-token:SECRET@",
			want:    "remote: [redacted-remote]",
			secrets: []string{"x-access-token", "SECRET"},
		},
		{
			name:    "nested helpers wrapping truncated scheme authority",
			input:   "remote: trace::cache::https://x-access-token:ghp_SECRET",
			want:    "remote: [redacted-remote]",
			secrets: []string{"x-access-token", "ghp_SECRET"},
		},
		{
			name:    "nested helpers wrapping truncated authority",
			input:   "remote: trace::cache::x-access-token:ghp_SECRET",
			want:    "remote: [redacted-remote]",
			secrets: []string{"x-access-token", "ghp_SECRET"},
		},
		{
			name:  "nested remote helper bracketed SCP path",
			input: "remote: trace::cache::_deploy-token@git.example.com:team/[private].git?secret=x#fragment",
			want:  "remote: trace::cache::[redacted]@git.example.com:team/[private].git",
		},
		{
			name:    "colored scheme URL",
			input:   "remote: ht\x1b[31mtps://example.com/repo.git?token=query-secret\x1b[0m",
			want:    "remote: [redacted-remote]",
			secrets: []string{"token=", "query-secret"},
		},
		{
			name:    "colored SCP URL",
			input:   "remote: deploy-secret\x1b[31m@github.com:o/r.git?token=query-secret\x1b[0m",
			want:    "remote: [redacted-remote]",
			secrets: []string{"deploy-secret", "token=", "query-secret"},
		},
		{
			name:    "colored remote helper URL",
			input:   "remote: cache::ht\x1b[31mtps://example.com/repo.git?token=query-secret\x1b[0m",
			want:    "remote: [redacted-remote]",
			secrets: []string{"token=", "query-secret"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := SanitizeDiagnostic(test.input)
			if got != test.want {
				t.Fatalf("SanitizeDiagnostic(%q) = %q, want %q", test.input, got, test.want)
			}
			for _, secret := range test.secrets {
				if strings.Contains(got, secret) {
					t.Fatalf("SanitizeDiagnostic(%q) leaked %q in %q", test.input, secret, got)
				}
			}
		})
	}
}

func TestSanitizeDiagnosticFailsClosedForAmbiguousRemoteTokens(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		secret string
	}{
		{
			name:   "trailing colon",
			input:  "fatal: repository 'x-access-token:trailing-secret@github.com:' is invalid",
			secret: "trailing-secret",
		},
		{
			name:   "numeric secret",
			input:  "fatal: repository 'https://x-access-token:123456' is invalid",
			secret: "123456",
		},
		{
			name:   "numeric helper transport",
			input:  "fatal: repository '123::https://numeric-helper-secret@github.com/o/r.git' is invalid",
			secret: "numeric-helper-secret",
		},
		{
			name:   "scheme delimiter split by CSI",
			input:  "fatal: repository 'https:\x1b[31m//split-secret@github.com/o/r.git' is invalid",
			secret: "split-secret",
		},
		{
			name:   "userinfo delimiter split by raw C1 CSI",
			input:  "fatal: repository 'split-secret\x9b31m@github.com:o/r.git' is invalid",
			secret: "split-secret",
		},
		{
			name:   "helper delimiter split by raw C1 OSC",
			input:  "fatal: repository 'cache:\x9dtitle\x9c:https://osc-secret@github.com/o/r.git' is invalid",
			secret: "osc-secret",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := SanitizeDiagnostic(test.input)
			if strings.Contains(got, test.secret) {
				t.Fatalf("SanitizeDiagnostic(%q) leaked %q in %q", test.input, test.secret, got)
			}
			if !strings.Contains(got, "fatal: repository") ||
				!strings.Contains(got, "is invalid") {
				t.Fatalf("SanitizeDiagnostic(%q) lost surrounding diagnostic text: %q", test.input, got)
			}
		})
	}
}

func TestSanitizeDiagnosticRemovesTerminalControlsWithoutEatingText(t *testing.T) {
	input := "fa\x1btal: re\x9b31mmo\x9b0mte \x9dtitle\x9cdenied"
	got := SanitizeDiagnostic(input)
	if got != "fatal: remote denied" {
		t.Fatalf("SanitizeDiagnostic(%q) = %q, want adjacent text preserved", input, got)
	}
}

func TestSanitizeDiagnosticWholeTokenRedactsControlTaintedRemotes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "scheme URL",
			input: "fatal: https://user:se\x9b31mcret@example.com/repo.git denied",
			want:  "fatal: [redacted-remote] denied",
		},
		{
			name:  "SCP URL",
			input: "fatal: deploy\x9b31m-token@git.example:repo denied",
			want:  "fatal: [redacted-remote] denied",
		},
		{
			name:  "remote helper",
			input: "fatal: cache::https://user:se\x9b31mcret@example.com/repo.git denied",
			want:  "fatal: [redacted-remote] denied",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SanitizeDiagnostic(test.input); got != test.want {
				t.Fatalf("SanitizeDiagnostic(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestSanitizeDiagnosticRemoteSecretCorpusNeverSurvives(t *testing.T) {
	const secret = "UNIQUE-REMOTE-SECRET-741852963"
	inputs := []string{
		"https://user:" + secret,
		"https://user:" + secret + "@github.com/o/r.git",
		"https://x-access-token:" + secret,
		"x-access-token:" + secret + "@github.com:",
		"123::https://" + secret + "@github.com/o/r.git",
		"cache::x-access-token:" + secret,
		"https:\x1b[31m//" + secret + "@github.com/o/r.git",
		secret + "\x9b31m@github.com:o/r.git",
		"cache:\x9dignored\x9c:https://" + secret + "@github.com/o/r.git",
	}
	for _, remote := range inputs {
		got := SanitizeDiagnostic("fatal: remote " + remote + " denied")
		if strings.Contains(got, secret) {
			t.Fatalf("SanitizeDiagnostic leaked corpus secret from %q in %q", remote, got)
		}
	}
}

func TestSanitizeDiagnosticBoundsRemoteHelperNesting(t *testing.T) {
	const helperDepth = 10000
	input := "remote: " + strings.Repeat("cache::", helperDepth) +
		"_deep-secret@git.example.com:team/repo.git?token=also-secret#fragment"

	got := SanitizeDiagnostic(input)

	if strings.Contains(got, "deep-secret") || strings.Contains(got, "also-secret") {
		t.Fatalf("SanitizeDiagnostic leaked a secret from deeply nested helper input: %q", got)
	}
	if got != "remote: "+redactedRemoteToken {
		t.Fatalf("SanitizeDiagnostic deep nesting result = %q, want one safe marker", got)
	}
}

func TestSanitizeDiagnosticHandlesLargePunctuationAndContinues(t *testing.T) {
	punctuation := strings.Repeat("!", 256*1024)
	input := punctuation +
		" https://token@git.example.com/team/repo.git?secret=x#fragment continuation"
	want := punctuation + " https://[redacted]@git.example.com/team/repo.git continuation"

	if got := SanitizeDiagnostic(input); got != want {
		t.Fatalf("SanitizeDiagnostic large input did not preserve and sanitize the full diagnostic")
	}
}

func TestSanitizeDiagnosticHandlesLargeTruncatedUserinfo(t *testing.T) {
	userinfo := strings.Repeat("token:", maxGitDiagnosticOutput/6) + "SECRET"
	input := "fatal: " + userinfo + "@ continuation"

	got := SanitizeDiagnostic(input)

	if got != "fatal: [redacted-remote] continuation" {
		t.Fatalf("SanitizeDiagnostic large truncated userinfo did not preserve surrounding prose")
	}
	if strings.Contains(got, "token") || strings.Contains(got, "SECRET") {
		t.Fatalf("SanitizeDiagnostic large truncated userinfo leaked a credential: %q", got)
	}
}

func TestDiagnosticBufferKeepsActionableTailAndMarksTruncation(t *testing.T) {
	var output diagnosticBuffer
	prefix := bytes.Repeat([]byte("x"), maxGitDiagnosticOutput)
	tail := []byte("fatal: final actionable diagnostic\n")
	if _, err := output.Write(prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write(tail); err != nil {
		t.Fatal(err)
	}

	got := string(output.Bytes())
	if !strings.Contains(got, "git diagnostic truncated") {
		t.Fatalf("bounded output %q is missing the truncation marker", got)
	}
	if !strings.Contains(got, "final actionable diagnostic") {
		t.Fatalf("bounded output does not preserve the actionable tail: %q", got)
	}
	if len(output.buffer.Bytes()) != maxGitDiagnosticOutput {
		t.Fatalf("captured bytes = %d, want %d", len(output.buffer.Bytes()), maxGitDiagnosticOutput)
	}
}

func TestDiagnosticBufferRedactsTokenSplitByTruncationBoundary(t *testing.T) {
	const leakedFragment = "cret-token"
	partialURL := leakedFragment + "@[2001:db8::1]/team/repo.git"
	retained := partialURL + "\n" +
		strings.Repeat("x", maxGitDiagnosticOutput-len(partialURL)-1)
	raw := "fatal: https://super-se" + retained

	var output diagnosticBuffer
	if _, err := output.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	got := SanitizeDiagnostic(string(output.Bytes()))

	if strings.Contains(got, leakedFragment) {
		t.Fatalf("bounded diagnostic leaked split credential fragment: %q", got[:256])
	}
	if !strings.Contains(got, "git diagnostic truncated") ||
		!strings.Contains(got, "truncated token redacted") {
		t.Fatalf("bounded diagnostic %q is missing truncation redaction markers", got[:256])
	}
	if !strings.HasSuffix(got, strings.Repeat("x", 128)) {
		t.Fatal("bounded diagnostic did not preserve its actionable tail")
	}
}

func TestDiagnosticBufferDoesNotRestartAtApostropheInsideTruncatedToken(t *testing.T) {
	const querySecret = "query-secret"
	partialURL := "ivate/repo's-private.git?token=" + querySecret
	retained := partialURL + "\n" +
		strings.Repeat("x", maxGitDiagnosticOutput-len(partialURL)-1)
	raw := "fatal: https://host.example/pr" + retained

	var output diagnosticBuffer
	if _, err := output.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	got := SanitizeDiagnostic(string(output.Bytes()))

	if strings.Contains(got, querySecret) || strings.Contains(got, "repo's-private.git") {
		t.Fatalf("bounded diagnostic leaked a split URL fragment: %q", got[:256])
	}
	if !strings.Contains(got, "truncated token redacted") ||
		!strings.HasSuffix(got, strings.Repeat("x", 128)) {
		t.Fatalf("bounded diagnostic did not preserve truncation markers and actionable tail")
	}
}

func TestDiagnosticBufferRedactsANSIPrefixedTruncatedToken(t *testing.T) {
	const secret = "truncated-ansi-secret"
	partialURL := "\x1b[31m" + secret + "@github.com/o/r.git"
	retained := partialURL + "\n" +
		strings.Repeat("x", maxGitDiagnosticOutput-len(partialURL)-1)
	raw := "fatal: https://user:" + retained

	var output diagnosticBuffer
	if _, err := output.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	got := SanitizeDiagnostic(string(output.Bytes()))
	if strings.Contains(got, secret) {
		t.Fatalf("bounded diagnostic leaked ANSI-prefixed split credential: %q", got[:256])
	}
	if !strings.Contains(got, "leading truncated token redacted") {
		t.Fatalf("bounded diagnostic %q is missing the boundary redaction marker", got[:256])
	}
}

func TestWorkMerged(t *testing.T) {
	repo := initRepo(t)
	start := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", "-b", "merged-work")
	if err := os.WriteFile(filepath.Join(repo, "merged.txt"), []byte("merged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "merged.txt")
	runGit(t, repo, "commit", "-q", "-m", "merged work")
	runGit(t, repo, "checkout", "-q", "-")
	runGit(t, repo, "merge", "-q", "--ff-only", "merged-work")

	merged, err := WorkMerged(repo, "merged-work", "HEAD", start)
	if err != nil {
		t.Fatalf("WorkMerged: %v", err)
	}
	if !merged {
		t.Fatal("WorkMerged = false, want true")
	}
}

func TestWorkMergedIntoExactSquash(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	start := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", "-b", "squashed-work")
	if err := os.WriteFile(filepath.Join(repo, "squashed.txt"), []byte("one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "squashed.txt")
	runGit(t, repo, "commit", "-q", "-m", "first")
	if err := os.WriteFile(filepath.Join(repo, "squashed.txt"), []byte("one\ntwo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "squashed.txt")
	runGit(t, repo, "commit", "-q", "-m", "second")
	tip := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", base)
	runGit(t, repo, "merge", "-q", "--squash", "squashed-work")
	runGit(t, repo, "commit", "-q", "-m", "squash work")

	gotTip, merged, err := WorkMergedInto(repo, "squashed-work", "refs/heads/"+base, start)
	if err != nil {
		t.Fatalf("WorkMergedInto: %v", err)
	}
	if gotTip != tip {
		t.Fatalf("WorkMergedInto tip = %s, want %s", gotTip, tip)
	}
	if !merged {
		t.Fatal("WorkMergedInto = false, want true")
	}
}

func TestWorkMergedIntoExactRebase(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	start := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", "-b", "rebased-work")
	commitFile(t, repo, "first.txt", "first\n", "first")
	first := gitOutput(t, repo, "rev-parse", "HEAD")
	commitFile(t, repo, "second.txt", "second\n", "second")
	tip := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", base)
	runGit(t, repo, "cherry-pick", first)
	runGit(t, repo, "cherry-pick", tip)

	gotTip, merged, err := WorkMergedInto(repo, "rebased-work", "refs/heads/"+base, start)
	if err != nil {
		t.Fatalf("WorkMergedInto: %v", err)
	}
	if gotTip != tip {
		t.Fatalf("WorkMergedInto tip = %s, want %s", gotTip, tip)
	}
	if !merged {
		t.Fatal("WorkMergedInto = false, want true")
	}
}

func TestWorkMergedIntoOrdinaryAncestry(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	start := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", "-b", "ordinary-work")
	commitFile(t, repo, "ordinary.txt", "ordinary\n", "ordinary")
	tip := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", base)
	runGit(t, repo, "merge", "-q", "--ff-only", "ordinary-work")

	gotTip, merged, err := WorkMergedInto(repo, "ordinary-work", "refs/heads/"+base, start)
	if err != nil {
		t.Fatalf("WorkMergedInto: %v", err)
	}
	if gotTip != tip || !merged {
		t.Fatalf("WorkMergedInto = (%s, %t), want (%s, true)", gotTip, merged, tip)
	}
}

func TestWorkMergedIntoHistoricalSquashAfterLaterEdit(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	start := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", "-b", "historical-work")
	commitFile(t, repo, "historical.txt", "project\n", "project")
	runGit(t, repo, "checkout", "-q", base)
	runGit(t, repo, "merge", "-q", "--squash", "historical-work")
	runGit(t, repo, "commit", "-q", "-m", "squash project")
	commitFile(t, repo, "historical.txt", "project\nlater\n", "later edit")

	_, merged, err := WorkMergedInto(repo, "historical-work", "refs/heads/"+base, start)
	if err != nil {
		t.Fatalf("WorkMergedInto: %v", err)
	}
	if !merged {
		t.Fatal("WorkMergedInto = false after later upstream edit, want true")
	}
}

func TestWorkMergedIntoRejectsIncompleteOrDifferentWork(t *testing.T) {
	for _, test := range []struct {
		name       string
		project    []string
		upstream   string
		upstreamAt int
	}{
		{
			name:       "partial",
			project:    []string{"one\n", "one\ntwo\n"},
			upstream:   "one\n",
			upstreamAt: 1,
		},
		{
			name:       "different",
			project:    []string{"project\n"},
			upstream:   "different\n",
			upstreamAt: 1,
		},
		{
			name:       "whitespace different",
			project:    []string{"project\n"},
			upstream:   "project \n",
			upstreamAt: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := initRepo(t)
			base := currentBranchInRepo(t, repo)
			start := gitOutput(t, repo, "rev-parse", "HEAD")
			runGit(t, repo, "checkout", "-q", "-b", "unverified-work")
			for i, content := range test.project {
				commitFile(t, repo, "work.txt", content, fmt.Sprintf("project %d", i))
			}
			runGit(t, repo, "checkout", "-q", base)
			commitFile(t, repo, "work.txt", test.upstream, fmt.Sprintf("upstream %d", test.upstreamAt))

			_, merged, err := WorkMergedInto(
				repo, "unverified-work", "refs/heads/"+base, start,
			)
			if err != nil {
				t.Fatalf("WorkMergedInto: %v", err)
			}
			if merged {
				t.Fatal("WorkMergedInto = true, want false")
			}
		})
	}
}

func TestWorkMergedIntoRejectsNonlinearWork(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	start := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", "-b", "side-work")
	commitFile(t, repo, "side.txt", "side\n", "side")
	runGit(t, repo, "checkout", "-q", "-b", "nonlinear-work", start)
	commitFile(t, repo, "main.txt", "main\n", "main")
	runGit(t, repo, "merge", "-q", "--no-ff", "side-work", "-m", "merge side")
	runGit(t, repo, "checkout", "-q", base)
	runGit(t, repo, "merge", "-q", "--squash", "nonlinear-work")
	runGit(t, repo, "commit", "-q", "-m", "squash nonlinear")

	_, merged, err := WorkMergedInto(repo, "nonlinear-work", "refs/heads/"+base, start)
	if err != nil {
		t.Fatalf("WorkMergedInto: %v", err)
	}
	if merged {
		t.Fatal("WorkMergedInto = true for nonlinear work")
	}
}

func TestWorkMergedIntoRejectsInvalidOrUnboundedStart(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	start := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", "-b", "work")
	commitFile(t, repo, "work.txt", "work\n", "work")
	runGit(t, repo, "checkout", "-q", base)

	for _, test := range []struct {
		name      string
		startSHA  string
		wantError bool
	}{
		{name: "missing", startSHA: ""},
		{name: "invalid", startSHA: "not-a-commit", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, merged, err := WorkMergedInto(repo, "work", "refs/heads/"+base, test.startSHA)
			if test.wantError && !errors.Is(err, ErrInvalidWorkStart) {
				t.Fatalf("WorkMergedInto error = %v, want %v", err, ErrInvalidWorkStart)
			}
			if !test.wantError && err != nil {
				t.Fatalf("WorkMergedInto: %v", err)
			}
			if merged {
				t.Fatal("WorkMergedInto = true for invalid or missing start")
			}
		})
	}

	runGit(t, repo, "checkout", "-q", "--orphan", "unrelated-base")
	runGit(t, repo, "rm", "-q", "-rf", ".")
	commitFile(t, repo, "unrelated.txt", "unrelated\n", "unrelated")
	runGit(t, repo, "merge", "-q", "--allow-unrelated-histories", "--no-ff", base, "-m", "merge old base")
	runGit(t, repo, "merge", "-q", "--squash", "work")
	runGit(t, repo, "commit", "-q", "-m", "squash work")

	_, merged, err := WorkMergedInto(repo, "work", "refs/heads/unrelated-base", start)
	if err != nil {
		t.Fatalf("WorkMergedInto unbounded start: %v", err)
	}
	if merged {
		t.Fatal("WorkMergedInto = true when start is not on base first-parent history")
	}
}

func TestWorkMergedUsesQualifiedBranchRefWhenTagConflicts(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	start := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", "-b", "qualified-work")
	if err := os.WriteFile(filepath.Join(repo, "qualified.txt"), []byte("merged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "qualified.txt")
	runGit(t, repo, "commit", "-q", "-m", "qualified work")
	runGit(t, repo, "checkout", "-q", base)
	runGit(t, repo, "merge", "-q", "--ff-only", "refs/heads/qualified-work")
	runGit(t, repo, "tag", "qualified-work", start)

	merged, err := WorkMerged(repo, "qualified-work", "refs/heads/"+base, start)
	if err != nil {
		t.Fatalf("WorkMerged: %v", err)
	}
	if !merged {
		t.Fatal("WorkMerged followed the conflicting tag instead of refs/heads/qualified-work")
	}
}

func TestWorkMergedIgnoresSuccessfulGitStderrWhenComparingStartSHA(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	start := gitOutput(t, repo, "rev-parse", base)
	runGit(t, repo, "branch", "unchanged-work", start)
	t.Setenv("GIT_TRACE", "1")

	merged, err := WorkMerged(repo, "unchanged-work", base, start)
	if err != nil {
		t.Fatalf("WorkMerged: %v", err)
	}
	if merged {
		t.Fatal("WorkMerged = true for a branch with no commits beyond start SHA")
	}
}

func TestWorkMergedReturnsFalseForNormalUnmergedStates(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	start := gitOutput(t, repo, "rev-parse", base)
	runGit(t, repo, "checkout", "-q", "-b", "unmerged-work")
	if err := os.WriteFile(filepath.Join(repo, "unmerged.txt"), []byte("unmerged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "unmerged.txt")
	runGit(t, repo, "commit", "-q", "-m", "unmerged work")
	runGit(t, repo, "checkout", "-q", base)

	for _, branch := range []string{"unmerged-work", "missing-work"} {
		t.Run(branch, func(t *testing.T) {
			merged, err := WorkMerged(repo, branch, base, start)
			if err != nil {
				t.Fatalf("WorkMerged: %v", err)
			}
			if merged {
				t.Fatal("WorkMerged = true, want false")
			}
		})
	}
}

func TestWorkMergedReturnsEvaluationError(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	start := gitOutput(t, repo, "rev-parse", base)
	runGit(t, repo, "checkout", "-q", "-b", "work")
	if err := os.WriteFile(filepath.Join(repo, "work.txt"), []byte("work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "work.txt")
	runGit(t, repo, "commit", "-q", "-m", "work")
	runGit(t, repo, "checkout", "-q", base)

	merged, err := WorkMerged(repo, "work", "missing-base", start)
	if err == nil {
		t.Fatal("WorkMerged error = nil, want invalid base diagnostic")
	}
	if merged {
		t.Fatal("WorkMerged = true after evaluation error")
	}
	if !strings.Contains(err.Error(), "git merge-base --is-ancestor") ||
		!strings.Contains(err.Error(), "missing-base") {
		t.Fatalf("WorkMerged error = %q", err)
	}
	if IsWorkMerged(repo, "work", "missing-base", start) {
		t.Fatal("IsWorkMerged should remain conservative on evaluation error")
	}
}

func TestWorkMergedRejectsInvalidStartCommit(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	runGit(t, repo, "checkout", "-q", "-b", "work")
	if err := os.WriteFile(filepath.Join(repo, "work.txt"), []byte("work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "work.txt")
	runGit(t, repo, "commit", "-q", "-m", "work")
	runGit(t, repo, "checkout", "-q", base)

	blobPath := filepath.Join(repo, "blob")
	if err := os.WriteFile(blobPath, []byte("not a commit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	blobSHA := gitOutput(t, repo, "hash-object", "-w", blobPath)

	runGit(t, repo, "checkout", "-q", "-b", "unrelated")
	if err := os.WriteFile(filepath.Join(repo, "unrelated.txt"), []byte("unrelated\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "unrelated.txt")
	runGit(t, repo, "commit", "-q", "-m", "unrelated")
	unrelatedSHA := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", base)

	for _, test := range []struct {
		name     string
		startSHA string
		want     string
	}{
		{name: "invalid", startSHA: "not-a-commit", want: "start_sha"},
		{name: "non-commit", startSHA: blobSHA, want: "commit"},
		{name: "not branch ancestor", startSHA: unrelatedSHA, want: "ancestor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			merged, err := WorkMerged(repo, "work", base, test.startSHA)
			if err == nil || !errors.Is(err, ErrInvalidWorkStart) {
				t.Fatalf("WorkMerged error = %v, want %v", err, ErrInvalidWorkStart)
			}
			if merged {
				t.Fatal("WorkMerged = true for invalid start commit")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("WorkMerged error %q is missing %q", err, test.want)
			}
		})
	}
}

func TestDetectDefaultBranchFallsBackWhenOriginHEADIsMalformed(t *testing.T) {
	for _, base := range []string{"main", "master"} {
		t.Run(base, func(t *testing.T) {
			repo := initRepo(t)
			runGit(t, repo, "branch", "-M", base)
			runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/heads/"+base)

			branch, err := DetectDefaultBranchWithError(repo)
			if err != nil {
				t.Fatalf("DetectDefaultBranchWithError: %v", err)
			}
			if branch != base {
				t.Fatalf("default branch = %q, want %q", branch, base)
			}
		})
	}
}

func currentBranchInRepo(t *testing.T, repo string) string {
	t.Helper()
	return gitOutput(t, repo, "branch", "--show-current")
}

func initRemoteRepo(t *testing.T, base string) (remote, source, repo string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "origin.git")
	source = filepath.Join(root, "source")
	repo = filepath.Join(root, "repo")
	runGit(t, root, "init", "-q", "--bare", "--initial-branch="+base, remote)
	runGit(t, root, "init", "-q", "-b", base, source)
	if err := os.WriteFile(filepath.Join(source, "README"), []byte("initial\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README")
	runGit(t, source, "commit", "-q", "-m", "initial")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-q", "-u", "origin", base)
	runGit(t, root, "clone", "-q", remote, repo)
	return remote, source, repo
}

func advanceRepo(t *testing.T, repo, name, content, message string) {
	t.Helper()
	commitFile(t, repo, name, content, message)
	runGit(t, repo, "push", "-q", "origin", "HEAD")
}

func commitFile(t *testing.T, repo, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", name)
	runGit(t, repo, "commit", "-q", "-m", message)
}

func runGit(t *testing.T, dir string, args ...string) {
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

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func awaitFetchResult(t *testing.T, result <-chan fetchSnapshotResult) fetchSnapshotResult {
	t.Helper()
	select {
	case fetched := <-result:
		return fetched
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for concurrent fetch")
		return fetchSnapshotResult{}
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
