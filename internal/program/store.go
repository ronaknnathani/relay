package program

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/project"
)

// saveLockTimeout bounds how long one Save waits for another writer's kernel
// lock before failing closed.
var saveLockTimeout = 5 * time.Second

// RelayDir returns ~/.relay.
func RelayDir() string {
	return filepath.Join(os.Getenv("HOME"), ".relay")
}

// ProgramsDir returns ~/.relay/programs.
func ProgramsDir() string {
	return filepath.Join(RelayDir(), "programs")
}

// ActiveDir returns ~/.relay/programs/active.
func ActiveDir() string {
	return filepath.Join(ProgramsDir(), "active")
}

// ArchivedDir returns ~/.relay/programs/archived.
func ArchivedDir() string {
	return filepath.Join(ProgramsDir(), "archived")
}

// ProgramDir returns a program directory beneath dir.
func ProgramDir(dir, slug string) string {
	return filepath.Join(dir, slug)
}

// ManifestPath returns the program.json path beneath dir for slug.
func ManifestPath(dir, slug string) string {
	return filepath.Join(ProgramDir(dir, slug), "program.json")
}

// ProgressPath returns the append-only progress log path for programDir.
func ProgressPath(programDir string) string {
	return filepath.Join(programDir, "progress.md")
}

// DecisionLogPath returns the append-only decision log path for programDir.
func DecisionLogPath(programDir string) string {
	return filepath.Join(programDir, "decisions.md")
}

// Find searches active then archived programs and returns the manifest path.
func Find(slug string) (string, error) {
	if err := validateStoreSlug(slug); err != nil {
		return "", err
	}
	for _, dir := range []string{ActiveDir(), ArchivedDir()} {
		path := ManifestPath(dir, slug)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("find program %q: stat %s: %w", slug, path, err)
		}
	}
	return "", fmt.Errorf("program %q not found", slug)
}

// Load reads, decodes, and validates a program manifest. Retired role
// identities are normalized to their canonical form immediately after decoding,
// so a manifest written before the tech-lead rename loads as canonical state
// and never reaches validation with a legacy actor.
func Load(path string) (Program, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Program{}, fmt.Errorf("read program %s: %w", path, err)
	}
	var program Program
	if err := json.Unmarshal(data, &program); err != nil {
		return Program{}, fmt.Errorf("parse program %s: %w", path, err)
	}
	program = program.Normalize()
	if err := program.Validate(); err != nil {
		return Program{}, fmt.Errorf("validate program %s: %w", path, err)
	}
	return program, nil
}

// LoadAll loads every program manifest under dir. A missing directory is an
// empty list.
func LoadAll(dir string) ([]Program, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Program{}, nil
		}
		return nil, fmt.Errorf("read programs directory %s: %w", dir, err)
	}
	result := make([]Program, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := ManifestPath(dir, entry.Name())
		program, err := Load(path)
		if err != nil {
			return nil, fmt.Errorf("load program %q: %w", entry.Name(), err)
		}
		result = append(result, program)
	}
	return result, nil
}

// Create writes a new active program using O_EXCL so an existing manifest is
// never overwritten. Actor fields are normalized first, so a durable manifest
// only ever carries canonical role identities.
func Create(program Program) error {
	now := timestamp()
	program = program.Normalize()
	if program.Revision == 0 {
		program.Revision = 1
	}
	if program.CreatedAt == "" {
		program.CreatedAt = now
	}
	program.UpdatedAt = now
	if err := program.Validate(); err != nil {
		return fmt.Errorf("create program %q: %w", program.Slug, err)
	}
	dir := ProgramDir(ActiveDir(), program.Slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create program %q directory %s: %w", program.Slug, dir, err)
	}
	path := ManifestPath(ActiveDir(), program.Slug)
	data, err := encodeProgram(program)
	if err != nil {
		return fmt.Errorf("create program %q: %w", program.Slug, err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create program %q manifest %s: %w", program.Slug, path, err)
	}
	if _, err := file.Write(data); err != nil {
		cleanupErr := closeAndRemove(file, path, err)
		return fmt.Errorf("create program %q: write %s: %w", program.Slug, path, cleanupErr)
	}
	if err := file.Sync(); err != nil {
		cleanupErr := closeAndRemove(file, path, err)
		return fmt.Errorf("create program %q: sync %s: %w", program.Slug, path, cleanupErr)
	}
	if err := file.Close(); err != nil {
		cleanupErr := removeWithCause(path, err)
		return fmt.Errorf("create program %q: close %s: %w", program.Slug, path, cleanupErr)
	}
	return nil
}

// Save atomically replaces path when program's revision still matches disk. The
// kernel-held save lock serializes the revision read, temporary write, and
// rename; it is released automatically when a holding process exits or crashes.
// Actor fields are normalized first, so saving state loaded from a manifest
// written before the tech-lead rename emits canonical role identities.
func Save(path string, program Program) (retErr error) {
	next := program.Normalize()
	next.UpdatedAt = timestamp()
	if err := next.Validate(); err != nil {
		return fmt.Errorf("save program %q: %w", next.Slug, err)
	}
	dir := filepath.Dir(path)
	lockPath := path + ".lock"
	lock, err := patrollock.AcquireWait(lockPath, saveLockTimeout)
	if err != nil {
		if errors.Is(err, patrollock.ErrLocked) {
			return fmt.Errorf(
				"save program %q: another Relay command held the save lock %s for longer than %s; retry after it finishes",
				next.Slug, lockPath, saveLockTimeout,
			)
		}
		return fmt.Errorf("save program %q: acquire save lock %s: %w", next.Slug, lockPath, err)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release program save lock %s: %w", lockPath, err))
		}
	}()

	currentData, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("save program %q: read current manifest %s while locked: %w", next.Slug, path, err)
	}
	var current struct {
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal(currentData, &current); err != nil {
		return fmt.Errorf("save program %q: parse current revision from %s: %w", next.Slug, path, err)
	}
	if next.Revision != current.Revision {
		return fmt.Errorf(
			"save program %q: stale program revision %d; current revision is %d. Reload the program and retry",
			next.Slug, next.Revision, current.Revision,
		)
	}
	next.Revision = current.Revision + 1
	if err := next.Validate(); err != nil {
		return fmt.Errorf("save program %q at revision %d: %w", next.Slug, next.Revision, err)
	}
	data, err := encodeProgram(next)
	if err != nil {
		return fmt.Errorf("save program %q: %w", next.Slug, err)
	}
	file, err := os.CreateTemp(dir, ".program.json-*")
	if err != nil {
		return fmt.Errorf("save program %q: create atomic file in %s: %w", next.Slug, dir, err)
	}
	tempPath := file.Name()
	if err := file.Chmod(0o644); err != nil {
		cleanupErr := closeAndRemove(file, tempPath, err)
		return fmt.Errorf("save program %q: chmod %s: %w", next.Slug, tempPath, cleanupErr)
	}
	if _, err := file.Write(data); err != nil {
		cleanupErr := closeAndRemove(file, tempPath, err)
		return fmt.Errorf("save program %q: write %s: %w", next.Slug, tempPath, cleanupErr)
	}
	if err := file.Sync(); err != nil {
		cleanupErr := closeAndRemove(file, tempPath, err)
		return fmt.Errorf("save program %q: sync %s: %w", next.Slug, tempPath, cleanupErr)
	}
	if err := file.Close(); err != nil {
		cleanupErr := removeWithCause(tempPath, err)
		return fmt.Errorf("save program %q: close %s: %w", next.Slug, tempPath, cleanupErr)
	}
	if err := os.Rename(tempPath, path); err != nil {
		cleanupErr := removeWithCause(tempPath, err)
		return fmt.Errorf("save program %q: rename %s to %s: %w", next.Slug, tempPath, path, cleanupErr)
	}
	return nil
}

// Archive moves an active program directory to the archived tree.
func Archive(slug string) error {
	if err := validateStoreSlug(slug); err != nil {
		return err
	}
	source := ProgramDir(ActiveDir(), slug)
	if _, err := Load(ManifestPath(ActiveDir(), slug)); err != nil {
		return fmt.Errorf("archive program %q: %w", slug, err)
	}
	if err := os.MkdirAll(ArchivedDir(), 0o755); err != nil {
		return fmt.Errorf("archive program %q: create archive directory: %w", slug, err)
	}
	target := ProgramDir(ArchivedDir(), slug)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("archive program %q: target %s already exists", slug, target)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("archive program %q: stat target %s: %w", slug, target, err)
	}
	if err := os.Rename(source, target); err != nil {
		return fmt.Errorf("archive program %q: rename %s to %s: %w", slug, source, target, err)
	}
	return nil
}

// AppendProgress appends a timestamped progress entry to path.
func AppendProgress(path, message string) error {
	return appendLog(path, message)
}

// AppendDecisionLog appends a timestamped decision entry to path.
func AppendDecisionLog(path, message string) error {
	return appendLog(path, message)
}

func encodeProgram(program Program) ([]byte, error) {
	data, err := json.MarshalIndent(program, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode program: %w", err)
	}
	return append(data, '\n'), nil
}

func appendLog(path, message string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("append log %s: create directory: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("append log %s: open: %w", path, err)
	}
	line := fmt.Sprintf("- %s %s\n", time.Now().UTC().Format(time.RFC3339), message)
	if _, err := file.WriteString(line); err != nil {
		return fmt.Errorf("append log %s: write: %w", path, errors.Join(err, file.Close()))
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("append log %s: close: %w", path, err)
	}
	return nil
}

func validateStoreSlug(slug string) error {
	if err := project.ValidateSlug(slug); err != nil {
		return fmt.Errorf("program slug: %w", err)
	}
	return nil
}

func closeAndRemove(file *os.File, path string, cause error) error {
	return errors.Join(cause, file.Close(), os.Remove(path))
}

func removeWithCause(path string, cause error) error {
	return errors.Join(cause, os.Remove(path))
}
