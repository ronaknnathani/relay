package generate

import (
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
)

// TestRenderSubagentClaude asserts the neutral subagent directive renders to
// Claude's Task + opus mechanism (the no-LCD machinery), validated with the one
// agent we have.
func TestRenderSubagentClaude(t *testing.T) {
	caps := (mustGet(t, "claude")).Capabilities()

	body := []byte("Step:\n{{subagent:large_context}} with this prompt:\n")
	got := string(renderBody(body, caps))
	want := `Launch a subagent (Agent tool, model: "opus")`
	if !strings.Contains(got, want) {
		t.Errorf("large_context directive: got %q, want it to contain %q", got, want)
	}
	if strings.Contains(got, "{{subagent") {
		t.Errorf("directive left unrendered: %q", got)
	}

	// The fast tier maps to a haiku Task subagent.
	fast := string(renderBody([]byte("{{subagent:fast}}"), caps))
	if !strings.Contains(fast, `Launch a subagent (Agent tool, model: "haiku")`) {
		t.Errorf("fast directive: got %q, want haiku subagent", fast)
	}

	// Without a tier, Claude still uses a Task subagent but not opus.
	plain := string(renderBody([]byte("{{subagent}}"), caps))
	if !strings.Contains(plain, `model: "default"`) {
		t.Errorf("plain subagent: got %q, want default model", plain)
	}
}

// TestRenderSubagentInlineFallback asserts the inline fallback appears ONLY for
// an agent that genuinely lacks a subagent mechanism — never for a capable one.
func TestRenderSubagentInlineFallback(t *testing.T) {
	none := agent.Capabilities{Subagents: agent.SubagentNone}
	got := string(renderBody([]byte("{{subagent:large_context}}"), none))
	if !strings.Contains(got, "inline") {
		t.Errorf("SubagentNone should fall back to inline, got %q", got)
	}

	// The capable Claude agent must NOT get the inline fallback.
	claudeOut := string(renderBody([]byte("{{subagent:large_context}}"), mustGet(t, "claude").Capabilities()))
	if strings.Contains(claudeOut, "inline") {
		t.Errorf("capable agent was downgraded to inline: %q", claudeOut)
	}

	codexOut := string(renderBody([]byte("{{subagent:large_context}}"), mustGet(t, "codex").Capabilities()))
	if strings.Contains(codexOut, "inline") || !strings.Contains(codexOut, "Launch a Codex subagent") {
		t.Errorf("Codex subagent directive rendered incorrectly: %q", codexOut)
	}
}

// TestRenderBodyNoDirectives confirms bodies without directives pass through
// unchanged, so the migrated Claude skills stay byte-for-byte.
func TestRenderBodyNoDirectives(t *testing.T) {
	in := []byte("# plan\n\nLaunch a subagent (Agent tool, model: \"opus\").\n")
	if got := renderBody(in, mustGet(t, "claude").Capabilities()); string(got) != string(in) {
		t.Errorf("body without directives changed:\n got: %q\nwant: %q", got, in)
	}
}

func mustGet(t *testing.T, name string) agent.Agent {
	t.Helper()
	a, err := agent.Get(name)
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return a
}
