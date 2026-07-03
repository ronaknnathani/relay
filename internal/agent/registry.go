package agent

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ronaknnathani/relay/internal/project"
)

// DefaultName is the agent used when none is configured or requested.
const DefaultName = "claude"

var registry = map[string]Agent{}

func register(a Agent) { registry[a.Name()] = a }

func init() {
	register(claude{})
	register(copilot{})
}

// PackageDir is the stable install location for a generated agent package:
// ~/.relay/agents/<name>. The generator writes here and adapters that load a
// package by path (e.g. Copilot's --plugin-dir) read from here.
func PackageDir(name string) string {
	return filepath.Join(project.RelayDir(), "agents", name)
}

// Get resolves an agent by name. An empty name resolves to DefaultName; an
// unknown name returns an error listing the supported agents.
func Get(name string) (Agent, error) {
	if name == "" {
		name = DefaultName
	}
	a, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown agent %q (supported: %s)", name, strings.Join(Names(), ", "))
	}
	return a, nil
}

// ResolveName picks an agent name by precedence: an explicit request (CLI flag),
// then the per-project manifest value, then the configured default. Empty values
// are skipped. The result may be empty, in which case Get resolves it to DefaultName.
func ResolveName(requested, manifest, configDefault string) string {
	for _, name := range []string{requested, manifest, configDefault} {
		if name != "" {
			return name
		}
	}
	return ""
}

// Names returns the registered agent names, sorted.
func Names() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
