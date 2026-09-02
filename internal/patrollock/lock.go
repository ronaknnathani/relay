// Package patrollock provides the shared kernel-released advisory locks used by
// Relay runtime singletons and the read-only program views that probe them.
package patrollock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrLocked reports that another process currently holds a lock.
var ErrLocked = errors.New("lock is held by another process")

const acquirePollInterval = 20 * time.Millisecond

// Lock is an advisory lock the kernel releases when the holding process exits.
type Lock struct {
	file *os.File
}

// Acquire takes the exclusive nonblocking lock at path, creating the lock file
// and its parent directory when they are absent. A held lock returns ErrLocked.
func Acquire(path string) (*Lock, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory %s: %w", dir, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("acquire lock %s: %w", path, errors.Join(ErrLocked, closeErr))
		}
		return nil, fmt.Errorf("acquire lock %s: %w", path, errors.Join(err, closeErr))
	}
	return &Lock{file: file}, nil
}

// AcquireWait retries Acquire until it succeeds, a non-contention error occurs,
// or timeout elapses. Contention past the deadline returns ErrLocked.
func AcquireWait(path string, timeout time.Duration) (*Lock, error) {
	deadline := time.Now().Add(timeout)
	for {
		lock, err := Acquire(path)
		if err == nil || !errors.Is(err, ErrLocked) {
			return lock, err
		}
		if !time.Now().Before(deadline) {
			return nil, err
		}
		remaining := time.Until(deadline)
		interval := acquirePollInterval
		if remaining < interval {
			interval = remaining
		}
		time.Sleep(interval)
	}
}

// Release unlocks and closes the lock. The kernel performs the same release
// when a holding process exits or crashes.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	path := l.file.Name()
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err := errors.Join(unlockErr, closeErr); err != nil {
		return fmt.Errorf("release lock %s: %w", path, err)
	}
	return nil
}

// IsHeld reports whether another process currently holds path.
func IsHeld(path string) (bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		closeErr := file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if closeErr != nil {
				return false, fmt.Errorf("close lock probe %s: %w", path, closeErr)
			}
			return true, nil
		}
		return false, fmt.Errorf("probe lock %s: %w", path, errors.Join(err, closeErr))
	}
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	if err := errors.Join(unlockErr, closeErr); err != nil {
		return false, fmt.Errorf("release lock probe %s: %w", path, err)
	}
	return false, nil
}
