package cli

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/prwatch"
)

type cleanupErrorWriter struct {
	err error
}

func (w cleanupErrorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

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
	branch := "user/" + childSlug
	worktree := addArchiveWorktree(t, repo, childSlug, branch)
	for i := range p.Items {
		if p.Items[i].ID == item.ID {
			p.Items[i].ProjectBranch = branch
			p.Items[i].ProjectWorktree = worktree
		}
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
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

func TestFailProgramWorkerCleanupJoinsCauseAndRenderError(t *testing.T) {
	cause := errors.New("cleanup cause")
	renderErr := errors.New("render failure")
	result := programWorkerCleanupOutput{
		Program: "delivery", Item: "w1", Project: "delivery-w1",
	}

	err := failProgramWorkerCleanup(
		cleanupErrorWriter{err: renderErr}, &result, true, cause,
	)

	if !errors.Is(err, cause) || !errors.Is(err, renderErr) {
		t.Fatalf("failProgramWorkerCleanup error = %v, want cause and render failure", err)
	}
	if result.Status != cleanupIncomplete || result.Error != cause.Error() {
		t.Fatalf("rendered cleanup result = %+v, want incomplete cause", result)
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
			if !errors.Is(err, errProgramWorkerCleanupIncomplete) {
				t.Fatalf("worker cleanup error = %v, want %v", err, errProgramWorkerCleanupIncomplete)
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
	if err == nil {
		t.Fatal("cleanup returned nil after an uncertain worker exit")
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
	if err == nil {
		t.Fatal("cleanup returned nil after detecting a replacement session")
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
	if err == nil {
		t.Fatal("cleanup returned nil after detecting a reused pane")
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
	if err == nil {
		t.Fatal("cleanup returned nil after a worker tab close failure")
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

func TestWorkerCleanupRetryUsesPersistedForceAuthorization(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)

	decision, err := decideArchive(manifest, manifest.Slug, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Status = "archived"
	manifest.ArchiveCleanup = archiveCleanupProof(decision.proof)
	if _, err := stageArchivedProject(
		filepath.Join(project.ActiveDir(), manifest.Slug),
		filepath.Join(project.ArchivedDir(), manifest.Slug),
		manifest,
	); err != nil {
		t.Fatal(err)
	}
	writeArchiveFile(t, *manifest.Worktree, "dirty.txt", "discard on worker retry\n")

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("worker cleanup retry: %v", err)
	}
	result := decodeCleanupOutput(t, out)
	if !result.AlreadyArchived || result.Status != cleanupClean {
		t.Fatalf("result = %+v, want clean force-authorized retry", result)
	}
	if pathExists(*manifest.Worktree) || gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("worker cleanup retry left force-authorized resources behind")
	}
	archived := loadArchivedManifest(t, manifest.Slug)
	if archived.ArchiveCleanup == nil || !archived.ArchiveCleanup.ForceAuthorized {
		t.Fatalf(
			"cleanup proof = %+v, want persisted force authorization",
			archived.ArchiveCleanup,
		)
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

	_, _, _, _, err := loadProgramCleanupTarget(p, item.ID)
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
	if err == nil {
		t.Fatal("cleanup returned nil for ambiguous worker ownership")
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
	if err == nil {
		t.Fatal("cleanup returned nil after the watcher stop failure")
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
	if !errors.Is(err, errProgramWorkerCleanupIncomplete) {
		t.Fatalf("first cleanup error = %v, want %v", err, errProgramWorkerCleanupIncomplete)
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
	if !errors.Is(err, errProgramWorkerCleanupIncomplete) {
		t.Fatalf("worker cleanup error = %v, want %v", err, errProgramWorkerCleanupIncomplete)
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
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)
	previous := archiveForceDeleteBranchAt
	archiveForceDeleteBranchAt = func(string, string, string) error {
		return errors.New("injected branch deletion failure")
	}
	t.Cleanup(func() { archiveForceDeleteBranchAt = previous })

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if !errors.Is(err, errProgramWorkerCleanupIncomplete) {
		t.Fatalf("worker cleanup error = %v, want %v", err, errProgramWorkerCleanupIncomplete)
	}
	result := decodeCleanupOutput(t, out)
	expectedSHA := gitx.RevParse(manifest.Repo, "refs/heads/"+manifest.Branch)
	wantCommand := manualBranchDeleteAtCommand(manifest.Repo, manifest.Branch, expectedSHA)
	if result.Status != cleanupIncomplete || !result.Archived {
		t.Fatalf("result = %+v, want incomplete archived cleanup", result)
	}
	if result.NextCommand != wantCommand {
		t.Fatalf("next command = %q, want %q", result.NextCommand, wantCommand)
	}
	if !gitx.BranchExists(manifest.Repo, manifest.Branch) || pathExists(*manifest.Worktree) {
		t.Fatal("branch deletion failure did not preserve the branch after removing the worktree")
	}
}

func TestWorkerCleanupRequiresInspectionWhenBranchAdvancedDuringDeletion(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)

	previous := archiveForceDeleteBranchAt
	var advancedTip string
	archiveForceDeleteBranchAt = func(repo, branch, expectedSHA string) error {
		tree := gitOutput(t, repo, "rev-parse", expectedSHA+"^{tree}")
		cmd := exec.Command("git", "-C", repo, "commit-tree", tree, "-p", expectedSHA, "-m", "late worker commit")
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
			"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
		)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("create late commit: %v", err)
		}
		advancedTip = strings.TrimSpace(string(out))
		runArchiveGit(t, repo, "update-ref", "refs/heads/"+branch, advancedTip, expectedSHA)
		return gitx.ForceDeleteBranchAt(repo, branch, expectedSHA)
	}
	t.Cleanup(func() { archiveForceDeleteBranchAt = previous })

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if !errors.Is(err, errProgramWorkerCleanupIncomplete) {
		t.Fatalf("worker cleanup error = %v, want %v", err, errProgramWorkerCleanupIncomplete)
	}
	result := decodeCleanupOutput(t, out)
	warnings := strings.Join(result.Warnings, "\n")
	if result.Status != cleanupIncomplete || !result.Archived || result.NextCommand != "" {
		t.Fatalf("result = %+v, want incomplete cleanup requiring inspection only", result)
	}
	for _, forbidden := range []string{"branch -D", "hint:"} {
		if strings.Contains(warnings, forbidden) {
			t.Fatalf("warnings %q include destructive recovery guidance %q", warnings, forbidden)
		}
	}
	if !strings.Contains(warnings, "inspect") {
		t.Fatalf("warnings %q do not require inspection", warnings)
	}
	tip, found, tipErr := gitx.LocalBranchTip(manifest.Repo, manifest.Branch)
	if tipErr != nil || !found || tip != advancedTip {
		t.Fatalf("advanced branch = (%q, %t, %v), want (%q, true, nil)", tip, found, tipErr, advancedTip)
	}
}

func TestWorkerCleanupPreservesWorktreeAttachedToWrongBranch(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	victimBranch := "user/worker-victim"
	runArchiveGit(t, manifest.Repo, "branch", victimBranch, manifest.Branch)
	runArchiveGit(t, *manifest.Worktree, "checkout", "-q", victimBranch)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	stopped := installStubWatcherState(t, manifest.Slug, false)

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil || !strings.Contains(err.Error(), "attached to") ||
		!strings.Contains(err.Error(), "refs/heads/"+manifest.Branch) {
		t.Fatalf("worker cleanup error = %v, want worktree branch mismatch", err)
	}
	if len(*stopped) != 0 {
		t.Fatalf("cleanup stopped the watcher before validating resources: %v", *stopped)
	}
	if !pathExists(*manifest.Worktree) || !gitx.BranchExists(manifest.Repo, victimBranch) {
		t.Fatal("worker cleanup removed the worktree or branch belonging to another project")
	}
	if !pathExists(filepath.Join(project.ActiveDir(), manifest.Slug)) {
		t.Fatal("worker cleanup archived metadata before validating worktree ownership")
	}
}

func TestWorkerCleanupReportsArchivedAfterPostDeleteSaveFailure(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)
	previous := saveArchiveManifest
	saveArchiveManifest = func(path string, candidate project.Manifest) error {
		if candidate.ArchiveCleanup != nil &&
			candidate.ArchiveCleanup.WorktreeState == project.ArchiveCleanupDone &&
			candidate.ArchiveCleanup.BranchState == project.ArchiveCleanupPending {
			return errors.New("injected cleanup completion save failure")
		}
		return project.Save(path, candidate)
	}
	t.Cleanup(func() { saveArchiveManifest = previous })

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil {
		t.Fatal("cleanup returned nil after archived cleanup save failure")
	}
	result := decodeCleanupOutput(t, out)
	if result.Status != cleanupIncomplete || !result.Archived || result.AlreadyArchived ||
		result.Archive == nil || !result.Archive.WorktreeRemoved {
		t.Fatalf("result = %+v, want archived incomplete cleanup with removed worktree", result)
	}
	if strings.Contains(result.Error, "project is still active") ||
		!strings.Contains(result.Error, "archived") ||
		!strings.Contains(result.Error, "injected cleanup completion save failure") {
		t.Fatalf("cleanup error = %q, want accurate archived partial failure", result.Error)
	}
	if pathExists(*manifest.Worktree) ||
		pathExists(filepath.Join(project.ActiveDir(), manifest.Slug)) ||
		!pathExists(filepath.Join(project.ArchivedDir(), manifest.Slug)) {
		t.Fatal("worker cleanup result did not match persisted resources")
	}
}

func TestWorkerCleanupReportsArchivedAfterConcurrentArchive(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)

	previous := programWorkerArchiveProject
	programWorkerArchiveProject = func(proof archiveProofSnapshot, force bool) (archiveResult, error) {
		if _, err := archiveProjectWithProof(proof, force); err != nil {
			return archiveResult{}, err
		}
		return archiveProjectWithProof(proof, force)
	}
	t.Cleanup(func() { programWorkerArchiveProject = previous })

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil {
		t.Fatal("cleanup returned nil after concurrent archive")
	}
	result := decodeCleanupOutput(t, out)
	archivedPath := filepath.Join(project.ArchivedDir(), manifest.Slug)
	if result.Status != cleanupIncomplete || !result.Archived ||
		result.Archive == nil ||
		result.Archive.ProjectLocation != archiveLocationArchived ||
		result.Archive.ProjectPath != archivedPath {
		t.Fatalf("result = %+v, want concurrent final archived location %q", result, archivedPath)
	}
	if strings.Contains(result.Error, "project is still active") ||
		!strings.Contains(result.Error, "archived") {
		t.Fatalf("cleanup error = %q, want accurate archived concurrent result", result.Error)
	}
}

func TestWorkerCleanupReportsArchivedAfterProofRevalidationRollbackFailure(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)

	previousSave := saveArchiveManifest
	advanced := false
	saveArchiveManifest = func(path string, candidate project.Manifest) error {
		if err := project.Save(path, candidate); err != nil {
			return err
		}
		if !advanced && candidate.ArchiveCleanup != nil {
			advanced = true
			commitArchiveFile(t, *manifest.Worktree, "late.txt", "late\n", "late change")
		}
		return nil
	}
	previousRename := archiveRename
	srcDir := filepath.Join(project.ActiveDir(), manifest.Slug)
	dstDir := filepath.Join(project.ArchivedDir(), manifest.Slug)
	archiveRename = func(oldPath, newPath string) error {
		if oldPath == dstDir && newPath == srcDir {
			return errors.New("injected directory restore failure")
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		saveArchiveManifest = previousSave
		archiveRename = previousRename
	})

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil {
		t.Fatal("cleanup returned nil after proof revalidation rollback failure")
	}
	result := decodeCleanupOutput(t, out)
	if result.Status != cleanupIncomplete || !result.Archived {
		t.Fatalf("result = %+v, want archived incomplete cleanup", result)
	}
	if strings.Contains(result.Error, "project is still active") ||
		!strings.Contains(result.Error, "rollback archive metadata") {
		t.Fatalf("cleanup error = %q, want accurate archived rollback failure", result.Error)
	}
}

func TestWorkerCleanupReportsArchivedAfterManifestInstallRollbackFailure(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)

	previousRename := archiveRename
	srcDir := filepath.Join(project.ActiveDir(), manifest.Slug)
	dstDir := filepath.Join(project.ArchivedDir(), manifest.Slug)
	archiveRename = func(oldPath, newPath string) error {
		switch {
		case strings.HasPrefix(filepath.Base(oldPath), ".manifest.archived-") &&
			filepath.Base(newPath) == "manifest.json":
			return errors.New("injected archived manifest install failure")
		case oldPath == dstDir && newPath == srcDir:
			return errors.New("injected directory restore failure")
		default:
			return os.Rename(oldPath, newPath)
		}
	}
	t.Cleanup(func() { archiveRename = previousRename })

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil {
		t.Fatal("cleanup returned nil after manifest install rollback failure")
	}
	result := decodeCleanupOutput(t, out)
	if result.Status != cleanupIncomplete || !result.Archived {
		t.Fatalf("result = %+v, want archived incomplete cleanup", result)
	}
	if strings.Contains(result.Error, "project is still active") ||
		!strings.Contains(result.Error, "rollback project directory") {
		t.Fatalf("cleanup error = %q, want accurate archived manifest failure", result.Error)
	}
}

func TestWorkerCleanupPreservesWatcherRetryWhenBranchDeletionAlsoFails(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{closeErr: errors.New("herdr refused to close the watcher tab")}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installCompletedWatcherState(t, manifest.Slug)
	previous := archiveForceDeleteBranchAt
	archiveForceDeleteBranchAt = func(string, string, string) error {
		return errors.New("injected branch deletion failure")
	}
	t.Cleanup(func() { archiveForceDeleteBranchAt = previous })

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if !errors.Is(err, errProgramWorkerCleanupIncomplete) {
		t.Fatalf("worker cleanup error = %v, want %v", err, errProgramWorkerCleanupIncomplete)
	}
	result := decodeCleanupOutput(t, out)
	watcherRetry := "relay program worker cleanup " + p.Slug + " " + item.ID
	expectedSHA := gitx.RevParse(manifest.Repo, "refs/heads/"+manifest.Branch)
	branchDelete := manualBranchDeleteAtCommand(manifest.Repo, manifest.Branch, expectedSHA)
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
	if !gitx.BranchExists(manifest.Repo, manifest.Branch) || pathExists(*manifest.Worktree) {
		t.Fatal("combined cleanup failure did not preserve the branch after removing the worktree")
	}
}

func TestWorkerCleanupRejectsRewrittenChildManifestBeforeSideEffects(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	victimBranch := "user/victim"
	victimWorktree := addArchiveWorktree(t, manifest.Repo, "victim", victimBranch)
	commitArchiveFile(t, victimWorktree, "victim.txt", "keep\n", "victim work")
	manifest.Branch = victimBranch
	manifest.Worktree = &victimWorktree
	if err := project.Save(
		project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest,
	); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) {
		return []herdr.Agent{liveWorkerAgent(manifest, herdr.StatusIdle)}, nil
	}
	installManagedHerdrFakes(t, client)
	stopped := installStubWatcherState(t, manifest.Slug, true)

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil || !strings.Contains(err.Error(), "resource identity does not match dispatch") {
		t.Fatalf("worker cleanup error = %v, want rewritten resource rejection", err)
	}
	if len(*stopped) != 0 || len(client.exited) != 0 ||
		len(client.closedTabs) != 0 || len(client.closedPanes) != 0 {
		t.Fatalf(
			"cleanup performed side effects: stopped=%v exited=%v tabs=%v panes=%v",
			*stopped, client.exited, client.closedTabs, client.closedPanes,
		)
	}
	if !pathExists(victimWorktree) || !gitx.BranchExists(manifest.Repo, victimBranch) {
		t.Fatal("cleanup removed victim resources from the rewritten manifest")
	}
}

func TestWorkerCleanupRejectsRewrittenChildRepositoryBeforeSideEffects(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	manifest.Repo = newTestRepo(t)
	if err := project.Save(
		project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest,
	); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{}
	installManagedHerdrFakes(t, client)
	stopped := installStubWatcherState(t, manifest.Slug, true)

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil || !strings.Contains(err.Error(), "repository identity does not match dispatch") {
		t.Fatalf("worker cleanup error = %v, want rewritten repository rejection", err)
	}
	if len(*stopped) != 0 || len(client.exited) != 0 ||
		len(client.closedTabs) != 0 || len(client.closedPanes) != 0 {
		t.Fatalf(
			"cleanup performed side effects: stopped=%v exited=%v tabs=%v panes=%v",
			*stopped, client.exited, client.closedTabs, client.closedPanes,
		)
	}
}

func TestWorkerCleanupUpgradesLegacyItemDispatchIdentityBeforeCleanup(t *testing.T) {
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
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("worker cleanup: %v", err)
	}
	result := decodeCleanupOutput(t, out)
	if result.Status != cleanupClean || !result.Archived {
		t.Fatalf("cleanup result = %+v, want successful legacy upgrade and archive", result)
	}
	reloaded, err := program.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, ok := reloaded.Item(item.ID)
	if !ok {
		t.Fatal("upgraded item disappeared")
	}
	if upgraded.ProjectBranch != manifest.Branch ||
		upgraded.ProjectWorktree != *manifest.Worktree {
		t.Fatalf(
			"upgraded dispatch identity = %q/%q, want %q/%q",
			upgraded.ProjectBranch, upgraded.ProjectWorktree,
			manifest.Branch, *manifest.Worktree,
		)
	}
	if pathExists(*manifest.Worktree) || gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("legacy cleanup left the upgraded resources behind")
	}
}

func TestWorkerCleanupLegacyUpgradeWaitsForWorkerLifecycleLock(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	path := program.ManifestPath(program.ActiveDir(), p.Slug)
	clearProgramItemDispatchIdentity(t, path, item.ID)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	stopped := installStubWatcherState(t, manifest.Slug, false)
	lock, err := acquireWorkerStartLock(manifest.Slug, "test worker start")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
		done <- runErr
	}()

	time.Sleep(100 * time.Millisecond)
	waiting, err := program.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	waitingItem, _ := waiting.Item(item.ID)
	if waitingItem.ProjectBranch != "" || waitingItem.ProjectWorktree != "" || len(*stopped) != 0 {
		t.Fatal("cleanup repaired metadata or touched the watcher while worker startup held the lifecycle lock")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cleanup after lifecycle release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not continue after worker lifecycle release")
	}
}

func TestWorkerCleanupLegacyUpgradeRejectsRepositoryMismatchWithoutChangingVictim(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	path := program.ManifestPath(program.ActiveDir(), p.Slug)
	clearProgramItemDispatchIdentity(t, path, item.ID)
	victimRepo, err := filepath.EvalSymlinks(newTestRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	victimBranch := "user/legacy-victim"
	victimWorktree := addArchiveWorktree(t, victimRepo, "legacy-victim", victimBranch)
	manifest.Repo = victimRepo
	manifest.Branch = victimBranch
	manifest.Worktree = &victimWorktree
	if err := project.Save(
		project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest,
	); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{}
	installManagedHerdrFakes(t, client)
	stopped := installStubWatcherState(t, manifest.Slug, true)

	_, err = runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil || !strings.Contains(err.Error(), "repository identity does not match dispatch") {
		t.Fatalf("worker cleanup error = %v, want repository mismatch rejection", err)
	}
	if len(*stopped) != 0 || len(client.exited) != 0 ||
		len(client.closedTabs) != 0 || len(client.closedPanes) != 0 {
		t.Fatalf(
			"cleanup performed side effects: stopped=%v exited=%v tabs=%v panes=%v",
			*stopped, client.exited, client.closedTabs, client.closedPanes,
		)
	}
	if !pathExists(victimWorktree) || !gitx.BranchExists(victimRepo, victimBranch) {
		t.Fatal("cleanup removed legacy mismatch victim resources")
	}
	reloaded, loadErr := program.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	unchanged, _ := reloaded.Item(item.ID)
	if unchanged.ProjectBranch != "" || unchanged.ProjectWorktree != "" {
		t.Fatalf("mismatched legacy identity was persisted: %+v", unchanged)
	}
}

func TestWorkerCleanupLegacyUpgradeRejectsReusedVictimWorktree(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	path := program.ManifestPath(program.ActiveDir(), p.Slug)
	clearProgramItemDispatchIdentity(t, path, item.ID)
	victimBranch := "user/legacy-reused-victim"
	victimWorktree := addArchiveWorktree(t, manifest.Repo, "legacy-reused-victim", victimBranch)
	manifest.Branch = "user/stale-recorded-branch"
	manifest.Worktree = &victimWorktree
	if err := project.Save(
		project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest,
	); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{}
	installManagedHerdrFakes(t, client)
	stopped := installStubWatcherState(t, manifest.Slug, true)

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil ||
		!strings.Contains(err.Error(), "could not be verified without ambiguity") ||
		!strings.Contains(err.Error(), "attached to") ||
		!strings.Contains(err.Error(), "no forced cleanup was authorized") {
		t.Fatalf("worker cleanup error = %v, want fail-closed legacy migration guidance", err)
	}
	if len(*stopped) != 0 || len(client.exited) != 0 ||
		len(client.closedTabs) != 0 || len(client.closedPanes) != 0 {
		t.Fatalf(
			"cleanup performed side effects: stopped=%v exited=%v tabs=%v panes=%v",
			*stopped, client.exited, client.closedTabs, client.closedPanes,
		)
	}
	if !pathExists(victimWorktree) || !gitx.BranchExists(manifest.Repo, victimBranch) {
		t.Fatal("legacy identity migration changed reused victim resources")
	}
	reloaded, loadErr := program.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	unchanged, _ := reloaded.Item(item.ID)
	if unchanged.ProjectBranch != "" || unchanged.ProjectWorktree != "" {
		t.Fatalf("ambiguous legacy identity was persisted: %+v", unchanged)
	}
}

func TestWorkerCleanupLegacyUpgradeRejectsMissingManifestIdentityWithoutChangingVictim(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	path := program.ManifestPath(program.ActiveDir(), p.Slug)
	clearProgramItemDispatchIdentity(t, path, item.ID)
	manifest.Worktree = nil
	if err := project.Save(
		project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest,
	); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{}
	installManagedHerdrFakes(t, client)
	stopped := installStubWatcherState(t, manifest.Slug, true)

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil || !strings.Contains(err.Error(), "manifest has no complete branch/worktree identity") ||
		!strings.Contains(err.Error(), "Restore manifest branch and worktree metadata") {
		t.Fatalf("worker cleanup error = %v, want actionable missing manifest identity rejection", err)
	}
	if len(*stopped) != 0 || !gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("cleanup changed resources after missing manifest identity")
	}
	reloaded, loadErr := program.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	unchanged, _ := reloaded.Item(item.ID)
	if unchanged.ProjectBranch != "" || unchanged.ProjectWorktree != "" {
		t.Fatalf("missing manifest identity was persisted: %+v", unchanged)
	}
}

func TestWorkerCleanupLegacyUpgradeRejectsAmbiguousProjectLocationWithoutChangingVictim(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	path := program.ManifestPath(program.ActiveDir(), p.Slug)
	clearProgramItemDispatchIdentity(t, path, item.ID)
	archivedPath := project.ManifestPath(project.ArchivedDir(), manifest.Slug)
	if err := os.MkdirAll(filepath.Dir(archivedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(archivedPath, manifest); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{}
	installManagedHerdrFakes(t, client)
	stopped := installStubWatcherState(t, manifest.Slug, true)

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil || !strings.Contains(err.Error(), "both active and archived stores") ||
		!strings.Contains(err.Error(), "resolve the ambiguous metadata") {
		t.Fatalf("worker cleanup error = %v, want actionable ambiguity rejection", err)
	}
	if len(*stopped) != 0 || !pathExists(*manifest.Worktree) ||
		!gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("cleanup changed resources while project location was ambiguous")
	}
	reloaded, loadErr := program.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	unchanged, _ := reloaded.Item(item.ID)
	if unchanged.ProjectBranch != "" || unchanged.ProjectWorktree != "" {
		t.Fatalf("ambiguous project identity was persisted: %+v", unchanged)
	}
}

func TestWorkerCleanupLegacyUpgradeSaveFailurePreservesVictim(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	path := program.ManifestPath(program.ActiveDir(), p.Slug)
	clearProgramItemDispatchIdentity(t, path, item.ID)
	previous := programWorkerSaveProgram
	programWorkerSaveProgram = func(string, program.Program) error {
		return errors.New("injected legacy upgrade save failure")
	}
	t.Cleanup(func() { programWorkerSaveProgram = previous })
	client := &fakeHerdrClient{}
	installManagedHerdrFakes(t, client)
	stopped := installStubWatcherState(t, manifest.Slug, true)

	_, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil || !strings.Contains(err.Error(), "injected legacy upgrade save failure") ||
		!strings.Contains(err.Error(), "retry") {
		t.Fatalf("worker cleanup error = %v, want actionable save failure", err)
	}
	if len(*stopped) != 0 || len(client.exited) != 0 ||
		len(client.closedTabs) != 0 || len(client.closedPanes) != 0 {
		t.Fatalf(
			"cleanup performed side effects: stopped=%v exited=%v tabs=%v panes=%v",
			*stopped, client.exited, client.closedTabs, client.closedPanes,
		)
	}
	if !pathExists(*manifest.Worktree) || !gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("cleanup removed resources after legacy upgrade save failure")
	}
	reloaded, loadErr := program.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	unchanged, _ := reloaded.Item(item.ID)
	if unchanged.ProjectBranch != "" || unchanged.ProjectWorktree != "" {
		t.Fatalf("failed legacy identity save changed the item: %+v", unchanged)
	}
}

func clearProgramItemDispatchIdentity(t *testing.T, path, itemID string) {
	t.Helper()
	loaded, err := program.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range loaded.Items {
		if loaded.Items[i].ID == itemID {
			loaded.Items[i].ProjectBranch = ""
			loaded.Items[i].ProjectWorktree = ""
		}
	}
	if err := program.Save(path, loaded); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerCleanupDoesNotReplayClaimedBranchDeletion(t *testing.T) {
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
	if !errors.Is(err, errProgramWorkerCleanupIncomplete) {
		t.Fatalf("first cleanup error = %v, want %v", err, errProgramWorkerCleanupIncomplete)
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
	if err == nil {
		t.Fatal("retry cleanup returned nil after branch config cleanup failure")
	}
	second := decodeCleanupOutput(t, out)
	manualDelete := manualBranchDeleteAtCommand(
		manifest.Repo, manifest.Branch, archived.ArchiveCleanup.ExpectedBranchTip,
	)
	if !second.AlreadyArchived || second.Status != cleanupIncomplete ||
		!strings.Contains(second.Error, "will not retry removal") ||
		second.NextCommand != manualDelete {
		t.Fatalf("retry result = %+v, want claimed cleanup command %q", second, manualDelete)
	}
	if !gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("retry replayed claimed branch deletion")
	}
	if deleteAttempts != 1 {
		t.Fatalf("branch deletion attempts = %d, want 1", deleteAttempts)
	}

	runArchiveGit(
		t, manifest.Repo, "update-ref", "-d", "refs/heads/"+manifest.Branch,
		archived.ArchiveCleanup.ExpectedBranchTip,
	)
	out, err = runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("cleanup after manual branch deletion: %v", err)
	}
	third := decodeCleanupOutput(t, out)
	if !third.AlreadyArchived || third.Status != cleanupClean || third.NextCommand != "" {
		t.Fatalf("manual recovery result = %+v, want clean archived cleanup", third)
	}
	if deleteAttempts != 1 {
		t.Fatalf("manual recovery replayed branch deletion: attempts = %d", deleteAttempts)
	}
	archived = loadArchivedManifest(t, manifest.Slug)
	if archived.ArchiveCleanup.BranchPresent ||
		archived.ArchiveCleanup.ExpectedBranchTip != "" {
		t.Fatalf("branch cleanup proof was not consumed: %+v", archived.ArchiveCleanup)
	}
}

func TestWorkerCleanupReturnsManualBranchConfigCommandForClaimedCleanup(t *testing.T) {
	p, item, manifest := createCleanupFixture(t)
	client := &fakeHerdrClient{}
	client.agentsHook = func() ([]herdr.Agent, error) { return nil, nil }
	installManagedHerdrFakes(t, client)
	installStubWatcherState(t, manifest.Slug, false)
	runArchiveGit(
		t, manifest.Repo, "config", "--local",
		"branch."+manifest.Branch+".remote", "origin",
	)

	previousDelete := archiveForceDeleteBranchAt
	archiveForceDeleteBranchAt = func(repo, branch, expectedSHA string) error {
		runArchiveGit(t, repo, "update-ref", "-d", "refs/heads/"+branch, expectedSHA)
		return errors.New("injected branch config cleanup failure")
	}
	t.Cleanup(func() { archiveForceDeleteBranchAt = previousDelete })

	out, err := runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if !errors.Is(err, errProgramWorkerCleanupIncomplete) {
		t.Fatalf("initial cleanup error = %v, want %v", err, errProgramWorkerCleanupIncomplete)
	}
	command := manualBranchConfigRemoveCommand(manifest.Repo, manifest.Branch)
	first := decodeCleanupOutput(t, out)
	if first.Status != cleanupIncomplete || !first.Archived || first.NextCommand != command {
		t.Fatalf("initial result = %+v, want manual branch config command %q", first, command)
	}

	archiveForceDeleteBranchAt = previousDelete
	previousConfig := archiveRemoveBranchConfig
	archiveRemoveBranchConfig = func(string, string) error {
		return errors.New("injected manual config cleanup failure")
	}
	t.Cleanup(func() { archiveRemoveBranchConfig = previousConfig })

	out, err = runProgramCommand(t, "worker", "cleanup", p.Slug, item.ID, "--json")
	if err == nil {
		t.Fatal("retry cleanup returned nil after branch config cleanup failure")
	}
	second := decodeCleanupOutput(t, out)
	if second.Status != cleanupIncomplete || !second.AlreadyArchived ||
		second.NextCommand != command {
		t.Fatalf("retry result = %+v, want manual branch config command %q", second, command)
	}
	if !strings.Contains(second.Error, "injected manual config cleanup failure") {
		t.Fatalf("retry error = %q, want config cleanup failure", second.Error)
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
	if err == nil {
		t.Fatal("cleanup returned nil after archived branch probe failure")
	}
	result := decodeCleanupOutput(t, out)
	if result.Status != cleanupIncomplete || !result.AlreadyArchived {
		t.Fatalf("result = %+v, want incomplete archived cleanup", result)
	}
	for _, want := range []string{"no durable cleanup proof", "git branch probe failed", "manual inspection"} {
		if !strings.Contains(result.Error, want) {
			t.Fatalf("cleanup error %q is missing %q", result.Error, want)
		}
	}
	if result.NextCommand != "relay program worker cleanup "+p.Slug+" "+item.ID {
		t.Fatalf("next command = %q, want worker cleanup retry", result.NextCommand)
	}
	if !pathExists(*manifest.Worktree) || !gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("legacy archived cleanup removed resources without durable proof")
	}
}
