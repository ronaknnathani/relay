// Package herdr provides Relay's small command-line integration with Herdr.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CommandRunner executes one Herdr command.
type CommandRunner func(args ...string) ([]byte, error)

// Client invokes the installed Herdr CLI.
type Client struct {
	run CommandRunner
}

// ErrPromptDeliveryUncertain means Herdr staged a prompt but Relay could not
// confirm or submit it. Callers must not retry automatically because doing so
// may append duplicate text to the agent's input buffer.
var ErrPromptDeliveryUncertain = errors.New("herdr prompt delivery is uncertain")

// Workspace is the subset of Herdr workspace state Relay uses.
type Workspace struct {
	ID      string `json:"workspace_id"`
	Label   string `json:"label"`
	Focused bool   `json:"focused"`
}

// Tab contains the identifiers returned when Herdr creates a tab.
type Tab struct {
	ID         string
	RootPaneID string
}

// Status is Herdr's observed agent liveness state.
type Status string

// Herdr agent statuses.
const (
	StatusIdle    Status = "idle"
	StatusWorking Status = "working"
	StatusBlocked Status = "blocked"
	StatusDone    Status = "done"
	StatusUnknown Status = "unknown"
)

// Agent is the subset of live Herdr agent state Relay uses.
type Agent struct {
	Status          Status
	PaneID          string
	TabID           string
	WorkspaceID     string
	TerminalTitle   string
	CWD             string
	ForegroundCWD   string
	NativeSessionID string
}

// NewClient creates a Client backed by the installed herdr binary.
func NewClient() *Client {
	return NewClientWithRunner(func(args ...string) ([]byte, error) {
		return exec.Command("herdr", args...).CombinedOutput()
	})
}

// NewClientWithCommandTimeout creates a Client whose commands are canceled
// with ctx and bounded by timeout.
func NewClientWithCommandTimeout(ctx context.Context, timeout time.Duration) *Client {
	if ctx == nil {
		ctx = context.Background()
	}
	return NewClientWithRunner(func(args ...string) ([]byte, error) {
		commandCtx := ctx
		cancel := func() {}
		if timeout > 0 {
			commandCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		defer cancel()
		output, err := exec.CommandContext(commandCtx, "herdr", args...).CombinedOutput()
		if commandCtx.Err() != nil {
			return output, commandCtx.Err()
		}
		return output, err
	})
}

// NewClientWithRunner creates a Client backed by runner.
func NewClientWithRunner(runner CommandRunner) *Client {
	return &Client{run: runner}
}

// Workspaces lists Herdr workspaces.
func (c *Client) Workspaces() ([]Workspace, error) {
	var response struct {
		Result struct {
			Workspaces []Workspace `json:"workspaces"`
		} `json:"result"`
	}
	if err := c.runJSON(&response, "workspace", "list"); err != nil {
		return nil, fmt.Errorf("list Herdr workspaces: %w", err)
	}
	return response.Result.Workspaces, nil
}

// CreateTab creates an unfocused tab in workspace.
func (c *Client) CreateTab(workspaceID, cwd, label string) (Tab, error) {
	var response struct {
		Result struct {
			Tab struct {
				ID string `json:"tab_id"`
			} `json:"tab"`
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := c.runJSON(
		&response,
		"tab", "create",
		"--workspace", workspaceID,
		"--cwd", cwd,
		"--label", label,
		"--no-focus",
	); err != nil {
		return Tab{}, fmt.Errorf("create Herdr tab in workspace %q: %w", workspaceID, err)
	}
	return Tab{ID: response.Result.Tab.ID, RootPaneID: response.Result.RootPane.ID}, nil
}

// Agents lists live agents recognized by Herdr.
func (c *Client) Agents() ([]Agent, error) {
	var response struct {
		Result struct {
			Agents []struct {
				Status        Status `json:"agent_status"`
				PaneID        string `json:"pane_id"`
				TabID         string `json:"tab_id"`
				WorkspaceID   string `json:"workspace_id"`
				TerminalTitle string `json:"terminal_title_stripped"`
				CWD           string `json:"cwd"`
				ForegroundCWD string `json:"foreground_cwd"`
				AgentSession  *struct {
					Value string `json:"value"`
				} `json:"agent_session"`
			} `json:"agents"`
		} `json:"result"`
	}
	if err := c.runJSON(&response, "agent", "list"); err != nil {
		return nil, fmt.Errorf("list Herdr agents: %w", err)
	}
	agents := make([]Agent, 0, len(response.Result.Agents))
	for _, raw := range response.Result.Agents {
		agent := Agent{
			Status:        raw.Status,
			PaneID:        raw.PaneID,
			TabID:         raw.TabID,
			WorkspaceID:   raw.WorkspaceID,
			TerminalTitle: raw.TerminalTitle,
			CWD:           raw.CWD,
			ForegroundCWD: raw.ForegroundCWD,
		}
		if raw.AgentSession != nil {
			agent.NativeSessionID = raw.AgentSession.Value
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

// RunPane submits command to an interactive shell pane.
func (c *Client) RunPane(paneID, command string) error {
	if c == nil || c.run == nil {
		return errors.New("herdr command runner is not configured")
	}
	args := []string{"pane", "run", paneID, command}
	output, err := c.run(args...)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return fmt.Errorf("run command in Herdr pane %q: run herdr %s: %w",
				paneID, strings.Join(args, " "), err)
		}
		return fmt.Errorf("run command in Herdr pane %q: run herdr %s: %w: %s",
			paneID, strings.Join(args, " "), err, detail)
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return nil
	}
	if err := json.Unmarshal(output, &struct{}{}); err != nil {
		return fmt.Errorf(
			"run command in Herdr pane %q: parse herdr %s JSON: %w",
			paneID, strings.Join(args, " "), err,
		)
	}
	return nil
}

// RenameAgent assigns name to the live agent targeted by target.
func (c *Client) RenameAgent(target, name string) error {
	if err := c.runJSON(&struct{}{}, "agent", "rename", target, name); err != nil {
		return fmt.Errorf("rename Herdr agent %q to %q: %w", target, name, err)
	}
	return nil
}

// PromptAgent immediately submits text to target without waiting.
func (c *Client) PromptAgent(target, text string) error {
	err := c.runJSON(&struct{}{}, "agent", "prompt", target, text)
	if err != nil && !strings.Contains(err.Error(), "agent_prompt_stalled") {
		return fmt.Errorf("prompt Herdr agent %q: %w", target, err)
	}
	if submitErr := c.runJSON(&struct{}{}, "agent", "send-keys", target, "enter"); submitErr != nil {
		return fmt.Errorf(
			"%w for agent %q: prompt text was staged, then Enter failed: %v",
			ErrPromptDeliveryUncertain, target, submitErr,
		)
	}
	return nil
}

// FocusAgent focuses the agent targeted by target.
func (c *Client) FocusAgent(target string) error {
	if err := c.runJSON(&struct{}{}, "agent", "focus", target); err != nil {
		return fmt.Errorf("focus Herdr agent %q: %w", target, err)
	}
	return nil
}

// ShowNotification raises a desktop notification. Callers treat it as a
// courtesy: Relay correctness never depends on a notification being delivered.
func (c *Client) ShowNotification(title, body string) error {
	if err := c.runJSON(
		&struct{}{},
		"notification", "show", title, "--body", body, "--sound", "request",
	); err != nil {
		return fmt.Errorf("show Herdr notification %q: %w", title, err)
	}
	return nil
}

func (c *Client) runJSON(target any, args ...string) error {
	if c == nil || c.run == nil {
		return errors.New("herdr command runner is not configured")
	}
	output, err := c.run(args...)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return fmt.Errorf("run herdr %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("run herdr %s: %w: %s", strings.Join(args, " "), err, detail)
	}
	if err := json.Unmarshal(output, target); err != nil {
		return fmt.Errorf("parse herdr %s JSON: %w", strings.Join(args, " "), err)
	}
	return nil
}
