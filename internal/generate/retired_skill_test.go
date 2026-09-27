package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredWrapperSkillsStayAbsent(t *testing.T) {
	root := repoRoot(t)
	_, out := generateClaude(t)
	for _, name := range []string{"relay-status", "relay-archive", "todo"} {
		for _, path := range []string{
			filepath.Join(root, "skills", name),
			filepath.Join(root, "skills-template", name),
			filepath.Join(out, "skills", name),
		} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("retired skill %s exists at %s: %v", name, path, err)
			}
		}
		readme := readFile(t, filepath.Join(root, "README.md"))
		for old, replacement := range map[string]string{
			"/relay-status":  "relay status",
			"/relay-archive": "relay archive",
			"/todo":          "relay todo",
		} {
			if !strings.Contains(readme, "`"+old+" <args>` -> `"+replacement+" <args>`") {
				t.Errorf("README does not map %s to %s", old, replacement)
			}
		}
	}
}
