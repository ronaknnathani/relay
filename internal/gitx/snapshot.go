package gitx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	HeadSHA      string `json:"head_sha"`
	Fingerprint  string `json:"fingerprint"`
	FileCount    int    `json:"file_count"`
	ChangedLines int    `json:"changed_lines"`
}

// Snapshot fingerprints all committed, staged, unstaged, and untracked work
// relative to baseRef.
func Snapshot(repo, baseRef string) (RepoSnapshot, error) {
	root, err := gitOutput(repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("locate repository %s: %w", repo, err)
	}
	root = strings.TrimSpace(root)
	headSHA, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("resolve HEAD in %s: %w", root, err)
	}
	if strings.TrimSpace(baseRef) == "" {
		baseRef = "HEAD"
	}
	baseSHA, err := gitOutput(root, "merge-base", "HEAD", baseRef)
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("resolve base %q in %s: %w", baseRef, root, err)
	}
	baseSHA = strings.TrimSpace(baseSHA)
	headSHA = strings.TrimSpace(headSHA)

	diff, err := gitBytes(root, "diff", "--no-ext-diff", "--binary", "--full-index", baseSHA, "--")
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("read tracked diff in %s: %w", root, err)
	}
	numstat, err := gitOutput(root, "diff", "--numstat", baseSHA, "--")
	if err != nil {
		return RepoSnapshot{}, fmt.Errorf("measure tracked diff in %s: %w", root, err)
	}
	names, err := gitBytes(root, "diff", "--name-only", "-z", baseSHA, "--")
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
	hash.Write([]byte("base\x00" + baseSHA + "\x00head\x00" + headSHA + "\x00tracked\x00"))
	hash.Write(diff)
	for _, relative := range untracked {
		file, err := readUntracked(root, relative)
		if err != nil {
			return RepoSnapshot{}, err
		}
		changedLines += contentLineCount(file.content)
		hash.Write([]byte("\x00untracked\x00" + relative + "\x00"))
		hash.Write([]byte(file.kind))
		hash.Write([]byte{0})
		hash.Write(file.content)
	}

	return RepoSnapshot{
		BaseSHA: baseSHA, HeadSHA: headSHA,
		Fingerprint: hex.EncodeToString(hash.Sum(nil)),
		FileCount:   trackedCount + len(untracked), ChangedLines: changedLines,
	}, nil
}

func gitOutput(repo string, args ...string) (string, error) {
	output, err := gitBytes(repo, args...)
	return string(output), err
}

func gitBytes(repo string, args ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-C", repo}, args...)...)
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
	kind    string
	content []byte
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
		return untrackedFile{kind: "symlink", content: []byte(target)}, nil
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
	return untrackedFile{kind: kind, content: content}, nil
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
