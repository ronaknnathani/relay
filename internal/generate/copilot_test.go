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
	for _, e := range src.Entries {
		body, err := transformCopilotSkill(e.Name, e.Body, caps)
		if err != nil {
			t.Fatalf("transform %s: %v", e.Name, err)
		}
		expectFile(t, out, filepath.Join("skills", e.Name, "SKILL.md"), body)
		for rel, data := range e.Bundled {
			expectFile(t, out, filepath.Join("skills", e.Name, rel), transformCopilotBundled(e.Name, data, caps))
		}
	}
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
		if strings.Contains(s, "/loop") {
			t.Errorf("%s still references the /loop command", rel)
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

	prMonitor := readFile(t, filepath.Join(out, "skills", "pr-monitor", "SKILL.md"))
	for _, snippet := range []string{
		"/every",
		"~10–15 min cadence",
		"Keep exactly **one**",
		"healthy loop per PR; restart it if it dies; record the loop id in the run's state",
		"only done — when the PR is **merged**",
		"each resume/invocation runs exactly **one** tick",
		"records `nextTickAfter`",
	} {
		if !strings.Contains(prMonitor, snippet) {
			t.Errorf("pr-monitor is missing %q", snippet)
		}
	}
}

func TestCopilotStackShipUsesNativeGoal(t *testing.T) {
	_, out := generateCopilot(t)
	body := readFile(t, filepath.Join(out, "skills", "stack-ship", "SKILL.md"))

	for _, snippet := range []string{
		`/goal Deliver the Relay stack-ship workflow for project "<slug>"`,
		"Use Relay project artifacts and `relay state` as the durable source of truth.",
		"Stop only when all acceptance criteria are met and all pull requests are merged.",
		"Never replace this native goal with the file-only fallback",
	} {
		if !strings.Contains(body, snippet) {
			t.Errorf("stack-ship is missing native goal guidance %q", snippet)
		}
	}
	if strings.Contains(body, "If `/goal` or `/loop` exists, use it") {
		t.Error("stack-ship still asks Copilot to choose a goal fallback")
	}

	for _, rel := range []string{
		"SKILL.md",
		"references/decomposition.md",
		"references/state-files.md",
	} {
		content := readFile(t, filepath.Join(out, "skills", "stack-ship", rel))
		for _, conflicting := range []string{
			"this is your `/goal`",
			"This list is your `/goal`",
			"This is `/goal`",
		} {
			if strings.Contains(content, conflicting) {
				t.Errorf("%s still treats a file or checklist as the session `/goal`: %q", rel, conflicting)
			}
		}
	}
}

func TestTransformCopilotStackShipRequiresGoalHarness(t *testing.T) {
	_, err := transformCopilotSkill("stack-ship", []byte("# Stack Ship\n\nSource drifted.\n"), mustGet(t, "copilot").Capabilities())
	if err == nil {
		t.Fatal("expected missing goal harness error")
	}
	if !strings.Contains(err.Error(), "stack-ship goal harness section") {
		t.Fatalf("error = %q, want descriptive goal harness context", err)
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
