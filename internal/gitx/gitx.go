// Package gitx wraps git CLI invocations used by the relay CLI. Every
// function returns an error describing the operation and includes git's
// stderr when available.
package gitx

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	gitURLPattern     = regexp.MustCompile(`(?i)(?:file|ftp|ftps|git|https?|ssh)://[^\s'"<>]+`)
	scpLikeURLPattern = regexp.MustCompile(
		`(?i)\b[^\s'"<>/@:]+@(?:\[[0-9a-f:.]+\]|[^\s'"<>/:]+):[^\s'"<>]+`,
	)
)

// RepoRoot returns the absolute path to the top-level directory of the
// current git repository, or an empty string and an error if cwd is not
// in a git repo.
func RepoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", gitOutputError("git rev-parse --show-toplevel", err)
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

// LocalBranchTip resolves a local branch tip. A missing branch is returned as
// found=false; other Git failures are returned to the caller.
func LocalBranchTip(repo, branch string) (sha string, found bool, err error) {
	exists, err := localBranchExists(repo, branch)
	if err != nil || !exists {
		return "", exists, err
	}
	ref := "refs/heads/" + branch
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", ref+"^{commit}").Output()
	if err != nil {
		return "", false, gitOutputError("git rev-parse --verify "+ref+"^{commit}", err)
	}
	return strings.TrimSpace(string(out)), true, nil
}

// WorktreeHead resolves the current commit of a registered worktree directory.
// Missing directories are returned as found=false; existing unregistered
// directories return an error.
func WorktreeHead(repo, dir string) (sha string, found bool, err error) {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("stat worktree %s: %w", dir, err)
	}
	registered, err := IsWorktree(repo, dir)
	if err != nil {
		return "", false, err
	}
	if !registered {
		return "", false, fmt.Errorf("worktree path %s exists but is not registered in %s", dir, repo)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "HEAD^{commit}").Output()
	if err != nil {
		return "", false, gitOutputError("git -C "+dir+" rev-parse --verify HEAD^{commit}", err)
	}
	return strings.TrimSpace(string(out)), true, nil
}

// OriginURL returns the configured URL for the origin remote.
func OriginURL(repo string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", gitOutputError("git remote get-url origin", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// HasOrigin reports whether the repo has an "origin" remote configured.
func HasOrigin(repo string) bool {
	_, err := OriginURL(repo)
	return err == nil
}

// BranchExists reports whether the named branch exists locally.
func BranchExists(repo, branch string) bool {
	exists, _ := localBranchExists(repo, branch)
	return exists
}

// DeleteBranch removes a branch with `git branch -d` (refuses if unmerged).
func DeleteBranch(repo, branch string) error {
	out, err := exec.Command("git", "-C", repo, "branch", "-d", branch).CombinedOutput()
	if err != nil {
		return gitCommandError("git branch -d "+branch, err, out)
	}
	return nil
}

// ForceDeleteBranch removes a branch with `git branch -D` (deletes unmerged).
func ForceDeleteBranch(repo, branch string) error {
	out, err := exec.Command("git", "-C", repo, "branch", "-D", branch).CombinedOutput()
	if err != nil {
		return gitCommandError("git branch -D "+branch, err, out)
	}
	return nil
}

// IsBranchReachable reports whether branch's tip is an ancestor of base's tip.
// "Safe to delete" semantics — true means deleting branch loses no work.
func IsBranchReachable(repo, branch, base string) bool {
	return exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", branch, base).Run() == nil
}

// WorkMerged reports whether branch has commits beyond startSHA and those
// commits are reachable from base. Missing branches and normal non-ancestor
// results are not errors.
func WorkMerged(repo, branch, base, startSHA string) (bool, error) {
	if startSHA == "" {
		return false, nil
	}
	exists, err := localBranchExists(repo, branch)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	ref := "refs/heads/" + branch
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", ref+"^{commit}").Output()
	if err != nil {
		return false, gitOutputError("git rev-parse --verify "+ref+"^{commit}", err)
	}
	if strings.TrimSpace(string(out)) == startSHA {
		return false, nil
	}
	_, err = exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", ref, base).Output()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, gitOutputError("git merge-base --is-ancestor "+ref+" "+base, err)
}

// IsWorkMerged preserves the conservative boolean interface for existing callers.
func IsWorkMerged(repo, branch, base, startSHA string) bool {
	merged, _ := WorkMerged(repo, branch, base, startSHA)
	return merged
}

func localBranchExists(repo, branch string) (bool, error) {
	ref := "refs/heads/" + branch
	_, err := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", ref).Output()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, gitOutputError("git show-ref --verify "+ref, err)
}

func gitCommandError(command string, err error, output []byte) error {
	diagnostic := SanitizeDiagnostic(string(output))
	if diagnostic == "" {
		return fmt.Errorf("%s: %w", command, err)
	}
	return fmt.Errorf("%s: %w\n%s", command, err, diagnostic)
}

func gitOutputError(command string, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return gitCommandError(command, err, exitErr.Stderr)
	}
	return fmt.Errorf("%s: %w", command, err)
}

// DetectDefaultBranch returns the repo's default branch. Prefers
// origin/HEAD; falls back to probing "main" then "master". Returns "" if
// none is found.
func DetectDefaultBranch(repo string) string {
	branch, _ := DetectDefaultBranchWithError(repo)
	return branch
}

// DetectDefaultBranchWithError returns the repo's default branch and preserves
// the Git diagnostic when neither origin/HEAD nor a local main/master exists.
func DetectDefaultBranchWithError(repo string) (string, error) {
	out, err := exec.Command(
		"git", "-C", repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD",
	).Output()
	var symbolicRefErr error
	if err == nil {
		ref := strings.TrimSpace(string(out))
		if branch, ok := strings.CutPrefix(ref, "origin/"); ok && branch != "" {
			return branch, nil
		}
		symbolicRefErr = fmt.Errorf("symbolic ref %q is invalid", ref)
	} else {
		symbolicRefErr = gitOutputError(
			"git symbolic-ref --short refs/remotes/origin/HEAD", err,
		)
	}
	for _, candidate := range []string{"main", "master"} {
		exists, branchErr := localBranchExists(repo, candidate)
		if branchErr != nil {
			return "", fmt.Errorf("cannot determine default branch: %w", branchErr)
		}
		if exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("cannot determine default branch: %w", symbolicRefErr)
}

// Fetch updates origin's remote-tracking ref for branch. It returns sanitized
// combined output alongside any error so callers can emit useful diagnostics.
func Fetch(repo, branch string) (string, error) {
	refspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch)
	out, err := exec.Command("git", "-C", repo, "fetch", "origin", refspec).CombinedOutput()
	diagnostic := SanitizeDiagnostic(string(out))
	if err != nil {
		return diagnostic, fmt.Errorf("git fetch origin %s: %w", branch, err)
	}
	return diagnostic, nil
}

// SanitizeDiagnostic removes secret-bearing URL components from subprocess
// output while preserving the host, path, and surrounding diagnostic.
func SanitizeDiagnostic(output string) string {
	output = strings.TrimSpace(output)
	output = gitURLPattern.ReplaceAllStringFunc(output, sanitizeGitDiagnosticURL)
	return scpLikeURLPattern.ReplaceAllStringFunc(output, sanitizeSCPStyleURL)
}

func sanitizeGitDiagnosticURL(rawURL string) string {
	if secretStart := strings.IndexAny(rawURL, "?#"); secretStart >= 0 {
		rawURL = rawURL[:secretStart]
	}
	schemeEnd := strings.Index(rawURL, "://")
	if schemeEnd < 0 {
		return rawURL
	}
	authorityStart := schemeEnd + len("://")
	authorityEnd := len(rawURL)
	if pathStart := strings.IndexByte(rawURL[authorityStart:], '/'); pathStart >= 0 {
		authorityEnd = authorityStart + pathStart
	}
	authority := rawURL[authorityStart:authorityEnd]
	if userinfoEnd := strings.LastIndexByte(authority, '@'); userinfoEnd >= 0 {
		rawURL = rawURL[:authorityStart] + "[redacted]@" + authority[userinfoEnd+1:] + rawURL[authorityEnd:]
	}
	return rawURL
}

func sanitizeSCPStyleURL(rawURL string) string {
	userinfoEnd := strings.LastIndexByte(rawURL, '@')
	if userinfoEnd < 0 {
		return rawURL
	}
	hostPath := rawURL[userinfoEnd+1:]
	pathStart := strings.IndexByte(hostPath, ':')
	if strings.HasPrefix(hostPath, "[") {
		pathStart = strings.Index(hostPath, "]:")
		if pathStart >= 0 {
			pathStart++
		}
	}
	if pathStart <= 0 || pathStart == len(hostPath)-1 {
		return rawURL
	}
	path := hostPath[pathStart+1:]
	if secretStart := strings.IndexAny(path, "?#"); secretStart >= 0 {
		path = path[:secretStart]
	}
	if path == "" || (!strings.Contains(path, "/") && !strings.HasSuffix(path, ".git")) {
		return rawURL
	}
	return "[redacted]@" + hostPath[:pathStart+1] + path
}

// WorktreeAdd creates a new worktree at dir on a new branch, started from startPoint.
func WorktreeAdd(repo, dir, branch, startPoint string) error {
	out, err := exec.Command("git", "-C", repo, "worktree", "add", dir, "-b", branch, startPoint).CombinedOutput()
	if err != nil {
		return gitCommandError("git worktree add "+dir, err, out)
	}
	return nil
}

// IsWorktree reports whether dir is registered as a git worktree of repo.
// The returned bool is only meaningful when err is nil.
func IsWorktree(repo, dir string) (bool, error) {
	out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return false, gitOutputError("git worktree list", err)
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
		return false, gitOutputError("git -C "+dir+" status --porcelain", err)
	}
	return strings.TrimSpace(string(out)) == "", nil
}

// WorktreeRemove removes the worktree at dir. If force is true, includes --force.
// An absent unregistered path is safe to prune, but an existing unregistered
// path is preserved because Relay cannot prove that it owns its contents.
func WorktreeRemove(repo, dir string, force bool) error {
	registered, err := IsWorktree(repo, dir)
	if err != nil {
		return err
	}
	if !registered {
		if _, statErr := os.Stat(dir); statErr == nil {
			return fmt.Errorf("worktree path %s exists but is not registered in %s", dir, repo)
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("stat worktree %s: %w", dir, statErr)
		}
		if out, pruneErr := exec.Command("git", "-C", repo, "worktree", "prune").CombinedOutput(); pruneErr != nil {
			return gitCommandError("git worktree prune", pruneErr, out)
		}
		return nil
	}
	return removeRegisteredWorktree(repo, dir, force)
}

// WorktreeReclaim removes a registered worktree or an unregistered direct
// child of the repository's .worktrees directory. The latter is reserved for
// Relay's interrupted-project setup recovery, where the caller has already
// applied its clean-worktree and confirmation policy.
func WorktreeReclaim(repo, dir string, force bool) error {
	if err := validateRelayWorktreeTarget(repo, dir); err != nil {
		return err
	}
	registered, err := IsWorktree(repo, dir)
	if err != nil {
		return err
	}
	if registered {
		return removeRegisteredWorktree(repo, dir, force)
	}
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat worktree %s: %w", dir, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove Relay worktree path %s: %w", dir, err)
	}
	if out, err := exec.Command("git", "-C", repo, "worktree", "prune").CombinedOutput(); err != nil {
		return gitCommandError("git worktree prune", err, out)
	}
	return nil
}

func validateRelayWorktreeTarget(repo, dir string) error {
	root, err := filepath.Abs(filepath.Join(repo, ".worktrees"))
	if err != nil {
		return fmt.Errorf("resolve Relay worktree root: %w", err)
	}
	root = canonPath(root)
	target, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve worktree path %s: %w", dir, err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(target); resolveErr == nil {
		target = resolved
	} else if parent, parentErr := filepath.EvalSymlinks(filepath.Dir(target)); parentErr == nil {
		target = filepath.Join(parent, filepath.Base(target))
	} else {
		target = filepath.Clean(target)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(rel) || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.Dir(rel) != "." {
		return fmt.Errorf("refuse to reclaim worktree path outside %s: %s", root, dir)
	}
	return nil
}

func removeRegisteredWorktree(repo, dir string, force bool) error {
	args := []string{"-C", repo, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, dir)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return gitCommandError("git worktree remove "+dir, err, out)
	}
	return nil
}
