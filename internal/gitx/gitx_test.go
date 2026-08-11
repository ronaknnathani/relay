package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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
// manifest points at a path that git does not consider a working tree. Removal
// must succeed rather than failing with "is not a working tree".
func TestWorktreeRemoveMissingWorktree(t *testing.T) {
	repo := initRepo(t)
	dir := filepath.Join(repo, ".worktrees", "never-registered")

	// Case 1: the directory does not exist at all.
	if err := WorktreeRemove(repo, dir, false); err != nil {
		t.Fatalf("WorktreeRemove (absent dir): %v", err)
	}

	// Case 2: a leftover directory exists but was never registered as a worktree.
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir leftover: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale"), []byte("x"), 0644); err != nil {
		t.Fatalf("write leftover: %v", err)
	}
	if err := WorktreeRemove(repo, dir, false); err != nil {
		t.Fatalf("WorktreeRemove (leftover dir): %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("leftover dir still present: %v", err)
	}
}
