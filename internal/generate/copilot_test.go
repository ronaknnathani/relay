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
		expectFile(t, out, filepath.Join("skills", e.Name, "SKILL.md"), transformCopilot(e.Body, caps))
		for rel, data := range e.Bundled {
			expectFile(t, out, filepath.Join("skills", e.Name, rel), transformCopilot(data, caps))
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
	tl := readFile(t, filepath.Join(out, "skills", "tl", "SKILL.md"))
	for _, snippet := range []string{
		`relay program patrol status "$PROGRAM" --json`,
		`relay program patrol start "$PROGRAM"`,
		"payload-free doorbell to this exact live pane",
		"terminal-session control stream",
		"still idle after the grace period",
		"never focuses the pane",
		"another tech lead session",
		"never invokes\n`program tick`",
		"reload durable state\nbefore acting",
		"suppresses all further doorbells",
		"Check Relay program mail and patrol state.",
		"Managed programs run only under Herdr",
		"There is no plain-terminal fallback",
		"stop_reason",
	} {
		if !strings.Contains(tl, snippet) {
			t.Errorf("tl is missing adaptive patrol guidance %q", snippet)
		}
	}
	for _, forbidden := range []string{
		"/loop", "/every", "program worker prompt", "fresh, bounded tech lead-role session",
		"RELAY_AUTOMATED_TURN=1",
	} {
		if strings.Contains(tl, forbidden) {
			t.Errorf("tl still contains retired patrol transport text %q", forbidden)
		}
	}
	for _, forbidden := range []string{
		"Outside Herdr",
		`if [ "${HERDR_ENV:-}" = "1" ]`,
		"manual foreground command",
	} {
		if strings.Contains(tl, forbidden) {
			t.Errorf("tl still offers a non-Herdr managed fallback %q", forbidden)
		}
	}
}

func TestCopilotStackShipUsesNativeGoal(t *testing.T) {
	_, out := generateCopilot(t)
	body := readFile(t, filepath.Join(out, "skills", "stack-ship", "SKILL.md"))
	if !strings.Contains(body, "never invoke inside a CTO-managed Relay program") {
		t.Error("stack-ship is missing the CTO-program exclusion")
	}

	for _, snippet := range []string{
		`/goal <the user's requested outcome>`,
		"not instructions to run the",
		"stack-ship workflow",
		"Relay project artifacts and `relay state` remain the durable source of truth",
		"Never replace this native goal",
	} {
		if !strings.Contains(body, snippet) {
			t.Errorf("stack-ship is missing native goal guidance %q", snippet)
		}
	}
	if strings.Contains(body, "Deliver the Relay stack-ship workflow") {
		t.Error("stack-ship still uses the Relay workflow as the native goal")
	}
	if strings.Contains(body, "If `/goal` or `/loop` exists, use it") {
		t.Error("stack-ship still asks Copilot to choose a goal fallback")
	}

	for rel, want := range map[string]string{
		"SKILL.md":                    "this is your `/goal`",
		"references/decomposition.md": "This list is your `/goal`",
		"references/state-files.md":   "This is `/goal`",
	} {
		content := readFile(t, filepath.Join(out, "skills", "stack-ship", rel))
		if !strings.Contains(content, want) {
			t.Errorf("%s is missing durable /goal guidance %q", rel, want)
		}
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
