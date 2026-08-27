package patrol

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
)

type recordingTurnRunner struct {
	requests []TurnRequest
	results  []TurnResult
	errs     []error
	index    int
}

func (r *recordingTurnRunner) RunTurn(_ context.Context, request TurnRequest) (TurnResult, error) {
	r.requests = append(r.requests, request)
	result := TurnResult{Status: TurnSucceeded, SessionID: "session", LogPath: "/log"}
	var err error
	if r.index < len(r.results) {
		result = r.results[r.index]
	}
	if r.index < len(r.errs) {
		err = r.errs[r.index]
	}
	r.index++
	return result, err
}

func idleCTO(slug string) []herdr.Agent {
	return []herdr.Agent{{
		PaneID: "cto-" + slug, TerminalTitle: "relay:program:" + slug + " - GitHub Copilot",
		Status: herdr.StatusIdle,
	}}
}

func attention(slug, fingerprint string) Observation {
	return Observation{
		ProgramSlug:          slug,
		AttentionKeys:        []string{"open-decision:d1"},
		AttentionFingerprint: fingerprint,
		Reasons:              []Reason{{Code: "open-decision:d1", Text: "Decision d1 is awaiting resolution."}},
	}
}

func TestTurnArmsFingerprintOnlyAfterASuccessfulTurn(t *testing.T) {
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
	runner := &recordingTurnRunner{}

	if warning := requestCTOTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleCTO("alpha"), runner, nil, now,
	); warning != "" {
		t.Fatalf("successful turn warned: %q", warning)
	}
	if len(runner.requests) != 1 ||
		runner.requests[0].ProgramSlug != "alpha" ||
		runner.requests[0].Fingerprint != "fp-1" {
		t.Fatalf("turn requests = %+v", runner.requests)
	}
	if state.AttentionFingerprint != "fp-1" ||
		state.LastNotifiedAt != now.Format(time.RFC3339) ||
		state.LastTurnStatus != string(TurnSucceeded) ||
		state.LastTurnSessionID != "session" || state.LastTurnLogPath != "/log" ||
		state.TurnFailures != 0 || !state.CTOPresent {
		t.Fatalf("state after success = %+v", state)
	}

	// Unchanged attention is not re-run before the two-hour rearm.
	if warning := requestCTOTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleCTO("alpha"), runner, nil,
		now.Add(time.Hour),
	); warning != "" {
		t.Fatal(warning)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("unchanged attention re-ran a turn: %d requests", len(runner.requests))
	}

	// The two-hour rearm runs a fresh bounded turn for unchanged attention.
	if warning := requestCTOTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleCTO("alpha"), runner, nil,
		now.Add(2*time.Hour),
	); warning != "" {
		t.Fatal(warning)
	}
	if len(runner.requests) != 2 {
		t.Fatalf("two-hour rearm requests = %d, want 2", len(runner.requests))
	}
}

func TestTurnLeavesFingerprintUnchangedOnFailureTimeoutAndSkip(t *testing.T) {
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		result TurnResult
		err    error
		fails  int
	}{
		{name: "failed", result: TurnResult{Status: TurnFailed, Error: "exit status 1"}, fails: 1},
		{name: "timed out", result: TurnResult{Status: TurnTimedOut, Error: "10m limit"}, fails: 1},
		{name: "runner error", result: TurnResult{}, err: errors.New("cannot start"), fails: 1},
		{name: "skipped", result: TurnResult{Status: TurnSkipped, Reason: "another writer is running"}, fails: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
			runner := &recordingTurnRunner{
				results: []TurnResult{test.result}, errs: []error{test.err},
			}
			warning := requestCTOTurn(
				context.Background(), &state, attention("alpha", "fp-1"), idleCTO("alpha"), runner, nil, now,
			)
			if warning == "" {
				t.Fatal("unsuccessful turn produced no warning")
			}
			if state.AttentionFingerprint != "" || state.LastNotifiedAt != "" {
				t.Fatalf("unsuccessful turn armed the fingerprint: %+v", state)
			}
			if state.TurnFailures != test.fails {
				t.Fatalf("turn failures = %d, want %d", state.TurnFailures, test.fails)
			}
			if !state.CTOPresent {
				t.Fatal("a turn problem cleared CTO presence")
			}

			// The next tick retries because attention is still unarmed.
			runner.results, runner.errs, runner.index = nil, nil, 0
			if warning := requestCTOTurn(
				context.Background(), &state, attention("alpha", "fp-1"), idleCTO("alpha"), runner, nil,
				now.Add(time.Minute),
			); warning != "" {
				t.Fatal(warning)
			}
			if state.AttentionFingerprint != "fp-1" {
				t.Fatalf("retry did not arm the fingerprint: %+v", state)
			}
		})
	}
}

func TestTurnSuppressesAfterThreeConsecutiveFailuresUntilAttentionChanges(t *testing.T) {
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
	failing := &recordingTurnRunner{
		results: []TurnResult{
			{Status: TurnFailed, Error: "one"},
			{Status: TurnFailed, Error: "two"},
			{Status: TurnFailed, Error: "three"},
		},
	}
	for attempt := 1; attempt <= 3; attempt++ {
		at := now.Add(time.Duration(attempt) * time.Minute)
		if warning := requestCTOTurn(
			context.Background(), &state, attention("alpha", "fp-1"), idleCTO("alpha"), failing, nil, at,
		); warning == "" {
			t.Fatalf("attempt %d produced no warning", attempt)
		}
		if state.TurnFailures != attempt {
			t.Fatalf("attempt %d failures = %d", attempt, state.TurnFailures)
		}
	}

	// A fourth tick must not attempt a turn at all.
	suppressed := &recordingTurnRunner{}
	warning := requestCTOTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleCTO("alpha"), suppressed, nil,
		now.Add(10*time.Minute),
	)
	if len(suppressed.requests) != 0 {
		t.Fatalf("suppressed patrol still ran %d turns", len(suppressed.requests))
	}
	for _, want := range []string{"suppressed", "3"} {
		if !strings.Contains(warning, want) {
			t.Errorf("suppression warning %q is missing %q", warning, want)
		}
	}

	// Changed attention resets the failure budget and resumes bounded turns.
	recovering := &recordingTurnRunner{}
	if warning := requestCTOTurn(
		context.Background(), &state, attention("alpha", "fp-2"), idleCTO("alpha"), recovering, nil,
		now.Add(11*time.Minute),
	); warning != "" {
		t.Fatal(warning)
	}
	if len(recovering.requests) != 1 || state.TurnFailures != 0 || state.AttentionFingerprint != "fp-2" {
		t.Fatalf("recovery = requests %d state %+v", len(recovering.requests), state)
	}
}

func TestTurnGatesOnExactIdleOrDoneCTO(t *testing.T) {
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	for _, status := range []herdr.Status{herdr.StatusIdle, herdr.StatusDone} {
		state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
		runner := &recordingTurnRunner{}
		agents := idleCTO("alpha")
		agents[0].Status = status
		if warning := requestCTOTurn(
			context.Background(), &state, attention("alpha", "fp-1"), agents, runner, nil, now,
		); warning != "" {
			t.Fatalf("%s CTO warned: %q", status, warning)
		}
		if len(runner.requests) != 1 {
			t.Fatalf("%s CTO ran %d turns, want 1", status, len(runner.requests))
		}
	}

	for _, status := range []herdr.Status{herdr.StatusWorking, herdr.StatusBlocked, herdr.StatusUnknown} {
		state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
		runner := &recordingTurnRunner{}
		agents := idleCTO("alpha")
		agents[0].Status = status
		if warning := requestCTOTurn(
			context.Background(), &state, attention("alpha", "fp-1"), agents, runner, nil, now,
		); warning != "" {
			t.Fatalf("%s CTO warned: %q", status, warning)
		}
		if len(runner.requests) != 0 {
			t.Fatalf("%s CTO ran a bounded turn", status)
		}
		if !state.CTOPresent || state.AttentionFingerprint != "" {
			t.Fatalf("%s CTO state = %+v", status, state)
		}
	}

	// Two programs in one repository must not be confused for each other.
	state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
	runner := &recordingTurnRunner{}
	if warning := requestCTOTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleCTO("beta"), runner, nil, now,
	); warning != "" {
		t.Fatal(warning)
	}
	if len(runner.requests) != 0 || state.CTOPresent {
		t.Fatalf("another program's CTO was used: requests %d state %+v", len(runner.requests), state)
	}
}

func TestTurnClearsAttentionOnDrainAndResetsFailures(t *testing.T) {
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	state := State{
		ProgramSlug:          "alpha",
		Reasons:              []Reason{},
		AttentionFingerprint: "fp-1",
		LastNotifiedAt:       now.Format(time.RFC3339),
		TurnFailures:         3,
	}
	runner := &recordingTurnRunner{}
	if warning := requestCTOTurn(
		context.Background(), &state, Observation{ProgramSlug: "alpha"}, idleCTO("alpha"), runner, nil,
		now.Add(time.Minute),
	); warning != "" {
		t.Fatal(warning)
	}
	if state.AttentionFingerprint != "" || state.LastNotifiedAt != "" || state.TurnFailures != 0 {
		t.Fatalf("drained state = %+v", state)
	}
	if len(runner.requests) != 0 {
		t.Fatal("drained attention ran a bounded turn")
	}

	// Recurring attention runs a fresh bounded turn immediately.
	if warning := requestCTOTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleCTO("alpha"), runner, nil,
		now.Add(2*time.Minute),
	); warning != "" {
		t.Fatal(warning)
	}
	if len(runner.requests) != 1 || state.AttentionFingerprint != "fp-1" {
		t.Fatalf("recurrence = requests %d state %+v", len(runner.requests), state)
	}
}

func TestTurnWarnsWhenNoRunnerIsConfigured(t *testing.T) {
	state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
	warning := requestCTOTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleCTO("alpha"), nil, nil,
		time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
	)
	if warning == "" {
		t.Fatal("missing runner produced no warning")
	}
	if state.AttentionFingerprint != "" {
		t.Fatalf("missing runner armed the fingerprint: %+v", state)
	}
}

type recordingNotifier struct {
	calls int
	err   error
}

func (n *recordingNotifier) ShowNotification(string, string) error {
	n.calls++
	return n.err
}

// The desktop notification is a courtesy. A failing notifier must never change
// the turn outcome or the patrol's fingerprint bookkeeping.
func TestTurnNotificationIsBestEffort(t *testing.T) {
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
	notifier := &recordingNotifier{err: errors.New("no desktop session")}
	if warning := requestCTOTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleCTO("alpha"),
		&recordingTurnRunner{}, notifier, now,
	); warning != "" {
		t.Fatalf("failing notifier changed the outcome: %q", warning)
	}
	if notifier.calls != 1 || state.AttentionFingerprint != "fp-1" {
		t.Fatalf("notifier calls = %d state = %+v", notifier.calls, state)
	}
}

// The patrol is a read-only observer. It must never stage or submit text into
// the CEO-facing CTO pane, so the package may not reference Herdr prompting.
func TestPatrolNeverPromptsHerdrAgents(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"PromptAgent", "send-keys", "FocusAgent", "RunPane"}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbiddenName := range forbidden {
			if strings.Contains(string(data), forbiddenName) {
				t.Errorf("%s references Herdr input %q; patrol must stay read-only", name, forbiddenName)
			}
		}
	}
}

// The transport is a fresh same-role session, never a resume of the live CEO
// session and never a Herdr keystroke.
func TestPatrolNeverResumesTheLiveCTOSession(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			if strings.Contains(literal.Value, "--resume") {
				t.Errorf("%s resumes an agent session: %s", name, literal.Value)
			}
			return true
		})
	}
}

// Two panes claiming one program is ambiguous ownership: the patrol must not
// pick one and run a turn beside the other, and it must say why it stopped.
func TestTurnSkipsAndWarnsWhenTwoLiveCTOsClaimTheProgram(t *testing.T) {
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
	runner := &recordingTurnRunner{}
	duplicates := []herdr.Agent{
		{PaneID: "p1", TerminalTitle: "relay:program:alpha - GitHub Copilot", Status: herdr.StatusIdle},
		{PaneID: "p2", TerminalTitle: "relay:program:alpha", Status: herdr.StatusWorking},
	}

	warning := requestCTOTurn(
		context.Background(), &state, attention("alpha", "fp-1"), duplicates, runner, nil, now,
	)
	if len(runner.requests) != 0 {
		t.Fatalf("a turn ran with duplicate CTOs: %+v", runner.requests)
	}
	for _, want := range []string{
		`skipped the bounded CTO turn for program "alpha"`, "2 live CTO sessions", "p1", "p2",
	} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q is missing %q", warning, want)
		}
	}
	if state.CTOPresent {
		t.Error("CTOPresent = true, want false while ownership is ambiguous")
	}
	if state.AttentionFingerprint != "" {
		t.Errorf("attention fingerprint = %q, want unarmed", state.AttentionFingerprint)
	}
}
