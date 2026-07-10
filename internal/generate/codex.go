package generate

import (
	"path/filepath"

	"github.com/ronaknnathani/relay/internal/agent"
)

// renderCodex emits a native Codex skill package. Codex personal skills live
// directly under ~/.codex/skills/<name>, so the generated package only needs a
// skills/ tree; setup links those skill directories into Codex's skills dir.
func renderCodex(a agent.Agent, src *Source, out string) error {
	caps := a.Capabilities()
	for _, e := range src.Entries {
		skillDir := filepath.Join(out, "skills", e.Name)
		if err := writeFile(filepath.Join(skillDir, "SKILL.md"), transformCodex(e.Body, caps)); err != nil {
			return err
		}
		for rel, data := range e.Bundled {
			if err := writeFile(filepath.Join(skillDir, rel), transformCodex(data, caps)); err != nil {
				return err
			}
		}
	}
	return nil
}

// transformCodex rewrites the neutral source into Codex's dialect: render
// relay directives, drop Claude-only frontmatter, and neutralize compound
// Claude tool names that appear in prose.
func transformCodex(body []byte, caps agent.Capabilities) []byte {
	out := renderBody(body, caps)
	out = dropClaudeFrontmatter(out)
	out = lowercaseCompoundTools(out, caps.ToolNames)
	return out
}
