// Package patrol observes Relay programs and rings their live CTO sessions
// without mutating program, project, git, or mailbox state.
package patrol

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

// SchemaVersion identifies the patrol runtime JSON contract.
const SchemaVersion = "relay.patrol.v1"

// Status is the patrol process lifecycle state.
type Status string

// Patrol lifecycle states.
const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	StatusFailed  Status = "failed"
)

// Reason is one stable explanation for an attentive patrol cadence.
type Reason struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// State is the runtime record stored outside program directories.
type State struct {
	Schema               string   `json:"schema"`
	Version              int      `json:"version"`
	ProgramSlug          string   `json:"program_slug"`
	PID                  int      `json:"pid"`
	RelayVersion         string   `json:"relay_version"`
	Status               Status   `json:"status"`
	StartedAt            string   `json:"started_at"`
	LastTickAt           string   `json:"last_tick_at"`
	NextTickAt           string   `json:"next_tick_at"`
	DelaySeconds         int64    `json:"delay_seconds"`
	Reasons              []Reason `json:"reasons"`
	AttentionFingerprint string   `json:"attention_fingerprint"`
	LastNotifiedAt       string   `json:"last_notified_at"`
	CTOPresent           bool     `json:"cto_present"`
	LastTurnStatus       string   `json:"last_turn_status"`
	LastTurnSessionID    string   `json:"last_turn_session_id"`
	LastTurnLogPath      string   `json:"last_turn_log_path"`
	LastTurnStartedAt    string   `json:"last_turn_started_at"`
	LastTurnEndedAt      string   `json:"last_turn_ended_at"`
	LastTurnFingerprint  string   `json:"last_turn_fingerprint"`
	LastTurnError        string   `json:"last_turn_error"`
	TurnFailures         int      `json:"turn_failures"`
	DoorbellSuppressed   bool     `json:"doorbell_suppressed"`
	ConsecutiveErrors    int      `json:"consecutive_errors"`
	Error                string   `json:"error"`
	Warning              string   `json:"warning"`
	StopReason           string   `json:"stop_reason"`
	UpdatedAt            string   `json:"updated_at"`
}

// RuntimeDir returns ~/.relay/run/<slug>.
func RuntimeDir(slug string) string {
	return filepath.Join(program.RelayDir(), "run", slug)
}

// StatePath returns the patrol state path for slug.
func StatePath(slug string) string {
	return filepath.Join(RuntimeDir(slug), "patrol.json")
}

// LockPath returns the lifetime-held patrol lock path for slug.
func LockPath(slug string) string {
	return filepath.Join(RuntimeDir(slug), "patrol.lock")
}

// ReadState reads one patrol runtime record.
func ReadState(slug string) (State, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return State{}, fmt.Errorf("patrol program slug: %w", err)
	}
	path := StatePath(slug)
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, fmt.Errorf("read patrol state %s: %w", path, err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("parse patrol state %s: %w", path, err)
	}
	state.Reasons = nonNilReasons(state.Reasons)
	return state, nil
}

// WriteState atomically replaces one patrol runtime record.
func WriteState(state State) error {
	if err := project.ValidateSlug(state.ProgramSlug); err != nil {
		return fmt.Errorf("patrol program slug: %w", err)
	}
	state.Reasons = nonNilReasons(state.Reasons)
	sort.Slice(state.Reasons, func(i, j int) bool {
		if state.Reasons[i].Code == state.Reasons[j].Code {
			return state.Reasons[i].Text < state.Reasons[j].Text
		}
		return state.Reasons[i].Code < state.Reasons[j].Code
	})
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode patrol state for %q: %w", state.ProgramSlug, err)
	}
	data = append(data, '\n')
	dir := RuntimeDir(state.ProgramSlug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create patrol runtime directory %s: %w", dir, err)
	}
	file, err := os.CreateTemp(dir, ".patrol.json-*")
	if err != nil {
		return fmt.Errorf("create atomic patrol state in %s: %w", dir, err)
	}
	tempPath := file.Name()
	cleanup := func(cause error) error {
		return errors.Join(cause, file.Close(), os.Remove(tempPath))
	}
	if err := file.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod patrol state %s: %w", tempPath, cleanup(err))
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write patrol state %s: %w", tempPath, cleanup(err))
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync patrol state %s: %w", tempPath, cleanup(err))
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close patrol state %s: %w", tempPath, errors.Join(err, os.Remove(tempPath)))
	}
	if err := os.Rename(tempPath, StatePath(state.ProgramSlug)); err != nil {
		return fmt.Errorf(
			"replace patrol state %s: %w",
			StatePath(state.ProgramSlug),
			errors.Join(err, os.Remove(tempPath)),
		)
	}
	return nil
}

func nonNilReasons(reasons []Reason) []Reason {
	result := append([]Reason(nil), reasons...)
	if result == nil {
		return []Reason{}
	}
	return result
}
