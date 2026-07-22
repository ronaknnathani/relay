package generate

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ronaknnathani/relay/internal/agent"
)

// Generate renders the workflow source at src into a package for agent a,
// rooted at out. Each supported agent has its own renderer; an unsupported
// agent returns an error until its renderer lands.
func Generate(a agent.Agent, src, out string) error {
	source, err := LoadSource(src)
	if err != nil {
		return err
	}
	switch a.Name() {
	case "claude":
		return renderClaude(a, source, out)
	case "codex":
		return renderCodex(a, source, out)
	case "copilot":
		return renderCopilot(a, source, out)
	default:
		return fmt.Errorf("generate: agent %q not supported yet", a.Name())
	}
}

// renderClaude emits the Claude plugin package: the plugin manifest plus each
// entry at its on-disk location. Skill bodies go to skills/<name>/SKILL.md;
// neutral directives in each body are rendered using the agent's capabilities.
func renderClaude(a agent.Agent, src *Source, out string) error {
	caps := a.Capabilities()
	if err := writeFile(filepath.Join(out, ".claude-plugin", "plugin.json"), src.PluginManifest); err != nil {
		return err
	}
	for _, e := range src.Entries {
		skillDir := filepath.Join(out, "skills", e.Name)
		if err := writeFile(filepath.Join(skillDir, "SKILL.md"), renderBody(e.Body, caps)); err != nil {
			return err
		}
		for rel, data := range e.Bundled {
			if err := writeFile(filepath.Join(skillDir, rel), data); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeFile creates parent directories and writes data to path.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// writeFileMode creates parent directories and writes data to path with mode.
func writeFileMode(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}
