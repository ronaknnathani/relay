package programturn

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/patrollock"
)

const holdWriterLockEnv = "RELAY_TEST_HOLD_WRITER_LOCK"

func TestWriterLockAdmitsOneHolderAndReportsContention(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first, err := AcquireWriter("alpha")
	if err != nil {
		t.Fatal(err)
	}
	second, err := AcquireWriter("alpha")
	if err == nil {
		_ = second.Release()
		t.Fatal("a second writer acquired the lock")
	}
	if !errors.Is(err, ErrWriterBusy) {
		t.Fatalf("contended acquire error = %v, want ErrWriterBusy", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	third, err := AcquireWriter("alpha")
	if err != nil {
		t.Fatalf("acquire after release failed: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatal(err)
	}
}

// TestHoldsWriterLockHelper is the subprocess half of the SIGKILL test. It is
// inert unless the parent test sets the marker environment variable.
func TestHoldsWriterLockHelper(t *testing.T) {
	if os.Getenv(holdWriterLockEnv) != "1" {
		t.Skip("helper process only")
	}
	lock, err := AcquireWriter("alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	time.Sleep(2 * time.Minute)
}

// The writer lock is a kernel-released flock, so a killed turn cannot wedge the
// program: SIGKILL must free it without any Relay cleanup.
func TestWriterLockIsReleasedWhenTheHolderIsKilled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	holder := exec.Command(os.Args[0], "-test.run=TestHoldsWriterLockHelper", "-test.timeout=3m")
	holder.Env = append(os.Environ(), "HOME="+home, holdWriterLockEnv+"=1")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = holder.Process.Kill()
		_, _ = holder.Process.Wait()
	}()
	waitFor(t, func() bool {
		held, err := patrollock.IsHeld(WriterLockPath("alpha"))
		return err == nil && held
	}, "helper never took the writer lock")

	if err := holder.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Process.Wait(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		lock, err := AcquireWriter("alpha")
		if err != nil {
			return false
		}
		return lock.Release() == nil
	}, "writer lock was not released after SIGKILL")
}

func waitFor(t *testing.T, ready func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if ready() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
