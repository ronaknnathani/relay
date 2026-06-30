package generate

import (
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
)

// update regenerates the checked-in golden package when set.
var update = flag.Bool("update", false, "update golden test fixtures")

// generateCopilot renders the root source into a temp dir for copilot.
func generateCopilot(t *testing.T) (root, out string) {
	t.Helper()
	root = repoRoot(t)
	out = t.TempDir()
	cop, err := agent.Get("copilot")
	if err != nil {
		t.Fatalf("get copilot: %v", err)
	}
	if err := Generate(cop, root, out); err != nil {
		t.Fatalf("Generate copilot: %v", err)
	}
	return root, out
}

// TestCopilotGolden regenerates the Copilot package and asserts it matches the
// checked-in golden byte-for-byte. Regenerate with `go test -run TestCopilotGolden
// -update` after an intentional change.
func TestCopilotGolden(t *testing.T) {
	_, out := generateCopilot(t)
	golden := filepath.Join(repoRoot(t), "internal", "generate", "testdata", "copilot-golden")

	if *update {
		if err := os.RemoveAll(golden); err != nil {
			t.Fatalf("clear golden: %v", err)
		}
		copyTree(t, out, golden)
		t.Log("golden updated")
		return
	}

	compareTree(t, golden, out)
}

// TestCopilotPackageInvariants asserts the Copilot-specific transforms hold,
// independent of the byte-golden, so a regression is described in plain terms.
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

	// Subagent directives render to Copilot's task mechanism, never inline. The
	// large-context tier is engaged by the adapter's --context launch flag, not as
	// task-tool prose, so the body must NOT carry a bogus --context clause.
	impl := readFile(t, filepath.Join(out, "skills", "implement", "SKILL.md"))
	if !strings.Contains(impl, "Launch a subagent (task tool)") {
		t.Errorf("implement did not render a task subagent")
	}
	if strings.Contains(impl, "--context") {
		t.Errorf("implement carries a bogus --context clause (it is a launch flag, not a task param)")
	}
	if strings.Contains(impl, "inline (no subagent available)") {
		t.Errorf("implement was downgraded to inline")
	}
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
	val := readFile(t, filepath.Join(out, "skills", "validate", "SKILL.md"))
	if strings.Contains(val, "AskUserQuestion") || !strings.Contains(val, "ask_user") {
		t.Errorf("validate did not lowercase AskUserQuestion → ask_user")
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

// compareTree asserts every file under want exists under got with identical
// bytes, and that got has no extra files.
func compareTree(t *testing.T, want, got string) {
	t.Helper()
	wantFiles := map[string]bool{}
	walkFiles(t, want, func(rel string, data []byte) {
		wantFiles[rel] = true
		g, err := os.ReadFile(filepath.Join(got, rel))
		if err != nil {
			t.Errorf("golden file %s missing from output: %v", rel, err)
			return
		}
		if string(g) != string(data) {
			t.Errorf("%s differs from golden", rel)
		}
	})
	walkFiles(t, got, func(rel string, _ []byte) {
		if !wantFiles[rel] {
			t.Errorf("output has extra file %s not in golden", rel)
		}
	})
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	walkFiles(t, src, func(rel string, data []byte) {
		p := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, data, 0644); err != nil {
			t.Fatalf("write golden %s: %v", rel, err)
		}
	})
}
