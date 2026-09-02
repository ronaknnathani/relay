package patrol

import (
	"errors"
	"fmt"
	"os"

	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/project"
)

// ErrAlreadyRunning reports that another process holds the patrol lock.
var ErrAlreadyRunning = errors.New("already running")

// Lock is a process-lifetime advisory patrol lock the kernel releases when the
// holding process exits.
type Lock struct {
	lock *patrollock.Lock
}

// Acquire takes the nonblocking singleton lock for slug.
func Acquire(slug string) (*Lock, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return nil, fmt.Errorf("patrol program slug: %w", err)
	}
	dir := RuntimeDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create patrol runtime directory %s: %w", dir, err)
	}
	lock, err := patrollock.Acquire(LockPath(slug))
	if err != nil {
		if errors.Is(err, patrollock.ErrLocked) {
			return nil, fmt.Errorf("patrol %q: %w", slug, errors.Join(ErrAlreadyRunning, err))
		}
		return nil, err
	}
	return &Lock{lock: lock}, nil
}

// IsRunning reports whether another process currently holds the patrol lock.
func IsRunning(slug string) (bool, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return false, fmt.Errorf("patrol program slug: %w", err)
	}
	path := LockPath(slug)
	running, err := patrollock.IsHeld(path)
	if err != nil {
		return false, fmt.Errorf("inspect patrol lock %s: %w", path, err)
	}
	return running, nil
}

// Release unlocks and closes the patrol lock.
func (l *Lock) Release() error {
	if l == nil || l.lock == nil {
		return nil
	}
	lock := l.lock
	l.lock = nil
	if err := lock.Release(); err != nil {
		return fmt.Errorf("release patrol lock: %w", err)
	}
	return nil
}
