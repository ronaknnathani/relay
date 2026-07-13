package generate

import (
	"path/filepath"
)

// PortableTemplateDir is the committed, standalone skills package generated
// from the canonical Relay source.
const PortableTemplateDir = "skills-template"

// GeneratePortableTemplate renders the Relay source into a standalone
// skills-template package: one direct skill directory per source skill.
func GeneratePortableTemplate(src, out string) error {
	source, err := LoadSource(src)
	if err != nil {
		return err
	}
	for _, e := range source.Entries {
		skillDir := filepath.Join(out, e.Name)
		if err := writeFile(filepath.Join(skillDir, "SKILL.md"), transformPortableTemplate(e.Body)); err != nil {
			return err
		}
		for rel, data := range e.Bundled {
			if err := writeFileMode(filepath.Join(skillDir, rel), transformPortableTemplate(data), e.BundledModes[rel]); err != nil {
				return err
			}
		}
	}
	return nil
}

func transformPortableTemplate(body []byte) []byte {
	out := subagentDirective.ReplaceAllFunc(body, func(m []byte) []byte {
		tier := string(subagentDirective.FindSubmatch(m)[1])
		return []byte(renderPortableSubagent(tier))
	})
	out = dropClaudeFrontmatter(out)
	out = lowercaseCompoundTools(out, portableToolNames)
	return out
}

func renderPortableSubagent(tier string) string {
	switch tier {
	case "large_context":
		return "Delegate this work to a subagent with a large-context model when the runtime supports it; otherwise do it inline"
	case "fast":
		return "Delegate this work to a fast subagent when the runtime supports it; otherwise do it inline"
	default:
		return "Delegate this work to a subagent when the runtime supports it; otherwise do it inline"
	}
}

var portableToolNames = map[string]string{
	"AskUserQuestion": "ask the user",
	"BashOutput":      "read shell command output",
	"KillBash":        "stop a shell command",
	"WebFetch":        "fetch web content",
	"WebSearch":       "search the web",
}
