package herdr

import (
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
		{[]string{"agent", "prompt", "w1:p2", "Check your Relay inbox."}, `{"result":{}}`},
		{[]string{"agent", "send-keys", "w1:p2", "enter"}, `{"result":{}}`},
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

func TestPromptAgentSubmitsStagedTextAfterPromptStalls(t *testing.T) {
	calls := [][]string{}
	client := NewClientWithRunner(func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) == 1 {
			return []byte(`{"error":{"code":"agent_prompt_stalled","message":"no state change"}}`),
				errors.New("exit status 1")
		}
		return []byte(`{"result":{"type":"ok"}}`), nil
	})

	if err := client.PromptAgent("w1:p2", "Check Relay program mail and patrol state."); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"agent", "prompt", "w1:p2", "Check Relay program mail and patrol state."},
		{"agent", "send-keys", "w1:p2", "enter"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestPromptAgentDoesNotEncourageRetryWhenStagedEnterFails(t *testing.T) {
	client := NewClientWithRunner(func(args ...string) ([]byte, error) {
		if args[1] == "prompt" {
			return []byte(`{"error":{"code":"agent_prompt_stalled"}}`), errors.New("exit status 1")
		}
		return []byte(`{"error":{"code":"pane_unavailable"}}`), errors.New("exit status 1")
	})

	err := client.PromptAgent("w1:p2", "Check Relay program mail and patrol state.")
	if !errors.Is(err, ErrPromptDeliveryUncertain) {
		t.Fatalf("error = %v, want ErrPromptDeliveryUncertain", err)
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
