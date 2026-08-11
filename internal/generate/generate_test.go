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

func TestGeneratedRecurringCommands(t *testing.T) {
	tests := []struct {
		name      string
		generate  func(*testing.T) (string, string)
		want      string
		forbidden string
	}{
		{name: "copilot", generate: generateCopilot, want: "/every", forbidden: "/loop"},
		{name: "claude", generate: generateClaude, want: "/loop", forbidden: "/every"},
		{name: "codex", generate: generateCodex, want: "/loop", forbidden: "/every"},
	}
	files := []string{
		filepath.Join("skills", "pr-monitor", "SKILL.md"),
		filepath.Join("skills", "stack-ship", "references", "state-files.md"),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, out := tt.generate(t)
			for _, rel := range files {
				body := readFile(t, filepath.Join(out, rel))
				if !strings.Contains(body, tt.want) {
					t.Errorf("%s does not contain %q", rel, tt.want)
				}
				if strings.Contains(body, tt.forbidden) {
					t.Errorf("%s contains forbidden command %q", rel, tt.forbidden)
				}
			}
		})
	}
}

func TestStackShipFallbackIsRuntimeNeutral(t *testing.T) {
	root := repoRoot(t)
	files := []string{
		filepath.Join("skills", "stack-ship", "SKILL.md"),
		filepath.Join("skills", "stack-ship", "references", "state-files.md"),
		filepath.Join("skills", "stack-ship", "references", "guardrails.md"),
	}
	forbidden := []string{
		"use Copilot monitor",
		"example in Copilot",
		"next Copilot tick",
		"runtime is Copilot without native loop support",
		"use Copilot\n**monitor-tick mode**",
	}

	for _, rel := range files {
		body := readFile(t, filepath.Join(root, rel))
		for _, phrase := range forbidden {
			if strings.Contains(body, phrase) {
				t.Errorf("%s contains stale runtime-specific fallback %q", rel, phrase)
			}
		}
	}
}

func TestStackShipGoalGuidanceIsGeneratedForEveryHarness(t *testing.T) {
	for _, agentName := range []string{"claude", "copilot", "codex"} {
		t.Run(agentName, func(t *testing.T) {
			_, out := generateAgent(t, agentName)
			body := readFile(t, filepath.Join(out, "skills", "stack-ship", "SKILL.md"))
			for _, want := range []string{
				"`/goal <the user's requested outcome>`",
				"not instructions to run the",
				"stack-ship workflow",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("%s stack-ship is missing goal guidance %q", agentName, want)
				}
			}
			if strings.Contains(body, "Deliver the Relay stack-ship workflow") {
				t.Errorf("%s stack-ship uses the workflow itself as the goal", agentName)
			}
		})
	}
}

func TestDeliverPRGoalGuidanceIsGeneratedForEveryHarness(t *testing.T) {
	for _, agentName := range []string{"claude", "copilot", "codex"} {
		t.Run(agentName, func(t *testing.T) {
			_, out := generateAgent(t, agentName)
			body := readFile(t, filepath.Join(out, "skills", "deliver-pr", "SKILL.md"))
			for _, want := range []string{
				"`/goal` to the user's requested outcome",
				"execution method, not the goal",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("%s deliver-pr is missing goal guidance %q", agentName, want)
				}
			}
		})
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
