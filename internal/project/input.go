package project

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var deliveryInputFiles = []string{"task.md", "requirements.md", "assignment.md"}

// ProjectInputRevision hashes the normalized task-side inputs that can change
// routing, exploration, or acceptance criteria without changing the worktree.
func ProjectInputRevision(projectDir string) (string, error) {
	hash := sha256.New()
	for _, name := range deliveryInputFiles {
		if err := binary.Write(hash, binary.BigEndian, uint64(len(name))); err != nil {
			return "", fmt.Errorf("hash project input name %s: %w", name, err)
		}
		hash.Write([]byte(name))
		path := filepath.Join(projectDir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			hash.Write([]byte{0})
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect project input %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("project input %s must be a regular file", path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read project input %s: %w", path, err)
		}
		normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
		normalized = strings.TrimSpace(normalized) + "\n"
		contentDigest := sha256.Sum256([]byte(normalized))
		hash.Write([]byte{1})
		hash.Write(contentDigest[:])
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
