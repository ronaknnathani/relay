package project

import (
	"crypto/sha256"
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
		path := filepath.Join(projectDir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			hash.Write([]byte(name + "\x00missing\x00"))
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
		hash.Write([]byte(name + "\x00present\x00" + normalized + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
