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
	recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
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

// watcherTabID is the tab the child's pull request watcher holds in these
// fixtures, recorded so it can be closed after the watcher is gone.
const watcherTabID = "w7:t42"

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
	return installWatcherState(t, slug, running, prwatch.StatusRunning)
}

// installCompletedWatcherState installs the record a watcher leaves behind when
// it finishes on its own: no live process, a terminal lifecycle status, and the
// tab it deliberately kept open so its last lines stay readable.
func installCompletedWatcherState(t *testing.T, slug string) *[]string {
	t.Helper()
	return installWatcherState(t, slug, false, prwatch.StatusComplete)
}

func installWatcherState(
	t *testing.T, slug string, running bool, status prwatch.Status,
) *[]string {
	t.Helper()
	stopped := []string{}
	previousRunning, previousRead, previousUpdate, previousSignal :=
		prWatchIsRunning, prWatchReadState, prWatchUpdateState, prWatchSignal
	live := running
	state := prwatch.State{
		Project: slug, PID: 4242, StartedAt: "2026-01-01T00:00:00Z",
		WorkspaceID: "w7", TabID: watcherTabID, PaneID: "w7:p42",
		TerminalID: "watcher-term-42", Status: status,
	}
	if client, ok := newHerdrClient().(*fakeHerdrClient); ok {
		installFakeWatcherInventory(client, state)
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

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("cleanup result: %v", err)
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
	result := decodeCleanupOutput(t, out)
	if result.Status != cleanupIncomplete || !result.WatcherStopped || !result.WatcherTabClosed ||
		!strings.Contains(result.Error, "left exactly as they are") {
		t.Fatalf("partial cleanup output = %+v, want the completed watcher stop and later failure", result)
	}
	if result.NextCommand != "relay program worker cleanup "+p.Slug+" "+item.ID {
		t.Fatalf("next command = %q", result.NextCommand)
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

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("cleanup result: %v", err)
	}
	if closedIDs(client).has(workerTabID) || closedIDs(client).has(worker.PaneID) {
		t.Fatalf("cleanup closed a replacement session's ids: %v %v",
			client.closedTabs, client.closedPanes)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), manifest.Slug)) {
		t.Fatal("cleanup archived the project a replacement session may be using")
	}
	result := decodeCleanupOutput(t, out)
	if result.Status != cleanupIncomplete || !strings.Contains(result.Error, "now running in its pane") {
		t.Fatalf("result = %+v, want the replacement failure after watcher cleanup", result)
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

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("cleanup result: %v", err)
	}
	if closedIDs(client).has(workerTabID) || closedIDs(client).has(worker.PaneID) {
		t.Fatalf("cleanup closed a reused id: %v %v", client.closedTabs, client.closedPanes)
	}
	if !pathExists(*manifest.Worktree) {
		t.Fatal("cleanup removed the worktree after refusing to close the tab")
	}
	result := decodeCleanupOutput(t, out)
	if result.Status != cleanupIncomplete || !strings.Contains(result.Error, "running there now") {
		t.Fatalf("result = %+v, want the revalidation failure", result)
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

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("cleanup result: %v", err)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), manifest.Slug)) {
		t.Fatal("cleanup archived the project after failing to close the tab")
	}
	result := decodeCleanupOutput(t, out)
	if result.Status != cleanupIncomplete || !strings.Contains(result.Error, "left intact") {
		t.Fatalf("result = %+v, want the failed close after watcher cleanup", result)
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

func TestWorkerCleanupRejectsInvalidRequestedProjectSlugWithoutChangingVictim(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	for i := range p.Items {
		if p.Items[i].ID == item.ID {
			p.Items[i].ProjectSlug = "nested/../" + manifest.Slug
		}
	}

	_, _, _, err := loadProgramCleanupTarget(p, item.ID)
	if err == nil || !strings.Contains(err.Error(), "invalid slug") {
		t.Fatalf("loadProgramCleanupTarget error = %v, want invalid slug rejection", err)
	}
	if !pathExists(*manifest.Worktree) {
		t.Fatal("cleanup removed the victim worktree")
	}
	if !gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("cleanup removed the victim branch")
	}
	if !pathExists(project.ManifestPath(project.ActiveDir(), manifest.Slug)) {
		t.Fatal("cleanup removed the victim manifest")
	}
}

func TestWorkerCleanupRejectsManifestSlugMismatchBeforeStoppingVictim(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	attackerSlug := "attacker"
	for i := range p.Items {
		if p.Items[i].ID == item.ID {
			p.Items[i].ProjectSlug = attackerSlug
		}
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	attackerPath := project.ManifestPath(project.ActiveDir(), attackerSlug)
	if err := os.MkdirAll(filepath.Dir(attackerPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(attackerPath, manifest); err != nil {
		t.Fatal(err)
	}

	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) {
		return []herdr.Agent{liveWorkerAgent(manifest, herdr.StatusIdle)}, nil
	}
	installManagedHerdrFakes(t, client)
	stopped := installStubWatcherState(t, manifest.Slug, true)

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil || !strings.Contains(err.Error(), `manifest slug "`+manifest.Slug+
		`" does not match requested child project "`+attackerSlug+`"`) {
		t.Fatalf("worker cleanup error = %v, want manifest slug mismatch rejection", err)
	}
	if len(*stopped) != 0 {
		t.Fatalf("cleanup stopped the victim watcher: %v", *stopped)
	}
	if len(client.exited) != 0 || len(client.closedTabs) != 0 || len(client.closedPanes) != 0 {
		t.Fatalf(
			"cleanup touched the victim session: exited=%v tabs=%v panes=%v",
			client.exited, client.closedTabs, client.closedPanes,
		)
	}
	if !pathExists(*manifest.Worktree) {
		t.Fatal("cleanup removed the victim worktree")
	}
	if !gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("cleanup removed the victim branch")
	}
	if !pathExists(project.ManifestPath(project.ActiveDir(), manifest.Slug)) {
		t.Fatal("cleanup removed the victim manifest")
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

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("cleanup result: %v", err)
	}
	if len(client.exited) != 0 {
		t.Fatalf("cleanup ended a session while ownership was ambiguous: %#v", client.exited)
	}
	if !pathExists(*manifest.Worktree) {
		t.Fatal("cleanup removed the worktree while ownership was ambiguous")
	}
	result := decodeCleanupOutput(t, out)
	if result.Status != cleanupIncomplete || !strings.Contains(result.Error, "2 live sessions") {
		t.Fatalf("result = %+v, want the ambiguity recorded", result)
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

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("cleanup result: %v", err)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), manifest.Slug)) {
		t.Fatal("cleanup archived the project after failing to stop the watcher")
	}
	result := decodeCleanupOutput(t, out)
	if result.Status != cleanupIncomplete || !strings.Contains(result.Error, "nothing else was torn down") {
		t.Fatalf("result = %+v, want the watcher stop failure", result)
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

// A watcher that reaches a merged pull request completes and exits on its own,
// and it keeps its tab: closing it from inside would race the flush of its own
// final lines. Cleanup is what closes it, so a finished watcher is not a step
// cleanup can skip — and it must not be signaled, because there is nothing
// left to signal.
func TestWorkerCleanupClosesACompletedWatchersTab(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	signaled := installCompletedWatcherState(t, manifest.Slug)

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("worker cleanup: %v", err)
	}
	result := decodeCleanupOutput(t, out)
	if len(*signaled) != 0 {
		t.Errorf("a finished watcher process was signaled: %v", *signaled)
	}
	if !result.WatcherTabClosed {
		t.Fatal("the completed watcher tab close was not reported")
	}
	if !closedIDs(client).has(watcherTabID) {
		t.Fatalf("the completed watcher's tab was left open: %v", closedIDs(client))
	}
	state, err := prWatchReadState(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if state.TabID != "" || state.PaneID != "" {
		t.Errorf("watcher record still names %s/%s after the close", state.TabID, state.PaneID)
	}
	if result.WorkerExit != cleanupWorkerAbsent || !result.Archived || result.Status != cleanupClean {
		t.Fatalf("result = %+v, want a clean retirement", result)
	}
	if result.NextCommand != "" {
		t.Errorf("a finished cleanup asked for a retry: %q", result.NextCommand)
	}
}

// A close that does not happen must leave the recorded ids alone: they are the
// only handle on that tab, and they are what the patrol reads to keep raising
// merged-worker-cleanup. Cleanup therefore names the retry, and the retry
// finishes the job even though the child is already archived and the worker is
// long gone. A third run has nothing left to close.
func TestWorkerCleanupRetriesAWatcherTabItCouldNotClose(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{closeErr: errors.New("herdr refused to close the tab")}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installCompletedWatcherState(t, manifest.Slug)

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	blocked := decodeCleanupOutput(t, out)
	retry := "relay program worker cleanup " + p.Slug + " " + item.ID
	if blocked.Status != cleanupIncomplete {
		t.Fatalf("status = %q, want %q while the watcher tab remains open", blocked.Status, cleanupIncomplete)
	}
	if blocked.NextCommand != retry {
		t.Errorf("next command = %q, want %q", blocked.NextCommand, retry)
	}
	if len(blocked.Warnings) == 0 || !strings.Contains(strings.Join(blocked.Warnings, " "), watcherTabID) {
		t.Errorf("warnings = %v, want the open watcher tab named", blocked.Warnings)
	}
	held, err := prWatchReadState(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if held.TabID != watcherTabID {
		t.Fatalf("watcher record = %+v, want the tab that is still open", held)
	}

	client.closeErr = nil
	out, err = runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	finished := decodeCleanupOutput(t, out)
	if !finished.AlreadyArchived || finished.WorkerExit != cleanupWorkerAbsent {
		t.Fatalf("retry result = %+v, want an archived child and an absent worker", finished)
	}
	if finished.Status != cleanupClean || finished.NextCommand != "" {
		t.Fatalf("retry result = %+v, want a clean finish", finished)
	}
	if !closedIDs(client).has(watcherTabID) {
		t.Fatalf("the retry left the watcher tab open: %v", closedIDs(client))
	}
	cleared, err := prWatchReadState(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.TabID != "" || cleared.PaneID != "" {
		t.Errorf("watcher record still names %s/%s after the close", cleared.TabID, cleared.PaneID)
	}

	closes := len(closedIDs(client))
	if _, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json"); err != nil {
		t.Fatalf("third cleanup: %v", err)
	}
	if len(closedIDs(client)) != closes {
		t.Errorf("a repeated cleanup closed ids again: %v", closedIDs(client))
	}
}

func TestWorkerCleanupExplainsHowToCloseALegacyWatcherTab(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installCompletedWatcherState(t, manifest.Slug)
	if _, err := prWatchUpdateState(manifest.Slug, func(state prwatch.State) (prwatch.State, error) {
		state.WorkspaceID = ""
		state.TerminalID = ""
		return state, nil
	}); err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("worker cleanup: %v", err)
	}
	result := decodeCleanupOutput(t, out)
	warnings := strings.Join(result.Warnings, " ")
	if result.Status != cleanupIncomplete || result.WatcherTabClosed ||
		!strings.Contains(warnings, "herdr tab close "+watcherTabID) {
		t.Fatalf("result = %+v, want incomplete cleanup with a manual close command", result)
	}
}

func TestWorkerCleanupReturnsIncompleteWhenBranchDeletionFails(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	actualWorktree := *manifest.Worktree
	missingWorktree := actualWorktree + "-missing"
	manifest.Worktree = &missingWorktree
	if err := project.Save(
		project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest,
	); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("worker cleanup: %v", err)
	}
	result := decodeCleanupOutput(t, out)
	wantCommand := "git -C " + shellQuote(manifest.Repo) + " branch -D " + shellQuote(manifest.Branch)
	if result.Status != cleanupIncomplete || !result.Archived {
		t.Fatalf("result = %+v, want incomplete archived cleanup", result)
	}
	if result.NextCommand != wantCommand {
		t.Fatalf("next command = %q, want %q", result.NextCommand, wantCommand)
	}
	if !gitx.BranchExists(manifest.Repo, manifest.Branch) || !pathExists(actualWorktree) {
		t.Fatal("branch deletion failure did not preserve the registered worktree and branch")
	}
}

func TestWorkerCleanupPreservesWatcherRetryWhenBranchDeletionAlsoFails(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	actualWorktree := *manifest.Worktree
	missingWorktree := actualWorktree + "-missing"
	manifest.Worktree = &missingWorktree
	if err := project.Save(
		project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest,
	); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{closeErr: errors.New("herdr refused to close the watcher tab")}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installCompletedWatcherState(t, manifest.Slug)

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("worker cleanup: %v", err)
	}
	result := decodeCleanupOutput(t, out)
	watcherRetry := "relay program worker cleanup " + p.Slug + " " + item.ID
	branchDelete := manualBranchDeleteCommand(manifest.Repo, manifest.Branch)
	warnings := strings.Join(result.Warnings, "\n")
	if result.Status != cleanupIncomplete || !result.Archived {
		t.Fatalf("result = %+v, want incomplete archived cleanup", result)
	}
	if result.NextCommand != watcherRetry {
		t.Fatalf("next command = %q, want watcher retry %q", result.NextCommand, watcherRetry)
	}
	if !strings.Contains(warnings, branchDelete) {
		t.Fatalf("warnings = %q, want branch deletion guidance %q", warnings, branchDelete)
	}
	if !gitx.BranchExists(manifest.Repo, manifest.Branch) || !pathExists(actualWorktree) {
		t.Fatal("combined cleanup failure did not preserve the registered worktree and branch")
	}

	client.closeErr = nil
	out, err = runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	retried := decodeCleanupOutput(t, out)
	if retried.Status != cleanupIncomplete || !retried.AlreadyArchived ||
		retried.NextCommand != watcherRetry {
		t.Fatalf("retry result = %+v, want pending archived cleanup", retried)
	}
	if !strings.Contains(strings.Join(retried.Warnings, "\n"), branchDelete) {
		t.Fatalf("retry warnings = %v, want branch deletion failure", retried.Warnings)
	}
	if !gitx.BranchExists(manifest.Repo, manifest.Branch) || !pathExists(actualWorktree) {
		t.Fatal("retry reported clean while branch/worktree cleanup remained")
	}
}

func TestWorkerCleanupRetryFinishesArchivedBranchCleanup(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{closeErr: errors.New("herdr refused to close the watcher tab")}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installCompletedWatcherState(t, manifest.Slug)

	previousDelete := archiveForceDeleteBranchAt
	deleteAttempts := 0
	archiveForceDeleteBranchAt = func(repo, branch, expectedSHA string) error {
		deleteAttempts++
		if deleteAttempts == 1 {
			return errors.New("injected branch deletion failure")
		}
		return gitx.ForceDeleteBranchAt(repo, branch, expectedSHA)
	}
	t.Cleanup(func() { archiveForceDeleteBranchAt = previousDelete })

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	first := decodeCleanupOutput(t, out)
	retry := "relay program worker cleanup " + p.Slug + " " + item.ID
	if first.Status != cleanupIncomplete || !first.Archived || first.NextCommand != retry {
		t.Fatalf("first result = %+v, want archived cleanup requiring command retry", first)
	}
	if pathExists(*manifest.Worktree) {
		t.Fatal("first cleanup left the worktree present")
	}
	if !gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("first cleanup unexpectedly deleted the branch")
	}
	archived := loadArchivedManifest(t, manifest.Slug)
	if archived.ArchiveCleanup == nil {
		t.Fatal("archived manifest has no durable cleanup proof")
	}
	branchTip := gitx.RevParse(manifest.Repo, "refs/heads/"+manifest.Branch)
	if archived.ArchiveCleanup.ExpectedBranchTip != branchTip {
		t.Fatalf(
			"archived cleanup branch tip = %q, want %q",
			archived.ArchiveCleanup.ExpectedBranchTip, branchTip,
		)
	}
	if archived.ArchiveCleanup.WorktreePresent ||
		archived.ArchiveCleanup.ExpectedWorktreeTip != "" ||
		archived.ArchiveCleanup.ExpectedWorktreeBranch != "" ||
		archived.ArchiveCleanup.WorktreeDetached {
		t.Fatalf("worktree cleanup proof was not consumed: %+v", archived.ArchiveCleanup)
	}

	client.closeErr = nil
	out, err = runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	second := decodeCleanupOutput(t, out)
	if !second.AlreadyArchived || second.Status != cleanupClean || second.NextCommand != "" {
		t.Fatalf("retry result = %+v, want clean archived cleanup", second)
	}
	if gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("retry reported clean while the branch remained")
	}
	if deleteAttempts != 2 {
		t.Fatalf("branch deletion attempts = %d, want 2", deleteAttempts)
	}
	archived = loadArchivedManifest(t, manifest.Slug)
	if archived.ArchiveCleanup.BranchPresent ||
		archived.ArchiveCleanup.ExpectedBranchTip != "" {
		t.Fatalf("branch cleanup proof was not consumed: %+v", archived.ArchiveCleanup)
	}
}

func TestWorkerCleanupRetryReturnsIncompleteWhenBranchProbeFails(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)

	activeDir := filepath.Join(project.ActiveDir(), manifest.Slug)
	archivedDir := filepath.Join(project.ArchivedDir(), manifest.Slug)
	if err := os.MkdirAll(project.ArchivedDir(), 0755); err != nil {
		t.Fatal(err)
	}
	manifest.Status = "archived"
	if err := project.Save(filepath.Join(activeDir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(activeDir, archivedDir); err != nil {
		t.Fatal(err)
	}
	previous := archiveBranchExists
	archiveBranchExists = func(string, string) (bool, error) {
		return false, errors.New("git branch probe failed")
	}
	t.Cleanup(func() { archiveBranchExists = previous })

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("worker cleanup: %v", err)
	}
	result := decodeCleanupOutput(t, out)
	if result.Status != cleanupIncomplete || !result.AlreadyArchived {
		t.Fatalf("result = %+v, want incomplete archived cleanup", result)
	}
	if !strings.Contains(result.Error, "no durable cleanup proof") {
		t.Fatalf("cleanup error %q is missing legacy manifest guidance", result.Error)
	}
	if result.NextCommand != "relay program worker cleanup "+p.Slug+" "+item.ID {
		t.Fatalf("next command = %q, want worker cleanup retry", result.NextCommand)
	}
	if !pathExists(*manifest.Worktree) || !gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("legacy archived cleanup removed resources without durable proof")
	}
}
