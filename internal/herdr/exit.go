package herdr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ExitCommand is the text that ends an interactive agent session. It is a slash
// command the agent itself interprets, not a signal, so the agent shuts down the
// way a person ending a session would.
const ExitCommand = "/exit"

// ErrExitUncertain means Relay could not confirm that the exact target session
// ended. Nothing may be closed or archived on an uncertain exit: the session may
// still be running with a staged command in its composer.
var ErrExitUncertain = errors.New("herdr agent exit is unconfirmed")

// ErrAgentBusy means the target agent is working or blocked. A busy agent is
// never interrupted; the caller retries later.
var ErrAgentBusy = errors.New("herdr agent is busy")

// SessionIdentity names one exact live agent session. Herdr reuses pane and tab
// ids, so identity is all of them together with the terminal and the agent's own
// native session id: a later session that happens to land on the same pane is a
// different session and must never be mistaken for this one.
type SessionIdentity struct {
	PaneID          string
	TabID           string
	WorkspaceID     string
	TerminalID      string
	NativeSessionID string
}

// Identity returns the exact identity of one observed agent.
func (a Agent) Identity() SessionIdentity {
	return SessionIdentity{
		PaneID:          a.PaneID,
		TabID:           a.TabID,
		WorkspaceID:     a.WorkspaceID,
		TerminalID:      a.TerminalID,
		NativeSessionID: a.NativeSessionID,
	}
}

// Matches reports whether agent is this exact session. Every field must agree,
// including the empty ones: an observation that gained a native session id the
// recorded identity never had is a different session.
func (i SessionIdentity) Matches(agent Agent) bool {
	return i.PaneID != "" && i == agent.Identity()
}

// String renders an identity for an operator-facing message.
func (i SessionIdentity) String() string {
	parts := []string{"pane " + i.PaneID}
	if i.TabID != "" {
		parts = append(parts, "tab "+i.TabID)
	}
	if i.TerminalID != "" {
		parts = append(parts, "terminal "+i.TerminalID)
	}
	if i.NativeSessionID != "" {
		parts = append(parts, "session "+i.NativeSessionID)
	}
	return strings.Join(parts, ", ")
}

// FindSession returns the live agent that is this exact session.
func FindSession(agents []Agent, identity SessionIdentity) (Agent, bool) {
	for _, agent := range agents {
		if identity.Matches(agent) {
			return agent, true
		}
	}
	return Agent{}, false
}

// PaneOccupant returns any live agent currently running in a pane or tab,
// whichever the identity records. It is how a caller sees that a replacement
// session took over ids the original session used to hold.
func PaneOccupant(agents []Agent, identity SessionIdentity) (Agent, bool) {
	for _, agent := range agents {
		if identity.PaneID != "" && agent.PaneID == identity.PaneID {
			return agent, true
		}
		if identity.TabID != "" && agent.TabID == identity.TabID {
			return agent, true
		}
	}
	return Agent{}, false
}

// ExitOutcome names how an exit attempt finished.
type ExitOutcome string

// Exit outcomes.
const (
	// ExitedNow means the exact target session ended during this call.
	ExitedNow ExitOutcome = "exited"
	// ExitedAlready means the exact target session was already gone.
	ExitedAlready ExitOutcome = "already-exited"
	// ExitedReplaced means the target session ended, but another agent now
	// holds its pane or tab. The exit succeeded; closing its ids would take
	// down somebody else's session, so the caller must not close them.
	ExitedReplaced ExitOutcome = "replaced"
)

// ExitResult reports one exit attempt.
type ExitResult struct {
	Outcome ExitOutcome
	// PaneGone reports that no agent occupies the recorded pane or tab, so the
	// recorded ids are safe to close.
	PaneGone bool
	// Replacement is the agent that took over the recorded ids, when one did.
	Replacement Agent
}

// ExitAgent ends one exact agent session and confirms it is gone.
//
// It is deliberately not PromptAgent. A prompt succeeds when the agent starts a
// new turn; an exit succeeds when the agent is no longer a recognized agent
// session at all. Confirming an exit by watching for a turn would report success
// for an agent that merely echoed the command and kept running.
//
// The delivery path is the same one prompts use, because it is the one that
// works: Herdr stages the text and runs its own delayed submit, and if this
// exact session is still idle with nothing submitted afterwards, Relay presses
// Enter through Herdr's terminal-session control stream. Focus is never taken.
//
// An exit that cannot be confirmed returns ErrExitUncertain, and the caller must
// leave the tab, project, and worktree exactly as they are: a session that is
// still running has this command staged in its composer, and closing its pane or
// discarding its worktree would destroy live work.
func (c *Client) ExitAgent(identity SessionIdentity) (ExitResult, error) {
	if identity.PaneID == "" {
		return ExitResult{}, errors.New("exit Herdr agent: no pane was named")
	}
	before, found, err := c.sessionByIdentity(identity)
	if err != nil {
		return ExitResult{}, fmt.Errorf("exit Herdr agent %s: %w", identity, err)
	}
	if !found {
		return c.exitResult(identity, ExitedAlready)
	}
	switch before.Status {
	case StatusIdle, StatusDone:
	default:
		return ExitResult{}, fmt.Errorf(
			"%w: agent %s is %s, want idle or done", ErrAgentBusy, identity, before.Status,
		)
	}

	err = c.runJSON(&struct{}{}, "agent", "prompt", identity.PaneID, ExitCommand)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ExitResult{}, fmt.Errorf(
			"%w for agent %s: the exit command ended before delivery could be confirmed: %w",
			ErrExitUncertain, identity, err,
		)
	}
	if err != nil && !strings.Contains(err.Error(), "agent_prompt_stalled") {
		return ExitResult{}, fmt.Errorf("exit Herdr agent %s: %w", identity, err)
	}

	gone, current, waitErr := c.awaitSessionExit(identity, c.promptSubmitGrace)
	if waitErr != nil {
		return ExitResult{}, fmt.Errorf(
			"%w for agent %s: the exit command may be staged, then agent state could not be read: %w",
			ErrExitUncertain, identity, waitErr,
		)
	}
	if gone {
		return c.exitResult(identity, ExitedNow)
	}
	if !promptStillStaged(before, current) {
		return ExitResult{}, fmt.Errorf(
			"%w for agent %s: state moved from %s/%d to %s/%d without the session ending; "+
				"terminal fallback was not attempted",
			ErrExitUncertain, identity,
			before.Status, before.StateChangeSeq, current.Status, current.StateChangeSeq,
		)
	}
	size, err := c.paneSize(identity.PaneID)
	if err != nil {
		return ExitResult{}, fmt.Errorf(
			"%w for agent %s: the exit command may be staged, then pane dimensions could not be read: %w",
			ErrExitUncertain, identity, err,
		)
	}
	if submitErr := c.sendTerminalInput(identity.PaneID, size, []byte{'\r'}); submitErr != nil {
		return ExitResult{}, fmt.Errorf(
			"%w for agent %s: the exit command was staged, then Enter failed: %w",
			ErrExitUncertain, identity, submitErr,
		)
	}
	gone, current, waitErr = c.awaitSessionExit(identity, c.promptTimeout)
	if waitErr != nil {
		return ExitResult{}, fmt.Errorf(
			"%w for agent %s: the exit command was staged and Enter was sent, then agent state could "+
				"not be read: %w",
			ErrExitUncertain, identity, waitErr,
		)
	}
	if !gone {
		return ExitResult{}, fmt.Errorf(
			"%w for agent %s: the exit command was staged and terminal-targeted Enter was sent, but the "+
				"session is still %s after %s; focus the pane and finish the exit by hand",
			ErrExitUncertain, identity, current.Status, c.promptTimeout,
		)
	}
	return c.exitResult(identity, ExitedNow)
}

// exitResult reports whether the recorded ids are now free. A replacement that
// took the pane over means the exit succeeded and the ids must not be closed.
func (c *Client) exitResult(identity SessionIdentity, outcome ExitOutcome) (ExitResult, error) {
	agents, err := c.Agents()
	if err != nil {
		return ExitResult{}, fmt.Errorf(
			"%w for agent %s: the session ended, then Herdr could not be re-read to confirm its pane is "+
				"free: %w",
			ErrExitUncertain, identity, err,
		)
	}
	if replacement, occupied := PaneOccupant(agents, identity); occupied {
		return ExitResult{Outcome: ExitedReplaced, Replacement: replacement}, nil
	}
	return ExitResult{Outcome: outcome, PaneGone: true}, nil
}

// awaitSessionExit polls until the exact session is no longer a recognized
// agent. It returns the last observation of the session while it is still there,
// so the caller can decide whether a staged command needs an Enter.
func (c *Client) awaitSessionExit(identity SessionIdentity, timeout time.Duration) (bool, Agent, error) {
	deadline := time.Now().Add(timeout)
	var last Agent
	for {
		current, found, err := c.sessionByIdentity(identity)
		if err != nil {
			if !time.Now().Before(deadline) {
				return false, last, err
			}
			c.sleep(c.promptPollInterval)
			continue
		}
		if !found {
			return true, last, nil
		}
		last = current
		if !time.Now().Before(deadline) {
			return false, last, nil
		}
		c.sleep(c.promptPollInterval)
	}
}

func (c *Client) sessionByIdentity(identity SessionIdentity) (Agent, bool, error) {
	agents, err := c.Agents()
	if err != nil {
		return Agent{}, false, err
	}
	agent, found := FindSession(agents, identity)
	return agent, found, nil
}
