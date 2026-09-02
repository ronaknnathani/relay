package herdr

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// scriptedHerdr answers herdr invocations in order and records every command,
// so a test can assert exactly what Relay sent and in what order.
type scriptedHerdr struct {
	t         *testing.T
	responses []scriptedResponse
	index     int
	calls     [][]string
	inputs    [][]byte
}

type scriptedResponse struct {
	args   []string
	output string
	err    error
}

func (s *scriptedHerdr) client() *Client {
	client := newClientWithRunners(
		func(args ...string) ([]byte, error) { return s.run(nil, args...) },
		func(input []byte, args ...string) ([]byte, error) {
			s.inputs = append(s.inputs, input)
			return s.run(input, args...)
		},
	)
	client.promptSubmitGrace = 0
	client.promptTimeout = 0
	client.promptPollInterval = 0
	client.sleep = func(time.Duration) {}
	return client
}

func (s *scriptedHerdr) run(_ []byte, args ...string) ([]byte, error) {
	s.t.Helper()
	s.calls = append(s.calls, args)
	if s.index >= len(s.responses) {
		s.t.Fatalf("unexpected herdr command: %v", args)
	}
	response := s.responses[s.index]
	s.index++
	if response.args != nil && !reflect.DeepEqual(args, response.args) {
		s.t.Fatalf("herdr args = %#v, want %#v", args, response.args)
	}
	return []byte(response.output), response.err
}

func (s *scriptedHerdr) commands() []string {
	names := make([]string, 0, len(s.calls))
	for _, call := range s.calls {
		names = append(names, strings.Join(call, " "))
	}
	return names
}

const workerAgentJSON = `{"agent_status":"idle","state_change_seq":10,"pane_id":"w1:p2",` +
	`"tab_id":"w1:t2","workspace_id":"w1","terminal_id":"term_a",` +
	`"terminal_title_stripped":"relay:child - GitHub Copilot","cwd":"/repo",` +
	`"foreground_cwd":"/repo/worktree","agent_session":{"value":"native-123"}}`

const replacementAgentJSON = `{"agent_status":"idle","state_change_seq":3,"pane_id":"w1:p2",` +
	`"tab_id":"w1:t2","workspace_id":"w1","terminal_id":"term_b",` +
	`"terminal_title_stripped":"relay:other - GitHub Copilot","cwd":"/repo",` +
	`"agent_session":{"value":"native-999"}}`

func agentList(agents ...string) string {
	return `{"result":{"agents":[` + strings.Join(agents, ",") + `]}}`
}

func workerIdentity() SessionIdentity {
	return SessionIdentity{
		PaneID: "w1:p2", TabID: "w1:t2", WorkspaceID: "w1",
		TerminalID: "term_a", NativeSessionID: "native-123",
	}
}

func TestAgentListParsesTheTerminalID(t *testing.T) {
	client := NewClientWithRunner(func(...string) ([]byte, error) {
		return []byte(agentList(workerAgentJSON)), nil
	})
	agents, err := client.Agents()
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	if len(agents) != 1 || agents[0].TerminalID != "term_a" {
		t.Fatalf("agents = %#v, want the terminal id parsed", agents)
	}
	if !workerIdentity().Matches(agents[0]) {
		t.Fatalf("identity %v does not match the observed agent %#v", workerIdentity(), agents[0])
	}
}

func TestSessionIdentityRejectsAReusedPane(t *testing.T) {
	identity := workerIdentity()
	for name, agent := range map[string]Agent{
		"different native session": {
			PaneID: "w1:p2", TabID: "w1:t2", WorkspaceID: "w1",
			TerminalID: "term_a", NativeSessionID: "native-999",
		},
		"different terminal": {
			PaneID: "w1:p2", TabID: "w1:t2", WorkspaceID: "w1",
			TerminalID: "term_b", NativeSessionID: "native-123",
		},
		"different tab": {
			PaneID: "w1:p2", TabID: "w1:t9", WorkspaceID: "w1",
			TerminalID: "term_a", NativeSessionID: "native-123",
		},
		"different workspace": {
			PaneID: "w1:p2", TabID: "w1:t2", WorkspaceID: "w9",
			TerminalID: "term_a", NativeSessionID: "native-123",
		},
		"missing native session": {
			PaneID: "w1:p2", TabID: "w1:t2", WorkspaceID: "w1", TerminalID: "term_a",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if identity.Matches(agent) {
				t.Fatalf("identity matched a different session %#v", agent)
			}
		})
	}
	if (SessionIdentity{}).Matches(Agent{}) {
		t.Fatal("an empty identity matched an empty agent")
	}
}

func TestExitAgentConfirmsTheExactSessionIsGone(t *testing.T) {
	script := &scriptedHerdr{t: t, responses: []scriptedResponse{
		{args: []string{"agent", "list"}, output: agentList(workerAgentJSON)},
		{args: []string{"agent", "prompt", "w1:p2", "/exit"}, output: `{"result":{}}`},
		{args: []string{"agent", "list"}, output: agentList()},
		{args: []string{"agent", "list"}, output: agentList()},
	}}

	result, err := script.client().ExitAgent(workerIdentity())
	if err != nil {
		t.Fatalf("ExitAgent: %v", err)
	}
	if result.Outcome != ExitedNow || !result.PaneGone {
		t.Fatalf("result = %#v, want a confirmed exit with a free pane", result)
	}
	for _, call := range script.commands() {
		if strings.HasPrefix(call, "agent focus") {
			t.Fatalf("ExitAgent focused the pane: %v", script.commands())
		}
	}
}

func TestExitAgentPressesEnterWhenHerdrOnlyStagesTheCommand(t *testing.T) {
	script := &scriptedHerdr{t: t, responses: []scriptedResponse{
		{args: []string{"agent", "list"}, output: agentList(workerAgentJSON)},
		{args: []string{"agent", "prompt", "w1:p2", "/exit"}, output: `{"result":{}}`},
		{args: []string{"agent", "list"}, output: agentList(workerAgentJSON)},
		{
			args:   []string{"pane", "layout", "--pane", "w1:p2"},
			output: `{"result":{"layout":{"panes":[{"pane_id":"w1:p2","rect":{"width":120,"height":40}}]}}}`,
		},
		{
			args: []string{
				"terminal", "session", "control", "w1:p2", "--takeover",
				"--cols", "120", "--rows", "40",
			},
			output: "",
		},
		{args: []string{"agent", "list"}, output: agentList()},
		{args: []string{"agent", "list"}, output: agentList()},
	}}

	result, err := script.client().ExitAgent(workerIdentity())
	if err != nil {
		t.Fatalf("ExitAgent: %v", err)
	}
	if result.Outcome != ExitedNow || !result.PaneGone {
		t.Fatalf("result = %#v, want a confirmed exit", result)
	}
	if len(script.inputs) != 1 || !strings.Contains(string(script.inputs[0]), `"bytes":"DQ=="`) {
		t.Fatalf("terminal input = %q, want one carriage return", script.inputs)
	}
}

func TestExitAgentReportsAnAgentThatNeverEnded(t *testing.T) {
	script := &scriptedHerdr{t: t, responses: []scriptedResponse{
		{args: []string{"agent", "list"}, output: agentList(workerAgentJSON)},
		{args: []string{"agent", "prompt", "w1:p2", "/exit"}, output: `{"result":{}}`},
		{args: []string{"agent", "list"}, output: agentList(workerAgentJSON)},
		{
			args:   []string{"pane", "layout", "--pane", "w1:p2"},
			output: `{"result":{"layout":{"panes":[{"pane_id":"w1:p2","rect":{"width":120,"height":40}}]}}}`,
		},
		{args: nil, output: ""},
		{args: []string{"agent", "list"}, output: agentList(workerAgentJSON)},
	}}

	_, err := script.client().ExitAgent(workerIdentity())
	if !errors.Is(err, ErrExitUncertain) {
		t.Fatalf("ExitAgent = %v, want ErrExitUncertain", err)
	}
}

func TestExitAgentRefusesToClaimAnExitItCannotRead(t *testing.T) {
	script := &scriptedHerdr{t: t, responses: []scriptedResponse{
		{args: []string{"agent", "list"}, output: agentList(workerAgentJSON)},
		{args: []string{"agent", "prompt", "w1:p2", "/exit"}, output: `{"result":{}}`},
		{args: []string{"agent", "list"}, err: errors.New("herdr server is unreachable")},
	}}

	_, err := script.client().ExitAgent(workerIdentity())
	if !errors.Is(err, ErrExitUncertain) {
		t.Fatalf("ExitAgent = %v, want ErrExitUncertain", err)
	}
}

func TestExitAgentReportsAReplacementSessionOnTheSamePane(t *testing.T) {
	script := &scriptedHerdr{t: t, responses: []scriptedResponse{
		{args: []string{"agent", "list"}, output: agentList(workerAgentJSON)},
		{args: []string{"agent", "prompt", "w1:p2", "/exit"}, output: `{"result":{}}`},
		{args: []string{"agent", "list"}, output: agentList(replacementAgentJSON)},
		{args: []string{"agent", "list"}, output: agentList(replacementAgentJSON)},
	}}

	result, err := script.client().ExitAgent(workerIdentity())
	if err != nil {
		t.Fatalf("ExitAgent: %v", err)
	}
	if result.Outcome != ExitedReplaced {
		t.Fatalf("outcome = %q, want %q", result.Outcome, ExitedReplaced)
	}
	if result.PaneGone {
		t.Fatal("a replaced pane was reported as free to close")
	}
	if result.Replacement.NativeSessionID != "native-999" {
		t.Fatalf("replacement = %#v", result.Replacement)
	}
}

func TestExitAgentTreatsAnAbsentSessionAsAlreadyExited(t *testing.T) {
	script := &scriptedHerdr{t: t, responses: []scriptedResponse{
		{args: []string{"agent", "list"}, output: agentList()},
		{args: []string{"agent", "list"}, output: agentList()},
	}}

	result, err := script.client().ExitAgent(workerIdentity())
	if err != nil {
		t.Fatalf("ExitAgent: %v", err)
	}
	if result.Outcome != ExitedAlready || !result.PaneGone {
		t.Fatalf("result = %#v, want an already-exited session", result)
	}
	for _, call := range script.commands() {
		if strings.Contains(call, "agent prompt") {
			t.Fatalf("ExitAgent prompted an absent session: %v", script.commands())
		}
	}
}

func TestExitAgentNeverInterruptsABusyAgent(t *testing.T) {
	for _, status := range []Status{StatusWorking, StatusBlocked} {
		t.Run(string(status), func(t *testing.T) {
			busy := strings.Replace(workerAgentJSON, `"agent_status":"idle"`,
				`"agent_status":"`+string(status)+`"`, 1)
			script := &scriptedHerdr{t: t, responses: []scriptedResponse{
				{args: []string{"agent", "list"}, output: agentList(busy)},
			}}

			_, err := script.client().ExitAgent(workerIdentity())
			if !errors.Is(err, ErrAgentBusy) {
				t.Fatalf("ExitAgent = %v, want ErrAgentBusy", err)
			}
			for _, call := range script.commands() {
				if strings.Contains(call, "agent prompt") {
					t.Fatalf("ExitAgent prompted a %s agent: %v", status, script.commands())
				}
			}
		})
	}
}

func TestExitAgentRefusesAnUnnamedPane(t *testing.T) {
	script := &scriptedHerdr{t: t}
	if _, err := script.client().ExitAgent(SessionIdentity{}); err == nil {
		t.Fatal("ExitAgent accepted an identity with no pane")
	}
	if len(script.calls) != 0 {
		t.Fatalf("ExitAgent ran %v for an unnamed pane", script.commands())
	}
}

func TestPaneOccupantFindsAReplacementByPaneOrTab(t *testing.T) {
	identity := workerIdentity()
	replacement := Agent{PaneID: "w1:p2", TabID: "w1:tZ", NativeSessionID: "native-999"}
	if agent, found := PaneOccupant([]Agent{replacement}, identity); !found || agent.NativeSessionID != "native-999" {
		t.Fatalf("PaneOccupant = %#v/%t, want the pane replacement", agent, found)
	}
	byTab := Agent{PaneID: "w1:pZ", TabID: "w1:t2", NativeSessionID: "native-888"}
	if agent, found := PaneOccupant([]Agent{byTab}, identity); !found || agent.NativeSessionID != "native-888" {
		t.Fatalf("PaneOccupant = %#v/%t, want the tab replacement", agent, found)
	}
	elsewhere := Agent{PaneID: "w1:pZ", TabID: "w1:tZ"}
	if _, found := PaneOccupant([]Agent{elsewhere}, identity); found {
		t.Fatal("PaneOccupant matched an unrelated agent")
	}
}
