package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/prwatch"
	"github.com/spf13/cobra"
)

func runPRCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := newCmdPR()
	command.SetArgs(args)
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	err := command.Execute()
	return out.String(), err
}

// installPRWatchFakes points every watcher seam at a test double and restores
// the real ones afterwards.
func installPRWatchFakes(t *testing.T, client *fakeHerdrClient) {
	t.Helper()
	previous := struct {
		herdrClient  func() herdrRuntimeClient
		patrolClient func(context.Context) herdrRuntimeClient
		available    func() bool
		running      func(string) (bool, error)
		read         func(string) (prwatch.State, error)
		readLocked   func(string) (prwatch.State, error)
		prepare      func(string, time.Time) (prwatch.State, string, string, error)
		update       func(string, func(prwatch.State) (prwatch.State, error)) (prwatch.State, error)
		run          func(context.Context, string, prwatch.Options) error
		tick         func(context.Context, string, prwatch.Options) (prwatch.Digest, error)
		locate       func(string) (prwatch.Target, error)
		owner        func([]herdr.Agent, prwatch.Mode, string, string) (herdr.Agent, error)
		managed      func(string) error
		now          func() time.Time
		sleep        func(time.Duration)
		signal       func(int, os.Signal) error
	}{
		newHerdrClient, newPatrolHerdrClient, herdrAvailable, prWatchIsRunning, prWatchReadState,
		prWatchReadStateLocked, prWatchPrepareState, prWatchUpdateState,
		prWatchRunLoop, prWatchTickOnce, prWatchLocate, prWatchRequireOwner, prWatchRequireManaged,
		prWatchNow, prWatchSleep, prWatchSignal,
	}
	// The runtime record is one record: reading it under the lock reads the
	// same double, and updating it applies the mutation to it.
	prWatchReadStateLocked = func(slug string) (prwatch.State, error) { return prWatchReadState(slug) }
	prWatchPrepareState = func(slug string, _ time.Time) (prwatch.State, string, string, error) {
		state, err := prWatchReadState(slug)
		if errors.Is(err, os.ErrNotExist) {
			return prwatch.State{}, "", "", nil
		}
		return state, "", "", err
	}
	prWatchUpdateState = func(
		slug string, mutate func(prwatch.State) (prwatch.State, error),
	) (prwatch.State, error) {
		current, _ := prWatchReadState(slug)
		return mutate(current)
	}
	newHerdrClient = func() herdrRuntimeClient { return client }
	newPatrolHerdrClient = func(context.Context) herdrRuntimeClient { return client }
	herdrAvailable = func() bool { return true }
	now := time.Unix(100, 0)
	prWatchNow = func() time.Time { return now }
	prWatchSleep = func(delay time.Duration) { now = now.Add(delay) }
	prWatchLocate = func(slug string) (prwatch.Target, error) {
		return prwatch.Target{Slug: slug, Dir: "/repo/" + slug, PRNumber: 42}, nil
	}
	t.Cleanup(func() {
		newHerdrClient = previous.herdrClient
		newPatrolHerdrClient = previous.patrolClient
		herdrAvailable = previous.available
		prWatchIsRunning = previous.running
		prWatchReadState = previous.read
		prWatchReadStateLocked = previous.readLocked
		prWatchPrepareState = previous.prepare
		prWatchUpdateState = previous.update
		prWatchRunLoop = previous.run
		prWatchTickOnce = previous.tick
		prWatchLocate = previous.locate
		prWatchRequireOwner = previous.owner
		prWatchRequireManaged = previous.managed
		prWatchNow = previous.now
		prWatchSleep = previous.sleep
		prWatchSignal = previous.signal
	})
}

// prWatchLaunch models the watcher process a start launches: nothing is running
// until Relay runs the pane command, and the recorded state reports the stale
// record before that and a running watcher after it.
type prWatchLaunch struct {
	mu       sync.Mutex
	launched bool
	stale    prwatch.State
	staleErr error
	running  prwatch.State
}

// install points the watcher liveness and state seams at this launch model.
// It must be called after installPRWatchFakes, which restores the real seams.
func (l *prWatchLaunch) install(t *testing.T, client *fakeHerdrClient) {
	t.Helper()
	client.runPaneHook = func(_, command string) error {
		if !strings.HasPrefix(command, "relay pr watch run ") {
			return nil
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		l.launched = true
		return nil
	}
	prWatchIsRunning = func(string) (bool, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.launched, nil
	}
	prWatchReadState = func(slug string) (prwatch.State, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if !l.launched {
			if l.staleErr != nil {
				return prwatch.State{}, l.staleErr
			}
			state := l.stale
			state.Project = slug
			return state, nil
		}
		state := l.running
		state.Project = slug
		return state, nil
	}
}

func TestPRWatchStartCreatesAWatcherTabAndRunsTheHiddenProcess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &fakeHerdrClient{
		tab:            herdr.Tab{ID: "tab-1", RootPaneID: "pane-1", TerminalID: "term-1"},
		agentResponses: [][]herdr.Agent{{{TerminalTitle: "relay:demo", PaneID: "pane-owner"}}},
	}
	installPRWatchFakes(t, client)
	launch := &prWatchLaunch{
		staleErr: os.ErrNotExist,
		running: prwatch.State{
			Schema: prwatch.SchemaVersion, Version: 1, PID: 77,
			Status: prwatch.StatusRunning, Mode: prwatch.ModeStandalone, OwnerSlug: "demo",
		},
	}
	launch.install(t, client)

	out, err := runPRCommand(t, "watch", "start", "demo", "--json")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var got prWatchStartOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Project != "demo" || !got.Running || got.Adopted {
		t.Fatalf("start output = %+v", got)
	}
	if want := []fakeCreatedTab{{
		workspace: "workspace-1", cwd: "/repo/demo", label: "relay-pr-watch:demo",
	}}; !reflect.DeepEqual(client.created, want) {
		t.Fatalf("created tabs = %#v, want %#v", client.created, want)
	}
	if want := []fakePaneCommand{{
		pane: "pane-1",
		command: "relay pr watch run 'demo' --mode 'standalone' --owner 'demo' " +
			"--workspace 'workspace-1' --tab 'tab-1' --pane 'pane-1' --terminal 'term-1'",
	}}; !reflect.DeepEqual(client.runPane, want) {
		t.Fatalf("pane commands = %#v, want %#v", client.runPane, want)
	}
	if got.OwnerPane != "pane-owner" || got.TabID != "tab-1" {
		t.Errorf("start output = %+v, want the validated owner pane and the watcher tab", got)
	}
}

// installPRWatchStaleTabs puts a stale watcher tab and one stale duplicate in
// the current workspace, with a runtime record that prefers the first.
func installPRWatchStaleTabs(t *testing.T, client *fakeHerdrClient) *prWatchLaunch {
	t.Helper()
	client.tabs = []herdr.TabInfo{
		{ID: "tab-stale", WorkspaceID: "workspace-1", Label: "relay-pr-watch:demo", PaneCount: 1},
		{ID: "tab-duplicate", WorkspaceID: "workspace-1", Label: "relay-pr-watch:demo", PaneCount: 1},
	}
	client.panes = []herdr.Pane{
		{ID: "pane-stale", TabID: "tab-stale", WorkspaceID: "workspace-1", TerminalID: "term-stale"},
		{ID: "pane-duplicate", TabID: "tab-duplicate", WorkspaceID: "workspace-1", TerminalID: "term-duplicate"},
	}
	launch := &prWatchLaunch{
		stale: prwatch.State{
			PID: 77, Status: prwatch.StatusComplete, Mode: prwatch.ModeStandalone,
			OwnerSlug: "demo", WorkspaceID: "workspace-1", TabID: "tab-stale",
			PaneID: "pane-stale", TerminalID: "term-stale",
		},
		running: prwatch.State{
			PID: 77, Status: prwatch.StatusRunning, Mode: prwatch.ModeStandalone,
			OwnerSlug: "demo", WorkspaceID: "workspace-1", TabID: "tab-stale",
			PaneID: "pane-stale", TerminalID: "term-stale",
		},
	}
	launch.install(t, client)
	return launch
}

func TestPRWatchStartReusesOnlyTheExactRecordedWatcherTab(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{{TerminalTitle: "relay:demo", PaneID: "pane-owner"}}},
	}

	installPRWatchFakes(t, client)
	installPRWatchStaleTabs(t, client)

	out, err := runPRCommand(t, "watch", "start", "demo", "--json")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var got prWatchStartOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(client.created) != 0 {
		t.Fatalf("start created a replacement tab: %#v", client.created)
	}
	if len(client.closedTabs) != 0 {
		t.Fatalf("start closed an unrecorded label-only tab: %#v", client.closedTabs)
	}
	if want := []fakePaneCommand{{
		pane: "pane-stale",
		command: "relay pr watch run 'demo' --mode 'standalone' --owner 'demo' " +
			"--workspace 'workspace-1' --tab 'tab-stale' --pane 'pane-stale' --terminal 'term-stale'",
	}}; !reflect.DeepEqual(client.runPane, want) {
		t.Fatalf("pane commands = %#v, want %#v", client.runPane, want)
	}
	if got.TabID != "tab-stale" || !got.TabReused || len(got.ClosedTabIDs) != 0 {
		t.Fatalf("start output = %+v, want an auditable reuse of tab-stale", got)
	}
	if !strings.Contains(got.Warning, "preserved unrecorded Herdr tab tab-duplicate") {
		t.Fatalf("warning = %q, want the label-only duplicate preserved", got.Warning)
	}
}

// A tab carrying the watcher label is only stale when Herdr proves it: nothing
// live in it, exactly one pane Relay can run in, and the current workspace.
// Anything else is a tab a person split, repurposed, or is reading, so start
// leaves it alone, says so, and creates its own tab instead.
func TestPRWatchStartPreservesWatcherTabsItCannotProveStale(t *testing.T) {
	for _, test := range []struct {
		name        string
		tab         herdr.TabInfo
		panes       []herdr.Pane
		wantWarning string
	}{
		{
			name: "active agent",
			tab: herdr.TabInfo{
				ID: "tab-live", WorkspaceID: "workspace-1",
				Label: "relay-pr-watch:demo", PaneCount: 1, Status: herdr.StatusWorking,
			},
			panes:       []herdr.Pane{{ID: "pane-live", TabID: "tab-live", WorkspaceID: "workspace-1"}},
			wantWarning: `its agent status is "working"`,
		},
		{
			name: "multiple panes",
			tab: herdr.TabInfo{
				ID: "tab-split", WorkspaceID: "workspace-1",
				Label: "relay-pr-watch:demo", PaneCount: 2,
			},
			panes: []herdr.Pane{
				{ID: "pane-split-1", TabID: "tab-split", WorkspaceID: "workspace-1"},
				{ID: "pane-split-2", TabID: "tab-split", WorkspaceID: "workspace-1"},
			},
			wantWarning: "it holds 2 panes",
		},
		{
			name: "missing pane",
			tab: herdr.TabInfo{
				ID: "tab-empty", WorkspaceID: "workspace-1",
				Label: "relay-pr-watch:demo", PaneCount: 1,
			},
			panes:       nil,
			wantWarning: "Herdr listed 0 panes for it",
		},
		{
			name: "another workspace",
			tab: herdr.TabInfo{
				ID: "tab-elsewhere", WorkspaceID: "workspace-2",
				Label: "relay-pr-watch:demo", PaneCount: 1,
			},
			panes:       []herdr.Pane{{ID: "pane-elsewhere", TabID: "tab-elsewhere", WorkspaceID: "workspace-2"}},
			wantWarning: "it belongs to workspace workspace-2",
		},
		{
			name: "unattributed workspace",
			tab: herdr.TabInfo{
				ID: "tab-unknown", Label: "relay-pr-watch:demo", PaneCount: 1,
			},
			panes:       []herdr.Pane{{ID: "pane-unknown", TabID: "tab-unknown", WorkspaceID: "workspace-1"}},
			wantWarning: "Herdr did not report which workspace owns it",
		},
		{
			name: "occupied pane",
			tab: herdr.TabInfo{
				ID: "tab-busy-pane", WorkspaceID: "workspace-1",
				Label: "relay-pr-watch:demo", PaneCount: 1,
			},
			panes: []herdr.Pane{{
				ID: "pane-busy", TabID: "tab-busy-pane",
				WorkspaceID: "workspace-1", Status: herdr.StatusIdle,
			}},
			wantWarning: `its pane pane-busy has agent status "idle"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("HERDR_ENV", "1")
			t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
			client := &fakeHerdrClient{
				tab:            herdr.Tab{ID: "tab-fresh", RootPaneID: "pane-fresh", TerminalID: "term-fresh"},
				tabs:           []herdr.TabInfo{test.tab},
				panes:          test.panes,
				agentResponses: [][]herdr.Agent{{{TerminalTitle: "relay:demo", PaneID: "pane-owner"}}},
			}
			installPRWatchFakes(t, client)
			launch := &prWatchLaunch{
				staleErr: os.ErrNotExist,
				running: prwatch.State{
					PID: 77, Status: prwatch.StatusRunning,
					Mode: prwatch.ModeStandalone, OwnerSlug: "demo",
				},
			}
			launch.install(t, client)

			out, err := runPRCommand(t, "watch", "start", "demo", "--json")
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			var got prWatchStartOutput
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("decode %q: %v", out, err)
			}
			if len(client.closedTabs) != 0 {
				t.Fatalf("start closed tabs it could not prove stale: %#v", client.closedTabs)
			}
			if want := []fakeCreatedTab{{
				workspace: "workspace-1", cwd: "/repo/demo", label: "relay-pr-watch:demo",
			}}; !reflect.DeepEqual(client.created, want) {
				t.Fatalf("created tabs = %#v, want %#v", client.created, want)
			}
			if got.TabID != "tab-fresh" || got.TabReused {
				t.Fatalf("start output = %+v, want a freshly created tab", got)
			}
			if !strings.Contains(got.Warning, "a label does not prove Relay owns the tab") ||
				!strings.Contains(got.Warning, test.tab.ID) {
				t.Fatalf("warning = %q, want it to preserve label-only tab %s", got.Warning, test.tab.ID)
			}
		})
	}
}

func TestPRWatchStartQuarantinesCorruptStateBeforeLaunching(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &fakeHerdrClient{
		tab: herdr.Tab{
			ID: "tab-fresh", RootPaneID: "pane-fresh", TerminalID: "term-fresh",
		},
		tabs: []herdr.TabInfo{
			{ID: "tab-stale", WorkspaceID: "workspace-1", Label: "relay-pr-watch:demo", PaneCount: 1},
		},
		panes: []herdr.Pane{{
			ID: "pane-stale", TabID: "tab-stale", WorkspaceID: "workspace-1", TerminalID: "term-stale",
		}},
		agentResponses: [][]herdr.Agent{{{TerminalTitle: "relay:demo", PaneID: "pane-owner"}}},
	}
	installPRWatchFakes(t, client)
	if err := os.MkdirAll(prwatch.RuntimeDir("demo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prwatch.StatePath("demo"), []byte(`{"pid":`), 0o600); err != nil {
		t.Fatal(err)
	}
	prWatchPrepareState = prwatch.PrepareStateForStart
	launch := &prWatchLaunch{
		staleErr: os.ErrNotExist,
		running: prwatch.State{
			PID: 77, Status: prwatch.StatusRunning, Mode: prwatch.ModeStandalone, OwnerSlug: "demo",
			WorkspaceID: "workspace-1", TabID: "tab-fresh", PaneID: "pane-fresh", TerminalID: "term-fresh",
		},
	}
	launch.install(t, client)

	out, err := runPRCommand(t, "watch", "start", "demo", "--json")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var got prWatchStartOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if !got.Running || got.TabID != "tab-fresh" || got.TabReused {
		t.Fatalf("start output = %+v, want a fresh watcher after quarantine", got)
	}
	for _, want := range []string{"was corrupt", "unexpected end of JSON input", "watch.json.corrupt-"} {
		if !strings.Contains(got.Warning, want) {
			t.Errorf("warning = %q, want it to name %q", got.Warning, want)
		}
	}
	matches, err := filepath.Glob(filepath.Join(prwatch.RuntimeDir("demo"), "watch.json.corrupt-*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("quarantined records = %v, err = %v", matches, err)
	}
}

// Two program resumes can start the same watcher at once. Without a start lock
// each would inventory the workspace, decide the same tab is stale, and launch
// its own process; the loser must adopt the winner instead.
func TestPRWatchStartSerializesConcurrentStarts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &concurrentHerdrClient{
		fakeHerdrClient: fakeHerdrClient{
			tab:            herdr.Tab{ID: "tab-fresh", RootPaneID: "pane-fresh", TerminalID: "term-fresh"},
			agentResponses: [][]herdr.Agent{{{TerminalTitle: "relay:demo", PaneID: "pane-owner"}}},
		},
		// Listing tabs is slow enough that an unserialized second start would
		// inventory the workspace before the first has launched anything.
		listDelay: 50 * time.Millisecond,
	}

	installPRWatchFakes(t, &client.fakeHerdrClient)
	newHerdrClient = func() herdrRuntimeClient { return client }
	installPRWatchStaleTabs(t, &client.fakeHerdrClient)
	prWatchSleep = func(time.Duration) {}

	const starts = 4
	results := make(chan prWatchStartOutput, starts)
	errs := make(chan error, starts)
	begin := make(chan struct{})
	for range starts {
		go func() {
			<-begin
			result, err := startPRWatcher("demo", &prWatchModeFlags{
				mode: string(prwatch.ModeStandalone),
			}, "relay pr watch start")
			results <- result
			errs <- err
		}()
	}
	close(begin)
	adopted := 0
	for range starts {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent pr watch start: %v", err)
		}
		if result := <-results; result.Adopted {
			adopted++
		}
	}
	if adopted != starts-1 {
		t.Fatalf("adopted starts = %d, want %d", adopted, starts-1)
	}
	if got := client.calls(&client.runs); got != 1 {
		t.Fatalf("pane launches = %d, want exactly 1", got)
	}
	if got := client.calls(&client.closes); got != 0 {
		t.Fatalf("tab closures = %d, want label-only duplicates preserved", got)
	}
	if got := client.calls(&client.creates); got != 0 {
		t.Fatalf("tab creations = %d, want the stale tab reused instead", got)
	}
}

// concurrentHerdrClient counts the runtime mutations concurrent starts perform
// and makes the workspace inventory slow enough for a race to be visible.
type concurrentHerdrClient struct {
	fakeHerdrClient
	mu        sync.Mutex
	listDelay time.Duration
	creates   int
	runs      int
	closes    int
}

func (c *concurrentHerdrClient) Tabs(workspaceID string) ([]herdr.TabInfo, error) {
	time.Sleep(c.listDelay)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fakeHerdrClient.Tabs(workspaceID)
}

func (c *concurrentHerdrClient) Panes(workspaceID string) ([]herdr.Pane, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fakeHerdrClient.Panes(workspaceID)
}

func (c *concurrentHerdrClient) CreateTab(workspaceID, cwd, label string) (herdr.Tab, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.creates++
	return c.fakeHerdrClient.CreateTab(workspaceID, cwd, label)
}

func (c *concurrentHerdrClient) CloseTab(tabID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes++
	return c.fakeHerdrClient.CloseTab(tabID)
}

func (c *concurrentHerdrClient) RunPane(paneID, command string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runs++
	return c.fakeHerdrClient.RunPane(paneID, command)
}

func (c *concurrentHerdrClient) Agents() ([]herdr.Agent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fakeHerdrClient.Agents()
}

func (c *concurrentHerdrClient) calls(counter *int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return *counter
}

// A watcher with no owner observes a pull request forever and hands its work to
// nobody. Refusing before CreateTab is what keeps that process from existing —
// this is exactly the shape of a `deliver-pr` sub-agent inside a stack run,
// whose surrounding pane belongs to the orchestrator, not to the child project.
func TestPRWatchStartRefusesWithoutExactlyOneLiveOwner(t *testing.T) {
	for name, agents := range map[string][]herdr.Agent{
		"no live owner": {{TerminalTitle: "relay:stack-run", PaneID: "pane-stack"}},
		"duplicate owners": {
			{TerminalTitle: "relay:demo", PaneID: "pane-a"},
			{TerminalTitle: "relay:demo", PaneID: "pane-b"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("HERDR_ENV", "1")
			t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
			client := &fakeHerdrClient{
				tab:            herdr.Tab{ID: "tab-1", RootPaneID: "pane-1"},
				agentResponses: [][]herdr.Agent{agents},
			}
			installPRWatchFakes(t, client)
			prWatchIsRunning = func(string) (bool, error) { return false, nil }

			_, err := runPRCommand(t, "watch", "start", "demo")
			if err == nil {
				t.Fatal("start created a watcher with no exact live owner")
			}
			if !strings.Contains(err.Error(), "cannot start a pr watcher") {
				t.Errorf("start error = %v, want an owner validation failure", err)
			}
			if len(client.created) != 0 {
				t.Fatalf("start created tabs before failing owner validation: %+v", client.created)
			}
			if len(client.runPane) != 0 {
				t.Fatalf("start ran a watcher process anyway: %+v", client.runPane)
			}
		})
	}
}

func TestPRWatchStartValidatesManagedInvariantsBeforeCreatingATab(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &fakeHerdrClient{
		tab:            herdr.Tab{ID: "tab-1", RootPaneID: "pane-1"},
		agentResponses: [][]herdr.Agent{{{TerminalTitle: "relay:demo", PaneID: "pane-owner"}}},
	}
	installPRWatchFakes(t, client)
	prWatchIsRunning = func(string) (bool, error) { return false, nil }
	prWatchRequireManaged = func(slug string) error {
		return fmt.Errorf("project %q is not a managed project", slug)
	}

	_, err := runPRCommand(t, "watch", "start", "demo", "--mode", "managed")
	if err == nil || !strings.Contains(err.Error(), "not a managed project") {
		t.Fatalf("managed start = %v, want the managed invariant enforced", err)
	}
	if len(client.created) != 0 {
		t.Fatalf("managed start created tabs before validating: %+v", client.created)
	}
}

func TestPRWatchStartAdoptsARunningWatcherAndWarnsOnADifferentTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	prWatchIsRunning = func(string) (bool, error) { return true, nil }
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{
			Project: slug, PID: 5, Status: prwatch.StatusRunning,
			Mode: prwatch.ModeStandalone, OwnerSlug: slug, WorkspaceID: "workspace-1",
		}, nil
	}

	out, err := runPRCommand(t, "watch", "start", "demo", "--json")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var adopted prWatchStartOutput
	if err := json.Unmarshal([]byte(out), &adopted); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if !adopted.Adopted || adopted.Warning != "" {
		t.Fatalf("adopted output = %+v, want a clean adoption", adopted)
	}
	if len(client.created) != 0 {
		t.Fatalf("adoption created tabs: %+v", client.created)
	}

	out, err = runPRCommand(t, "watch", "start", "demo", "--mode", "stack", "--owner", "stack-run", "--json")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var retarget prWatchStartOutput
	if err := json.Unmarshal([]byte(out), &retarget); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if !strings.Contains(retarget.Warning, "relay pr watch stop demo") {
		t.Errorf("warning = %q, want the stop command for a differently targeted watcher", retarget.Warning)
	}
}

func TestPRWatchStartRequiresHerdr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	prWatchIsRunning = func(string) (bool, error) { return false, nil }

	_, err := runPRCommand(t, "watch", "start", "demo")
	if err == nil || !strings.Contains(err.Error(), "HERDR_ENV=1") {
		t.Fatalf("start outside Herdr = %v, want a Herdr readiness failure", err)
	}
	if len(client.created) != 0 {
		t.Fatalf("start outside Herdr created tabs: %+v", client.created)
	}
}

func TestPRWatchStackModeRequiresAnOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})
	prWatchIsRunning = func(string) (bool, error) { return false, nil }

	_, err := runPRCommand(t, "watch", "start", "demo", "--mode", "stack")
	if err == nil || !strings.Contains(err.Error(), "--owner") {
		t.Fatalf("stack start without an owner = %v, want an owner requirement", err)
	}

	_, err = runPRCommand(t, "watch", "start", "demo", "--mode", "managed", "--owner", "program-tl")
	if err == nil || !strings.Contains(err.Error(), "not a valid owner") {
		t.Fatalf("managed start with a foreign owner = %v, want a rejection", err)
	}
}

func TestPRWatchRunRequiresHerdrAndPassesTheOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	herdrAvailable = func() bool { return false }

	_, err := runPRCommand(t, "watch", "run", "demo", "--mode", "stack", "--owner", "stack-run")
	if err == nil || !strings.Contains(err.Error(), "requires Herdr") {
		t.Fatalf("run without Herdr = %v, want a readiness failure", err)
	}

	herdrAvailable = func() bool { return true }
	var got prwatch.Options
	prWatchRunLoop = func(_ context.Context, slug string, options prwatch.Options) error {
		got = options
		if slug != "demo" {
			t.Errorf("run slug = %q", slug)
		}
		return nil
	}
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{Project: slug, Status: prwatch.StatusStopped}, nil
	}
	out, err := runPRCommand(t, "watch", "run", "demo", "--mode", "stack", "--owner", "stack-run")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got.Mode != prwatch.ModeStack || got.Owner != "stack-run" {
		t.Errorf("run options = %+v, want the stack orchestrator", got)
	}
	if got.Client == nil {
		t.Error("run options carried no Herdr client")
	}
	if !strings.Contains(out, "PR watch stopped for demo") {
		t.Errorf("run output = %q", out)
	}
}

func TestPRWatchStatusWorksWithoutHerdr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})
	herdrAvailable = func() bool { return false }
	prWatchIsRunning = func(string) (bool, error) { return true, nil }
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{
			Project: slug, Status: prwatch.StatusRunning, Mode: prwatch.ModeStandalone,
			OwnerSlug: slug, PRNumber: 42, PRState: "OPEN", ScheduledChecks: 3,
			NextCheckAt: "2026-03-01T09:00:00Z", ActionableCount: 2,
			CurrentFingerprint: strings.Repeat("a", 64), RelayVersion: version,
		}, nil
	}

	out, err := runPRCommand(t, "watch", "status", "demo", "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var got prWatchStatusOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Status != string(prwatch.StatusRunning) || got.State.ActionableCount != 2 {
		t.Fatalf("status = %+v", got)
	}
	if got.Warning != "" {
		t.Errorf("status warning = %q, want none", got.Warning)
	}

	text, err := runPRCommand(t, "watch", "status", "demo")
	if err != nil {
		t.Fatalf("status text: %v", err)
	}
	for _, want := range []string{"PR watch: running", "Owner: demo", "PR: #42 OPEN", "Scheduled checks: 3"} {
		if !strings.Contains(text, want) {
			t.Errorf("status text %q is missing %q", text, want)
		}
	}
}

func TestPRWatchStatusReportsACompletedWatcher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})
	prWatchIsRunning = func(string) (bool, error) { return false, nil }
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{
			Project: slug, Status: prwatch.StatusComplete, StopReason: "pull request merged",
			RelayVersion: version,
		}, nil
	}
	out, err := runPRCommand(t, "watch", "status", "demo", "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var got prWatchStatusOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Status != string(prwatch.StatusComplete) || got.StopReason != "pull request merged" {
		t.Fatalf("status = %+v, want the completed watch", got)
	}
}

func TestPRWatchStatusOfARecordWithoutALifecycleStaysNotRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})
	prWatchIsRunning = func(string) (bool, error) { return false, nil }
	// A record written before any watcher process ran carries no lifecycle
	// status; status must still read as not-running.
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{Project: slug, NextCheckAt: "2026-03-01T09:00:00Z"}, nil
	}

	out, err := runPRCommand(t, "watch", "status", "demo", "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var got prWatchStatusOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Status != "not-running" {
		t.Errorf("status = %q, want not-running", got.Status)
	}
}

func TestPRWatchStopSignalsTheRecordedProcessAndClosesItsTab(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	state := testRecordedWatcherState("demo", "tab-1", "pane-1", "term-1")
	installFakeWatcherInventory(client, state)
	calls := 0
	prWatchIsRunning = func(string) (bool, error) {
		calls++
		return calls == 1, nil
	}
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return state, nil
	}
	var signaled []int
	prWatchSignal = func(pid int, _ os.Signal) error {
		signaled = append(signaled, pid)
		return nil
	}

	out, err := runPRCommand(t, "watch", "stop", "demo")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if len(signaled) != 1 || signaled[0] != 4242 {
		t.Fatalf("signaled = %v, want the recorded pid", signaled)
	}
	if want := []string{"tab-1"}; !reflect.DeepEqual(client.closedTabs, want) {
		t.Errorf("closed tabs = %v, want the exact recorded tab %v", client.closedTabs, want)
	}
	if len(client.closedPanes) != 0 {
		t.Errorf("stop closed panes as well as the tab: %v", client.closedPanes)
	}
	for _, want := range []string{"PR watcher stopped for demo", "Closed watcher tab tab-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("stop output %q is missing %q", out, want)
		}
	}
}

// A watcher that finished on its own leaves its tab behind, so stopping it is
// still how the tab is cleaned up.
func TestPRWatchStopClosesTheTabOfAFinishedWatcher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	state := testRecordedWatcherState("demo", "tab-9", "pane-9", "term-9")
	state.Status = prwatch.StatusComplete
	installFakeWatcherInventory(client, state)
	prWatchIsRunning = func(string) (bool, error) { return false, nil }
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return state, nil
	}

	prWatchSignal = func(int, os.Signal) error {
		t.Fatal("stop signaled a watcher that was not running")
		return nil
	}

	out, err := runPRCommand(t, "watch", "stop", "demo")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if want := []string{"tab-9"}; !reflect.DeepEqual(client.closedTabs, want) {
		t.Errorf("closed tabs = %v, want %v", client.closedTabs, want)
	}
	if !strings.Contains(out, "Closed watcher tab tab-9") {
		t.Errorf("stop output = %q, want the tab cleanup reported", out)
	}
}

// Stopping from outside Herdr must not claim a cleanup it could not perform.
func TestPRWatchStopWithoutHerdrNamesTheTabItCouldNotClose(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	herdrAvailable = func() bool { return false }
	calls := 0
	prWatchIsRunning = func(string) (bool, error) {
		calls++
		return calls == 1, nil
	}
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{
			Project: slug, PID: 4242, Status: prwatch.StatusRunning, TabID: "tab-5",
		}, nil
	}
	prWatchSignal = func(int, os.Signal) error { return nil }

	out, err := runPRCommand(t, "watch", "stop", "demo", "--json")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	var got prWatchStopOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if !got.Stopped || got.Closed {
		t.Fatalf("stop output = %+v, want a stopped process and no claimed close", got)
	}
	if !strings.Contains(got.Warning, "herdr tab close tab-5") {
		t.Errorf("warning = %q, want the exact command that closes the tab", got.Warning)
	}
	if len(client.closedTabs) != 0 {
		t.Errorf("stop closed tabs with no Herdr: %v", client.closedTabs)
	}
}

func TestPRWatchTickAndDigestWorkWithoutHerdr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installPRWatchFakes(t, &fakeHerdrClient{})
	herdrAvailable = func() bool { return false }
	items := []prwatch.Item{{
		Reason: prwatch.ReasonNewComment, Source: prwatch.SourceComment, ID: "1",
		Key: "comment:1:2026-01-01T00:00:00Z", Body: "please rename this", Author: "reviewer",
	}}
	digest := prwatch.Digest{
		Project: "demo", Mode: prwatch.ModeStandalone, Fingerprint: prwatch.Fingerprint(items),
		ObservedAt: "2026-03-01T08:00:00Z", HeadSHA: "head222",
		PR:      prwatch.PullRequest{Number: 42, State: "OPEN", HeadSHA: "head222"},
		Items:   items,
		Waiting: []string{},
	}
	prWatchTickOnce = func(context.Context, string, prwatch.Options) (prwatch.Digest, error) {
		if err := prwatch.WriteDigest(digest); err != nil {
			t.Fatalf("WriteDigest: %v", err)
		}
		return digest, nil
	}

	out, err := runPRCommand(t, "watch", "tick", "demo", "--json")
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	var ticked prwatch.Digest
	if err := json.Unmarshal([]byte(out), &ticked); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if ticked.Fingerprint != digest.Fingerprint {
		t.Fatalf("tick digest = %+v", ticked)
	}

	out, err = runPRCommand(t, "watch", "digest", "demo", "--fingerprint", digest.Fingerprint, "--json")
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	var read prwatch.Digest
	if err := json.Unmarshal([]byte(out), &read); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(read.Items) != 1 || read.Items[0].Body != "please rename this" {
		t.Fatalf("digest items = %+v, want the recorded body", read.Items)
	}

	text, err := runPRCommand(t, "watch", "digest", "demo", "--fingerprint", digest.Fingerprint)
	if err != nil {
		t.Fatalf("digest text: %v", err)
	}
	if strings.Contains(text, "please rename this") {
		t.Errorf("digest text printed a body: %q", text)
	}
	if !strings.Contains(text, "new-comment comment 1") {
		t.Errorf("digest text = %q, want the item summary", text)
	}
}

func TestPRWatchRejectsInvalidFingerprints(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})

	if _, err := runPRCommand(t, "watch", "digest", "demo", "--fingerprint", "../escape"); err == nil {
		t.Error("digest accepted a traversing fingerprint")
	}
	if _, err := runPRCommand(t, "watch", "digest", "demo",
		"--fingerprint", strings.Repeat("a", 64)); err == nil {
		t.Error("digest accepted a fingerprint with no record")
	}
}

// The acknowledgement subsystem is gone: a watcher never asserts locally that
// attention was handled, so there is no command that could claim it.
func TestPRWatchHasNoAcknowledgeCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})
	out, err := runPRCommand(t, "watch", "--help")
	if err != nil {
		t.Fatalf("watch help: %v", err)
	}
	if strings.Contains(out, "acknowledge") {
		t.Errorf("`relay pr watch --help` still offers acknowledge:\n%s", out)
	}
	out, _ = runPRCommand(t, "watch", "acknowledge", "demo")
	if !strings.Contains(out, "Available Commands:") || strings.Contains(out, "acknowledge ") {
		t.Errorf("`relay pr watch acknowledge` did not fall back to usage:\n%s", out)
	}
}

func TestPRWatchTickReportsAProjectWithoutAPullRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(project.ActiveDir(), "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), "demo"), project.Manifest{
		Slug: "demo", Repo: home,
	}); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	if _, err := runPRCommand(t, "watch", "tick", "demo"); err == nil ||
		!strings.Contains(err.Error(), "relay state pr demo") {
		t.Fatalf("tick without a pull request = %v, want the recording command", err)
	}
}

func TestPRWatchIsRegisteredOnTheRootCommand(t *testing.T) {
	root := newRootCmd()
	var pr *cobra.Command
	for _, command := range root.Commands() {
		if command.Name() == "pr" {
			pr = command
		}
	}
	if pr == nil {
		t.Fatal("root command has no `pr` command")
	}
	var watch *cobra.Command
	for _, command := range pr.Commands() {
		if command.Name() == "watch" {
			watch = command
		}
	}
	if watch == nil {
		t.Fatal("`relay pr` has no `watch` command")
	}
	want := map[string]bool{
		"start": true, "run": true, "status": true, "stop": true,
		"tick": true, "digest": true,
	}
	for _, command := range watch.Commands() {
		if command.Name() == "acknowledge" {
			t.Error("`relay pr watch acknowledge` is still registered")
		}
		delete(want, command.Name())
		if command.Name() == "run" && !command.Hidden {
			t.Error("`relay pr watch run` is not hidden")
		}
	}
	if len(want) != 0 {
		t.Errorf("`relay pr watch` is missing %v", want)
	}
}

// seedPRWatchState writes a real watcher runtime record and points the stop
// path at the real store, so these tests exercise the state lock, the atomic
// write, and the revision the way `stop` actually does.
func seedPRWatchState(t *testing.T, slug string, mutate func(*prwatch.State)) prwatch.State {
	t.Helper()
	prWatchReadState = prwatch.ReadState
	prWatchReadStateLocked = prwatch.ReadStateLocked
	prWatchUpdateState = prwatch.UpdateState
	state, err := prwatch.UpdateState(slug, func(state prwatch.State) (prwatch.State, error) {
		state.PID = 4242
		state.Status = prwatch.StatusRunning
		state.StartedAt = "2026-03-01T08:00:00Z"
		state.WorkspaceID = "workspace-1"
		state.TabID = "tab-1"
		state.PaneID = "pane-1"
		state.TerminalID = "term-1"
		state.OwnerSlug = slug
		state.Mode = prwatch.ModeStandalone
		mutate(&state)
		return state, nil
	})
	if err != nil {
		t.Fatalf("seed watcher state: %v", err)
	}
	return state
}

func testRecordedWatcherState(slug, tabID, paneID, terminalID string) prwatch.State {
	return prwatch.State{
		Project: slug, PID: 4242, Status: prwatch.StatusRunning,
		StartedAt: "2026-03-01T08:00:00Z", WorkspaceID: "workspace-1",
		TabID: tabID, PaneID: paneID, TerminalID: terminalID,
	}
}

func installFakeWatcherInventory(client *fakeHerdrClient, state prwatch.State) {
	client.tabs = []herdr.TabInfo{{
		ID: state.TabID, WorkspaceID: state.WorkspaceID,
		Label: prWatchWorkspaceLabel(state.Project), PaneCount: 1,
	}}
	client.panes = []herdr.Pane{{
		ID: state.PaneID, TabID: state.TabID,
		WorkspaceID: state.WorkspaceID, TerminalID: state.TerminalID,
	}}
}

func readPRWatchState(t *testing.T, slug string) prwatch.State {
	t.Helper()
	state, err := prwatch.ReadState(slug)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	return state
}

// stopRunOnce makes IsRunning report a watcher that stops after the first call,
// which is what signaling and awaiting its exit looks like.
func stopRunOnce() {
	calls := 0
	prWatchIsRunning = func(string) (bool, error) {
		calls++
		return calls == 1, nil
	}
}

// A tab id that stays in the record after its tab is gone is a lie the next
// stop acts on, and Herdr reuses ids.
func TestPRWatchStopClearsTheTabItClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	seeded := seedPRWatchState(t, "demo", func(state *prwatch.State) {
		state.StopReason = ""
		state.LastWakeStatus = "delivered"
	})
	installFakeWatcherInventory(client, seeded)
	stopRunOnce()
	prWatchSignal = func(int, os.Signal) error { return nil }

	if _, err := runPRCommand(t, "watch", "stop", "demo"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if want := []string{"tab-1"}; !reflect.DeepEqual(client.closedTabs, want) {
		t.Fatalf("closed tabs = %v, want %v", client.closedTabs, want)
	}
	cleared := readPRWatchState(t, "demo")
	if cleared.TabID != "" || cleared.PaneID != "" {
		t.Errorf("state = %+v, want the closed tab and pane cleared", cleared)
	}
	if cleared.Revision <= seeded.Revision {
		t.Errorf("revision = %d, want it past the seeded %d", cleared.Revision, seeded.Revision)
	}
	if cleared.PID != seeded.PID || cleared.LastWakeStatus != "delivered" ||
		cleared.OwnerSlug != seeded.OwnerSlug {
		t.Errorf("state = %+v, want everything but the tab and pane preserved", cleared)
	}
}

// A watcher that finished on its own still has its tab closed and cleared, and
// stopping it again closes nothing and claims nothing.
func TestPRWatchStopIsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	seeded := seedPRWatchState(t, "demo", func(state *prwatch.State) {
		state.Status = prwatch.StatusComplete
		state.StopReason = "pull request merged"
	})
	installFakeWatcherInventory(client, seeded)
	prWatchIsRunning = func(string) (bool, error) { return false, nil }
	prWatchSignal = func(int, os.Signal) error {
		t.Fatal("stop signaled a watcher that was not running")
		return nil
	}

	first, err := runPRCommand(t, "watch", "stop", "demo")
	if err != nil {
		t.Fatalf("first stop: %v", err)
	}
	if !strings.Contains(first, "Closed watcher tab tab-1") {
		t.Fatalf("first stop = %q, want the tab cleanup reported", first)
	}
	if got := readPRWatchState(t, "demo"); got.StopReason != "pull request merged" ||
		got.Status != prwatch.StatusComplete {
		t.Errorf("state = %+v, want why the watcher stopped preserved", got)
	}

	out, err := runPRCommand(t, "watch", "stop", "demo", "--json")
	if err != nil {
		t.Fatalf("second stop: %v", err)
	}
	var second prWatchStopOutput
	if err := json.Unmarshal([]byte(out), &second); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if second.Closed || second.TabID != "" || second.PaneID != "" || second.Warning != "" {
		t.Errorf("second stop = %+v, want no second close and no claim about a tab", second)
	}
	if want := []string{"tab-1"}; !reflect.DeepEqual(client.closedTabs, want) {
		t.Errorf("closed tabs = %v, want the tab closed exactly once", client.closedTabs)
	}
}

// A close that failed leaves the ids in the record, because the tab is still
// there and the next stop is how it gets cleaned up.
func TestPRWatchStopKeepsTheTabItCouldNotClose(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{closeErr: fmt.Errorf("herdr: tab is busy")}
	installPRWatchFakes(t, client)
	seeded := seedPRWatchState(t, "demo", func(*prwatch.State) {})
	installFakeWatcherInventory(client, seeded)
	stopRunOnce()
	prWatchSignal = func(int, os.Signal) error { return nil }

	out, err := runPRCommand(t, "watch", "stop", "demo", "--json")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	var got prWatchStopOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Closed || !strings.Contains(got.Warning, "herdr tab close tab-1") {
		t.Fatalf("stop = %+v, want the failed close reported with the command that finishes it", got)
	}
	kept := readPRWatchState(t, "demo")
	if kept.TabID != "tab-1" || kept.PaneID != "pane-1" {
		t.Errorf("state = %+v, want the ids kept for the tab that is still open", kept)
	}
}

// Herdr reuses tab and pane ids. A watcher somebody restarted between the
// signal and the close must not have its brand-new pane closed by an id that no
// longer means what it meant.
func TestPRWatchStopSkipsAReplacedWatcher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	seeded := seedPRWatchState(t, "demo", func(*prwatch.State) {})
	installFakeWatcherInventory(client, seeded)
	stopRunOnce()
	prWatchSignal = func(int, os.Signal) error {
		// The operator restarted the watcher, which took the same tab id back.
		if _, err := prwatch.UpdateState("demo", func(state prwatch.State) (prwatch.State, error) {
			state.PID = 5150
			state.StartedAt = "2026-03-01T09:30:00Z"
			state.Status = prwatch.StatusRunning
			return state, nil
		}); err != nil {
			t.Fatalf("restart the watcher: %v", err)
		}
		return nil
	}

	out, err := runPRCommand(t, "watch", "stop", "demo", "--json")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	var got prWatchStopOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Closed {
		t.Fatalf("stop = %+v, want no close of a reused tab id", got)
	}
	if len(client.closedTabs) != 0 || len(client.closedPanes) != 0 {
		t.Fatalf("closed = %v/%v, want nothing closed", client.closedTabs, client.closedPanes)
	}
	for _, want := range []string{"pid 5150", "relay pr watch stop demo"} {
		if !strings.Contains(got.Warning, want) {
			t.Errorf("warning = %q, want it to name %q", got.Warning, want)
		}
	}
	live := readPRWatchState(t, "demo")
	if live.TabID != "tab-1" || live.PaneID != "pane-1" || live.PID != 5150 {
		t.Errorf("state = %+v, want the running watcher's own tab left alone", live)
	}
}

// pinDisplayZone fixes the zone human status output renders in, so an assertion
// about an offset holds wherever the suite runs. The process's own zone is
// never changed.
func pinDisplayZone(t *testing.T) {
	t.Helper()
	previous := displayZone
	zone := time.FixedZone("TEST", -4*60*60)
	displayZone = func() *time.Location { return zone }
	t.Cleanup(func() { displayZone = previous })
}

// Status is read by a person, so every timestamp it prints is the reader's own
// wall clock. The JSON is read by a program, so it hands back the stored UTC
// record byte for byte.
func TestPRWatchStatusPrintsLocalTimeAndReportsUTCJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})
	pinDisplayZone(t)
	prWatchIsRunning = func(string) (bool, error) { return true, nil }
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{
			Project: slug, Status: prwatch.StatusRunning, Mode: prwatch.ModeStandalone,
			OwnerSlug: slug, PRNumber: 42, PRState: "OPEN", ScheduledChecks: 3,
			LastCheckAt: "2026-03-01T08:45:00Z", NextCheckAt: "2026-03-01T09:00:00Z",
			LastWakeStatus: "delivered", LastWakeAt: "2026-03-01T08:45:01Z",
			RelayVersion: version,
		}, nil
	}

	text, err := runPRCommand(t, "watch", "status", "demo")
	if err != nil {
		t.Fatalf("status text: %v", err)
	}
	for _, want := range []string{
		"Last check: 2026-03-01T04:45:00-04:00",
		"Next check: 2026-03-01T05:00:00-04:00",
		"Last owner wake: delivered at 2026-03-01T04:45:01-04:00",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status text is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "2026-03-01T09:00:00Z") {
		t.Errorf("status text printed a stored UTC value:\n%s", text)
	}

	raw, err := runPRCommand(t, "watch", "status", "demo", "--json")
	if err != nil {
		t.Fatalf("status json: %v", err)
	}
	if strings.Contains(raw, "-04:00") {
		t.Errorf("status JSON carries a display offset:\n%s", raw)
	}
	var got prWatchStatusOutput
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if got.State.NextCheckAt != "2026-03-01T09:00:00Z" || got.State.LastCheckAt != "2026-03-01T08:45:00Z" {
		t.Errorf("status JSON timestamps = %q and %q, want the stored UTC record",
			got.State.LastCheckAt, got.State.NextCheckAt)
	}
	if got.State.LastWakeAt != "2026-03-01T08:45:01Z" {
		t.Errorf("status JSON wake time = %q, want the stored UTC record", got.State.LastWakeAt)
	}
}

// A recorded value that is not RFC3339 — written by hand, or by a build that
// predates the field — is shown exactly as recorded rather than blanked.
func TestPRWatchStatusPrintsAnUnparsableTimestampVerbatim(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installPRWatchFakes(t, &fakeHerdrClient{})
	pinDisplayZone(t)
	prWatchIsRunning = func(string) (bool, error) { return true, nil }
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{
			Project: slug, Status: prwatch.StatusRunning, Mode: prwatch.ModeStandalone,
			OwnerSlug: slug, PRNumber: 42, PRState: "OPEN",
			LastCheckAt: "a while ago", NextCheckAt: "", RelayVersion: version,
		}, nil
	}
	text, err := runPRCommand(t, "watch", "status", "demo")
	if err != nil {
		t.Fatalf("status text: %v", err)
	}
	if !strings.Contains(text, "Last check: a while ago") {
		t.Errorf("status text is missing the verbatim value:\n%s", text)
	}
}

func TestPRWatchStartPreservesRecordedTabReplacedBeforeReuse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	recorded := testRecordedWatcherState("demo", "tab-stale", "pane-stale", "term-original")
	validTabs := []herdr.TabInfo{{
		ID: recorded.TabID, WorkspaceID: recorded.WorkspaceID,
		Label: prWatchWorkspaceLabel("demo"), PaneCount: 1,
	}}
	tabCalls := 0
	client := &fakeHerdrClient{
		tab: herdr.Tab{
			ID: "tab-fresh", RootPaneID: "pane-fresh", TerminalID: "term-fresh",
		},
		tabsHook: func(string) ([]herdr.TabInfo, error) {
			tabCalls++
			return validTabs, nil
		},
		panesHook: func(string) ([]herdr.Pane, error) {
			terminal := "term-original"
			if tabCalls > 1 {
				terminal = "term-replacement"
			}
			return []herdr.Pane{{
				ID: recorded.PaneID, TabID: recorded.TabID,
				WorkspaceID: recorded.WorkspaceID, TerminalID: terminal,
			}}, nil
		},
		agentResponses: [][]herdr.Agent{{{TerminalTitle: "relay:demo", PaneID: "pane-owner"}}},
	}
	installPRWatchFakes(t, client)
	launch := &prWatchLaunch{
		stale: recorded,
		running: prwatch.State{
			Project: "demo", PID: 78, Status: prwatch.StatusRunning,
			Mode: prwatch.ModeStandalone, OwnerSlug: "demo", WorkspaceID: "workspace-1",
			TabID: "tab-fresh", PaneID: "pane-fresh", TerminalID: "term-fresh",
		},
	}
	launch.install(t, client)

	out, err := runPRCommand(t, "watch", "start", "demo", "--json")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var got prWatchStartOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.TabReused || got.TabID != "tab-fresh" {
		t.Fatalf("result = %+v, want a fresh tab after identity replacement", got)
	}
	if len(client.closedTabs) != 0 || len(client.runPane) != 1 ||
		client.runPane[0].pane != "pane-fresh" {
		t.Fatalf("replacement was mutated: closed=%v run=%v", client.closedTabs, client.runPane)
	}
	if !strings.Contains(got.Warning, "terminal term-replacement") {
		t.Fatalf("warning = %q, want the replacement terminal named", got.Warning)
	}
}

func TestPRWatchStopAndCleanupWaitForTheStartLifecycleLock(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func() error
	}{
		{
			name: "stop",
			run: func() error {
				return runPRWatchStop(io.Discard, "demo", false)
			},
		},
		{
			name: "program cleanup watcher step",
			run: func() error {
				_, _, _, err := stopCleanupWatcher("demo")
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			installPRWatchFakes(t, &fakeHerdrClient{})
			livenessChecked := make(chan struct{}, 1)
			prWatchIsRunning = func(string) (bool, error) {
				livenessChecked <- struct{}{}
				return false, nil
			}
			prWatchReadState = func(string) (prwatch.State, error) {
				return prwatch.State{}, os.ErrNotExist
			}
			startLock, err := prwatch.AcquireLifecycleLock("demo", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- test.run() }()

			select {
			case <-livenessChecked:
				t.Fatal("stop inspected liveness while a start held the lifecycle lock")
			case <-time.After(100 * time.Millisecond):
			}
			if err := startLock.Release(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("operation after lifecycle release: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("operation did not continue after lifecycle release")
			}
		})
	}
}

func TestPRWatchStartReportsRunningWatcherInAnotherWorkspaceAsIncomplete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-current")
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	prWatchIsRunning = func(string) (bool, error) { return true, nil }
	prWatchReadState = func(slug string) (prwatch.State, error) {
		return prwatch.State{
			Project: slug, PID: 5, Status: prwatch.StatusRunning,
			Mode: prwatch.ModeStandalone, OwnerSlug: slug,
			WorkspaceID: "workspace-other", TabID: "tab-other", PaneID: "pane-other",
		}, nil
	}

	out, err := runPRCommand(t, "watch", "start", "demo", "--json")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var got prWatchStartOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Running || !got.Adopted || !got.Incomplete {
		t.Fatalf("result = %+v, want an incomplete adoption", got)
	}
	for _, want := range []string{"workspace-other", "workspace-current", "no duplicate was started"} {
		if !strings.Contains(got.Warning, want) {
			t.Errorf("warning = %q, want %q", got.Warning, want)
		}
	}
	if len(client.created) != 0 || len(client.runPane) != 0 {
		t.Fatalf("different-workspace adoption launched a duplicate: created=%v run=%v", client.created, client.runPane)
	}
}

func TestPRWatchStopPreservesARecordedTabWhoseTerminalWasReplaced(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPRWatchFakes(t, client)
	state := testRecordedWatcherState("demo", "tab-9", "pane-9", "term-original")
	state.Status = prwatch.StatusComplete
	installFakeWatcherInventory(client, state)
	client.panes[0].TerminalID = "term-replacement"
	prWatchIsRunning = func(string) (bool, error) { return false, nil }
	prWatchReadState = func(string) (prwatch.State, error) { return state, nil }
	prWatchReadStateLocked = prWatchReadState

	out, err := runPRCommand(t, "watch", "stop", "demo", "--json")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	var got prWatchStopOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Closed || len(client.closedTabs) != 0 {
		t.Fatalf("stop closed a replacement terminal: result=%+v closed=%v", got, client.closedTabs)
	}
	if !strings.Contains(got.Warning, "terminal term-replacement") {
		t.Fatalf("warning = %q, want the replacement identity", got.Warning)
	}
}

func TestPRWatchStartPreservesARepurposedRecordedSinglePaneTab(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	recorded := testRecordedWatcherState("demo", "tab-recorded", "pane-recorded", "term-recorded")
	client := &fakeHerdrClient{
		tab: herdr.Tab{
			ID: "tab-fresh", RootPaneID: "pane-fresh", TerminalID: "term-fresh",
		},
		tabs: []herdr.TabInfo{{
			ID: recorded.TabID, WorkspaceID: recorded.WorkspaceID,
			Label: "My shell", PaneCount: 1,
		}},
		panes: []herdr.Pane{{
			ID: recorded.PaneID, TabID: recorded.TabID,
			WorkspaceID: recorded.WorkspaceID, TerminalID: recorded.TerminalID,
		}},
		agentResponses: [][]herdr.Agent{{{TerminalTitle: "relay:demo", PaneID: "pane-owner"}}},
	}
	installPRWatchFakes(t, client)
	launch := &prWatchLaunch{
		stale: recorded,
		running: prwatch.State{
			Project: "demo", PID: 78, Status: prwatch.StatusRunning,
			Mode: prwatch.ModeStandalone, OwnerSlug: "demo", WorkspaceID: "workspace-1",
			TabID: "tab-fresh", PaneID: "pane-fresh", TerminalID: "term-fresh",
		},
	}
	launch.install(t, client)

	out, err := runPRCommand(t, "watch", "start", "demo", "--json")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var got prWatchStartOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.TabReused || got.TabID != "tab-fresh" || len(client.closedTabs) != 0 {
		t.Fatalf("result = %+v closed=%v, want the repurposed tab preserved", got, client.closedTabs)
	}
	if !strings.Contains(got.Warning, `now labeled "My shell"`) {
		t.Fatalf("warning = %q, want the repurposed label", got.Warning)
	}
}

func TestPRWatchStartReportsLaunchFailureWithoutClosingLabelOnlyTabs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	recorded := testRecordedWatcherState("demo", "tab-recorded", "pane-recorded", "term-recorded")
	client := &fakeHerdrClient{
		tabs: []herdr.TabInfo{
			{
				ID: recorded.TabID, WorkspaceID: recorded.WorkspaceID,
				Label: prWatchWorkspaceLabel("demo"), PaneCount: 1,
			},
			{
				ID: "tab-label-only", WorkspaceID: "workspace-1",
				Label: prWatchWorkspaceLabel("demo"), PaneCount: 1,
			},
		},
		panes: []herdr.Pane{
			{
				ID: recorded.PaneID, TabID: recorded.TabID,
				WorkspaceID: recorded.WorkspaceID, TerminalID: recorded.TerminalID,
			},
			{
				ID: "pane-label-only", TabID: "tab-label-only",
				WorkspaceID: "workspace-1", TerminalID: "term-label-only",
			},
		},
		agentResponses: [][]herdr.Agent{{{TerminalTitle: "relay:demo", PaneID: "pane-owner"}}},
	}
	installPRWatchFakes(t, client)
	launch := &prWatchLaunch{stale: recorded}
	launch.install(t, client)
	client.runPaneHook = func(string, string) error { return errors.New("herdr launch failed") }

	_, err := runPRCommand(t, "watch", "start", "demo", "--json")
	if err == nil {
		t.Fatal("start succeeded although the watcher command failed")
	}
	for _, want := range []string{"recorded tab tab-recorded pane pane-recorded", "herdr launch failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want %q", err, want)
		}
	}
	if len(client.closedTabs) != 0 {
		t.Fatalf("launch failure closed preserved tabs: %v", client.closedTabs)
	}
	if len(client.runPane) != 1 || client.runPane[0].pane != recorded.PaneID {
		t.Fatalf("run attempts = %v, want the exact recorded pane", client.runPane)
	}
}

func TestPRWatchStartPreservesRecordedTabWhenReuseRevalidationFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	recorded := testRecordedWatcherState("demo", "tab-recorded", "pane-recorded", "term-recorded")
	paneCalls := 0
	client := &fakeHerdrClient{
		tabs: []herdr.TabInfo{{
			ID: recorded.TabID, WorkspaceID: recorded.WorkspaceID,
			Label: prWatchWorkspaceLabel("demo"), PaneCount: 1,
		}},
		panesHook: func(string) ([]herdr.Pane, error) {
			paneCalls++
			if paneCalls == 2 {
				return nil, errors.New("Herdr inventory unavailable")
			}
			return []herdr.Pane{{
				ID: recorded.PaneID, TabID: recorded.TabID,
				WorkspaceID: recorded.WorkspaceID, TerminalID: recorded.TerminalID,
			}}, nil
		},
		agentResponses: [][]herdr.Agent{{{TerminalTitle: "relay:demo", PaneID: "pane-owner"}}},
	}
	installPRWatchFakes(t, client)
	launch := &prWatchLaunch{stale: recorded}
	launch.install(t, client)

	_, err := runPRCommand(t, "watch", "start", "demo", "--json")
	if err == nil || !strings.Contains(err.Error(), "revalidate recorded PR watcher tab") ||
		!strings.Contains(err.Error(), "Herdr inventory unavailable") {
		t.Fatalf("error = %v, want the revalidation failure", err)
	}
	if len(client.closedTabs) != 0 || len(client.runPane) != 0 {
		t.Fatalf("revalidation failure mutated the recorded tab: closed=%v run=%v",
			client.closedTabs, client.runPane)
	}
}

func TestPRWatchStartWaitsForConcurrentStopBeforeCheckingLiveness(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &fakeHerdrClient{
		tab: herdr.Tab{
			ID: "tab-fresh", RootPaneID: "pane-fresh", TerminalID: "term-fresh",
		},
		agentResponses: [][]herdr.Agent{{{TerminalTitle: "relay:demo", PaneID: "pane-owner"}}},
	}
	installPRWatchFakes(t, client)

	var mu sync.Mutex
	running := true
	launched := false
	livenessCalls := 0
	prWatchIsRunning = func(string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		livenessCalls++
		return running, nil
	}
	prWatchReadState = func(slug string) (prwatch.State, error) {
		mu.Lock()
		defer mu.Unlock()
		if !launched {
			return prwatch.State{}, os.ErrNotExist
		}
		return prwatch.State{
			Project: slug, PID: 78, Status: prwatch.StatusRunning,
			Mode: prwatch.ModeStandalone, OwnerSlug: slug, WorkspaceID: "workspace-1",
			TabID: "tab-fresh", PaneID: "pane-fresh", TerminalID: "term-fresh",
		}, nil
	}
	client.runPaneHook = func(string, string) error {
		mu.Lock()
		defer mu.Unlock()
		launched = true
		running = true
		return nil
	}

	stopLock, err := prwatch.AcquireLifecycleLock("demo", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result prWatchStartOutput
		err    error
	}
	done := make(chan outcome, 1)
	entered := make(chan struct{})
	go func() {
		close(entered)
		result, err := startPRWatcher(
			"demo", &prWatchModeFlags{mode: string(prwatch.ModeStandalone)},
			"relay pr watch start",
		)
		done <- outcome{result: result, err: err}
	}()

	<-entered
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	if livenessCalls != 0 {
		mu.Unlock()
		t.Fatal("start inspected liveness while stop held the lifecycle lock")
	}
	running = false
	mu.Unlock()
	if err := stopLock.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("start after stop: %v", got.err)
		}
		if got.result.Adopted || !got.result.Running {
			t.Fatalf("result = %+v, want a fresh start after stop completed", got.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("start did not continue after stop released the lifecycle lock")
	}
	if len(client.runPane) != 1 {
		t.Fatalf("pane launches = %v, want one fresh watcher", client.runPane)
	}
}
