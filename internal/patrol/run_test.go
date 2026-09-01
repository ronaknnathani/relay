package patrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
)

type fakeTicker struct {
	channel chan time.Time
}

func (f *fakeTicker) C() <-chan time.Time { return f.channel }
func (f *fakeTicker) Stop()               {}

type lockedClock struct {
	mutex sync.Mutex
	now   time.Time
}

func (c *lockedClock) Now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.now
}

func (c *lockedClock) Set(now time.Time) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.now = now
}

func TestRunTicksImmediatelyUsesWallClockWithoutBurstAndStopsArchived(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	start := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 8)}
	var calls atomic.Int32
	var archived atomic.Bool
	builder := func(slug string, _ programview.Options) (programview.Snapshot, error) {
		calls.Add(1)
		return programview.Snapshot{
			Program: programview.ProgramDTO{
				Slug: slug, State: string(program.StateActive), Archived: archived.Load(),
			},
			Plan: programview.PlanDTO{
				Ready: []string{}, Blocked: []programview.BlockedPlanDTO{},
				Orphaned: []string{}, OpenDecisions: []string{},
			},
			Items:         []programview.ItemDTO{},
			OpenDecisions: []programview.DecisionDTO{},
		}, nil
	}
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), "wall-clock", Options{
			Now: clock.Now,
			Ticker: func(interval time.Duration) Ticker {
				if interval != 30*time.Second {
					t.Errorf("ticker interval = %s", interval)
				}
				return ticker
			},
			BuildSnapshot: builder,
			PID:           1234,
			RelayVersion:  "v-test",
		})
	}()

	waitForPatrolState(t, "wall-clock", func(state State) bool {
		return state.Status == StatusRunning && calls.Load() == 1 &&
			state.NextTickAt == start.Add(30*time.Minute).Format(time.RFC3339)
	})
	clock.Set(start.Add(29 * time.Minute))
	ticker.channel <- clock.Now()
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("early wakeup calls = %d, want 1", got)
	}

	jumped := start.Add(2 * time.Hour)
	clock.Set(jumped)
	ticker.channel <- jumped
	waitForPatrolState(t, "wall-clock", func(state State) bool {
		return calls.Load() == 2 &&
			state.NextTickAt == jumped.Add(30*time.Minute).Format(time.RFC3339)
	})
	ticker.channel <- jumped
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 2 {
		t.Fatalf("wall-clock catch-up burst calls = %d, want 2", got)
	}

	archived.Store(true)
	clock.Set(jumped.Add(31 * time.Minute))
	ticker.channel <- clock.Now()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after archive")
	}
	state, err := ReadState("wall-clock")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusStopped || state.StopReason != "program archived" ||
		state.PID != 1234 || state.RelayVersion != "v-test" {
		t.Fatalf("stopped state = %+v", state)
	}
}

func TestRunFailsAfterThreeSameClassErrorsWithFifteenMinuteRetries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	start := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 4)}
	builder := func(string, programview.Options) (programview.Snapshot, error) {
		return programview.Snapshot{}, errors.New("source unavailable")
	}
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), "errors", Options{
			Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: builder,
		})
	}()
	waitForPatrolState(t, "errors", func(state State) bool {
		return state.Status == StatusRunning && state.ConsecutiveErrors == 1 &&
			state.DelaySeconds == 900 &&
			state.NextTickAt == start.Add(15*time.Minute).Format(time.RFC3339)
	})
	for count := 2; count <= 3; count++ {
		clock.Set(clock.Now().Add(15 * time.Minute))
		ticker.channel <- clock.Now()
		if count < 3 {
			waitForPatrolState(t, "errors", func(state State) bool {
				return state.Status == StatusRunning && state.ConsecutiveErrors == count
			})
		}
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil after repeated errors")
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not fail after third error")
	}
	state, err := ReadState("errors")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusFailed || state.ConsecutiveErrors != 3 ||
		state.DelaySeconds != 900 || state.Error == "" {
		t.Fatalf("failed state = %+v", state)
	}
}

func TestRunKeepsTickingWhenHerdrBecomesUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	start := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 4)}
	var calls atomic.Int32
	builder := func(slug string, _ programview.Options) (programview.Snapshot, error) {
		calls.Add(1)
		return programview.Snapshot{
			Program: programview.ProgramDTO{Slug: slug, State: string(program.StateActive)},
			Plan: programview.PlanDTO{
				Ready: []string{}, Blocked: []programview.BlockedPlanDTO{},
				Orphaned: []string{}, OpenDecisions: []string{},
			},
			Items:         []programview.ItemDTO{},
			OpenDecisions: []programview.DecisionDTO{},
		}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "herdr-degraded", Options{
			Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: builder,
			Agents: patrolAgentListerFunc(func() ([]herdr.Agent, error) {
				return nil, errors.New("herdr unavailable")
			}),
		})
	}()

	waitForPatrolState(t, "herdr-degraded", func(state State) bool {
		return calls.Load() == 1 && state.Status == StatusRunning
	})
	for tick := 2; tick <= 4; tick++ {
		clock.Set(start.Add(time.Duration(tick-1) * 31 * time.Minute))
		ticker.channel <- clock.Now()
		waitForPatrolState(t, "herdr-degraded", func(state State) bool {
			return calls.Load() == int32(tick) && state.Status == StatusRunning
		})
	}
	state, err := ReadState("herdr-degraded")
	if err != nil {
		t.Fatal(err)
	}
	if state.ConsecutiveErrors != 0 || state.Error != "" || state.TLPresent {
		t.Fatalf("degraded Herdr became fatal patrol state: %+v", state)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunWithoutHerdrKeepsTicking(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	start := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 2)}
	var calls atomic.Int32
	builder := func(slug string, _ programview.Options) (programview.Snapshot, error) {
		calls.Add(1)
		return programview.Snapshot{
			Program: programview.ProgramDTO{Slug: slug, State: string(program.StateActive)},
			Plan: programview.PlanDTO{
				Ready: []string{}, Blocked: []programview.BlockedPlanDTO{},
				Orphaned: []string{}, OpenDecisions: []string{},
			},
			Items:         []programview.ItemDTO{},
			OpenDecisions: []programview.DecisionDTO{},
		}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "no-herdr", Options{
			Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: builder,
		})
	}()

	waitForPatrolState(t, "no-herdr", func(state State) bool {
		return calls.Load() == 1 && state.Status == StatusRunning
	})
	for tick := 2; tick <= 3; tick++ {
		clock.Set(start.Add(time.Duration(tick-1) * 31 * time.Minute))
		ticker.channel <- clock.Now()
		waitForPatrolState(t, "no-herdr", func(state State) bool {
			return calls.Load() == int32(tick) && state.Status == StatusRunning
		})
	}
	state, err := ReadState("no-herdr")
	if err != nil {
		t.Fatal(err)
	}
	if state.ConsecutiveErrors != 0 || state.Error != "" || state.Warning != "" || state.TLPresent {
		t.Fatalf("no-Herdr patrol state = %+v", state)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunKeepsTickingWhenBoundedTurnsFail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	start := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 3)}
	var calls atomic.Int32
	builder := func(slug string, _ programview.Options) (programview.Snapshot, error) {
		calls.Add(1)
		return programview.Snapshot{
			Program: programview.ProgramDTO{Slug: slug, State: string(program.StateActive)},
			Plan: programview.PlanDTO{
				Ready: []string{}, Blocked: []programview.BlockedPlanDTO{},
				Orphaned: []string{}, OpenDecisions: []string{"d1"},
			},
			Items: []programview.ItemDTO{},
			OpenDecisions: []programview.DecisionDTO{{
				ID: "d1",
			}},
		}, nil
	}
	agents := patrolAgentListerFunc(func() ([]herdr.Agent, error) {
		return []herdr.Agent{{
			PaneID: "tl", TerminalTitle: "relay:program:doorbell-degraded",
			Status: herdr.StatusIdle,
		}}, nil
	})
	runner := &recordingTurnRunner{
		results: []TurnResult{
			{Status: TurnFailed, Error: "one"},
			{Status: TurnFailed, Error: "two"},
			{Status: TurnFailed, Error: "three"},
			{Status: TurnFailed, Error: "four"},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "doorbell-degraded", Options{
			Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: builder, Agents: agents, Turns: runner,
		})
	}()

	waitForPatrolState(t, "doorbell-degraded", func(state State) bool {
		return calls.Load() == 1 && state.Status == StatusRunning
	})
	for tick := 2; tick <= 4; tick++ {
		clock.Set(start.Add(time.Duration(tick-1) * 16 * time.Minute))
		ticker.channel <- clock.Now()
		waitForPatrolState(t, "doorbell-degraded", func(state State) bool {
			return calls.Load() == int32(tick) && state.Status == StatusRunning
		})
	}
	state, err := ReadState("doorbell-degraded")
	if err != nil {
		t.Fatal(err)
	}
	if state.ConsecutiveErrors != 0 || state.Error != "" || state.Warning == "" || !state.TLPresent {
		t.Fatalf("failed bounded turn patrol state = %+v", state)
	}
	if state.AttentionFingerprint != "" || state.LastTurnStatus != string(TurnFailed) {
		t.Fatalf("failed bounded turn armed attention: %+v", state)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunClearsNotificationStateOnDrainBeforeHerdrFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	start := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 2)}
	var snapshots atomic.Int32
	builder := func(slug string, _ programview.Options) (programview.Snapshot, error) {
		call := snapshots.Add(1)
		openDecisions := []programview.DecisionDTO{}
		decisionIDs := []string{}
		if call != 2 {
			openDecisions = []programview.DecisionDTO{{ID: "d1"}}
			decisionIDs = []string{"d1"}
		}
		return programview.Snapshot{
			Program: programview.ProgramDTO{Slug: slug, State: string(program.StateActive)},
			Plan: programview.PlanDTO{
				Ready: []string{}, Blocked: []programview.BlockedPlanDTO{},
				Orphaned: []string{}, OpenDecisions: decisionIDs,
			},
			Items:         []programview.ItemDTO{},
			OpenDecisions: openDecisions,
		}, nil
	}
	var agentCalls atomic.Int32
	agents := patrolAgentListerFunc(func() ([]herdr.Agent, error) {
		if agentCalls.Add(1) == 2 {
			return nil, errors.New("herdr unavailable during drain")
		}
		return []herdr.Agent{{
			PaneID: "tl", TerminalTitle: "relay:program:drain-recur",
			Status: herdr.StatusIdle,
		}}, nil
	})
	runner := &recordingTurnRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "drain-recur", Options{
			Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: builder, Agents: agents, Turns: runner,
		})
	}()

	waitForPatrolState(t, "drain-recur", func(state State) bool {
		return snapshots.Load() == 1 && state.AttentionFingerprint != ""
	})
	clock.Set(start.Add(16 * time.Minute))
	ticker.channel <- clock.Now()
	waitForPatrolState(t, "drain-recur", func(state State) bool {
		return snapshots.Load() == 2 && state.AttentionFingerprint == "" && state.LastNotifiedAt == ""
	})
	clock.Set(start.Add(47 * time.Minute))
	ticker.channel <- clock.Now()
	waitForPatrolState(t, "drain-recur", func(state State) bool {
		return snapshots.Load() == 3 &&
			state.LastNotifiedAt == clock.Now().Format(time.RFC3339)
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if len(runner.requests) != 2 {
		t.Fatalf("drain-then-recur bounded turns = %d, want 2", len(runner.requests))
	}
}

func TestRunStopsCleanlyWhenActiveProgramIsArchived(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	p, err := program.New("archive-mid-run", "Archive", repo, "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 1)}
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), p.Slug, Options{
			Now: clock.Now, Ticker: func(time.Duration) Ticker { return ticker },
			Agents: patrolAgentListerFunc(func() ([]herdr.Agent, error) { return []herdr.Agent{}, nil }),
		})
	}()
	waitForPatrolState(t, p.Slug, func(state State) bool {
		return state.Status == StatusRunning && state.LastTickAt != ""
	})
	if err := program.Archive(p.Slug); err != nil {
		t.Fatal(err)
	}
	clock.Set(start.Add(31 * time.Minute))
	ticker.channel <- clock.Now()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after program archive")
	}
	state, err := ReadState(p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusStopped || state.StopReason != "program archived" {
		t.Fatalf("archived stop state = %+v", state)
	}
	for _, name := range []string{"patrol.json", "patrol.lock"} {
		path := filepath.Join(program.ProgramDir(program.ArchivedDir(), p.Slug), name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("archived program contains runtime file %s: %v", name, err)
		}
	}
}

func waitForPatrolState(t *testing.T, slug string, ready func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		state, err := ReadState(slug)
		if err == nil && ready(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("patrol state did not become ready: last state %+v err %v", state, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
