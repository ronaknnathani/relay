package herdr

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClientWorkspacesParsesJSON(t *testing.T) {
	client := NewClientWithRunner(func(args ...string) ([]byte, error) {
		expected := []string{"workspace", "list"}
		if !reflect.DeepEqual(args, expected) {
			t.Fatalf("args = %#v, want %#v", args, expected)
		}
		return []byte(`{"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[{"workspace_id":"w1","label":"relay","focused":true}]}}`), nil
	})

	got, err := client.Workspaces()
	if err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	want := []Workspace{{ID: "w1", Label: "relay", Focused: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Workspaces = %#v, want %#v", got, want)
	}
}

func TestClientCommandsParseJSONAndPassExactArguments(t *testing.T) {
	responses := []struct {
		args   []string
		output string
	}{
		{
			[]string{"tab", "create", "--workspace", "w1", "--cwd", "/repo/worktree", "--label", "w1: Build it", "--no-focus"},
			`{"result":{"tab":{"tab_id":"w1:t2"},"root_pane":{"pane_id":"w1:p2"}}}`,
		},
		{
			[]string{"agent", "list"},
			`{"result":{"agents":[{"agent_status":"working","pane_id":"w1:p2","tab_id":"w1:t2","workspace_id":"w1","terminal_title_stripped":"relay:child - GitHub Copilot","cwd":"/repo","foreground_cwd":"/repo/worktree","agent_session":{"value":"native-123"}}]}}`,
		},
		{[]string{"pane", "run", "w1:p2", "relay resume child"}, ``},
		{[]string{"agent", "rename", "w1:p2", "program-w1"}, `{"result":{}}`},
		{
			[]string{"agent", "list"},
			`{"result":{"agents":[{"agent_status":"idle","state_change_seq":10,"pane_id":"w1:p2"}]}}`,
		},
		{[]string{"agent", "prompt", "w1:p2", "Check your Relay inbox."}, `{"result":{}}`},
		{
			[]string{"agent", "list"},
			`{"result":{"agents":[{"agent_status":"idle","state_change_seq":10,"pane_id":"w1:p2"}]}}`,
		},
		{
			[]string{"agent", "list"},
			`{"result":{"agents":[{"agent_status":"idle","state_change_seq":10,"pane_id":"w1:p2"}]}}`,
		},
		{
			[]string{"pane", "layout", "--pane", "w1:p2"},
			`{"result":{"layout":{"panes":[{"pane_id":"other","rect":{"width":60,"height":40}},{"pane_id":"w1:p2","rect":{"width":123,"height":40}}]}}}`,
		},
		{
			[]string{"agent", "list"},
			`{"result":{"agents":[{"agent_status":"working","state_change_seq":11,"pane_id":"w1:p2"}]}}`,
		},
		{[]string{"agent", "focus", "w1:p2"}, `{"result":{}}`},
	}
	index := 0
	client := NewClientWithRunner(func(args ...string) ([]byte, error) {
		if index >= len(responses) {
			t.Fatalf("unexpected command: %#v", args)
		}
		response := responses[index]
		index++
		if !reflect.DeepEqual(args, response.args) {
			t.Fatalf("args = %#v, want %#v", args, response.args)
		}
		return []byte(response.output), nil
	})
	var terminalInput []byte
	var terminalArgs []string
	client.runInput = func(input []byte, args ...string) ([]byte, error) {
		terminalInput = append([]byte(nil), input...)
		terminalArgs = append([]string(nil), args...)
		return nil, nil
	}
	client.sleep = func(time.Duration) {}

	tab, err := client.CreateTab("w1", "/repo/worktree", "w1: Build it")
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	if want := (Tab{ID: "w1:t2", RootPaneID: "w1:p2"}); tab != want {
		t.Fatalf("CreateTab = %#v, want %#v", tab, want)
	}
	agents, err := client.Agents()
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	wantAgents := []Agent{{
		Status:          StatusWorking,
		PaneID:          "w1:p2",
		TabID:           "w1:t2",
		WorkspaceID:     "w1",
		TerminalTitle:   "relay:child - GitHub Copilot",
		CWD:             "/repo",
		ForegroundCWD:   "/repo/worktree",
		NativeSessionID: "native-123",
	}}
	if !reflect.DeepEqual(agents, wantAgents) {
		t.Fatalf("Agents = %#v, want %#v", agents, wantAgents)
	}
	if err := client.RunPane("w1:p2", "relay resume child"); err != nil {
		t.Fatalf("RunPane: %v", err)
	}
	if err := client.RenameAgent("w1:p2", "program-w1"); err != nil {
		t.Fatalf("RenameAgent: %v", err)
	}
	if err := client.PromptAgent("w1:p2", "Check your Relay inbox."); err != nil {
		t.Fatalf("PromptAgent: %v", err)
	}
	if string(terminalInput) != "{\"type\":\"terminal.input\",\"bytes\":\"DQ==\"}\n" {
		t.Fatalf("terminal input = %q", terminalInput)
	}
	if want := []string{
		"terminal", "session", "control", "w1:p2",
		"--takeover", "--cols", "123", "--rows", "40",
	}; !reflect.DeepEqual(terminalArgs, want) {
		t.Fatalf("terminal args = %#v, want %#v", terminalArgs, want)
	}
	if err := client.FocusAgent("w1:p2"); err != nil {
		t.Fatalf("FocusAgent: %v", err)
	}
	if index != len(responses) {
		t.Fatalf("ran %d commands, want %d", index, len(responses))
	}
}

func TestClientReportsCommandAndJSONErrors(t *testing.T) {
	client := NewClientWithRunner(func(args ...string) ([]byte, error) {
		return []byte(`{"error":"server unavailable"}`), errors.New("exit status 1")
	})
	_, err := client.Agents()
	if err == nil || !strings.Contains(err.Error(), "herdr agent list") || !strings.Contains(err.Error(), "server unavailable") {
		t.Fatalf("Agents error = %v", err)
	}

	client = NewClientWithRunner(func(args ...string) ([]byte, error) {
		return []byte(`not-json`), nil
	})
	_, err = client.Workspaces()
	if err == nil || !strings.Contains(err.Error(), "parse herdr workspace list JSON") {
		t.Fatalf("Workspaces error = %v", err)
	}
}

func TestRunPaneAcceptsEmptyOrJSONSuccessAndReportsFailures(t *testing.T) {
	for _, output := range []string{"", `{"result":{}}`} {
		client := NewClientWithRunner(func(args ...string) ([]byte, error) {
			return []byte(output), nil
		})
		if err := client.RunPane("w1:p2", "relay program patrol run demo"); err != nil {
			t.Fatalf("RunPane output %q: %v", output, err)
		}
	}

	client := NewClientWithRunner(func(args ...string) ([]byte, error) {
		return []byte("pane unavailable"), errors.New("exit status 1")
	})
	err := client.RunPane("w1:p2", "relay program patrol run demo")
	if err == nil || !strings.Contains(err.Error(), "pane unavailable") {
		t.Fatalf("RunPane error = %v", err)
	}
}

func TestPromptAgentSubmitsStagedTextThroughTerminalControl(t *testing.T) {
	calls := [][]string{}
	responses := []string{
		`{"result":{"agents":[{"agent_status":"idle","state_change_seq":20,"pane_id":"w1:p2"}]}}`,
		`{"result":{"type":"agent_prompted"}}`,
		`{"result":{"agents":[{"agent_status":"idle","state_change_seq":20,"pane_id":"w1:p2"}]}}`,
		`{"result":{"agents":[{"agent_status":"idle","state_change_seq":20,"pane_id":"w1:p2"}]}}`,
		`{"result":{"layout":{"panes":[{"pane_id":"other","rect":{"width":60,"height":40}},{"pane_id":"w1:p2","rect":{"width":123,"height":40}}]}}}`,
		`{"result":{"agents":[{"agent_status":"working","state_change_seq":21,"pane_id":"w1:p2"}]}}`,
	}
	client := NewClientWithRunner(func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte(responses[len(calls)-1]), nil
	})
	inputCalls := 0
	client.runInput = func(input []byte, args ...string) ([]byte, error) {
		inputCalls++
		if string(input) != "{\"type\":\"terminal.input\",\"bytes\":\"DQ==\"}\n" {
			t.Fatalf("terminal input = %q", input)
		}
		if want := []string{
			"terminal", "session", "control", "w1:p2",
			"--takeover", "--cols", "123", "--rows", "40",
		}; !reflect.DeepEqual(args, want) {
			t.Fatalf("terminal args = %#v, want %#v", args, want)
		}
		return nil, nil
	}
	client.sleep = func(time.Duration) {}

	if err := client.PromptAgent("w1:p2", "Check Relay program mail and patrol state."); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"agent", "list"},
		{"agent", "prompt", "w1:p2", "Check Relay program mail and patrol state."},
		{"agent", "list"},
		{"agent", "list"},
		{"pane", "layout", "--pane", "w1:p2"},
		{"agent", "list"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if inputCalls != 1 {
		t.Fatalf("terminal input calls = %d, want 1", inputCalls)
	}
}

func TestPromptAgentDoesNotSendEnterWhenPromptAlreadyStartedTheTurn(t *testing.T) {
	calls := [][]string{}
	responses := []string{
		`{"result":{"agents":[{"agent_status":"done","state_change_seq":20,"pane_id":"w1:p2"}]}}`,
		`{"result":{"type":"agent_prompted"}}`,
		`{"result":{"agents":[{"agent_status":"working","state_change_seq":21,"pane_id":"w1:p2"}]}}`,
	}
	client := NewClientWithRunner(func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte(responses[len(calls)-1]), nil
	})
	client.runInput = func(input []byte, args ...string) ([]byte, error) {
		t.Fatal("PromptAgent sent an extra Enter after the turn started")
		return nil, nil
	}

	if err := client.PromptAgent("w1:p2", "Check Relay program mail and patrol state."); err != nil {
		t.Fatal(err)
	}
}

func TestPromptAgentReportsUncertainDeliveryWithoutFocusingOrRetrying(t *testing.T) {
	calls := [][]string{}
	responses := []string{
		`{"result":{"agents":[{"agent_status":"idle","state_change_seq":30,"pane_id":"w1:p2"}]}}`,
		`{"result":{"type":"agent_prompted"}}`,
		`{"result":{"agents":[{"agent_status":"idle","state_change_seq":30,"pane_id":"w1:p2"}]}}`,
		`{"result":{"agents":[{"agent_status":"idle","state_change_seq":30,"pane_id":"w1:p2"}]}}`,
		`{"result":{"layout":{"panes":[{"pane_id":"w1:p2","rect":{"width":123,"height":40}}]}}}`,
		`{"result":{"agents":[{"agent_status":"idle","state_change_seq":30,"pane_id":"w1:p2"}]}}`,
	}
	client := NewClientWithRunner(func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte(responses[len(calls)-1]), nil
	})
	inputCalls := 0
	client.runInput = func(input []byte, args ...string) ([]byte, error) {
		inputCalls++
		return nil, nil
	}
	client.promptTimeout = 0
	client.sleep = func(time.Duration) {}

	err := client.PromptAgent("w1:p2", "Check Relay program mail and patrol state.")
	if !errors.Is(err, ErrPromptDeliveryUncertain) {
		t.Fatalf("error = %v, want ErrPromptDeliveryUncertain", err)
	}
	want := [][]string{
		{"agent", "list"},
		{"agent", "prompt", "w1:p2", "Check Relay program mail and patrol state."},
		{"agent", "list"},
		{"agent", "list"},
		{"pane", "layout", "--pane", "w1:p2"},
		{"agent", "list"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if inputCalls != 1 {
		t.Fatalf("terminal input calls = %d, want 1", inputCalls)
	}
}

func TestPromptAgentTreatsPromptTimeoutAndCancellationAsUncertain(t *testing.T) {
	for _, promptErr := range []error{context.DeadlineExceeded, context.Canceled} {
		t.Run(promptErr.Error(), func(t *testing.T) {
			calls := 0
			client := NewClientWithRunner(func(args ...string) ([]byte, error) {
				calls++
				if calls == 1 {
					return []byte(
						`{"result":{"agents":[{"agent_status":"idle","state_change_seq":30,"pane_id":"w1:p2"}]}}`,
					), nil
				}
				return nil, promptErr
			})

			err := client.PromptAgent("w1:p2", "Check Relay program mail and patrol state.")
			if !errors.Is(err, ErrPromptDeliveryUncertain) ||
				!errors.Is(err, promptErr) {
				t.Fatalf("prompt error = %v, want ErrPromptDeliveryUncertain wrapping %v", err, promptErr)
			}
			if calls != 2 {
				t.Fatalf("Herdr calls = %d, want preflight plus one prompt attempt", calls)
			}
		})
	}
}

func TestPromptAgentToleratesTransientStateReadFailures(t *testing.T) {
	type response struct {
		output string
		err    error
	}
	responses := []response{
		{output: `{"result":{"agents":[{"agent_status":"idle","state_change_seq":50,"pane_id":"w1:p2"}]}}`},
		{output: `{"result":{"type":"agent_prompted"}}`},
		{output: `{"error":{"code":"temporarily_unavailable"}}`, err: errors.New("exit status 1")},
		{output: `{"result":{"agents":[{"agent_status":"idle","state_change_seq":50,"pane_id":"w1:p2"}]}}`},
		{output: `{"result":{"agents":[{"agent_status":"idle","state_change_seq":50,"pane_id":"w1:p2"}]}}`},
		{output: `{"result":{"layout":{"panes":[{"pane_id":"w1:p2","rect":{"width":123,"height":40}}]}}}`},
		{output: `{"error":{"code":"temporarily_unavailable"}}`, err: errors.New("exit status 1")},
		{output: `{"result":{"agents":[{"agent_status":"working","state_change_seq":51,"pane_id":"w1:p2"}]}}`},
	}
	index := 0
	client := NewClientWithRunner(func(args ...string) ([]byte, error) {
		response := responses[index]
		index++
		return []byte(response.output), response.err
	})
	client.runInput = func(input []byte, args ...string) ([]byte, error) {
		return nil, nil
	}
	client.promptPollInterval = 0
	client.sleep = func(time.Duration) {}

	if err := client.PromptAgent("w1:p2", "Check your Relay inbox."); err != nil {
		t.Fatalf("PromptAgent rejected recoverable state reads: %v", err)
	}
	if index != len(responses) {
		t.Fatalf("PromptAgent used %d responses, want %d", index, len(responses))
	}
}

func TestPromptAgentDoesNotFallbackFromUnknownOrChangedState(t *testing.T) {
	responses := []string{
		`{"result":{"agents":[{"agent_status":"idle","state_change_seq":60,"pane_id":"w1:p2"}]}}`,
		`{"result":{"type":"agent_prompted"}}`,
		`{"result":{"agents":[{"agent_status":"idle","state_change_seq":60,"pane_id":"w1:p2"}]}}`,
		`{"result":{"agents":[{"agent_status":"unknown","state_change_seq":61,"pane_id":"w1:p2"}]}}`,
	}
	index := 0
	client := NewClientWithRunner(func(args ...string) ([]byte, error) {
		output := responses[index]
		index++
		return []byte(output), nil
	})
	client.runInput = func(input []byte, args ...string) ([]byte, error) {
		t.Fatal("unknown agent state triggered terminal input")
		return nil, nil
	}
	client.sleep = func(time.Duration) {}

	err := client.PromptAgent("w1:p2", "Check your Relay inbox.")
	if !errors.Is(err, ErrPromptDeliveryUncertain) ||
		!strings.Contains(err.Error(), "terminal fallback was not attempted") {
		t.Fatalf("unknown-state error = %v", err)
	}
}

func TestPromptAgentReportsTerminalControlInBandFailure(t *testing.T) {
	responses := []string{
		`{"result":{"agents":[{"agent_status":"idle","state_change_seq":70,"pane_id":"w1:p2"}]}}`,
		`{"result":{"type":"agent_prompted"}}`,
		`{"result":{"agents":[{"agent_status":"idle","state_change_seq":70,"pane_id":"w1:p2"}]}}`,
		`{"result":{"agents":[{"agent_status":"idle","state_change_seq":70,"pane_id":"w1:p2"}]}}`,
		`{"result":{"layout":{"panes":[{"pane_id":"w1:p2","rect":{"width":100,"height":30}}]}}}`,
	}
	index := 0
	client := NewClientWithRunner(func(args ...string) ([]byte, error) {
		output := responses[index]
		index++
		return []byte(output), nil
	})
	client.runInput = func(input []byte, args ...string) ([]byte, error) {
		return []byte(
			`{"reason":"terminal session control failed: terminal target w1:p2 not found","type":"terminal.closed"}` + "\n",
		), nil
	}
	client.sleep = func(time.Duration) {}

	err := client.PromptAgent("w1:p2", "Check your Relay inbox.")
	if !errors.Is(err, ErrPromptDeliveryUncertain) ||
		!strings.Contains(err.Error(), "terminal target w1:p2 not found") {
		t.Fatalf("terminal-control failure = %v", err)
	}
}

func TestPromptAgentRefusesBusyTargetsBeforeStagingText(t *testing.T) {
	calls := [][]string{}
	client := NewClientWithRunner(func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte(
			`{"result":{"agents":[{"agent_status":"working","state_change_seq":40,"pane_id":"w1:p2"}]}}`,
		), nil
	})

	err := client.PromptAgent("w1:p2", "Check your Relay inbox.")
	if err == nil || !strings.Contains(err.Error(), "want idle or done") {
		t.Fatalf("busy prompt error = %v", err)
	}
	if want := [][]string{{"agent", "list"}}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("busy prompt calls = %#v, want %#v", calls, want)
	}
}

func TestTerminalControlOutputIsBounded(t *testing.T) {
	var output truncatingBuffer
	data := bytes.Repeat([]byte("x"), maxTerminalControlOutput+1024)
	written, err := output.Write(data)
	if err != nil {
		t.Fatal(err)
	}
	if written != len(data) {
		t.Fatalf("written = %d, want %d", written, len(data))
	}
	if output.buffer.Len() != maxTerminalControlOutput {
		t.Fatalf("buffer length = %d, want %d", output.buffer.Len(), maxTerminalControlOutput)
	}
	if !bytes.Equal(output.buffer.Bytes(), data[len(data)-maxTerminalControlOutput:]) {
		t.Fatal("terminal control output did not retain the diagnostic tail")
	}

	if _, err := output.Write([]byte(
		"\n{\"reason\":\"terminal session control failed: pane disappeared\",\"type\":\"terminal.closed\"}\n",
	)); err != nil {
		t.Fatal(err)
	}
	err = terminalControlError(output.buffer.Bytes())
	if err == nil || !strings.Contains(err.Error(), "pane disappeared") {
		t.Fatalf("terminal control tail error = %v", err)
	}
}

func TestClientCommandTimeoutCancelsHungHerdr(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "herdr")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec /bin/sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", binDir)
	client := NewClientWithCommandTimeout(context.Background(), 20*time.Millisecond)

	_, err := client.Agents()
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("Agents timeout error = %v", err)
	}
}

func TestClientCommandTimeoutCancelsHungTerminalControl(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "herdr")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec /bin/sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	client := NewClientWithCommandTimeout(context.Background(), 20*time.Millisecond)

	err := client.sendTerminalInput("w1:p2", paneSize{columns: 100, rows: 30}, []byte{'\r'})
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("terminal control timeout error = %v", err)
	}
}
