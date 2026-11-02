package herdr

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeHC serves canned `hc` responses keyed by the joined arguments and
// records every call.
type fakeHC struct {
	t         *testing.T
	responses map[string][]string
	calls     []string
}

func (f *fakeHC) run(args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	queue, ok := f.responses[key]
	if !ok || len(queue) == 0 {
		f.t.Fatalf("unexpected hc %s", key)
	}
	response := queue[0]
	if len(queue) > 1 {
		f.responses[key] = queue[1:]
	}
	if strings.HasPrefix(response, "ERR ") {
		return []byte(strings.TrimPrefix(response, "ERR ")), errors.New("exit status 3")
	}
	return []byte(response), nil
}

func newFakeDesktop(t *testing.T, responses map[string][]string) (*Client, *fakeHC, *[]string) {
	t.Helper()
	hc := &fakeHC{t: t, responses: responses}
	var appCalls []string
	client := NewDesktopClientWithRunners(hc.run, func(args ...string) ([]byte, error) {
		appCalls = append(appCalls, strings.Join(args, " "))
		return []byte(`{"ok":true}`), nil
	})
	client.sleep = func(time.Duration) {}
	return client, hc, &appCalls
}

const desktopOwner = `{"address":"t8","cwd":"/repo","state":"live","title":"relay:demo - GitHub Copilot",` +
	`"labels":{"herdr.space":"sp1","herdr.tab":"tb1"},"status":{"state":"idle","app":"copilot"},` +
	`"status_ms":100,"foreground":{"cwd":"/repo/wt"}}`

func TestDesktopAgentsListsOnlyLocalTerminalsThatReportStatus(t *testing.T) {
	client, _, _ := newFakeDesktop(t, map[string][]string{
		"ls --json": {`{"terminals":[` +
			`{"address":"t2","state":"live"},` +
			`{"address":"t8","state":"live","status":"idle"},` +
			`{"address":"coder/t3","state":"live","status":"working"}]}`},
		"get --json t8": {desktopOwner},
	})

	agents, err := client.Agents()
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	want := []Agent{{
		Status: StatusIdle, StateChangeSeq: 100, PaneID: "t8", TabID: "tb1", WorkspaceID: "sp1",
		TerminalID: "t8", TerminalTitle: "relay:demo - GitHub Copilot", CWD: "/repo", ForegroundCWD: "/repo/wt",
	}}
	if !reflect.DeepEqual(agents, want) {
		t.Fatalf("Agents = %#v, want %#v", agents, want)
	}
	if owner, err := FindLiveProjectOwner(agents, "demo"); err != nil || owner.PaneID != "t8" {
		t.Fatalf("FindLiveProjectOwner = %#v, %v", owner, err)
	}
}

func TestDesktopTabsAndPanesGroupTerminalsBySpaceAndTab(t *testing.T) {
	ls := `{"terminals":[{"address":"t8","state":"live","status":"idle"},{"address":"t9","state":"live"}]}`
	watcher := `{"address":"t9","cwd":"/repo","state":"live","title":"zsh",` +
		`"labels":{"herdr.space":"sp1","herdr.tab":"tb2","relay.tab":"relay-pr-watch:demo"},"status":null}`
	client, _, _ := newFakeDesktop(t, map[string][]string{
		"ls --json --label herdr.space=sp1": {ls, ls},
		"get --json t8":                     {desktopOwner, desktopOwner},
		"get --json t9":                     {watcher, watcher},
	})

	tabs, err := client.Tabs("sp1")
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	wantTabs := []TabInfo{
		{ID: "tb1", WorkspaceID: "sp1", PaneCount: 1, Status: StatusIdle},
		{ID: "tb2", WorkspaceID: "sp1", Label: "relay-pr-watch:demo", PaneCount: 1},
	}
	if !reflect.DeepEqual(tabs, wantTabs) {
		t.Fatalf("Tabs = %#v, want %#v", tabs, wantTabs)
	}
	panes, err := client.Panes("sp1")
	if err != nil {
		t.Fatalf("Panes: %v", err)
	}
	if len(panes) != 2 || panes[1] != (Pane{ID: "t9", TabID: "tb2", WorkspaceID: "sp1", TerminalID: "t9", CWD: "/repo"}) {
		t.Fatalf("Panes = %#v", panes)
	}
}

func TestDesktopCreateTabStartsALabeledParentlessShell(t *testing.T) {
	run := "run --bg --json --keep --no-parent --outlive-parent --no-notify --cwd /repo " +
		"--label herdr.space=sp1 --label herdr.tab=tbNEW --label relay.tab=relay-pr-watch:demo"
	client, _, appCalls := newFakeDesktop(t, map[string][]string{run: {`{"address":"t1z","state":"live"}`}})
	client.desktop.newTabID = func() (string, error) { return "tbNEW", nil }

	tab, err := client.CreateTab("sp1", "/repo", "relay-pr-watch:demo")
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	want := Tab{ID: "tbNEW", RootPaneID: "t1z", WorkspaceID: "sp1", TerminalID: "t1z"}
	if tab != want {
		t.Fatalf("CreateTab = %#v, want %#v", tab, want)
	}
	if len(*appCalls) != 1 || !strings.Contains((*appCalls)[0], `tab.rename {"id":"tbNEW","name":"relay-pr-watch:demo"}`) {
		t.Fatalf("herdr-app calls = %#v", *appCalls)
	}
}

func TestDesktopCloseTabEndsAndRemovesEveryTerminalInTheTab(t *testing.T) {
	client, hc, _ := newFakeDesktop(t, map[string][]string{
		"ls --all --json --label herdr.tab=tb2": {`{"terminals":[` +
			`{"address":"t9","state":"live"},{"address":"t1b","state":"finished"}]}`},
		"kill --json t9": {`{}`},
		"rm --json t9":   {`ERR not_found`},
		"rm --json t1b":  {`{}`},
	})

	if err := client.CloseTab("tb2"); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}
	want := []string{"ls --all --json --label herdr.tab=tb2", "kill --json t9", "rm --json t9", "rm --json t1b"}
	if got := hc.calls; !reflect.DeepEqual(got, want) {
		t.Fatalf("hc calls = %#v", hc.calls)
	}
}

func TestDesktopPromptAgentSendsTextAndConfirmsTheTurn(t *testing.T) {
	working := strings.Replace(strings.Replace(desktopOwner, `"idle"`, `"working"`, 1), `"status_ms":100`, `"status_ms":200`, 1)
	ls := `{"terminals":[{"address":"t8","state":"live","status":"idle"}]}`
	client, hc, _ := newFakeDesktop(t, map[string][]string{
		"ls --json":            {ls},
		"get --json t8":        {desktopOwner, working},
		"send --json t8 hello": {`{}`},
	})

	if err := client.PromptAgent("t8", "hello"); err != nil {
		t.Fatalf("PromptAgent: %v", err)
	}
	for _, call := range hc.calls {
		if strings.HasPrefix(call, "keys") {
			t.Fatalf("pressed Enter although the turn had started: %#v", hc.calls)
		}
	}
}

func TestDesktopPromptAgentPressesEnterWhenTheTextStaysStaged(t *testing.T) {
	working := strings.Replace(strings.Replace(desktopOwner, `"idle"`, `"working"`, 1), `"status_ms":100`, `"status_ms":200`, 1)
	ls := `{"terminals":[{"address":"t8","state":"live","status":"idle"}]}`
	client, hc, _ := newFakeDesktop(t, map[string][]string{
		"ls --json":            {ls},
		"get --json t8":        {desktopOwner, desktopOwner, desktopOwner, working},
		"send --json t8 hello": {`{}`},
		"keys --json t8 enter": {`{}`},
	})

	if err := client.PromptAgent("t8", "hello"); err != nil {
		t.Fatalf("PromptAgent: %v", err)
	}
	if !containsCall(hc.calls, "keys --json t8 enter") {
		t.Fatalf("Enter was not pressed: %#v", hc.calls)
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func TestDesktopEnvironmentPrefersTheTUI(t *testing.T) {
	t.Setenv("HC_TERMINAL_ID", "t8")
	t.Setenv("HERDR_ENV", "")
	if !DesktopEnvironment() || !InManagedTerminal() {
		t.Fatal("a desktop terminal should select the desktop backend")
	}
	t.Setenv("HERDR_ENV", "1")
	if DesktopEnvironment() {
		t.Fatal("a Herdr TUI pane inside a desktop terminal should keep the TUI backend")
	}
}

func TestRequireEnvironmentUsesTheDesktopSpace(t *testing.T) {
	t.Setenv("HC_TERMINAL_ID", "t8")
	t.Setenv("HERDR_ENV", "")
	original := currentDesktopSpace
	t.Cleanup(func() { currentDesktopSpace = original })
	currentDesktopSpace = func() (string, error) { return "sp1", nil }

	workspace, err := RequireEnvironment()
	if err != nil || workspace != "sp1" {
		t.Fatalf("RequireEnvironment = %q, %v", workspace, err)
	}
}

func TestDesktopStatusRecord(t *testing.T) {
	record, err := DesktopStatusRecord("blocked", "permission", "copilot")
	if err != nil || record != "\x1b]7501;state=blocked:kind=permission:app=copilot\x1b\\" {
		t.Fatalf("record = %q, %v", record, err)
	}
	for _, bad := range [][3]string{{"busy", "", ""}, {"idle", "permission", ""}, {"idle", "", "a:b"}} {
		if _, err := DesktopStatusRecord(bad[0], bad[1], bad[2]); err == nil {
			t.Fatalf("DesktopStatusRecord(%q) accepted", bad)
		}
	}
}

func TestReportDesktopStatusIgnoresSubagentEvents(t *testing.T) {
	if !firedInSubagent([]byte(`{"agentId":"a1"}`)) || !firedInSubagent([]byte(`{"agent_id":"a1"}`)) {
		t.Fatal("subagent hook input was not recognized")
	}
	if firedInSubagent([]byte(`{"sessionId":"s1"}`)) || firedInSubagent(nil) {
		t.Fatal("root hook input was treated as a subagent")
	}
}

func TestDesktopExitAgentWaitsThroughTheExitTurn(t *testing.T) {
	working := strings.Replace(strings.Replace(desktopOwner, `"idle"`, `"working"`, 1), `"status_ms":100`, `"status_ms":200`, 1)
	client, hc, _ := newFakeDesktop(t, map[string][]string{
		"ls --json": {
			`{"terminals":[{"address":"t8","state":"live","status":"idle"}]}`,
			`{"terminals":[{"address":"t8","state":"live","status":"working"}]}`,
			`{"terminals":[{"address":"t8","state":"live"}]}`,
		},
		"get --json t8":        {desktopOwner, working},
		"send --json t8 /exit": {`{}`},
	})
	client.promptSubmitGrace = 0

	result, err := client.ExitAgent(SessionIdentity{PaneID: "t8", TabID: "tb1", WorkspaceID: "sp1", TerminalID: "t8"})
	if err != nil {
		t.Fatalf("ExitAgent: %v", err)
	}
	if result.Outcome != ExitedNow || !result.PaneGone {
		t.Fatalf("ExitAgent = %#v; hc calls %#v", result, hc.calls)
	}
}

func TestDesktopAgentsReadTheRecordedSessionID(t *testing.T) {
	owner := strings.Replace(desktopOwner, `"herdr.tab":"tb1"`, `"herdr.tab":"tb1","relay.session":"abc-123"`, 1)
	client, _, _ := newFakeDesktop(t, map[string][]string{
		"ls --json":     {`{"terminals":[{"address":"t8","state":"live","status":"idle"}]}`},
		"get --json t8": {owner},
	})

	agents, err := client.Agents()
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	if len(agents) != 1 || agents[0].NativeSessionID != "abc-123" {
		t.Fatalf("Agents = %#v", agents)
	}
}

func TestHookSessionID(t *testing.T) {
	for input, want := range map[string]string{
		`{"session_id":"s1"}`: "s1",
		`{"sessionId":"s2"}`:  "s2",
		`{"sessionId":"a=b"}`: "",
		`{"cwd":"/repo"}`:     "",
		`not json`:            "",
	} {
		if got := hookSessionID([]byte(input)); got != want {
			t.Errorf("hookSessionID(%s) = %q, want %q", input, got, want)
		}
	}
}
