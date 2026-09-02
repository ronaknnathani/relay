package patrol

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
)

// syncBuffer collects patrol events written from the patrol goroutine while the
// test asserts on them.
type syncBuffer struct {
	mutex   sync.Mutex
	builder strings.Builder
}

func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.builder.Write(data)
}

func (b *syncBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.builder.String()
}

func (b *syncBuffer) lines() []string {
	value := strings.TrimSuffix(b.String(), "\n")
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}

func attentionSnapshot(reasons ...programview.DecisionDTO) SnapshotBuilder {
	return func(slug string, _ programview.Options) (programview.Snapshot, error) {
		ids := make([]string, 0, len(reasons))
		for _, decision := range reasons {
			ids = append(ids, decision.ID)
		}
		return programview.Snapshot{
			Program: programview.ProgramDTO{Slug: slug, State: string(program.StateActive)},
			Plan: programview.PlanDTO{
				Ready: []string{}, Blocked: []programview.BlockedPlanDTO{},
				Orphaned: []string{}, OpenDecisions: ids,
			},
			Items:         []programview.ItemDTO{},
			OpenDecisions: reasons,
		}, nil
	}
}

func waitForPatrolEvents(t *testing.T, buffer *syncBuffer, count int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		lines := buffer.lines()
		if len(lines) >= count {
			return lines
		}
		if time.Now().After(deadline) {
			t.Fatalf("patrol printed %d events, want %d:\n%s", len(lines), count, buffer.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunPrintsStartTickWakeAndShutdownToStdout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	start := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 2)}
	out, errOut := &syncBuffer{}, &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "events", Options{
			Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: attentionSnapshot(programview.DecisionDTO{ID: "d1"}),
			Agents: patrolAgentListerFunc(func() ([]herdr.Agent, error) {
				return []herdr.Agent{{
					PaneID: "pC", TerminalTitle: "relay:program:events - GitHub Copilot",
					Status: herdr.StatusIdle,
				}}, nil
			}),
			Turns: &recordingTurnRunner{},
			Out:   out, Err: errOut, Location: testDisplayZone,
		})
	}()

	waitForPatrolEvents(t, out, 4)
	shutdown := start.Add(20 * time.Minute)
	clock.Set(shutdown)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}

	want := []string{
		"2026-09-01T00:45:00-04:00 patrol started program=events",
		"2026-09-01T00:45:00-04:00 tick reasons=open-decision:d1 cadence=15m",
		"2026-09-01T00:45:00-04:00 TL wake delivered program=events pane=pC status=idle",
		"2026-09-01T00:45:00-04:00 next tick at=2026-09-01T01:00:00-04:00 cadence=15m",
		"2026-09-01T01:05:00-04:00 patrol stopped program=events reason=context canceled",
	}
	if got := out.lines(); !equalLines(got, want) {
		t.Errorf("patrol events =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if errOut.String() != "" {
		t.Errorf("a healthy patrol wrote to stderr: %q", errOut.String())
	}
}

func TestRunPrintsDegradedWakesToStderrWithoutRinging(t *testing.T) {
	for _, test := range []struct {
		name   string
		agents []herdr.Agent
		state  func(*recordingTurnRunner) []TurnResult
		want   string
	}{
		{
			name: "busy",
			agents: []herdr.Agent{{
				PaneID: "pA", TerminalTitle: "relay:program:degraded", Status: herdr.StatusWorking,
			}},
			want: "TL wake busy program=degraded pane=pA status=working",
		},
		{
			name:   "absent",
			agents: []herdr.Agent{},
			want:   "TL wake absent program=degraded",
		},
		{
			name: "duplicate",
			agents: []herdr.Agent{
				{PaneID: "p1", TerminalTitle: "relay:program:degraded", Status: herdr.StatusIdle},
				{PaneID: "p2", TerminalTitle: "relay:program:degraded", Status: herdr.StatusIdle},
			},
			want: "TL wake duplicate program=degraded panes=p1,p2",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			start := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
			clock := &lockedClock{now: start}
			ticker := &fakeTicker{channel: make(chan time.Time, 2)}
			out, errOut := &syncBuffer{}, &syncBuffer{}
			runner := &recordingTurnRunner{}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, "degraded", Options{
					Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
					BuildSnapshot: attentionSnapshot(programview.DecisionDTO{ID: "d1"}),
					Agents: patrolAgentListerFunc(func() ([]herdr.Agent, error) {
						return test.agents, nil
					}),
					Turns: runner, Out: out, Err: errOut,
				})
			}()

			waitForPatrolEvents(t, errOut, 1)
			cancel()
			<-done

			line := errOut.lines()[0]
			if !strings.Contains(line, "warning: "+test.want) ||
				!strings.Contains(line, "attention remains pending") {
				t.Errorf("stderr line = %q, want %q", line, test.want)
			}
			if len(runner.requests) != 0 {
				t.Errorf("a degraded wake still rang the tech lead: %+v", runner.requests)
			}
			if strings.Contains(out.String(), "TL wake") {
				t.Errorf("a degraded wake was printed to stdout: %q", out.String())
			}
		})
	}
}

func TestRunPrintsSuppressedWakeToStderr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	start := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 4)}
	out, errOut := &syncBuffer{}, &syncBuffer{}
	runner := &recordingTurnRunner{results: []TurnResult{{
		Status: TurnUncertain, Error: "no new turn was observed",
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "suppressed", Options{
			Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: attentionSnapshot(programview.DecisionDTO{ID: "d1"}),
			Agents: patrolAgentListerFunc(func() ([]herdr.Agent, error) {
				return []herdr.Agent{{
					PaneID: "pA", TerminalTitle: "relay:program:suppressed", Status: herdr.StatusIdle,
				}}, nil
			}),
			Turns: runner, Out: out, Err: errOut,
		})
	}()

	waitForPatrolEvents(t, errOut, 1)
	clock.Set(start.Add(16 * time.Minute))
	ticker.channel <- clock.Now()
	lines := waitForPatrolEvents(t, errOut, 2)
	cancel()
	<-done

	if !strings.Contains(lines[0], "TL wake uncertain program=suppressed pane=pA") {
		t.Errorf("first stderr line = %q, want an uncertain wake", lines[0])
	}
	if !strings.Contains(lines[1], "TL wake suppressed program=suppressed pane=pA") {
		t.Errorf("second stderr line = %q, want a suppressed wake", lines[1])
	}
	if len(runner.requests) != 1 {
		t.Errorf("suppressed patrol rang the tech lead %d times, want 1", len(runner.requests))
	}
	// The uncertain delivery detail belongs in patrol state, not in the pane.
	if strings.Contains(errOut.String(), "no new turn was observed") {
		t.Errorf("the pane printed the recorded turn detail: %q", errOut.String())
	}
}

func TestRunPrintsObservationAndHerdrFailuresToStderr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	start := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 4)}
	out, errOut := &syncBuffer{}, &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), "failing", Options{
			Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: func(string, programview.Options) (programview.Snapshot, error) {
				return programview.Snapshot{}, errors.New("source unavailable")
			},
			Out: out, Err: errOut,
		})
	}()

	waitForPatrolEvents(t, errOut, 1)
	for attempt := 2; attempt <= 3; attempt++ {
		clock.Set(start.Add(time.Duration(attempt-1) * 16 * time.Minute))
		ticker.channel <- clock.Now()
		// Each retry has to land before the clock moves again, or the patrol
		// reads one wakeup with the next tick's clock and skips a retry.
		waitForPatrolEvents(t, errOut, attempt)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil after repeated observation failures")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not fail after three observation failures")
	}

	lines := errOut.lines()
	if len(lines) != 4 {
		t.Fatalf("stderr lines = %d, want 4:\n%s", len(lines), errOut.String())
	}
	for _, line := range lines[:3] {
		if !strings.Contains(line, "error: patrol observation failed") ||
			!strings.Contains(line, "source unavailable") ||
			!strings.Contains(line, "retrying in 15m") {
			t.Errorf("observation failure line = %q", line)
		}
	}
	if !strings.Contains(lines[3], "error: patrol failed program=failing after 3 consecutive errors") {
		t.Errorf("final stderr line = %q", lines[3])
	}
	if strings.Contains(out.String(), "tick reasons") {
		t.Errorf("a failed observation printed a tick: %q", out.String())
	}
}

func TestRunPrintsHerdrAgentFailureToStderrAndKeepsTicking(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	start := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 2)}
	out, errOut := &syncBuffer{}, &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "herdr-down", Options{
			Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: attentionSnapshot(programview.DecisionDTO{ID: "d1"}),
			Agents: patrolAgentListerFunc(func() ([]herdr.Agent, error) {
				return nil, errors.New("connection refused")
			}),
			Out: out, Err: errOut,
		})
	}()

	waitForPatrolEvents(t, errOut, 1)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	line := errOut.lines()[0]
	if !strings.Contains(line, "error: list Herdr agents for patrol") ||
		!strings.Contains(line, "connection refused") ||
		!strings.Contains(line, "tech lead presence is unknown this tick") {
		t.Errorf("Herdr failure line = %q", line)
	}
	if !strings.Contains(out.String(), "next tick at=") {
		t.Errorf("patrol stopped ticking after a Herdr failure: %q", out.String())
	}
}

// The 30-second wakeup only checks the wall clock. Only a due tick is an event.
func TestRunPrintsNothingForEarlyTickerWakeups(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	start := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 8)}
	out, errOut := &syncBuffer{}, &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "quiet", Options{
			Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: attentionSnapshot(),
			Out:           out, Err: errOut,
		})
	}()

	waitForPatrolEvents(t, out, 4)
	for wakeup := 1; wakeup <= 4; wakeup++ {
		clock.Set(start.Add(time.Duration(wakeup) * 30 * time.Second))
		ticker.channel <- clock.Now()
	}
	clock.Set(start.Add(31 * time.Minute))
	ticker.channel <- clock.Now()
	waitForPatrolEvents(t, out, 7)
	cancel()
	<-done

	if got := strings.Count(out.String(), " tick reasons="); got != 2 {
		t.Errorf("tick events = %d, want 2 (one per due tick):\n%s", got, out.String())
	}
	if errOut.String() != "" {
		t.Errorf("idle patrol wrote to stderr: %q", errOut.String())
	}
}

// Reason text is program content: it can quote a mailbox body, a path, or an
// error. The pane only ever sees the code.
func TestRunPrintsReasonCodesWithoutReasonText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	start := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 2)}
	out, errOut := &syncBuffer{}, &syncBuffer{}
	secret := "open /home/ceo/.config/relay/token.json: permission denied"
	builder := func(slug string, _ programview.Options) (programview.Snapshot, error) {
		snapshot := programview.Snapshot{
			Program: programview.ProgramDTO{Slug: slug, State: string(program.StateActive)},
			Plan: programview.PlanDTO{
				Ready: []string{"w2"}, Blocked: []programview.BlockedPlanDTO{},
				Orphaned: []string{}, OpenDecisions: []string{},
			},
			Items:         []programview.ItemDTO{},
			OpenDecisions: []programview.DecisionDTO{},
		}
		snapshot.SourceHealth.Projects.Warnings = []string{"load active project: " + secret}
		return snapshot, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "private", Options{
			Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: builder, Out: out, Err: errOut,
		})
	}()

	waitForPatrolEvents(t, out, 3)
	waitForPatrolEvents(t, errOut, 1)
	cancel()
	<-done

	printed := out.String() + errOut.String()
	if !strings.Contains(printed, "tick reasons=project-warning,ready-item:w2 cadence=15m") {
		t.Errorf("tick line did not print safe codes: %q", printed)
	}
	for _, leak := range []string{secret, "token.json", "/home/ceo", "permission denied"} {
		if strings.Contains(printed, leak) {
			t.Errorf("patrol events leaked %q:\n%s", leak, printed)
		}
	}

	// The full warning text still reaches the operator through patrol state.
	state, err := ReadState("private")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, reason := range state.Reasons {
		if strings.Contains(reason.Text, secret) {
			found = true
		}
	}
	if !found {
		t.Errorf("patrol state lost the warning detail: %+v", state.Reasons)
	}
}

func TestRunStopsWhenAnEventCannotBeWritten(t *testing.T) {
	for _, test := range []struct {
		name  string
		after int
		want  string
	}{
		{name: "start event", want: "patrol started program=broken-writer"},
		{name: "tick event", after: 1, want: "tick reasons="},
		{name: "next tick event", after: 3, want: "next tick at="},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			start := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
			clock := &lockedClock{now: start}
			ticker := &fakeTicker{channel: make(chan time.Time, 2)}
			errOut := &syncBuffer{}
			done := make(chan error, 1)
			go func() {
				done <- Run(context.Background(), "broken-writer", Options{
					Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
					BuildSnapshot: attentionSnapshot(programview.DecisionDTO{ID: "d1"}),
					Agents: patrolAgentListerFunc(func() ([]herdr.Agent, error) {
						return []herdr.Agent{{
							PaneID: "pA", TerminalTitle: "relay:program:broken-writer",
							Status: herdr.StatusIdle,
						}}, nil
					}),
					Turns: &recordingTurnRunner{},
					Out:   &failingWriter{err: errors.New("broken pipe"), after: test.after},
					Err:   errOut,
				})
			}()

			select {
			case err := <-done:
				if err == nil {
					t.Fatal("Run kept going with an unwritable patrol pane")
				}
				for _, want := range []string{"write patrol event", test.want, "broken pipe"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("Run error %q is missing %q", err, want)
					}
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Run did not stop when its events could not be written")
			}

			state, err := ReadState("broken-writer")
			if err != nil {
				t.Fatal(err)
			}
			if state.Status != StatusFailed || !strings.Contains(state.Error, "write patrol event") {
				t.Fatalf("unwritable pane left state %+v", state)
			}
		})
	}
}

func TestRunReportsAndPropagatesRuntimeStateWriteFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// A directory where the state file belongs makes the atomic replace fail
	// the same way a broken runtime directory would.
	if err := os.MkdirAll(StatePath("state-broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, errOut := &syncBuffer{}, &syncBuffer{}
	err := Run(context.Background(), "state-broken", Options{
		Now:           func() time.Time { return time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC) },
		Ticker:        func(time.Duration) Ticker { return &fakeTicker{channel: make(chan time.Time, 1)} },
		BuildSnapshot: attentionSnapshot(),
		Out:           out, Err: errOut,
	})
	if err == nil {
		t.Fatal("Run ignored an unwritable patrol state file")
	}
	if !strings.Contains(err.Error(), "patrol state") {
		t.Errorf("Run error = %v, want the state write failure", err)
	}
	if !strings.Contains(errOut.String(), "error: write patrol runtime state") {
		t.Errorf("stderr = %q, want the state write failure", errOut.String())
	}
	if !strings.Contains(out.String(), "patrol started program=state-broken") {
		t.Errorf("stdout = %q, want the start event", out.String())
	}
}

func equalLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// The pane is human output and the runtime record is machine state. A patrol
// that prints the reader's wall clock must still record UTC: the next tick is
// decided by comparing recorded values, so a record that drifted into local
// time would silently change the cadence.
func TestRunStampsThePaneLocallyAndKeepsTheRecordInUTC(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	start := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 2)}
	out, errOut := &syncBuffer{}, &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "zones", Options{
			Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: attentionSnapshot(programview.DecisionDTO{ID: "d1"}),
			Agents: patrolAgentListerFunc(func() ([]herdr.Agent, error) {
				return []herdr.Agent{{
					PaneID: "pC", TerminalTitle: "relay:program:zones - GitHub Copilot",
					Status: herdr.StatusIdle,
				}}, nil
			}),
			Turns: &recordingTurnRunner{},
			Out:   out, Err: errOut, Location: testDisplayZone,
		})
	}()
	waitForPatrolEvents(t, out, 4)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}

	if lines := out.lines(); !strings.HasPrefix(lines[0], "2026-09-01T00:45:00-04:00 ") {
		t.Errorf("first event = %q, want the reader's wall clock", lines[0])
	}
	state, err := ReadState("zones")
	if err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]string{
		"StartedAt":  state.StartedAt,
		"UpdatedAt":  state.UpdatedAt,
		"LastTickAt": state.LastTickAt,
	} {
		if value == "" {
			t.Errorf("recorded %s is empty", field)
			continue
		}
		if !strings.HasSuffix(value, "Z") {
			t.Errorf("recorded %s = %q, want a UTC record", field, value)
		}
	}
	recorded, err := os.ReadFile(StatePath("zones"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(recorded), "-04:00") {
		t.Errorf("patrol.json carries the reader's offset:\n%s", recorded)
	}
}
