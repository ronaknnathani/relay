package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
)

// phaseSkills are the deterministic entry points migrated from commands.
var phaseSkills = []string{"build", "plan", "implement", "improve", "validate", "ship", "todo"}

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

// TestGenerateSkillsOnly asserts the migrated Claude package is skills-only:
// every phase entry point is present as a skill and there is no commands/ dir.
func TestGenerateSkillsOnly(t *testing.T) {
	_, out := generateClaude(t)

	if _, err := os.Stat(filepath.Join(out, "commands")); !os.IsNotExist(err) {
		t.Errorf("generated package has a commands/ dir; expected skills-only")
	}
	for _, name := range phaseSkills {
		if _, err := os.Stat(filepath.Join(out, "skills", name, "SKILL.md")); err != nil {
			t.Errorf("phase entry %q missing as a skill: %v", name, err)
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

// TestPhaseSkillsBehavioralParity asserts the migrated phase entry points keep
// Claude's behavior: each is a slash-only (disable-model-invocation) skill with
// the right name and argument-hint, the auto-chain order is intact, and the
// subagent/opus usage is preserved.
func TestPhaseSkillsBehavioralParity(t *testing.T) {
	_, out := generateClaude(t)

	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(out, "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatalf("read skill %s: %v", name, err)
		}
		return string(b)
	}

	// Every phase entry point is a deterministic, slash-only skill with its name.
	for _, name := range phaseSkills {
		body := read(name)
		fm, ok := frontmatter(body)
		if !ok {
			t.Errorf("%s: missing frontmatter", name)
			continue
		}
		if !strings.Contains(fm, "disable-model-invocation: true") {
			t.Errorf("%s: not slash-only (missing disable-model-invocation: true)", name)
		}
		if !strings.Contains(fm, "name: "+name) {
			t.Errorf("%s: frontmatter name does not match", name)
		}
		if !strings.Contains(fm, "argument-hint:") {
			t.Errorf("%s: missing argument-hint", name)
		}
	}

	// Auto-chain order: implement → improve → validate, and the ship pipeline.
	wantChains := map[string][]string{
		"plan":      {"/implement $SLUG"},
		"implement": {"/improve $SLUG"},
		"improve":   {"/validate $SLUG"},
		"validate":  {"/ship $SLUG", "/improve"},
		"ship":      {"rebase → create PR → CI → code review"},
	}
	for name, wants := range wantChains {
		body := read(name)
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("%s: auto-chain reference %q missing", name, w)
			}
		}
	}

	// Subagent + opus usage preserved in the batch phases.
	for _, name := range []string{"implement", "improve", "validate"} {
		body := read(name)
		if !strings.Contains(body, `model: "opus"`) {
			t.Errorf("%s: lost opus subagent usage", name)
		}
	}
	if !strings.Contains(read("todo"), `model: "haiku"`) {
		t.Errorf("todo: lost haiku subagent usage")
	}
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
