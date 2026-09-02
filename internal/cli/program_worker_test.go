package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

type fakeHerdrClient struct {
	agentResponses [][]herdr.Agent
	// agentsHook answers every agent list from live test state, so a test can
	// model an agent that only appears once Relay has created its project.
	agentsHook      func() ([]herdr.Agent, error)
	agentCalls      int
	agentErr        error
	created         []fakeCreatedTab
	runPane         []fakePaneCommand
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
	closedTabs      []string
	closedPanes     []string
	closeErr        error
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

func (f *fakeHerdrClient) CreateTab(workspace, cwd, label string) (herdr.Tab, error) {
	f.created = append(f.created, fakeCreatedTab{workspace: workspace, cwd: cwd, label: label})
	return f.tab, nil
}

func (f *fakeHerdrClient) CloseTab(tabID string) error {
	f.closedTabs = append(f.closedTabs, tabID)
	return f.closeErr
}

func (f *fakeHerdrClient) ClosePane(paneID string) error {
	f.closedPanes = append(f.closedPanes, paneID)
	return f.closeErr
}

func (f *fakeHerdrClient) RunPane(pane, command string) error {
	f.runPane = append(f.runPane, fakePaneCommand{pane: pane, command: command})
	return nil
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
	repo := t.TempDir()
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
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	loadedItem, _ := p.Item(item.ID)
	worktree := filepath.Join(repo, ".worktrees", childSlug)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := project.Manifest{
		Slug: childSlug, Title: item.Title, Repo: repo, Agent: "copilot",
		Worktree: &worktree, Program: p.Slug, ProgramItem: item.ID, Phase: "implement",
	}
	manifestPath := project.ManifestPath(project.ActiveDir(), childSlug)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	return p, loadedItem, manifest
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
	lock, err := acquireWorkerStartLock("child")
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
