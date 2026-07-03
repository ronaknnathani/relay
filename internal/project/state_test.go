package project

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var deliverPhases = []string{"clarify", "plan", "implement", "simplify", "review", "validate", "open-pr"}

func TestNewStateAllPending(t *testing.T) {
	ws, err := NewState("demo", "deliver-pr", deliverPhases)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if !reflect.DeepEqual(ws.Order, deliverPhases) {
		t.Errorf("order = %v, want %v", ws.Order, deliverPhases)
	}
	for _, p := range deliverPhases {
		if ws.Phases[p].Status != PhasePending {
			t.Errorf("phase %q = %q, want pending", p, ws.Phases[p].Status)
		}
	}
	if got := ws.Next(); got != "clarify" {
		t.Errorf("Next() = %q, want clarify", got)
	}
}

func TestNewStateRejectsBadOrder(t *testing.T) {
	if _, err := NewState("demo", "wf", nil); err == nil {
		t.Error("expected error for empty order")
	}
	if _, err := NewState("demo", "wf", []string{"a", "b", "a"}); err == nil {
		t.Error("expected error for duplicate phase")
	}
}

func TestSaveLoadStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	ws, _ := NewState("demo", "deliver-pr", deliverPhases)
	if err := ws.SetPhase("clarify", PhaseDone, "requirements.md", ""); err != nil {
		t.Fatalf("SetPhase: %v", err)
	}
	if err := ws.SetPhase("implement", PhaseInProgress, "", "3/7"); err != nil {
		t.Fatalf("SetPhase: %v", err)
	}
	ws.SetPR(412, "https://example/pr/412")
	if err := SaveState(path, ws); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if loaded.Updated == "" {
		t.Error("Updated not stamped on save")
	}
	// Updated is set on save; clear before structural compare.
	loaded.Updated = ""
	want := ws
	want.Updated = ""
	if !reflect.DeepEqual(loaded, want) {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", loaded, want)
	}
}

func TestNextAndCurrent(t *testing.T) {
	ws, _ := NewState("demo", "wf", []string{"a", "b", "c"})
	if got := ws.Next(); got != "a" {
		t.Errorf("Next() = %q, want a", got)
	}
	if got := ws.Current(); got != "a" {
		t.Errorf("Current() = %q, want a", got)
	}
	// Mark a done, b in-progress: Next skips done, Current prefers in-progress.
	_ = ws.SetPhase("a", PhaseDone, "", "")
	_ = ws.SetPhase("b", PhaseInProgress, "", "")
	if got := ws.Next(); got != "b" {
		t.Errorf("Next() = %q, want b (first not-done)", got)
	}
	if got := ws.Current(); got != "b" {
		t.Errorf("Current() = %q, want b (in-progress)", got)
	}
}

func TestAdvance(t *testing.T) {
	ws, _ := NewState("demo", "wf", []string{"a", "b"})
	next, err := ws.Advance()
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if ws.Phases["a"].Status != PhaseDone {
		t.Errorf("a not marked done after advance")
	}
	if next != "b" {
		t.Errorf("Advance() next = %q, want b", next)
	}
	next, err = ws.Advance()
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if next != "" {
		t.Errorf("Advance() next = %q, want empty (run complete)", next)
	}
	if _, err := ws.Advance(); err == nil {
		t.Error("expected error advancing past the last phase")
	}
}

func TestSetPhaseRejectsBadInput(t *testing.T) {
	ws, _ := NewState("demo", "wf", []string{"a"})
	if err := ws.SetPhase("nope", PhaseDone, "", ""); err == nil {
		t.Error("expected error for unknown phase")
	}
	if err := ws.SetPhase("a", "halfway", "", ""); err == nil {
		t.Error("expected error for invalid status")
	}
}

func TestLoadStateRejectsCorrupt(t *testing.T) {
	cases := map[string]string{
		"empty order":        `{"slug":"x","order":[],"phases":{}}`,
		"missing phase":      `{"slug":"x","order":["a","b"],"phases":{"a":{"status":"done"}}}`,
		"bad status":         `{"slug":"x","order":["a"],"phases":{"a":{"status":"bogus"}}}`,
		"duplicate in order": `{"slug":"x","order":["a","a"],"phases":{"a":{"status":"pending"}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := LoadState(path); err == nil {
				t.Errorf("LoadState accepted corrupt state %q", name)
			}
		})
	}
}

func TestAdvanceActsOnCurrentPhase(t *testing.T) {
	// b in-progress while a is still pending: Advance must mark b (the current
	// phase) done, not the earlier pending a.
	ws, _ := NewState("demo", "wf", []string{"a", "b", "c"})
	_ = ws.SetPhase("b", PhaseInProgress, "", "")
	next, err := ws.Advance()
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if ws.Phases["b"].Status != PhaseDone {
		t.Errorf("b status = %q, want done (Advance acted on current phase)", ws.Phases["b"].Status)
	}
	if ws.Phases["a"].Status != PhasePending {
		t.Errorf("a status = %q, want pending (Advance must not touch an unrelated phase)", ws.Phases["a"].Status)
	}
	if next != "a" {
		t.Errorf("Advance() next = %q, want a (first not-done)", next)
	}
}

func TestNewStateCopiesOrder(t *testing.T) {
	order := []string{"a", "b", "c"}
	ws, _ := NewState("demo", "wf", order)
	order[0] = "MUTATED"
	if ws.Order[0] != "a" {
		t.Errorf("Order aliased the caller slice: got %q after caller mutation", ws.Order[0])
	}
}

func TestCreateStateIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	ws, _ := NewState("demo", "wf", []string{"a"})
	if err := CreateState(path, ws); err != nil {
		t.Fatalf("CreateState: %v", err)
	}
	err := CreateState(path, ws)
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("second CreateState err = %v, want fs.ErrExist", err)
	}
}

func TestValidateSlug(t *testing.T) {
	for _, bad := range []string{"", "../escape", "a/b", ".hidden", "..", "x/../y"} {
		if err := ValidateSlug(bad); err == nil {
			t.Errorf("ValidateSlug(%q) = nil, want error", bad)
		}
	}
	for _, ok := range []string{"demo", "stable-pod-identity", "pr1-go-migration"} {
		if err := ValidateSlug(ok); err != nil {
			t.Errorf("ValidateSlug(%q) = %v, want nil", ok, err)
		}
	}
}

func TestAppendProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.md")
	if err := AppendProgress(path, "PR opened #412"); err != nil {
		t.Fatalf("AppendProgress: %v", err)
	}
	if err := AppendProgress(path, "CI green"); err != nil {
		t.Fatalf("AppendProgress: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), data)
	}
	if !strings.Contains(lines[0], "PR opened #412") || !strings.Contains(lines[1], "CI green") {
		t.Errorf("progress lines missing messages:\n%s", data)
	}
}
