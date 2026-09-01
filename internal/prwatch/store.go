package prwatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/project"
)

// stateLockTimeout bounds how long a watcher or acknowledgement waits for the
// short state mutation lock. The critical section is a read, a mutation, and an
// atomic rename, so contention is brief.
const stateLockTimeout = 10 * time.Second

// Retention bounds for the immutable runtime records.
const (
	MaxRetainedDigests          = 100
	MaxRetainedAcknowledgements = 200
)

// ErrAlreadyRunning reports that another process holds the watcher lock.
var ErrAlreadyRunning = errors.New("already running")

// ErrOutcomeConflict reports that a fingerprint was already acknowledged with a
// different outcome. Acknowledgements are immutable, so the caller must not
// reinterpret one that is already recorded.
var ErrOutcomeConflict = errors.New("fingerprint is already acknowledged with a different outcome")

// RuntimeDir returns ~/.relay/run/pr-watch/<slug>.
func RuntimeDir(slug string) string {
	return filepath.Join(project.RelayDir(), "run", "pr-watch", slug)
}

// WatchLockPath returns the lifetime-held watcher singleton lock path.
func WatchLockPath(slug string) string {
	return filepath.Join(RuntimeDir(slug), "watch.lock")
}

// StateLockPath returns the short mutation lock path guarding watch.json.
func StateLockPath(slug string) string {
	return filepath.Join(RuntimeDir(slug), "state.lock")
}

// StatePath returns the watcher runtime state path.
func StatePath(slug string) string {
	return filepath.Join(RuntimeDir(slug), "watch.json")
}

// DigestsDir returns the immutable digest directory.
func DigestsDir(slug string) string {
	return filepath.Join(RuntimeDir(slug), "digests")
}

// AcknowledgementsDir returns the immutable acknowledgement directory.
func AcknowledgementsDir(slug string) string {
	return filepath.Join(RuntimeDir(slug), "acknowledgements")
}

// DigestPath returns the immutable digest path for one fingerprint.
func DigestPath(slug, fingerprint string) (string, error) {
	if err := validateRecordName(slug, fingerprint); err != nil {
		return "", err
	}
	return filepath.Join(DigestsDir(slug), fingerprint+".json"), nil
}

// AcknowledgementPath returns the immutable acknowledgement path for one
// fingerprint.
func AcknowledgementPath(slug, fingerprint string) (string, error) {
	if err := validateRecordName(slug, fingerprint); err != nil {
		return "", err
	}
	return filepath.Join(AcknowledgementsDir(slug), fingerprint+".json"), nil
}

func validateRecordName(slug, fingerprint string) error {
	if err := project.ValidateSlug(slug); err != nil {
		return fmt.Errorf("pr watch project slug: %w", err)
	}
	if err := ValidateFingerprint(fingerprint); err != nil {
		return fmt.Errorf("pr watch record for project %q: %w", slug, err)
	}
	return nil
}

// Lock is a process-lifetime advisory watcher lock the kernel releases when the
// holding process exits.
type Lock struct {
	lock *patrollock.Lock
}

// Acquire takes the nonblocking watcher singleton lock for slug.
func Acquire(slug string) (*Lock, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return nil, fmt.Errorf("pr watch project slug: %w", err)
	}
	if err := ensureRuntimeDir(slug); err != nil {
		return nil, err
	}
	lock, err := patrollock.Acquire(WatchLockPath(slug))
	if err != nil {
		if errors.Is(err, patrollock.ErrLocked) {
			return nil, fmt.Errorf("pr watch %q: %w", slug, errors.Join(ErrAlreadyRunning, err))
		}
		return nil, err
	}
	return &Lock{lock: lock}, nil
}

// Release unlocks and closes the watcher lock.
func (l *Lock) Release() error {
	if l == nil || l.lock == nil {
		return nil
	}
	lock := l.lock
	l.lock = nil
	if err := lock.Release(); err != nil {
		return fmt.Errorf("release pr watch lock: %w", err)
	}
	return nil
}

// IsRunning reports whether a watcher process currently holds the lock.
func IsRunning(slug string) (bool, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return false, fmt.Errorf("pr watch project slug: %w", err)
	}
	path := WatchLockPath(slug)
	running, err := patrollock.IsHeld(path)
	if err != nil {
		return false, fmt.Errorf("inspect pr watch lock %s: %w", path, err)
	}
	return running, nil
}

func ensureRuntimeDir(slug string) error {
	dir := RuntimeDir(slug)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create pr watch runtime directory %s: %w", dir, err)
	}
	return nil
}

// ReadState reads the watcher runtime record. An absent record reports
// os.ErrNotExist so callers can distinguish a first-ever start.
func ReadState(slug string) (State, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return State{}, fmt.Errorf("pr watch project slug: %w", err)
	}
	path := StatePath(slug)
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, fmt.Errorf("read pr watch state %s: %w", path, err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("parse pr watch state %s: %w", path, err)
	}
	if state.AcknowledgedKeys == nil {
		state.AcknowledgedKeys = []string{}
	}
	return state, nil
}

// UpdateState applies mutate to the current runtime record under the state
// lock and writes the result atomically. The revision increases on every write,
// so a running watcher can see that an acknowledgement changed its schedule.
// An absent record is presented to mutate as the zero State.
func UpdateState(slug string, mutate func(State) (State, error)) (state State, retErr error) {
	if err := project.ValidateSlug(slug); err != nil {
		return State{}, fmt.Errorf("pr watch project slug: %w", err)
	}
	if err := ensureRuntimeDir(slug); err != nil {
		return State{}, err
	}
	lock, err := patrollock.AcquireWait(StateLockPath(slug), stateLockTimeout)
	if err != nil {
		return State{}, fmt.Errorf("lock pr watch state for project %q: %w", slug, err)
	}
	defer func() {
		retErr = errors.Join(retErr, lock.Release())
	}()

	current, err := ReadState(slug)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return State{}, err
	}
	next, err := mutate(current)
	if err != nil {
		return State{}, err
	}
	next.Schema = SchemaVersion
	next.Version = 1
	next.Project = slug
	next.Revision = current.Revision + 1
	if next.AcknowledgedKeys == nil {
		next.AcknowledgedKeys = []string{}
	}
	data, err := marshalRecord(next)
	if err != nil {
		return State{}, fmt.Errorf("encode pr watch state for project %q: %w", slug, err)
	}
	if err := writeAtomic(RuntimeDir(slug), ".watch.json-*", StatePath(slug), data); err != nil {
		return State{}, err
	}
	return next, nil
}

// WriteDigest records one immutable digest. A digest that already exists is
// left untouched, because a fingerprint identifies exactly one set of
// actionable items and its record must not change under a reader.
func WriteDigest(digest Digest) error {
	path, err := DigestPath(digest.Project, digest.Fingerprint)
	if err != nil {
		return err
	}
	digest.Schema = SchemaVersion
	digest.Version = 1
	if digest.Items == nil {
		digest.Items = []Item{}
	}
	if digest.Waiting == nil {
		digest.Waiting = []string{}
	}
	data, err := marshalRecord(digest)
	if err != nil {
		return fmt.Errorf("encode pr watch digest %s: %w", path, err)
	}
	return writeImmutable(DigestsDir(digest.Project), ".digest-*", path, data)
}

// ReadDigest reads one immutable digest.
func ReadDigest(slug, fingerprint string) (Digest, error) {
	path, err := DigestPath(slug, fingerprint)
	if err != nil {
		return Digest{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Digest{}, fmt.Errorf(
				"no pr watch digest %s for project %q: %w; run `relay pr watch tick %s --json` to observe the pull request",
				fingerprint, slug, err, slug,
			)
		}
		return Digest{}, fmt.Errorf("read pr watch digest %s: %w", path, err)
	}
	var digest Digest
	if err := json.Unmarshal(data, &digest); err != nil {
		return Digest{}, fmt.Errorf("parse pr watch digest %s: %w", path, err)
	}
	return digest, nil
}

// ReadAcknowledgement reads one immutable acknowledgement record.
func ReadAcknowledgement(slug, fingerprint string) (Acknowledgement, error) {
	path, err := AcknowledgementPath(slug, fingerprint)
	if err != nil {
		return Acknowledgement{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Acknowledgement{}, fmt.Errorf("read pr watch acknowledgement %s: %w", path, err)
	}
	var ack Acknowledgement
	if err := json.Unmarshal(data, &ack); err != nil {
		return Acknowledgement{}, fmt.Errorf("parse pr watch acknowledgement %s: %w", path, err)
	}
	return ack, nil
}

// Acknowledge records that a woken owner covered every item in one digest, then
// resets the watcher schedule under the state lock. Acknowledging the same
// fingerprint with the same outcome is idempotent; a different outcome for an
// already-acknowledged fingerprint is ErrOutcomeConflict; an unknown digest is
// an error.
func Acknowledge(slug, fingerprint string, outcome Outcome, now time.Time) (Acknowledgement, State, error) {
	path, err := AcknowledgementPath(slug, fingerprint)
	if err != nil {
		return Acknowledgement{}, State{}, err
	}
	digest, err := ReadDigest(slug, fingerprint)
	if err != nil {
		return Acknowledgement{}, State{}, err
	}
	ack := Acknowledgement{
		Schema:         SchemaVersion,
		Version:        1,
		Project:        slug,
		Fingerprint:    fingerprint,
		Outcome:        outcome,
		AcknowledgedAt: now.UTC().Format(time.RFC3339),
		HeadSHA:        digest.HeadSHA,
		Keys:           ItemKeys(digest.Items),
	}
	existing, readErr := ReadAcknowledgement(slug, fingerprint)
	switch {
	case readErr == nil && existing.Outcome != outcome:
		return Acknowledgement{}, State{}, fmt.Errorf(
			"acknowledge pr watch digest %s for project %q as %q: %w (recorded %q at %s)",
			fingerprint, slug, outcome, ErrOutcomeConflict, existing.Outcome, existing.AcknowledgedAt,
		)
	case readErr == nil:
		ack = existing
	case !errors.Is(readErr, os.ErrNotExist):
		return Acknowledgement{}, State{}, readErr
	default:
		data, err := marshalRecord(ack)
		if err != nil {
			return Acknowledgement{}, State{}, fmt.Errorf(
				"encode pr watch acknowledgement %s: %w", path, err,
			)
		}
		if err := writeImmutable(AcknowledgementsDir(slug), ".ack-*", path, data); err != nil {
			return Acknowledgement{}, State{}, err
		}
	}

	state, err := UpdateState(slug, func(state State) (State, error) {
		if acknowledgementApplied(state, ack) {
			return state, nil
		}
		state = state.WithAcknowledgedKeys(ack.Keys)
		state.AcknowledgedFingerprint = ack.Fingerprint
		state.AcknowledgedOutcome = ack.Outcome
		state.AcknowledgedAt = ack.AcknowledgedAt
		state.AttentionPending = false
		state.ScheduledChecks = 0
		state.DelaySeconds = int64(FastCadence / time.Second)
		state.NextCheckAt = now.UTC().Add(FastCadence).Format(time.RFC3339)
		state.UpdatedAt = now.UTC().Format(time.RFC3339)
		return state, nil
	})
	if err != nil {
		return Acknowledgement{}, State{}, err
	}
	if err := Prune(slug, MaxRetainedDigests, MaxRetainedAcknowledgements, state.CurrentFingerprint); err != nil {
		return Acknowledgement{}, State{}, err
	}
	return ack, state, nil
}

// acknowledgementApplied reports whether the runtime state already carries this
// acknowledgement, which makes repeating it a no-op instead of resetting the
// schedule a second time.
func acknowledgementApplied(state State, ack Acknowledgement) bool {
	if state.AcknowledgedFingerprint != ack.Fingerprint {
		return false
	}
	for _, key := range ack.Keys {
		if !state.Acknowledged(key) {
			return false
		}
	}
	return true
}

// Prune bounds the immutable runtime directories to their newest records. The
// digest a watcher currently carries and every digest that is not yet
// acknowledged are always retained, and only regular files are ever removed.
func Prune(slug string, keepDigests, keepAcknowledgements int, protected ...string) error {
	if err := project.ValidateSlug(slug); err != nil {
		return fmt.Errorf("pr watch project slug: %w", err)
	}
	keep := make(map[string]bool, len(protected))
	for _, fingerprint := range protected {
		if fingerprint != "" {
			keep[fingerprint+".json"] = true
		}
	}
	acknowledgements, err := oldestFirstRecords(AcknowledgementsDir(slug))
	if err != nil {
		return err
	}
	acknowledged := make(map[string]bool, len(acknowledgements))
	for _, entry := range acknowledgements {
		acknowledged[entry.name] = true
	}
	digests, err := oldestFirstRecords(DigestsDir(slug))
	if err != nil {
		return err
	}
	for _, entry := range digests {
		if !acknowledged[entry.name] {
			keep[entry.name] = true
		}
	}
	if err := pruneDir(DigestsDir(slug), digests, keepDigests, keep); err != nil {
		return err
	}
	return pruneDir(AcknowledgementsDir(slug), acknowledgements, keepAcknowledgements, nil)
}

type record struct {
	name     string
	modified time.Time
}

// oldestFirstRecords lists the regular files in dir, oldest first. Anything
// that is not a regular file is ignored, so pruning can never follow a link out
// of the runtime directory.
func oldestFirstRecords(dir string) ([]record, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read pr watch runtime directory %s: %w", dir, err)
	}
	records := make([]record, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf(
				"stat pr watch runtime record %s: %w", filepath.Join(dir, entry.Name()), err,
			)
		}
		records = append(records, record{name: entry.Name(), modified: info.ModTime()})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].modified.Equal(records[j].modified) {
			return records[i].name < records[j].name
		}
		return records[i].modified.Before(records[j].modified)
	})
	return records, nil
}

func pruneDir(dir string, ordered []record, keep int, protected map[string]bool) error {
	removable := len(ordered) - keep
	for _, entry := range ordered {
		if removable <= 0 {
			return nil
		}
		if protected[entry.name] {
			continue
		}
		path := filepath.Join(dir, entry.name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("prune pr watch runtime record %s: %w", path, err)
		}
		removable--
	}
	return nil
}

func marshalRecord(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// writeImmutable writes a record that must never change once observed. An
// existing record is kept, so re-observing one fingerprint is idempotent.
func writeImmutable(dir, pattern, path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat pr watch record %s: %w", path, err)
	}
	return writeAtomic(dir, pattern, path, data)
}

// writeAtomic writes data to path through a unique temporary file in the same
// directory, so a reader never sees a partial record. Every runtime record is
// mode 0600 because digests carry human review bodies.
func writeAtomic(dir, pattern, path string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create pr watch runtime directory %s: %w", dir, err)
	}
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("create atomic pr watch record in %s: %w", dir, err)
	}
	tempPath := file.Name()
	cleanup := func(cause error) error {
		return errors.Join(cause, file.Close(), os.Remove(tempPath))
	}
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod pr watch record %s: %w", tempPath, cleanup(err))
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write pr watch record %s: %w", tempPath, cleanup(err))
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync pr watch record %s: %w", tempPath, cleanup(err))
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close pr watch record %s: %w", tempPath, errors.Join(err, os.Remove(tempPath)))
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace pr watch record %s: %w", path, errors.Join(err, os.Remove(tempPath)))
	}
	return nil
}
