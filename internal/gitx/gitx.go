// Package gitx wraps git CLI invocations used by the relay CLI. Every
// function returns an error describing the operation and includes git's
// stderr when available.
package gitx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoRoot returns the absolute path to the top-level directory of the
// current git repository, or an empty string and an error if cwd is not
// in a git repo.
func RepoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CurrentBranch returns the abbreviated current branch name, or "unknown"
// if git fails.
func CurrentBranch() string {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// RevParse resolves ref to a commit SHA. Returns "" if ref cannot be resolved.
func RevParse(repo, ref string) string {
	out, err := exec.Command("git", "-C", repo, "rev-parse", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// HasOrigin reports whether the repo has an "origin" remote configured.
func HasOrigin(repo string) bool {
	return exec.Command("git", "-C", repo, "remote", "get-url", "origin").Run() == nil
}

// BranchExists reports whether the named branch exists locally.
func BranchExists(repo, branch string) bool {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", branch).CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// DeleteBranch removes a branch with `git branch -d` (refuses if unmerged).
func DeleteBranch(repo, branch string) error {
	out, err := exec.Command("git", "-C", repo, "branch", "-d", branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch -d %s: %w\n%s", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ForceDeleteBranch removes a branch with `git branch -D` (deletes unmerged).
func ForceDeleteBranch(repo, branch string) error {
	out, err := exec.Command("git", "-C", repo, "branch", "-D", branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch -D %s: %w\n%s", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// IsBranchReachable reports whether branch's tip is an ancestor of base's tip.
// "Safe to delete" semantics — true means deleting branch loses no work.
func IsBranchReachable(repo, branch, base string) bool {
	return exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", branch, base).Run() == nil
}

// IsWorkMerged reports whether branch has commits beyond startSHA AND those
// commits are reachable from base. A freshly-created branch (tip == startSHA)
// is not considered merged.
func IsWorkMerged(repo, branch, base, startSHA string) bool {
	if startSHA == "" {
		return false
	}
	tip := RevParse(repo, branch)
	if tip == "" || tip == startSHA {
		return false
	}
	return IsBranchReachable(repo, branch, base)
}

// DetectDefaultBranch returns the repo's default branch. Prefers
// origin/HEAD; falls back to probing "main" then "master". Returns "" if
// none is found.
func DetectDefaultBranch(repo string) string {
	out, err := exec.Command("git", "-C", repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output()
	if err == nil {
		ref := strings.TrimSpace(string(out))
		if i := strings.Index(ref, "/"); i >= 0 && i+1 < len(ref) {
			return ref[i+1:]
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if BranchExists(repo, candidate) {
			return candidate
		}
	}
	return ""
}

// Fetch runs `git fetch origin <branch>`. Returns the trimmed combined output
// alongside any error so callers can decide whether to warn.
func Fetch(repo, branch string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "fetch", "origin", branch).CombinedOutput()
	if err != nil {
		return strings.TrimRight(string(out), "\n"), fmt.Errorf("git fetch origin %s: %w", branch, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// WorktreeAdd creates a new worktree at dir on a new branch, started from startPoint.
func WorktreeAdd(repo, dir, branch, startPoint string) error {
	out, err := exec.Command("git", "worktree", "add", dir, "-b", branch, startPoint).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add %s: %w\n%s", dir, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// IsWorktree reports whether dir is registered as a git worktree of repo.
// The returned bool is only meaningful when err is nil.
func IsWorktree(repo, dir string) (bool, error) {
	out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("git worktree list: %w", err)
	}
	target := canonPath(dir)
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			if canonPath(strings.TrimSpace(p)) == target {
				return true, nil
			}
		}
	}
	return false, nil
}

// canonPath resolves symlinks so paths from git (which reports real paths, e.g.
// /private/var on macOS) compare equal to relay's constructed paths (/var).
// Falls back to Clean when the path does not exist on disk.
func canonPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// WorktreeClean reports whether the registered worktree at dir has no
// uncommitted changes and no untracked files (i.e. `git status --porcelain`
// is empty). The bool is only meaningful when err is nil.
func WorktreeClean(dir string) (bool, error) {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("git -C %s status: %w", dir, err)
	}
	return strings.TrimSpace(string(out)) == "", nil
}

// WorktreeRemove removes the worktree at dir. If force is true, includes --force.
// When dir is not a registered worktree (e.g. setup was interrupted before the
// worktree finished, or it was removed manually), it cleans up any leftover
// directory and prunes stale metadata instead of failing, so callers such as
// `relay archive` can still make progress.
func WorktreeRemove(repo, dir string, force bool) error {
	registered, err := IsWorktree(repo, dir)
	if err != nil {
		return err
	}
	if !registered {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return fmt.Errorf("remove leftover worktree dir %s: %w", dir, rmErr)
		}
		if out, pruneErr := exec.Command("git", "-C", repo, "worktree", "prune").CombinedOutput(); pruneErr != nil {
			return fmt.Errorf("git worktree prune: %w\n%s", pruneErr, strings.TrimSpace(string(out)))
		}
		return nil
	}
	args := []string{"-C", repo, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, dir)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove %s: %w\n%s", dir, err, strings.TrimSpace(string(out)))
	}
	return nil
}
