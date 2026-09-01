package cli

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/patrol"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

type liveAcceptanceTicker struct {
	channel chan time.Time
}

func (a *liveAcceptanceTicker) C() <-chan time.Time { return a.channel }
func (a *liveAcceptanceTicker) Stop()               {}

type liveAcceptanceClock struct {
	mutex sync.Mutex
	now   time.Time
}

func (c *liveAcceptanceClock) Now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.now
}

func (c *liveAcceptanceClock) Set(now time.Time) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.now = now
}

func TestPatrolWakesTheExistingCTOPaneWithoutStartingASession(t *testing.T) {
	p, childDir := createLiveCTOFixture(t)
	cto := herdr.Agent{
		PaneID: "cto-pane", TerminalTitle: "relay:program:" + p.Slug + " - GitHub Copilot",
		Status: herdr.StatusIdle, NativeSessionID: "existing-session",
	}
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{cto}}}
	sendLiveCTOMail(t, p.Slug, childDir, "m-1")

	start := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	clock := &liveAcceptanceClock{now: start}
	ticker := &liveAcceptanceTicker{channel: make(chan time.Time, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- patrol.Run(ctx, p.Slug, patrol.Options{
			Now:          clock.Now,
			Ticker:       func(time.Duration) patrol.Ticker { return ticker },
			Agents:       client,
			Turns:        liveCTOTurnRunner{client: client},
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

	state := waitForLiveAcceptanceState(t, p.Slug, func(state patrol.State) bool {
		return state.LastTurnStatus == string(patrol.TurnSucceeded) &&
			state.AttentionFingerprint != ""
	})
	if !state.CTOPresent || state.LastTurnSessionID != "" || state.LastTurnLogPath != "" {
		t.Fatalf("live doorbell state = %+v", state)
	}
	if len(client.prompted) != 1 || client.prompted[0] != (fakePrompt{
		target: "cto-pane", text: liveCTODoorbell,
	}) {
		t.Fatalf("CTO prompts = %#v", client.prompted)
	}
	if len(client.created) != 0 || len(client.runPane) != 0 || len(client.focused) != 0 {
		t.Fatalf("patrol started or focused a session: created=%+v run=%+v focused=%+v",
			client.created, client.runPane, client.focused)
	}

	clock.Set(start.Add(31 * time.Minute))
	ticker.channel <- clock.Now()
	waitForLiveAcceptanceState(t, p.Slug, func(state patrol.State) bool {
		return state.LastTickAt == start.Add(31*time.Minute).Format(time.RFC3339)
	})
	if len(client.prompted) != 1 {
		t.Fatalf("unchanged attention prompted %d times, want 1", len(client.prompted))
	}
}

func TestPatrolSuppressesRetriesAfterUnconfirmedLiveDoorbell(t *testing.T) {
	p, childDir := createLiveCTOFixture(t)
	cto := herdr.Agent{
		PaneID: "cto-pane", TerminalTitle: "relay:program:" + p.Slug,
		Status: herdr.StatusDone, NativeSessionID: "existing-session",
	}
	client := &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{cto}},
		promptErr:      herdr.ErrPromptDeliveryUncertain,
	}
	sendLiveCTOMail(t, p.Slug, childDir, "m-1")

	start := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	clock := &liveAcceptanceClock{now: start}
	ticker := &liveAcceptanceTicker{channel: make(chan time.Time, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- patrol.Run(ctx, p.Slug, patrol.Options{
			Now:          clock.Now,
			Ticker:       func(time.Duration) patrol.Ticker { return ticker },
			Agents:       client,
			Turns:        liveCTOTurnRunner{client: client},
			RelayVersion: "test",
		})
	}()
	defer func() {
		cancel()
		<-done
	}()

	waitForLiveAcceptanceState(t, p.Slug, func(state patrol.State) bool {
		return state.LastTurnStatus == string(patrol.TurnUncertain) &&
			state.DoorbellSuppressed
	})
	sendLiveCTOMail(t, p.Slug, childDir, "m-2")
	clock.Set(start.Add(31 * time.Minute))
	ticker.channel <- clock.Now()
	state := waitForLiveAcceptanceState(t, p.Slug, func(state patrol.State) bool {
		return state.LastTickAt == start.Add(31*time.Minute).Format(time.RFC3339)
	})
	if len(client.prompted) != 1 {
		t.Fatalf("uncertain delivery prompted %d times, want 1", len(client.prompted))
	}
	if state.Warning == "" || !state.DoorbellSuppressed {
		t.Fatalf("uncertain delivery state = %+v", state)
	}
}

func TestProgramCTOHeadlessTurnCommandIsUnavailable(t *testing.T) {
	if _, err := runProgramCommand(t, "cto", "turn", "adaptive"); err == nil {
		t.Fatal("the retired headless CTO turn command is still registered")
	}
}

func createLiveCTOFixture(t *testing.T) (program.Program, string) {
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
	at := "2026-08-31T12:00:00Z"
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

func sendLiveCTOMail(t *testing.T, programSlug, childDir, id string) {
	t.Helper()
	if _, err := mailbox.Send(childDir, mailbox.Outbox, mailbox.Message{
		ID: id, Kind: mailbox.KindQuestion, Program: programSlug, Item: "w1",
		From: mailbox.ActorWorker, To: mailbox.ActorTL, Body: "ready?",
		Options: []string{}, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
}

func waitForLiveAcceptanceState(
	t *testing.T,
	slug string,
	ready func(patrol.State) bool,
) patrol.State {
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
