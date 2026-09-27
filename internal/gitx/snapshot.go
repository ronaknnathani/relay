package gitx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// RepoSnapshot identifies one exact repository state and its change size.
type RepoSnapshot struct {
	BaseSHA      string `json:"base_sha"`
	BaseTipSHA   string `json:"base_tip_sha"`
	HeadSHA      string `json:"head_sha"`
	Fingerprint  string `json:"fingerprint"`
	FileCount    int    `json:"file_count"`
	ChangedLines int    `json:"changed_lines"`
}

var snapshotStat = os.Stat

// SnapshotBaseRef returns the current base branch ref used for change sizing
// and freshness. The immutable start SHA is only a fallback when no base ref
// can be resolved.
func SnapshotBaseRef(repo, baseBranch, startSHA string) string {
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "HEAD" {
		if strings.TrimSpace(startSHA) != "" {
			return startSHA
		}
		return "HEAD"
	}
	if baseBranch != "" {
		if HasOrigin(repo) && !strings.Contains(baseBranch, "/") {
			remote := "origin/" + baseBranch
			if RevParse(repo, remote) != "" {
				return remote
			}
		}
		if RevParse(repo, baseBranch) != "" {
			return baseBranch
		}
	}
	if strings.TrimSpace(startSHA) != "" {
		return startSHA
	}
	return "HEAD"
}

// Snapshot fingerprints all committed, staged, unstaged, and untracked work
// relative to baseRef.
func Snapshot(repo, baseRef string) (RepoSnapshot, error) {
	root, err := snapshotGitOutput(repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("locate repository %s: %w", repo, err)
	}
	root = strings.TrimSpace(root)
	headSHA, err := snapshotGitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("resolve HEAD in %s: %w", root, err)
	}
	if strings.TrimSpace(baseRef) == "" {
		baseRef = "HEAD"
	}
	baseTipSHA, err := snapshotGitOutput(root, "rev-parse", baseRef)
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("resolve base tip %q in %s: %w", baseRef, root, err)
	}
	baseTipSHA = strings.TrimSpace(baseTipSHA)
	baseSHA, err := snapshotGitOutput(root, "merge-base", "HEAD", baseTipSHA)
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("resolve base %q in %s: %w", baseRef, root, err)
	}
	baseSHA = strings.TrimSpace(baseSHA)
	headSHA = strings.TrimSpace(headSHA)

	diff, err := gitBytes(root, "diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", "--binary", "--full-index", baseSHA, "--")
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("read tracked diff in %s: %w", root, err)
	}
	numstat, err := snapshotGitOutput(root, "diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", "--numstat", baseSHA, "--")
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("measure tracked diff in %s: %w", root, err)
	}
	names, err := gitBytes(root, "diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", "--name-only", "-z", baseSHA, "--")
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("list tracked changes in %s: %w", root, err)
	}
	untrackedRaw, err := gitBytes(root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("list untracked files in %s: %w", root, err)
	}

	trackedCount := len(splitNUL(names))
	changedLines, err := parseNumstat(numstat)
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("measure tracked diff in %s: %w", root, err)
	}

	untracked := splitNUL(untrackedRaw)
	sort.Strings(untracked)
	hash := sha256.New()
	hash.Write([]byte(
		"base\x00" + baseSHA + "\x00base-tip\x00" + baseTipSHA +
			"\x00head\x00" + headSHA + "\x00tracked\x00",
	))
	hash.Write(diff)
	if err := writeTrackedGitlinkDigests(hash, root, make(map[string]bool)); err != nil {
		return RepoSnapshot{}, err
	}
	for _, relative := range untracked {
		file, err := readUntracked(root, relative)
		if err != nil {
			return RepoSnapshot{}, err
		}
		changedLines += file.changedLines
		hash.Write([]byte("\x00untracked\x00" + relative + "\x00"))
		hash.Write([]byte(file.kind))
		hash.Write([]byte{0})
		contentDigest := sha256.Sum256(file.content)
		hash.Write(contentDigest[:])
	}

	return RepoSnapshot{
		BaseSHA: baseSHA, BaseTipSHA: baseTipSHA, HeadSHA: headSHA,
		Fingerprint: hex.EncodeToString(hash.Sum(nil)),
		FileCount:   trackedCount + len(untracked), ChangedLines: changedLines,
	}, nil
}

func snapshotGitOutput(repo string, args ...string) (string, error) {
	output, err := gitBytes(repo, args...)
	return string(output), err
}

func gitBytes(repo string, args ...string) ([]byte, error) {
	command := snapshotGitCommand(repo, args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func splitNUL(data []byte) []string {
	parts := bytes.Split(data, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			result = append(result, string(part))
		}
	}
	return result
}

func parseNumstat(output string) (int, error) {
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			return 0, fmt.Errorf("unexpected numstat line %q", line)
		}
		for _, field := range fields[:2] {
			if field == "-" {
				total++
				continue
			}
			count, err := strconv.Atoi(field)
			if err != nil {
				return 0, fmt.Errorf("parse numstat count %q: %w", field, err)
			}
			total += count
		}
	}
	return total, nil
}

type untrackedFile struct {
	kind         string
	content      []byte
	changedLines int
}

func readUntracked(root, relative string) (untrackedFile, error) {
	path := filepath.Join(root, filepath.FromSlash(relative))
	if !pathWithin(path, root) {
		return untrackedFile{}, fmt.Errorf("untracked path %q escapes repository %s", relative, root)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return untrackedFile{}, fmt.Errorf("inspect untracked file %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return untrackedFile{}, fmt.Errorf("read untracked symlink %s: %w", path, err)
		}
		content := []byte(target)
		return untrackedFile{
			kind: "symlink", content: content, changedLines: contentLineCount(content),
		}, nil
	}
	if info.IsDir() {
		top, err := snapshotGitOutput(path, "rev-parse", "--show-toplevel")
		topInfo, topErr := snapshotStat(strings.TrimSpace(top))
		pathInfo, pathErr := snapshotStat(path)
		if err != nil || topErr != nil || pathErr != nil || !os.SameFile(topInfo, pathInfo) {
			return untrackedFile{}, fmt.Errorf("untracked path %s is not a nested Git repository", path)
		}
		digest, err := nestedRepositoryDigest(path)
		if err != nil {
			return untrackedFile{}, err
		}
		return untrackedFile{
			kind: "nested-git-repository", content: digest,
		}, nil
	}
	if !info.Mode().IsRegular() {
		return untrackedFile{}, fmt.Errorf("untracked path %s is not a regular file", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return untrackedFile{}, fmt.Errorf("read untracked file %s: %w", path, err)
	}
	kind := "regular"
	if info.Mode().Perm()&0o111 != 0 {
		kind = "regular-executable"
	}
	return untrackedFile{kind: kind, content: content, changedLines: contentLineCount(content)}, nil
}

func nestedRepositoryDigest(repo string) ([]byte, error) {
	return nestedRepositoryDigestVisited(repo, make(map[string]bool))
}

func nestedRepositoryDigestVisited(repo string, visited map[string]bool) ([]byte, error) {
	canonical, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return nil, fmt.Errorf("resolve nested Git repository %s: %w", repo, err)
	}
	if visited[canonical] {
		return nil, fmt.Errorf("nested Git repository cycle at %s", repo)
	}
	visited[canonical] = true
	defer delete(visited, canonical)

	head, err := snapshotGitOutput(repo, "rev-parse", "--verify", "HEAD")
	if err != nil {
		if _, symbolicErr := snapshotGitOutput(repo, "symbolic-ref", "-q", "HEAD"); symbolicErr != nil {
			return nil, fmt.Errorf("resolve nested Git HEAD %s: %w", repo, err)
		}
		head = "unborn"
	}

	identity := sha256.New()
	identity.Write([]byte("head\x00" + strings.TrimSpace(head) + "\x00index\x00"))
	if err := writeGitOutput(identity, repo, "ls-files", "--stage", "-z"); err != nil {
		return nil, fmt.Errorf("read nested Git index %s: %w", repo, err)
	}
	identity.Write([]byte("\x00worktree\x00"))
	if err := writeGitOutput(
		identity,
		repo,
		"diff",
		"--no-ext-diff",
		"--no-textconv",
		"--ignore-submodules=none",
		"--binary",
		"--full-index",
		"--",
	); err != nil {
		return nil, fmt.Errorf("read nested Git worktree %s: %w", repo, err)
	}
	if err := writeTrackedGitlinkDigests(identity, repo, visited); err != nil {
		return nil, err
	}

	untrackedRaw, err := gitBytes(repo, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("list nested Git untracked files %s: %w", repo, err)
	}
	untracked := splitNUL(untrackedRaw)
	sort.Strings(untracked)
	for _, relative := range untracked {
		kind, digest, err := nestedUntrackedDigest(repo, relative)
		if err != nil {
			return nil, err
		}
		identity.Write([]byte("\x00untracked\x00" + relative + "\x00" + kind + "\x00"))
		identity.Write(digest)
	}
	return []byte(hex.EncodeToString(identity.Sum(nil))), nil
}

func writeTrackedGitlinkDigests(writer io.Writer, repo string, visited map[string]bool) error {
	raw, err := gitBytes(repo, "ls-files", "--stage", "-z")
	if err != nil {
		return fmt.Errorf("list tracked Git links in %s: %w", repo, err)
	}
	for _, entry := range splitNUL(raw) {
		metadata, relative, ok := strings.Cut(entry, "\t")
		if !ok {
			return fmt.Errorf("parse tracked Git entry in %s", repo)
		}
		fields := strings.Fields(metadata)
		if len(fields) != 3 || fields[0] != "160000" || fields[2] != "0" {
			continue
		}
		path := filepath.Join(repo, filepath.FromSlash(relative))
		if !pathWithin(path, repo) {
			return fmt.Errorf("tracked Git link %q escapes repository %s", relative, repo)
		}
		if _, err := io.WriteString(
			writer,
			"\x00gitlink\x00"+relative+"\x00index\x00"+fields[1]+"\x00",
		); err != nil {
			return fmt.Errorf("hash tracked Git link %s: %w", path, err)
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect tracked Git link %s: %w", path, err)
		}
		if !info.IsDir() {
			continue
		}
		top, err := snapshotGitOutput(path, "rev-parse", "--show-toplevel")
		if err != nil {
			return fmt.Errorf("locate tracked nested Git repository %s: %w", path, err)
		}
		topInfo, topErr := snapshotStat(strings.TrimSpace(top))
		pathInfo, pathErr := snapshotStat(path)
		if topErr != nil {
			return fmt.Errorf("inspect tracked nested Git root %s: %w", strings.TrimSpace(top), topErr)
		}
		if pathErr != nil {
			return fmt.Errorf("inspect tracked Git link %s: %w", path, pathErr)
		}
		if !os.SameFile(topInfo, pathInfo) {
			continue
		}
		digest, err := nestedRepositoryDigestVisited(path, visited)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(writer, "worktree\x00"); err != nil {
			return fmt.Errorf("hash tracked Git link %s: %w", path, err)
		}
		if _, err := writer.Write(digest); err != nil {
			return fmt.Errorf("hash tracked Git link %s: %w", path, err)
		}
	}
	return nil
}

func nestedUntrackedDigest(root, relative string) (string, []byte, error) {
	path := filepath.Join(root, filepath.FromSlash(relative))
	if !pathWithin(path, root) {
		return "", nil, fmt.Errorf("nested Git untracked path %q escapes repository %s", relative, root)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, fmt.Errorf("inspect nested Git untracked file %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", nil, fmt.Errorf("read nested Git untracked symlink %s: %w", path, err)
		}
		sum := sha256.Sum256([]byte(target))
		return "symlink", sum[:], nil
	}
	if info.IsDir() {
		top, err := snapshotGitOutput(path, "rev-parse", "--show-toplevel")
		topInfo, topErr := os.Stat(strings.TrimSpace(top))
		pathInfo, pathErr := os.Stat(path)
		if err != nil || topErr != nil || pathErr != nil || !os.SameFile(topInfo, pathInfo) {
			return "", nil, fmt.Errorf("nested Git untracked path %s is not a repository", path)
		}
		digest, err := nestedRepositoryDigest(path)
		return "nested-git-repository", digest, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("nested Git untracked path %s is not a regular file", path)
	}
	objectID, err := snapshotGitOutput(root, "hash-object", "--no-filters", "--", relative)
	if err != nil {
		return "", nil, fmt.Errorf("hash nested Git untracked file %s: %w", path, err)
	}
	kind := "regular"
	if info.Mode().Perm()&0o111 != 0 {
		kind = "regular-executable"
	}
	return kind, []byte(strings.TrimSpace(objectID)), nil
}

func writeGitOutput(writer io.Writer, repo string, args ...string) error {
	command := snapshotGitCommand(repo, args...)
	var stderr bytes.Buffer
	command.Stdout = writer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf(
			"git %s: %w: %s",
			strings.Join(args, " "),
			err,
			strings.TrimSpace(stderr.String()),
		)
	}
	return nil
}

func snapshotGitCommand(repo string, args ...string) *exec.Cmd {
	command := exec.Command("git", append([]string{
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null",
		"-C", repo,
	}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_EXTERNAL_DIFF=",
		"GIT_PAGER=cat",
		"PAGER=cat",
	)
	return command
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func contentLineCount(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	count := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		count++
	}
	return count
}
