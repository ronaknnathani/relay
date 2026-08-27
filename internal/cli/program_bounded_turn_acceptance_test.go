package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/patrol"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programturn"
	"github.com/ronaknnathani/relay/internal/project"
)

type acceptanceTicker struct {
	channel chan time.Time
}

func (a *acceptanceTicker) C() <-chan time.Time { return a.channel }
func (a *acceptanceTicker) Stop()               {}

type acceptanceClock struct {
	mutex sync.Mutex
	now   time.Time
}

func (c *acceptanceClock) Now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.now
}

func (c *acceptanceClock) Set(now time.Time) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.now = now
}

// TestPatrolWakesTheCTOThroughBoundedTurnsEndToEnd drives the real patrol loop
// against a real program, real unread worker mail, the real writer lock, and the
// real runtime turn records, with only the agent process faked. No coding agent
// is ever launched.
func TestPatrolWakesTheCTOThroughBoundedTurnsEndToEnd(t *testing.T) {
	p, childDir := createBoundedTurnFixture(t)
	saveProgramTestConfig(t)
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{
		append(idleCTOAgents(p.Slug), herdr.Agent{
			PaneID: "worker-w1", TerminalTitle: "relay:adaptive-w1",
			Status: herdr.StatusWorking,
			CWD:    filepath.Join(p.Repo, ".worktrees", "adaptive-w1"),
		}),
	}}
	turn := &fakeTurn{}
	installCTOTurnFakes(t, client, turn)

	start := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	clock := &acceptanceClock{now: start}
	ticker := &acceptanceTicker{channel: make(chan time.Time, 8)}
	sendBoundedTurnMail(t, p.Slug, childDir, "m-1")

	programDir := program.ProgramDir(program.ActiveDir(), p.Slug)
	beforePatrol := snapshotDir(t, programDir)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- patrol.Run(ctx, p.Slug, patrol.Options{
			Now:          clock.Now,
			Ticker:       func(time.Duration) patrol.Ticker { return ticker },
			Agents:       client,
			Turns:        boundedCTOTurnRunner{},
			RelayVersion: "test",
		})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("patrol did not stop after cancellation")
		}
	}()

	// One unread outbox message wakes exactly one bounded turn.
	state := waitForAcceptanceState(t, p.Slug, func(state patrol.State) bool {
		return state.LastTurnStatus == string(patrol.TurnSucceeded) && state.AttentionFingerprint != ""
	})
	if turn.calls() != 1 {
		t.Fatalf("bounded turns = %d, want 1", turn.calls())
	}
	if !state.CTOPresent || state.LastTurnSessionID != "session-1" || state.LastTurnLogPath == "" {
		t.Fatalf("state after the first turn = %+v", state)
	}
	armed := state.AttentionFingerprint

	// Unchanged attention does not re-run the turn before the two-hour rearm.
	advanceAcceptanceTick(clock, ticker, start.Add(31*time.Minute))
	waitForAcceptanceState(t, p.Slug, func(state patrol.State) bool {
		return state.LastTickAt == start.Add(31*time.Minute).Format(time.RFC3339)
	})
	if turn.calls() != 1 {
		t.Fatalf("unchanged attention ran %d turns, want 1", turn.calls())
	}

	// A second message on the same item changes the fingerprint immediately.
	sendBoundedTurnMail(t, p.Slug, childDir, "m-2")
	advanceAcceptanceTick(clock, ticker, start.Add(62*time.Minute))
	state = waitForAcceptanceState(t, p.Slug, func(state patrol.State) bool {
		return state.AttentionFingerprint != "" && state.AttentionFingerprint != armed
	})
	if turn.calls() != 2 {
		t.Fatalf("new mail on the same item ran %d turns, want 2", turn.calls())
	}
	rearmed := state.AttentionFingerprint

	// A failing turn leaves attention unarmed and warns without stopping.
	turn.setResult(agent.HeadlessTurnResult{ExitCode: 1, Error: "boom"})
	sendBoundedTurnMail(t, p.Slug, childDir, "m-3")
	advanceAcceptanceTick(clock, ticker, start.Add(93*time.Minute))
	state = waitForAcceptanceState(t, p.Slug, func(state patrol.State) bool {
		return state.LastTurnStatus == string(patrol.TurnFailed)
	})
	// A failed turn does not arm the new attention: the fingerprint still names
	// the last successful turn, so the next tick retries the same work.
	if state.AttentionFingerprint != rearmed || state.TurnFailures != 1 || state.Warning == "" ||
		state.Status != patrol.StatusRunning || state.Error != "" {
		t.Fatalf("failed turn state = %+v", state)
	}

	// Draining attention clears the fingerprint and the failure budget.
	turn.setResult(agent.HeadlessTurnResult{})
	ackBoundedTurnMail(t, childDir, "m-1", "m-2", "m-3")
	advanceAcceptanceTick(clock, ticker, start.Add(124*time.Minute))
	state = waitForAcceptanceState(t, p.Slug, func(state patrol.State) bool {
		return state.TurnFailures == 0 && state.AttentionFingerprint == "" &&
			state.LastTickAt == start.Add(124*time.Minute).Format(time.RFC3339)
	})
	if state.LastNotifiedAt != "" {
		t.Fatalf("drained state kept a notification time: %+v", state)
	}

	// Runtime history recorded every attempt outside the program directory.
	history, err := programturn.Read(p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Turns) != 3 {
		t.Fatalf("recorded turns = %d, want 3", len(history.Turns))
	}
	if _, err := os.Stat(filepath.Join(programDir, "turns.json")); !os.IsNotExist(err) {
		t.Fatalf("turn history leaked into the program directory: %v", err)
	}
	// The patrol and its transport are read-only toward governed state: only the
	// CLI commands a bounded turn's agent runs may write the program directory.
	if after := snapshotDir(t, programDir); !equalSnapshots(beforePatrol, after) {
		t.Fatal("the patrol or its transport wrote to the program directory")
	}
	if len(client.prompted) != 0 || len(client.focused) != 0 || len(client.runPane) != 0 {
		t.Fatalf("the transport touched the CTO pane: %+v %+v %+v",
			client.prompted, client.focused, client.runPane)
	}
}

func advanceAcceptanceTick(clock *acceptanceClock, ticker *acceptanceTicker, to time.Time) {
	clock.Set(to)
	ticker.channel <- to
}

func waitForAcceptanceState(t *testing.T, slug string, ready func(patrol.State) bool) patrol.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last patrol.State
	for {
		state, err := patrol.ReadState(slug)
		if err == nil {
			last = state
			if ready(state) {
				return state
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("patrol state never became ready: %+v", last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func createBoundedTurnFixture(t *testing.T) (program.Program, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	p, err := program.New("adaptive", "Adaptive", repo, "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "Build it", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	childSlug := "adaptive-" + item.ID
	if err := p.DispatchItem(item.ID, childSlug); err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	childDir := filepath.Join(project.ActiveDir(), childSlug)
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(repo, ".worktrees", childSlug)
	at := "2026-08-27T09:00:00Z"
	manifest := project.Manifest{
		Slug: childSlug, Title: "Build it", Repo: repo, Branch: "feature",
		BaseBranch: "main", Worktree: &worktree, Status: "active",
		Workflow: "deliver-pr", Phase: "implement", Created: at, Updated: at,
		PhasesCompleted: []string{}, PhasesRemaining: []string{},
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), childSlug), manifest); err != nil {
		t.Fatal(err)
	}
	if err := mailbox.Ensure(childDir); err != nil {
		t.Fatal(err)
	}
	return p, childDir
}

func sendBoundedTurnMail(t *testing.T, programSlug, childDir, id string) {
	t.Helper()
	if _, err := mailbox.Send(childDir, mailbox.Outbox, mailbox.Message{
		ID: id, Kind: mailbox.KindQuestion, Program: programSlug, Item: "w1",
		From: mailbox.ActorWorker, To: mailbox.ActorCTO, Body: "ready?",
		Options: []string{}, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
}

func ackBoundedTurnMail(t *testing.T, childDir string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := mailbox.Acknowledge(childDir, mailbox.Outbox, id); err != nil {
			t.Fatal(err)
		}
	}
}

// The bounded turn transport is the only wake path. No Relay code the patrol
// reaches may prompt or focus the CEO-facing CTO pane, or resume its session.
func TestPatrolCLINeverPromptsTheCTO(t *testing.T) {
	for path, forbidden := range map[string][]string{
		"program_patrol.go":   {"PromptAgent", "FocusAgent", "ctoDoorbell", "AgentPrompter"},
		"program_cto_turn.go": {"PromptAgent", "FocusAgent", "RunPane", "--resume"},
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range forbidden {
			if strings.Contains(string(body), name) {
				t.Errorf("%s references forbidden CTO input %q", path, name)
			}
		}
	}
}
