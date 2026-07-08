package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ronaknnathani/relay/internal/gitx"
)

// newTestRepo creates a throwaway git repo with one commit on main and returns
// its root.
func newTestRepo(t *testing.T) string {
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
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README")
	run("commit", "-q", "-m", "init")
	return repo
}

func TestReclaimLeftoversRemovesBranchWorktreeAndDir(t *testing.T) {
	repo := newTestRepo(t)
	worktreeDir := filepath.Join(repo, ".worktrees", "wt")
	branch := "user/demo"
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", worktreeDir, "-b", branch, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	projDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projDir, "task.md"), []byte("x"), 0644); err != nil {
		t.Fatalf("seed projDir: %v", err)
	}

	if err := reclaimLeftovers(repo, branch, worktreeDir, projDir); err != nil {
		t.Fatalf("reclaimLeftovers: %v", err)
	}
	if gitx.BranchExists(repo, branch) {
		t.Errorf("branch %q still exists after reclaim", branch)
	}
	if pathExists(worktreeDir) {
		t.Errorf("worktree dir still present after reclaim")
	}
	if pathExists(projDir) {
		t.Errorf("project dir still present after reclaim")
	}
}

func TestReclaimLeftoversHandlesUnregisteredWorktree(t *testing.T) {
	repo := newTestRepo(t)
	// A leftover directory that was never registered as a worktree, plus a
	// branch — mimics an interrupted setup.
	worktreeDir := filepath.Join(repo, ".worktrees", "orphan")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}
	branch := "user/orphan"
	if out, err := exec.Command("git", "-C", repo, "branch", branch).CombinedOutput(); err != nil {
		t.Fatalf("create branch: %v\n%s", err, out)
	}

	if err := reclaimLeftovers(repo, branch, worktreeDir, ""); err != nil {
		t.Fatalf("reclaimLeftovers: %v", err)
	}
	if gitx.BranchExists(repo, branch) {
		t.Errorf("branch %q still exists after reclaim", branch)
	}
	if pathExists(worktreeDir) {
		t.Errorf("orphan dir still present after reclaim")
	}
}

func TestBranchMerged(t *testing.T) {
	repo := newTestRepo(t)
	// A branch at HEAD is reachable from main (merged / no unique work).
	if out, err := exec.Command("git", "-C", repo, "branch", "merged", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("branch merged: %v\n%s", err, out)
	}
	if !branchMerged(repo, "merged", "main") {
		t.Error("branchMerged(merged) = false, want true")
	}

	// A branch with an extra commit is NOT reachable from main.
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", filepath.Join(repo, ".worktrees", "u"), "-b", "unmerged", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("worktree add unmerged: %v\n%s", err, out)
	}
	wt := filepath.Join(repo, ".worktrees", "u")
	if err := os.WriteFile(filepath.Join(wt, "new.txt"), []byte("y"), 0644); err != nil {
		t.Fatalf("write new file: %v", err)
	}
	commit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", wt}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
			"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit("add", "new.txt")
	commit("commit", "-q", "-m", "extra")
	if branchMerged(repo, "unmerged", "main") {
		t.Error("branchMerged(unmerged) = true, want false")
	}
}
