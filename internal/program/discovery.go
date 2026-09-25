package program

import (
	"fmt"
	"os"
	"sort"
)

// DiscoveryDiagnostic describes one program directory that could not be loaded.
type DiscoveryDiagnostic struct {
	Directory string
	Err       error
}

// Discover loads valid programs beneath dir while isolating invalid children.
func Discover(dir string) ([]Program, []DiscoveryDiagnostic, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Program{}, []DiscoveryDiagnostic{}, nil
		}
		return nil, nil, fmt.Errorf("read programs directory %s: %w", dir, err)
	}

	programs := make([]Program, 0, len(entries))
	diagnostics := make([]DiscoveryDiagnostic, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := entry.Name()
		loaded, err := Load(ManifestPath(dir, child))
		if err != nil {
			diagnostics = append(diagnostics, DiscoveryDiagnostic{Directory: child, Err: err})
			continue
		}
		if loaded.Slug != child {
			diagnostics = append(diagnostics, DiscoveryDiagnostic{
				Directory: child,
				Err:       fmt.Errorf("manifest slug %q does not match directory %q", loaded.Slug, child),
			})
			continue
		}
		programs = append(programs, loaded)
	}
	sort.Slice(programs, func(i, j int) bool {
		return programs[i].Slug < programs[j].Slug
	})
	return programs, diagnostics, nil
}
