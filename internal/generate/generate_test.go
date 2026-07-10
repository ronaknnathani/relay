package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
)

// coreSkills are foundation skills that must always render into the package.
var coreSkills = []string{
	"explore", "clarify", "plan", "implement", "simplify", "review",
	"validate", "commit", "rebase", "open-pr", "pr-fix",
}

// repoRoot returns the module root (two levels up from internal/generate).
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// generateClaude renders the root source into a temp dir and returns it.
func generateClaude(t *testing.T) (root, out string) {
	return generateAgent(t, "claude")
}

func generateAgent(t *testing.T, name string) (root, out string) {
	t.Helper()
	root = repoRoot(t)
	out = t.TempDir()
	a, err := agent.Get(name)
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	if err := Generate(a, root, out); err != nil {
		t.Fatalf("Generate %s: %v", name, err)
	}
	return root, out
}

// TestGenerateSkillsOnly asserts the package is skills-only (no commands/ dir)
// and that every core foundation skill renders.
func TestGenerateSkillsOnly(t *testing.T) {
	_, out := generateClaude(t)

	if _, err := os.Stat(filepath.Join(out, "commands")); !os.IsNotExist(err) {
		t.Errorf("generated package has a commands/ dir; expected skills-only")
	}
	for _, name := range coreSkills {
		if _, err := os.Stat(filepath.Join(out, "skills", name, "SKILL.md")); err != nil {
			t.Errorf("core skill %q missing from package: %v", name, err)
		}
	}
}

// TestClaudePackageMatchesSource asserts the Claude renderer is a deterministic
// transform of the real source tree without duplicating every skill in testdata.
func TestClaudePackageMatchesSource(t *testing.T) {
	root, out := generateClaude(t)
	src := loadSourceForTest(t, root)

	expectFile(t, out, ".claude-plugin/plugin.json", src.PluginManifest)
	assertRenderedSkills(t, out, src, func(body []byte) []byte {
		return renderBody(body, mustGet(t, "claude").Capabilities())
	})
	assertNoUnexpectedFiles(t, out, expectedSkillFiles(src, ".claude-plugin/plugin.json"))
}

func TestGenerateUnsupportedAgent(t *testing.T) {
	if err := Generate(stubAgent{}, t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("expected error for unsupported agent")
	}
}

// frontmatter returns the YAML frontmatter block (between the first two ---
// fences) of a markdown body.
func frontmatter(body string) (string, bool) {
	if !strings.HasPrefix(body, "---\n") {
		return "", false
	}
	rest := body[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

func loadSourceForTest(t *testing.T, root string) *Source {
	t.Helper()
	src, err := LoadSource(root)
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}
	return src
}

func assertRenderedSkills(t *testing.T, out string, src *Source, transform func([]byte) []byte) {
	t.Helper()
	for _, e := range src.Entries {
		expectFile(t, out, filepath.Join("skills", e.Name, "SKILL.md"), transform(e.Body))
		for rel, data := range e.Bundled {
			expectFile(t, out, filepath.Join("skills", e.Name, rel), transform(data))
		}
	}
}

func expectFile(t *testing.T, root, rel string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read generated %s: %v", rel, err)
	}
	if string(got) != string(want) {
		t.Errorf("generated %s differs from source-derived expectation", rel)
	}
}

func expectedSkillFiles(src *Source, extra ...string) map[string]bool {
	files := map[string]bool{}
	for _, rel := range extra {
		files[filepath.Clean(rel)] = true
	}
	for _, e := range src.Entries {
		files[filepath.Join("skills", e.Name, "SKILL.md")] = true
		for rel := range e.Bundled {
			files[filepath.Join("skills", e.Name, rel)] = true
		}
	}
	return files
}

func assertNoUnexpectedFiles(t *testing.T, out string, want map[string]bool) {
	t.Helper()
	walkFiles(t, out, func(rel string, _ []byte) {
		if !want[filepath.Clean(rel)] {
			t.Errorf("output has unexpected file %s", rel)
		}
	})
}

// stubAgent is a minimal non-Claude agent for the unsupported-agent path.
type stubAgent struct{}

func (stubAgent) Name() string                            { return "stub" }
func (stubAgent) Lookup() (string, error)                 { return "", nil }
func (stubAgent) Prepare(agent.LaunchOptions) error       { return nil }
func (stubAgent) LaunchArgs(agent.LaunchOptions) []string { return nil }
func (stubAgent) Capabilities() agent.Capabilities        { return agent.Capabilities{} }
func (stubAgent) PermissionModes() []string               { return nil }
