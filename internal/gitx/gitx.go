// Package gitx wraps git CLI invocations used by the relay CLI. Every
// function returns an error describing the operation and includes git's
// stderr when available.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
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
		return fmt.Errorf(
			"branch %q is checked out in worktree %s; remove or detach that worktree before deleting it",
			branch, worktree,
		)
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

// WorkMergedTip reports whether the exact returned branch tip contains work
// beyond startSHA and is reachable from base.
func WorkMergedTip(repo, branch, base, startSHA string) (tip string, merged bool, err error) {
	if startSHA == "" {
		return "", false, nil
	}
	startExpression := strings.TrimSpace(startSHA) + "^{commit}"
	startOutput, err := exec.Command(
		"git", "-C", repo, "rev-parse", "--verify", "--end-of-options", startExpression,
	).Output()
	if err != nil {
		return "", false, fmt.Errorf(
			"%w: start_sha %q in %s does not resolve to a commit: %v",
			ErrInvalidWorkStart, startSHA, repo,
			gitOutputError("git rev-parse --verify --end-of-options "+startExpression, err),
		)
	}
	startCommit := strings.TrimSpace(string(startOutput))

	exists, err := localBranchExists(repo, branch)
	if err != nil {
		return "", false, err
	}
	if !exists {
		return "", false, nil
	}
	ref := "refs/heads/" + branch
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", ref+"^{commit}").Output()
	if err != nil {
		return "", false, gitOutputError("git rev-parse --verify "+ref+"^{commit}", err)
	}
	branchTip := strings.TrimSpace(string(out))
	_, err = exec.Command(
		"git", "-C", repo, "merge-base", "--is-ancestor", startCommit, branchTip,
	).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return branchTip, false, fmt.Errorf(
				"%w: start_sha %q resolves to %s, which is not an ancestor of branch %q",
				ErrInvalidWorkStart, startSHA, startCommit, branch,
			)
		}
		return branchTip, false, fmt.Errorf(
			"%w: verify start_sha %q against branch %q: %v",
			ErrInvalidWorkStart, startSHA, branch,
			gitOutputError("git merge-base --is-ancestor "+startCommit+" "+branchTip, err),
		)
	}
	if branchTip == startCommit {
		return branchTip, false, nil
	}
	reachable, err := CommitReachable(repo, branchTip, base)
	return branchTip, reachable, err
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
	if len(retained) > 0 && retained[0] > ' ' {
		boundary := bytes.IndexFunc(retained, func(character rune) bool {
			return character <= ' ' || strings.ContainsRune("\"'<>`", character)
		})
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
	refspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch)
	out, err := boundedCombinedOutput(exec.Command("git", "-C", repo, "fetch", "origin", refspec))
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
	scanner := newDiagnosticURLScanner(output)
	var sanitized strings.Builder
	for index := 0; index < len(output); {
		if end, ok := scanner.remoteHelperURLTokenEnd(index); ok {
			sanitized.WriteString(sanitizeRemoteHelperURL(output[index:end]))
			index = end
			continue
		}
		if end, ok := scanner.schemeURLTokenEnd(index); ok {
			sanitized.WriteString(sanitizeGitDiagnosticURL(output[index:end]))
			index = end
			continue
		}
		if end, ok := scanner.scpStyleURLTokenEnd(index); ok {
			sanitized.WriteString(sanitizeSCPStyleURL(output[index:end]))
			index = end
			continue
		}
		sanitized.WriteByte(output[index])
		index++
	}
	return sanitized.String()
}

const maxRemoteHelperDepth = 8

type diagnosticURLScanner struct {
	output           string
	nextAt           []int
	nextColon        []int
	nextHardBoundary []int
	nextHostInvalid  []int
	nextHostMarker   []int
	nextPathSlash    []int
	nextBracketColon []int
}

func newDiagnosticURLScanner(output string) diagnosticURLScanner {
	size := len(output) + 1
	scanner := diagnosticURLScanner{
		output:           output,
		nextAt:           make([]int, size),
		nextColon:        make([]int, size),
		nextHardBoundary: make([]int, size),
		nextHostInvalid:  make([]int, size),
		nextHostMarker:   make([]int, size),
		nextPathSlash:    make([]int, size),
		nextBracketColon: make([]int, size),
	}
	nextAt, nextColon := -1, -1
	nextHardBoundary, nextHostInvalid := len(output), -1
	nextHostMarker, nextPathSlash, nextBracketColon := -1, -1, -1
	for index := len(output) - 1; index >= 0; index-- {
		character := output[index]
		if character == '@' {
			nextAt = index
		}
		if character == ':' {
			nextColon = index
		}
		if character <= ' ' || strings.ContainsRune("\"<>`", rune(character)) {
			nextHardBoundary = index
		}
		if character == '/' || character == '@' {
			nextHostInvalid = index
		}
		if strings.ContainsRune("._-", rune(character)) {
			nextHostMarker = index
		}
		if character == '/' {
			nextPathSlash = index
		}
		if character == ']' && index+1 < len(output) && output[index+1] == ':' {
			nextBracketColon = index + 1
		}
		scanner.nextAt[index] = nextAt
		scanner.nextColon[index] = nextColon
		scanner.nextHardBoundary[index] = nextHardBoundary
		scanner.nextHostInvalid[index] = nextHostInvalid
		scanner.nextHostMarker[index] = nextHostMarker
		scanner.nextPathSlash[index] = nextPathSlash
		scanner.nextBracketColon[index] = nextBracketColon
	}
	return scanner
}

func (s diagnosticURLScanner) remoteHelperURLTokenEnd(start int) (int, bool) {
	if !urlTokenBoundary(s.output, start) {
		return 0, false
	}
	addressStart, ok := remoteHelperAddressStart(s.output, start)
	if !ok {
		return 0, false
	}
	for depth := 1; ; depth++ {
		nextAddressStart, nested := remoteHelperAddressStart(s.output, addressStart)
		if !nested {
			break
		}
		if depth == maxRemoteHelperDepth {
			end := scanURLTokenEnd(s.output, start, nextAddressStart)
			return end, end > nextAddressStart
		}
		addressStart = nextAddressStart
	}
	if end, ok := s.schemeURLTokenEnd(addressStart); ok {
		return end, true
	}
	return s.scpStyleURLTokenEnd(addressStart)
}

func remoteHelperAddressStart(output string, start int) (int, bool) {
	if start >= len(output) || !isASCIILetter(output[start]) {
		return 0, false
	}
	separator := start + 1
	for separator < len(output) &&
		(isSchemeCharacter(output[separator]) || output[separator] == '_') {
		separator++
	}
	if !strings.HasPrefix(output[separator:], "::") {
		return 0, false
	}
	addressStart := separator + 2
	return addressStart, addressStart < len(output)
}

func schemeURLTokenEnd(output string, start int) (int, bool) {
	return newDiagnosticURLScanner(output).schemeURLTokenEnd(start)
}

func (s diagnosticURLScanner) schemeURLTokenEnd(start int) (int, bool) {
	output := s.output
	if !urlTokenBoundary(output, start) || start >= len(output) ||
		!isASCIILetter(output[start]) {
		return 0, false
	}
	index := start + 1
	for index < len(output) && isSchemeCharacter(output[index]) {
		index++
	}
	if !strings.HasPrefix(output[index:], "://") {
		return 0, false
	}
	end := scanURLTokenEnd(output, start, index+3)
	if end == index+3 {
		return 0, false
	}
	return end, true
}

func scpStyleURLTokenEnd(output string, start int) (int, bool) {
	return newDiagnosticURLScanner(output).scpStyleURLTokenEnd(start)
}

func (s diagnosticURLScanner) scpStyleURLTokenEnd(start int) (int, bool) {
	output := s.output
	if !urlTokenBoundary(output, start) || start >= len(output) ||
		!isSCPStyleURLStart(output[start]) {
		return 0, false
	}
	tokenLimit := s.nextHardBoundary[start]
	pathStart := s.nextColon[start]
	if pathStart < 0 || pathStart >= tokenLimit {
		return 0, false
	}
	userinfoEnd := s.nextAt[start]
	if userinfoEnd < 0 || userinfoEnd >= pathStart {
		userinfoEnd = -1
	}
	hostStart := start
	if userinfoEnd >= 0 {
		hostStart = userinfoEnd + 1
	}
	if hostStart < tokenLimit && output[hostStart] == '[' {
		pathStart = s.nextBracketColon[hostStart]
		if pathStart < 0 || pathStart >= tokenLimit {
			return 0, false
		}
	}
	if pathStart <= start || pathStart+1 >= tokenLimit {
		return 0, false
	}
	if pathStart+1 < len(output) && output[pathStart+1] == ':' {
		return 0, false
	}
	if userinfoEnd >= 0 {
		if userinfoEnd == start || userinfoEnd == pathStart-1 {
			return 0, false
		}
		if anotherAt := s.nextAt[userinfoEnd+1]; anotherAt >= 0 && anotherAt < pathStart {
			return 0, false
		}
	}
	if invalid := s.nextHostInvalid[hostStart]; invalid >= 0 && invalid < pathStart {
		return 0, false
	}
	pathHasSlash := s.nextPathSlash[pathStart+1] >= 0 &&
		s.nextPathSlash[pathStart+1] < tokenLimit
	hostHasMarker := s.nextHostMarker[hostStart] >= 0 &&
		s.nextHostMarker[hostStart] < pathStart
	if userinfoEnd < 0 && !pathHasSlash && !hostHasMarker && output[hostStart] != '[' {
		return 0, false
	}
	end := scanURLTokenEnd(output, start, pathStart+1)
	if end <= pathStart+1 {
		return 0, false
	}
	return end, true
}

func isSCPStyleURLStart(character byte) bool {
	return isASCIILetter(character) ||
		character >= '0' && character <= '9' ||
		character == '[' ||
		strings.ContainsRune("-._~!$&()*+,;=%", rune(character))
}

func scanURLTokenEnd(output string, start, scanFrom int) int {
	wrappedInSingleQuotes := start > 0 && output[start-1] == '\''
	for index := scanFrom; index < len(output); index++ {
		character := output[index]
		if character <= ' ' || strings.ContainsRune("\"<>`", rune(character)) {
			return trimURLTrailingProse(output, start, index)
		}
		if character == '\'' && wrappedInSingleQuotes && closesQuotedURL(output, index) {
			return index
		}
	}
	return trimURLTrailingProse(output, start, len(output))
}

func closesQuotedURL(output string, quote int) bool {
	if quote+1 == len(output) {
		return true
	}
	next := output[quote+1]
	if next <= ' ' {
		return true
	}
	return strings.ContainsRune(":,.;!?)]}", rune(next)) &&
		(quote+2 == len(output) || output[quote+2] <= ' ')
}

func trimURLTrailingProse(output string, start, end int) int {
	for end > start && strings.ContainsRune(",.;!", rune(output[end-1])) {
		end--
	}
	return end
}

func urlTokenBoundary(output string, start int) bool {
	if start == 0 {
		return true
	}
	previous := output[start-1]
	return !isASCIILetter(previous) &&
		(previous < '0' || previous > '9') &&
		previous != '_'
}

func isASCIILetter(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func isSchemeCharacter(character byte) bool {
	return isASCIILetter(character) || character >= '0' && character <= '9' ||
		character == '+' || character == '-' || character == '.'
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

func sanitizeRemoteHelperURL(rawURL string) string {
	addressStart, ok := remoteHelperAddressStart(rawURL, 0)
	if !ok {
		return rawURL
	}
	for depth := 1; ; depth++ {
		nextAddressStart, nested := remoteHelperAddressStart(rawURL, addressStart)
		if !nested {
			break
		}
		if depth == maxRemoteHelperDepth {
			return rawURL[:addressStart] + "[redacted]"
		}
		addressStart = nextAddressStart
	}
	address := rawURL[addressStart:]
	if _, ok := schemeURLTokenEnd(rawURL, addressStart); ok {
		return rawURL[:addressStart] + sanitizeGitDiagnosticURL(address)
	}
	return rawURL[:addressStart] + sanitizeSCPStyleURL(address)
}

func sanitizeSCPStyleURL(rawURL string) string {
	pathStart := scpPathStart(rawURL)
	if pathStart <= 0 || pathStart == len(rawURL)-1 {
		return rawURL
	}
	userinfoEnd := strings.LastIndexByte(rawURL[:pathStart], '@')
	path := rawURL[pathStart+1:]
	if secretStart := strings.IndexAny(path, "?#"); secretStart >= 0 {
		path = path[:secretStart]
	}
	if path == "" {
		return rawURL
	}
	cleaned := rawURL[:pathStart+1] + path
	if userinfoEnd < 0 {
		return cleaned
	}
	return "[redacted]@" + rawURL[userinfoEnd+1:pathStart+1] + path
}

func scpPathStart(value string) int {
	hostStart := 0
	if userinfoEnd := strings.IndexByte(value, '@'); userinfoEnd >= 0 {
		hostStart = userinfoEnd + 1
	}
	if hostStart < len(value) && value[hostStart] == '[' {
		if bracketEnd := strings.Index(value[hostStart:], "]:"); bracketEnd >= 0 {
			return hostStart + bracketEnd + 1
		}
		return -1
	}
	pathStart := strings.IndexByte(value[hostStart:], ':')
	if pathStart < 0 {
		return -1
	}
	return hostStart + pathStart
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
