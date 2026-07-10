package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func generateCodex(t *testing.T) (root, out string) {
	return generateAgent(t, "codex")
}

// TestCodexPackageMatchesSource asserts Codex output is a deterministic
// transform of the real source tree without duplicated skill fixtures.
func TestCodexPackageMatchesSource(t *testing.T) {
	root, out := generateCodex(t)
	src := loadSourceForTest(t, root)

	caps := mustGet(t, "codex").Capabilities()
	assertRenderedSkills(t, out, src, func(body []byte) []byte {
		return transformCodex(body, caps)
	})
	assertNoUnexpectedFiles(t, out, expectedSkillFiles(src))
}

func TestCodexPackageInvariants(t *testing.T) {
	_, out := generateCodex(t)

	if _, err := os.Stat(filepath.Join(out, ".claude-plugin", "plugin.json")); !os.IsNotExist(err) {
		t.Errorf("Codex package should not generate a Claude plugin manifest, stat err = %v", err)
	}

	walkFiles(t, out, func(rel string, data []byte) {
		s := string(data)
		if strings.Contains(s, "{{subagent") {
			t.Errorf("%s still has an unrendered subagent directive", rel)
		}
		if strings.Contains(s, "AskUserQuestion") {
			t.Errorf("%s still has Claude-only AskUserQuestion prose", rel)
		}
	})

	todo := readFile(t, filepath.Join(out, "skills", "todo", "SKILL.md"))
	if !strings.Contains(todo, "Launch a Codex subagent") {
		t.Errorf("todo did not render Codex subagent instructions")
	}

	plan := readFile(t, filepath.Join(out, "skills", "plan", "SKILL.md"))
	if fm, _ := frontmatter(plan); strings.Contains(fm, "argument-hint") {
		t.Errorf("plan kept argument-hint frontmatter")
	}
	if fm, _ := frontmatter(plan); strings.Contains(fm, "disable-model-invocation") {
		t.Errorf("plan kept disable-model-invocation frontmatter")
	}
}
