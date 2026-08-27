package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestHeadlessTurnCapabilitiesMatchAdapters(t *testing.T) {
	tests := map[string]bool{
		"copilot": true,
		"claude":  false,
		"codex":   false,
	}
	for name, want := range tests {
		a, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := a.Capabilities().HeadlessTurn; got != want {
			t.Errorf("%s HeadlessTurn = %t, want %t", name, got, want)
		}
		_, ok := a.(HeadlessTurner)
		if ok != want {
			t.Errorf("%s implements HeadlessTurner = %t, want %t", name, ok, want)
		}
	}
}

func TestCopilotHeadlessTurnArgsAreExact(t *testing.T) {
	got := (copilot{}).HeadlessTurnArgs(HeadlessTurnOptions{
		Repo:       "/repos/relay",
		ProgramDir: "/home/u/.relay/programs/active/alpha",
		SessionID:  "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		Prompt:     "Run the relay cto skill for alpha.",
	})
	want := []string{
		"-C", "/repos/relay",
		"--add-dir", "/home/u/.relay/programs/active/alpha",
		"--context", "long_context",
		"--allow-all",
		"--session-id", "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		"-p", "Run the relay cto skill for alpha.",
		"--silent",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("headless args mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

// A bounded turn is noninteractive and cannot answer a permission prompt, so
// it always runs with Copilot's managed-program trust regardless of how the
// interactive permission mode is configured.
func TestCopilotHeadlessTurnArgsAlwaysAllowAll(t *testing.T) {
	got := (copilot{}).HeadlessTurnArgs(HeadlessTurnOptions{
		Repo: "/repo", SessionID: "s", Prompt: "p",
	})
	if !containsArg(got, "--allow-all") {
		t.Errorf("headless args dropped --allow-all: %#v", got)
	}
	if containsArg(got, "--add-dir") {
		t.Errorf("headless args added --add-dir without a program directory: %#v", got)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestNewSessionIDIsRandomVersion4UUID(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		id, err := NewSessionID()
		if err != nil {
			t.Fatal(err)
		}
		if !pattern.MatchString(id) {
			t.Fatalf("session id %q is not a v4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("session id %q repeated", id)
		}
		seen[id] = true
	}
}

func TestRunHeadlessTurnBuildsArgvEnvLogAndSessionID(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "turns", "20260827T101500Z-session.log")
	var gotPath string
	var gotArgs, gotEnv []string
	result, err := RunHeadlessTurn(context.Background(), HeadlessTurnRequest{
		Agent:      copilot{},
		Repo:       "/repos/relay",
		ProgramDir: "/programs/alpha",
		Prompt:     "Run the relay cto skill for alpha.",
		LogPath:    logPath,
	}, HeadlessTurnDeps{
		Now:          fixedClock(time.Date(2026, 8, 27, 10, 15, 0, 0, time.UTC), time.Minute),
		NewSessionID: func() (string, error) { return "session-1", nil },
		Lookup:       func(Agent) (string, error) { return "/usr/local/bin/copilot", nil },
		Run: func(_ context.Context, path string, args, env []string, output io.Writer) (int, error) {
			gotPath, gotArgs, gotEnv = path, args, env
			if _, err := io.WriteString(output, "turn output\n"); err != nil {
				return 0, err
			}
			return 0, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/usr/local/bin/copilot" {
		t.Errorf("binary = %q", gotPath)
	}
	wantArgs := []string{
		"-C", "/repos/relay",
		"--add-dir", "/programs/alpha",
		"--context", "long_context",
		"--allow-all",
		"--session-id", "session-1",
		"-p", "Run the relay cto skill for alpha.",
		"--silent",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("args mismatch:\n got: %#v\nwant: %#v", gotArgs, wantArgs)
	}
	if count := countEnv(gotEnv, "RELAY_AUTOMATED_TURN="); count != 1 {
		t.Errorf("RELAY_AUTOMATED_TURN entries = %d, want 1: %#v", count, gotEnv)
	}
	if !containsArg(gotEnv, AutomatedTurnEnvEntry) {
		t.Errorf("env is missing %q: %#v", AutomatedTurnEnvEntry, gotEnv)
	}
	if count := countEnv(gotEnv, AutomatedTurnSessionEnvVar+"="); count != 1 {
		t.Errorf("%s entries = %d, want 1: %#v", AutomatedTurnSessionEnvVar, count, gotEnv)
	}
	if !containsArg(gotEnv, AutomatedTurnSessionEnvVar+"=session-1") {
		t.Errorf("env is missing %s=session-1: %#v", AutomatedTurnSessionEnvVar, gotEnv)
	}
	if result.SessionID != "session-1" || result.ExitCode != 0 || result.TimedOut {
		t.Errorf("result = %+v", result)
	}
	if result.LogPath != logPath {
		t.Errorf("log path = %q, want %q", result.LogPath, logPath)
	}
	if !result.StartedAt.Equal(time.Date(2026, 8, 27, 10, 15, 0, 0, time.UTC)) ||
		!result.EndedAt.Equal(time.Date(2026, 8, 27, 10, 16, 0, 0, time.UTC)) {
		t.Errorf("timestamps = %+v", result)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "turn output") {
		t.Errorf("log = %q", data)
	}
}

// The bounded turn must inherit a clean automated marker even when the parent
// process already carries a stale or forged one.
func TestRunHeadlessTurnReplacesInheritedAutomatedMarker(t *testing.T) {
	t.Setenv("RELAY_AUTOMATED_TURN", "0")
	t.Setenv(AutomatedTurnSessionEnvVar, "forged-session")
	var gotEnv []string
	if _, err := RunHeadlessTurn(context.Background(), HeadlessTurnRequest{
		Agent: copilot{}, Repo: "/repo", Prompt: "p",
		LogPath: filepath.Join(t.TempDir(), "turn.log"),
	}, HeadlessTurnDeps{
		Lookup: func(Agent) (string, error) { return "copilot", nil },
		Run: func(_ context.Context, _ string, _, env []string, _ io.Writer) (int, error) {
			gotEnv = env
			return 0, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if count := countEnv(gotEnv, "RELAY_AUTOMATED_TURN="); count != 1 {
		t.Fatalf("RELAY_AUTOMATED_TURN entries = %d, want 1", count)
	}
	if !containsArg(gotEnv, AutomatedTurnEnvEntry) {
		t.Fatalf("env is missing %q", AutomatedTurnEnvEntry)
	}
	if count := countEnv(gotEnv, AutomatedTurnSessionEnvVar+"="); count != 1 {
		t.Fatalf("%s entries = %d, want 1", AutomatedTurnSessionEnvVar, count)
	}
	if containsArg(gotEnv, AutomatedTurnSessionEnvVar+"=forged-session") {
		t.Fatal("a forged automated session id survived into the bounded turn")
	}
}

func TestRunHeadlessTurnRejectsAgentsWithoutHeadlessSupport(t *testing.T) {
	for _, a := range []Agent{claude{}, codex{}} {
		_, err := RunHeadlessTurn(context.Background(), HeadlessTurnRequest{
			Agent: a, Repo: "/repo", Prompt: "p",
			LogPath: filepath.Join(t.TempDir(), "turn.log"),
		}, HeadlessTurnDeps{
			Lookup: func(Agent) (string, error) { return "bin", nil },
			Run: func(context.Context, string, []string, []string, io.Writer) (int, error) {
				t.Fatalf("%s ran a headless turn", a.Name())
				return 0, nil
			},
		})
		if err == nil {
			t.Fatalf("%s headless turn returned nil error", a.Name())
		}
		if !strings.Contains(err.Error(), a.Name()) ||
			!strings.Contains(err.Error(), "copilot") {
			t.Errorf("%s error is not actionable: %v", a.Name(), err)
		}
	}
}

func TestRunHeadlessTurnRecordsNonZeroExitAndTimeout(t *testing.T) {
	dir := t.TempDir()
	failed, err := RunHeadlessTurn(context.Background(), HeadlessTurnRequest{
		Agent: copilot{}, Repo: "/repo", Prompt: "p",
		LogPath: filepath.Join(dir, "failed.log"),
	}, HeadlessTurnDeps{
		Lookup: func(Agent) (string, error) { return "copilot", nil },
		Run: func(context.Context, string, []string, []string, io.Writer) (int, error) {
			return 3, errors.New("exit status 3")
		},
	})
	if err != nil {
		t.Fatalf("non-zero exit returned a Go error: %v", err)
	}
	if failed.ExitCode != 3 || failed.TimedOut || failed.Error == "" {
		t.Fatalf("failed result = %+v", failed)
	}

	timedOut, err := RunHeadlessTurn(context.Background(), HeadlessTurnRequest{
		Agent: copilot{}, Repo: "/repo", Prompt: "p",
		LogPath: filepath.Join(dir, "timeout.log"), Timeout: 5 * time.Millisecond,
	}, HeadlessTurnDeps{
		Lookup: func(Agent) (string, error) { return "copilot", nil },
		Run: func(ctx context.Context, _ string, _, _ []string, _ io.Writer) (int, error) {
			<-ctx.Done()
			return -1, ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("timeout returned a Go error: %v", err)
	}
	if !timedOut.TimedOut || timedOut.ExitCode == 0 {
		t.Fatalf("timed-out result = %+v", timedOut)
	}
}

func TestRunHeadlessTurnDefaultsToTenMinuteBound(t *testing.T) {
	var deadline time.Time
	var ok bool
	if _, err := RunHeadlessTurn(context.Background(), HeadlessTurnRequest{
		Agent: copilot{}, Repo: "/repo", Prompt: "p",
		LogPath: filepath.Join(t.TempDir(), "turn.log"),
	}, HeadlessTurnDeps{
		Lookup: func(Agent) (string, error) { return "copilot", nil },
		Run: func(ctx context.Context, _ string, _, _ []string, _ io.Writer) (int, error) {
			deadline, ok = ctx.Deadline()
			return 0, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("headless turn ran without a deadline")
	}
	if remaining := time.Until(deadline); remaining < 9*time.Minute || remaining > HeadlessTurnTimeout {
		t.Fatalf("deadline remaining = %s, want ~%s", remaining, HeadlessTurnTimeout)
	}
}

// The real runner must start the agent in its own process group and kill the
// whole group when the bound elapses, so orphaned children cannot survive.
func TestExecTurnRunnerKillsProcessGroupOnTimeout(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh is unavailable")
	}
	marker := filepath.Join(t.TempDir(), "child-alive")
	script := fmt.Sprintf(
		"( while true; do echo alive >> %q; sleep 0.05; done ) & sleep 30",
		marker,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	code, err := ExecTurnRunner(ctx, "/bin/sh", []string{"-c", script}, os.Environ(), io.Discard)
	if err == nil {
		t.Fatal("timed-out process returned nil error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timed-out process took %s to return", elapsed)
	}
	if code == 0 {
		t.Fatalf("timed-out exit code = %d, want non-zero", code)
	}
	size := markerSize(t, marker)
	time.Sleep(300 * time.Millisecond)
	if grown := markerSize(t, marker); grown != size {
		t.Fatalf("process group survived the kill: marker grew %d -> %d", size, grown)
	}
}

func markerSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	return info.Size()
}

func countEnv(env []string, prefix string) int {
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			count++
		}
	}
	return count
}

func fixedClock(start time.Time, step time.Duration) func() time.Time {
	current := start.Add(-step)
	return func() time.Time {
		current = current.Add(step)
		return current
	}
}

// A caller that already knows the session id (because the log file is named
// after it) must be able to pin it instead of receiving a generated one.
func TestRunHeadlessTurnHonorsAPinnedSessionID(t *testing.T) {
	var gotArgs []string
	result, err := RunHeadlessTurn(context.Background(), HeadlessTurnRequest{
		Agent: copilot{}, Repo: "/repo", Prompt: "p", SessionID: "pinned-session",
		LogPath: filepath.Join(t.TempDir(), "turn.log"),
	}, HeadlessTurnDeps{
		NewSessionID: func() (string, error) {
			t.Fatal("a pinned session id was regenerated")
			return "", nil
		},
		Lookup: func(Agent) (string, error) { return "copilot", nil },
		Run: func(_ context.Context, _ string, args, _ []string, _ io.Writer) (int, error) {
			gotArgs = args
			return 0, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "pinned-session" {
		t.Fatalf("session id = %q", result.SessionID)
	}
	for i, arg := range gotArgs {
		if arg == "--session-id" && i+1 < len(gotArgs) && gotArgs[i+1] == "pinned-session" {
			return
		}
	}
	t.Fatalf("argv did not carry the pinned session id: %#v", gotArgs)
}
