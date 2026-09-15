package project

import (
	"encoding/json"
	"fmt"
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
		return Manifest{}, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return m, nil
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

// ManifestLoadResult reports the manifest or load error for one project directory.
type ManifestLoadResult struct {
	Name     string
	Path     string
	Manifest Manifest
	Err      error
}

// LoadAllResults reads every project directory under dir without discarding
// manifest errors. Results preserve the order returned by os.ReadDir.
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
