package patrollock

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestIsHeldIgnoresConcurrentSharedProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "patrol.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)

	held, err := IsHeld(path)
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Fatal("shared liveness probe was mistaken for a running patrol")
	}
}

func TestIsHeldDetectsExclusivePatrolLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "patrol.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)

	held, err := IsHeld(path)
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("exclusive patrol lock was not detected")
	}
}

func TestAcquireIsExclusiveAndReleasable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "start.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire error = %v, want ErrLocked", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireWaitBlocksUntilTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "start.lock")
	held, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	start := time.Now()
	if _, err := AcquireWait(path, 60*time.Millisecond); !errors.Is(err, ErrLocked) {
		t.Fatalf("AcquireWait error = %v, want ErrLocked", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("AcquireWait returned after %s, want at least the full timeout", elapsed)
	}
}

func TestAcquireWaitSucceedsAfterHolderReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "start.lock")
	held, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = held.Release()
	}()

	lock, err := AcquireWait(path, 2*time.Second)
	if err != nil {
		t.Fatalf("AcquireWait: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestReleasedLockIsDetectedAsFreeAfterProcessExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "start.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	held, err := IsHeld(path)
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("acquired lock is not observable")
	}
	// Closing the descriptor is what the kernel does when a process dies.
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	held, err = IsHeld(path)
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Fatal("lock file remained held after the descriptor closed")
	}
}
