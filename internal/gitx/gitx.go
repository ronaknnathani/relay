// Package gitx wraps git CLI invocations used by the relay CLI. Every
// function returns an error describing the operation and includes git's
// stderr when available.
package gitx

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	// ErrInvalidWorkStart marks start_sha values that cannot safely anchor a
	// destructive merge decision.
	ErrInvalidWorkStart = errors.New("invalid work start")

	removeBranchConfig = removeLocalBranchConfig
)

const maxGitDiagnosticOutput = 8 * 1024

const fetchTempRefPrefix = "refs/relay/fetch/"

// BranchCheckedOutError reports the worktree preventing branch deletion.
type BranchCheckedOutError struct {
	Branch   string
	Worktree string
}

func (e *BranchCheckedOutError) Error() string {
	return fmt.Sprintf(
		"branch %q is checked out in worktree %s; remove or detach that worktree before deleting it",
		e.Branch, e.Worktree,
	)
}

// RepoRoot returns the absolute path to the top-level directory of the
// current git repository, or an empty string and an error if cwd is not
// in a git repo.
func RepoRoot() (string, error) {
	return RepositoryRoot(".")
}

// RepositoryRoot returns the canonical top-level worktree containing path.
func RepositoryRoot(path string) (string, error) {
	out, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", gitOutputError("git rev-parse --show-toplevel", err)
	}
	root := strings.TrimSpace(string(out))
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved, nil
	}
	return filepath.Clean(root), nil
}

// CanonicalRepositoryRoot resolves path and requires it to name the repository
// root exactly. Symlink-resolution failures are returned instead of guessed.
func CanonicalRepositoryRoot(path string) (string, error) {
	root, err := RepositoryRoot(path)
	if err != nil {
		return "", err
	}
	root, err = CanonicalPath(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root %s: %w", root, err)
	}
	recorded, err := CanonicalPath(path)
	if err != nil {
		return "", fmt.Errorf("resolve recorded repository %s: %w", path, err)
	}
	if recorded != root {
		return "", fmt.Errorf(
			"recorded repository %q resolves inside repository root %q instead of naming the root",
			path, root,
		)
	}
	return root, nil
}

// CanonicalGitCommonDir returns the canonical Git common directory shared by
// the repository's linked worktrees.
func CanonicalGitCommonDir(path string) (string, error) {
	out, err := exec.Command("git", "-C", path, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", gitOutputError("git rev-parse --git-common-dir", err)
	}
	commonDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(path, commonDir)
	}
	commonDir, err = CanonicalPath(commonDir)
	if err != nil {
		return "", fmt.Errorf("resolve git common directory %s: %w", commonDir, err)
	}
	return commonDir, nil
}

// CanonicalPath resolves all existing symlinks in path. Missing final
// components are appended only after their nearest existing ancestor resolves.
func CanonicalPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is empty")
	}
	current, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make path absolute: %w", err)
	}
	var suffix []string
	for {
		_, err := os.Lstat(current)
		switch {
		case err == nil:
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return "", fmt.Errorf("resolve symlink path %s: %w", current, resolveErr)
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		case !errors.Is(err, os.ErrNotExist):
			return "", fmt.Errorf("inspect path %s: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve path %s: no existing ancestor", path)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
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

// RefExists reports whether an exact Git ref exists without discarding Git failures.
func RefExists(repo, ref string) (bool, error) {
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

// WorktreeHead resolves the current commit of a registered worktree. A missing
// path remains found when Git still records it; existing unregistered paths
// return an error.
func WorktreeHead(repo, dir string) (sha string, found bool, err error) {
	state, found, err := RegisteredWorktreeState(repo, dir)
	return state.Head, found, err
}

// WorktreeState identifies the commit and checked-out branch of a registered
// worktree. Branch is empty when Detached is true.
type WorktreeState struct {
	Head     string
	Branch   string
	Detached bool
}

// RegisteredWorktreeState resolves the identity of a registered worktree.
// It queries Git independently of path existence so stale registrations remain
// visible. Existing unregistered paths are errors.
func RegisteredWorktreeState(repo, dir string) (state WorktreeState, found bool, err error) {
	info, statErr := os.Lstat(dir)
	if statErr != nil && !os.IsNotExist(statErr) {
		return WorktreeState{}, false, fmt.Errorf("inspect worktree %s: %w", dir, statErr)
	}
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return WorktreeState{}, false, fmt.Errorf("worktree path %s is a symlink", dir)
	}
	state, found, err = listedWorktreeState(repo, dir)
	if err != nil || found {
		return state, found, err
	}
	if os.IsNotExist(statErr) {
		return WorktreeState{}, false, nil
	}
	return WorktreeState{}, false, fmt.Errorf(
		"worktree path %s exists but is not registered in %s", dir, repo,
	)
}

func listedWorktreeState(repo, dir string) (state WorktreeState, found bool, err error) {
	out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return WorktreeState{}, false, gitOutputError("git worktree list", err)
	}
	target := canonPath(dir)
	var currentPath string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			if currentPath != "" && canonPath(currentPath) == target {
				return state, true, nil
			}
			currentPath = ""
			state = WorktreeState{}
			continue
		}
		switch {
		case strings.HasPrefix(line, "worktree "):
			currentPath = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "HEAD "):
			state.Head = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
		case strings.HasPrefix(line, "branch "):
			state.Branch = strings.TrimSpace(strings.TrimPrefix(line, "branch "))
		case line == "detached":
			state.Detached = true
		}
	}
	if currentPath != "" && canonPath(currentPath) == target {
		return state, true, nil
	}
	return WorktreeState{}, false, nil
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
	exists, _ := LocalBranchExists(repo, branch)
	return exists
}

// LocalBranchExists reports whether the named branch exists locally without
// discarding Git failures.
func LocalBranchExists(repo, branch string) (bool, error) {
	return localBranchExists(repo, branch)
}

// DeleteBranch removes a branch with `git branch -d` (refuses if unmerged).
func DeleteBranch(repo, branch string) error {
	out, err := boundedCombinedOutput(exec.Command("git", "-C", repo, "branch", "-d", branch))
	if err != nil {
		return gitCommandError("git branch -d "+branch, err, out)
	}
	return nil
}

// ForceDeleteBranch removes a branch with `git branch -D` (deletes unmerged).
func ForceDeleteBranch(repo, branch string) error {
	out, err := boundedCombinedOutput(exec.Command("git", "-C", repo, "branch", "-D", branch))
	if err != nil {
		return gitCommandError("git branch -D "+branch, err, out)
	}
	return nil
}

// ForceDeleteBranchAt removes a branch only when it still points at expectedSHA.
func ForceDeleteBranchAt(repo, branch, expectedSHA string) error {
	if err := validateBranchName(repo, branch); err != nil {
		return err
	}
	ref := "refs/heads/" + branch
	worktree, checkedOut, err := branchCheckout(repo, ref)
	if err != nil {
		return err
	}
	if checkedOut {
		return &BranchCheckedOutError{Branch: branch, Worktree: worktree}
	}
	command := "git update-ref -d " + ref + " " + expectedSHA
	out, err := boundedCombinedOutput(exec.Command(
		"git", "-C", repo, "update-ref", "-d", ref, expectedSHA,
	))
	if err == nil {
		if err := removeBranchConfig(repo, branch); err != nil {
			return fmt.Errorf(
				"branch ref %q was deleted, but its local config could not be removed: %w",
				branch, err,
			)
		}
		return nil
	}
	deleteErr := gitCommandError(command, err, out)
	tip, found, tipErr := LocalBranchTip(repo, branch)
	if tipErr != nil {
		return fmt.Errorf("%w; inspect branch %q after failed deletion: %v", deleteErr, branch, tipErr)
	}
	if !found {
		return fmt.Errorf("branch %q disappeared before deletion: %w", branch, deleteErr)
	}
	if tip != expectedSHA {
		return fmt.Errorf(
			"branch %q changed from %s to %s before deletion: %w",
			branch, expectedSHA, tip, deleteErr,
		)
	}
	return deleteErr
}

// BranchCheckoutWorktree reports the worktree currently checking out branch.
func BranchCheckoutWorktree(repo, branch string) (string, bool, error) {
	if err := validateBranchName(repo, branch); err != nil {
		return "", false, err
	}
	return branchCheckout(repo, "refs/heads/"+branch)
}

// RemoveBranchConfig removes the local branch.<name> configuration section.
// A branch without a section is already clean.
func RemoveBranchConfig(repo, branch string) error {
	if err := validateBranchName(repo, branch); err != nil {
		return err
	}
	return removeLocalBranchConfig(repo, branch)
}

// CanonicalBranchRef validates branch as an exact local branch name and
// returns its fully qualified ref.
func CanonicalBranchRef(repo, branch string) (string, error) {
	if strings.TrimSpace(branch) == "" {
		return "", fmt.Errorf("branch is empty")
	}
	ref := "refs/heads/" + branch
	out, err := boundedCombinedOutput(exec.Command("git", "-C", repo, "check-ref-format", ref))
	if err != nil {
		return "", gitCommandError("git check-ref-format "+ref, err, out)
	}
	return ref, nil
}

func validateBranchName(repo, branch string) error {
	out, err := boundedCombinedOutput(exec.Command(
		"git", "-C", repo, "check-ref-format", "--branch", branch,
	))
	if err != nil {
		return gitCommandError("git check-ref-format --branch "+branch, err, out)
	}
	return nil
}

func removeLocalBranchConfig(repo, branch string) error {
	section := "branch." + branch
	out, err := boundedCombinedOutput(exec.Command(
		"git", "-C", repo, "config", "--local", "--remove-section", section,
	))
	if err == nil {
		return nil
	}
	if configSectionAbsent(err, out, section) {
		return nil
	}
	return gitCommandError("git config --local --remove-section "+section, err, out)
}

func configSectionAbsent(err error, output []byte, section string) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) &&
		strings.HasSuffix(
			strings.TrimSpace(string(output)),
			"fatal: no such section: "+section,
		)
}

func branchCheckout(repo, ref string) (string, bool, error) {
	out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return "", false, gitOutputError("git worktree list", err)
	}
	var worktree string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			worktree = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "branch ") &&
			strings.TrimSpace(strings.TrimPrefix(line, "branch ")) == ref:
			return worktree, true, nil
		case line == "":
			worktree = ""
		}
	}
	return "", false, nil
}

// CommitReachable reports whether commit is an ancestor of base.
func CommitReachable(repo, commit, base string) (bool, error) {
	_, err := exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", commit, base).Output()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, gitOutputError("git merge-base --is-ancestor "+commit+" "+base, err)
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
	_, merged, err := WorkMergedTip(repo, branch, base, startSHA)
	return merged, err
}

// WorkMergedInto reports whether the branch's complete work range is present
// in base through ordinary ancestry or an exact squash/rebase-equivalent patch.
func WorkMergedInto(repo, branch, base, startSHA string) (tip string, merged bool, err error) {
	startCommit, branchTip, exists, err := workRange(repo, branch, startSHA)
	if err != nil || !exists || branchTip == startCommit {
		return branchTip, false, err
	}
	reachable, err := CommitReachable(repo, branchTip, base)
	if err != nil || reachable {
		return branchTip, reachable, err
	}

	workCommits, linear, err := linearCommitRange(repo, startCommit, branchTip)
	if err != nil || !linear {
		return branchTip, false, err
	}
	upstreamCommits, bounded, err := firstParentCommitsAfter(repo, startCommit, base)
	if err != nil || !bounded {
		return branchTip, false, err
	}
	workFingerprint, err := patchFingerprint(repo, startCommit, branchTip)
	if err != nil || workFingerprint == "" {
		return branchTip, false, err
	}

	squashed, err := containsPatchFingerprint(
		repo, startCommit, upstreamCommits, 1, workFingerprint,
	)
	if err != nil || squashed {
		return branchTip, squashed, err
	}

	rebased, err := containsPatchFingerprint(
		repo, startCommit, upstreamCommits, len(workCommits), workFingerprint,
	)
	return branchTip, rebased, err
}

func containsPatchFingerprint(
	repo, rangeStart string, commits []string, windowSize int, expected string,
) (bool, error) {
	for end := windowSize; end <= len(commits); end++ {
		start := end - windowSize
		parent := rangeStart
		if start > 0 {
			parent = commits[start-1]
		}
		fingerprint, err := patchFingerprint(repo, parent, commits[end-1])
		if err != nil {
			return false, err
		}
		if fingerprint == expected {
			return true, nil
		}
	}
	return false, nil
}

// WorkMergedTip reports whether the exact returned branch tip contains work
// beyond startSHA and is reachable from base.
func WorkMergedTip(repo, branch, base, startSHA string) (tip string, merged bool, err error) {
	startCommit, branchTip, exists, err := workRange(repo, branch, startSHA)
	if err != nil || !exists || branchTip == startCommit {
		return branchTip, false, err
	}
	reachable, err := CommitReachable(repo, branchTip, base)
	return branchTip, reachable, err
}

func workRange(repo, branch, startSHA string) (startCommit, branchTip string, exists bool, err error) {
	if startSHA == "" {
		return "", "", false, nil
	}
	startExpression := strings.TrimSpace(startSHA) + "^{commit}"
	startOutput, err := exec.Command(
		"git", "-C", repo, "rev-parse", "--verify", "--end-of-options", startExpression,
	).Output()
	if err != nil {
		return "", "", false, fmt.Errorf(
			"%w: start_sha %q in %s does not resolve to a commit: %v",
			ErrInvalidWorkStart, startSHA, repo,
			gitOutputError("git rev-parse --verify --end-of-options "+startExpression, err),
		)
	}
	startCommit = strings.TrimSpace(string(startOutput))

	exists, err = localBranchExists(repo, branch)
	if err != nil {
		return "", "", false, err
	}
	if !exists {
		return startCommit, "", false, nil
	}
	ref := "refs/heads/" + branch
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", ref+"^{commit}").Output()
	if err != nil {
		return "", "", false, gitOutputError("git rev-parse --verify "+ref+"^{commit}", err)
	}
	branchTip = strings.TrimSpace(string(out))
	_, err = exec.Command(
		"git", "-C", repo, "merge-base", "--is-ancestor", startCommit, branchTip,
	).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", branchTip, false, fmt.Errorf(
				"%w: start_sha %q resolves to %s, which is not an ancestor of branch %q",
				ErrInvalidWorkStart, startSHA, startCommit, branch,
			)
		}
		return "", branchTip, false, fmt.Errorf(
			"%w: verify start_sha %q against branch %q: %v",
			ErrInvalidWorkStart, startSHA, branch,
			gitOutputError("git merge-base --is-ancestor "+startCommit+" "+branchTip, err),
		)
	}
	return startCommit, branchTip, true, nil
}

func linearCommitRange(repo, start, tip string) ([]string, bool, error) {
	out, err := exec.Command(
		"git", "-C", repo, "rev-list", "--reverse", "--topo-order", "--parents", start+".."+tip,
	).Output()
	if err != nil {
		return nil, false, gitOutputError("git rev-list linear work range", err)
	}
	expectedParent := start
	var commits []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != expectedParent {
			return nil, false, nil
		}
		commits = append(commits, fields[0])
		expectedParent = fields[0]
	}
	return commits, len(commits) > 0, nil
}

func firstParentCommitsAfter(repo, start, base string) ([]string, bool, error) {
	out, err := exec.Command(
		"git", "-C", repo, "rev-list", "--first-parent", "--reverse", start+".."+base,
	).Output()
	if err != nil {
		return nil, false, gitOutputError("git rev-list first-parent base range", err)
	}
	commits := strings.Fields(string(out))
	if len(commits) == 0 {
		return nil, base == start, nil
	}
	parentOutput, err := exec.Command(
		"git", "-C", repo, "rev-list", "--parents", "-n", "1", commits[0],
	).Output()
	if err != nil {
		return nil, false, gitOutputError("git rev-list first-parent base boundary", err)
	}
	parentFields := strings.Fields(string(parentOutput))
	if len(parentFields) < 2 || parentFields[1] != start {
		return nil, false, nil
	}
	return commits, true, nil
}

func patchFingerprint(repo, from, to string) (string, error) {
	diff, err := exec.Command(
		"git", "-C", repo, "diff", "--binary", "--full-index", "--no-ext-diff",
		"--no-renames", from, to,
	).Output()
	if err != nil {
		return "", gitOutputError("git diff complete work range", err)
	}
	command := exec.Command("git", "-C", repo, "patch-id", "--verbatim")
	command.Stdin = bytes.NewReader(diff)
	out, err := command.Output()
	if err != nil {
		return "", gitOutputError("git patch-id --verbatim", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// IsWorkMerged preserves the conservative boolean interface for existing callers.
func IsWorkMerged(repo, branch, base, startSHA string) bool {
	merged, _ := WorkMerged(repo, branch, base, startSHA)
	return merged
}

func localBranchExists(repo, branch string) (bool, error) {
	return RefExists(repo, "refs/heads/"+branch)
}

func gitCommandError(command string, err error, output []byte) error {
	diagnostic := SanitizeDiagnostic(string(output))
	if diagnostic == "" {
		return fmt.Errorf("%s: %w", command, err)
	}
	return fmt.Errorf("%s: %w\n%s", command, err, diagnostic)
}

type diagnosticBuffer struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *diagnosticBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	if len(data) >= maxGitDiagnosticOutput {
		b.buffer.Reset()
		_, _ = b.buffer.Write(data[len(data)-maxGitDiagnosticOutput:])
		b.truncated = true
		return originalLength, nil
	}
	overflow := b.buffer.Len() + len(data) - maxGitDiagnosticOutput
	if overflow > 0 {
		current := append([]byte(nil), b.buffer.Bytes()[overflow:]...)
		b.buffer.Reset()
		_, _ = b.buffer.Write(current)
		b.truncated = true
	}
	_, _ = b.buffer.Write(data)
	return originalLength, nil
}

func (b *diagnosticBuffer) Bytes() []byte {
	if !b.truncated {
		return append([]byte(nil), b.buffer.Bytes()...)
	}
	notice := fmt.Sprintf(
		"[... git diagnostic truncated; showing last %d bytes ...]\n",
		maxGitDiagnosticOutput,
	)
	retained := b.buffer.Bytes()
	if len(retained) > 0 && retained[0] != '\n' && retained[0] != '\t' && retained[0] != ' ' {
		boundary := bytes.IndexAny(retained, " \t\r\n")
		notice += "[... leading truncated token redacted ...]"
		if boundary < 0 {
			return []byte(notice)
		}
		retained = retained[boundary:]
	}
	return append([]byte(notice), retained...)
}

func boundedCombinedOutput(command *exec.Cmd) ([]byte, error) {
	var output diagnosticBuffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return output.Bytes(), err
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
	diagnostic, _, err := FetchBaseSnapshot(repo, branch)
	return diagnostic, err
}

// FetchBaseSnapshot updates origin's remote-tracking ref for branch and
// returns the exact fetched commit for immutable merge evaluation.
func FetchBaseSnapshot(repo, branch string) (diagnostic, commit string, retErr error) {
	tempRef, err := newFetchTempRef()
	if err != nil {
		return "", "", err
	}
	defer func() {
		err := exec.Command("git", "-C", repo, "update-ref", "-d", tempRef).Run()
		if err != nil {
			retErr = errors.Join(
				retErr,
				gitOutputError("git update-ref -d "+tempRef, err),
			)
		}
	}()

	refspec := fmt.Sprintf("+refs/heads/%s:%s", branch, tempRef)
	out, err := boundedCombinedOutput(exec.Command(
		"git", "-C", repo, "fetch", "--no-write-fetch-head", "origin", refspec,
	))
	diagnostic = SanitizeDiagnostic(string(out))
	if err != nil {
		return diagnostic, "", fmt.Errorf("git fetch origin %s: %w", branch, err)
	}
	fetched, err := exec.Command(
		"git", "-C", repo, "rev-parse", "--verify", tempRef+"^{commit}",
	).Output()
	if err != nil {
		return diagnostic, "", gitOutputError("git rev-parse --verify "+tempRef+"^{commit}", err)
	}
	commit = strings.TrimSpace(string(fetched))
	trackingRef := "refs/remotes/origin/" + branch
	if out, err := boundedCombinedOutput(exec.Command(
		"git", "-C", repo, "update-ref", trackingRef, commit,
	)); err != nil {
		return diagnostic, "", gitCommandError("git update-ref "+trackingRef, err, out)
	}
	return diagnostic, commit, nil
}

func newFetchTempRef() (string, error) {
	var identifier [16]byte
	if _, err := rand.Read(identifier[:]); err != nil {
		return "", fmt.Errorf("generate temporary fetch ref: %w", err)
	}
	return fetchTempRefPrefix + hex.EncodeToString(identifier[:]), nil
}

const redactedRemoteToken = "[redacted-remote]"

// SanitizeDiagnostic removes terminal controls and secret-bearing remote
// tokens while preserving surrounding subprocess diagnostics.
func SanitizeDiagnostic(output string) string {
	var sanitized, token strings.Builder
	sanitized.Grow(len(output))
	token.Grow(128)
	tokenTainted := false
	pendingControl := false
	flushToken := func() {
		if token.Len() == 0 {
			return
		}
		sanitized.WriteString(sanitizeDiagnosticToken(token.String(), tokenTainted))
		token.Reset()
		tokenTainted = false
	}

	for index := 0; index < len(output); {
		if end, control := terminalControlEnd(output, index); control {
			if token.Len() > 0 {
				tokenTainted = true
			} else {
				pendingControl = true
			}
			index = end
			continue
		}
		character := output[index]
		if character == '\r' {
			flushToken()
			sanitized.WriteByte('\n')
			pendingControl = false
			index++
			continue
		}
		if character == '\n' || character == '\t' || character == ' ' {
			flushToken()
			sanitized.WriteByte(character)
			pendingControl = false
			index++
			continue
		}
		if token.Len() == 0 {
			tokenTainted = pendingControl
			pendingControl = false
		}
		token.WriteByte(character)
		index++
	}
	flushToken()
	return strings.TrimSpace(sanitized.String())
}

func terminalControlEnd(output string, start int) (int, bool) {
	character := output[start]
	switch {
	case character == '\x1b':
		if start+1 >= len(output) {
			return start + 1, true
		}
		switch output[start+1] {
		case '[':
			return controlSequenceEnd(output, start+2), true
		case ']', 'P', 'X', '^', '_':
			return controlStringEnd(output, start+2), true
		case '\\':
			return start + 2, true
		default:
			if output[start+1] >= 0x40 && output[start+1] <= 0x5f {
				return start + 2, true
			}
			return start + 1, true
		}
	case character == '\x9b':
		return controlSequenceEnd(output, start+1), true
	case character == '\x90' || character == '\x98' || character == '\x9d' ||
		character == '\x9e' || character == '\x9f':
		return controlStringEnd(output, start+1), true
	case character < ' ' && character != '\n' && character != '\t' && character != '\r':
		return start + 1, true
	case character == '\x7f' || character >= '\x80' && character <= '\x9f':
		return start + 1, true
	default:
		return start, false
	}
}

func controlSequenceEnd(output string, start int) int {
	for index := start; index < len(output); index++ {
		if output[index] >= 0x40 && output[index] <= 0x7e {
			return index + 1
		}
		if output[index] < 0x20 || output[index] > 0x3f {
			return index
		}
	}
	return start
}

func controlStringEnd(output string, start int) int {
	for index := start; index < len(output); index++ {
		switch output[index] {
		case '\a', '\x9c':
			return index + 1
		case '\x1b':
			if index+1 < len(output) && output[index+1] == '\\' {
				return index + 2
			}
		}
	}
	return start
}

func sanitizeDiagnosticToken(token string, controlTainted bool) string {
	if controlTainted && looksRemoteLike(token) {
		return redactedRemoteToken
	}
	prefix, remote, suffix := splitDiagnosticToken(token)
	cleaned, remoteLike, confident := sanitizeRemoteToken(remote)
	if remoteLike && !confident {
		return redactedRemoteToken
	}
	if confident {
		return prefix + cleaned + suffix
	}
	return token
}

func splitDiagnosticToken(token string) (prefix, core, suffix string) {
	if token == "" || token[0] != '\'' && token[0] != '"' {
		end := len(token)
		for end > 0 && strings.ContainsRune(",.;!", rune(token[end-1])) {
			end--
		}
		return "", token[:end], token[end:]
	}
	quote := token[0]
	close := strings.LastIndexByte(token[1:], quote)
	if close < 0 {
		return token[:1], token[1:], ""
	}
	close++
	for _, character := range token[close+1:] {
		if !strings.ContainsRune(":,.;!?)]}", character) {
			return token[:1], token[1:], ""
		}
	}
	return token[:1], token[1:close], token[close:]
}

func sanitizeRemoteToken(remote string) (cleaned string, remoteLike, confident bool) {
	helperPrefix, address, hasHelper, validHelpers := splitRemoteHelpers(remote)
	if !validHelpers {
		return "", true, false
	}
	if strings.Contains(address, "://") {
		cleaned, ok := sanitizeSchemeRemote(address)
		return helperPrefix + cleaned, true, ok
	}
	if strings.Contains(address, "@") {
		cleaned, ok := sanitizeUserinfoRemote(address)
		return helperPrefix + cleaned, true, ok
	}
	if cleaned, ok, resemblesRemote := sanitizeSCPRemote(address); resemblesRemote {
		return helperPrefix + cleaned, true, ok
	}
	if hasHelper || looksRemoteLike(address) {
		return "", true, false
	}
	return remote, false, false
}

func splitRemoteHelpers(remote string) (prefix, address string, hasHelper, valid bool) {
	const maxHelpers = 32
	offset := 0
	address = remote[offset:]
	helperCount := 0
	for {
		helperEnd := strings.Index(address, "::")
		schemeEnd := strings.Index(address, "://")
		if helperEnd < 0 || schemeEnd >= 0 && schemeEnd < helperEnd {
			return remote[:offset], address, hasHelper, true
		}
		candidate := address[:helperEnd]
		if strings.ContainsAny(candidate, "@[]") {
			return remote[:offset], address, hasHelper, true
		}
		if !validRemoteHelper(candidate) {
			return "", "", true, false
		}
		helperCount++
		if helperCount > maxHelpers {
			return "", "", true, false
		}
		hasHelper = true
		offset += helperEnd + 2
		address = remote[offset:]
		if address == "" {
			return "", "", true, false
		}
	}
}

func validRemoteHelper(helper string) bool {
	if helper == "" || !isASCIILetter(helper[0]) {
		return false
	}
	for index := 1; index < len(helper); index++ {
		if !isSchemeCharacter(helper[index]) && helper[index] != '_' {
			return false
		}
	}
	return true
}

func sanitizeSchemeRemote(remote string) (string, bool) {
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Scheme == "" || parsed.Opaque != "" {
		return "", false
	}
	schemeEnd := strings.Index(remote, "://")
	authorityStart := schemeEnd + 3
	authorityEnd := len(remote)
	if boundary := strings.IndexAny(remote[authorityStart:], "/?#"); boundary >= 0 {
		authorityEnd = authorityStart + boundary
	}
	authority := remote[authorityStart:authorityEnd]
	if parsed.Scheme != "file" && authority == "" || strings.HasSuffix(authority, ":") {
		return "", false
	}
	if parsed.User == nil && parsed.Port() != "" && credentialName(parsed.Hostname()) {
		return "", false
	}
	end := len(remote)
	if secretStart := strings.IndexAny(remote, "?#"); secretStart >= 0 {
		end = secretStart
	}
	cleaned := remote[:end]
	if parsed.User == nil {
		return cleaned, true
	}
	userinfoEnd := strings.LastIndexByte(authority, '@')
	if userinfoEnd < 0 {
		return "", false
	}
	return remote[:authorityStart] + "[redacted]@" +
		authority[userinfoEnd+1:] + remote[authorityEnd:end], true
}

func sanitizeUserinfoRemote(remote string) (string, bool) {
	if strings.Count(remote, "@") != 1 {
		return "", false
	}
	userinfoEnd := strings.IndexByte(remote, '@')
	if userinfoEnd <= 0 {
		return "", false
	}
	target := remote[userinfoEnd+1:]
	if secretStart := strings.IndexAny(target, "?#"); secretStart >= 0 {
		target = target[:secretStart]
	}
	if target == "" || strings.HasSuffix(target, ":") || !validRemoteTarget(target) {
		return "", false
	}
	return "[redacted]@" + target, true
}

func validRemoteTarget(target string) bool {
	if target[0] == '[' {
		close := strings.IndexByte(target, ']')
		if close < 0 {
			return false
		}
		remainder := target[close+1:]
		return remainder == "" ||
			len(remainder) > 1 && (remainder[0] == ':' || remainder[0] == '/')
	}
	hostEnd := len(target)
	if separator := strings.IndexAny(target, ":/"); separator >= 0 {
		hostEnd = separator
		if separator == len(target)-1 {
			return false
		}
	}
	return hostEnd > 0
}

func sanitizeSCPRemote(remote string) (cleaned string, confident, remoteLike bool) {
	secretStart := strings.IndexAny(remote, "?#")
	if secretStart >= 0 {
		remote = remote[:secretStart]
	}
	separator := strings.IndexByte(remote, ':')
	if separator <= 0 {
		return "", false, false
	}
	host, path := remote[:separator], remote[separator+1:]
	hostLooksRemote := strings.ContainsAny(host, "._-") || strings.HasPrefix(host, "[")
	if path == "" {
		return "", false, hostLooksRemote || credentialName(host)
	}
	if credentialName(host) {
		return "", false, true
	}
	if !hostLooksRemote && !strings.Contains(path, "/") {
		return "", false, false
	}
	return remote, true, true
}

func looksRemoteLike(value string) bool {
	if strings.Contains(value, "://") || strings.Contains(value, "::") ||
		strings.Contains(value, "@") {
		return true
	}
	separator := strings.IndexByte(value, ':')
	return separator > 0 && credentialName(value[:separator])
}

func credentialName(value string) bool {
	value = strings.ToLower(value)
	for _, marker := range []string{"auth", "credential", "oauth", "password", "secret", "token"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func isASCIILetter(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func isSchemeCharacter(character byte) bool {
	return isASCIILetter(character) || character >= '0' && character <= '9' ||
		character == '+' || character == '-' || character == '.'
}

// WorktreeAdd creates a new worktree at dir on a new branch, started from startPoint.
func WorktreeAdd(repo, dir, branch, startPoint string) error {
	out, err := boundedCombinedOutput(
		exec.Command("git", "-C", repo, "worktree", "add", dir, "-b", branch, startPoint),
	)
	if err != nil {
		return gitCommandError("git worktree add "+dir, err, out)
	}
	return nil
}

// IsWorktree reports whether dir is registered as a git worktree of repo.
// The returned bool is only meaningful when err is nil.
func IsWorktree(repo, dir string) (bool, error) {
	_, found, err := listedWorktreeState(repo, dir)
	return found, err
}

// canonPath resolves symlinks so paths from git (which reports real paths, e.g.
// /private/var on macOS) compare equal to relay's constructed paths (/var).
// Falls back to Clean when the path does not exist on disk.
func canonPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	cleaned := filepath.Clean(p)
	current := cleaned
	var suffix []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return cleaned
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			continue
		}
		for i := len(suffix) - 1; i >= 0; i-- {
			resolved = filepath.Join(resolved, suffix[i])
		}
		return resolved
	}
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
	info, statErr := os.Lstat(dir)
	if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect worktree %s: %w", dir, statErr)
	}
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("worktree path %s is a symlink", dir)
	}
	registered, err := IsWorktree(repo, dir)
	if err != nil {
		return err
	}
	if !registered {
		if statErr == nil {
			return fmt.Errorf("worktree path %s exists but is not registered in %s", dir, repo)
		}
		if out, pruneErr := boundedCombinedOutput(
			exec.Command("git", "-C", repo, "worktree", "prune"),
		); pruneErr != nil {
			return gitCommandError("git worktree prune", pruneErr, out)
		}
		return nil
	}
	return removeRegisteredWorktree(repo, dir, force)
}

// WorktreeRemoveAt removes a registered worktree only when its identity still
// matches the caller's proof immediately before invoking Git.
func WorktreeRemoveAt(repo, dir string, expected WorktreeState, force bool) error {
	current, found, err := RegisteredWorktreeState(repo, dir)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("worktree path %s is no longer registered in %s", dir, repo)
	}
	if current != expected {
		return fmt.Errorf(
			"worktree path %s changed before removal: current HEAD %s branch %q detached %t, "+
				"want HEAD %s branch %q detached %t",
			dir, current.Head, current.Branch, current.Detached,
			expected.Head, expected.Branch, expected.Detached,
		)
	}
	return removeRegisteredWorktree(repo, dir, force)
}

// WorktreeReclaim removes a registered worktree or an unregistered direct
// child of the repository's .worktrees directory. The latter is reserved for
// Relay's interrupted-project setup recovery, where the caller has already
// applied its clean-worktree and confirmation policy.
func WorktreeReclaim(repo, dir string, force bool) (returnErr error) {
	root, targetName, targetExists, targetSymlink, err := openRelayWorktreeTarget(repo, dir)
	if err != nil {
		return err
	}
	if root != nil {
		defer func() {
			returnErr = errors.Join(returnErr, root.Close())
		}()
	}
	if !targetExists {
		return nil
	}
	if targetSymlink {
		if err := root.Remove(targetName); err != nil {
			return fmt.Errorf("remove Relay worktree symlink %s: %w", dir, err)
		}
	} else {
		registered, err := IsWorktree(repo, dir)
		if err != nil {
			return err
		}
		if registered {
			return removeRegisteredWorktree(repo, dir, force)
		}
		if err := root.RemoveAll(targetName); err != nil {
			return fmt.Errorf("remove Relay worktree path %s: %w", dir, err)
		}
	}
	if out, err := boundedCombinedOutput(
		exec.Command("git", "-C", repo, "worktree", "prune"),
	); err != nil {
		return gitCommandError("git worktree prune", err, out)
	}
	return nil
}

func openRelayWorktreeTarget(repo, dir string) (*os.Root, string, bool, bool, error) {
	rootPath, err := filepath.Abs(filepath.Join(repo, ".worktrees"))
	if err != nil {
		return nil, "", false, false, fmt.Errorf("resolve Relay worktree root: %w", err)
	}
	targetPath, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", false, false, fmt.Errorf("resolve worktree path %s: %w", dir, err)
	}
	rel, err := filepath.Rel(rootPath, targetPath)
	if err != nil || filepath.IsAbs(rel) || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.Dir(rel) != "." {
		return nil, "", false, false, fmt.Errorf(
			"refuse to reclaim worktree path outside %s: %s", rootPath, dir,
		)
	}

	rootInfo, err := os.Lstat(rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, rel, false, false, nil
		}
		return nil, "", false, false, fmt.Errorf("inspect Relay worktree root %s: %w", rootPath, err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, "", false, false, fmt.Errorf(
			"refuse to reclaim through symlinked Relay worktree root %s", rootPath,
		)
	}
	if !rootInfo.IsDir() {
		return nil, "", false, false, fmt.Errorf("relay worktree root %s is not a directory", rootPath)
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, "", false, false, fmt.Errorf("open Relay worktree root %s: %w", rootPath, err)
	}
	openedInfo, err := root.Stat(".")
	if err != nil {
		closeErr := root.Close()
		return nil, "", false, false, errors.Join(
			fmt.Errorf("verify Relay worktree root %s: %w", rootPath, err),
			closeErr,
		)
	}
	if !os.SameFile(rootInfo, openedInfo) {
		closeErr := root.Close()
		return nil, "", false, false, errors.Join(
			fmt.Errorf("refuse to reclaim through changed Relay worktree root %s", rootPath),
			closeErr,
		)
	}
	targetInfo, err := root.Lstat(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return root, rel, false, false, nil
		}
		closeErr := root.Close()
		return nil, "", false, false, errors.Join(
			fmt.Errorf("inspect Relay worktree path %s: %w", dir, err),
			closeErr,
		)
	}
	return root, rel, true, targetInfo.Mode()&os.ModeSymlink != 0, nil
}

func removeRegisteredWorktree(repo, dir string, force bool) error {
	args := []string{"-C", repo, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, dir)
	out, err := boundedCombinedOutput(exec.Command("git", args...))
	if err != nil {
		return gitCommandError("git worktree remove "+dir, err, out)
	}
	return nil
}
