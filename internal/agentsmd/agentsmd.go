// Package agentsmd reconciles relay's managed project instructions in AGENTS.md.
package agentsmd

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ronaknnathani/relay/internal/project"
)

const (
	stateVersion = 1
	startMarker  = "<!-- relay:agents-md:start -->"
	endMarker    = "<!-- relay:agents-md:end -->"
)

// ConflictError reports an ambiguous AGENTS.md state that relay cannot safely
// reconcile without risking user content.
type ConflictError struct {
	Path   string
	Reason string
}

func (e ConflictError) Error() string {
	return fmt.Sprintf("%s conflict: %s", e.Path, e.Reason)
}

// Apply appends or replaces relay's managed AGENTS.md block and persists the
// exact fragment in the project manifest for deterministic cleanup.
func Apply(worktree, projectDir, systemPrompt string) error {
	manifestPath, m, err := loadManifest(projectDir)
	if err != nil {
		return err
	}

	agentsPath := filepath.Join(worktree, "AGENTS.md")
	data, existed, err := readOptionalFile(agentsPath)
	if err != nil {
		return err
	}

	var oldFragment []byte
	if m.AgentsMD != nil {
		oldFragment, err = decodeRelayFragment(*m.AgentsMD)
		if err != nil {
			return err
		}
	}

	newFragment := buildFragment(separatorFor(data), systemPrompt)
	out := append([]byte(nil), data...)
	if oldFragment == nil {
		if hasRelayMarkers(data) {
			return conflict(agentsPath, "relay markers exist without project state")
		}
		out = append(out, newFragment...)
		m.AgentsMD = &project.AgentsMDState{
			Version:            stateVersion,
			Existed:            existed,
			OriginalContentB64: encode(data),
			RelayFragmentB64:   encode(newFragment),
		}
	} else {
		count := bytes.Count(data, oldFragment)
		switch {
		case count == 1:
			newFragment = buildFragment(fragmentSeparator(oldFragment), systemPrompt)
			out = bytes.Replace(data, oldFragment, newFragment, 1)
		case count == 0 && !hasRelayMarkers(data):
			out = append(out, newFragment...)
		case count == 0:
			return conflict(agentsPath, "relay markers do not match persisted project state")
		default:
			return conflict(agentsPath, "persisted relay fragment appears more than once")
		}
		m.AgentsMD.RelayFragmentB64 = encode(newFragment)
	}

	if err := os.WriteFile(agentsPath, out, 0644); err != nil {
		return fmt.Errorf("write AGENTS.md %s: %w", agentsPath, err)
	}
	if err := project.Save(manifestPath, m); err != nil {
		return fmt.Errorf("save AGENTS.md state: %w", err)
	}
	return nil
}

// Cleanup removes relay's persisted AGENTS.md fragment, preserving bytes outside
// that managed block and clearing manifest state after successful cleanup.
func Cleanup(worktree, projectDir string) error {
	manifestPath, m, err := loadManifest(projectDir)
	if err != nil {
		return err
	}
	if m.AgentsMD == nil {
		return nil
	}

	agentsPath := filepath.Join(worktree, "AGENTS.md")
	data, existed, err := readOptionalFile(agentsPath)
	if err != nil {
		return err
	}
	if !existed {
		if m.AgentsMD.Existed {
			return conflict(agentsPath, "pre-existing AGENTS.md is missing")
		}
		m.AgentsMD = nil
		if err := project.Save(manifestPath, m); err != nil {
			return fmt.Errorf("save AGENTS.md state: %w", err)
		}
		return nil
	}

	oldFragment, err := decodeRelayFragment(*m.AgentsMD)
	if err != nil {
		return err
	}
	count := bytes.Count(data, oldFragment)
	switch {
	case count == 1:
		out := bytes.Replace(data, oldFragment, nil, 1)
		if len(out) == 0 && !m.AgentsMD.Existed {
			if err := os.Remove(agentsPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove AGENTS.md %s: %w", agentsPath, err)
			}
		} else if err := os.WriteFile(agentsPath, out, 0644); err != nil {
			return fmt.Errorf("write AGENTS.md %s: %w", agentsPath, err)
		}
	case count == 0 && !hasRelayMarkers(data):
		// The managed fragment is already gone; leave user bytes untouched.
	case count == 0:
		return conflict(agentsPath, "relay markers do not match persisted project state")
	default:
		return conflict(agentsPath, "persisted relay fragment appears more than once")
	}

	m.AgentsMD = nil
	if err := project.Save(manifestPath, m); err != nil {
		return fmt.Errorf("save AGENTS.md state: %w", err)
	}
	return nil
}

func loadManifest(projectDir string) (string, project.Manifest, error) {
	if projectDir == "" {
		return "", project.Manifest{}, fmt.Errorf("project dir required for AGENTS.md state")
	}
	manifestPath := filepath.Join(projectDir, "manifest.json")
	m, err := project.Load(manifestPath)
	if err != nil {
		return "", project.Manifest{}, fmt.Errorf("load AGENTS.md state manifest %s: %w", manifestPath, err)
	}
	return manifestPath, m, nil
}

func readOptionalFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("read AGENTS.md %s: %w", path, err)
}

func buildFragment(separator []byte, systemPrompt string) []byte {
	var b bytes.Buffer
	b.Write(separator)
	b.WriteString(startMarker)
	b.WriteByte('\n')
	b.WriteString("# relay\n\n")
	b.WriteString(systemPrompt)
	b.WriteByte('\n')
	b.WriteString(endMarker)
	b.WriteByte('\n')
	return b.Bytes()
}

func separatorFor(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	if bytes.HasSuffix(data, []byte("\n")) {
		return []byte("\n")
	}
	return []byte("\n\n")
}

func fragmentSeparator(fragment []byte) []byte {
	idx := bytes.Index(fragment, []byte(startMarker))
	if idx <= 0 {
		return nil
	}
	return append([]byte(nil), fragment[:idx]...)
}

func decodeRelayFragment(state project.AgentsMDState) ([]byte, error) {
	if state.Version != stateVersion {
		return nil, fmt.Errorf("unsupported AGENTS.md state version %d", state.Version)
	}
	fragment, err := base64.StdEncoding.DecodeString(state.RelayFragmentB64)
	if err != nil {
		return nil, fmt.Errorf("decode AGENTS.md relay fragment: %w", err)
	}
	if len(fragment) == 0 || !hasRelayMarkers(fragment) {
		return nil, fmt.Errorf("invalid AGENTS.md relay fragment in project state")
	}
	return fragment, nil
}

func hasRelayMarkers(data []byte) bool {
	return bytes.Contains(data, []byte(startMarker)) || bytes.Contains(data, []byte(endMarker))
}

func encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func conflict(path, reason string) error {
	return ConflictError{Path: path, Reason: reason}
}
