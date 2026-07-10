package generate

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// generateCopilot renders the root source into a temp dir for copilot.
func generateCopilot(t *testing.T) (root, out string) {
	return generateAgent(t, "copilot")
}

// TestCopilotPackageMatchesSource asserts Copilot output is a deterministic
// transform of the real source tree without duplicated skill fixtures.
func TestCopilotPackageMatchesSource(t *testing.T) {
	root, out := generateCopilot(t)
	src := loadSourceForTest(t, root)

	expectFile(t, out, ".claude-plugin/plugin.json", copilotManifest)
	caps := mustGet(t, "copilot").Capabilities()
	assertRenderedSkills(t, out, src, func(body []byte) []byte {
		return transformCopilot(body, caps)
	})
	assertNoUnexpectedFiles(t, out, expectedSkillFiles(src, ".claude-plugin/plugin.json"))
}

// TestCopilotPackageInvariants asserts the Copilot-specific transforms hold, so
// a regression is described in plain terms.
func TestCopilotPackageInvariants(t *testing.T) {
	_, out := generateCopilot(t)

	walkFiles(t, out, func(rel string, data []byte) {
		s := string(data)
		// Forward-looking: Copilot has no namespaced slash invocation, so no
		// skill body should carry a "relay:" command namespace. This is inert
		// today (no skill uses it); stack-ship still carries deferred "/build:"
		// refs that get rewired to bare names in a later PR. The check guards
		// against a namespace being (re)introduced.
		if strings.Contains(s, "relay:") {
			t.Errorf("%s still has a relay: namespace ref", rel)
		}
		if strings.Contains(s, "CLAUDE_PLUGIN_ROOT") {
			t.Errorf("%s references CLAUDE_PLUGIN_ROOT", rel)
		}
		if strings.Contains(s, "PreCompact") {
			t.Errorf("%s mentions PreCompact", rel)
		}
	})

	// The Copilot package emits no hook file (the prime hook was removed).
	if _, err := os.Stat(filepath.Join(out, "hooks.json")); !os.IsNotExist(err) {
		t.Errorf("hooks.json should not be generated, stat err = %v", err)
	}

	// The subagent directive renders to Copilot's task mechanism, never inline.
	todo := readFile(t, filepath.Join(out, "skills", "todo", "SKILL.md"))
	if !strings.Contains(todo, "Launch a subagent (task tool) with this prompt") {
		t.Errorf("todo did not render a plain task subagent")
	}

	// Claude-only frontmatter dropped: both argument-hint AND disable-model-invocation
	// must be gone, the latter so Copilot can model-invoke the phase skill by prose.
	plan := readFile(t, filepath.Join(out, "skills", "plan", "SKILL.md"))
	if fm, _ := frontmatter(plan); strings.Contains(fm, "argument-hint") {
		t.Errorf("plan kept argument-hint frontmatter")
	}
	if fm, _ := frontmatter(plan); strings.Contains(fm, "disable-model-invocation") {
		t.Errorf("plan kept disable-model-invocation: Copilot would not be able to invoke it by prose")
	}
	ss := readFile(t, filepath.Join(out, "skills", "stack-ship", "SKILL.md"))
	if strings.Contains(ss, "AskUserQuestion") || !strings.Contains(ss, "ask_user") {
		t.Errorf("stack-ship did not lowercase AskUserQuestion → ask_user")
	}
}

func walkFiles(t *testing.T, root string, fn func(rel string, data []byte)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		fn(rel, data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
