package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/patrol"
	"github.com/ronaknnathani/relay/internal/program"
)

func createPatrolProgram(t *testing.T, slug string, state program.State) program.Program {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	p, err := program.New(slug, "Patrol", t.TempDir(), "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if state != program.StateDraft {
		if err := p.Transition(program.StatePendingApproval, ""); err != nil {
			t.Fatal(err)
		}
	}
	if state == program.StateActive || state == program.StateHeld {
		if err := p.Transition(program.StateActive, "ceo"); err != nil {
			t.Fatal(err)
		}
	}
	if state == program.StateHeld {
		if err := p.Transition(program.StateHeld, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProgramPatrolStartCreatesPlainHerdrTabAndAdoptsRunning(t *testing.T) {
	p := createPatrolProgram(t, "adaptive", program.StateActive)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &fakeHerdrClient{tab: herdr.Tab{ID: "tab-1", RootPaneID: "pane-1"}}
	installPatrolFakes(t, client)
	runningCalls := 0
	patrolIsRunning = func(string) (bool, error) {
		runningCalls++
		return runningCalls > 1, nil
	}
	patrolReadState = func(slug string) (patrol.State, error) {
		return patrol.State{
			Schema: patrol.SchemaVersion, Version: 1, ProgramSlug: slug,
			Status: patrol.StatusRunning, PID: 77, Reasons: []patrol.Reason{},
		}, nil
	}

	out, err := runProgramCommand(t, "patrol", "start", p.Slug, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got programPatrolStartOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Program != p.Slug || !got.Running || got.Adopted {
		t.Fatalf("start output = %+v", got)
	}
	if want := []fakeCreatedTab{{
		workspace: "workspace-1", cwd: p.Repo, label: "relay-patrol:" + p.Slug,
	}}; !reflect.DeepEqual(client.created, want) {
		t.Fatalf("created tabs = %#v, want %#v", client.created, want)
	}
	if want := []fakePaneCommand{{
		pane: "pane-1", command: "relay program patrol run '" + p.Slug + "'",
	}}; !reflect.DeepEqual(client.runPane, want) {
		t.Fatalf("pane commands = %#v, want %#v", client.runPane, want)
	}
	if len(client.renamed) != 0 {
		t.Fatalf("patrol tab launched an agent rename: %+v", client.renamed)
	}

	client.created = nil
	client.runPane = nil
	patrolIsRunning = func(string) (bool, error) { return true, nil }
	out, err = runProgramCommand(t, "patrol", "start", p.Slug, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Adopted || len(client.created) != 0 || len(client.runPane) != 0 {
		t.Fatalf("adopt output/client = %+v / %+v / %+v", got, client.created, client.runPane)
	}
}

func TestProgramPatrolStartOutsideHerdrFailsClosed(t *testing.T) {
	p := createPatrolProgram(t, "manual", program.StateDraft)
	t.Setenv("HERDR_ENV", "")
	t.Setenv("HERDR_WORKSPACE_ID", "")
	client := &fakeHerdrClient{}
	installPatrolFakes(t, client)
	patrolIsRunning = func(string) (bool, error) { return false, nil }

	_, err := runProgramCommand(t, "patrol", "start", p.Slug)
	if err == nil || !strings.Contains(err.Error(), "HERDR_ENV=1") ||
		!strings.Contains(err.Error(), "start or attach Herdr") {
		t.Fatalf("start outside Herdr error = %v", err)
	}
	if strings.Contains(err.Error(), "run manually") {
		t.Fatalf("start error offers a non-Herdr fallback: %v", err)
	}
	if len(client.created) != 0 {
		t.Fatalf("outside start created tabs: %+v", client.created)
	}
}

func TestProgramPatrolStartWithoutHerdrBinaryFailsClosed(t *testing.T) {
	p := createPatrolProgram(t, "no-binary", program.StateActive)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &fakeHerdrClient{}
	installPatrolFakes(t, client)
	herdrAvailable = func() bool { return false }
	patrolIsRunning = func(string) (bool, error) { return false, nil }

	_, err := runProgramCommand(t, "patrol", "start", p.Slug)
	if err == nil || !strings.Contains(err.Error(), "herdr binary is not on PATH") {
		t.Fatalf("start without the Herdr binary error = %v", err)
	}
	if len(client.created) != 0 {
		t.Fatalf("start without Herdr created tabs: %+v", client.created)
	}
}

func installPatrolFakes(t *testing.T, client *fakeHerdrClient) {
	t.Helper()
	previousClient := newHerdrClient
	previousPatrolClient := newPatrolHerdrClient
	previousAvailable := herdrAvailable
	previousRunning := patrolIsRunning
	previousRead := patrolReadState
	previousRun := patrolRunLoop
	previousNow := patrolNow
	previousSleep := patrolSleep
	newHerdrClient = func() herdrRuntimeClient { return client }
	newPatrolHerdrClient = func(context.Context) herdrRuntimeClient { return client }
	herdrAvailable = func() bool { return true }
	now := time.Unix(100, 0)
	patrolNow = func() time.Time { return now }
	patrolSleep = func(duration time.Duration) { now = now.Add(duration) }
	t.Cleanup(func() {
		newHerdrClient = previousClient
		newPatrolHerdrClient = previousPatrolClient
		herdrAvailable = previousAvailable
		patrolIsRunning = previousRunning
		patrolReadState = previousRead
		patrolRunLoop = previousRun
		patrolNow = previousNow
		patrolSleep = previousSleep
	})
}

func TestProgramPatrolRunUsesPatrolHerdrClientAndLiveDoorbell(t *testing.T) {
	p := createPatrolProgram(t, "with-herdr", program.StateActive)
	client := &fakeHerdrClient{}
	installPatrolFakes(t, client)
	patrolRunLoop = func(_ context.Context, slug string, options patrol.Options) error {
		if slug != p.Slug || options.Agents != client || options.Notifier != client {
			t.Fatalf("available Herdr options = %+v", options)
		}
		runner, ok := options.Turns.(liveTLTurnRunner)
		if !ok || runner.client != client {
			t.Fatalf("patrol turn runner = %#v, want live tech lead runner using the Herdr client", options.Turns)
		}
		return nil
	}
	patrolReadState = func(slug string) (patrol.State, error) {
		return patrol.State{
			Schema: patrol.SchemaVersion, Version: 1, ProgramSlug: slug,
			Status: patrol.StatusStopped, Reasons: []patrol.Reason{},
		}, nil
	}

	if _, err := runProgramCommand(t, "patrol", "run", p.Slug); err != nil {
		t.Fatal(err)
	}
}

// The patrol pane is the patrol's log: the foreground process hands its own
// stdout and stderr to the loop, and nothing is written to a file.
func TestProgramPatrolRunGivesTheLoopItsPaneWriters(t *testing.T) {
	p := createPatrolProgram(t, "pane-events", program.StateActive)
	client := &fakeHerdrClient{}
	installPatrolFakes(t, client)
	patrolRunLoop = func(_ context.Context, _ string, options patrol.Options) error {
		if options.Out == nil || options.Err == nil {
			t.Fatalf("patrol options carry no pane writers: %+v", options)
		}
		fmt.Fprintln(options.Out, "2026-09-01T04:45:00Z patrol started program=pane-events")
		fmt.Fprintln(options.Err, "2026-09-01T04:45:00Z warning: TL wake absent program=pane-events")
		return nil
	}
	patrolReadState = func(slug string) (patrol.State, error) {
		return patrol.State{
			Schema: patrol.SchemaVersion, Version: 1, ProgramSlug: slug,
			Status: patrol.StatusStopped, Reasons: []patrol.Reason{},
		}, nil
	}

	output, err := runProgramCommand(t, "patrol", "run", p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"patrol started program=pane-events",
		"warning: TL wake absent program=pane-events",
		"Patrol stopped for pane-events",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("patrol run output %q is missing %q", output, want)
		}
	}
}

func TestProgramPatrolRunWithoutHerdrFailsClosed(t *testing.T) {
	tests := []struct {
		name      string
		available bool
		agentErr  error
		want      string
	}{
		{name: "missing binary", want: "herdr binary is not on PATH"},
		{
			name: "unreachable server", available: true,
			agentErr: errors.New("connection refused"), want: "running Herdr server",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p := createPatrolProgram(t, "no-herdr", program.StateActive)
			client := &fakeHerdrClient{agentErr: test.agentErr}
			installPatrolFakes(t, client)
			herdrAvailable = func() bool { return test.available }
			runCalled := false
			patrolRunLoop = func(context.Context, string, patrol.Options) error {
				runCalled = true
				return nil
			}

			_, err := runProgramCommand(t, "patrol", "run", p.Slug)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("patrol run error = %v", err)
			}
			if runCalled {
				t.Fatal("patrol run loop started without Herdr")
			}
		})
	}
}

func TestProgramPatrolStartAndRunRejectAgentsWithoutNamedSessions(t *testing.T) {
	p := createPatrolProgram(t, "codex-patrol", program.StateActive)
	p.Agent = "codex"
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{}
	installPatrolFakes(t, client)
	patrolIsRunning = func(string) (bool, error) { return false, nil }
	runCalled := false
	patrolRunLoop = func(context.Context, string, patrol.Options) error {
		runCalled = true
		return nil
	}

	for _, args := range [][]string{
		{"patrol", "start", p.Slug},
		{"patrol", "run", p.Slug},
	} {
		_, err := runProgramCommand(t, args...)
		if err == nil ||
			!strings.Contains(err.Error(), "codex") ||
			!strings.Contains(err.Error(), "named sessions") ||
			!strings.Contains(err.Error(), "copilot") ||
			!strings.Contains(err.Error(), "claude") {
			t.Fatalf("%v error = %v", args, err)
		}
	}
	if runCalled {
		t.Fatal("unsupported patrol agent reached run loop")
	}
}

func TestProgramPatrolStatusAndStopUseAuthoritativeLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPatrolFakes(t, client)
	stateReads := 0
	patrolReadState = func(slug string) (patrol.State, error) {
		stateReads++
		return patrol.State{
			Schema: patrol.SchemaVersion, Version: 1, ProgramSlug: slug,
			Status: patrol.StatusRunning, PID: 4321, RelayVersion: "older",
			Reasons: []patrol.Reason{},
		}, nil
	}
	patrolIsRunning = func(string) (bool, error) { return false, nil }

	out, err := runProgramCommand(t, "patrol", "status", "adaptive", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var status programPatrolStatusOutput
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatal(err)
	}
	if status.Running || status.Status != "not-running" ||
		status.State == nil || !strings.Contains(status.Warning, "differs") {
		t.Fatalf("stale status = %+v", status)
	}

	previousSignal := patrolSignal
	signalCalls := []int{}
	patrolSignal = func(pid int, _ os.Signal) error {
		signalCalls = append(signalCalls, pid)
		return nil
	}
	t.Cleanup(func() { patrolSignal = previousSignal })
	readsBeforeStop := stateReads
	out, err = runProgramCommand(t, "patrol", "stop", "adaptive")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "not running") || len(signalCalls) != 0 || stateReads != readsBeforeStop {
		t.Fatalf("dead stop = output %q signals %v stateReads %d", out, signalCalls, stateReads)
	}

	runningCalls := 0
	patrolIsRunning = func(string) (bool, error) {
		runningCalls++
		return runningCalls < 3, nil
	}
	out, err = runProgramCommand(t, "patrol", "stop", "adaptive")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stopped") || !reflect.DeepEqual(signalCalls, []int{4321}) {
		t.Fatalf("live stop = output %q signals %v", out, signalCalls)
	}
}

func TestProgramPatrolStatusRecoversFromCorruptState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPatrolFakes(t, client)
	patrolIsRunning = func(string) (bool, error) { return false, nil }
	patrolReadState = func(string) (patrol.State, error) {
		return patrol.State{}, errors.New("parse patrol state: invalid JSON")
	}

	out, err := runProgramCommand(t, "patrol", "status", "corrupt", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var status programPatrolStatusOutput
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatal(err)
	}
	if status.Running || status.Status != "not-running" ||
		!strings.Contains(status.Warning, "invalid JSON") {
		t.Fatalf("corrupt status = %+v", status)
	}
}

func TestProgramPatrolStatusShowsDegradedTLDoorbell(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPatrolFakes(t, client)
	patrolIsRunning = func(string) (bool, error) { return true, nil }
	patrolReadState = func(slug string) (patrol.State, error) {
		return patrol.State{
			Schema: patrol.SchemaVersion, Version: 1, ProgramSlug: slug,
			Status: patrol.StatusRunning, Reasons: []patrol.Reason{},
			TLPresent: false, Warning: "Herdr doorbell unavailable",
		}, nil
	}

	out, err := runProgramCommand(t, "patrol", "status", "degraded")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Patrol: running") ||
		!strings.Contains(out, "TL present: false") ||
		!strings.Contains(out, "Warning: Herdr doorbell unavailable") {
		t.Fatalf("degraded status output = %q", out)
	}
}

func TestProgramPatrolStatusWarnsForAgentWithoutNamedSessions(t *testing.T) {
	p := createPatrolProgram(t, "codex-status", program.StateActive)
	p.Agent = "codex"
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{}
	installPatrolFakes(t, client)
	patrolIsRunning = func(string) (bool, error) { return true, nil }
	patrolReadState = func(slug string) (patrol.State, error) {
		return patrol.State{
			Schema: patrol.SchemaVersion, Version: 1, ProgramSlug: slug,
			Status: patrol.StatusRunning, Reasons: []patrol.Reason{}, TLPresent: true,
		}, nil
	}

	out, err := runProgramCommand(t, "patrol", "status", p.Slug, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var status programPatrolStatusOutput
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatal(err)
	}
	if status.State == nil || status.State.TLPresent ||
		!strings.Contains(status.Warning, "codex") ||
		!strings.Contains(status.Warning, "named sessions") {
		t.Fatalf("unsupported agent status = %+v", status)
	}
}

func TestProgramPatrolStatusReportsFailureDetailWhenNotRunning(t *testing.T) {
	tests := []struct {
		name       string
		state      patrol.State
		wantStatus string
		wantText   []string
	}{
		{
			name: "failed patrol",
			state: patrol.State{
				Schema: patrol.SchemaVersion, Version: 1, ProgramSlug: "adaptive",
				Status: patrol.StatusFailed, Reasons: []patrol.Reason{},
				Error: "build patrol snapshot: permission denied", ConsecutiveErrors: 3,
			},
			wantStatus: "failed",
			wantText:   []string{"Patrol: failed", "Error: build patrol snapshot: permission denied"},
		},
		{
			name: "stopped patrol",
			state: patrol.State{
				Schema: patrol.SchemaVersion, Version: 1, ProgramSlug: "adaptive",
				Status: patrol.StatusStopped, Reasons: []patrol.Reason{},
				StopReason: "program archived", Warning: "Herdr doorbell unavailable",
			},
			wantStatus: "stopped",
			wantText: []string{
				"Patrol: stopped", "Stop reason: program archived",
				"Warning: Herdr doorbell unavailable",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			client := &fakeHerdrClient{}
			installPatrolFakes(t, client)
			patrolIsRunning = func(string) (bool, error) { return false, nil }
			patrolReadState = func(string) (patrol.State, error) { return test.state, nil }

			out, err := runProgramCommand(t, "patrol", "status", "adaptive", "--json")
			if err != nil {
				t.Fatal(err)
			}
			var status programPatrolStatusOutput
			if err := json.Unmarshal([]byte(out), &status); err != nil {
				t.Fatal(err)
			}
			if status.Running {
				t.Fatalf("status = %+v, want a dead patrol", status)
			}
			if status.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q", status.Status, test.wantStatus)
			}
			if status.Error != test.state.Error || status.StopReason != test.state.StopReason {
				t.Fatalf("status detail = %+v", status)
			}

			text, err := runProgramCommand(t, "patrol", "status", "adaptive")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range test.wantText {
				if !strings.Contains(text, want) {
					t.Fatalf("status text %q missing %q", text, want)
				}
			}
		})
	}
}

func TestProgramPatrolStatusStaysNotRunningWithoutFailureDetail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPatrolFakes(t, client)
	patrolIsRunning = func(string) (bool, error) { return false, nil }
	patrolReadState = func(slug string) (patrol.State, error) {
		return patrol.State{
			Schema: patrol.SchemaVersion, Version: 1, ProgramSlug: slug,
			Status: patrol.StatusRunning, PID: 4321, Reasons: []patrol.Reason{},
		}, nil
	}

	out, err := runProgramCommand(t, "patrol", "status", "adaptive", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var status programPatrolStatusOutput
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatal(err)
	}
	if status.Running || status.Status != "not-running" {
		t.Fatalf("stale running state = %+v", status)
	}
}

func TestProgramPatrolSupportsNamedClaudeSessionsWithoutHeadlessTurns(t *testing.T) {
	p := createPatrolProgram(t, "claude-patrol", program.StateActive)
	p.Agent = "claude"
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &fakeHerdrClient{}
	installPatrolFakes(t, client)
	patrolIsRunning = func(string) (bool, error) { return true, nil }
	patrolReadState = func(slug string) (patrol.State, error) {
		return patrol.State{
			Schema: patrol.SchemaVersion, Version: 1, ProgramSlug: slug,
			Status: patrol.StatusRunning, PID: 77, Reasons: []patrol.Reason{},
		}, nil
	}
	patrolRunLoop = func(_ context.Context, _ string, options patrol.Options) error {
		if _, ok := options.Turns.(liveTLTurnRunner); !ok {
			t.Fatalf("Claude patrol runner = %#v, want live tech lead doorbell", options.Turns)
		}
		return nil
	}

	for _, args := range [][]string{
		{"patrol", "start", p.Slug},
		{"patrol", "run", p.Slug},
	} {
		if _, err := runProgramCommand(t, args...); err != nil {
			t.Fatalf("%v rejected named Claude session: %v", args, err)
		}
	}
}

func TestProgramPatrolStatusReportsLiveDoorbellMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &fakeHerdrClient{}
	installPatrolFakes(t, client)
	patrolIsRunning = func(string) (bool, error) { return true, nil }
	patrolReadState = func(slug string) (patrol.State, error) {
		return patrol.State{
			Schema: patrol.SchemaVersion, Version: 1, ProgramSlug: slug,
			Status: patrol.StatusRunning, Reasons: []patrol.Reason{}, TLPresent: true,
			LastTurnStatus: "failed", LastTurnSessionID: "session-1",
			LastTurnLogPath:   "/home/u/.relay/run/adaptive/turns/20260827T101530Z-session-1.log",
			LastTurnStartedAt: "2026-08-27T10:15:30Z", LastTurnEndedAt: "2026-08-27T10:17:00Z",
			LastTurnError: "exit status 2", TurnFailures: 2,
		}, nil
	}

	out, err := runProgramCommand(t, "patrol", "status", "adaptive")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Last TL wake: failed at 2026-08-27T10:17:00Z (session session-1)",
		"Legacy turn log: /home/u/.relay/run/adaptive/turns/20260827T101530Z-session-1.log",
		"TL wake error: exit status 2",
		"Consecutive TL wake failures: 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("patrol status output is missing %q:\n%s", want, out)
		}
	}

	jsonOut, err := runProgramCommand(t, "patrol", "status", "adaptive", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var status programPatrolStatusOutput
	if err := json.Unmarshal([]byte(jsonOut), &status); err != nil {
		t.Fatal(err)
	}
	if status.State == nil || status.State.LastTurnSessionID != "session-1" ||
		status.State.TurnFailures != 2 {
		t.Fatalf("patrol status JSON turn metadata = %+v", status.State)
	}
}

// A patrol that never confirms itself is usually failing for a concrete
// reason. The verification loop tolerates a state file that does not exist yet,
// but it must keep any real read failure and name it in the timeout instead of
// leaving the CEO with a bare "did not report running".
func TestProgramPatrolStartTimeoutReportsTheLastStateReadFailure(t *testing.T) {
	p := createPatrolProgram(t, "corrupt-state", program.StateActive)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &fakeHerdrClient{tab: herdr.Tab{ID: "tab-7", RootPaneID: "pane-7"}}
	installPatrolFakes(t, client)
	runningCalls := 0
	patrolIsRunning = func(string) (bool, error) {
		runningCalls++
		return runningCalls > 1, nil
	}
	corrupt := errors.New("parse patrol state state.json: unexpected end of JSON input")
	reads := 0
	patrolReadState = func(string) (patrol.State, error) {
		reads++
		if reads == 1 {
			return patrol.State{}, os.ErrNotExist
		}
		return patrol.State{}, corrupt
	}

	_, err := runProgramCommand(t, "patrol", "start", p.Slug)
	if err == nil {
		t.Fatal("start with an unreadable patrol state succeeded")
	}
	if !errors.Is(err, corrupt) {
		t.Errorf("start error %v does not wrap the state read failure", err)
	}
	for _, want := range []string{"did not report running within 10s", corrupt.Error(), "tab-7"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("start error %q is missing %q", err, want)
		}
	}
}

// A state file that has not appeared yet is normal during startup and must not
// be reported as the cause of anything.
func TestProgramPatrolStartToleratesAMissingStateFileWhileStarting(t *testing.T) {
	p := createPatrolProgram(t, "slow-start", program.StateActive)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &fakeHerdrClient{tab: herdr.Tab{ID: "tab-8", RootPaneID: "pane-8"}}
	installPatrolFakes(t, client)
	runningCalls := 0
	patrolIsRunning = func(string) (bool, error) {
		runningCalls++
		return runningCalls > 1, nil
	}
	reads := 0
	patrolReadState = func(slug string) (patrol.State, error) {
		reads++
		if reads < 3 {
			return patrol.State{}, os.ErrNotExist
		}
		return patrol.State{
			Schema: patrol.SchemaVersion, Version: 1, ProgramSlug: slug,
			Status: patrol.StatusRunning, PID: 91, Reasons: []patrol.Reason{},
		}, nil
	}

	out, err := runProgramCommand(t, "patrol", "start", p.Slug, "--json")
	if err != nil {
		t.Fatalf("start after a slow state file: %v", err)
	}
	var got programPatrolStartOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Running || got.Adopted || got.State.PID != 91 {
		t.Fatalf("start output = %+v", got)
	}
}

// The timeout stays clean when nothing failed: a patrol that simply never
// reached running must not invent a read failure.
func TestProgramPatrolStartTimeoutStaysCleanWithoutAReadFailure(t *testing.T) {
	p := createPatrolProgram(t, "never-running", program.StateActive)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace-1")
	client := &fakeHerdrClient{tab: herdr.Tab{ID: "tab-9", RootPaneID: "pane-9"}}
	installPatrolFakes(t, client)
	runningCalls := 0
	patrolIsRunning = func(string) (bool, error) {
		runningCalls++
		return runningCalls > 1, nil
	}
	patrolReadState = func(slug string) (patrol.State, error) {
		return patrol.State{
			Schema: patrol.SchemaVersion, Version: 1, ProgramSlug: slug,
			Status: patrol.StatusStopped, PID: 92, Reasons: []patrol.Reason{},
		}, nil
	}

	_, err := runProgramCommand(t, "patrol", "start", p.Slug)
	if err == nil {
		t.Fatal("start without a running patrol succeeded")
	}
	if !strings.Contains(err.Error(), "did not report running within 10s") ||
		strings.Contains(err.Error(), "state read failed") {
		t.Fatalf("start timeout error = %v", err)
	}
}
