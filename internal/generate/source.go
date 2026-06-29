// Package generate renders the agent-neutral source at the repo root
// (plugin.json + skills/) into a per-agent package. It reads each adapter's
// capability descriptor and emits the best mechanism that agent supports; the
// Claude and Copilot adapters exist today.
package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Entry is one invocable unit of the workflow, authored once in the neutral
// source as a skill. Body holds the full SKILL.md contents (frontmatter +
// markdown), so the renderer can emit it without reconstructing frontmatter.
// Bundled holds any sibling files under the skill directory (e.g.
// review-pr/agents/*.md), keyed by their path relative to the skill dir, so a
// self-contained skill travels with its companion prompts.
type Entry struct {
	Name    string // skill directory name, e.g. "plan" or "rebase"
	Body    []byte
	Bundled map[string][]byte
}

// Source is the parsed root source tree: the plugin manifest plus every skill.
type Source struct {
	PluginManifest []byte
	Entries        []Entry
}

// pluginManifestFile is the source file holding the verbatim plugin manifest.
const pluginManifestFile = "plugin.json"

// LoadSource reads the workflow source rooted at dir. The layout is
// self-describing: plugin.json is the plugin manifest and skills/<name>/SKILL.md
// are the skill entries. Entries are returned sorted by name for deterministic
// output.
func LoadSource(dir string) (*Source, error) {
	manifest, err := os.ReadFile(filepath.Join(dir, pluginManifestFile))
	if err != nil {
		return nil, fmt.Errorf("read plugin manifest: %w", err)
	}

	entries, err := loadSkills(filepath.Join(dir, "skills"))
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return &Source{PluginManifest: manifest, Entries: entries}, nil
}

// loadSkills reads skills/<name>/SKILL.md. A missing directory yields no entries.
func loadSkills(dir string) ([]Entry, error) {
	dirs, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}
	var entries []Entry
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		skillDir := filepath.Join(dir, d.Name())
		body, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
		if err != nil {
			return nil, fmt.Errorf("read skill %s: %w", d.Name(), err)
		}
		bundled, err := loadBundled(skillDir)
		if err != nil {
			return nil, fmt.Errorf("read skill %s: %w", d.Name(), err)
		}
		entries = append(entries, Entry{Name: d.Name(), Body: body, Bundled: bundled})
	}
	return entries, nil
}

// loadBundled reads every file under a skill directory except its SKILL.md,
// keyed by path relative to the skill dir, so companion files (e.g. agent
// prompts) ship with the skill. Returns nil when there are none.
func loadBundled(skillDir string) (map[string][]byte, error) {
	var bundled map[string][]byte
	err := filepath.WalkDir(skillDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path == filepath.Join(skillDir, "SKILL.md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(skillDir, path)
		if err != nil {
			return err
		}
		if bundled == nil {
			bundled = make(map[string][]byte)
		}
		bundled[rel] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return bundled, nil
}
