package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/patrol"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/spf13/cobra"
)

const (
	patrolPollInterval  = 100 * time.Millisecond
	herdrCommandTimeout = 5 * time.Second
)

var (
	patrolIsRunning      = patrol.IsRunning
	patrolReadState      = patrol.ReadState
	patrolRunLoop        = patrol.Run
	patrolTickOnce       = patrol.Tick
	newPatrolHerdrClient = func(ctx context.Context) herdrRuntimeClient {
		return herdr.NewClientWithCommandTimeout(ctx, herdrCommandTimeout)
	}
	patrolNow    = time.Now
	patrolSleep  = time.Sleep
	patrolSignal = func(pid int, signal os.Signal) error {
		process, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("find patrol process %d: %w", pid, err)
		}
		return process.Signal(signal)
	}
)

type programPatrolStartOutput struct {
	Program string       `json:"program"`
	Running bool         `json:"running"`
	Adopted bool         `json:"adopted"`
	State   patrol.State `json:"state"`
}

type programPatrolStatusOutput struct {
	Program    string        `json:"program"`
	Running    bool          `json:"running"`
	Status     string        `json:"status"`
	State      *patrol.State `json:"state,omitempty"`
	Error      string        `json:"error,omitempty"`
	StopReason string        `json:"stop_reason,omitempty"`
	Warning    string        `json:"warning,omitempty"`
}

func newCmdProgramPatrol() *cobra.Command {
	command := &cobra.Command{
		Use:   "patrol",
		Short: "Observe programs and wake the live tech lead when attention is needed",
	}
	command.AddCommand(
		newCmdProgramPatrolStart(),
		newCmdProgramPatrolRun(),
		newCmdProgramPatrolStatus(),
		newCmdProgramPatrolStop(),
		newCmdProgramPatrolTick(),
	)
	return command
}

func newCmdProgramPatrolStart() *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "start <slug>",
		Short: "Start or adopt a program patrol in the current Herdr workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runProgramPatrolStart(command.OutOrStdout(), args[0], jsonOutput)
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return command
}

func runProgramPatrolStart(out io.Writer, slug string, jsonOutput bool) error {
	_, p, err := loadActiveProgram(slug)
	if err != nil {
		return err
	}
	if err := validateProgramPatrolAgent(p); err != nil {
		return err
	}
	switch p.State {
	case program.StateDraft, program.StatePendingApproval, program.StateActive, program.StateHeld:
	default:
		return fmt.Errorf("program %q state %q cannot be patrolled", slug, p.State)
	}
	running, err := patrolIsRunning(slug)
	if err != nil {
		return err
	}
	if running {
		state, err := patrolReadState(slug)
		if err != nil {
			return err
		}
		return renderProgramPatrolStart(out, programPatrolStartOutput{
			Program: slug, Running: true, Adopted: true, State: state,
		}, jsonOutput)
	}
	readiness, err := requireHerdrRuntime("relay program patrol start", true)
	if err != nil {
		return err
	}
	client := newHerdrClient()
	tab, err := client.CreateTab(readiness.WorkspaceID, p.Repo, "relay-patrol:"+slug)
	if err != nil {
		return err
	}
	if err := client.RunPane(tab.RootPaneID, "relay program patrol run "+shellQuote(slug)); err != nil {
		return err
	}
	deadline := patrolNow().Add(10 * time.Second)
	// A state file that does not exist yet is normal while the patrol boots, but
	// any other read failure is the reason start never confirmed. Keep the last
	// one so the timeout names the cause instead of swallowing it.
	var lastStateErr error
	for {
		running, err := patrolIsRunning(slug)
		if err != nil {
			return err
		}
		if running {
			state, stateErr := patrolReadState(slug)
			switch {
			case stateErr == nil:
				if state.Status == patrol.StatusRunning {
					return renderProgramPatrolStart(out, programPatrolStartOutput{
						Program: slug, Running: true, State: state,
					}, jsonOutput)
				}
			case !errors.Is(stateErr, os.ErrNotExist):
				lastStateErr = stateErr
			}
		}
		if !patrolNow().Before(deadline) {
			return programPatrolStartTimeout(slug, tab.ID, lastStateErr)
		}
		patrolSleep(patrolPollInterval)
	}
}

// programPatrolStartTimeout reports why start never confirmed a running patrol,
// carrying the last real state read failure when there was one.
func programPatrolStartTimeout(slug, tabID string, stateErr error) error {
	if stateErr == nil {
		return fmt.Errorf(
			"patrol for program %q did not report running within 10s; inspect Herdr tab %s",
			slug, tabID,
		)
	}
	return fmt.Errorf(
		"patrol for program %q did not report running within 10s; its last patrol state read failed: %w; "+
			"inspect Herdr tab %s, then run `relay program patrol status %s` to see the recorded state",
		slug, stateErr, tabID, slug,
	)
}

func renderProgramPatrolStart(out io.Writer, result programPatrolStartOutput, jsonOutput bool) error {
	if jsonOutput {
		return writeProgramJSON(out, result)
	}
	if result.Adopted {
		fmt.Fprintf(out, "Patrol already running for %s (pid %d)\n", result.Program, result.State.PID)
		return nil
	}
	fmt.Fprintf(out, "Patrol running for %s (pid %d)\n", result.Program, result.State.PID)
	return nil
}

func newCmdProgramPatrolRun() *cobra.Command {
	command := &cobra.Command{
		Use:    "run <slug>",
		Short:  "Run a program patrol in the foreground",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			_, p, err := loadActiveProgram(args[0])
			if err != nil {
				return err
			}
			if err := validateProgramPatrolAgent(p); err != nil {
				return err
			}
			if _, err := requireHerdrRuntime("relay program patrol run", false); err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			client := newPatrolHerdrClient(ctx)
			options := patrol.Options{
				RelayVersion: version,
				Agents:       client,
				Turns:        liveTLTurnRunner{client: client},
				Notifier:     client,
				// The patrol pane is the patrol's log. High-level events go to
				// this process's own stdout and stderr; nothing is written to a
				// file, so the pane is where the CEO reads what the patrol did.
				Out: command.OutOrStdout(),
				Err: command.ErrOrStderr(),
			}
			if err := patrolRunLoop(ctx, args[0], options); err != nil {
				return err
			}
			state, err := patrolReadState(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "Patrol %s for %s\n", state.Status, args[0])
			return nil
		},
	}
	return command
}

func validateProgramPatrolAgent(p program.Program) error {
	a, err := agent.Get(p.Agent)
	if err != nil {
		return fmt.Errorf("validate patrol agent for program %q: %w", p.Slug, err)
	}
	if !a.Capabilities().NamedSessions {
		return fmt.Errorf(
			"program %q uses %s, whose launch adapter cannot carry named sessions; "+
				"patrol tech lead discovery requires SessionName—use a program launched with copilot or claude",
			p.Slug, a.Name(),
		)
	}
	return nil
}

const liveTLDoorbell = "Check Relay program mail and patrol state."

// liveTLTurnRunner submits a payload-free doorbell to the exact existing tech
// lead pane. PromptAgent writes Enter through terminal control, so focus is
// stable.
type liveTLTurnRunner struct {
	client herdrRuntimeClient
}

func (r liveTLTurnRunner) RunTurn(
	_ context.Context,
	request patrol.TurnRequest,
) (patrol.TurnResult, error) {
	if r.client == nil {
		return patrol.TurnResult{
			Status: patrol.TurnFailed,
			Error:  "live tech lead doorbell has no Herdr client",
		}, nil
	}
	if request.PaneID == "" {
		return patrol.TurnResult{
			Status: patrol.TurnFailed,
			Error:  "live tech lead doorbell has no target pane",
		}, nil
	}
	if err := r.client.PromptAgent(request.PaneID, liveTLDoorbell); err != nil {
		status := patrol.TurnFailed
		if errors.Is(err, herdr.ErrPromptDeliveryUncertain) {
			status = patrol.TurnUncertain
		}
		return patrol.TurnResult{Status: status, Error: err.Error()}, nil
	}
	return patrol.TurnResult{Status: patrol.TurnSucceeded}, nil
}

func newCmdProgramPatrolStatus() *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "status <slug>",
		Short: "Show program patrol runtime status",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runProgramPatrolStatus(command.OutOrStdout(), args[0], jsonOutput)
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return command
}

func runProgramPatrolStatus(out io.Writer, slug string, jsonOutput bool) error {
	running, err := patrolIsRunning(slug)
	if err != nil {
		return err
	}
	result := programPatrolStatusOutput{Program: slug, Running: running, Status: "not-running"}
	if running {
		result.Status = string(patrol.StatusRunning)
	}
	state, stateErr := patrolReadState(slug)
	if stateErr == nil {
		result.State = &state
		result.Error = state.Error
		result.StopReason = state.StopReason
		switch {
		case running:
			result.Status = string(state.Status)
		// A dead patrol still explains why it died, so failed and stopped
		// outcomes stay visible after the lock is released.
		case state.Status == patrol.StatusFailed || state.Status == patrol.StatusStopped:
			result.Status = string(state.Status)
		}
		result.Warning = appendPatrolWarning(result.Warning, state.Warning)
		if state.RelayVersion != "" && state.RelayVersion != version {
			result.Warning = appendPatrolWarning(result.Warning, fmt.Sprintf(
				"patrol version %s differs from relay version %s",
				state.RelayVersion, version,
			))
		}
	} else if !errors.Is(stateErr, os.ErrNotExist) || running {
		result.Warning = appendPatrolWarning(result.Warning, stateErr.Error())
	}
	if warning := programPatrolCapabilityWarning(slug); warning != "" {
		result.Warning = appendPatrolWarning(result.Warning, warning)
		if result.State != nil {
			result.State.TLPresent = false
		}
	}
	if jsonOutput {
		return writeProgramJSON(out, result)
	}
	fmt.Fprintf(out, "Patrol: %s\n", result.Status)
	if result.State != nil {
		fmt.Fprintf(out, "Last tick: %s\nNext tick: %s\n", result.State.LastTickAt, result.State.NextTickAt)
		fmt.Fprintf(out, "TL present: %t\n", result.State.TLPresent)
		renderProgramPatrolTurn(out, *result.State)
	}
	if result.Error != "" {
		fmt.Fprintf(out, "Error: %s\n", result.Error)
	}
	if result.StopReason != "" {
		fmt.Fprintf(out, "Stop reason: %s\n", result.StopReason)
	}
	if result.Warning != "" {
		fmt.Fprintf(out, "Warning: %s\n", result.Warning)
	}
	return nil
}

// renderProgramPatrolTurn reports the last live tech lead wake. Legacy session
// and log fields remain visible when reading state written by an older build.
func renderProgramPatrolTurn(out io.Writer, state patrol.State) {
	if state.LastTurnStatus == "" {
		return
	}
	fmt.Fprintf(out, "Last TL wake: %s", state.LastTurnStatus)
	if state.LastTurnEndedAt != "" {
		fmt.Fprintf(out, " at %s", state.LastTurnEndedAt)
	}
	if state.LastTurnSessionID != "" {
		fmt.Fprintf(out, " (session %s)", state.LastTurnSessionID)
	}
	fmt.Fprintln(out)
	if state.LastTurnLogPath != "" {
		fmt.Fprintf(out, "Legacy turn log: %s\n", state.LastTurnLogPath)
	}
	if state.LastTurnError != "" {
		fmt.Fprintf(out, "TL wake error: %s\n", state.LastTurnError)
	}
	if state.TurnFailures > 0 {
		fmt.Fprintf(out, "Consecutive TL wake failures: %d\n", state.TurnFailures)
	}
	if state.DoorbellSuppressed {
		fmt.Fprintln(out, "Automatic TL wakes suppressed until the tech lead composer is inspected and patrol is restarted")
	}
}

func programPatrolCapabilityWarning(slug string) string {
	path, err := program.Find(slug)
	if err != nil {
		return ""
	}
	p, err := program.Load(path)
	if err != nil {
		return fmt.Sprintf("load program %q for patrol capability status: %v", slug, err)
	}
	if err := validateProgramPatrolAgent(p); err != nil {
		return err.Error()
	}
	return ""
}

func appendPatrolWarning(current, next string) string {
	if current == "" {
		return next
	}
	if next == "" {
		return current
	}
	return current + "; " + next
}

func newCmdProgramPatrolStop() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <slug>",
		Short: "Stop a running program patrol",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runProgramPatrolStop(command.OutOrStdout(), args[0])
		},
	}
}

func runProgramPatrolStop(out io.Writer, slug string) error {
	running, err := patrolIsRunning(slug)
	if err != nil {
		return err
	}
	if !running {
		fmt.Fprintf(out, "Patrol for %s is not running\n", slug)
		return nil
	}
	state, err := patrolReadState(slug)
	if err != nil {
		return err
	}
	if state.PID <= 0 {
		return fmt.Errorf("patrol for program %q has invalid pid %d", slug, state.PID)
	}
	if err := patrolSignal(state.PID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop patrol for program %q: %w", slug, err)
	}
	deadline := patrolNow().Add(10 * time.Second)
	for {
		running, err := patrolIsRunning(slug)
		if err != nil {
			return err
		}
		if !running {
			fmt.Fprintf(out, "Patrol stopped for %s\n", slug)
			return nil
		}
		if !patrolNow().Before(deadline) {
			return fmt.Errorf("patrol for program %q did not stop within 10s", slug)
		}
		patrolSleep(patrolPollInterval)
	}
}

func newCmdProgramPatrolTick() *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "tick <slug>",
		Short: "Run one read-only patrol observation",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			options := patrol.Options{}
			if herdrAvailable() {
				options.Agents = newPatrolHerdrClient(command.Context())
			}
			observation, err := patrolTickOnce(args[0], options)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeProgramJSON(command.OutOrStdout(), observation)
			}
			fmt.Fprintf(command.OutOrStdout(), "Program: %s\nCadence: %dm\n",
				observation.ProgramSlug, observation.DelaySeconds/60)
			for _, reason := range observation.Reasons {
				fmt.Fprintf(command.OutOrStdout(), "- %s: %s\n", reason.Code, reason.Text)
			}
			if observation.Stop {
				fmt.Fprintf(command.OutOrStdout(), "Stop: %s\n", observation.StopReason)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return command
}
