package patrol

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/project"
)

type mergedPRIndex struct {
	state programview.PRState
}

func (i mergedPRIndex) Lookup(string) (programview.PRState, bool) {
	return i.state, true
}

// A merged pull request is authoritative GitHub state, not something the CEO or
// the tech lead has to record first. programview reconciles it in memory on
// every snapshot, so the patrol sees the dependent item become ready and rings
// the live tech lead once—without any `relay program tick` having been run and
// without the patrol itself writing a single program file.
func TestMergedPullRequestUnlocksDependentWorkAndWakesTheLiveTL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	p := mergeProgressionProgram(t, repo)
	manifestPath := program.ManifestPath(program.ActiveDir(), p.Slug)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 4)}
	out, errOut := &syncBuffer{}, &syncBuffer{}
	runner := &recordingTurnRunner{}
	tl := []herdr.Agent{{
		PaneID: "pTL", TerminalTitle: "relay:program:" + p.Slug + " - GitHub Copilot",
		Status: herdr.StatusIdle, NativeSessionID: "existing-session",
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, p.Slug, Options{
			Now:    clock.Now,
			Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: func(slug string, options programview.Options) (programview.Snapshot, error) {
				options.PRIndex = mergedPRIndex{state: programview.PRStateMerged}
				return programview.Build(slug, options)
			},
			Agents: patrolAgentListerFunc(func() ([]herdr.Agent, error) { return tl, nil }),
			Turns:  runner,
			Out:    out, Err: errOut,
		})
	}()

	state := waitForPatrolState(t, p.Slug, func(state State) bool {
		return state.LastTurnStatus == string(TurnSucceeded)
	})
	if !hasReason(state.Reasons, "ready-item:w2") {
		t.Fatalf("patrol reasons = %+v, want the dependent item to be ready", state.Reasons)
	}
	if len(runner.requests) != 1 || runner.requests[0].PaneID != "pTL" {
		t.Fatalf("live tech lead wakes = %+v, want exactly one to pane pTL", runner.requests)
	}
	if !state.TLPresent || state.AttentionFingerprint == "" {
		t.Fatalf("patrol state = %+v", state)
	}

	// A second scheduled tick observes the same attention and must not ring the
	// same tech lead again.
	clock.Set(start.Add(16 * time.Minute))
	ticker.channel <- clock.Now()
	waitForPatrolState(t, p.Slug, func(state State) bool {
		return state.LastTickAt == clock.Now().Format(time.RFC3339)
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("unchanged attention rang the tech lead %d times, want 1", len(runner.requests))
	}

	// The pane shows the progression: a ready dependent item and a delivered
	// wake, with no reason text and no session identity.
	events := out.String()
	if !strings.Contains(events, "tick reasons=ready-item:w2 cadence=15m") {
		t.Errorf("patrol events did not report the ready item:\n%s", events)
	}
	if !strings.Contains(events, "TL wake delivered program="+p.Slug+" pane=pTL status=idle") {
		t.Errorf("patrol events did not report the delivered wake:\n%s", events)
	}
	for _, leak := range []string{"existing-session", "ready to dispatch", state.AttentionFingerprint} {
		if strings.Contains(events+errOut.String(), leak) {
			t.Errorf("patrol events leaked %q:\n%s", leak, events+errOut.String())
		}
	}

	// The patrol is read-only: the merge is reconciled in the snapshot, never
	// persisted, so the program file is byte-for-byte what it was.
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("patrol rewrote the program manifest:\nbefore\n%s\nafter\n%s", before, after)
	}
	stored, err := program.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := stored.Item("w1")
	second, _ := stored.Item("w2")
	if first.Status != program.ItemDispatched || second.Status != program.ItemPending {
		t.Fatalf("stored items = %s/%s, want the recorded statuses untouched", first.Status, second.Status)
	}
}

// The dependent item only becomes ready because GitHub says the first item's
// pull request merged: with the same recorded state and an open pull request
// there is no ready work and no reason to wake anyone.
func TestOpenPullRequestLeavesDependentWorkBlockedAndTheTLAsleep(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	p := mergeProgressionProgram(t, repo)

	start := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	ticker := &fakeTicker{channel: make(chan time.Time, 2)}
	out, errOut := &syncBuffer{}, &syncBuffer{}
	runner := &recordingTurnRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, p.Slug, Options{
			Now:    clock.Now,
			Ticker: func(time.Duration) Ticker { return ticker },
			BuildSnapshot: func(slug string, options programview.Options) (programview.Snapshot, error) {
				options.PRIndex = mergedPRIndex{state: programview.PRStateOpen}
				return programview.Build(slug, options)
			},
			Agents: patrolAgentListerFunc(func() ([]herdr.Agent, error) {
				return []herdr.Agent{
					{
						PaneID: "pTL", TerminalTitle: "relay:program:" + p.Slug,
						Status: herdr.StatusIdle,
					},
					{
						PaneID: "pW1", TerminalTitle: "relay:" + p.Slug + "-w1",
						Status: herdr.StatusWorking,
						CWD:    filepath.Join(repo, ".worktrees", p.Slug+"-w1"),
					},
				}, nil
			}),
			Turns: runner,
			Out:   out, Err: errOut,
		})
	}()

	state := waitForPatrolState(t, p.Slug, func(state State) bool {
		return state.LastTickAt == start.Format(time.RFC3339)
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if hasReason(state.Reasons, "ready-item:w2") {
		t.Fatalf("an open pull request unlocked the dependent item: %+v", state.Reasons)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("an open pull request rang the tech lead: %+v", runner.requests)
	}
	if !strings.Contains(out.String(), "tick reasons=none cadence=30m") ||
		!strings.Contains(out.String(), "TL wake not-needed") {
		t.Errorf("patrol events = %q, want a quiet tick and an unneeded wake", out.String())
	}
}

func hasReason(reasons []Reason, code string) bool {
	for _, reason := range reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}

// mergeProgressionProgram creates an active program whose second item waits on
// the first, with the first dispatched to a child project that has an open
// recorded pull request.
func mergeProgressionProgram(t *testing.T, repo string) program.Program {
	t.Helper()
	p, err := program.New("merge-progression", "Merge progression", repo, "copilot", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	first, err := p.AddItem(program.WorkItem{Title: "Ship the API", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.AddItem(program.WorkItem{
		Title: "Ship the client", Priority: program.PriorityP1, Dependencies: []string{first.ID},
	}); err != nil {
		t.Fatal(err)
	}
	childSlug := "merge-progression-" + first.ID
	if err := p.DispatchItem(first.ID, childSlug); err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}

	number := 41
	if err := os.MkdirAll(filepath.Join(project.ActiveDir(), childSlug), 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(repo, ".worktrees", childSlug)
	at := "2026-09-01T04:00:00Z"
	if err := project.Save(project.ManifestPath(project.ActiveDir(), childSlug), project.Manifest{
		Slug: childSlug, Title: "Ship the API", Repo: repo, Branch: "feature/api",
		BaseBranch: "main", Worktree: &worktree, Status: "active",
		Workflow: "deliver-pr", Phase: "implement", Created: at, Updated: at,
		PhasesCompleted: []string{}, PhasesRemaining: []string{},
		PR: project.PRInfo{Number: &number},
	}); err != nil {
		t.Fatal(err)
	}
	return p
}
