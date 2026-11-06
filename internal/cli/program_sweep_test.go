package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

// sweepTLTabID is the tab the program's tech lead runs in.
const sweepTLTabID = "w7:t1"

// deadPatrolTabID is a patrol tab left behind by a program whose patrol exited.
const deadPatrolTabID = "w7:t60"

func sweepTLAgent() herdr.Agent {
	return herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p1", TabID: sweepTLTabID, WorkspaceID: "w7",
		TerminalID: "tl-term", TerminalTitle: "relay:program:governance - GitHub Copilot",
	}
}

// installSweepHerdr installs a fake Herdr in workspace w7 that forgets an agent
// once it exits and a tab once it closes, so a second sweep sees what the
// first one left behind. No patrol is running unless a test says otherwise.
func installSweepHerdr(t *testing.T, agents ...herdr.Agent) *fakeHerdrClient {
	t.Helper()
	client := installManagedHerdrFakes(t, nil)
	live := append([]herdr.Agent{sweepTLAgent()}, agents...)
	client.agentsHook = func() ([]herdr.Agent, error) {
		return append([]herdr.Agent(nil), live...), nil
	}
	client.exitHook = func(identity herdr.SessionIdentity) (herdr.ExitResult, error) {
		remaining := make([]herdr.Agent, 0, len(live))
		for _, agent := range live {
			if agent.PaneID != identity.PaneID {
				remaining = append(remaining, agent)
			}
		}
		live = remaining
		return herdr.ExitResult{Outcome: herdr.ExitedNow, PaneGone: true}, nil
	}
	client.closeHook = func(tabID string) error {
		removeFakeTab(client, tabID)
		return nil
	}
	stubPatrolRunning(t, func(string) bool { return false })
	return client
}

func removeFakeTab(client *fakeHerdrClient, tabID string) {
	remaining := make([]herdr.TabInfo, 0, len(client.tabs))
	for _, tab := range client.tabs {
		if tab.ID != tabID {
			remaining = append(remaining, tab)
		}
	}
	client.tabs = remaining
}

func stubPatrolRunning(t *testing.T, running func(slug string) bool) {
	t.Helper()
	previous := patrolIsRunning
	patrolIsRunning = func(slug string) (bool, error) { return running(slug), nil }
	t.Cleanup(func() { patrolIsRunning = previous })
}

func runSweep(t *testing.T, programSlug string) (programSweepOutput, error) {
	t.Helper()
	var out bytes.Buffer
	runErr := runProgramSweep(&out, programSlug, true)
	var result programSweepOutput
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode sweep output %q: %v (sweep error: %v)", out.String(), err, runErr)
	}
	return result, runErr
}

func sweepOutput(closed, preserved, errs []programSweepEntry) programSweepOutput {
	result := programSweepOutput{
		Program: "governance", WorkspaceID: "w7",
		Closed: []programSweepEntry{}, Preserved: []programSweepEntry{}, Errors: []programSweepEntry{},
	}
	result.Closed = append(result.Closed, closed...)
	result.Preserved = append(result.Preserved, preserved...)
	result.Errors = append(result.Errors, errs...)
	return result
}

func assertSweepOutput(t *testing.T, got, want programSweepOutput) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sweep output:\n got %+v\nwant %+v", got, want)
	}
}

func cancelProgramItem(t *testing.T, programSlug, itemID string) {
	t.Helper()
	path := program.ManifestPath(program.ActiveDir(), programSlug)
	loaded, err := program.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.CancelItem(itemID, ""); err != nil {
		t.Fatal(err)
	}
	if err := program.Save(path, loaded); err != nil {
		t.Fatal(err)
	}
}

// workerTab is the tab Relay opened for an item's worker, labeled as launch
// labels it.
func workerTab(item program.WorkItem) herdr.TabInfo {
	return herdr.TabInfo{ID: workerTabID, WorkspaceID: "w7", Label: workerTabLabel(item), PaneCount: 1}
}

func deadPatrolTab() herdr.TabInfo {
	return herdr.TabInfo{ID: deadPatrolTabID, WorkspaceID: "w7", Label: "relay-patrol:old", PaneCount: 1}
}

func TestProgramSweepClosesFinishedWatcherTab(t *testing.T) {
	p, _, manifest := createCleanupFixture(t)
	client := installSweepHerdr(t)
	installCompletedWatcherState(t, manifest.Slug)

	got, err := runSweep(t, p.Slug)
	if err != nil {
		t.Fatal(err)
	}

	assertSweepOutput(t, got, sweepOutput([]programSweepEntry{{
		TabID: watcherTabID, Kind: sweepKindPRWatch, Subject: manifest.Slug, Reason: sweepReasonWatcherNotRunning,
	}}, nil, nil))
	if !reflect.DeepEqual(client.closedTabs, []string{watcherTabID}) {
		t.Fatalf("closed tabs = %v, want only the watcher tab", client.closedTabs)
	}
	state, err := prWatchReadState(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if state.TabID != "" || state.PaneID != "" {
		t.Fatalf("watcher record still names tab %q pane %q", state.TabID, state.PaneID)
	}
}

func TestProgramSweepPreservesWatcherTabs(t *testing.T) {
	p, _, manifest := createCleanupFixture(t)
	client := installSweepHerdr(t)
	installStubWatcherState(t, manifest.Slug, true)
	client.tabs = append(client.tabs,
		herdr.TabInfo{ID: "w7:t50", WorkspaceID: "w7", Label: "relay-pr-watch:unrecorded", PaneCount: 1},
		herdr.TabInfo{ID: "w8:t42", WorkspaceID: "w8", Label: prWatchWorkspaceLabel(manifest.Slug), PaneCount: 1},
		herdr.TabInfo{ID: "w7:t5", WorkspaceID: "w7", Label: "zsh", PaneCount: 1},
	)

	got, err := runSweep(t, p.Slug)
	if err != nil {
		t.Fatal(err)
	}

	assertSweepOutput(t, got, sweepOutput(nil, []programSweepEntry{
		{TabID: watcherTabID, Kind: sweepKindPRWatch, Subject: manifest.Slug, Reason: sweepReasonWatcherRunning},
		{TabID: "w7:t50", Kind: sweepKindPRWatch, Subject: "unrecorded", Reason: sweepReasonNoWatcherRecord},
	}, nil))
	if len(client.closedTabs) != 0 || len(client.closedPanes) != 0 {
		t.Fatalf("closed tabs = %v, panes = %v; want nothing closed", client.closedTabs, client.closedPanes)
	}
}

func TestProgramSweepClosesMergedAndCancelledWorkerTabs(t *testing.T) {
	tests := []struct {
		name         string
		fixture      func(t *testing.T) (program.Program, program.WorkItem, project.Manifest)
		workerStatus herdr.Status
		wantReason   string
	}{
		{
			name:         "merged item with idle worker",
			fixture:      createCleanupFixture,
			workerStatus: herdr.StatusIdle,
			wantReason:   "item merged; worker idle",
		},
		{
			name: "canceled item with done worker",
			fixture: func(t *testing.T) (program.Program, program.WorkItem, project.Manifest) {
				t.Helper()
				p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
				cancelProgramItem(t, p.Slug, item.ID)
				return p, item, manifest
			},
			workerStatus: herdr.StatusDone,
			wantReason:   "item " + string(program.ItemCancelled) + "; worker done",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, item, manifest := test.fixture(t)
			worker := liveWorkerAgent(manifest, test.workerStatus)
			client := installSweepHerdr(t, worker)
			client.tabs = append(client.tabs, workerTab(item))

			got, err := runSweep(t, p.Slug)
			if err != nil {
				t.Fatal(err)
			}

			assertSweepOutput(t, got, sweepOutput([]programSweepEntry{{
				TabID: workerTabID, Kind: sweepKindWorker, Subject: item.ID, Reason: test.wantReason,
			}}, nil, nil))
			if !reflect.DeepEqual(client.exited, []herdr.SessionIdentity{worker.Identity()}) {
				t.Fatalf("exits = %#v, want exactly the worker session", client.exited)
			}
			if !reflect.DeepEqual(client.closedTabs, []string{workerTabID}) {
				t.Fatalf("closed tabs = %v, want only the worker tab", client.closedTabs)
			}
			if len(client.focused) != 0 {
				t.Fatalf("sweep stole focus: %v", client.focused)
			}
		})
	}
}

func TestProgramSweepPreservesActiveItemWorkers(t *testing.T) {
	tests := []struct {
		name   string
		status program.ItemStatus
		adjust func(t *testing.T, p program.Program, itemID string)
	}{
		{name: "pending", status: program.ItemDispatched, adjust: func(t *testing.T, p program.Program, itemID string) {
			t.Helper()
			path := program.ManifestPath(program.ActiveDir(), p.Slug)
			loaded, err := program.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			for i := range loaded.Items {
				if loaded.Items[i].ID == itemID {
					loaded.Items[i].Status = program.ItemPending
					loaded.Items[i].DispatchedAt = ""
				}
			}
			if err := program.Save(path, loaded); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "dispatched", status: program.ItemDispatched},
		{name: "in-review", status: program.ItemDispatched, adjust: func(t *testing.T, p program.Program, itemID string) {
			t.Helper()
			recordItemPR(t, p, itemID, "https://github.com/acme/widgets/pull/7")
		}},
		{name: "blocked", status: program.ItemBlocked},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, item, manifest := createWorkerFixture(t, test.status)
			if test.adjust != nil {
				test.adjust(t, p, item.ID)
			}
			client := installSweepHerdr(t, liveWorkerAgent(manifest, herdr.StatusIdle))
			client.tabs = append(client.tabs, workerTab(item))

			got, err := runSweep(t, p.Slug)
			if err != nil {
				t.Fatal(err)
			}

			assertSweepOutput(t, got, sweepOutput(nil, nil, nil))
			if len(client.exited) != 0 || len(client.closedTabs) != 0 {
				t.Fatalf("exits = %v, closed tabs = %v; want the worker untouched", client.exited, client.closedTabs)
			}
		})
	}
}

func TestProgramSweepPreservesBusyWorkers(t *testing.T) {
	tests := []struct {
		name         string
		workerStatus herdr.Status
		exit         func(herdr.SessionIdentity) (herdr.ExitResult, error)
		wantExits    int
		wantReason   string
	}{
		{name: "working", workerStatus: herdr.StatusWorking, wantReason: "agent working"},
		{name: "blocked", workerStatus: herdr.StatusBlocked, wantReason: "agent blocked"},
		{
			name: "became busy before exit", workerStatus: herdr.StatusIdle,
			exit: func(identity herdr.SessionIdentity) (herdr.ExitResult, error) {
				return herdr.ExitResult{}, fmt.Errorf("%w: agent %s is working", herdr.ErrAgentBusy, identity)
			},
			wantExits: 1, wantReason: sweepReasonWorkerBusy,
		},
		{
			name: "pane taken over after exit", workerStatus: herdr.StatusIdle,
			exit: func(herdr.SessionIdentity) (herdr.ExitResult, error) {
				return herdr.ExitResult{Outcome: herdr.ExitedReplaced}, nil
			},
			wantExits: 1, wantReason: sweepReasonPaneReplaced,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
			cancelProgramItem(t, p.Slug, item.ID)
			client := installSweepHerdr(t, liveWorkerAgent(manifest, test.workerStatus))
			client.tabs = append(client.tabs, workerTab(item))
			if test.exit != nil {
				client.exitHook = test.exit
			}

			got, err := runSweep(t, p.Slug)
			if err != nil {
				t.Fatal(err)
			}

			assertSweepOutput(t, got, sweepOutput(nil, []programSweepEntry{{
				TabID: workerTabID, Kind: sweepKindWorker, Subject: item.ID, Reason: test.wantReason,
			}}, nil))
			if len(client.exited) != test.wantExits {
				t.Fatalf("exits = %d, want %d", len(client.exited), test.wantExits)
			}
			if len(client.closedTabs) != 0 {
				t.Fatalf("closed tabs = %v, want the worker tab kept", client.closedTabs)
			}
		})
	}
}

func TestProgramSweepPreservesUserAgentInWorkerTab(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	cancelProgramItem(t, p.Slug, item.ID)
	userAgent := liveWorkerAgent(manifest, herdr.StatusIdle)
	userAgent.TerminalTitle = "Claude Code"
	client := installSweepHerdr(t, userAgent)
	client.tabs = append(client.tabs, workerTab(item))

	got, err := runSweep(t, p.Slug)
	if err != nil {
		t.Fatal(err)
	}

	assertSweepOutput(t, got, sweepOutput(nil, []programSweepEntry{{
		TabID: workerTabID, Kind: sweepKindWorker, Subject: item.ID, Reason: sweepReasonForeignSession,
	}}, nil))
	if len(client.exited) != 0 || len(client.closedTabs) != 0 {
		t.Fatalf("exits = %v, closed tabs = %v; want the user's agent untouched", client.exited, client.closedTabs)
	}
}

func TestProgramSweepExitedWorkerTabs(t *testing.T) {
	tests := []struct {
		name       string
		paneCWD    func(worktree string) string
		wantClosed bool
		wantReason string
	}{
		{
			name:       "shell left in the worktree",
			paneCWD:    func(worktree string) string { return worktree },
			wantClosed: true,
			wantReason: "item " + string(program.ItemCancelled) + "; worker exited",
		},
		{
			name:       "shell moved outside the worktree",
			paneCWD:    func(string) string { return t.TempDir() },
			wantReason: sweepReasonOutsideWorktree,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
			cancelProgramItem(t, p.Slug, item.ID)
			client := installSweepHerdr(t)
			client.tabs = append(client.tabs, workerTab(item))
			client.panes = append(client.panes, herdr.Pane{
				ID: "w7:p9", TabID: workerTabID, WorkspaceID: "w7", CWD: test.paneCWD(*manifest.Worktree),
			})

			got, err := runSweep(t, p.Slug)
			if err != nil {
				t.Fatal(err)
			}

			entry := []programSweepEntry{{
				TabID: workerTabID, Kind: sweepKindWorker, Subject: item.ID, Reason: test.wantReason,
			}}
			want, wantClosedTabs := sweepOutput(nil, entry, nil), []string(nil)
			if test.wantClosed {
				want, wantClosedTabs = sweepOutput(entry, nil, nil), []string{workerTabID}
			}
			assertSweepOutput(t, got, want)
			if !reflect.DeepEqual(client.closedTabs, wantClosedTabs) || len(client.exited) != 0 {
				t.Fatalf("closed tabs = %v, exits = %v; want closed tabs %v and no exits",
					client.closedTabs, client.exited, wantClosedTabs)
			}
		})
	}
}

func TestProgramSweepLeavesWorkersInOtherWorkspaces(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	local := liveWorkerAgent(manifest, herdr.StatusIdle)
	elsewhere := local
	elsewhere.PaneID, elsewhere.TabID, elsewhere.WorkspaceID = "w8:p9", "w8:t9", "w8"
	client := installSweepHerdr(t, local, elsewhere)
	otherTab := workerTab(item)
	otherTab.ID, otherTab.WorkspaceID = "w8:t9", "w8"
	client.tabs = append(client.tabs, workerTab(item), otherTab)

	got, err := runSweep(t, p.Slug)
	if err != nil {
		t.Fatal(err)
	}

	assertSweepOutput(t, got, sweepOutput([]programSweepEntry{{
		TabID: workerTabID, Kind: sweepKindWorker, Subject: item.ID, Reason: "item merged; worker idle",
	}}, nil, nil))
	if !reflect.DeepEqual(client.exited, []herdr.SessionIdentity{local.Identity()}) {
		t.Fatalf("exits = %#v, want only this workspace's worker", client.exited)
	}
	if !reflect.DeepEqual(client.closedTabs, []string{workerTabID}) {
		t.Fatalf("closed tabs = %v, want only this workspace's worker tab", client.closedTabs)
	}
}

func TestProgramSweepClosesDeadPatrolTab(t *testing.T) {
	p, _, _ := createWorkerFixture(t, program.ItemDispatched)
	client := installSweepHerdr(t)
	client.tabs = []herdr.TabInfo{deadPatrolTab()}

	got, err := runSweep(t, p.Slug)
	if err != nil {
		t.Fatal(err)
	}

	assertSweepOutput(t, got, sweepOutput([]programSweepEntry{{
		TabID: deadPatrolTabID, Kind: sweepKindPatrol, Subject: "old", Reason: sweepReasonPatrolNotRunning,
	}}, nil, nil))
	if !reflect.DeepEqual(client.closedTabs, []string{deadPatrolTabID}) {
		t.Fatalf("closed tabs = %v, want only the dead patrol tab", client.closedTabs)
	}
}

func TestProgramSweepPreservesLivePatrolAndTLTabs(t *testing.T) {
	p, _, _ := createWorkerFixture(t, program.ItemDispatched)
	client := installSweepHerdr(t)
	stubPatrolRunning(t, func(slug string) bool { return slug == p.Slug })
	client.tabs = []herdr.TabInfo{
		{ID: "w7:t2", WorkspaceID: "w7", Label: "relay-patrol:" + p.Slug, PaneCount: 1},
		{ID: sweepTLTabID, WorkspaceID: "w7", Label: "relay-patrol:old", PaneCount: 1},
		{ID: "w7:t3", WorkspaceID: "w7", Label: "relay-patrol:stale", PaneCount: 1, Status: herdr.StatusWorking},
	}

	got, err := runSweep(t, p.Slug)
	if err != nil {
		t.Fatal(err)
	}

	assertSweepOutput(t, got, sweepOutput(nil, []programSweepEntry{
		{TabID: "w7:t2", Kind: sweepKindPatrol, Subject: p.Slug, Reason: sweepReasonPatrolRunning},
		{TabID: sweepTLTabID, Kind: sweepKindPatrol, Subject: "old", Reason: sweepReasonTechLeadTab},
		{TabID: "w7:t3", Kind: sweepKindPatrol, Subject: "stale", Reason: "agent working"},
	}, nil))
	if len(client.closedTabs) != 0 {
		t.Fatalf("closed tabs = %v, want nothing closed", client.closedTabs)
	}
}

// installFinishedWorkspace builds a workspace holding one finished watcher, the
// idle worker of the merged item, and a dead patrol's tab.
func installFinishedWorkspace(t *testing.T) (program.Program, program.WorkItem, project.Manifest, *fakeHerdrClient) {
	t.Helper()
	p, item, manifest := createCleanupFixture(t)
	client := installSweepHerdr(t, liveWorkerAgent(manifest, herdr.StatusIdle))
	installCompletedWatcherState(t, manifest.Slug)
	client.tabs = append(client.tabs,
		workerTab(item),
		deadPatrolTab(),
	)
	return p, item, manifest, client
}

func TestProgramSweepIsIdempotent(t *testing.T) {
	p, item, manifest, client := installFinishedWorkspace(t)

	first, err := runSweep(t, p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	assertSweepOutput(t, first, sweepOutput([]programSweepEntry{
		{TabID: watcherTabID, Kind: sweepKindPRWatch, Subject: manifest.Slug, Reason: sweepReasonWatcherNotRunning},
		{TabID: deadPatrolTabID, Kind: sweepKindPatrol, Subject: "old", Reason: sweepReasonPatrolNotRunning},
		{TabID: workerTabID, Kind: sweepKindWorker, Subject: item.ID, Reason: "item merged; worker idle"},
	}, nil, nil))
	second, err := runSweep(t, p.Slug)
	if err != nil {
		t.Fatal(err)
	}

	assertSweepOutput(t, second, sweepOutput(nil, nil, nil))
	if len(client.closedTabs) != 3 || len(client.exited) != 1 {
		t.Fatalf("closed tabs = %v, exits = %v; the second sweep acted again", client.closedTabs, client.exited)
	}
}

func TestProgramSweepAggregatesCloseFailures(t *testing.T) {
	p, item, manifest, client := installFinishedWorkspace(t)
	refused := errors.New("herdr refused")
	client.closeHook = func(tabID string) error {
		if tabID == watcherTabID {
			removeFakeTab(client, tabID)
			return nil
		}
		return refused
	}

	got, err := runSweep(t, p.Slug)
	if err == nil || !strings.Contains(err.Error(), "2 error(s)") {
		t.Fatalf("sweep error = %v, want both failures counted", err)
	}

	assertSweepOutput(t, got, sweepOutput(
		[]programSweepEntry{{
			TabID: watcherTabID, Kind: sweepKindPRWatch, Subject: manifest.Slug, Reason: sweepReasonWatcherNotRunning,
		}},
		nil,
		[]programSweepEntry{
			{
				TabID: deadPatrolTabID, Kind: sweepKindPatrol, Subject: "old",
				Reason: `close patrol tab w7:t60 for program "old": herdr refused`,
			},
			{
				TabID: workerTabID, Kind: sweepKindWorker, Subject: item.ID,
				Reason: "the worker for item " + item.ID + " exited, but its tab w7:t9 could not be closed: herdr refused",
			},
		},
	))
}

func TestProgramSweepThenCleanupArchivesMergedItem(t *testing.T) {
	p, item, _, _ := installFinishedWorkspace(t)
	if _, err := runSweep(t, p.Slug); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runProgramWorkerCleanup(&out, p.Slug, item.ID, true); err != nil {
		t.Fatal(err)
	}
	result := decodeCleanupOutput(t, out.String())
	if result.Status != cleanupClean || !result.Archived ||
		result.WorkerExit != cleanupWorkerAbsent || len(result.Warnings) != 0 {
		t.Fatalf("cleanup after sweep = %+v, want a clean archive with no worker left to exit", result)
	}
}

func TestProgramSweepPreservesReusedWorkerTabID(t *testing.T) {
	// Herdr gave the worker tab's id to a new tab before the lock was taken.
	reused := []herdr.TabInfo{{ID: workerTabID, WorkspaceID: "w7", Label: "notes", PaneCount: 1}}
	tests := []struct {
		name  string
		later []herdr.TabInfo
		owner bool
	}{
		{name: "owner tab id now labels another tab", later: reused, owner: true},
		{name: "agent-less tab id now labels another tab", later: reused},
		{name: "agent-less tab is gone"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
			cancelProgramItem(t, p.Slug, item.ID)
			var agents []herdr.Agent
			if test.owner {
				agents = append(agents, liveWorkerAgent(manifest, herdr.StatusIdle))
			}
			client := installSweepHerdr(t, agents...)
			client.panes = append(client.panes, herdr.Pane{
				ID: "w7:p9", TabID: workerTabID, WorkspaceID: "w7", CWD: *manifest.Worktree,
			})
			listed := 0
			client.tabsHook = func(string) ([]herdr.TabInfo, error) {
				listed++
				if listed == 1 {
					return []herdr.TabInfo{workerTab(item)}, nil
				}
				return test.later, nil
			}

			got, err := runSweep(t, p.Slug)
			if err != nil {
				t.Fatal(err)
			}

			assertSweepOutput(t, got, sweepOutput(nil, []programSweepEntry{{
				TabID: workerTabID, Kind: sweepKindWorker, Subject: item.ID, Reason: sweepReasonTabChanged,
			}}, nil))
			if len(client.exited) != 0 || len(client.closedTabs) != 0 {
				t.Fatalf("exits = %v, closed tabs = %v; want the reused tab untouched", client.exited, client.closedTabs)
			}
		})
	}
}

func TestProgramSweepItemWithoutWorktree(t *testing.T) {
	tests := []struct {
		name          string
		owner         bool
		wantClosed    bool
		wantReason    string
		wantExitCount int
	}{
		{
			name:  "live owner still exits",
			owner: true, wantClosed: true, wantReason: "item merged; worker idle", wantExitCount: 1,
		},
		{name: "agent-less tab is kept", wantReason: sweepReasonNoWorktree},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, item, manifest := createCleanupFixture(t)
			path := program.ManifestPath(program.ActiveDir(), p.Slug)
			loaded, err := program.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			for i := range loaded.Items {
				if loaded.Items[i].ID == item.ID {
					loaded.Items[i].ProjectBranch = ""
					loaded.Items[i].ProjectWorktree = ""
				}
			}
			if err := program.Save(path, loaded); err != nil {
				t.Fatal(err)
			}
			var agents []herdr.Agent
			if test.owner {
				agents = append(agents, liveWorkerAgent(manifest, herdr.StatusIdle))
			}
			client := installSweepHerdr(t, agents...)
			client.tabs = append(client.tabs, workerTab(item))
			client.panes = append(client.panes, herdr.Pane{
				ID: "w7:p9", TabID: workerTabID, WorkspaceID: "w7", CWD: *manifest.Worktree,
			})

			got, err := runSweep(t, p.Slug)
			if err != nil {
				t.Fatal(err)
			}

			entry := []programSweepEntry{{
				TabID: workerTabID, Kind: sweepKindWorker, Subject: item.ID, Reason: test.wantReason,
			}}
			want, wantClosedTabs := sweepOutput(nil, entry, nil), []string(nil)
			if test.wantClosed {
				want, wantClosedTabs = sweepOutput(entry, nil, nil), []string{workerTabID}
			}
			assertSweepOutput(t, got, want)
			if !reflect.DeepEqual(client.closedTabs, wantClosedTabs) || len(client.exited) != test.wantExitCount {
				t.Fatalf("closed tabs = %v, exits = %v; want closed tabs %v and %d exit(s)",
					client.closedTabs, client.exited, wantClosedTabs, test.wantExitCount)
			}
		})
	}
}
