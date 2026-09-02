package patrol

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestLockIsSingletonAndReleases(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lock, err := Acquire("singleton")
	if err != nil {
		t.Fatal(err)
	}
	running, err := IsRunning("singleton")
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Fatal("IsRunning = false while lock is held")
	}
	if _, err := Acquire("singleton"); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire error = %v, want ErrAlreadyRunning", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	running, err = IsRunning("singleton")
	if err != nil {
		t.Fatal(err)
	}
	if running {
		t.Fatal("IsRunning = true after release")
	}
	reacquired, err := Acquire("singleton")
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLockKernelReleaseAfterProcessKill(t *testing.T) {
	if os.Getenv("RELAY_PATROL_LOCK_HELPER") == "1" {
		lock, err := Acquire("killed")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer lock.Release()
		fmt.Println("ready")
		select {}
	}

	home := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=TestLockKernelReleaseAfterProcessKill")
	command.Env = append(os.Environ(), "HOME="+home, "RELAY_PATROL_LOCK_HELPER=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if !bufio.NewScanner(stdout).Scan() {
		_ = command.Process.Kill()
		t.Fatal("helper exited before acquiring lock")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed helper exited successfully")
	}

	t.Setenv("HOME", home)
	deadline := time.Now().Add(time.Second)
	for {
		lock, err := Acquire("killed")
		if err == nil {
			if err := lock.Release(); err != nil {
				t.Fatal(err)
			}
			return
		}
		if !errors.Is(err, ErrAlreadyRunning) || time.Now().After(deadline) {
			t.Fatalf("reacquire after kill: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
