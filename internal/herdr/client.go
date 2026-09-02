// Package herdr provides Relay's small command-line integration with Herdr.
package herdr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CommandRunner executes one Herdr command.
type CommandRunner func(args ...string) ([]byte, error)

// InputCommandRunner executes one Herdr command with explicit standard input.
type InputCommandRunner func(input []byte, args ...string) ([]byte, error)

// Client invokes the installed Herdr CLI.
type Client struct {
	run                CommandRunner
	runInput           InputCommandRunner
	promptSubmitGrace  time.Duration
	promptTimeout      time.Duration
	promptPollInterval time.Duration
	sleep              func(time.Duration)
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
	Status         Status
	StateChangeSeq int64
	PaneID         string
	TabID          string
	WorkspaceID    string
	// TerminalID names the terminal hosting the agent. Herdr reuses pane and
	// tab ids, so it is part of what makes one live session identifiable.
	TerminalID      string
	TerminalTitle   string
	CWD             string
	ForegroundCWD   string
	NativeSessionID string
}

// NewClient creates a Client backed by the installed herdr binary.
func NewClient() *Client {
	return newClientWithRunners(func(args ...string) ([]byte, error) {
		return exec.Command("herdr", args...).CombinedOutput()
	}, func(input []byte, args ...string) ([]byte, error) {
		command := exec.Command("herdr", args...)
		command.Stdin = bytes.NewReader(input)
		return runInputCommand(command)
	})
}

// NewClientWithCommandTimeout creates a Client whose commands are canceled
// with ctx and bounded by timeout.
func NewClientWithCommandTimeout(ctx context.Context, timeout time.Duration) *Client {
	if ctx == nil {
		ctx = context.Background()
	}
	run := func(input []byte, args ...string) ([]byte, error) {
		commandCtx := ctx
		cancel := func() {}
		if timeout > 0 {
			commandCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		defer cancel()
		command := exec.CommandContext(commandCtx, "herdr", args...)
		if input != nil {
			command.Stdin = bytes.NewReader(input)
		}
		var output []byte
		var err error
		if input == nil {
			output, err = command.CombinedOutput()
		} else {
			output, err = runInputCommand(command)
		}
		if commandCtx.Err() != nil {
			return output, commandCtx.Err()
		}
		return output, err
	}
	return newClientWithRunners(
		func(args ...string) ([]byte, error) { return run(nil, args...) },
		func(input []byte, args ...string) ([]byte, error) { return run(input, args...) },
	)
}

// NewClientWithRunner creates a Client backed by runner.
func NewClientWithRunner(runner CommandRunner) *Client {
	return newClientWithRunners(runner, nil)
}

func newClientWithRunners(runner CommandRunner, inputRunner InputCommandRunner) *Client {
	return &Client{
		run:                runner,
		runInput:           inputRunner,
		promptSubmitGrace:  time.Second,
		promptTimeout:      5 * time.Second,
		promptPollInterval: 100 * time.Millisecond,
		sleep:              time.Sleep,
	}
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

// CloseTab closes one Herdr tab. Relay closes only tabs it created and
// recorded, never one it inferred.
func (c *Client) CloseTab(tabID string) error {
	if strings.TrimSpace(tabID) == "" {
		return errors.New("close Herdr tab: no tab was named")
	}
	if err := c.runOptionalJSON("tab", "close", tabID); err != nil {
		return fmt.Errorf("close Herdr tab %q: %w", tabID, err)
	}
	return nil
}

// ClosePane closes one Herdr pane. It is the fallback for a recorded pane whose
// tab is not known, so cleanup still names an exact target.
func (c *Client) ClosePane(paneID string) error {
	if strings.TrimSpace(paneID) == "" {
		return errors.New("close Herdr pane: no pane was named")
	}
	if err := c.runOptionalJSON("pane", "close", paneID); err != nil {
		return fmt.Errorf("close Herdr pane %q: %w", paneID, err)
	}
	return nil
}

// Agents lists live agents recognized by Herdr.
func (c *Client) Agents() ([]Agent, error) {
	var response struct {
		Result struct {
			Agents []struct {
				Status         Status `json:"agent_status"`
				StateChangeSeq int64  `json:"state_change_seq"`
				PaneID         string `json:"pane_id"`
				TabID          string `json:"tab_id"`
				WorkspaceID    string `json:"workspace_id"`
				TerminalID     string `json:"terminal_id"`
				TerminalTitle  string `json:"terminal_title_stripped"`
				CWD            string `json:"cwd"`
				ForegroundCWD  string `json:"foreground_cwd"`
				AgentSession   *struct {
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
			Status:         raw.Status,
			StateChangeSeq: raw.StateChangeSeq,
			PaneID:         raw.PaneID,
			TabID:          raw.TabID,
			WorkspaceID:    raw.WorkspaceID,
			TerminalID:     raw.TerminalID,
			TerminalTitle:  raw.TerminalTitle,
			CWD:            raw.CWD,
			ForegroundCWD:  raw.ForegroundCWD,
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
	args := []string{"pane", "run", paneID, command}
	if err := c.runOptionalJSON(args...); err != nil {
		return fmt.Errorf("run command in Herdr pane %q: %w", paneID, err)
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

// PromptAgent submits text to an idle target and confirms that Herdr observed
// a new turn. Terminal-session input works without changing the user's focus.
func (c *Client) PromptAgent(target, text string) error {
	before, err := c.agentByPane(target)
	if err != nil {
		return fmt.Errorf("prompt Herdr agent %q: %w", target, err)
	}
	switch before.Status {
	case StatusIdle, StatusDone:
	default:
		return fmt.Errorf(
			"prompt Herdr agent %q: agent is %s, want idle or done",
			target, before.Status,
		)
	}

	err = c.runJSON(&struct{}{}, "agent", "prompt", target, text)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf(
			"%w for agent %q: prompt command ended before delivery could be confirmed: %w",
			ErrPromptDeliveryUncertain, target, err,
		)
	}
	if err != nil && !strings.Contains(err.Error(), "agent_prompt_stalled") {
		return fmt.Errorf("prompt Herdr agent %q: %w", target, err)
	}

	afterPrompt, agentErr := c.readAgentWithRetry(target, c.promptTimeout)
	if agentErr != nil {
		return fmt.Errorf(
			"%w for agent %q: prompt text may be staged, then agent state could not be read: %v",
			ErrPromptDeliveryUncertain, target, agentErr,
		)
	}
	if turnStarted(before, afterPrompt) {
		return nil
	}
	c.sleep(c.promptSubmitGrace)
	afterGrace, agentErr := c.readAgentWithRetry(target, c.promptTimeout)
	if agentErr != nil {
		return fmt.Errorf(
			"%w for agent %q: prompt text may be staged, then agent state could not be read after the submit grace period: %v",
			ErrPromptDeliveryUncertain, target, agentErr,
		)
	}
	if turnStarted(before, afterGrace) {
		return nil
	}
	if !promptStillStaged(before, afterGrace) {
		return fmt.Errorf(
			"%w for agent %q: prompt submission state changed from %s/%d to %s/%d without a confirmed turn; "+
				"terminal fallback was not attempted",
			ErrPromptDeliveryUncertain, target,
			before.Status, before.StateChangeSeq, afterGrace.Status, afterGrace.StateChangeSeq,
		)
	}
	size, err := c.paneSize(target)
	if err != nil {
		return fmt.Errorf(
			"%w for agent %q: prompt text may be staged, then pane dimensions could not be read: %v",
			ErrPromptDeliveryUncertain, target, err,
		)
	}
	if submitErr := c.sendTerminalInput(target, size, []byte{'\r'}); submitErr != nil {
		return fmt.Errorf(
			"%w for agent %q: prompt text was staged, then Enter failed: %v",
			ErrPromptDeliveryUncertain, target, submitErr,
		)
	}

	if err := c.waitForTurnStart(target, before); err != nil {
		return fmt.Errorf(
			"%w for agent %q: prompt text was staged and terminal-targeted Enter was sent, but no new turn was observed: %v",
			ErrPromptDeliveryUncertain, target, err,
		)
	}
	return nil
}

type paneSize struct {
	columns int
	rows    int
}

func (c *Client) paneSize(target string) (paneSize, error) {
	var response struct {
		Result struct {
			Layout struct {
				Panes []struct {
					PaneID string `json:"pane_id"`
					Rect   struct {
						Width  int `json:"width"`
						Height int `json:"height"`
					} `json:"rect"`
				} `json:"panes"`
			} `json:"layout"`
		} `json:"result"`
	}
	if err := c.runJSON(&response, "pane", "layout", "--pane", target); err != nil {
		return paneSize{}, fmt.Errorf("read Herdr pane %q dimensions: %w", target, err)
	}
	for _, pane := range response.Result.Layout.Panes {
		if pane.PaneID != target {
			continue
		}
		if pane.Rect.Width <= 0 || pane.Rect.Height <= 0 {
			return paneSize{}, fmt.Errorf(
				"read Herdr pane %q dimensions: invalid %dx%d pane rect",
				target, pane.Rect.Width, pane.Rect.Height,
			)
		}
		return paneSize{columns: pane.Rect.Width, rows: pane.Rect.Height}, nil
	}
	return paneSize{}, fmt.Errorf("read Herdr pane %q dimensions: target is absent from layout", target)
}

func (c *Client) sendTerminalInput(target string, size paneSize, input []byte) error {
	if c == nil || c.runInput == nil {
		return errors.New("herdr input command runner is not configured")
	}
	command := struct {
		Type  string `json:"type"`
		Bytes string `json:"bytes"`
	}{
		Type:  "terminal.input",
		Bytes: base64.StdEncoding.EncodeToString(input),
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return fmt.Errorf("encode terminal input for Herdr pane %q: %w", target, err)
	}
	payload = append(payload, '\n')
	output, err := c.runInput(
		payload,
		"terminal", "session", "control", target,
		"--takeover",
		"--cols", strconv.Itoa(size.columns),
		"--rows", strconv.Itoa(size.rows),
	)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return fmt.Errorf("control Herdr terminal for pane %q: %w", target, err)
		}
		return fmt.Errorf("control Herdr terminal for pane %q: %w: %s", target, err, detail)
	}
	if err := terminalControlError(output); err != nil {
		return fmt.Errorf("control Herdr terminal for pane %q: %w", target, err)
	}
	return nil
}

func terminalControlError(output []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 1024), maxTerminalControlOutput)
	for scanner.Scan() {
		var event struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Type == "terminal.closed" && strings.Contains(
			strings.ToLower(event.Reason), "failed",
		) {
			return errors.New(event.Reason)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("parse Herdr terminal control output: %w", err)
	}
	return nil
}

func (c *Client) readAgentWithRetry(target string, timeout time.Duration) (Agent, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		current, err := c.agentByPane(target)
		if err == nil {
			return current, nil
		}
		lastErr = err
		if !time.Now().Before(deadline) {
			return Agent{}, lastErr
		}
		c.sleep(c.promptPollInterval)
	}
}

const maxTerminalControlOutput = 8 * 1024

type truncatingBuffer struct {
	buffer bytes.Buffer
}

func (b *truncatingBuffer) Write(data []byte) (int, error) {
	original := len(data)
	if len(data) >= maxTerminalControlOutput {
		b.buffer.Reset()
		_, _ = b.buffer.Write(data[len(data)-maxTerminalControlOutput:])
		return original, nil
	}
	overflow := b.buffer.Len() + len(data) - maxTerminalControlOutput
	if overflow > 0 {
		retained := append([]byte(nil), b.buffer.Bytes()[overflow:]...)
		b.buffer.Reset()
		_, _ = b.buffer.Write(retained)
	}
	_, _ = b.buffer.Write(data)
	return original, nil
}

func runInputCommand(command *exec.Cmd) ([]byte, error) {
	var output truncatingBuffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return output.buffer.Bytes(), err
}

func (c *Client) agentByPane(target string) (Agent, error) {
	agents, err := c.Agents()
	if err != nil {
		return Agent{}, err
	}
	for _, candidate := range agents {
		if candidate.PaneID == target {
			return candidate, nil
		}
	}
	return Agent{}, fmt.Errorf("pane %q is not a live Herdr agent", target)
}

func (c *Client) waitForTurnStart(target string, before Agent) error {
	deadline := time.Now().Add(c.promptTimeout)
	var lastErr error
	lastStatus := before.Status
	lastSequence := before.StateChangeSeq
	for {
		current, err := c.agentByPane(target)
		if err == nil {
			lastErr = nil
			lastStatus = current.Status
			lastSequence = current.StateChangeSeq
			if turnStarted(before, current) {
				return nil
			}
		} else {
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			timeoutErr := fmt.Errorf(
				"agent remained %s at state sequence %d for %s",
				lastStatus, lastSequence, c.promptTimeout,
			)
			if lastErr != nil {
				return errors.Join(timeoutErr, lastErr)
			}
			return timeoutErr
		}
		c.sleep(c.promptPollInterval)
	}
}

func turnStarted(before, after Agent) bool {
	if after.Status != StatusWorking && after.Status != StatusBlocked && after.Status != StatusDone {
		return false
	}
	if before.StateChangeSeq == 0 || after.StateChangeSeq == 0 {
		return after.Status != before.Status
	}
	return after.StateChangeSeq > before.StateChangeSeq
}

func promptStillStaged(before, after Agent) bool {
	if after.Status != StatusIdle && after.Status != StatusDone {
		return false
	}
	if before.StateChangeSeq == 0 || after.StateChangeSeq == 0 {
		return after.Status == before.Status
	}
	return after.StateChangeSeq == before.StateChangeSeq
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

func (c *Client) runOptionalJSON(args ...string) error {
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
	if len(strings.TrimSpace(string(output))) == 0 {
		return nil
	}
	if err := json.Unmarshal(output, &struct{}{}); err != nil {
		return fmt.Errorf("parse herdr %s JSON: %w", strings.Join(args, " "), err)
	}
	return nil
}
