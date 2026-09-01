package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ronaknnathani/relay/internal/prwatch"
	"github.com/spf13/cobra"
)

const prWatchStartTimeout = 10 * time.Second

var (
	prWatchIsRunning      = prwatch.IsRunning
	prWatchReadState      = prwatch.ReadState
	prWatchRunLoop        = prwatch.Run
	prWatchTickOnce       = prwatch.Tick
	prWatchLocate         = prwatch.LoadTarget
	prWatchRequireOwner   = prwatch.RequireLiveOwner
	prWatchRequireManaged = prwatch.RequireManagedProject
	prWatchNow            = time.Now
	prWatchSleep          = time.Sleep
	prWatchSignal         = func(pid int, signal os.Signal) error {
		process, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("find pr watch process %d: %w", pid, err)
		}
		return process.Signal(signal)
	}
)

type prWatchStartOutput struct {
	Project   string         `json:"project"`
	Running   bool           `json:"running"`
	Adopted   bool           `json:"adopted"`
	OwnerPane string         `json:"owner_pane,omitempty"`
	TabID     string         `json:"tab_id,omitempty"`
	Warning   string         `json:"warning,omitempty"`
	State     prwatch.State  `json:"state"`
	Target    prwatch.Target `json:"-"`
}

type prWatchStopOutput struct {
	Project string `json:"project"`
	Stopped bool   `json:"stopped"`
	TabID   string `json:"tab_id,omitempty"`
	PaneID  string `json:"pane_id,omitempty"`
	Closed  bool   `json:"closed"`
	Warning string `json:"warning,omitempty"`
}

type prWatchStatusOutput struct {
	Project    string         `json:"project"`
	Running    bool           `json:"running"`
	Status     string         `json:"status"`
	State      *prwatch.State `json:"state,omitempty"`
	Error      string         `json:"error,omitempty"`
	StopReason string         `json:"stop_reason,omitempty"`
	Warning    string         `json:"warning,omitempty"`
}

// newCmdPR groups the project-scoped pull request commands.
func newCmdPR() *cobra.Command {
	command := &cobra.Command{
		Use:   "pr",
		Short: "Work with the pull request a project produced",
	}
	command.AddCommand(newCmdPRWatch())
	return command
}

func newCmdPRWatch() *cobra.Command {
	command := &cobra.Command{
		Use:   "watch",
		Short: "Observe a project's pull request and wake its owner when it needs attention",
	}
	command.AddCommand(
		newCmdPRWatchStart(),
		newCmdPRWatchRun(),
		newCmdPRWatchStatus(),
		newCmdPRWatchStop(),
		newCmdPRWatchTick(),
		newCmdPRWatchDigest(),
	)
	return command
}

// prWatchModeFlags carries the mode and owner every watcher entry point needs.
type prWatchModeFlags struct {
	mode  string
	owner string
}

func (f *prWatchModeFlags) bind(command *cobra.Command) {
	command.Flags().StringVar(&f.mode, "mode", string(prwatch.ModeStandalone),
		"watcher mode: standalone, managed, or stack")
	command.Flags().StringVar(&f.owner, "owner", "",
		"session slug to wake; required for --mode stack")
}

// resolve validates the mode and the owner the watcher will wake.
func (f *prWatchModeFlags) resolve(slug string) (prwatch.Mode, string, error) {
	mode, err := prwatch.ParseMode(f.mode)
	if err != nil {
		return "", "", err
	}
	owner, err := prwatch.OwnerSlug(mode, slug, f.owner)
	if err != nil {
		return "", "", err
	}
	return mode, owner, nil
}

func newCmdPRWatchStart() *cobra.Command {
	var jsonOutput bool
	flags := &prWatchModeFlags{}
	command := &cobra.Command{
		Use:   "start <project-slug>",
		Short: "Start or adopt a project PR watcher in the current Herdr workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPRWatchStart(command.OutOrStdout(), args[0], flags, jsonOutput)
		},
	}
	flags.bind(command)
	command.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return command
}

func runPRWatchStart(out io.Writer, slug string, flags *prWatchModeFlags, jsonOutput bool) error {
	mode, owner, err := flags.resolve(slug)
	if err != nil {
		return err
	}
	target, err := prWatchLocate(slug)
	if err != nil {
		return err
	}
	running, err := prWatchIsRunning(slug)
	if err != nil {
		return err
	}
	if running {
		state, err := prWatchReadState(slug)
		if err != nil {
			return err
		}
		return renderPRWatchStart(out, prWatchStartOutput{
			Project: slug, Running: true, Adopted: true, State: state,
			Warning: adoptedWatcherWarning(slug, state, mode, owner),
		}, jsonOutput)
	}
	readiness, err := requireHerdrRuntime("relay pr watch start", true)
	if err != nil {
		return err
	}
	// Everything that could make this watcher pointless is checked before a tab
	// exists, so a refused start never leaves an orphan process observing a
	// pull request on behalf of nobody.
	if mode == prwatch.ModeManaged {
		if err := prWatchRequireManaged(slug); err != nil {
			return err
		}
	}
	ownerAgent, err := prWatchRequireOwner(readiness.Agents, mode, slug, owner)
	if err != nil {
		return err
	}
	client := newHerdrClient()
	tab, err := client.CreateTab(readiness.WorkspaceID, target.Dir, "relay-pr-watch:"+slug)
	if err != nil {
		return err
	}
	runCommand := fmt.Sprintf(
		"relay pr watch run %s --mode %s --owner %s --tab %s --pane %s",
		shellQuote(slug), shellQuote(string(mode)), shellQuote(owner),
		shellQuote(tab.ID), shellQuote(tab.RootPaneID),
	)
	if err := client.RunPane(tab.RootPaneID, runCommand); err != nil {
		return err
	}
	deadline := prWatchNow().Add(prWatchStartTimeout)
	// A state file that does not exist yet is normal while the watcher boots;
	// any other read failure is why start never confirmed, so keep the last one.
	var lastStateErr error
	for {
		running, err := prWatchIsRunning(slug)
		if err != nil {
			return err
		}
		if running {
			state, stateErr := prWatchReadState(slug)
			switch {
			case stateErr == nil:
				if state.Status == prwatch.StatusRunning {
					return renderPRWatchStart(out, prWatchStartOutput{
						Project: slug, Running: true, State: state,
						OwnerPane: ownerAgent.PaneID, TabID: tab.ID,
					}, jsonOutput)
				}
			case !errors.Is(stateErr, os.ErrNotExist):
				lastStateErr = stateErr
			}
		}
		if !prWatchNow().Before(deadline) {
			return prWatchStartTimeoutError(slug, tab.ID, lastStateErr)
		}
		prWatchSleep(patrolPollInterval)
	}
}

// adoptedWatcherWarning reports an adopted watcher that is watching for someone
// other than the caller asked for, which a stack retarget must notice.
func adoptedWatcherWarning(slug string, state prwatch.State, mode prwatch.Mode, owner string) string {
	if state.Mode == mode && state.OwnerSlug == owner {
		return ""
	}
	return fmt.Sprintf(
		"the running watcher for %s is mode %s waking %s, not mode %s waking %s; "+
			"stop it with `relay pr watch stop %s` before starting a differently targeted watcher",
		slug, state.Mode, state.OwnerSlug, mode, owner, slug,
	)
}

func prWatchStartTimeoutError(slug, tabID string, stateErr error) error {
	if stateErr == nil {
		return fmt.Errorf(
			"pr watch for project %q did not report running within %s; inspect Herdr tab %s",
			slug, prWatchStartTimeout, tabID,
		)
	}
	return fmt.Errorf(
		"pr watch for project %q did not report running within %s; its last state read failed: %w; "+
			"inspect Herdr tab %s, then run `relay pr watch status %s` to see the recorded state",
		slug, prWatchStartTimeout, stateErr, tabID, slug,
	)
}

func renderPRWatchStart(out io.Writer, result prWatchStartOutput, jsonOutput bool) error {
	if jsonOutput {
		return writeProgramJSON(out, result)
	}
	verb := "PR watcher running"
	if result.Adopted {
		verb = "PR watcher already running"
	}
	fmt.Fprintf(out, "%s for %s (pid %d, mode %s, owner %s)\n",
		verb, result.Project, result.State.PID, result.State.Mode, result.State.OwnerSlug)
	if result.Warning != "" {
		fmt.Fprintf(out, "Warning: %s\n", result.Warning)
	}
	return nil
}

func newCmdPRWatchRun() *cobra.Command {
	flags := &prWatchModeFlags{}
	var tabID, paneID string
	command := &cobra.Command{
		Use:    "run <project-slug>",
		Short:  "Run a project PR watcher in the foreground",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			slug := args[0]
			mode, owner, err := flags.resolve(slug)
			if err != nil {
				return err
			}
			if _, err := requireHerdrRuntime("relay pr watch run", false); err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := prWatchRunLoop(ctx, slug, prwatch.Options{
				Mode:         mode,
				Owner:        owner,
				Client:       newPatrolHerdrClient(ctx),
				TabID:        tabID,
				PaneID:       paneID,
				RelayVersion: version,
				// The watcher pane is the watcher's log: routine events go to
				// this process's stdout and undelivered attention to its
				// stderr. Nothing is written to a file.
				Out: command.OutOrStdout(),
				Err: command.ErrOrStderr(),
			}); err != nil {
				return err
			}
			state, err := prWatchReadState(slug)
			if err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "PR watch %s for %s\n", state.Status, slug)
			// The watcher must not close its own tab: doing so races with
			// flushing this final line, and the pane is the only log there is.
			// Cleanup belongs to whoever stops it.
			if state.Status != prwatch.StatusRunning {
				fmt.Fprintf(command.OutOrStdout(),
					"Run `relay pr watch stop %s` to close this watcher tab\n", slug)
			}
			return nil
		},
	}
	flags.bind(command)
	command.Flags().StringVar(&tabID, "tab", "", "Herdr tab hosting this watcher, recorded for cleanup")
	command.Flags().StringVar(&paneID, "pane", "", "Herdr pane hosting this watcher, recorded for cleanup")
	return command
}

func newCmdPRWatchStatus() *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "status <project-slug>",
		Short: "Show project PR watcher runtime status",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPRWatchStatus(command.OutOrStdout(), args[0], jsonOutput)
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return command
}

func runPRWatchStatus(out io.Writer, slug string, jsonOutput bool) error {
	running, err := prWatchIsRunning(slug)
	if err != nil {
		return err
	}
	result := prWatchStatusOutput{Project: slug, Running: running, Status: "not-running"}
	if running {
		result.Status = string(prwatch.StatusRunning)
	}
	state, stateErr := prWatchReadState(slug)
	switch {
	case stateErr == nil:
		result.State = &state
		result.Error = state.Error
		result.StopReason = state.StopReason
		result.Warning = state.Warning
		// A watcher that is no longer holding the lock still explains why it
		// finished, so complete, stopped, and failed outcomes stay visible. A
		// record written by an acknowledgement alone carries no status, and
		// stays "not-running".
		if !running && state.Status != "" && state.Status != prwatch.StatusRunning {
			result.Status = string(state.Status)
		}
		if state.RelayVersion != "" && state.RelayVersion != version {
			result.Warning = appendPatrolWarning(result.Warning, fmt.Sprintf(
				"watcher version %s differs from relay version %s", state.RelayVersion, version,
			))
		}
	case !errors.Is(stateErr, os.ErrNotExist) || running:
		result.Warning = appendPatrolWarning(result.Warning, stateErr.Error())
	}
	if jsonOutput {
		return writeProgramJSON(out, result)
	}
	fmt.Fprintf(out, "PR watch: %s\n", result.Status)
	if result.State != nil {
		fmt.Fprintf(out, "Project: %s\nMode: %s\nOwner: %s\nPR: #%d %s\n",
			result.State.Project, result.State.Mode, result.State.OwnerSlug,
			result.State.PRNumber, result.State.PRState)
		fmt.Fprintf(out, "Last check: %s\nNext check: %s\nScheduled checks: %d\n",
			result.State.LastCheckAt, result.State.NextCheckAt, result.State.ScheduledChecks)
		fmt.Fprintf(out, "Actionable: %d\nCurrent digest: %s\n",
			result.State.ActionableCount, prWatchFingerprintLabel(result.State.CurrentFingerprint))
		if result.State.LastWakeStatus != "" {
			fmt.Fprintf(out, "Last owner wake: %s at %s\n",
				result.State.LastWakeStatus, result.State.LastWakeAt)
		}
		if result.State.WakesSuppressed {
			fmt.Fprintln(out,
				"Automatic wakes are suppressed until the owner composer is inspected and the watcher is restarted")
		}
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

func prWatchFingerprintLabel(fingerprint string) string {
	if fingerprint == "" {
		return "none"
	}
	return fingerprint
}

func newCmdPRWatchStop() *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "stop <project-slug>",
		Short: "Stop a running project PR watcher and close its Herdr tab",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPRWatchStop(command.OutOrStdout(), args[0], jsonOutput)
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return command
}

// runPRWatchStop signals the exact recorded process, waits for it to release
// the watcher lock, then closes the exact recorded tab. A watcher that already
// finished on its own still leaves its tab behind, so cleanup runs either way.
func runPRWatchStop(out io.Writer, slug string, jsonOutput bool) error {
	running, err := prWatchIsRunning(slug)
	if err != nil {
		return err
	}
	state, stateErr := prWatchReadState(slug)
	if stateErr != nil && (running || !errors.Is(stateErr, os.ErrNotExist)) {
		return stateErr
	}
	result := prWatchStopOutput{
		Project: slug, Stopped: !running, TabID: state.TabID, PaneID: state.PaneID,
	}
	if running {
		if state.PID <= 0 {
			return fmt.Errorf("pr watch for project %q has invalid pid %d", slug, state.PID)
		}
		if err := prWatchSignal(state.PID, syscall.SIGTERM); err != nil {
			return fmt.Errorf("stop pr watch for project %q: %w", slug, err)
		}
		if err := awaitPRWatchExit(slug); err != nil {
			return err
		}
		result.Stopped = true
	}
	result.Closed, result.Warning = closePRWatchTab(state)
	if jsonOutput {
		return writeProgramJSON(out, result)
	}
	renderPRWatchStop(out, result, running)
	return nil
}

func awaitPRWatchExit(slug string) error {
	deadline := prWatchNow().Add(prWatchStartTimeout)
	for {
		running, err := prWatchIsRunning(slug)
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		if !prWatchNow().Before(deadline) {
			return fmt.Errorf(
				"pr watch for project %q did not stop within %s", slug, prWatchStartTimeout,
			)
		}
		prWatchSleep(patrolPollInterval)
	}
}

// closePRWatchTab closes the exact Herdr tab or pane the watcher recorded. It
// never guesses at one, and it never claims a close it did not make: with no
// recorded target or no Herdr, it returns the warning that names what is left.
func closePRWatchTab(state prwatch.State) (bool, string) {
	if state.TabID == "" && state.PaneID == "" {
		return false, ""
	}
	target := "tab " + state.TabID
	if state.TabID == "" {
		target = "pane " + state.PaneID
	}
	if !herdrAvailable() {
		return false, fmt.Sprintf(
			"Herdr is not available here, so the watcher's %s is still open; close it with "+
				"`herdr %s` from the workspace that hosts it",
			target, herdrCloseCommand(state),
		)
	}
	client := newHerdrClient()
	var err error
	if state.TabID != "" {
		err = client.CloseTab(state.TabID)
	} else {
		err = client.ClosePane(state.PaneID)
	}
	if err != nil {
		return false, fmt.Sprintf(
			"the watcher process stopped, but its %s is still open: %v; close it with `herdr %s`",
			target, err, herdrCloseCommand(state),
		)
	}
	return true, ""
}

func herdrCloseCommand(state prwatch.State) string {
	if state.TabID != "" {
		return "tab close " + state.TabID
	}
	return "pane close " + state.PaneID
}

func renderPRWatchStop(out io.Writer, result prWatchStopOutput, wasRunning bool) {
	switch {
	case wasRunning:
		fmt.Fprintf(out, "PR watcher stopped for %s\n", result.Project)
	default:
		fmt.Fprintf(out, "PR watcher for %s is not running\n", result.Project)
	}
	if result.Closed {
		fmt.Fprintf(out, "Closed watcher %s\n", strings.TrimSpace(herdrCloseTarget(result)))
	}
	if result.Warning != "" {
		fmt.Fprintf(out, "Warning: %s\n", result.Warning)
	}
}

func herdrCloseTarget(result prWatchStopOutput) string {
	if result.TabID != "" {
		return "tab " + result.TabID
	}
	return "pane " + result.PaneID
}

func newCmdPRWatchTick() *cobra.Command {
	var jsonOutput bool
	flags := &prWatchModeFlags{}
	command := &cobra.Command{
		Use:   "tick <project-slug>",
		Short: "Run one read-only observation and record its digest",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			slug := args[0]
			mode, err := prwatch.ParseMode(flags.mode)
			if err != nil {
				return err
			}
			digest, err := prWatchTickOnce(command.Context(), slug, prwatch.Options{Mode: mode})
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeProgramJSON(command.OutOrStdout(), digest)
			}
			renderPRWatchDigest(command.OutOrStdout(), digest)
			return nil
		},
	}
	command.Flags().StringVar(&flags.mode, "mode", string(prwatch.ModeStandalone),
		"watcher mode used when no watcher has recorded one: standalone, managed, or stack")
	command.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return command
}

func newCmdPRWatchDigest() *cobra.Command {
	var jsonOutput bool
	var fingerprint string
	command := &cobra.Command{
		Use:   "digest <project-slug> --fingerprint <fingerprint>",
		Short: "Read one recorded watcher digest",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			digest, err := prwatch.ReadDigest(args[0], fingerprint)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeProgramJSON(command.OutOrStdout(), digest)
			}
			renderPRWatchDigest(command.OutOrStdout(), digest)
			return nil
		},
	}
	command.Flags().StringVar(&fingerprint, "fingerprint", "", "digest fingerprint (64 lowercase hex characters)")
	command.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	_ = command.MarkFlagRequired("fingerprint")
	return command
}

// renderPRWatchDigest prints a digest summary. Bodies stay out of the terminal
// summary; `--json` is the way to hand the full record to the owner that acts
// on it.
func renderPRWatchDigest(out io.Writer, digest prwatch.Digest) {
	fmt.Fprintf(out, "Project: %s\nPR: #%d %s\nObserved: %s\nFingerprint: %s\n",
		digest.Project, digest.PR.Number, digest.PR.State, digest.ObservedAt,
		prWatchFingerprintLabel(digest.Fingerprint))
	if digest.Complete {
		fmt.Fprintln(out, "Complete: the pull request merged")
	}
	if len(digest.Waiting) > 0 {
		fmt.Fprintf(out, "Waiting: %s\n", strings.Join(digest.Waiting, ", "))
	}
	if len(digest.Items) == 0 {
		fmt.Fprintln(out, "Actionable: none")
		return
	}
	fmt.Fprintf(out, "Actionable: %d\n", len(digest.Items))
	for _, item := range digest.Items {
		line := fmt.Sprintf("- %s %s %s", item.Reason, item.Source, item.ID)
		if item.Path != "" {
			line += fmt.Sprintf(" %s:%d", item.Path, item.Line)
		}
		if item.CheckRunID != "" {
			line += " run=" + item.CheckRunID
		}
		fmt.Fprintln(out, line)
	}
	fmt.Fprintln(out, "Run with --json for the full record, including comment bodies.")
}
