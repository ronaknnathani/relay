package generate

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ronaknnathani/relay/internal/agent"
)

// renderCopilot emits the Copilot package: a transform of the Claude package.
// Copilot reads Claude plugins but strips the plugin namespace, ignores Claude-
// only frontmatter, uses lowercase tool names, and resolves hooks differently.
// Each skill body is rewritten accordingly; the subagent directive renders to
// Copilot's task mechanism (never inline). See the harness research, copilot §.
func renderCopilot(a agent.Agent, src *Source, out string) error {
	caps := a.Capabilities()
	if err := writeFile(filepath.Join(out, ".claude-plugin", "plugin.json"), copilotManifest); err != nil {
		return err
	}
	for _, e := range src.Entries {
		skillDir := filepath.Join(out, "skills", e.Name)
		body, err := transformCopilotSkill(e.Name, e.Body, caps)
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(skillDir, "SKILL.md"), body); err != nil {
			return err
		}
		for rel, data := range e.Bundled {
			if err := writeFile(filepath.Join(skillDir, rel), transformCopilotBundled(e.Name, data, caps)); err != nil {
				return err
			}
		}
	}
	return nil
}

// copilotManifest is the Copilot-loadable plugin manifest. Copilot does not
// namespace commands, so the plugin name only identifies the package.
var copilotManifest = []byte(`{
  "name": "relay",
  "description": "AI-powered development workflow — plan, implement, ship",
  "version": "0.1.0"
}
`)

// compoundToolName matches a tool name that is a compound identifier (an
// interior uppercase letter), e.g. WebFetch or AskUserQuestion. These never
// collide with English prose, so they are safe to rewrite wherever they appear;
// single-word tool names (Read, Write, …) are left alone to avoid mangling prose.
var compoundToolName = regexp.MustCompile(`[A-Z][a-z]+[A-Z]\w*`)

// transformCopilot rewrites a Claude skill body into Copilot's dialect: render
// the subagent directive to Copilot's task mechanism, drop Claude-only
// frontmatter, lowercase compound tool names via the map, and translate the
// native recurrence command.
func transformCopilot(body []byte, caps agent.Capabilities) []byte {
	out := renderBody(body, caps)
	out = dropClaudeFrontmatter(out)
	out = lowercaseCompoundTools(out, caps.ToolNames)
	out = []byte(strings.ReplaceAll(string(out), "/loop", "/every"))
	return out
}

const neutralStackShipGoalHarness = "9. **Use the best native harness.** Detect runtime capabilities once and record them in `state.json`.\n" +
	"   If `/goal` or `/loop` exists, use it; never downgrade native primitives to a fallback because\n" +
	"   another runtime lacks them. If no native loop or approved scheduler exists, use monitor-tick mode\n" +
	"   automatically on resume and be honest that coverage is tick-based, not continuous."

const copilotStackShipGoalHarness = "9. **Use Copilot's native goal harness.** The session's launch input must set\n" +
	"   `/goal <the user's requested outcome>` using the original task, not instructions to run the stack-ship workflow.\n" +
	"   Relay project artifacts and `relay state` remain the durable source of truth for executing and resuming the workflow.\n" +
	"   Never replace this native goal with the file-only fallback.\n" +
	"   Use `/loop` for monitoring when available; otherwise use monitor-tick mode automatically on resume and be\n" +
	"   honest that coverage is tick-based, not continuous."

func transformCopilotSkill(name string, body []byte, caps agent.Capabilities) ([]byte, error) {
	if name != "stack-ship" {
		return transformCopilot(body, caps), nil
	}
	s := string(body)
	if !strings.Contains(s, neutralStackShipGoalHarness) {
		return nil, fmt.Errorf("transform Copilot stack-ship: expected stack-ship goal harness section not found")
	}
	s = strings.Replace(s, neutralStackShipGoalHarness, copilotStackShipGoalHarness, 1)
	return transformCopilotStackShipAliases(transformCopilot([]byte(s), caps)), nil
}

func transformCopilotBundled(skillName string, body []byte, caps agent.Capabilities) []byte {
	out := transformCopilot(body, caps)
	if skillName == "stack-ship" {
		out = transformCopilotStackShipAliases(out)
	}
	return out
}

func transformCopilotStackShipAliases(body []byte) []byte {
	s := strings.ReplaceAll(string(body), "your `/goal`", "the durable definition of done")
	s = strings.ReplaceAll(s, "This is `/goal`", "This is the durable definition of done")
	return []byte(s)
}

// claudeOnlyKeys are frontmatter keys that must not survive into the Copilot
// package. "model"/"color"/"argument-hint" are simply ignored by Copilot, so
// dropping them keeps the package clean. "disable-model-invocation" is the
// Claude mechanism that makes the phase skills deterministic slash-only; Copilot
// has no reliable namespaced slash invocation (Capabilities.DeterministicSlash is
// false) and launches them by prose, i.e. model invocation — so leaving the flag
// in would HIDE plan/implement/ship/etc. from Copilot entirely. It is dropped so
// those skills stay model-invocable, which is how Copilot runs the flow.
var claudeOnlyKeys = []string{"model", "color", "argument-hint", "disable-model-invocation"}

// dropClaudeFrontmatter removes Claude-only keys from a body's YAML frontmatter
// block. Bodies without frontmatter are returned unchanged.
func dropClaudeFrontmatter(body []byte) []byte {
	s := string(body)
	if !strings.HasPrefix(s, "---\n") {
		return body
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return body
	}
	var kept []string
	for _, line := range strings.Split(rest[:end], "\n") {
		drop := false
		for _, k := range claudeOnlyKeys {
			if strings.HasPrefix(line, k+":") {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, line)
		}
	}
	return []byte("---\n" + strings.Join(kept, "\n") + rest[end:])
}

// lowercaseCompoundTools rewrites compound tool names (WebFetch, AskUserQuestion)
// to their Copilot names via the map. Only mapped compound names are touched.
func lowercaseCompoundTools(body []byte, names agent.ToolNameMap) []byte {
	return compoundToolName.ReplaceAllFunc(body, func(m []byte) []byte {
		if got, ok := names[string(m)]; ok {
			return []byte(got)
		}
		return m
	})
}
