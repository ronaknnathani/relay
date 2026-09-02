package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/prwatch"
)

// createCleanupFixture builds a merged managed item whose child project has a
// real worktree and branch, so cleanup can actually archive it.
func createCleanupFixture(t *testing.T) (program.Program, program.WorkItem, project.Manifest) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	saveProgramTestConfig(t)
	repo, err := filepath.EvalSymlinks(newTestRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	p, err := program.New("governance", "Ship governed changes", repo, "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "Build managed runtime", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	childSlug := "governance-" + item.ID
	if err := p.DispatchItem(item.ID, childSlug); err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	branch := "user/" + childSlug
	worktree := addArchiveWorktree(t, repo, childSlug, branch)
	manifest := project.Manifest{
		Slug: childSlug, Title: item.Title, Repo: repo, Agent: "copilot",
		Branch: branch, BaseBranch: "main", Worktree: &worktree,
		Program: p.Slug, ProgramItem: item.ID, Phase: "implement", Status: "active",
	}
	manifestPath := project.ManifestPath(project.ActiveDir(), childSlug)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	recordItemPR(t, p, item.ID, "7")
	mergeProgramItem(t, p.Slug, item.ID)
	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	mergedItem, _ := loaded.Item(item.ID)
	return loaded, mergedItem, manifest
}

// workerTabID is the tab a managed worker holds in these fixtures. The child's
// pull request watcher holds its own separate tab, so an assertion about the
// worker's tab must name it exactly.
const workerTabID = "w7:t9"

type closedTargets []string

func (c closedTargets) has(id string) bool {
	for _, closed := range c {
		if closed == id {
			return true
		}
	}
	return false
}

func closedIDs(client *fakeHerdrClient) closedTargets {
	return closedTargets(append(append([]string(nil), client.closedTabs...), client.closedPanes...))
}

func decodeCleanupOutput(t *testing.T, out string) programWorkerCleanupOutput {
	t.Helper()
	var result programWorkerCleanupOutput
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	return result
}

// installStubWatcherState installs a recorded watcher for a child project and
// reports whether stop was asked to signal a running process.
func installStubWatcherState(t *testing.T, slug string, running bool) *[]string {
	t.Helper()
	stopped := []string{}
	previousRunning, previousRead, previousUpdate, previousSignal :=
		prWatchIsRunning, prWatchReadState, prWatchUpdateState, prWatchSignal
	live := running
	state := prwatch.State{
		Project: slug, PID: 4242, StartedAt: "2026-01-01T00:00:00Z",
		TabID: "w7:t42", PaneID: "w7:p42", Status: prwatch.StatusRunning,
	}
	prWatchIsRunning = func(candidate string) (bool, error) {
		if candidate != slug {
			return false, nil
		}
		return live, nil
	}
	prWatchReadState = func(candidate string) (prwatch.State, error) {
		if candidate != slug {
			return prwatch.State{}, os.ErrNotExist
		}
		return state, nil
	}
	prWatchReadStateLocked = prWatchReadState
	prWatchUpdateState = func(candidate string, mutate func(prwatch.State) (prwatch.State, error)) (prwatch.State, error) {
		next, err := mutate(state)
		if err != nil {
			return prwatch.State{}, err
		}
		state = next
		return next, nil
	}
	prWatchSignal = func(pid int, _ os.Signal) error {
		stopped = append(stopped, slug)
		live = false
		return nil
	}
	t.Cleanup(func() {
		prWatchIsRunning, prWatchReadState, prWatchUpdateState, prWatchSignal =
			previousRunning, previousRead, previousUpdate, previousSignal
		prWatchReadStateLocked = prWatchReadState
	})
	return &stopped
}

func TestWorkerCleanupStopsTheWatcherExitsTheWorkerAndArchivesTheChild(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	worker := liveWorkerAgent(manifest, herdr.StatusIdle)
	worker.TerminalID = "term_a"
	live := true
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) {
		if !live {
			return nil, nil
		}
		return []herdr.Agent{worker}, nil
	}
	client.exitHook = func(herdr.SessionIdentity) (herdr.ExitResult, error) {
		live = false
		return herdr.ExitResult{Outcome: herdr.ExitedNow, PaneGone: true}, nil
	}
	installManagedHerdrFakes(t, client)
	stopped := installStubWatcherState(t, manifest.Slug, true)

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("worker cleanup: %v", err)
	}
	result := decodeCleanupOutput(t, out)
	if result.Status != cleanupClean {
		t.Fatalf("status = %q, want %q (%+v)", result.Status, cleanupClean, result)
	}
	if len(*stopped) != 1 {
		t.Fatalf("watcher stops = %v, want the child watcher stopped first", *stopped)
	}
	if !result.WatcherStopped {
		t.Fatal("the watcher stop was not reported")
	}
	if result.WorkerExit != cleanupWorkerExited {
		t.Fatalf("worker exit = %q", result.WorkerExit)
	}
	if len(client.exited) != 1 || client.exited[0] != worker.Identity() {
		t.Fatalf("exits = %#v, want the exact worker session", client.exited)
	}
	if !result.TabClosed || len(client.closedTabs) == 0 {
		t.Fatalf("tab closed = %t, closed tabs = %v", result.TabClosed, client.closedTabs)
	}
	if client.closedTabs[len(client.closedTabs)-1] != worker.TabID {
		t.Fatalf("closed tabs = %v, want the worker's own tab %q", client.closedTabs, worker.TabID)
	}
	if len(client.focused) != 0 {
		t.Fatalf("cleanup stole focus: %v", client.focused)
	}
	if !result.Archived {
		t.Fatal("the child project was not archived")
	}
	if pathExists(*manifest.Worktree) {
		t.Fatalf("worktree %s survived cleanup", *manifest.Worktree)
	}
	if pathExists(filepath.Join(project.ActiveDir(), manifest.Slug)) {
		t.Fatal("the child project is still active")
	}
	if !pathExists(filepath.Join(project.ArchivedDir(), manifest.Slug)) {
		t.Fatal("the child project was not moved to archived")
	}
	after, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	merged, _ := after.Item(item.ID)
	if merged.Status != program.ItemMerged {
		t.Fatalf("item status = %q after cleanup, want merged", merged.Status)
	}
}

func TestWorkerCleanupOutputsCleanJSON(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, true)

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("worker cleanup: %v", err)
	}
	trimmed := strings.TrimSpace(out)
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		t.Fatalf("JSON output is polluted: %q", out)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if raw["status"] != cleanupClean {
		t.Fatalf("status = %v", raw["status"])
	}
}

func TestWorkerCleanupDiscardsDirtyAndUntrackedWorktreeFiles(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	commitArchiveFile(t, *manifest.Worktree, "feature.txt", "unique\n", "unique work")
	writeArchiveFile(t, *manifest.Worktree, "feature.txt", "dirty edit\n")
	writeArchiveFile(t, *manifest.Worktree, "scratch/untracked.txt", "scratch\n")
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("worker cleanup: %v", err)
	}
	if !decodeCleanupOutput(t, out).Archived {
		t.Fatal("a dirty worktree was not archived")
	}
	if pathExists(*manifest.Worktree) {
		t.Fatalf("dirty worktree %s survived cleanup", *manifest.Worktree)
	}
	if gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatalf("branch %s survived cleanup", manifest.Branch)
	}
}

func TestWorkerCleanupLeavesABusyWorkerAlone(t *testing.T) {
	for _, status := range []herdr.Status{herdr.StatusWorking, herdr.StatusBlocked} {
		t.Run(string(status), func(t *testing.T) {
			p, item, manifest := createCleanupFixture(t)
			client := &fakeHerdrClient{
				agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, status)}},
			}
			installManagedHerdrFakes(t, client)
			installStubWatcherState(t, manifest.Slug, true)

			out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
			if err != nil {
				t.Fatalf("worker cleanup: %v", err)
			}
			result := decodeCleanupOutput(t, out)
			if result.Status != cleanupWorkerBusy {
				t.Fatalf("status = %q, want %q", result.Status, cleanupWorkerBusy)
			}
			if result.NextCommand != "relay program worker cleanup "+p.Slug+" "+item.ID {
				t.Fatalf("next command = %q", result.NextCommand)
			}
			if len(client.exited) != 0 {
				t.Fatalf("a %s worker was asked to exit: %#v", status, client.exited)
			}
			if closedIDs(client).has(workerTabID) {
				t.Fatalf("a %s worker's tab was closed", status)
			}
			if result.Archived {
				t.Fatal("the child project was archived under a busy worker")
			}
			if !pathExists(*manifest.Worktree) {
				t.Fatalf("worktree %s was removed under a busy worker", *manifest.Worktree)
			}
			if !pathExists(filepath.Join(project.ActiveDir(), manifest.Slug)) {
				t.Fatal("the child project was archived under a busy worker")
			}
		})
	}
}

func TestWorkerCleanupWithNoLiveWorkerStillArchives(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("worker cleanup: %v", err)
	}
	result := decodeCleanupOutput(t, out)
	if result.WorkerExit != cleanupWorkerAbsent {
		t.Fatalf("worker exit = %q, want %q", result.WorkerExit, cleanupWorkerAbsent)
	}
	if len(client.exited) != 0 {
		t.Fatalf("an absent worker was asked to exit: %#v", client.exited)
	}
	if closedIDs(client).has(workerTabID) {
		t.Fatalf("cleanup closed a worker tab it never found: %v", client.closedTabs)
	}
	if !result.Archived || result.Status != cleanupClean {
		t.Fatalf("result = %+v, want a clean archive", result)
	}
}

func TestWorkerCleanupKeepsEverythingWhenTheExitIsUncertain(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, herdr.StatusIdle)}},
		exitErr:        herdr.ErrExitUncertain,
	}
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, true)

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil {
		t.Fatal("cleanup succeeded although the exit was uncertain")
	}
	if !strings.Contains(err.Error(), "left exactly as they are") {
		t.Fatalf("error = %v, want the untouched state explained", err)
	}
	if closedIDs(client).has(workerTabID) {
		t.Fatal("an uncertain exit still closed the worker tab")
	}
	if !pathExists(*manifest.Worktree) {
		t.Fatal("an uncertain exit still removed the worktree")
	}
	if !pathExists(filepath.Join(project.ActiveDir(), manifest.Slug)) {
		t.Fatal("an uncertain exit still archived the child project")
	}
}

func TestWorkerCleanupRefusesToCloseAReplacementSession(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	worker := liveWorkerAgent(manifest, herdr.StatusIdle)
	client := &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{worker}},
		exitHook: func(herdr.SessionIdentity) (herdr.ExitResult, error) {
			return herdr.ExitResult{
				Outcome: herdr.ExitedReplaced,
				Replacement: herdr.Agent{
					PaneID: worker.PaneID, TabID: worker.TabID, WorkspaceID: "w7",
					TerminalID: "term_new", NativeSessionID: "session-new",
				},
			}, nil
		},
	}
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, true)

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil {
		t.Fatal("cleanup succeeded although a replacement session took the pane")
	}
	if closedIDs(client).has(workerTabID) || closedIDs(client).has(worker.PaneID) {
		t.Fatalf("cleanup closed a replacement session's ids: %v %v",
			client.closedTabs, client.closedPanes)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), manifest.Slug)) {
		t.Fatal("cleanup archived the project a replacement session may be using")
	}
}

func TestWorkerCleanupRefusesToCloseAReusedPaneAfterTheExit(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	worker := liveWorkerAgent(manifest, herdr.StatusIdle)
	worker.TerminalID = "term_a"
	replacement := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: worker.PaneID, TabID: worker.TabID,
		WorkspaceID: "w7", TerminalID: "term_new", NativeSessionID: "session-new",
		TerminalTitle: "someone else",
	}
	exited := false
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) {
		if exited {
			return []herdr.Agent{replacement}, nil
		}
		return []herdr.Agent{worker}, nil
	}
	client.exitHook = func(herdr.SessionIdentity) (herdr.ExitResult, error) {
		exited = true
		return herdr.ExitResult{Outcome: herdr.ExitedNow, PaneGone: true}, nil
	}
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, true)

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil {
		t.Fatal("cleanup succeeded although the pane was reused before the close")
	}
	if closedIDs(client).has(workerTabID) || closedIDs(client).has(worker.PaneID) {
		t.Fatalf("cleanup closed a reused id: %v %v", client.closedTabs, client.closedPanes)
	}
	if !pathExists(*manifest.Worktree) {
		t.Fatal("cleanup removed the worktree after refusing to close the tab")
	}
}

func TestWorkerCleanupReportsAFailedTabClose(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	worker := liveWorkerAgent(manifest, herdr.StatusIdle)
	live := true
	client := &fakeHerdrClient{closeErr: errors.New("herdr refused to close the tab")}
	client.agentsHook = func() ([]herdr.Agent, error) {
		if !live {
			return nil, nil
		}
		return []herdr.Agent{worker}, nil
	}
	client.exitHook = func(herdr.SessionIdentity) (herdr.ExitResult, error) {
		live = false
		return herdr.ExitResult{Outcome: herdr.ExitedNow, PaneGone: true}, nil
	}
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, true)

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil {
		t.Fatal("cleanup succeeded although the tab close failed")
	}
	if !strings.Contains(err.Error(), "left intact") {
		t.Fatalf("error = %v, want the untouched project explained", err)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), manifest.Slug)) {
		t.Fatal("cleanup archived the project after failing to close the tab")
	}
}

func TestWorkerCleanupIsIdempotentForAnArchivedChild(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)

	if _, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json"); err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	result := decodeCleanupOutput(t, out)
	if !result.AlreadyArchived || result.Status != cleanupClean {
		t.Fatalf("result = %+v, want an idempotent clean result", result)
	}
	after, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	merged, _ := after.Item(item.ID)
	if merged.Status != program.ItemMerged {
		t.Fatalf("item status = %q after a repeated cleanup, want merged", merged.Status)
	}
}

func TestWorkerCleanupRefusesEveryUnmergedItemStatus(t *testing.T) {
	for _, status := range []program.ItemStatus{
		program.ItemPending, program.ItemDispatched, program.ItemInReview,
		program.ItemBlocked, program.ItemCancelled,
	} {
		t.Run(string(status), func(t *testing.T) {
			p, item, manifest := createCleanupFixture(t)
			setProgramItemStatus(t, p.Slug, item.ID, status)
			client := &fakeHerdrClient{}
			client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
			installManagedHerdrFakes(t, client)
			stopped := installStubWatcherState(t, manifest.Slug, true)

			_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
			if err == nil {
				t.Fatalf("cleanup accepted a %s item", status)
			}
			if !strings.Contains(err.Error(), "want merged") {
				t.Fatalf("error = %v, want the merged gate explained", err)
			}
			if len(*stopped) != 0 {
				t.Fatalf("cleanup stopped the watcher for a %s item", status)
			}
			if !pathExists(*manifest.Worktree) {
				t.Fatalf("cleanup removed a %s item's worktree", status)
			}
		})
	}
}

func TestWorkerCleanupRefusesAnAmbiguousOwner(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	first := liveWorkerAgent(manifest, herdr.StatusIdle)
	second := first
	second.PaneID = "w7:pZ"
	second.TabID = "w7:tZ"
	second.NativeSessionID = "session-z"
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{first, second}}}
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, true)

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil {
		t.Fatal("cleanup accepted two live sessions for one child project")
	}
	if len(client.exited) != 0 {
		t.Fatalf("cleanup ended a session while ownership was ambiguous: %#v", client.exited)
	}
	if !pathExists(*manifest.Worktree) {
		t.Fatal("cleanup removed the worktree while ownership was ambiguous")
	}
}

func TestWorkerCleanupStopsWhenTheWatcherCannotBeStopped(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, true)
	previousSignal := prWatchSignal
	prWatchSignal = func(int, os.Signal) error { return errors.New("no such process permission") }
	t.Cleanup(func() { prWatchSignal = previousSignal })

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil {
		t.Fatal("cleanup succeeded although the watcher could not be stopped")
	}
	if !strings.Contains(err.Error(), "nothing else was torn down") {
		t.Fatalf("error = %v, want the untouched state explained", err)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), manifest.Slug)) {
		t.Fatal("cleanup archived the project after failing to stop the watcher")
	}
}

// setProgramItemStatus rewrites one item's durable status so a gate can be
// tested against every state cleanup must refuse.
func setProgramItemStatus(t *testing.T, programSlug, itemID string, status program.ItemStatus) {
	t.Helper()
	path := program.ManifestPath(program.ActiveDir(), programSlug)
	loaded, err := program.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range loaded.Items {
		if loaded.Items[i].ID != itemID {
			continue
		}
		loaded.Items[i].Status = status
		loaded.Items[i].MergedAt = ""
		loaded.Items[i].CancelledAt = ""
		loaded.Items[i].BlockedReason = ""
		switch status {
		case program.ItemPending:
			loaded.Items[i].DispatchedAt = ""
			loaded.Items[i].InReviewAt = ""
			loaded.Items[i].PRRef = ""
		case program.ItemBlocked:
			loaded.Items[i].BlockedReason = "waiting for a decision"
		case program.ItemCancelled:
			loaded.Items[i].CancelledAt = loaded.Items[i].UpdatedAt
		case program.ItemDispatched:
			loaded.Items[i].PRRef = ""
			loaded.Items[i].InReviewAt = ""
		}
	}
	if err := program.Save(path, loaded); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerCleanupStopsTheWatcherBeforeItTouchesTheWorker(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	worker := liveWorkerAgent(manifest, herdr.StatusIdle)
	var order []string
	live := true
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) {
		if !live {
			return nil, nil
		}
		return []herdr.Agent{worker}, nil
	}
	client.exitHook = func(herdr.SessionIdentity) (herdr.ExitResult, error) {
		order = append(order, "exit-worker")
		live = false
		return herdr.ExitResult{Outcome: herdr.ExitedNow, PaneGone: true}, nil
	}
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, true)
	previousSignal := prWatchSignal
	stubSignal := prWatchSignal
	prWatchSignal = func(pid int, sig os.Signal) error {
		order = append(order, "stop-watcher")
		return stubSignal(pid, sig)
	}
	t.Cleanup(func() { prWatchSignal = previousSignal })

	if _, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json"); err != nil {
		t.Fatalf("worker cleanup: %v", err)
	}
	if len(order) != 2 || order[0] != "stop-watcher" || order[1] != "exit-worker" {
		t.Fatalf("cleanup order = %v, want the watcher stopped before the worker exits", order)
	}
	if !pathExists(filepath.Join(project.ArchivedDir(), manifest.Slug)) {
		t.Fatal("the child project was not archived last")
	}
}
