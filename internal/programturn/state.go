// Package programturn stores the runtime record of bounded automated CTO turns.
// It lives entirely under ~/.relay/run/<program>/, outside active and archived
// program directories, so automation history never mixes with governed program
// truth.
package programturn

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

// SchemaVersion identifies the bounded-turn runtime JSON contract.
const SchemaVersion = "relay.program.turn.v1"

// HistoryLimit bounds how many recent turns are retained per program.
const HistoryLimit = 50

const stateLockTimeout = 10 * time.Second

// Status is the outcome of one bounded automated turn.
type Status string

// Bounded turn outcomes.
const (
	// StatusSucceeded means the agent process exited zero.
	StatusSucceeded Status = "succeeded"
	// StatusFailed means the agent process exited non-zero or could not start.
	StatusFailed Status = "failed"
	// StatusTimedOut means the agent exceeded the bound and its process group
	// was killed.
	StatusTimedOut Status = "timed-out"
	// StatusSkipped means no turn ran because another writer held the lock or
	// the program was not in a state that admits an automated turn.
	StatusSkipped Status = "skipped"
)

// Record is one bounded automated turn attempt.
type Record struct {
	SessionID   string `json:"session_id"`
	Fingerprint string `json:"fingerprint"`
	Status      Status `json:"status"`
	Reason      string `json:"reason,omitempty"`
	StartedAt   string `json:"started_at"`
	EndedAt     string `json:"ended_at"`
	ExitCode    int    `json:"exit_code"`
	TimedOut    bool   `json:"timed_out"`
	LogPath     string `json:"log_path"`
	Error       string `json:"error,omitempty"`
}

// State is the bounded automated turn history for one program.
type State struct {
	Schema      string   `json:"schema"`
	Version     int      `json:"version"`
	ProgramSlug string   `json:"program_slug"`
	UpdatedAt   string   `json:"updated_at"`
	Turns       []Record `json:"turns"`
}

// RuntimeDir returns ~/.relay/run/<slug>.
func RuntimeDir(slug string) string {
	return filepath.Join(program.RelayDir(), "run", slug)
}

// WriterLockPath returns the lifetime-held single-writer lock for slug. Every
// bounded automated turn holds it, so at most one automated writer acts on a
// program at a time and the kernel releases it if that writer dies.
func WriterLockPath(slug string) string {
	return filepath.Join(RuntimeDir(slug), "writer.lock")
}

// StatePath returns the bounded turn history path for slug.
func StatePath(slug string) string {
	return filepath.Join(RuntimeDir(slug), "turns.json")
}

// LogDir returns the directory holding bounded turn transcripts for slug.
func LogDir(slug string) string {
	return filepath.Join(RuntimeDir(slug), "turns")
}

// LogPath returns the combined-output transcript path for one bounded turn.
func LogPath(slug string, startedAt time.Time, sessionID string) string {
	name := startedAt.UTC().Format("20060102T150405Z") + "-" + safeName(sessionID) + ".log"
	return filepath.Join(LogDir(slug), name)
}

// Read returns the bounded turn history for slug. A missing history is empty,
// not an error.
func Read(slug string) (State, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return State{}, fmt.Errorf("bounded turn program slug: %w", err)
	}
	return readState(slug)
}

// Latest returns the newest recorded bounded turn for slug.
func Latest(slug string) (Record, bool, error) {
	state, err := Read(slug)
	if err != nil {
		return Record{}, false, err
	}
	if len(state.Turns) == 0 {
		return Record{}, false, nil
	}
	return state.Turns[0], true, nil
}

// Append records one bounded turn newest-first, trims the history to
// HistoryLimit, and removes the transcripts the trimmed history no longer
// references. Concurrent writers are serialized with an advisory lock and the
// file is replaced atomically.
func Append(slug string, record Record) (state State, retErr error) {
	if err := project.ValidateSlug(slug); err != nil {
		return State{}, fmt.Errorf("bounded turn program slug: %w", err)
	}
	dir := RuntimeDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return State{}, fmt.Errorf("create bounded turn runtime directory %s: %w", dir, err)
	}
	lock, err := patrollock.AcquireWait(StatePath(slug)+".lock", stateLockTimeout)
	if err != nil {
		return State{}, fmt.Errorf("serialize bounded turn history for program %q: %w", slug, err)
	}
	defer func() {
		retErr = errors.Join(retErr, lock.Release())
	}()

	state, err = readState(slug)
	if err != nil {
		return State{}, err
	}
	state.Schema = SchemaVersion
	state.Version = 1
	state.ProgramSlug = slug
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	state.Turns = append([]Record{record}, state.Turns...)
	if len(state.Turns) > HistoryLimit {
		state.Turns = state.Turns[:HistoryLimit]
	}
	if err := writeState(slug, state); err != nil {
		return State{}, err
	}
	// Pruning runs only after the trimmed history is durable, because that
	// history is the sole definition of which transcripts still matter. A prune
	// failure is reported, never swallowed, and never unwinds the recorded turn.
	if err := pruneOrphanLogs(slug, state.Turns); err != nil {
		return state, fmt.Errorf(
			"bounded turn for program %q was recorded, but stale transcripts in %s could not be pruned: %w; "+
				"the history in %s is durable—fix the directory and the next turn prunes them",
			slug, LogDir(slug), err, StatePath(slug),
		)
	}
	return state, nil
}

// logName matches exactly the transcript names LogPath generates.
var logName = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9_-]+\.log$`)

// pruneOrphanLogs removes bounded turn transcripts that no retained record
// references. It is deliberately narrow: only regular files directly inside the
// exact LogDir(slug), whose names match the transcript pattern Relay itself
// writes, are ever removed. A directory, a symlink, a foreign file, and any
// path outside that one directory are left untouched.
func pruneOrphanLogs(slug string, retained []Record) error {
	dir := LogDir(slug)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("list bounded turn transcripts %s: %w", dir, err)
	}
	keep := make(map[string]bool, len(retained))
	for _, record := range retained {
		if record.LogPath != "" {
			keep[filepath.Base(record.LogPath)] = true
		}
	}
	var errs []error
	for _, entry := range entries {
		name := entry.Name()
		if keep[name] || !entry.Type().IsRegular() || !logName.MatchString(name) {
			continue
		}
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove orphan bounded turn transcript %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

func readState(slug string) (State, error) {
	path := StatePath(slug)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{
				Schema: SchemaVersion, Version: 1, ProgramSlug: slug, Turns: []Record{},
			}, nil
		}
		return State{}, fmt.Errorf("read bounded turn history %s: %w", path, err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("parse bounded turn history %s: %w", path, err)
	}
	if state.Turns == nil {
		state.Turns = []Record{}
	}
	return state, nil
}

func writeState(slug string, state State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode bounded turn history for program %q: %w", slug, err)
	}
	data = append(data, '\n')
	dir := RuntimeDir(slug)
	file, err := os.CreateTemp(dir, ".turns.json-*")
	if err != nil {
		return fmt.Errorf("create atomic bounded turn history in %s: %w", dir, err)
	}
	tempPath := file.Name()
	cleanup := func(cause error) error {
		return errors.Join(cause, file.Close(), os.Remove(tempPath))
	}
	if err := file.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod bounded turn history %s: %w", tempPath, cleanup(err))
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write bounded turn history %s: %w", tempPath, cleanup(err))
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync bounded turn history %s: %w", tempPath, cleanup(err))
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close bounded turn history %s: %w", tempPath, errors.Join(err, os.Remove(tempPath)))
	}
	if err := os.Rename(tempPath, StatePath(slug)); err != nil {
		return fmt.Errorf(
			"replace bounded turn history %s: %w",
			StatePath(slug), errors.Join(err, os.Remove(tempPath)),
		)
	}
	return nil
}

func safeName(value string) string {
	var result strings.Builder
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			result.WriteRune(r)
		default:
			result.WriteByte('-')
		}
	}
	name := strings.Trim(result.String(), "-")
	if name == "" {
		return "session"
	}
	return name
}
