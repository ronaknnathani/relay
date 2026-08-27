package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/patrol"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programturn"
)

type fakeTurn struct {
	requests []agent.HeadlessTurnRequest
	result   agent.HeadlessTurnResult
	err      error
	hook     func()
	mu       sync.Mutex
}

func (f *fakeTurn) run(
	_ context.Context,
	request agent.HeadlessTurnRequest,
	_ agent.HeadlessTurnDeps,
) (agent.HeadlessTurnResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	hook := f.hook
	result, err := f.result, f.err
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if result.SessionID == "" {
		result.SessionID = request.SessionID
	}
	if result.LogPath == "" {
		result.LogPath = request.LogPath
	}
	if result.StartedAt.IsZero() {
		result.StartedAt = time.Date(2026, 8, 27, 10, 15, 30, 0, time.UTC)
	}
	if result.EndedAt.IsZero() {
		result.EndedAt = result.StartedAt.Add(90 * time.Second)
	}
	return result, err
}

func (f *fakeTurn) setResult(result agent.HeadlessTurnResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result = result
}

func (f *fakeTurn) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func installCTOTurnFakes(t *testing.T, client *fakeHerdrClient, turn *fakeTurn) {
	t.Helper()
	previousClient := newHerdrClient
	previousAvailable := herdrAvailable
	previousRun := runHeadlessTurn
	previousID := newTurnSessionID
	previousNow := turnNow
	newHerdrClient = func() herdrRuntimeClient { return client }
	herdrAvailable = func() bool { return true }
	runHeadlessTurn = turn.run
	newTurnSessionID = func() (string, error) { return "session-1", nil }
	turnNow = func() time.Time { return time.Date(2026, 8, 27, 10, 15, 30, 0, time.UTC) }
	t.Cleanup(func() {
		newHerdrClient = previousClient
		herdrAvailable = previousAvailable
		runHeadlessTurn = previousRun
		newTurnSessionID = previousID
		turnNow = previousNow
	})
}

func idleCTOAgents(slug string) []herdr.Agent {
	return []herdr.Agent{{
		PaneID: "cto-" + slug, TerminalTitle: "relay:program:" + slug + " - GitHub Copilot",
		Status: herdr.StatusIdle,
	}}
}

func TestProgramCTOTurnRunsAFreshBoundedSessionAndRecordsMetadata(t *testing.T) {
	p := createPatrolProgram(t, "adaptive", program.StateActive)
	saveProgramTestConfig(t)
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{idleCTOAgents(p.Slug)}}
	turn := &fakeTurn{result: agent.HeadlessTurnResult{
		StartedAt: time.Date(2026, 8, 27, 10, 15, 30, 0, time.UTC),
		EndedAt:   time.Date(2026, 8, 27, 10, 17, 0, 0, time.UTC),
	}}
	installCTOTurnFakes(t, client, turn)

	out, err := runProgramCommand(t, "cto", "turn", p.Slug, "--json", "--fingerprint", "fp-1")
	if err != nil {
		t.Fatal(err)
	}
	var result programCTOTurnResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if result.Status != string(programturn.StatusSucceeded) ||
		result.SessionID != "session-1" || result.Fingerprint != "fp-1" || result.ExitCode != 0 {
		t.Fatalf("turn result = %+v", result)
	}
	wantLog := programturn.LogPath(p.Slug, turnNow(), "session-1")
	if result.LogPath != wantLog {
		t.Fatalf("log path = %q, want %q", result.LogPath, wantLog)
	}

	if turn.calls() != 1 {
		t.Fatalf("headless turns = %d, want 1", turn.calls())
	}
	request := turn.requests[0]
	if request.Repo != p.Repo || request.SessionID != "session-1" || request.LogPath != wantLog {
		t.Fatalf("headless request = %+v", request)
	}
	if request.ProgramDir != program.ProgramDir(program.ActiveDir(), p.Slug) {
		t.Fatalf("program dir = %q", request.ProgramDir)
	}
	if request.Agent == nil || request.Agent.Name() != "copilot" {
		t.Fatalf("headless agent = %+v", request.Agent)
	}
	for _, want := range []string{
		`Run the relay "cto" skill for program ` + p.Slug,
		"relay program message list " + p.Slug + " --json",
		"relay program status " + p.Slug + " --json",
		"relay program tick " + p.Slug + " --json",
		"ONE bounded automated CTO turn",
		"never typed into",
		"resolve a decision",
		"hold or release a program",
		"publish a contract version",
		"invoke stack-ship",
		"wait, sleep, poll, loop",
		"RELAY_AUTOMATED_TURN=1",
		"concise digest",
		"signed automatically as cto-automated:<session-prefix>",
		"Do not pass --by,",
	} {
		if !strings.Contains(request.Prompt, want) {
			t.Errorf("bounded turn prompt is missing %q", want)
		}
	}
	if strings.Contains(request.Prompt, "--resume") {
		t.Error("bounded turn prompt resumes a session")
	}

	// The turn is recorded in runtime state, never in the program directory.
	record, ok, err := programturn.Latest(p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || record.SessionID != "session-1" || record.Status != programturn.StatusSucceeded ||
		record.Fingerprint != "fp-1" || record.LogPath != wantLog {
		t.Fatalf("recorded turn = %+v ok=%t", record, ok)
	}
	manifest, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.UpdatedAt != p.UpdatedAt {
		t.Fatalf("bounded turn transport mutated the program manifest: %q -> %q", p.UpdatedAt, manifest.UpdatedAt)
	}
	if len(client.prompted) != 0 || len(client.focused) != 0 || len(client.runPane) != 0 {
		t.Fatalf("bounded turn touched the CTO pane: %+v %+v %+v",
			client.prompted, client.focused, client.runPane)
	}
}

func TestProgramCTOTurnSkipsWhenAnotherWriterHoldsTheLock(t *testing.T) {
	p := createPatrolProgram(t, "adaptive", program.StateActive)
	saveProgramTestConfig(t)
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{idleCTOAgents(p.Slug)}}
	turn := &fakeTurn{}
	installCTOTurnFakes(t, client, turn)

	writer, err := programturn.AcquireWriter(p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Release() }()

	out, err := runProgramCommand(t, "cto", "turn", p.Slug, "--json")
	if err != nil {
		t.Fatalf("a contended bounded turn returned an error: %v", err)
	}
	var result programCTOTurnResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != string(programturn.StatusSkipped) ||
		!strings.Contains(result.Reason, "already running") {
		t.Fatalf("contended turn result = %+v", result)
	}
	if turn.calls() != 0 {
		t.Fatal("a contended bounded turn started an agent")
	}
}

// Concurrent bounded turns must not both act: exactly one runs and the other
// skips immediately instead of queueing a duplicate governance turn.
func TestProgramCTOTurnAdmitsExactlyOneConcurrentRun(t *testing.T) {
	p := createPatrolProgram(t, "adaptive", program.StateActive)
	saveProgramTestConfig(t)
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{idleCTOAgents(p.Slug)}}
	release := make(chan struct{})
	turn := &fakeTurn{hook: func() { <-release }}
	installCTOTurnFakes(t, client, turn)

	results := make(chan programCTOTurnResult, 2)
	errs := make(chan error, 2)
	started := make(chan struct{})
	go func() {
		close(started)
		result, err := runProgramCTOTurn(context.Background(), p.Slug, programCTOTurnOptions{})
		results <- result
		errs <- err
	}()
	<-started
	deadline := time.Now().Add(3 * time.Second)
	for turn.calls() == 0 {
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("the first bounded turn never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	second, err := runProgramCTOTurn(context.Background(), p.Slug, programCTOTurnOptions{})
	if err != nil {
		t.Fatalf("the contended bounded turn errored: %v", err)
	}
	if second.Status != string(programturn.StatusSkipped) {
		t.Fatalf("second concurrent turn = %+v, want skipped", second)
	}
	close(release)
	first := <-results
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if first.Status != string(programturn.StatusSucceeded) {
		t.Fatalf("first concurrent turn = %+v", first)
	}
	if turn.calls() != 1 {
		t.Fatalf("agent runs = %d, want exactly 1", turn.calls())
	}
}

func TestProgramCTOTurnGatesOnTheExactLiveCTOStatus(t *testing.T) {
	for _, test := range []struct {
		name   string
		agents []herdr.Agent
		reason string
	}{
		{name: "absent", agents: []herdr.Agent{}, reason: "no live CEO-facing CTO"},
		{name: "another program", agents: idleCTOAgents("other"), reason: "no live CEO-facing CTO"},
		{name: "working", agents: statusCTOAgents("adaptive", herdr.StatusWorking), reason: "is working"},
		{name: "blocked", agents: statusCTOAgents("adaptive", herdr.StatusBlocked), reason: "is blocked"},
		{name: "unknown", agents: statusCTOAgents("adaptive", herdr.StatusUnknown), reason: "is unknown"},
		{name: "duplicate idle owners", agents: []herdr.Agent{
			{PaneID: "p1", TerminalTitle: "relay:program:adaptive - GitHub Copilot", Status: herdr.StatusIdle},
			{PaneID: "p2", TerminalTitle: "relay:program:adaptive", Status: herdr.StatusIdle},
		}, reason: "2 live CTO sessions (panes p1, p2)"},
		{name: "duplicate mixed owners", agents: []herdr.Agent{
			{PaneID: "p1", TerminalTitle: "relay:program:adaptive", Status: herdr.StatusIdle},
			{PaneID: "near", TerminalTitle: "relay:program:adaptive-other", Status: herdr.StatusIdle},
			{PaneID: "p2", TerminalTitle: "relay:program:adaptive - GitHub Copilot", Status: herdr.StatusWorking},
		}, reason: "2 live CTO sessions (panes p1, p2)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := createPatrolProgram(t, "adaptive", program.StateActive)
			saveProgramTestConfig(t)
			client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{test.agents}}
			turn := &fakeTurn{}
			installCTOTurnFakes(t, client, turn)

			result, err := runProgramCTOTurn(context.Background(), p.Slug, programCTOTurnOptions{})
			if err != nil {
				t.Fatalf("gated turn returned an error: %v", err)
			}
			if result.Status != string(programturn.StatusSkipped) ||
				!strings.Contains(result.Reason, test.reason) {
				t.Fatalf("gated turn = %+v, want skipped containing %q", result, test.reason)
			}
			if turn.calls() != 0 {
				t.Fatal("a gated bounded turn started an agent")
			}
			record, ok, err := programturn.Latest(p.Slug)
			if err != nil {
				t.Fatal(err)
			}
			if !ok || record.Status != programturn.StatusSkipped {
				t.Fatalf("skip was not recorded: %+v ok=%t", record, ok)
			}
		})
	}

	for _, status := range []herdr.Status{herdr.StatusIdle, herdr.StatusDone} {
		t.Run(string(status), func(t *testing.T) {
			p := createPatrolProgram(t, "adaptive", program.StateActive)
			saveProgramTestConfig(t)
			client := &fakeHerdrClient{
				agentResponses: [][]herdr.Agent{statusCTOAgents("adaptive", status)},
			}
			turn := &fakeTurn{}
			installCTOTurnFakes(t, client, turn)
			result, err := runProgramCTOTurn(context.Background(), p.Slug, programCTOTurnOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != string(programturn.StatusSucceeded) || turn.calls() != 1 {
				t.Fatalf("%s CTO = %+v with %d runs", status, result, turn.calls())
			}
		})
	}
}

// A near-miss program title such as "relay:program:adaptive-other" belongs to
// a different program and must neither be adopted nor counted as a duplicate.
func TestProgramCTOTurnRunsBesideANearMissProgramTitle(t *testing.T) {
	p := createPatrolProgram(t, "adaptive", program.StateActive)
	saveProgramTestConfig(t)
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{
		{PaneID: "near", TerminalTitle: "relay:program:adaptive-other", Status: herdr.StatusIdle},
		{PaneID: "cto", TerminalTitle: "relay:program:adaptive - GitHub Copilot", Status: herdr.StatusIdle},
	}}}
	turn := &fakeTurn{}
	installCTOTurnFakes(t, client, turn)

	result, err := runProgramCTOTurn(context.Background(), p.Slug, programCTOTurnOptions{})
	if err != nil {
		t.Fatalf("bounded turn beside a near-miss title: %v", err)
	}
	if result.Status != string(programturn.StatusSucceeded) {
		t.Fatalf("bounded turn = %+v, want succeeded", result)
	}
	if turn.calls() != 1 {
		t.Fatalf("agent runs = %d, want exactly 1", turn.calls())
	}
}

func statusCTOAgents(slug string, status herdr.Status) []herdr.Agent {
	agents := idleCTOAgents(slug)
	agents[0].Status = status
	return agents
}

func TestProgramCTOTurnRecordsFailureAndTimeout(t *testing.T) {
	for _, test := range []struct {
		name   string
		result agent.HeadlessTurnResult
		status programturn.Status
	}{
		{
			name:   "non-zero exit",
			result: agent.HeadlessTurnResult{ExitCode: 2, Error: "exit status 2"},
			status: programturn.StatusFailed,
		},
		{
			name:   "timeout",
			result: agent.HeadlessTurnResult{ExitCode: -1, TimedOut: true, Error: "10m limit"},
			status: programturn.StatusTimedOut,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := createPatrolProgram(t, "adaptive", program.StateActive)
			saveProgramTestConfig(t)
			client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{idleCTOAgents(p.Slug)}}
			turn := &fakeTurn{result: test.result}
			installCTOTurnFakes(t, client, turn)

			result, err := runProgramCTOTurn(context.Background(), p.Slug, programCTOTurnOptions{})
			if err == nil {
				t.Fatal("a failed bounded turn returned no error")
			}
			if result.Status != string(test.status) {
				t.Fatalf("failed turn status = %q, want %q", result.Status, test.status)
			}
			record, ok, err := programturn.Latest(p.Slug)
			if err != nil {
				t.Fatal(err)
			}
			if !ok || record.Status != test.status || record.Error == "" {
				t.Fatalf("recorded failure = %+v ok=%t", record, ok)
			}
		})
	}
}

func TestProgramCTOTurnRejectsAgentsWithoutHeadlessTurns(t *testing.T) {
	for _, agentName := range []string{"claude", "codex"} {
		t.Run(agentName, func(t *testing.T) {
			p := createPatrolProgram(t, "adaptive", program.StateActive)
			p.Agent = agentName
			if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
				t.Fatal(err)
			}
			saveProgramTestConfig(t)
			client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{idleCTOAgents(p.Slug)}}
			turn := &fakeTurn{}
			installCTOTurnFakes(t, client, turn)

			_, err := runProgramCTOTurn(context.Background(), p.Slug, programCTOTurnOptions{})
			if err == nil {
				t.Fatalf("%s ran a bounded turn", agentName)
			}
			for _, want := range []string{agentName, "copilot", "bounded"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s error %q is missing %q", agentName, err, want)
				}
			}
			if turn.calls() != 0 {
				t.Fatalf("%s started an agent process", agentName)
			}
		})
	}
}

func TestProgramCTOTurnRejectsArchivedAndFinishedPrograms(t *testing.T) {
	p := createPatrolProgram(t, "adaptive", program.StateActive)
	saveProgramTestConfig(t)
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{idleCTOAgents(p.Slug)}}
	turn := &fakeTurn{}
	installCTOTurnFakes(t, client, turn)
	if err := program.Archive(p.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCTOTurn(context.Background(), p.Slug, programCTOTurnOptions{}); err == nil {
		t.Fatal("an archived program ran a bounded turn")
	}
	if turn.calls() != 0 {
		t.Fatal("an archived program started an agent")
	}
}

func TestProgramCTOTurnRequiresHerdr(t *testing.T) {
	p := createPatrolProgram(t, "adaptive", program.StateActive)
	saveProgramTestConfig(t)
	client := &fakeHerdrClient{agentErr: errors.New("connection refused")}
	turn := &fakeTurn{}
	installCTOTurnFakes(t, client, turn)

	if _, err := runProgramCTOTurn(context.Background(), p.Slug, programCTOTurnOptions{}); err == nil ||
		!strings.Contains(err.Error(), "running Herdr server") {
		t.Fatalf("unreachable Herdr error = %v", err)
	}
	herdrAvailable = func() bool { return false }
	if _, err := runProgramCTOTurn(context.Background(), p.Slug, programCTOTurnOptions{}); err == nil ||
		!strings.Contains(err.Error(), "herdr binary is not on PATH") {
		t.Fatalf("missing Herdr binary error = %v", err)
	}
	if turn.calls() != 0 {
		t.Fatal("a bounded turn started without Herdr")
	}
}

// The bounded turn transport is the patrol's only wake path, so the patrol's
// TurnRunner must translate every command outcome without panicking or
// promoting a skip into a failure.
func TestBoundedCTOTurnRunnerMapsOutcomesForPatrol(t *testing.T) {
	p := createPatrolProgram(t, "adaptive", program.StateActive)
	saveProgramTestConfig(t)
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{idleCTOAgents(p.Slug)}}
	turn := &fakeTurn{}
	installCTOTurnFakes(t, client, turn)

	result, err := boundedCTOTurnRunner{}.RunTurn(context.Background(), patrol.TurnRequest{
		ProgramSlug: p.Slug, Fingerprint: "fp-1",
		Reasons: []patrol.Reason{{Code: "open-decision:d1", Text: "Decision d1 is awaiting resolution."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != patrol.TurnSucceeded || result.SessionID != "session-1" {
		t.Fatalf("mapped success = %+v", result)
	}
	if !strings.Contains(turn.requests[0].Prompt, "open-decision:d1") {
		t.Error("bounded turn prompt did not carry the patrol's attention reasons")
	}

	turn.result = agent.HeadlessTurnResult{ExitCode: 1, Error: "boom"}
	failed, err := boundedCTOTurnRunner{}.RunTurn(context.Background(), patrol.TurnRequest{
		ProgramSlug: p.Slug, Fingerprint: "fp-2",
	})
	if err != nil {
		t.Fatalf("a failed bounded turn broke the patrol: %v", err)
	}
	if failed.Status != patrol.TurnFailed || failed.Error == "" {
		t.Fatalf("mapped failure = %+v", failed)
	}

	writer, err := programturn.AcquireWriter(p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Release() }()
	skipped, err := boundedCTOTurnRunner{}.RunTurn(context.Background(), patrol.TurnRequest{
		ProgramSlug: p.Slug, Fingerprint: "fp-3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if skipped.Status != patrol.TurnSkipped {
		t.Fatalf("mapped skip = %+v", skipped)
	}
}

// The bounded turn writes only runtime state; program, project, and mailbox
// truth stay owned by the CLI commands the turn's agent runs.
func TestProgramCTOTurnWritesOnlyRuntimeState(t *testing.T) {
	p := createPatrolProgram(t, "adaptive", program.StateActive)
	saveProgramTestConfig(t)
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{idleCTOAgents(p.Slug)}}
	turn := &fakeTurn{}
	installCTOTurnFakes(t, client, turn)

	programDir := program.ProgramDir(program.ActiveDir(), p.Slug)
	before := snapshotDir(t, programDir)
	if _, err := runProgramCTOTurn(context.Background(), p.Slug, programCTOTurnOptions{}); err != nil {
		t.Fatal(err)
	}
	if after := snapshotDir(t, programDir); !equalSnapshots(before, after) {
		t.Fatalf("bounded turn transport changed the program directory:\n%v\n%v", before, after)
	}
	if _, err := os.Stat(programturn.StatePath(p.Slug)); err != nil {
		t.Fatalf("bounded turn did not record runtime state: %v", err)
	}
}

func snapshotDir(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func equalSnapshots(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for name, content := range left {
		if right[name] != content {
			return false
		}
	}
	return true
}

// A bounded turn is unattended: it must never prompt for configuration or write
// the shared Relay config, even when none exists yet.
func TestProgramCTOTurnNeverWritesOrPromptsForConfig(t *testing.T) {
	p := createPatrolProgram(t, "adaptive", program.StateActive)
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{idleCTOAgents(p.Slug)}}
	turn := &fakeTurn{}
	installCTOTurnFakes(t, client, turn)

	if _, err := os.Stat(config.Path()); !os.IsNotExist(err) {
		t.Fatalf("the fixture already has a config: %v", err)
	}
	if _, err := runProgramCTOTurn(context.Background(), p.Slug, programCTOTurnOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(config.Path()); !os.IsNotExist(err) {
		t.Fatalf("the bounded turn wrote the shared config: %v", err)
	}
	if turn.calls() != 1 {
		t.Fatalf("bounded turns = %d, want 1", turn.calls())
	}
}
