package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// RelayDir returns ~/.relay. The directory may not exist yet; callers
// that need it created should MkdirAll first.
func RelayDir() string {
	return filepath.Join(os.Getenv("HOME"), ".relay")
}

// ProjectsDir returns ~/.relay/projects.
func ProjectsDir() string { return filepath.Join(RelayDir(), "projects") }

// ActiveDir returns ~/.relay/projects/active.
func ActiveDir() string { return filepath.Join(ProjectsDir(), "active") }

// ArchivedDir returns ~/.relay/projects/archived.
func ArchivedDir() string { return filepath.Join(ProjectsDir(), "archived") }

// ManifestPath returns the conventional manifest path for a project under dir.
func ManifestPath(dir, slug string) string {
	return filepath.Join(dir, slug, "manifest.json")
}

// Load reads and decodes a manifest from disk.
func Load(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return m, nil
}

// LoadEffective reads a manifest and overlays colocated adaptive state when
// present. Legacy and route-less custom workflows are returned unchanged.
func LoadEffective(path string) (Manifest, error) {
	manifest, _, err := LoadEffectiveProject(path)
	return manifest, err
}

// LoadEffectiveProject reads a manifest and its colocated workflow state.
// Historical non-workflow stack state is recognized and returned as no
// workflow state so every human and machine view uses the same classification.
func LoadEffectiveProject(path string) (Manifest, *WorkflowState, error) {
	manifest, err := Load(path)
	if err != nil {
		return Manifest{}, nil, err
	}
	directorySlug := filepath.Base(filepath.Dir(path))
	if manifest.Slug != directorySlug {
		return Manifest{}, nil, fmt.Errorf(
			"manifest slug %q does not match project directory %q",
			manifest.Slug, directorySlug,
		)
	}
	state, err := LoadState(filepath.Join(filepath.Dir(path), "state.json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return manifest, nil, nil
		}
		legacy, inspectErr := legacyNonWorkflowState(filepath.Join(filepath.Dir(path), "state.json"))
		if inspectErr != nil {
			return Manifest{}, nil, inspectErr
		}
		if legacy {
			return manifest, nil, nil
		}
		return Manifest{}, nil, err
	}
	if state.Slug != manifest.Slug {
		return Manifest{}, nil, fmt.Errorf(
			"state slug %q does not match manifest slug %q",
			state.Slug, manifest.Slug,
		)
	}
	return EffectiveManifest(manifest, state), &state, nil
}

func legacyNonWorkflowState(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read state %s: %w", path, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return false, nil
	}
	if _, ok := fields["order"]; ok {
		return false, nil
	}
	_, hasGoal := fields["goalSlug"]
	_, hasFront := fields["frontPr"]
	_, hasPRs := fields["prs"]
	return hasGoal || hasFront || hasPRs, nil
}

// Save writes the manifest to disk, refreshing the Updated timestamp.
func Save(path string, m Manifest) error {
	m.Updated = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write manifest %s: %w", path, err)
	}
	return nil
}

// Find searches active then archived for a slug. Returns the manifest path
// or an error if not found.
func Find(slug string) (string, error) {
	active := ManifestPath(ActiveDir(), slug)
	if _, err := os.Stat(active); err == nil {
		return active, nil
	}
	archived := ManifestPath(ArchivedDir(), slug)
	if _, err := os.Stat(archived); err == nil {
		return archived, nil
	}
	return "", fmt.Errorf("project not found: %s", slug)
}

// FindActive returns the manifest path for one active project only.
func FindActive(slug string) (string, error) {
	if err := ValidateSlug(slug); err != nil {
		return "", err
	}
	active := ManifestPath(ActiveDir(), slug)
	if _, err := os.Stat(active); err == nil {
		return active, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("inspect active project %s: %w", active, err)
	}
	return "", fmt.Errorf("active project not found: %s", slug)
}

// FindState searches active then archived for an existing state file.
func FindState(slug string) (string, error) {
	active := StatePath(slug)
	if _, err := os.Stat(active); err == nil {
		return active, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("inspect active state %s: %w", active, err)
	}
	archived := filepath.Join(ArchivedDir(), slug, "state.json")
	if _, err := os.Stat(archived); err == nil {
		return archived, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("inspect archived state %s: %w", archived, err)
	}
	return "", fmt.Errorf("state not found %s: %w", slug, fs.ErrNotExist)
}

// ManifestLoadResult reports the manifest or load error for one project directory.
type ManifestLoadResult struct {
	Name     string
	Path     string
	Manifest Manifest
	Err      error
}

// LoadAllResults reads every project directory under dir without discarding
// manifest errors. Directories without manifest.json are intentionally ignored:
// the active store also contains coordination and incomplete directories that
// are not Relay projects. Results preserve the order returned by os.ReadDir.
func LoadAllResults(dir string) ([]ManifestLoadResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}
	var results []ManifestLoadResult
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := ManifestPath(dir, e.Name())
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		if statErr != nil {
			results = append(results, ManifestLoadResult{
				Name: e.Name(),
				Path: path,
				Err:  fmt.Errorf("inspect manifest %s: %w", path, statErr),
			})
			continue
		}
		if !info.Mode().IsRegular() {
			results = append(results, ManifestLoadResult{
				Name: e.Name(),
				Path: path,
				Err:  fmt.Errorf("inspect manifest %s: not a regular file", path),
			})
			continue
		}
		m, err := Load(path)
		results = append(results, ManifestLoadResult{
			Name:     e.Name(),
			Path:     path,
			Manifest: m,
			Err:      err,
		})
	}
	return results, nil
}

// LoadAll reads every manifest under dir. Subdirectories without a
// readable manifest are silently skipped (matches existing behavior).
func LoadAll(dir string) ([]Manifest, error) {
	results, err := LoadAllResults(dir)
	if err != nil {
		return nil, err
	}
	var manifests []Manifest
	for _, result := range results {
		if result.Err != nil {
			continue
		}
		manifests = append(manifests, result.Manifest)
	}
	return manifests, nil
}

// LoadAllEffective reads every project by its enumerated directory identity,
// returning per-project warnings without hiding healthy projects.
func LoadAllEffective(dir string) ([]Manifest, []error, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read dir %s: %w", dir, err)
	}
	var manifests []Manifest
	var warnings []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := ManifestPath(dir, entry.Name())
		manifest, err := Load(path)
		if err != nil {
			warnings = append(warnings, fmt.Errorf("load project directory %q: %w", entry.Name(), err))
			continue
		}
		if manifest.Slug != entry.Name() {
			warnings = append(warnings, fmt.Errorf(
				"load project directory %q: manifest slug %q does not match directory",
				entry.Name(), manifest.Slug,
			))
			continue
		}
		effective, err := LoadEffective(path)
		if err != nil {
			warnings = append(warnings, fmt.Errorf("load effective project %q: %w", entry.Name(), err))
			continue
		}
		manifests = append(manifests, effective)
	}
	return manifests, warnings, nil
}
