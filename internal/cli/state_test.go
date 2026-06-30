package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// wrote. The state commands print their machine-readable output to os.Stdout,
// which is the contract skills consume.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

// runState executes `relay state <args...>` against an isolated HOME and
// returns its stdout and error.
func runState(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newCmdState()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return captureStdout(t, cmd.Execute)
}

func TestStateInitNextAdvance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := runState(t, "init", "demo", "--workflow", "deliver-pr", "--phases", "clarify,plan,implement")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if strings.TrimSpace(out) != "clarify" {
		t.Errorf("init printed %q, want clarify", out)
	}
	out, _ = runState(t, "next", "demo")
	if strings.TrimSpace(out) != "clarify" {
		t.Errorf("next printed %q, want clarify", out)
	}
	if _, err := runState(t, "set", "demo", "clarify", "done"); err != nil {
		t.Fatalf("set: %v", err)
	}
	out, _ = runState(t, "advance", "demo")
	if strings.TrimSpace(out) != "implement" {
		t.Errorf("advance printed %q, want implement (clarify done + plan advanced)", out)
	}
}

func TestStateMissingSlugIsActionable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := runState(t, "next", "ghost")
	if err == nil || !strings.Contains(err.Error(), "relay state init ghost") {
		t.Errorf("missing-state error = %v, want it to point at `relay state init ghost`", err)
	}
}

func TestStateRejectsSlugTraversal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := runState(t, "init", "../escape", "--workflow", "wf", "--phases", "a")
	if err == nil || !strings.Contains(err.Error(), "invalid slug") {
		t.Errorf("traversal slug error = %v, want an invalid-slug rejection", err)
	}
}

func TestStateDoubleInitRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := runState(t, "init", "demo", "--workflow", "wf", "--phases", "a,b"); err != nil {
		t.Fatalf("first init: %v", err)
	}
	_, err := runState(t, "init", "demo", "--workflow", "wf", "--phases", "a,b")
	if err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Errorf("double init error = %v, want an already-initialized rejection", err)
	}
}

func TestStateCurrentQuotesTask(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := runState(t, "init", "demo", "--workflow", "wf", "--phases", "a,b,c"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runState(t, "set", "demo", "b", "in-progress", "--task", "step 3 of 7"); err != nil {
		t.Fatalf("set: %v", err)
	}
	out, _ := runState(t, "current", "demo")
	if !strings.Contains(out, `task="step 3 of 7"`) {
		t.Errorf("current digest = %q, want a quoted task field", out)
	}
	if !strings.Contains(out, "phase=b status=in-progress next=c") {
		t.Errorf("current digest = %q, want phase=b status=in-progress next=c", out)
	}
}
