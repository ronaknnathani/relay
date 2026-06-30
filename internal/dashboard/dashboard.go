// Package dashboard writes the JSON payload consumed by the HTML dashboard.
package dashboard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ronaknnathani/relay/internal/project"
)

// Data is the envelope written to dashboard-data.js and emitted by --json.
type Data struct {
	Generated string             `json:"generated"`
	Active    []project.Manifest `json:"active"`
	Archived  []project.Manifest `json:"archived"`
}

// Marshal returns the JSON-encoded envelope. Nil input slices are normalized
// to `[]` so the JSON never contains `null` for active/archived.
//
// json.MarshalIndent cannot fail on this type graph (only strings, slices,
// and pointers to int/string), so the error from the encoder is dropped.
func Marshal(active, archived []project.Manifest, now time.Time) []byte {
	if active == nil {
		active = []project.Manifest{}
	}
	if archived == nil {
		archived = []project.Manifest{}
	}
	d := Data{
		Generated: now.UTC().Format(time.RFC3339),
		Active:    active,
		Archived:  archived,
	}
	out, _ := json.MarshalIndent(d, "", "  ")
	return out
}

// Write writes the dashboard-data.js file to <repoDir>/dashboard/. Skips
// silently if repoDir is empty or the dashboard directory does not exist
// (matches existing best-effort behavior).
func Write(repoDir string, active, archived []project.Manifest) error {
	if repoDir == "" {
		return nil
	}
	dashDir := filepath.Join(repoDir, "dashboard")
	fi, err := os.Stat(dashDir)
	if err != nil || !fi.IsDir() {
		return nil
	}
	payload := Marshal(active, archived, time.Now())
	js := fmt.Sprintf("window.__DASHBOARD_DATA__ = %s;\n", string(payload))
	out := filepath.Join(dashDir, "dashboard-data.js")
	if err := os.WriteFile(out, []byte(js), 0644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	return nil
}

// RepoDirFromExe resolves the repository directory from this binary's
// install symlink. Layout: <repo>/bin/<os>/relay → <repo>. Returns "" if
// the layout doesn't match (e.g., `go run`, Homebrew, etc.).
func RepoDirFromExe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return ""
	}
	if filepath.Base(filepath.Dir(filepath.Dir(resolved))) != "bin" {
		return ""
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(resolved)))
}
