package patrol

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
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
	result := TurnResult{Status: TurnSucceeded}
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

func idleTL(slug string) []herdr.Agent {
	return []herdr.Agent{{
		PaneID: "tl-" + slug, TerminalTitle: "relay:program:" + slug + " - GitHub Copilot",
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

	outcome := requestTLTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleTL("alpha"), runner, nil, now,
	)
	if outcome.Warning != "" {
		t.Fatalf("successful turn warned: %q", outcome.Warning)
	}
	if outcome.Kind != wakeDelivered || !slices.Equal(outcome.Panes, []string{"tl-alpha"}) ||
		outcome.Status != string(herdr.StatusIdle) {
		t.Fatalf("delivered outcome = %+v", outcome)
	}
	if len(runner.requests) != 1 ||
		runner.requests[0].ProgramSlug != "alpha" ||
		runner.requests[0].PaneID != "tl-alpha" ||
		runner.requests[0].Fingerprint != "fp-1" {
		t.Fatalf("turn requests = %+v", runner.requests)
	}
	if state.AttentionFingerprint != "fp-1" ||
		state.LastNotifiedAt != now.Format(time.RFC3339) ||
		state.LastTurnStatus != string(TurnSucceeded) ||
		state.LastTurnSessionID != "" || state.LastTurnLogPath != "" ||
		state.TurnFailures != 0 || !state.TLPresent {
		t.Fatalf("state after success = %+v", state)
	}

	// Unchanged attention is not re-run before the two-hour rearm.
	if outcome := requestTLTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleTL("alpha"), runner, nil,
		now.Add(time.Hour),
	); outcome.Warning != "" || outcome.Kind != wakeNotNeeded {
		t.Fatalf("unchanged attention outcome = %+v", outcome)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("unchanged attention re-ran a turn: %d requests", len(runner.requests))
	}

	// The two-hour rearm rings the same live tech lead for unchanged attention.
	if outcome := requestTLTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleTL("alpha"), runner, nil,
		now.Add(2*time.Hour),
	); outcome.Warning != "" || outcome.Kind != wakeDelivered {
		t.Fatalf("rearmed outcome = %+v", outcome)
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
		kind   wakeKind
	}{
		{
			name: "failed", result: TurnResult{Status: TurnFailed, Error: "exit status 1"},
			fails: 1, kind: wakeFailed,
		},
		{
			name: "timed out", result: TurnResult{Status: TurnTimedOut, Error: "10m limit"},
			fails: 1, kind: wakeFailed,
		},
		{
			name: "runner error", result: TurnResult{}, err: errors.New("cannot start"),
			fails: 1, kind: wakeFailed,
		},
		{
			name: "skipped", result: TurnResult{Status: TurnSkipped, Reason: "another writer is running"},
			fails: 0, kind: wakeSkipped,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
			runner := &recordingTurnRunner{
				results: []TurnResult{test.result}, errs: []error{test.err},
			}
			outcome := requestTLTurn(
				context.Background(), &state, attention("alpha", "fp-1"), idleTL("alpha"), runner, nil, now,
			)
			if outcome.Warning == "" {
				t.Fatal("unsuccessful turn produced no warning")
			}
			if outcome.Kind != test.kind {
				t.Fatalf("outcome kind = %q, want %q", outcome.Kind, test.kind)
			}
			if state.AttentionFingerprint != "" || state.LastNotifiedAt != "" {
				t.Fatalf("unsuccessful turn armed the fingerprint: %+v", state)
			}
			if state.TurnFailures != test.fails {
				t.Fatalf("turn failures = %d, want %d", state.TurnFailures, test.fails)
			}
			if !state.TLPresent {
				t.Fatal("a turn problem cleared tech lead presence")
			}

			// The next tick retries because attention is still unarmed.
			runner.results, runner.errs, runner.index = nil, nil, 0
			if outcome := requestTLTurn(
				context.Background(), &state, attention("alpha", "fp-1"), idleTL("alpha"), runner, nil,
				now.Add(time.Minute),
			); outcome.Warning != "" {
				t.Fatal(outcome.Warning)
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
		if outcome := requestTLTurn(
			context.Background(), &state, attention("alpha", "fp-1"), idleTL("alpha"), failing, nil, at,
		); outcome.Warning == "" || outcome.Kind != wakeFailed {
			t.Fatalf("attempt %d outcome = %+v", attempt, outcome)
		}
		if state.TurnFailures != attempt {
			t.Fatalf("attempt %d failures = %d", attempt, state.TurnFailures)
		}
	}

	// A fourth tick must not attempt a turn at all.
	suppressed := &recordingTurnRunner{}
	outcome := requestTLTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleTL("alpha"), suppressed, nil,
		now.Add(10*time.Minute),
	)
	if len(suppressed.requests) != 0 {
		t.Fatalf("suppressed patrol still ran %d turns", len(suppressed.requests))
	}
	if outcome.Kind != wakeSuppressed {
		t.Errorf("suppressed outcome kind = %q, want %q", outcome.Kind, wakeSuppressed)
	}
	for _, want := range []string{"suppressed", "3"} {
		if !strings.Contains(outcome.Warning, want) {
			t.Errorf("suppression warning %q is missing %q", outcome.Warning, want)
		}
	}

	// Changed attention resets the failure budget and resumes live doorbells.
	recovering := &recordingTurnRunner{}
	if outcome := requestTLTurn(
		context.Background(), &state, attention("alpha", "fp-2"), idleTL("alpha"), recovering, nil,
		now.Add(11*time.Minute),
	); outcome.Warning != "" {
		t.Fatal(outcome.Warning)
	}
	if len(recovering.requests) != 1 || state.TurnFailures != 0 || state.AttentionFingerprint != "fp-2" {
		t.Fatalf("recovery = requests %d state %+v", len(recovering.requests), state)
	}
}

func TestTurnGatesOnExactIdleOrDoneTL(t *testing.T) {
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	for _, status := range []herdr.Status{herdr.StatusIdle, herdr.StatusDone} {
		state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
		runner := &recordingTurnRunner{}
		agents := idleTL("alpha")
		agents[0].Status = status
		outcome := requestTLTurn(
			context.Background(), &state, attention("alpha", "fp-1"), agents, runner, nil, now,
		)
		if outcome.Warning != "" {
			t.Fatalf("%s tech lead warned: %q", status, outcome.Warning)
		}
		if len(runner.requests) != 1 || outcome.Kind != wakeDelivered {
			t.Fatalf("%s tech lead ran %d turns (%+v), want 1 delivered", status, len(runner.requests), outcome)
		}
	}

	for _, status := range []herdr.Status{herdr.StatusWorking, herdr.StatusBlocked, herdr.StatusUnknown} {
		state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
		runner := &recordingTurnRunner{}
		agents := idleTL("alpha")
		agents[0].Status = status
		outcome := requestTLTurn(
			context.Background(), &state, attention("alpha", "fp-1"), agents, runner, nil, now,
		)
		if outcome.Warning != "" {
			t.Fatalf("%s tech lead warned: %q", status, outcome.Warning)
		}
		if outcome.Kind != wakeBusy || !slices.Equal(outcome.Panes, []string{"tl-alpha"}) ||
			outcome.Status != string(status) {
			t.Fatalf("%s tech lead outcome = %+v", status, outcome)
		}
		if len(runner.requests) != 0 {
			t.Fatalf("%s tech lead received a live doorbell", status)
		}
		if !state.TLPresent || state.AttentionFingerprint != "" {
			t.Fatalf("%s tech lead state = %+v", status, state)
		}
	}

	// Two programs in one repository must not be confused for each other.
	state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
	runner := &recordingTurnRunner{}
	if outcome := requestTLTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleTL("beta"), runner, nil, now,
	); outcome.Warning != "" || outcome.Kind != wakeAbsent {
		t.Fatalf("absent tech lead outcome = %+v", outcome)
	}
	if len(runner.requests) != 0 || state.TLPresent {
		t.Fatalf("another program's tech lead was used: requests %d state %+v", len(runner.requests), state)
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
	if outcome := requestTLTurn(
		context.Background(), &state, Observation{ProgramSlug: "alpha"}, idleTL("alpha"), runner, nil,
		now.Add(time.Minute),
	); outcome.Warning != "" || outcome.Kind != wakeNotNeeded {
		t.Fatalf("drained outcome = %+v", outcome)
	}
	if state.AttentionFingerprint != "" || state.LastNotifiedAt != "" || state.TurnFailures != 0 {
		t.Fatalf("drained state = %+v", state)
	}
	if len(runner.requests) != 0 {
		t.Fatal("drained attention rang the tech lead")
	}

	// Recurring attention rings the live tech lead immediately.
	if outcome := requestTLTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleTL("alpha"), runner, nil,
		now.Add(2*time.Minute),
	); outcome.Warning != "" {
		t.Fatal(outcome.Warning)
	}
	if len(runner.requests) != 1 || state.AttentionFingerprint != "fp-1" {
		t.Fatalf("recurrence = requests %d state %+v", len(runner.requests), state)
	}
}

func TestTurnWarnsWhenNoRunnerIsConfigured(t *testing.T) {
	state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
	outcome := requestTLTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleTL("alpha"), nil, nil,
		time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
	)
	if outcome.Warning == "" || outcome.Kind != wakeFailed {
		t.Fatalf("missing runner outcome = %+v", outcome)
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
	if outcome := requestTLTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleTL("alpha"),
		&recordingTurnRunner{}, notifier, now,
	); outcome.Warning != "" {
		t.Fatalf("failing notifier changed the outcome: %q", outcome.Warning)
	}
	if notifier.calls != 1 || state.AttentionFingerprint != "fp-1" {
		t.Fatalf("notifier calls = %d state = %+v", notifier.calls, state)
	}
}

// The patrol package delegates delivery through TurnRunner instead of owning a
// Herdr client, so it cannot focus panes or start terminal commands directly.
func TestPatrolDoesNotOwnHerdrInput(t *testing.T) {
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
				t.Errorf("%s owns Herdr input %q; delivery belongs to the CLI adapter", name, forbiddenName)
			}
		}
	}
}

// The patrol never resumes the live tech lead through an agent CLI. It only
// rings the already-running pane through its TurnRunner.
func TestPatrolNeverResumesTheLiveTLSession(t *testing.T) {
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
func TestTurnSkipsAndWarnsWhenTwoLiveTLsClaimTheProgram(t *testing.T) {
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
	runner := &recordingTurnRunner{}
	duplicates := []herdr.Agent{
		{PaneID: "p1", TerminalTitle: "relay:program:alpha - GitHub Copilot", Status: herdr.StatusIdle},
		{PaneID: "p2", TerminalTitle: "relay:program:alpha", Status: herdr.StatusWorking},
	}

	outcome := requestTLTurn(
		context.Background(), &state, attention("alpha", "fp-1"), duplicates, runner, nil, now,
	)
	if len(runner.requests) != 0 {
		t.Fatalf("a turn ran with duplicate tech leads: %+v", runner.requests)
	}
	if outcome.Kind != wakeDuplicate || !slices.Equal(outcome.Panes, []string{"p1", "p2"}) {
		t.Fatalf("duplicate outcome = %+v, want both pane IDs", outcome)
	}
	for _, want := range []string{
		`skipped the live tech lead doorbell for program "alpha"`, "2 live tech lead sessions", "p1", "p2",
	} {
		if !strings.Contains(outcome.Warning, want) {
			t.Errorf("warning %q is missing %q", outcome.Warning, want)
		}
	}

	if state.TLPresent {
		t.Error("TLPresent = true, want false while ownership is ambiguous")
	}
	if state.AttentionFingerprint != "" {
		t.Errorf("attention fingerprint = %q, want unarmed", state.AttentionFingerprint)
	}
}

func TestUncertainDoorbellSuppressesAllAutomaticRetriesUntilRestart(t *testing.T) {
	now := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	state := State{ProgramSlug: "alpha", Reasons: []Reason{}}
	uncertain := &recordingTurnRunner{results: []TurnResult{{
		Status: TurnUncertain,
		Error:  "terminal-targeted Enter was sent, but no new turn was observed",
	}}}
	outcome := requestTLTurn(
		context.Background(), &state, attention("alpha", "fp-1"), idleTL("alpha"), uncertain, nil, now,
	)
	if outcome.Warning == "" || !state.DoorbellSuppressed ||
		state.LastTurnStatus != string(TurnUncertain) {
		t.Fatalf("uncertain doorbell state = %+v warning = %q", state, outcome.Warning)
	}
	if outcome.Kind != wakeUncertain {
		t.Errorf("uncertain outcome kind = %q, want %q", outcome.Kind, wakeUncertain)
	}

	retry := &recordingTurnRunner{}
	outcome = requestTLTurn(
		context.Background(), &state, attention("alpha", "fp-2"), idleTL("alpha"), retry, nil,
		now.Add(3*time.Hour),
	)
	if len(retry.requests) != 0 {
		t.Fatalf("uncertain delivery retried with changed attention: %+v", retry.requests)
	}
	if outcome.Kind != wakeSuppressed {
		t.Errorf("suppressed retry kind = %q, want %q", outcome.Kind, wakeSuppressed)
	}
	for _, want := range []string{"suppressed", "inspect and clear", "restart"} {
		if !strings.Contains(outcome.Warning, want) {
			t.Errorf("suppression warning %q is missing %q", outcome.Warning, want)
		}
	}
}
