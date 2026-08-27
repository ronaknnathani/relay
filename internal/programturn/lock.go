package programturn

import (
	"errors"
	"fmt"
	"os"

	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/project"
)

// ErrWriterBusy reports that another automated writer already holds the
// program's single-writer lock. Bounded turns skip instead of queueing, so a
// slow turn never accumulates a backlog of duplicate governance actions.
var ErrWriterBusy = errors.New("another bounded program writer is running")

// Writer is a process-lifetime advisory single-writer lock. The kernel releases
// it when the holding process exits, crashes, or is killed.
type Writer struct {
	lock *patrollock.Lock
}

// AcquireWriter takes the nonblocking single-writer lock for slug.
func AcquireWriter(slug string) (*Writer, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return nil, fmt.Errorf("bounded turn program slug: %w", err)
	}
	dir := RuntimeDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create bounded turn runtime directory %s: %w", dir, err)
	}
	lock, err := patrollock.Acquire(WriterLockPath(slug))
	if err != nil {
		if errors.Is(err, patrollock.ErrLocked) {
			return nil, fmt.Errorf("program %q: %w", slug, errors.Join(ErrWriterBusy, err))
		}
		return nil, err
	}
	return &Writer{lock: lock}, nil
}

// Release unlocks and closes the single-writer lock.
func (w *Writer) Release() error {
	if w == nil || w.lock == nil {
		return nil
	}
	lock := w.lock
	w.lock = nil
	if err := lock.Release(); err != nil {
		return fmt.Errorf("release bounded turn writer lock: %w", err)
	}
	return nil
}
