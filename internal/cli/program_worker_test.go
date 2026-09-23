package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/prwatch"
)

type fakeHerdrClient struct {
	agentResponses [][]herdr.Agent
	// agentsHook answers every agent list from live test state, so a test can
	// model an agent that only appears once Relay has created its project.
	agentsHook func() ([]herdr.Agent, error)
	agentCalls int
	agentErr   error
	tabs       []herdr.TabInfo
	tabsHook   func(string) ([]herdr.TabInfo, error)
	tabsErr    error
	panes      []herdr.Pane
	panesHook  func(string) ([]herdr.Pane, error)
	panesErr   error
	created    []fakeCreatedTab
	runPane    []fakePaneCommand
	// runPaneHook lets a test model the process a pane command launches, so
	// runtime liveness changes exactly when Relay runs the command.
	runPaneHook     func(pane, command string) error
	renamed         []fakeRename
	prompted        []fakePrompt
	promptHook      func() error
	promptErr       error
	exited          []herdr.SessionIdentity
	exitHook        func(herdr.SessionIdentity) (herdr.ExitResult, error)
	exitErr         error
	focused         []string
	notifications   []fakeNotification
	notificationErr error
	tab             herdr.Tab
	createErr       error
	closedTabs      []string
	closedPanes     []string
	closeHook       func(string) error
	closeErr        error
	runPaneErr      error
}

type fakeNotification struct {
	title string
	body  string
}

type fakeCreatedTab struct {
	workspace string
	cwd       string
	label     string
}

type fakePaneCommand struct {
	pane    string
	command string
}

type fakeRename struct {
	target string
	name   string
}

type fakePrompt struct {
	target string
	text   string
}

func (f *fakeHerdrClient) Agents() ([]herdr.Agent, error) {
	index := f.agentCalls
	f.agentCalls++
	if f.agentErr != nil {
		return nil, f.agentErr
	}
	if f.agentsHook != nil {
		return f.agentsHook()
	}
	if len(f.agentResponses) == 0 {
		return nil, nil
	}
	if index >= len(f.agentResponses) {
		index = len(f.agentResponses) - 1
	}
	return f.agentResponses[index], nil
}

func (f *fakeHerdrClient) Tabs(workspace string) ([]herdr.TabInfo, error) {
	if f.tabsHook != nil {
		return f.tabsHook(workspace)
	}
	return f.tabs, f.tabsErr
}

func (f *fakeHerdrClient) Panes(workspace string) ([]herdr.Pane, error) {
	if f.panesHook != nil {
		return f.panesHook(workspace)
	}
	return f.panes, f.panesErr
}

func (f *fakeHerdrClient) CreateTab(workspace, cwd, label string) (herdr.Tab, error) {
	f.created = append(f.created, fakeCreatedTab{workspace: workspace, cwd: cwd, label: label})
	if f.createErr != nil {
		return herdr.Tab{}, f.createErr
	}
	tab := f.tab
	if tab.WorkspaceID == "" {
		tab.WorkspaceID = workspace
	}
	return tab, nil
}

func (f *fakeHerdrClient) CloseTab(tabID string) error {
	f.closedTabs = append(f.closedTabs, tabID)
	if f.closeHook != nil {
		return f.closeHook(tabID)
	}
	return f.closeErr
}

func (f *fakeHerdrClient) ClosePane(paneID string) error {
	f.closedPanes = append(f.closedPanes, paneID)
	return f.closeErr
}

func (f *fakeHerdrClient) RunPane(pane, command string) error {
	f.runPane = append(f.runPane, fakePaneCommand{pane: pane, command: command})
	if f.runPaneHook != nil {
		return f.runPaneHook(pane, command)
	}
	return f.runPaneErr
}

func (f *fakeHerdrClient) RenameAgent(target, name string) error {
	f.renamed = append(f.renamed, fakeRename{target: target, name: name})
	return nil
}

func (f *fakeHerdrClient) PromptAgent(target, text string) error {
	f.prompted = append(f.prompted, fakePrompt{target: target, text: text})
	if f.promptHook != nil {
		return f.promptHook()
	}
	return f.promptErr
}

func (f *fakeHerdrClient) ExitAgent(identity herdr.SessionIdentity) (herdr.ExitResult, error) {
	f.exited = append(f.exited, identity)
	if f.exitHook != nil {
		return f.exitHook(identity)
	}
	if f.exitErr != nil {
		return herdr.ExitResult{}, f.exitErr
	}
	return herdr.ExitResult{Outcome: herdr.ExitedNow, PaneGone: true}, nil
}

func (f *fakeHerdrClient) FocusAgent(target string) error {
	f.focused = append(f.focused, target)
	return nil
}

func (f *fakeHerdrClient) ShowNotification(title, body string) error {
	f.notifications = append(f.notifications, fakeNotification{title: title, body: body})
	return f.notificationErr
}

func createWorkerFixture(t *testing.T, status program.ItemStatus) (program.Program, program.WorkItem, project.Manifest) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
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
	if status == program.ItemBlocked {
		if err := p.BlockItem(item.ID, "waiting for decision"); err != nil {
			t.Fatal(err)
		}
	}
	branch := "test/" + childSlug
	worktree := addArchiveWorktree(t, repo, childSlug, branch)
	manifest := project.Manifest{
		Slug: childSlug, Title: item.Title, Repo: repo, Agent: "copilot",
		Branch: branch, Worktree: &worktree,
		Program: p.Slug, ProgramItem: item.ID, Phase: "implement",
	}
	if err := bindDispatchIdentity(&p, item.ID, manifest); err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	loadedItem, _ := p.Item(item.ID)
	manifestPath := project.ManifestPath(project.ActiveDir(), childSlug)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	return p, loadedItem, manifest
}

func createLegacyWorkerFixture(
	t *testing.T, status program.ItemStatus,
) (program.Program, program.WorkItem, project.Manifest) {
	t.Helper()
	p, item, manifest := createWorkerFixture(t, status)
	expectedWorktree := filepath.Join(
		manifest.Repo, ".worktrees", strings.ReplaceAll(manifest.Branch, "/", "_"),
	)
	runArchiveGit(t, manifest.Repo, "worktree", "move", *manifest.Worktree, expectedWorktree)
	manifest.Worktree = &expectedWorktree
	if err := project.Save(
		project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest,
	); err != nil {
		t.Fatal(err)
	}
	programPath := program.ManifestPath(program.ActiveDir(), p.Slug)
	loaded, err := program.Load(programPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range loaded.Items {
		if loaded.Items[i].ID == item.ID {
			loaded.Items[i].ProjectBranch = ""
			loaded.Items[i].ProjectWorktree = ""
		}
	}
	if err := program.Save(programPath, loaded); err != nil {
		t.Fatal(err)
	}
	reloaded, err := program.Load(programPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyItem, ok := reloaded.Item(item.ID)
	if !ok {
		t.Fatal("legacy item disappeared")
	}
	return reloaded, legacyItem, manifest
}

// installManagedHerdrFakes puts a test inside a healthy Herdr workspace, which
// every managed program and managed child command now requires.
func installManagedHerdrFakes(t *testing.T, client *fakeHerdrClient) *fakeHerdrClient {
	t.Helper()
	if client == nil {
		client = &fakeHerdrClient{}
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	previousClient := newHerdrClient
	previousAvailable := herdrAvailable
	newHerdrClient = func() herdrRuntimeClient { return client }
	herdrAvailable = func() bool { return true }
	t.Cleanup(func() {
		newHerdrClient = previousClient
		herdrAvailable = previousAvailable
	})
	return client
}

func installWorkerFakes(t *testing.T, client *fakeHerdrClient) {
	t.Helper()
	previousClient := newHerdrClient
	previousAvailable := herdrAvailable
	previousNow := workerNow
	previousSleep := workerSleep
	now := time.Unix(100, 0)
	newHerdrClient = func() herdrRuntimeClient { return client }
	herdrAvailable = func() bool { return true }
	workerNow = func() time.Time { return now }
	workerSleep = func(duration time.Duration) { now = now.Add(duration) }
	t.Cleanup(func() {
		newHerdrClient = previousClient
		herdrAvailable = previousAvailable
		workerNow = previousNow
		workerSleep = previousSleep
	})
}

func TestProgramWorkerStartCreatesRunsPollsAndRenames(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	worker := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug + " - GitHub Copilot",
		ForegroundCWD: *manifest.Worktree, NativeSessionID: "session-9",
	}
	client := &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{nil, nil, {worker}},
		tab:            herdr.Tab{ID: "w7:t9", RootPaneID: "w7:p9"},
	}
	installWorkerFakes(t, client)

	programBefore, err := os.ReadFile(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	projectBefore, err := os.ReadFile(project.ManifestPath(project.ActiveDir(), manifest.Slug))
	if err != nil {
		t.Fatal(err)
	}
	out, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("worker start: %v", err)
	}

	var got programWorkerOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	want := programWorkerOutput{
		Item: item.ID, Project: manifest.Slug, Worktree: *manifest.Worktree,
		WorkspaceID: "w7", TabID: "w7:t9", PaneID: "w7:p9",
		WorkerName: "governance-" + item.ID, NativeSessionID: "session-9",
		Status: herdr.StatusIdle, Adopted: false, FocusCommand: "herdr agent focus w7:p9",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("output:\n got: %#v\nwant: %#v", got, want)
	}
	if wantCreated := []fakeCreatedTab{{"w7", *manifest.Worktree, item.ID + ": " + item.Title}}; !reflect.DeepEqual(client.created, wantCreated) {
		t.Fatalf("created tabs = %#v, want %#v", client.created, wantCreated)
	}
	if wantRun := []fakePaneCommand{{"w7:p9", "relay resume '" + manifest.Slug + "'"}}; !reflect.DeepEqual(client.runPane, wantRun) {
		t.Fatalf("pane commands = %#v, want %#v", client.runPane, wantRun)
	}
	if wantRename := []fakeRename{{"w7:p9", "governance-" + item.ID}}; !reflect.DeepEqual(client.renamed, wantRename) {
		t.Fatalf("renames = %#v, want %#v", client.renamed, wantRename)
	}
	programAfter, err := os.ReadFile(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	projectAfter, err := os.ReadFile(project.ManifestPath(project.ActiveDir(), manifest.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(programAfter, programBefore) || !reflect.DeepEqual(projectAfter, projectBefore) {
		t.Fatal("worker start persisted runtime state")
	}
}

func TestProgramWorkerStartRepairsLegacyDispatchIdentity(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createLegacyWorkerFixture(t, program.ItemDispatched)
	worker := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug + " - GitHub Copilot",
		ForegroundCWD: *manifest.Worktree,
	}
	client := &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{nil, nil, {worker}},
		tab:            herdr.Tab{ID: "w7:t9", RootPaneID: "w7:p9"},
	}
	installWorkerFakes(t, client)

	if _, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID, "--json"); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	reloaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	repaired, ok := reloaded.Item(item.ID)
	if !ok {
		t.Fatal("repaired item disappeared")
	}
	if repaired.ProjectBranch != manifest.Branch ||
		repaired.ProjectWorktree != *manifest.Worktree {
		t.Fatalf(
			"repaired identity = %q/%q, want %q/%q",
			repaired.ProjectBranch, repaired.ProjectWorktree,
			manifest.Branch, *manifest.Worktree,
		)
	}
	if len(client.created) != 1 || len(client.runPane) != 1 || len(client.renamed) != 1 {
		t.Fatalf(
			"worker was not started after repair: created=%v run=%v renamed=%v",
			client.created, client.runPane, client.renamed,
		)
	}
}

func TestProgramWorkerEnsureRepairsLegacyDispatchIdentity(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createLegacyWorkerFixture(t, program.ItemDispatched)
	worker := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug + " - GitHub Copilot",
		ForegroundCWD: *manifest.Worktree,
	}
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{worker}}}
	installWorkerFakes(t, client)

	out, err := runProgramCommand(t, "worker", "ensure", p.Slug, "--json")
	if err != nil {
		t.Fatalf("worker ensure: %v", err)
	}
	var result programWorkerEnsureOutput
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	if len(result.Warnings) != 0 || len(result.Entries) != 1 ||
		result.Entries[0].Worker == nil || !result.Entries[0].Worker.Adopted {
		t.Fatalf("worker ensure result = %+v, want adopted repaired worker", result)
	}
	reloaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	repaired, ok := reloaded.Item(item.ID)
	if !ok {
		t.Fatal("repaired item disappeared")
	}
	if repaired.ProjectBranch != manifest.Branch ||
		repaired.ProjectWorktree != *manifest.Worktree {
		t.Fatalf(
			"repaired identity = %q/%q, want %q/%q",
			repaired.ProjectBranch, repaired.ProjectWorktree,
			manifest.Branch, *manifest.Worktree,
		)
	}
}

func TestProgramWorkerStartRejectsLegacyManifestRedirectToVictimWorktree(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createLegacyWorkerFixture(t, program.ItemDispatched)
	originalBranch := manifest.Branch
	originalWorktree := *manifest.Worktree
	victimBranch := "test/legacy-worker-victim"
	victimWorktree := addArchiveWorktree(t, manifest.Repo, "legacy-worker-victim", victimBranch)
	manifest.Branch = victimBranch
	manifest.Worktree = &victimWorktree
	if err := project.Save(
		project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest,
	); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{{
		Status: herdr.StatusIdle, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug + " - GitHub Copilot",
		ForegroundCWD: victimWorktree,
	}}}}
	installWorkerFakes(t, client)

	_, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID, "--json")
	if err == nil || !strings.Contains(err.Error(), "expected canonical worktree path") {
		t.Fatalf("worker start error = %v, want victim worktree rejection", err)
	}
	if client.agentCalls != 0 || len(client.created) != 0 ||
		len(client.runPane) != 0 || len(client.renamed) != 0 {
		t.Fatalf(
			"victim rejection mutated Herdr: agents=%d created=%v run=%v renamed=%v",
			client.agentCalls, client.created, client.runPane, client.renamed,
		)
	}
	reloaded, loadErr := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	unchanged, _ := reloaded.Item(item.ID)
	if unchanged.ProjectBranch != "" || unchanged.ProjectWorktree != "" {
		t.Fatalf("victim identity was persisted: %+v", unchanged)
	}
	if !pathExists(originalWorktree) || !gitx.BranchExists(manifest.Repo, originalBranch) ||
		!pathExists(victimWorktree) || !gitx.BranchExists(manifest.Repo, victimBranch) {
		t.Fatal("legacy worker repair changed original or victim resources")
	}
}

func TestProgramWorkerStartRejectsAmbiguousLegacyResourceOwnership(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createLegacyWorkerFixture(t, program.ItemDispatched)
	duplicate := manifest
	duplicate.Slug = "duplicate-legacy-worker"
	duplicate.Program = "other-program"
	duplicate.ProgramItem = "other-item"
	duplicatePath := project.ManifestPath(project.ActiveDir(), duplicate.Slug)
	if err := os.MkdirAll(filepath.Dir(duplicatePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(duplicatePath, duplicate); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{}
	installWorkerFakes(t, client)

	_, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID, "--json")
	if err == nil || !strings.Contains(err.Error(), duplicate.Slug) ||
		!strings.Contains(err.Error(), "also claimed") {
		t.Fatalf("worker start error = %v, want ambiguous ownership rejection", err)
	}
	if client.agentCalls != 0 || len(client.created) != 0 ||
		len(client.runPane) != 0 || len(client.renamed) != 0 {
		t.Fatalf(
			"ownership rejection mutated Herdr: agents=%d created=%v run=%v renamed=%v",
			client.agentCalls, client.created, client.runPane, client.renamed,
		)
	}
	reloaded, loadErr := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	unchanged, _ := reloaded.Item(item.ID)
	if unchanged.ProjectBranch != "" || unchanged.ProjectWorktree != "" {
		t.Fatalf("ambiguous identity was persisted: %+v", unchanged)
	}
	if !pathExists(*manifest.Worktree) || !gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("ambiguous repair changed the shared resources")
	}
}

func TestProgramWorkerStartLegacyRepairSaveFailureStopsBeforeHerdr(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createLegacyWorkerFixture(t, program.ItemDispatched)
	previousSave := programWorkerStartSaveProgram
	programWorkerStartSaveProgram = func(string, program.Program) error {
		return errors.New("injected worker identity save failure")
	}
	t.Cleanup(func() { programWorkerStartSaveProgram = previousSave })
	client := &fakeHerdrClient{}
	installWorkerFakes(t, client)

	_, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID, "--json")
	if err == nil || !strings.Contains(err.Error(), "injected worker identity save failure") ||
		!strings.Contains(err.Error(), "no Herdr state was changed") {
		t.Fatalf("worker start error = %v, want fail-closed save failure", err)
	}
	if client.agentCalls != 0 || len(client.created) != 0 ||
		len(client.runPane) != 0 || len(client.renamed) != 0 {
		t.Fatalf(
			"save failure mutated Herdr: agents=%d created=%v run=%v renamed=%v",
			client.agentCalls, client.created, client.runPane, client.renamed,
		)
	}
	reloaded, loadErr := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	unchanged, _ := reloaded.Item(item.ID)
	if unchanged.ProjectBranch != "" || unchanged.ProjectWorktree != "" {
		t.Fatalf("failed repair changed the item: %+v", unchanged)
	}
	if !pathExists(*manifest.Worktree) || !gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("save failure changed the child resources")
	}
}

func TestProgramWorkerStartLegacyRepairRevalidatesSavedIdentityBeforeHerdr(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createLegacyWorkerFixture(t, program.ItemDispatched)
	previousSave := programWorkerStartSaveProgram
	programWorkerStartSaveProgram = func(path string, candidate program.Program) error {
		if err := program.Save(path, candidate); err != nil {
			return err
		}
		redirected, err := program.Load(path)
		if err != nil {
			return err
		}
		for i := range redirected.Items {
			if redirected.Items[i].ID == item.ID {
				redirected.Items[i].ProjectWorktree = filepath.Join(
					manifest.Repo, ".worktrees", "post-save-victim",
				)
			}
		}
		return program.Save(path, redirected)
	}
	t.Cleanup(func() { programWorkerStartSaveProgram = previousSave })
	client := &fakeHerdrClient{}
	installWorkerFakes(t, client)

	_, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID, "--json")
	if err == nil || !strings.Contains(err.Error(), "reloaded program/item/manifest identity") ||
		!strings.Contains(err.Error(), "no Herdr state was changed") {
		t.Fatalf("worker start error = %v, want post-save revalidation failure", err)
	}
	if client.agentCalls != 0 || len(client.created) != 0 ||
		len(client.runPane) != 0 || len(client.renamed) != 0 {
		t.Fatalf(
			"post-save mismatch mutated Herdr: agents=%d created=%v run=%v renamed=%v",
			client.agentCalls, client.created, client.runPane, client.renamed,
		)
	}
	if !pathExists(*manifest.Worktree) || !gitx.BranchExists(manifest.Repo, manifest.Branch) {
		t.Fatal("post-save mismatch changed the child resources")
	}
}

func TestProgramWorkerStartShellQuotesProjectSlug(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	unsafeSlug := "child;echo-owned"
	manifest.Slug = unsafeSlug
	manifest.Program = p.Slug
	manifest.ProgramItem = item.ID
	manifestPath := project.ManifestPath(project.ActiveDir(), unsafeSlug)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(project.ActiveDir(), "governance-"+item.ID)); err != nil {
		t.Fatal(err)
	}
	p.Items[0].ProjectSlug = unsafeSlug
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	worker := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		TerminalTitle: "relay:" + unsafeSlug + " - GitHub Copilot",
		CWD:           p.Repo, ForegroundCWD: *manifest.Worktree,
	}
	client := &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{nil, {worker}},
		tab:            herdr.Tab{ID: "w7:t9", RootPaneID: "w7:p9"},
	}
	installWorkerFakes(t, client)

	if _, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	want := []fakePaneCommand{{pane: "w7:p9", command: "relay resume 'child;echo-owned'"}}
	if !reflect.DeepEqual(client.runPane, want) {
		t.Fatalf("pane commands = %#v, want %#v", client.runPane, want)
	}
}

func TestProgramWorkerStartAdoptsExistingWorkerWithoutCreatingTab(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	pluginDir := filepath.Join(*manifest.Worktree, "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	existing := herdr.Agent{
		Status: herdr.StatusWorking, PaneID: "w2:p4", TabID: "w2:t4", WorkspaceID: "w2",
		TerminalTitle: "relay:" + manifest.Slug + " - GitHub Copilot",
		CWD:           manifest.Repo, ForegroundCWD: pluginDir,
	}
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{existing}}}
	installWorkerFakes(t, client)

	out, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID)
	if err != nil {
		t.Fatalf("worker start: %v", err)
	}
	if len(client.created) != 0 || len(client.runPane) != 0 || len(client.renamed) != 0 {
		t.Fatalf("adoption mutated Herdr: created=%#v run=%#v renamed=%#v", client.created, client.runPane, client.renamed)
	}
	for _, want := range []string{"Adopted: true", "Status: working", "herdr agent focus w2:p4"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestProgramWorkerEnsureStartsEveryMissingActiveWorker(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	worker := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug + " - GitHub Copilot",
		ForegroundCWD: *manifest.Worktree,
	}
	client := &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{nil, nil, {worker}},
		tab:            herdr.Tab{ID: "w7:t9", RootPaneID: "w7:p9"},
	}
	installWorkerFakes(t, client)

	out, err := runProgramCommand(t, "worker", "ensure", p.Slug, "--json")
	if err != nil {
		t.Fatalf("worker ensure: %v", err)
	}
	var got programWorkerEnsureOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	wantWorker := programWorkerOutput{
		Item: item.ID, Project: manifest.Slug, Worktree: *manifest.Worktree,
		WorkspaceID: "w7", TabID: "w7:t9", PaneID: "w7:p9",
		WorkerName: "governance-" + item.ID, Status: herdr.StatusIdle,
		FocusCommand: "herdr agent focus w7:p9",
	}
	want := programWorkerEnsureOutput{
		Entries: []programWorkerEnsureEntry{{
			Item: item.ID, ItemStatus: program.ItemDispatched, Project: manifest.Slug,
			Live: true, Worker: &wantWorker,
		}},
		Warnings: []programItemWarning{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ensure output:\n got: %#v\nwant: %#v", got, want)
	}
	if len(client.created) != 1 || len(client.runPane) != 1 {
		t.Fatalf("ensure did not start the missing worker: created=%#v run=%#v", client.created, client.runPane)
	}
}

func TestProgramWorkerEnsureIsEmptyBeforeProgramActivation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	repo := t.TempDir()
	p, err := program.New("governance", "Ship governed changes", repo, "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{}
	installWorkerFakes(t, client)

	out, err := runProgramCommand(t, "worker", "ensure", p.Slug, "--json")
	if err != nil {
		t.Fatalf("worker ensure: %v", err)
	}
	var got programWorkerEnsureOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	want := programWorkerEnsureOutput{
		Entries:  []programWorkerEnsureEntry{},
		Warnings: []programItemWarning{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ensure output = %#v, want %#v", got, want)
	}
	if client.agentCalls != 0 {
		t.Fatalf("empty ensure queried Herdr agents %d times", client.agentCalls)
	}
}

func TestProgramWorkerEnsureStartsManagedWatcherForPRBackedWorker(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, _, manifest := createWorkerFixture(t, program.ItemDispatched)
	p.Items[0].Status = program.ItemInReview
	p.Items[0].PRRef = "#42"
	p.Items[0].InReviewAt = p.Items[0].UpdatedAt
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	worker := herdr.Agent{
		Status: herdr.StatusWorking, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug + " - GitHub Copilot",
		ForegroundCWD: *manifest.Worktree,
	}
	client := &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{worker}, {worker}},
		tabs: []herdr.TabInfo{
			{ID: "watch-tab", WorkspaceID: "w7", Label: "relay-pr-watch:" + manifest.Slug, PaneCount: 1},
			{ID: "watch-duplicate", WorkspaceID: "w7", Label: "relay-pr-watch:" + manifest.Slug, PaneCount: 1},
		},
		panes: []herdr.Pane{
			{ID: "watch-pane", TabID: "watch-tab", WorkspaceID: "w7", TerminalID: "watch-term"},
			{ID: "watch-duplicate-pane", TabID: "watch-duplicate", WorkspaceID: "w7", TerminalID: "duplicate-term"},
		},
	}
	installWorkerFakes(t, client)
	installPRWatchFakes(t, client)
	prWatchRequireManaged = func(string) error { return nil }
	launch := &prWatchLaunch{
		stale: prwatch.State{
			PID: 77, Status: prwatch.StatusComplete, Mode: prwatch.ModeManaged,
			OwnerSlug: manifest.Slug, WorkspaceID: "w7", TabID: "watch-tab",
			PaneID: "watch-pane", TerminalID: "watch-term",
		},
		running: prwatch.State{
			PID: 77, Status: prwatch.StatusRunning, Mode: prwatch.ModeManaged,
			OwnerSlug: manifest.Slug, WorkspaceID: "w7", TabID: "watch-tab",
			PaneID: "watch-pane", TerminalID: "watch-term",
		},
	}
	launch.install(t, client)

	out, err := runProgramCommand(t, "worker", "ensure", p.Slug, "--json")
	if err != nil {
		t.Fatalf("worker ensure: %v", err)
	}
	var got programWorkerEnsureOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	entry := got.Entries[0]
	if len(got.Entries) != 1 || !entry.Live || entry.ItemStatus != program.ItemInReview ||
		entry.Worker == nil || entry.Watcher == nil {
		t.Fatalf("ensure output = %#v, want one live worker entry carrying its watcher", got)
	}
	if entry.Watcher.TabID != "watch-tab" || !entry.Watcher.TabReused ||
		entry.Watcher.State.Mode != prwatch.ModeManaged || len(entry.Watcher.ClosedTabIDs) != 0 {
		t.Fatalf("watcher = %#v, want a managed watcher in the reused tab", entry.Watcher)
	}
	if len(client.closedTabs) != 0 {
		t.Fatalf("closed label-only tabs = %#v, want none", client.closedTabs)
	}
	if len(client.created) != 0 {
		t.Fatalf("ensure created tabs instead of adopting worker and reusing watcher: %#v", client.created)
	}
	if want := []fakePaneCommand{{
		pane: "watch-pane",
		command: "relay pr watch run '" + manifest.Slug + "' --mode 'managed' --owner '" +
			manifest.Slug + "' --workspace 'w7' --tab 'watch-tab' --pane 'watch-pane' --terminal 'watch-term'",
	}}; !reflect.DeepEqual(client.runPane, want) {
		t.Fatalf("pane commands = %#v, want %#v", client.runPane, want)
	}
}

// Herdr recognizes a cold-started worker by its worktree before the agent has
// set the exact Relay terminal title a managed watcher's owner check needs. The
// watcher start must wait for that title rather than refuse.
func TestProgramWorkerEnsureWaitsForTheNewWorkerTitleBeforeStartingItsWatcher(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, _, manifest := createWorkerFixture(t, program.ItemDispatched)
	p.Items[0].Status = program.ItemInReview
	p.Items[0].PRRef = "#42"
	p.Items[0].InReviewAt = p.Items[0].UpdatedAt
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}

	untitled := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		ForegroundCWD: *manifest.Worktree,
	}
	titled := untitled
	titled.TerminalTitle = "relay:" + manifest.Slug + " - GitHub Copilot"
	client := &fakeHerdrClient{tab: herdr.Tab{
		ID: "watch-tab", RootPaneID: "watch-pane", TerminalID: "watch-term",
	}}
	agentCalls := 0
	client.agentsHook = func() ([]herdr.Agent, error) {
		agentCalls++
		switch {
		case agentCalls <= 2:
			return nil, nil
		case agentCalls <= 4:
			// Discovered by worktree while the agent is still booting.
			return []herdr.Agent{untitled}, nil
		default:
			return []herdr.Agent{titled}, nil
		}
	}
	installWorkerFakes(t, client)
	installPRWatchFakes(t, client)
	prWatchRequireManaged = func(string) error { return nil }
	launch := &prWatchLaunch{
		staleErr: os.ErrNotExist,
		running: prwatch.State{
			PID: 77, Status: prwatch.StatusRunning, Mode: prwatch.ModeManaged,
			OwnerSlug: manifest.Slug, WorkspaceID: "w7", TabID: "watch-tab",
			PaneID: "watch-pane", TerminalID: "watch-term",
		},
	}
	launch.install(t, client)

	out, err := runProgramCommand(t, "worker", "ensure", p.Slug, "--json")
	if err != nil {
		t.Fatalf("worker ensure: %v", err)
	}
	var got programWorkerEnsureOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	if len(got.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want the delayed title waited out", got.Warnings)
	}
	if len(got.Entries) != 1 || got.Entries[0].Watcher == nil || got.Entries[0].Watcher.Adopted {
		t.Fatalf("ensure output = %#v, want a freshly started watcher", got)
	}
	if agentCalls <= 4 {
		t.Fatalf("agent list calls = %d, want the watcher start to wait for the exact title", agentCalls)
	}
}

// Two sessions claiming one project identity is never resolved by waiting, so
// the owner wait reports it on the first observation.
func TestWaitForProgramWorkerOwnerFailsImmediatelyOnDuplicateOwners(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{
		{TerminalTitle: "relay:child", PaneID: "w7:p1"},
		{TerminalTitle: "relay:child", PaneID: "w7:p2"},
	}}}
	installWorkerFakes(t, client)

	err := waitForProgramWorkerOwner(client, "child", "")
	var duplicate *herdr.DuplicateProjectOwnerError
	if !errors.As(err, &duplicate) {
		t.Fatalf("error = %v, want a duplicate project owner error", err)
	}
	if client.agentCalls != 1 {
		t.Fatalf("agent list calls = %d, want exactly 1", client.agentCalls)
	}
}

// The tech lead reads this output as often as the JSON one, so every watcher
// outcome it acted on must be visible in it.
func TestProgramWorkerEnsureReportsWatcherOutcomesInReadableOutput(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p.Items[0].Status = program.ItemInReview
	p.Items[0].PRRef = "#42"
	p.Items[0].InReviewAt = p.Items[0].UpdatedAt
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}

	worker := herdr.Agent{
		Status: herdr.StatusWorking, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug + " - GitHub Copilot",
		ForegroundCWD: *manifest.Worktree,
	}
	client := &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{worker}},
		tabs: []herdr.TabInfo{
			{ID: "watch-tab", WorkspaceID: "w7", Label: "relay-pr-watch:" + manifest.Slug, PaneCount: 1},
			{ID: "watch-duplicate", WorkspaceID: "w7", Label: "relay-pr-watch:" + manifest.Slug, PaneCount: 1},
			{
				ID: "watch-live", WorkspaceID: "w7", Label: "relay-pr-watch:" + manifest.Slug,
				PaneCount: 1, Status: herdr.StatusWorking,
			},
		},
		panes: []herdr.Pane{
			{ID: "watch-pane", TabID: "watch-tab", WorkspaceID: "w7", TerminalID: "watch-term"},
			{ID: "watch-duplicate-pane", TabID: "watch-duplicate", WorkspaceID: "w7", TerminalID: "duplicate-term"},
			{ID: "watch-live-pane", TabID: "watch-live", WorkspaceID: "w7", TerminalID: "live-term"},
		},
	}
	installWorkerFakes(t, client)
	installPRWatchFakes(t, client)
	prWatchRequireManaged = func(string) error { return nil }
	launch := &prWatchLaunch{
		stale: prwatch.State{
			PID: 77, Status: prwatch.StatusComplete, Mode: prwatch.ModeManaged,
			OwnerSlug: manifest.Slug, WorkspaceID: "w7", TabID: "watch-tab",
			PaneID: "watch-pane", TerminalID: "watch-term",
		},
		running: prwatch.State{
			PID: 77, Status: prwatch.StatusRunning, Mode: prwatch.ModeManaged,
			OwnerSlug: manifest.Slug, WorkspaceID: "w7", TabID: "watch-tab",
			PaneID: "watch-pane", TerminalID: "watch-term",
		},
	}
	launch.install(t, client)

	out, err := runProgramCommand(t, "worker", "ensure", p.Slug)
	if err != nil {
		t.Fatalf("worker ensure: %v", err)
	}
	for _, want := range []string{
		item.ID, "in-review", "working", manifest.Slug,
		"watcher: started", "Tab: watch-tab (reused)",
		"preserved unrecorded Herdr tab watch-duplicate",
		"preserved unrecorded Herdr tab watch-live",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q is missing %q", out, want)
		}
	}
}

func TestProgramWorkerListReportsLiveRuntimeWithoutWritingState(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	live := herdr.Agent{
		Status: herdr.StatusBlocked, PaneID: "w7:p3", TabID: "w7:t3", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug + " - GitHub Copilot",
		ForegroundCWD: *manifest.Worktree, NativeSessionID: "native-3",
	}
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{live}}}
	installWorkerFakes(t, client)
	before, err := os.ReadFile(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "worker", "list", p.Slug, "--json")
	if err != nil {
		t.Fatalf("worker list: %v", err)
	}
	var got programWorkerListOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	want := programWorkerListOutput{
		Entries: []programWorkerListEntry{{
			Item: item.ID, ItemStatus: program.ItemDispatched, Project: manifest.Slug,
			Worktree: *manifest.Worktree, WorkerName: "governance-" + item.ID,
			WorkspaceID: "w7", TabID: "w7:t3", PaneID: "w7:p3",
			NativeSessionID: "native-3", Status: herdr.StatusBlocked, Live: true,
		}},
		Warnings: []programItemWarning{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list output:\n got: %#v\nwant: %#v", got, want)
	}
	after, err := os.ReadFile(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("worker list wrote program state")
	}
}

func TestProgramWorkerListReportsNotRunningWithoutRuntimeIDs(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{nil}}
	installWorkerFakes(t, client)

	out, err := runProgramCommand(t, "worker", "list", p.Slug, "--json")
	if err != nil {
		t.Fatalf("worker list: %v", err)
	}
	var got programWorkerListOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	want := programWorkerListOutput{
		Entries: []programWorkerListEntry{{
			Item: item.ID, ItemStatus: program.ItemDispatched, Project: manifest.Slug,
			Worktree: *manifest.Worktree, WorkerName: "governance-" + item.ID,
			Status: workerNotRunning,
		}},
		Warnings: []programItemWarning{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list output:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestProgramWorkerListContinuesPastUnavailableItemsAndSkipsPendingLinks(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	missing, err := p.AddItem(program.WorkItem{Title: "Missing child", Priority: program.PriorityP2})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(missing.ID, "governance-"+missing.ID); err != nil {
		t.Fatal(err)
	}
	archived, err := p.AddItem(program.WorkItem{Title: "Archived child", Priority: program.PriorityP2})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(archived.ID, "governance-"+archived.ID); err != nil {
		t.Fatal(err)
	}
	saveArchivedWorkerChild(t, p, archived)
	pending, err := p.AddItem(program.WorkItem{Title: "Linked but pending", Priority: program.PriorityP3})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.LinkItem(pending.ID, "governance-"+pending.ID); err != nil {
		t.Fatal(err)
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{}
	installWorkerFakes(t, client)

	out, err := runProgramCommand(t, "worker", "list", p.Slug, "--json")
	if err != nil {
		t.Fatalf("worker list: %v", err)
	}
	var got struct {
		Entries  []programWorkerListEntry `json:"entries"`
		Warnings []struct {
			Item    string `json:"item"`
			Project string `json:"project"`
			Error   string `json:"error"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	wantEntry := programWorkerListEntry{
		Item: item.ID, ItemStatus: program.ItemDispatched, Project: manifest.Slug,
		Worktree: *manifest.Worktree, WorkerName: "governance-" + item.ID,
		Status: workerNotRunning,
	}
	if !reflect.DeepEqual(got.Entries, []programWorkerListEntry{wantEntry}) {
		t.Fatalf("entries = %#v, want %#v", got.Entries, []programWorkerListEntry{wantEntry})
	}
	if len(got.Warnings) != 2 || got.Warnings[0].Item != missing.ID ||
		got.Warnings[0].Project != "governance-"+missing.ID ||
		!strings.Contains(got.Warnings[0].Error, "is not active") ||
		got.Warnings[1].Item != archived.ID ||
		got.Warnings[1].Project != "governance-"+archived.ID ||
		!strings.Contains(got.Warnings[1].Error, "is not active") {
		t.Fatalf("warnings = %#v", got.Warnings)
	}
	text, err := runProgramCommand(t, "worker", "list", p.Slug)
	if err != nil {
		t.Fatalf("worker list text: %v", err)
	}
	if !strings.Contains(text, item.ID) || !strings.Contains(text, "Warning: "+missing.ID) {
		t.Fatalf("worker list text = %q", text)
	}
}

func saveArchivedWorkerChild(t *testing.T, p program.Program, item program.WorkItem) project.Manifest {
	t.Helper()
	slug := "governance-" + item.ID
	worktree := filepath.Join(p.Repo, ".worktrees", slug)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := project.Manifest{
		Slug: slug, Title: item.Title, Repo: p.Repo, Worktree: &worktree,
		Program: p.Slug, ProgramItem: item.ID, Phase: "implement",
	}
	path := project.ManifestPath(project.ArchivedDir(), slug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(path, manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestProgramWorkerFocusTargetsLiveOwner(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemBlocked)
	live := herdr.Agent{
		Status: herdr.StatusDone, PaneID: "w7:p5", TabID: "w7:t5", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug + " - GitHub Copilot",
		ForegroundCWD: *manifest.Worktree,
	}
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{live}}}
	installWorkerFakes(t, client)

	if _, err := runProgramCommand(t, "worker", "focus", p.Slug, item.ID); err != nil {
		t.Fatalf("worker focus: %v", err)
	}
	if want := []string{"w7:p5"}; !reflect.DeepEqual(client.focused, want) {
		t.Fatalf("focused = %#v, want %#v", client.focused, want)
	}
}

func TestProgramWorkerNotifyPromptsOnceForNewInbox(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	messages := make([]mailbox.Message, 0, 2)
	for index, id := range []string{"instruction-1", "instruction-2"} {
		message, err := mailbox.Send(messageProjectDir(manifest), mailbox.Inbox, mailbox.Message{
			ID: id, Kind: mailbox.KindInstruction, Program: p.Slug, Item: item.ID,
			From: mailbox.ActorTL, To: mailbox.ActorWorker, Body: "Use the adapter.",
			CreatedAt: time.Date(2026, time.August, 26, 7, 0, index, 0, time.UTC).Format(time.RFC3339),
		})
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	live := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p5", TabID: "w7:t5", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug,
		ForegroundCWD: *manifest.Worktree,
	}
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{live}, {live}}}
	installWorkerFakes(t, client)

	first, err := runProgramCommand(t, "worker", "notify", p.Slug, item.ID)
	if err != nil {
		t.Fatalf("first worker notify: %v", err)
	}
	second, err := runProgramCommand(t, "worker", "notify", p.Slug, item.ID)
	if err != nil {
		t.Fatalf("second worker notify: %v", err)
	}
	if want := []fakePrompt{{"w7:p5", "Check your Relay inbox."}}; !reflect.DeepEqual(client.prompted, want) {
		t.Fatalf("prompted = %#v, want %#v", client.prompted, want)
	}
	for _, message := range messages {
		notified, err := mailbox.IsNotified(messageProjectDir(manifest), message.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !notified {
			t.Fatalf("inbox message %s was not marked notified", message.ID)
		}
	}
	if !strings.Contains(first, "Notified "+item.ID) {
		t.Fatalf("first output = %q", first)
	}
	if !strings.Contains(second, "no unnotified inbox messages") {
		t.Fatalf("second output = %q", second)
	}
}

func TestProgramWorkerNotifyWithoutInboxDoesNotPrompt(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, _ := createWorkerFixture(t, program.ItemDispatched)
	client := &fakeHerdrClient{}
	installWorkerFakes(t, client)

	out, err := runProgramCommand(t, "worker", "notify", p.Slug, item.ID)
	if err != nil {
		t.Fatalf("worker notify: %v", err)
	}
	if len(client.prompted) != 0 || client.agentCalls != 0 {
		t.Fatalf("no-message notify called Herdr: agents=%d prompted=%#v", client.agentCalls, client.prompted)
	}
	if !strings.Contains(out, "no unnotified inbox messages") {
		t.Fatalf("output = %q", out)
	}
}

func TestProgramWorkerNotifyPromptsDoneWorker(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	message, err := mailbox.Send(messageProjectDir(manifest), mailbox.Inbox, mailbox.Message{
		ID: "instruction-1", Kind: mailbox.KindInstruction, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorTL, To: mailbox.ActorWorker, Body: "Use the adapter.",
		CreatedAt: "2026-08-26T07:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{{
		Status: herdr.StatusDone, PaneID: "w7:p5", TerminalTitle: "relay:" + manifest.Slug,
		ForegroundCWD: *manifest.Worktree,
	}}}}
	installWorkerFakes(t, client)

	if _, err := runProgramCommand(t, "worker", "notify", p.Slug, item.ID); err != nil {
		t.Fatalf("worker notify: %v", err)
	}
	if want := []fakePrompt{{"w7:p5", "Check your Relay inbox."}}; !reflect.DeepEqual(client.prompted, want) {
		t.Fatalf("prompted = %#v, want %#v", client.prompted, want)
	}
	notified, err := mailbox.IsNotified(messageProjectDir(manifest), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !notified {
		t.Fatal("done worker inbox was not marked notified")
	}
}

func TestProgramWorkerNotifyWaitsForBusyWorker(t *testing.T) {
	for _, status := range []herdr.Status{herdr.StatusWorking, herdr.StatusBlocked} {
		t.Run(string(status), func(t *testing.T) {
			t.Setenv("HERDR_ENV", "1")
			t.Setenv("HERDR_WORKSPACE_ID", "w7")
			p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
			message, err := mailbox.Send(messageProjectDir(manifest), mailbox.Inbox, mailbox.Message{
				ID: "instruction-1", Kind: mailbox.KindInstruction, Program: p.Slug, Item: item.ID,
				From: mailbox.ActorTL, To: mailbox.ActorWorker, Body: "Use the adapter.",
				CreatedAt: "2026-08-26T07:00:00Z",
			})
			if err != nil {
				t.Fatal(err)
			}
			busy := herdr.Agent{
				Status: status, PaneID: "w7:p5", TerminalTitle: "relay:" + manifest.Slug,
				ForegroundCWD: *manifest.Worktree,
			}
			idle := busy
			idle.Status = herdr.StatusIdle
			client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{busy}, {idle}}}
			installWorkerFakes(t, client)

			first, err := runProgramCommand(t, "worker", "notify", p.Slug, item.ID)
			if err != nil {
				t.Fatalf("busy worker notify: %v", err)
			}
			if len(client.prompted) != 0 {
				t.Fatalf("busy worker was prompted: %#v", client.prompted)
			}
			notified, err := mailbox.IsNotified(messageProjectDir(manifest), message.ID)
			if err != nil {
				t.Fatal(err)
			}
			if notified {
				t.Fatal("busy worker inbox was marked notified")
			}
			if !strings.Contains(first, "is "+string(status)) ||
				!strings.Contains(first, "durable inbox remains pending") {
				t.Fatalf("busy output = %q", first)
			}

			if _, err := runProgramCommand(t, "worker", "notify", p.Slug, item.ID); err != nil {
				t.Fatalf("idle worker notify: %v", err)
			}
			if want := []fakePrompt{{"w7:p5", "Check your Relay inbox."}}; !reflect.DeepEqual(client.prompted, want) {
				t.Fatalf("prompted = %#v, want %#v", client.prompted, want)
			}
			notified, err = mailbox.IsNotified(messageProjectDir(manifest), message.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !notified {
				t.Fatal("idle worker inbox was not marked notified")
			}
		})
	}
}

func TestProgramWorkerNotifyReportsMarkerFailureAfterPrompt(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	message, err := mailbox.Send(messageProjectDir(manifest), mailbox.Inbox, mailbox.Message{
		ID: "instruction-1", Kind: mailbox.KindInstruction, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorTL, To: mailbox.ActorWorker, Body: "Use the adapter.",
		CreatedAt: "2026-08-26T07:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	live := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p5", TerminalTitle: "relay:" + manifest.Slug,
		ForegroundCWD: *manifest.Worktree,
	}
	notifiedDir := filepath.Join(messageProjectDir(manifest), "mail", "notified")
	client := &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{live}},
		promptHook: func() error {
			if err := os.Remove(notifiedDir); err != nil {
				return err
			}
			return os.WriteFile(notifiedDir, []byte("not a directory\n"), 0o644)
		},
	}
	installWorkerFakes(t, client)

	_, err = runProgramCommand(t, "worker", "notify", p.Slug, item.ID)
	if err == nil || !strings.Contains(err.Error(), "prompt succeeded") ||
		!strings.Contains(err.Error(), message.ID) || !strings.Contains(err.Error(), "retrying may ring") {
		t.Fatalf("worker notify error = %v", err)
	}
	if want := []fakePrompt{{"w7:p5", "Check your Relay inbox."}}; !reflect.DeepEqual(client.prompted, want) {
		t.Fatalf("prompted = %#v, want %#v", client.prompted, want)
	}
}

func TestProgramWorkerNotifySuppressesRetryAfterUncertainPrompt(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	message, err := mailbox.Send(messageProjectDir(manifest), mailbox.Inbox, mailbox.Message{
		ID: "instruction-1", Kind: mailbox.KindInstruction, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorTL, To: mailbox.ActorWorker, Body: "Use the adapter.",
		CreatedAt: "2026-08-26T07:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	live := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p5", TerminalTitle: "relay:" + manifest.Slug,
		ForegroundCWD: *manifest.Worktree,
	}
	client := &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{live}},
		promptErr:      herdr.ErrPromptDeliveryUncertain,
	}
	installWorkerFakes(t, client)

	_, err = runProgramCommand(t, "worker", "notify", p.Slug, item.ID)
	if err == nil || !strings.Contains(err.Error(), "suppress duplicate input") {
		t.Fatalf("worker notify error = %v", err)
	}
	notified, markerErr := mailbox.IsNotified(messageProjectDir(manifest), message.ID)
	if markerErr != nil {
		t.Fatal(markerErr)
	}
	if !notified {
		t.Fatal("uncertain prompt did not suppress later retries")
	}
}

func TestProgramWorkerFailuresAreActionable(t *testing.T) {
	t.Run("missing Herdr environment", func(t *testing.T) {
		p, item, _ := createWorkerFixture(t, program.ItemDispatched)
		client := &fakeHerdrClient{}
		installWorkerFakes(t, client)
		t.Setenv("HERDR_ENV", "")
		t.Setenv("HERDR_WORKSPACE_ID", "")

		_, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID)
		if err == nil || !strings.Contains(err.Error(), "HERDR_ENV=1") {
			t.Fatalf("error = %v", err)
		}
		if len(client.created) != 0 {
			t.Fatalf("start outside Herdr created tabs: %#v", client.created)
		}
	})

	t.Run("missing Herdr binary", func(t *testing.T) {
		p, item, _ := createWorkerFixture(t, program.ItemDispatched)
		client := &fakeHerdrClient{}
		installWorkerFakes(t, client)
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_WORKSPACE_ID", "w7")
		herdrAvailable = func() bool { return false }

		_, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID)
		if err == nil || !strings.Contains(err.Error(), "herdr binary is not on PATH") ||
			!strings.Contains(err.Error(), "install Herdr") {
			t.Fatalf("error = %v", err)
		}
		if len(client.created) != 0 {
			t.Fatalf("start without Herdr created tabs: %#v", client.created)
		}
	})

	t.Run("unreachable Herdr server", func(t *testing.T) {
		p, item, _ := createWorkerFixture(t, program.ItemDispatched)
		client := &fakeHerdrClient{agentErr: errors.New("connection refused")}
		installWorkerFakes(t, client)
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_WORKSPACE_ID", "w7")

		_, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID)
		if err == nil || !strings.Contains(err.Error(), "running Herdr server") ||
			!strings.Contains(err.Error(), "herdr agent list") {
			t.Fatalf("error = %v", err)
		}
		if len(client.created) != 0 {
			t.Fatalf("start with an unreachable server created tabs: %#v", client.created)
		}
	})

	t.Run("blocked item without child", func(t *testing.T) {
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_WORKSPACE_ID", "w7")
		t.Setenv("HOME", t.TempDir())
		p, err := program.New("governance", "Ship governed changes", t.TempDir(), "copilot", 3)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Transition(program.StatePendingApproval, ""); err != nil {
			t.Fatal(err)
		}
		if err := p.Transition(program.StateActive, "ceo"); err != nil {
			t.Fatal(err)
		}
		item, err := p.AddItem(program.WorkItem{Title: "Waiting item", Priority: program.PriorityP1})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.BlockItem(item.ID, "waiting"); err != nil {
			t.Fatal(err)
		}
		if err := program.Create(p); err != nil {
			t.Fatal(err)
		}
		client := &fakeHerdrClient{}
		installWorkerFakes(t, client)

		_, err = runProgramCommand(t, "worker", "start", p.Slug, item.ID)
		if err == nil || !strings.Contains(err.Error(), "not linked to a child project") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("mismatched title and cwd are not owners", func(t *testing.T) {
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_WORKSPACE_ID", "w7")
		p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
		client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{
			{
				PaneID:        "wrong-title",
				TerminalTitle: "relay:other",
				CWD:           manifest.Repo,
				ForegroundCWD: filepath.Join(manifest.Repo, ".worktrees", "other"),
			},
			{PaneID: "wrong-cwd", TerminalTitle: "relay:" + manifest.Slug, ForegroundCWD: filepath.Dir(*manifest.Worktree)},
		}}}
		installWorkerFakes(t, client)

		_, err := runProgramCommand(t, "worker", "focus", p.Slug, item.ID)
		if err == nil || !strings.Contains(err.Error(), "no live Herdr owner") ||
			!strings.Contains(err.Error(), "relay program worker start") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("start times out waiting for recognition", func(t *testing.T) {
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_WORKSPACE_ID", "w7")
		p, item, _ := createWorkerFixture(t, program.ItemDispatched)
		client := &fakeHerdrClient{
			agentResponses: [][]herdr.Agent{nil},
			tab:            herdr.Tab{ID: "w7:t8", RootPaneID: "w7:p8"},
		}
		installWorkerFakes(t, client)

		_, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID)
		if err == nil || !strings.Contains(err.Error(), "timed out waiting for Herdr") {
			t.Fatalf("error = %v", err)
		}
		if len(client.created) != 1 || len(client.runPane) != 1 || len(client.renamed) != 0 {
			t.Fatalf("timeout calls: created=%d run=%d renamed=%d", len(client.created), len(client.runPane), len(client.renamed))
		}
	})

	t.Run("notify rejects unknown status", func(t *testing.T) {
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_WORKSPACE_ID", "w7")
		p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
		if _, err := mailbox.Send(messageProjectDir(manifest), mailbox.Inbox, mailbox.Message{
			ID: "instruction-1", Kind: mailbox.KindInstruction, Program: p.Slug, Item: item.ID,
			From: mailbox.ActorTL, To: mailbox.ActorWorker, Body: "Use the adapter.",
			CreatedAt: "2026-08-26T07:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
		client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{{
			Status: herdr.StatusUnknown, PaneID: "w7:p6",
			TerminalTitle: "relay:" + manifest.Slug, ForegroundCWD: *manifest.Worktree,
		}}}}
		installWorkerFakes(t, client)

		_, err := runProgramCommand(t, "worker", "notify", p.Slug, item.ID)
		if err == nil || !strings.Contains(err.Error(), `status is "unknown"`) {
			t.Fatalf("error = %v", err)
		}
		if len(client.prompted) != 0 {
			t.Fatalf("unknown worker was prompted: %#v", client.prompted)
		}
	})
}

func TestProgramWorkerStartSerializesConcurrentStarts(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	worker := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug + " - GitHub Copilot",
		ForegroundCWD: *manifest.Worktree,
	}
	client := &lockstepHerdrClient{
		tab:       herdr.Tab{ID: "w7:t9", RootPaneID: "w7:p9"},
		recognize: worker,
		// Tab creation is slow enough that an unserialized second start would
		// reach discovery before Herdr recognizes the first worker.
		createDelay: 50 * time.Millisecond,
	}
	installWorkerFakes(t, client.asFake())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	newHerdrClient = func() herdrRuntimeClient { return client }
	workerSleep = func(time.Duration) {}

	const starts = 4
	errs := make(chan error, starts)
	outputs := make(chan string, starts)
	begin := make(chan struct{})
	for range starts {
		go func() {
			<-begin
			out, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID)
			outputs <- out
			errs <- err
		}()
	}
	close(begin)
	adopted := 0
	for range starts {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent worker start: %v", err)
		}
		if strings.Contains(<-outputs, "Adopted: true") {
			adopted++
		}
	}
	if got := client.createCalls(); got != 1 {
		t.Fatalf("tab creations = %d, want exactly 1", got)
	}
	if got := client.runCalls(); got != 1 {
		t.Fatalf("resume commands = %d, want exactly 1", got)
	}
	if got := client.renameCalls(); got != 1 {
		t.Fatalf("renames = %d, want exactly 1", got)
	}
	if adopted != starts-1 {
		t.Fatalf("adopted starts = %d, want %d", adopted, starts-1)
	}
}

func TestWorkerStartLockPathIsPerChildProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := filepath.Join(program.RelayDir(), "run", "workers", "child", "start.lock")
	if got := workerStartLockPath("child"); got != want {
		t.Fatalf("workerStartLockPath = %q, want %q", got, want)
	}
	lock, err := acquireWorkerStartLock("child", "relay program worker start")
	if err != nil {
		t.Fatal(err)
	}
	held, err := patrollock.IsHeld(want)
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("worker start lock is not observable while held")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestProgramWorkerStartReloadsAfterWorkerThenProjectLifecycleLock(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	client := &fakeHerdrClient{}
	installWorkerFakes(t, client)

	lifecycleLock, err := patrollock.Acquire(projectLifecycleLockPath(manifest.Slug))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() {
		_, startErr := startProgramWorker(
			p.Slug, item.ID, "relay program worker start",
		)
		started <- startErr
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-started:
			t.Fatalf("worker start returned before waiting for the lifecycle lock: %v", err)
		default:
		}
		held, inspectErr := patrollock.IsHeld(workerStartLockPath(manifest.Slug))
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker start did not acquire its worker lock before the project lifecycle lock")
		}
		time.Sleep(time.Millisecond)
	}

	activeDir := filepath.Join(project.ActiveDir(), manifest.Slug)
	archivedDir := filepath.Join(project.ArchivedDir(), manifest.Slug)
	if err := os.MkdirAll(project.ArchivedDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(activeDir, archivedDir); err != nil {
		t.Fatal(err)
	}
	if err := lifecycleLock.Release(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-started:
		if err == nil || !strings.Contains(err.Error(), "is not active") {
			t.Fatalf("worker start error = %v, want archived child rejection", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker start did not continue after the lifecycle lock was released")
	}
	if len(client.created) != 0 || len(client.runPane) != 0 || len(client.renamed) != 0 {
		t.Fatalf(
			"stale worker start mutated Herdr: created=%v run=%v renamed=%v",
			client.created, client.runPane, client.renamed,
		)
	}
}

func TestProgramWorkerStartRejectsDurableRedirectWhileWaitingForLifecycleLock(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	client := &fakeHerdrClient{}
	installWorkerFakes(t, client)

	lifecycleLock, err := patrollock.Acquire(projectLifecycleLockPath(manifest.Slug))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() {
		_, startErr := startProgramWorker(
			p.Slug, item.ID, "relay program worker start",
		)
		started <- startErr
	}()
	waitForWorkerStartLock(t, manifest.Slug, started)

	redirectBranch := "test/redirected-worker"
	redirectWorktree := addArchiveWorktree(t, manifest.Repo, "redirected-worker", redirectBranch)
	programPath := program.ManifestPath(program.ActiveDir(), p.Slug)
	redirectedProgram, err := program.Load(programPath)
	if err != nil {
		t.Fatal(err)
	}
	for index := range redirectedProgram.Items {
		if redirectedProgram.Items[index].ID == item.ID {
			redirectedProgram.Items[index].ProjectBranch = redirectBranch
			redirectedProgram.Items[index].ProjectWorktree = redirectWorktree
		}
	}
	if err := program.Save(programPath, redirectedProgram); err != nil {
		t.Fatal(err)
	}
	redirectedManifest, err := project.Load(
		project.ManifestPath(project.ActiveDir(), manifest.Slug),
	)
	if err != nil {
		t.Fatal(err)
	}
	redirectedManifest.Branch = redirectBranch
	redirectedManifest.Worktree = &redirectWorktree
	if err := project.Save(
		project.ManifestPath(project.ActiveDir(), manifest.Slug), redirectedManifest,
	); err != nil {
		t.Fatal(err)
	}
	if err := lifecycleLock.Release(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-started:
		if err == nil || !strings.Contains(err.Error(), "changed while waiting") ||
			!strings.Contains(err.Error(), "repository/branch/worktree changed") {
			t.Fatalf("worker start error = %v, want durable redirect rejection", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker start did not continue after the lifecycle lock was released")
	}
	if len(client.created) != 0 || len(client.runPane) != 0 || len(client.renamed) != 0 {
		t.Fatalf(
			"redirected worker start mutated Herdr: created=%v run=%v renamed=%v",
			client.created, client.runPane, client.renamed,
		)
	}
	if !pathExists(*manifest.Worktree) || !gitx.BranchExists(manifest.Repo, manifest.Branch) ||
		!pathExists(redirectWorktree) || !gitx.BranchExists(manifest.Repo, redirectBranch) {
		t.Fatal("worker start changed resources after a durable redirect")
	}
}

func TestProgramWorkerStartRejectsRegisteredWorktreeRedirectWhileWaiting(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	client := &fakeHerdrClient{}
	installWorkerFakes(t, client)

	lifecycleLock, err := patrollock.Acquire(projectLifecycleLockPath(manifest.Slug))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() {
		_, startErr := startProgramWorker(
			p.Slug, item.ID, "relay program worker start",
		)
		started <- startErr
	}()
	waitForWorkerStartLock(t, manifest.Slug, started)

	runArchiveGit(t, manifest.Repo, "worktree", "remove", "--force", *manifest.Worktree)
	redirectBranch := "test/registered-redirect"
	runArchiveGit(
		t, manifest.Repo, "worktree", "add", "-b", redirectBranch, *manifest.Worktree, "HEAD",
	)
	if err := lifecycleLock.Release(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-started:
		if err == nil || !strings.Contains(err.Error(), "resource identity is not safe") ||
			!strings.Contains(err.Error(), "attached to") {
			t.Fatalf("worker start error = %v, want registered worktree redirect rejection", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker start did not continue after the lifecycle lock was released")
	}
	if len(client.created) != 0 || len(client.runPane) != 0 || len(client.renamed) != 0 {
		t.Fatalf(
			"redirected worker start mutated Herdr: created=%v run=%v renamed=%v",
			client.created, client.runPane, client.renamed,
		)
	}
	if !pathExists(*manifest.Worktree) || !gitx.BranchExists(manifest.Repo, redirectBranch) {
		t.Fatal("worker start changed redirected registered worktree resources")
	}
}

func waitForWorkerStartLock(t *testing.T, childSlug string, started <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-started:
			t.Fatalf("worker start returned before waiting for the lifecycle lock: %v", err)
		default:
		}
		held, err := patrollock.IsHeld(workerStartLockPath(childSlug))
		if err != nil {
			t.Fatal(err)
		}
		if held {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("worker start did not acquire its worker lock before the project lifecycle lock")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWorkerPollBacksOffToBoundedCalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	worktree := t.TempDir()
	manifest := project.Manifest{Slug: "child", Repo: t.TempDir(), Worktree: &worktree}
	client := &fakeHerdrClient{}
	installWorkerFakes(t, client)
	delays := []time.Duration{}
	now := time.Unix(500, 0)
	workerNow = func() time.Time { return now }
	workerSleep = func(delay time.Duration) {
		delays = append(delays, delay)
		now = now.Add(delay)
	}

	if _, err := waitForProgramWorker(client, manifest); err == nil ||
		!strings.Contains(err.Error(), "timed out waiting for Herdr") {
		t.Fatalf("waitForProgramWorker error = %v", err)
	}
	if client.agentCalls > 34 {
		t.Fatalf("agent list calls = %d, want a bounded backoff below fixed 100ms polling", client.agentCalls)
	}
	want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, time.Second}
	if len(delays) < len(want) {
		t.Fatalf("delays = %v", delays)
	}
	for index, expected := range want {
		if delays[index] != expected {
			t.Fatalf("delays = %v, want a 250ms to 1s backoff", delays)
		}
	}
	for _, delay := range delays {
		if delay > time.Second {
			t.Fatalf("delay %s exceeds the 1s cap", delay)
		}
	}
}

// lockstepHerdrClient models Herdr recognizing a worker only after the tab that
// created it finished, so concurrent starts must serialize to stay correct.
type lockstepHerdrClient struct {
	mu          sync.Mutex
	tab         herdr.Tab
	recognize   herdr.Agent
	live        bool
	creates     int
	runs        int
	renames     int
	createDelay time.Duration
}

func (c *lockstepHerdrClient) asFake() *fakeHerdrClient { return &fakeHerdrClient{} }

func (c *lockstepHerdrClient) CloseTab(string) error { return nil }

func (c *lockstepHerdrClient) ClosePane(string) error { return nil }

func (c *lockstepHerdrClient) Tabs(string) ([]herdr.TabInfo, error) { return nil, nil }

func (c *lockstepHerdrClient) Panes(string) ([]herdr.Pane, error) { return nil, nil }

func (c *lockstepHerdrClient) Agents() ([]herdr.Agent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.live {
		return nil, nil
	}
	return []herdr.Agent{c.recognize}, nil
}

func (c *lockstepHerdrClient) CreateTab(string, string, string) (herdr.Tab, error) {
	c.mu.Lock()
	c.creates++
	delay := c.createDelay
	c.mu.Unlock()
	time.Sleep(delay)
	return c.tab, nil
}

func (c *lockstepHerdrClient) RunPane(string, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runs++
	c.live = true
	return nil
}

func (c *lockstepHerdrClient) RenameAgent(string, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.renames++
	return nil
}

func (c *lockstepHerdrClient) PromptAgent(string, string) error { return nil }

func (c *lockstepHerdrClient) ExitAgent(herdr.SessionIdentity) (herdr.ExitResult, error) {
	return herdr.ExitResult{}, errors.New("lockstep client does not exit agents")
}

func (c *lockstepHerdrClient) FocusAgent(string) error { return nil }

func (c *lockstepHerdrClient) ShowNotification(string, string) error { return nil }

func (c *lockstepHerdrClient) createCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.creates
}

func (c *lockstepHerdrClient) runCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs
}

func (c *lockstepHerdrClient) renameCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.renames
}

func TestProgramWorkerStartRejectsDuplicateWorktreeOwners(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	first := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p1", TabID: "w7:t1", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug, ForegroundCWD: *manifest.Worktree,
	}
	second := first
	second.PaneID, second.TabID = "w7:p2", "w7:t2"
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{first, second}}}
	installWorkerFakes(t, client)

	_, err := runProgramCommand(t, "worker", "start", p.Slug, item.ID, "--json")
	if err == nil {
		t.Fatal("worker start accepted two worktree-matching owners")
	}
	for _, want := range []string{"2 live workers", "w7:p1", "w7:p2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want %q", err, want)
		}
	}
	if len(client.created) != 0 || len(client.runPane) != 0 {
		t.Fatalf("ambiguous start mutated Herdr: created=%v run=%v", client.created, client.runPane)
	}
}

func TestProgramWorkerEnsureWaitsForAnAdoptedWorkerTitle(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, _, manifest := createWorkerFixture(t, program.ItemDispatched)
	p.Items[0].Status = program.ItemInReview
	p.Items[0].PRRef = "#42"
	p.Items[0].InReviewAt = p.Items[0].UpdatedAt
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	untitled := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		ForegroundCWD: *manifest.Worktree,
	}
	titled := untitled
	titled.TerminalTitle = "relay:" + manifest.Slug
	client := &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{untitled}, {untitled}, {titled}, {titled}},
		tab: herdr.Tab{
			ID: "watch-tab", RootPaneID: "watch-pane", WorkspaceID: "w7", TerminalID: "watch-term",
		},
	}
	installWorkerFakes(t, client)
	installPRWatchFakes(t, client)
	prWatchRequireManaged = func(string) error { return nil }
	launch := &prWatchLaunch{
		staleErr: os.ErrNotExist,
		running: prwatch.State{
			PID: 77, Status: prwatch.StatusRunning, Mode: prwatch.ModeManaged,
			OwnerSlug: manifest.Slug, WorkspaceID: "w7", TabID: "watch-tab",
			PaneID: "watch-pane", TerminalID: "watch-term",
		},
	}
	launch.install(t, client)

	out, err := runProgramCommand(t, "worker", "ensure", p.Slug, "--json")
	if err != nil {
		t.Fatalf("worker ensure: %v", err)
	}
	var got programWorkerEnsureOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 0 || got.Entries[0].Watcher == nil {
		t.Fatalf("ensure output = %+v, want the adopted owner title waited out", got)
	}
}

func TestProgramWorkerEnsureTimesOutWhenAdoptedWorkerNeverGetsExactTitle(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p.Items[0].Status = program.ItemInReview
	p.Items[0].PRRef = "#42"
	p.Items[0].InReviewAt = p.Items[0].UpdatedAt
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	untitled := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		ForegroundCWD: *manifest.Worktree,
	}
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{untitled}}}
	installWorkerFakes(t, client)
	installPRWatchFakes(t, client)

	out, err := runProgramCommand(t, "worker", "ensure", p.Slug, "--json")
	if err != nil {
		t.Fatalf("worker ensure: %v", err)
	}
	var got programWorkerEnsureOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 1 ||
		!strings.Contains(got.Warnings[0].Error, "timed out waiting for Herdr to report the session titled") {
		t.Fatalf("warnings = %+v, want an exact-title timeout", got.Warnings)
	}
	if got.Entries[0].Worker == nil || !got.Entries[0].Worker.Adopted ||
		got.Entries[0].Watcher != nil || len(client.created) != 0 {
		t.Fatalf("ensure output = %+v, want adopted worker and no watcher launch", got)
	}
	if got.Warnings[0].Item != item.ID {
		t.Fatalf("warning item = %q, want %q", got.Warnings[0].Item, item.ID)
	}
}

func TestProgramWorkerEnsureRejectsDuplicateWorktreeOwnersWithAndWithoutPR(t *testing.T) {
	for _, withPR := range []bool{false, true} {
		t.Run(fmt.Sprintf("pr=%t", withPR), func(t *testing.T) {
			t.Setenv("HERDR_ENV", "1")
			t.Setenv("HERDR_WORKSPACE_ID", "w7")
			p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
			if withPR {
				p.Items[0].Status = program.ItemInReview
				p.Items[0].PRRef = "#42"
				p.Items[0].InReviewAt = p.Items[0].UpdatedAt
				if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
					t.Fatal(err)
				}
			}
			first := herdr.Agent{
				Status: herdr.StatusIdle, PaneID: "w7:p1", TabID: "w7:t1", WorkspaceID: "w7",
				TerminalTitle: "relay:" + manifest.Slug, ForegroundCWD: *manifest.Worktree,
			}
			second := first
			second.PaneID, second.TabID = "w7:p2", "w7:t2"
			client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{first, second}}}
			installWorkerFakes(t, client)
			installPRWatchFakes(t, client)

			out, err := runProgramCommand(t, "worker", "ensure", p.Slug, "--json")
			if err != nil {
				t.Fatalf("worker ensure: %v", err)
			}
			var got programWorkerEnsureOutput
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Warnings) != 1 {
				t.Fatalf("warnings = %+v, want one ambiguity warning", got.Warnings)
			}
			for _, want := range []string{"2 live workers", "w7:p1", "w7:p2"} {
				if !strings.Contains(got.Warnings[0].Error, want) {
					t.Errorf("warning = %q, want %q", got.Warnings[0].Error, want)
				}
			}
			if got.Entries[0].Live || got.Entries[0].Watcher != nil || len(client.created) != 0 {
				t.Fatalf("ensure mutated ambiguous runtime: %+v created=%v", got, client.created)
			}
			if got.Warnings[0].Item != item.ID {
				t.Fatalf("warning item = %q, want %q", got.Warnings[0].Item, item.ID)
			}
		})
	}
}

func TestProgramWorkerEnsureSurfacesWatcherInAnotherWorkspace(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p.Items[0].Status = program.ItemInReview
	p.Items[0].PRRef = "#42"
	p.Items[0].InReviewAt = p.Items[0].UpdatedAt
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	worker := herdr.Agent{
		Status: herdr.StatusIdle, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug, ForegroundCWD: *manifest.Worktree,
	}
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{worker}}}
	installWorkerFakes(t, client)
	installPRWatchFakes(t, client)
	prWatchIsRunning = func(string) (bool, error) { return true, nil }
	prWatchReadState = func(string) (prwatch.State, error) {
		return prwatch.State{
			Project: manifest.Slug, PID: 77, Status: prwatch.StatusRunning,
			Mode: prwatch.ModeManaged, OwnerSlug: manifest.Slug,
			WorkspaceID: "w-other", TabID: "watch-other", PaneID: "pane-other",
		}, nil
	}

	out, err := runProgramCommand(t, "worker", "ensure", p.Slug, "--json")
	if err != nil {
		t.Fatalf("worker ensure: %v", err)
	}
	var got programWorkerEnsureOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Watcher == nil ||
		!got.Entries[0].Watcher.Incomplete {
		t.Fatalf("ensure output = %+v, want an incomplete watcher entry", got)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Item != item.ID ||
		!strings.Contains(got.Warnings[0].Error, "w-other") ||
		!strings.Contains(got.Warnings[0].Error, "w7") {
		t.Fatalf("warnings = %+v, want the workspace mismatch surfaced", got.Warnings)
	}
	if len(client.created) != 0 || len(client.runPane) != 0 {
		t.Fatalf("ensure launched a duplicate watcher: created=%v run=%v", client.created, client.runPane)
	}
}
