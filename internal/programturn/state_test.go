package programturn

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPathsLiveOutsideProgramDirectories(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(home, ".relay", "run", "alpha")
	if got := RuntimeDir("alpha"); got != runtime {
		t.Errorf("RuntimeDir = %q, want %q", got, runtime)
	}
	if got := WriterLockPath("alpha"); got != filepath.Join(runtime, "writer.lock") {
		t.Errorf("WriterLockPath = %q", got)
	}
	if got := StatePath("alpha"); got != filepath.Join(runtime, "turns.json") {
		t.Errorf("StatePath = %q", got)
	}
	started := time.Date(2026, 8, 27, 10, 15, 30, 0, time.UTC)
	want := filepath.Join(runtime, "turns", "20260827T101530Z-session-1.log")
	if got := LogPath("alpha", started, "session-1"); got != want {
		t.Errorf("LogPath = %q, want %q", got, want)
	}
}

func TestLogPathRejectsUnsafeSessionIdentifiers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	started := time.Date(2026, 8, 27, 10, 15, 30, 0, time.UTC)
	got := LogPath("alpha", started, "../../escape")
	if strings.Contains(got, "..") {
		t.Fatalf("LogPath escaped the runtime directory: %q", got)
	}
	if filepath.Dir(got) != filepath.Join(RuntimeDir("alpha"), "turns") {
		t.Fatalf("LogPath = %q", got)
	}
}

func TestAppendKeepsBoundedNewestFirstHistory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Read("alpha"); err != nil {
		t.Fatalf("reading a missing turn state failed: %v", err)
	}
	for i := 0; i < HistoryLimit+10; i++ {
		if _, err := Append("alpha", Record{
			SessionID: "s" + string(rune('a'+i%26)),
			Status:    StatusSucceeded,
			StartedAt: time.Date(2026, 8, 27, 10, i%60, 0, 0, time.UTC).Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
	state, err := Read("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Turns) != HistoryLimit {
		t.Fatalf("history = %d turns, want %d", len(state.Turns), HistoryLimit)
	}
	if state.Schema != SchemaVersion || state.ProgramSlug != "alpha" || state.UpdatedAt == "" {
		t.Fatalf("state envelope = %+v", state)
	}
	last, ok, err := Latest("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || last != state.Turns[0] {
		t.Fatalf("Latest = %+v ok=%t, want newest %+v", last, ok, state.Turns[0])
	}
}

func TestAppendWritesAtomicallyAndIsConcurrencySafe(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := Append("alpha", Record{
				SessionID: "session", Status: StatusSucceeded,
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	data, err := os.ReadFile(StatePath("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("turn state is not valid JSON after concurrent appends: %v", err)
	}
	if len(state.Turns) != 8 {
		t.Fatalf("concurrent appends recorded %d turns, want 8", len(state.Turns))
	}
	entries, err := os.ReadDir(RuntimeDir("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".turns.json-") {
			t.Fatalf("atomic temporary file %s was left behind", entry.Name())
		}
	}
}

func TestReadDoesNotTouchProgramDirectories(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Append("alpha", Record{SessionID: "s", Status: StatusFailed, Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".relay", "programs")); !os.IsNotExist(err) {
		t.Fatalf("turn state touched program directories: %v", err)
	}
}

func TestValidatesSlug(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Read("../escape"); err == nil {
		t.Fatal("Read accepted an unsafe slug")
	}
	if _, err := Append("../escape", Record{}); err == nil {
		t.Fatal("Append accepted an unsafe slug")
	}
}

// Trimming the history to HistoryLimit must not leave transcripts behind that
// nothing references: turns.json is the only record of which logs still matter.
func TestAppendPrunesTranscriptsTheHistoryNoLongerReferences(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	started := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	var logs []string
	for i := 0; i < HistoryLimit+10; i++ {
		at := started.Add(time.Duration(i) * time.Minute)
		logPath := LogPath("alpha", at, fmt.Sprintf("session-%02d", i))
		writeTurnLog(t, logPath)
		logs = append(logs, logPath)
		if _, err := Append("alpha", Record{
			SessionID: fmt.Sprintf("session-%02d", i),
			Status:    StatusSucceeded,
			StartedAt: at.Format(time.RFC3339),
			LogPath:   logPath,
		}); err != nil {
			t.Fatal(err)
		}
	}

	state, err := Read("alpha")
	if err != nil {
		t.Fatal(err)
	}
	retained := map[string]bool{}
	for _, record := range state.Turns {
		retained[record.LogPath] = true
	}
	if len(retained) != HistoryLimit {
		t.Fatalf("retained turns = %d, want %d", len(retained), HistoryLimit)
	}
	for _, logPath := range logs {
		_, err := os.Stat(logPath)
		if retained[logPath] && err != nil {
			t.Errorf("retained transcript %s was pruned: %v", logPath, err)
		}
		if !retained[logPath] && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("orphan transcript %s survived: %v", logPath, err)
		}
	}
	entries, err := os.ReadDir(LogDir("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != HistoryLimit {
		t.Fatalf("transcript directory holds %d entries, want %d", len(entries), HistoryLimit)
	}
}

// Pruning only ever touches regular transcript files Relay itself named, and
// only inside the exact log directory of that one program.
func TestAppendPruningNeverTouchesForeignEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := LogDir("alpha")
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := LogPath("alpha", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), "gone")
	writeTurnLog(t, orphan)
	preserved := []string{
		filepath.Join(dir, "notes.txt"),
		filepath.Join(dir, "operator.log.bak"),
		filepath.Join(dir, "manual-debug.log"),
		filepath.Join(dir, "nested", "20260827T090000Z-nested.log"),
	}
	for _, path := range preserved {
		writeTurnLog(t, path)
	}
	sibling := LogPath("beta", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), "beta")
	writeTurnLog(t, sibling)

	kept := LogPath("alpha", time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC), "kept")
	writeTurnLog(t, kept)
	if _, err := Append("alpha", Record{
		SessionID: "kept", Status: StatusSucceeded, LogPath: kept,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("orphan transcript survived: %v", err)
	}
	for _, path := range append(preserved, kept, sibling) {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("pruning removed %s: %v", path, err)
		}
	}
}

// A prune that cannot delete must not be silent, and must not undo the turn it
// just recorded: the history stays durable and the error names the directory.
func TestAppendReportsPruneFailuresWithoutLosingTheRecordedTurn(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	t.Setenv("HOME", t.TempDir())
	dir := LogDir("alpha")
	orphan := LogPath("alpha", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), "gone")
	writeTurnLog(t, orphan)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	state, err := Append("alpha", Record{SessionID: "kept", Status: StatusSucceeded})
	if err == nil {
		t.Fatal("Append hid a prune failure")
	}
	for _, want := range []string{"was recorded", dir, "could not be pruned"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("prune error %q is missing %q", err, want)
		}
	}
	if len(state.Turns) != 1 || state.Turns[0].SessionID != "kept" {
		t.Fatalf("returned state = %+v, want the recorded turn", state.Turns)
	}
	durable, readErr := Read("alpha")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(durable.Turns) != 1 || durable.Turns[0].SessionID != "kept" {
		t.Fatalf("durable state = %+v, want the recorded turn", durable.Turns)
	}
}

func writeTurnLog(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("transcript\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
