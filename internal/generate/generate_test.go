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
	t.Helper()
	root = repoRoot(t)
	out = t.TempDir()
	claude, err := agent.Get("claude")
	if err != nil {
		t.Fatalf("get claude: %v", err)
	}
	if err := Generate(claude, root, out); err != nil {
		t.Fatalf("Generate: %v", err)
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

// TestClaudeGolden regenerates the Claude package and asserts it matches the
// checked-in golden byte-for-byte. Regenerate with `go test -run TestClaudeGolden
// -update` after an intentional change. This byte-pins the full Claude render
// (the manifest plus directive rendering for every skill), symmetric to
// TestCopilotGolden, so a generator regression is caught rather than hidden.
func TestClaudeGolden(t *testing.T) {
	_, out := generateClaude(t)
	golden := filepath.Join(repoRoot(t), "internal", "generate", "testdata", "claude-golden")

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

// stubAgent is a minimal non-Claude agent for the unsupported-agent path.
type stubAgent struct{}

func (stubAgent) Name() string                            { return "stub" }
func (stubAgent) Lookup() (string, error)                 { return "", nil }
func (stubAgent) Prepare(agent.LaunchOptions) error       { return nil }
func (stubAgent) LaunchArgs(agent.LaunchOptions) []string { return nil }
func (stubAgent) Capabilities() agent.Capabilities        { return agent.Capabilities{} }
func (stubAgent) PermissionModes() []string               { return nil }
