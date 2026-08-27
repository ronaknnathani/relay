package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/patrol"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programturn"
	"github.com/spf13/cobra"
)

var (
	acquireProgramWriter = programturn.AcquireWriter
	appendProgramTurn    = programturn.Append
	runHeadlessTurn      = agent.RunHeadlessTurn
	newTurnSessionID     = agent.NewSessionID
	turnNow              = func() time.Time { return time.Now().UTC() }
)

// programCTOTurnResult is the JSON contract for one bounded automated CTO turn.
type programCTOTurnResult struct {
	Program     string `json:"program"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	LogPath     string `json:"log_path,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	EndedAt     string `json:"ended_at,omitempty"`
	ExitCode    int    `json:"exit_code"`
	TimedOut    bool   `json:"timed_out,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Error       string `json:"error,omitempty"`
}

type programCTOTurnOptions struct {
	fingerprint string
	reasons     []patrol.Reason
}

// newCmdProgramCTO groups the internal transport the patrol uses to wake a
// program's CTO. It is hidden because a human never runs it: the CEO uses the
// interactive CTO session, and the patrol uses this command.
func newCmdProgramCTO() *cobra.Command {
	command := &cobra.Command{
		Use:    "cto",
		Short:  "Internal bounded CTO turn transport",
		Hidden: true,
	}
	command.AddCommand(newCmdProgramCTOTurn())
	return command
}

func newCmdProgramCTOTurn() *cobra.Command {
	var jsonOutput bool
	var fingerprint string
	command := &cobra.Command{
		Use:    "turn <slug>",
		Short:  "Run one bounded automated CTO turn in a fresh agent session",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			result, err := runProgramCTOTurn(
				command.Context(), args[0], programCTOTurnOptions{fingerprint: fingerprint},
			)
			if renderErr := renderProgramCTOTurn(command.OutOrStdout(), result, jsonOutput); renderErr != nil {
				return errors.Join(err, renderErr)
			}
			return err
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	command.Flags().StringVar(&fingerprint, "fingerprint", "", "attention fingerprint that triggered the turn")
	return command
}

func renderProgramCTOTurn(out io.Writer, result programCTOTurnResult, jsonOutput bool) error {
	if jsonOutput {
		return writeProgramJSON(out, result)
	}
	fmt.Fprintf(out, "Turn %s for %s\n", result.Status, result.Program)
	if result.Reason != "" {
		fmt.Fprintf(out, "Reason: %s\n", result.Reason)
	}
	if result.SessionID != "" {
		fmt.Fprintf(out, "Session: %s\nLog: %s\n", result.SessionID, result.LogPath)
	}
	if result.Error != "" {
		fmt.Fprintf(out, "Error: %s\n", result.Error)
	}
	return nil
}

// runProgramCTOTurn starts exactly one fresh, bounded, same-role CTO session
// that reconstructs durable program state and exits. It never resumes the live
// CEO-facing session and never sends input to a Herdr pane.
func runProgramCTOTurn(
	ctx context.Context,
	slug string,
	options programCTOTurnOptions,
) (result programCTOTurnResult, retErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result = programCTOTurnResult{
		Program: slug, Status: string(programturn.StatusSkipped), Fingerprint: options.fingerprint,
	}

	// The writer lock is taken first and held for the whole turn: a bounded turn
	// that is still running must make the next one skip, never queue.
	writer, err := acquireProgramWriter(slug)
	if err != nil {
		if errors.Is(err, programturn.ErrWriterBusy) {
			result.Reason = "another bounded program writer is already running"
			return result, nil
		}
		return result, err
	}
	defer func() {
		retErr = errors.Join(retErr, writer.Release())
	}()

	path, p, err := loadActiveProgram(slug)
	if err != nil {
		return result, err
	}
	switch p.State {
	case program.StateDraft, program.StatePendingApproval, program.StateActive, program.StateHeld:
	default:
		return result, fmt.Errorf(
			"program %q state %q cannot run a bounded automated CTO turn", slug, p.State,
		)
	}
	a, err := requireHeadlessTurnAgent(p)
	if err != nil {
		return result, err
	}
	readiness, err := requireHerdrRuntime("relay program cto turn", false)
	if err != nil {
		return result, err
	}
	if reason := gateOnLiveCTO(readiness.Agents, slug); reason != "" {
		result.Reason = reason
		if _, err := appendProgramTurn(slug, programturn.Record{
			Status: programturn.StatusSkipped, Reason: reason,
			Fingerprint: options.fingerprint, StartedAt: turnNow().Format(time.RFC3339),
		}); err != nil {
			return result, err
		}
		return result, nil
	}

	sessionID, err := newTurnSessionID()
	if err != nil {
		return result, err
	}
	startedAt := turnNow()
	programDir := filepath.Dir(path)
	logPath := programturn.LogPath(slug, startedAt, sessionID)
	turn, err := runHeadlessTurn(ctx, agent.HeadlessTurnRequest{
		Agent:      a,
		Repo:       p.Repo,
		ProgramDir: programDir,
		Prompt:     boundedCTOTurnPrompt(p, programDir, options),
		SessionID:  sessionID,
		LogPath:    logPath,
	}, agent.HeadlessTurnDeps{Now: turnNow})
	if err != nil {
		return result, err
	}

	status := programturn.StatusSucceeded
	switch {
	case turn.TimedOut:
		status = programturn.StatusTimedOut
	case turn.ExitCode != 0:
		status = programturn.StatusFailed
	}
	record := programturn.Record{
		SessionID:   turn.SessionID,
		Fingerprint: options.fingerprint,
		Status:      status,
		StartedAt:   turn.StartedAt.Format(time.RFC3339),
		EndedAt:     turn.EndedAt.Format(time.RFC3339),
		ExitCode:    turn.ExitCode,
		TimedOut:    turn.TimedOut,
		LogPath:     turn.LogPath,
		Error:       turn.Error,
	}
	result = programCTOTurnResult{
		Program: slug, Status: string(status), SessionID: record.SessionID,
		LogPath: record.LogPath, StartedAt: record.StartedAt, EndedAt: record.EndedAt,
		ExitCode: record.ExitCode, TimedOut: record.TimedOut,
		Fingerprint: options.fingerprint, Error: record.Error,
	}
	// The result is complete before the history is written, so a recording or
	// pruning failure still reports what the turn actually did.
	if _, err := appendProgramTurn(slug, record); err != nil {
		return result, err
	}
	if status == programturn.StatusSucceeded {
		return result, nil
	}
	return result, fmt.Errorf(
		"bounded CTO turn for program %q %s (exit %d); see %s%s",
		slug, status, record.ExitCode, record.LogPath, turnErrorSuffix(record.Error),
	)
}

func turnErrorSuffix(message string) string {
	if message == "" {
		return ""
	}
	return ": " + message
}

// requireHeadlessTurnAgent fails closed for agents whose CLI Relay cannot drive
// noninteractively. Guessing flags for an unverified agent would silently open
// an interactive session that nobody can answer.
func requireHeadlessTurnAgent(p program.Program) (agent.Agent, error) {
	a, err := agent.Get(p.Agent)
	if err != nil {
		return nil, fmt.Errorf("validate bounded turn agent for program %q: %w", p.Slug, err)
	}
	if a.Capabilities().HeadlessTurn {
		return a, nil
	}
	return nil, fmt.Errorf(
		"program %q uses %s, which Relay cannot run as a bounded noninteractive turn; "+
			"bounded patrol turns require an agent with verified headless flags (copilot today)—"+
			"recreate the program with copilot or drive this program from the interactive CTO session",
		p.Slug, a.Name(),
	)
}

// gateOnLiveCTO returns a skip reason unless exactly the program's CEO-facing
// CTO exists and is between turns. The pane itself is never focused or typed
// into; its status is only evidence that a second turn would collide.
func gateOnLiveCTO(agents []herdr.Agent, slug string) string {
	cto, err := herdr.FindLiveCTO(agents, slug)
	if err != nil {
		// Ambiguous ownership is reported verbatim: it names the panes the CEO
		// has to reconcile before any automated turn may run again.
		return err.Error()
	}
	switch cto.Status {
	case herdr.StatusIdle, herdr.StatusDone:
		return ""
	default:
		return fmt.Sprintf("CEO-facing CTO session for program %q is %s", slug, cto.Status)
	}
}

func boundedCTOTurnPrompt(p program.Program, programDir string, options programCTOTurnOptions) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Run the relay \"cto\" skill for program %s.\n\n", p.Slug)
	prompt.WriteString(
		"This is ONE bounded automated CTO turn started by the Relay patrol because durable " +
			"program state needs attention. You hold the same CTO role as the interactive session, " +
			"but no CEO is present and this session exits when the turn ends. The interactive " +
			"CEO-facing CTO pane is never typed into; do not prompt, focus, or send keys to any " +
			"Herdr pane.\n\n",
	)
	fmt.Fprintf(&prompt, "Program directory: %s\nRepository: %s\nState: %s\n", programDir, p.Repo, p.State)
	if options.fingerprint != "" {
		fmt.Fprintf(&prompt, "Attention fingerprint: %s\n", options.fingerprint)
	}
	if len(options.reasons) > 0 {
		prompt.WriteString("\nPatrol observed:\n")
		for _, reason := range options.reasons {
			fmt.Fprintf(&prompt, "- %s: %s\n", reason.Code, reason.Text)
		}
	}
	prompt.WriteString(`
1. Reconstruct durable state first; this session has no memory of previous turns:

   relay program message list ` + p.Slug + ` --json
   relay program status ` + p.Slug + ` --json
   relay program tick ` + p.Slug + ` --json
   relay program worker list ` + p.Slug + ` --json
   relay program patrol tick ` + p.Slug + ` --json

   Then read goal.md, decisions.md, open decisions, approved contracts, and the work-item
   dependency graph in the program directory.

2. Perform one bounded turn of routine governance, then stop. Allowed actions:
   - relay program message list/inbox/outbox/reply/ack
   - relay program decision open (idempotent: re-raising the same open question reuses it)
   - relay program grant-open-pr and revoke-open-pr
   - relay program dispatch
   - relay program worker start and worker notify
   - relay program tick, status, queue, can-open-pr

3. Never, in this automated turn:
   - approve, submit, finish, or abandon a program
   - hold or release a program (pausing the work is a CEO judgment)
   - resolve a decision, or approve or reject a contract
   - publish a contract version (immutable publication needs an interactive CTO/CEO turn)
   - add, update, or cancel a work item (plan shaping belongs to a CEO turn)
   - change the open pull request limit
   - approve, merge, or enable auto-merge on a pull request
   - invoke stack-ship or start a second orchestrator
   - wait, sleep, poll, loop, or block on a worker or a pull request
   RELAY_AUTOMATED_TURN=1 is set and the Relay CLI refuses every CEO-only command. When a
   matter needs the CEO, record it once with ` + "`relay program decision open`" + ` and stop.

   Everything you record durably is signed automatically as cto-automated:<session-prefix> and
   annotated "[automated CTO turn <session-prefix>, on behalf of CEO]". Do not pass --by,
   --raised-by, or any other actor flag trying to sign as cto, worker, or ceo: Relay overrides
   it, and claiming a human identity in a message body is a governance violation.

4. End with a concise digest: what durable state you found, what you did, and what is now
   waiting on the CEO. Then exit; do not start another turn.
`)
	return prompt.String()
}
