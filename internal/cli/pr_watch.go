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

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/prwatch"
	"github.com/spf13/cobra"
)

const (
	prWatchStartTimeout = 10 * time.Second
	// prWatchLifecycleLockTimeout bounds how long start, stop, or cleanup waits
	// for another lifecycle mutation of the same watcher.
	prWatchLifecycleLockTimeout = 60 * time.Second
)

var (
	prWatchIsRunning       = prwatch.IsRunning
	prWatchReadState       = prwatch.ReadState
	prWatchReadStateLocked = prwatch.ReadStateLocked
	prWatchPrepareState    = prwatch.PrepareStateForStart
	prWatchUpdateState     = prwatch.UpdateState
	prWatchRunLoop         = prwatch.Run
	prWatchTickOnce        = prwatch.Tick
	prWatchLocate          = prwatch.LoadTarget
	prWatchRequireOwner    = prwatch.RequireLiveOwner
	prWatchRequireManaged  = prwatch.RequireManagedProject
	prWatchNow             = time.Now
	prWatchSleep           = time.Sleep
	prWatchSignal          = func(pid int, signal os.Signal) error {
		process, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("find pr watch process %d: %w", pid, err)
		}
		return process.Signal(signal)
	}
)

type prWatchStartOutput struct {
	Project      string         `json:"project"`
	Running      bool           `json:"running"`
	Adopted      bool           `json:"adopted"`
	OwnerPane    string         `json:"owner_pane,omitempty"`
	TabID        string         `json:"tab_id,omitempty"`
	TabReused    bool           `json:"tab_reused"`
	ClosedTabIDs []string       `json:"closed_tab_ids,omitempty"`
	Incomplete   bool           `json:"incomplete,omitempty"`
	Warning      string         `json:"warning,omitempty"`
	State        prwatch.State  `json:"state"`
	Target       prwatch.Target `json:"-"`
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
	result, err := startPRWatcher(slug, flags, "relay pr watch start")
	if err != nil {
		return err
	}
	return renderPRWatchStart(out, result, jsonOutput)
}

// startPRWatcher starts or adopts the single watcher process for one project.
// command names the caller's command so every refusal points at the command the
// operator actually ran.
func startPRWatcher(
	slug string, flags *prWatchModeFlags, command string,
) (result prWatchStartOutput, retErr error) {
	mode, owner, err := flags.resolve(slug)
	if err != nil {
		return prWatchStartOutput{}, err
	}
	target, err := prWatchLocate(slug)
	if err != nil {
		return prWatchStartOutput{}, err
	}
	// Lock ordering is lifecycle before state. The watcher process itself only
	// takes the state lock, so it can finish while stop holds lifecycle.
	lock, err := acquirePRWatchLifecycleLock(slug, command)
	if err != nil {
		return prWatchStartOutput{}, err
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			retErr = errors.Join(retErr, releaseErr)
		}
	}()

	readiness, err := requireHerdrRuntime(command, true)
	if err != nil {
		return prWatchStartOutput{}, err
	}
	running, err := prWatchIsRunning(slug)
	if err != nil {
		return prWatchStartOutput{}, err
	}
	if running {
		return adoptRunningPRWatcher(slug, mode, owner, readiness.WorkspaceID)
	}

	var warning string
	staleState, quarantined, diagnostic, err := prWatchPrepareState(slug, prWatchNow())
	if err != nil {
		return prWatchStartOutput{}, err
	}
	if quarantined != "" {
		warning = appendPatrolWarning(warning, fmt.Sprintf(
			"the watcher state for project %q was corrupt (%s) and has been quarantined at %s; "+
				"starting from a clean runtime record", slug, diagnostic, quarantined,
		))
	}
	// Everything that could make this watcher pointless is checked before a tab
	// exists, so a refused start never leaves an orphan process observing a
	// pull request on behalf of nobody.
	if mode == prwatch.ModeManaged {
		if err := prWatchRequireManaged(slug); err != nil {
			return prWatchStartOutput{}, err
		}
	}
	ownerAgent, err := prWatchRequireOwner(readiness.Agents, mode, slug, owner)
	if err != nil {
		return prWatchStartOutput{}, err
	}
	client := newHerdrClient()
	plan, err := preparePRWatchTab(client, readiness.WorkspaceID, target.Dir, slug, staleState)
	warning = appendPatrolWarning(warning, plan.warning)
	if err != nil {
		return prWatchStartOutput{}, err
	}
	tab := plan.tab
	if plan.reused {
		revalidated, reason, revalidateErr := recordedPRWatchTab(
			client, readiness.WorkspaceID, slug, staleState,
		)
		if revalidateErr != nil {
			return prWatchStartOutput{}, fmt.Errorf(
				"revalidate recorded PR watcher tab for project %q before reuse: %w", slug, revalidateErr,
			)
		}
		if reason != "" {
			warning = appendPatrolWarning(warning, fmt.Sprintf(
				"preserved recorded Herdr tab %s because %s; created a new watcher tab instead",
				staleState.TabID, reason,
			))
			tab, err = createPRWatchTab(
				client, readiness.WorkspaceID, target.Dir, prWatchWorkspaceLabel(slug),
			)
			if err != nil {
				return prWatchStartOutput{}, fmt.Errorf(
					"create replacement PR watcher tab for project %q after reuse revalidation failed: %w",
					slug, err,
				)
			}
			if tab.TerminalID == "" {
				warning = appendPatrolWarning(warning, missingPRWatchTerminalWarning(tab))
			}
			plan.reused = false
		} else {
			tab = revalidated
		}
	}
	runCommand := fmt.Sprintf(
		"relay pr watch run %s --mode %s --owner %s --workspace %s --tab %s --pane %s --terminal %s",
		shellQuote(slug), shellQuote(string(mode)), shellQuote(owner),
		shellQuote(readiness.WorkspaceID), shellQuote(tab.ID), shellQuote(tab.RootPaneID),
		shellQuote(tab.TerminalID),
	)
	if err := client.RunPane(tab.RootPaneID, runCommand); err != nil {
		kind := "new"
		if plan.reused {
			kind = "recorded"
		}
		return prWatchStartOutput{}, fmt.Errorf(
			"launch PR watcher for project %q in %s tab %s pane %s: %w",
			slug, kind, tab.ID, tab.RootPaneID, err,
		)
	}
	deadline := prWatchNow().Add(prWatchStartTimeout)
	// A state file that does not exist yet is normal while the watcher boots;
	// any other read failure is why start never confirmed, so keep the last one.
	var lastStateErr error
	for {
		running, err := prWatchIsRunning(slug)
		if err != nil {
			return prWatchStartOutput{}, err
		}
		if running {
			state, stateErr := prWatchReadState(slug)
			switch {
			case stateErr == nil:
				if state.Status == prwatch.StatusRunning {
					return prWatchStartOutput{
						Project: slug, Running: true, State: state,
						OwnerPane: ownerAgent.PaneID, TabID: tab.ID,
						TabReused: plan.reused, ClosedTabIDs: plan.closed,
						Warning: warning,
					}, nil
				}
			case !errors.Is(stateErr, os.ErrNotExist):
				lastStateErr = stateErr
			}
		}
		if !prWatchNow().Before(deadline) {
			return prWatchStartOutput{}, prWatchStartTimeoutError(slug, tab.ID, lastStateErr)
		}
		prWatchSleep(patrolPollInterval)
	}
}

// adoptRunningPRWatcher reports the watcher process that already observes this
// project. Mode or owner mismatches are refused; workspace drift is warned.
func adoptRunningPRWatcher(
	slug string, mode prwatch.Mode, owner, workspaceID string,
) (prWatchStartOutput, error) {
	state, err := prWatchReadState(slug)
	if err != nil {
		return prWatchStartOutput{}, err
	}
	if mismatch := adoptedWatcherWarning(slug, state, mode, owner); mismatch != "" {
		return prWatchStartOutput{}, errors.New(mismatch)
	}
	var warning string
	incomplete := state.WorkspaceID == "" || state.WorkspaceID != workspaceID
	if incomplete {
		recordedWorkspace := state.WorkspaceID
		if recordedWorkspace == "" {
			recordedWorkspace = "unknown"
		}
		warning = appendPatrolWarning(warning, fmt.Sprintf(
			"the running watcher for %s is recorded in Herdr workspace %s, not the current workspace %s; "+
				"it was left running and no duplicate was started",
			slug, recordedWorkspace, workspaceID,
		))
	}
	return prWatchStartOutput{
		Project: slug, Running: true, Adopted: true, State: state, TabID: state.TabID,
		Incomplete: incomplete, Warning: warning,
	}, nil
}

func acquirePRWatchLifecycleLock(slug, command string) (*patrollock.Lock, error) {
	lock, err := prwatch.AcquireLifecycleLock(slug, prWatchLifecycleLockTimeout)
	if err != nil {
		path := prwatch.LifecycleLockPath(slug)
		if errors.Is(err, patrollock.ErrLocked) {
			return nil, fmt.Errorf(
				"another pr watcher lifecycle operation for project %q has held %s for longer than %s; "+
					"wait for it to finish, then retry `%s`",
				slug, path, prWatchLifecycleLockTimeout, command,
			)
		}
		return nil, fmt.Errorf("lock pr watch lifecycle for project %q: %w", slug, err)
	}
	return lock, nil
}

// prWatchTabPlan records the tab a start will run the watcher in.
type prWatchTabPlan struct {
	tab     herdr.Tab
	reused  bool
	closed  []string
	warning string
}

// preparePRWatchTab reuses only the exact terminal recorded by Relay. A label
// is diagnostic metadata, never ownership proof; unrecorded matching tabs are
// preserved and reported.
func preparePRWatchTab(
	client herdrRuntimeClient,
	workspaceID, cwd, slug string, recorded prwatch.State,
) (prWatchTabPlan, error) {
	var plan prWatchTabPlan
	tabs, err := client.Tabs(workspaceID)
	if err != nil {
		return plan, err
	}
	panes, err := client.Panes(workspaceID)
	if err != nil {
		return plan, err
	}
	panesByTab := make(map[string][]herdr.Pane, len(panes))
	for _, pane := range panes {
		panesByTab[pane.TabID] = append(panesByTab[pane.TabID], pane)
	}

	label := prWatchWorkspaceLabel(slug)
	for _, tab := range tabs {
		if tab.Label != label || tab.ID == recorded.TabID {
			continue
		}
		plan.warning = appendPatrolWarning(plan.warning, fmt.Sprintf(
			"preserved unrecorded Herdr tab %s labeled %q; a label does not prove Relay owns the tab",
			tab.ID, label,
		))
	}

	if recorded.TabID != "" || recorded.PaneID != "" {
		reuse, reason := recordedPRWatchTabFromInventory(
			tabs, panesByTab, workspaceID, slug, recorded,
		)
		if reason == "" {
			plan.tab, plan.reused = reuse, true
			return plan, nil
		}
		plan.warning = appendPatrolWarning(plan.warning, fmt.Sprintf(
			"preserved recorded Herdr tab %s because %s", recorded.TabID, reason,
		))
	}

	plan.tab, err = createPRWatchTab(client, workspaceID, cwd, label)
	if err != nil {
		return plan, err
	}
	if plan.tab.TerminalID == "" {
		plan.warning = appendPatrolWarning(plan.warning, missingPRWatchTerminalWarning(plan.tab))
	}
	return plan, nil
}

func missingPRWatchTerminalWarning(tab herdr.Tab) string {
	return fmt.Sprintf(
		"Herdr did not report a terminal identity for new watcher tab %s pane %s; "+
			"Relay will preserve it during cleanup rather than risk closing a reused id",
		tab.ID, tab.RootPaneID,
	)
}

func createPRWatchTab(
	client herdrRuntimeClient, workspaceID, cwd, label string,
) (herdr.Tab, error) {
	tab, err := client.CreateTab(workspaceID, cwd, label)
	if err != nil {
		return herdr.Tab{}, err
	}
	if tab.WorkspaceID == "" {
		tab.WorkspaceID = workspaceID
	}
	if tab.TerminalID != "" {
		return tab, nil
	}
	panes, err := client.Panes(workspaceID)
	if err != nil {
		return herdr.Tab{}, fmt.Errorf(
			"identify newly created PR watcher pane %s in workspace %s: %w",
			tab.RootPaneID, workspaceID, err,
		)
	}
	for _, pane := range panes {
		if pane.ID == tab.RootPaneID && pane.TabID == tab.ID {
			tab.TerminalID = pane.TerminalID
			break
		}
	}
	return tab, nil
}

func recordedPRWatchTab(
	client herdrRuntimeClient, workspaceID, slug string, recorded prwatch.State,
) (herdr.Tab, string, error) {
	if reason := recordedPRWatchIdentityPrerequisite(workspaceID, slug, recorded); reason != "" {
		return herdr.Tab{}, reason, nil
	}
	tabs, err := client.Tabs(workspaceID)
	if err != nil {
		return herdr.Tab{}, "", err
	}
	panes, err := client.Panes(workspaceID)
	if err != nil {
		return herdr.Tab{}, "", err
	}
	panesByTab := make(map[string][]herdr.Pane, len(panes))
	for _, pane := range panes {
		panesByTab[pane.TabID] = append(panesByTab[pane.TabID], pane)
	}
	tab, reason := recordedPRWatchTabFromInventory(tabs, panesByTab, workspaceID, slug, recorded)
	return tab, reason, nil
}

func recordedPRWatchTabFromInventory(
	tabs []herdr.TabInfo,
	panesByTab map[string][]herdr.Pane,
	workspaceID, slug string,
	recorded prwatch.State,
) (herdr.Tab, string) {
	if reason := recordedPRWatchIdentityPrerequisite(workspaceID, slug, recorded); reason != "" {
		return herdr.Tab{}, reason
	}
	var tab herdr.TabInfo
	foundTab := false
	for _, candidate := range tabs {
		if candidate.ID == recorded.TabID {
			tab, foundTab = candidate, true
			break
		}
	}
	switch {
	case !foundTab:
		return herdr.Tab{}, "the recorded tab is no longer present"
	case tab.WorkspaceID != recorded.WorkspaceID:
		return herdr.Tab{}, fmt.Sprintf(
			"the recorded tab now belongs to workspace %s", tab.WorkspaceID,
		)
	case tab.Label != prWatchWorkspaceLabel(slug):
		return herdr.Tab{}, fmt.Sprintf("the recorded tab is now labeled %q", tab.Label)
	case tab.PaneCount != 1:
		return herdr.Tab{}, fmt.Sprintf("the recorded tab now holds %d panes", tab.PaneCount)
	case occupiedHerdrStatus(tab.Status):
		return herdr.Tab{}, fmt.Sprintf("the recorded tab now has agent status %q", tab.Status)
	}
	panes := panesByTab[recorded.TabID]
	if len(panes) != 1 {
		return herdr.Tab{}, fmt.Sprintf("Herdr now lists %d panes for the recorded tab", len(panes))
	}
	pane := panes[0]
	switch {
	case pane.ID != recorded.PaneID:
		return herdr.Tab{}, fmt.Sprintf("the recorded tab now contains pane %s", pane.ID)
	case pane.WorkspaceID != recorded.WorkspaceID:
		return herdr.Tab{}, fmt.Sprintf("the recorded pane now belongs to workspace %s", pane.WorkspaceID)
	case pane.TerminalID != recorded.TerminalID:
		return herdr.Tab{}, fmt.Sprintf("the recorded pane now belongs to terminal %s", pane.TerminalID)
	case occupiedHerdrStatus(pane.Status):
		return herdr.Tab{}, fmt.Sprintf("the recorded pane now has agent status %q", pane.Status)
	}
	return herdr.Tab{
		ID: recorded.TabID, RootPaneID: recorded.PaneID,
		WorkspaceID: recorded.WorkspaceID, TerminalID: recorded.TerminalID,
	}, ""
}

func recordedPRWatchIdentityPrerequisite(
	workspaceID, slug string, recorded prwatch.State,
) string {
	switch {
	case recorded.TabID == "" || recorded.PaneID == "":
		return "the runtime record does not name both a tab and pane"
	case recorded.WorkspaceID == "":
		return "the runtime record has no workspace identity"
	case recorded.WorkspaceID != workspaceID:
		return fmt.Sprintf(
			"the runtime record names workspace %s, not workspace %s",
			recorded.WorkspaceID, workspaceID,
		)
	case recorded.TerminalID == "":
		return "the runtime record has no terminal identity"
	case slug == "":
		return "the runtime record has no project identity"
	default:
		return ""
	}
}

// occupiedHerdrStatus reports a tab or pane Herdr sees an agent in. A watcher
// runs a plain command rather than an agent, so a reusable watcher tab reports
// no status at all or "unknown"; every other status names something live.
func occupiedHerdrStatus(status herdr.Status) bool {
	switch status {
	case "", herdr.StatusUnknown:
		return false
	default:
		return true
	}
}

func prWatchWorkspaceLabel(slug string) string {
	return "relay-pr-watch:" + slug
}

// adoptedWatcherWarning reports why an existing watcher's mode or owner prevents
// the caller from adopting it.
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
	fmt.Fprintf(out, "%s\n", prWatchTabSummary(result))
	if result.Warning != "" {
		fmt.Fprintf(out, "Warning: %s\n", result.Warning)
	}
	return nil
}

// prWatchOutcome reports whether a start launched this watcher or adopted the
// one already running.
func prWatchOutcome(result prWatchStartOutput) string {
	if result.Incomplete {
		return "incomplete"
	}
	if result.Adopted {
		return "adopted"
	}
	return "started"
}

// prWatchTabSummary describes what a start did to this project's watcher tabs.
func prWatchTabSummary(result prWatchStartOutput) string {
	tab := result.TabID
	if tab == "" {
		tab = "unknown"
	}
	summary := "Tab: " + tab
	switch {
	case result.Adopted:
		summary += " (already running)"
	case result.TabReused:
		summary += " (reused)"
	default:
		summary += " (created)"
	}
	if len(result.ClosedTabIDs) > 0 {
		summary += fmt.Sprintf(" closed recorded tabs: %s", strings.Join(result.ClosedTabIDs, ", "))
	}
	return summary
}

func newCmdPRWatchRun() *cobra.Command {
	flags := &prWatchModeFlags{}
	var workspaceID, tabID, paneID, terminalID string
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
				WorkspaceID:  workspaceID,
				TabID:        tabID,
				PaneID:       paneID,
				TerminalID:   terminalID,
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
	command.Flags().StringVar(&workspaceID, "workspace", "", "Herdr workspace hosting this watcher")
	command.Flags().StringVar(&tabID, "tab", "", "Herdr tab hosting this watcher, recorded for cleanup")
	command.Flags().StringVar(&paneID, "pane", "", "Herdr pane hosting this watcher, recorded for cleanup")
	command.Flags().StringVar(&terminalID, "terminal", "", "Herdr terminal identity recorded for safe cleanup")
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
		// finished, so complete, stopped, and failed outcomes stay visible.
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
			localTime(result.State.LastCheckAt), localTime(result.State.NextCheckAt),
			result.State.ScheduledChecks)
		if result.State.ConsecutiveErrors > 0 {
			fmt.Fprintf(out, "Consecutive errors: %d\n", result.State.ConsecutiveErrors)
		}
		fmt.Fprintf(out, "Actionable: %d\nCurrent digest: %s\n",
			result.State.ActionableCount, prWatchFingerprintLabel(result.State.CurrentFingerprint))
		if result.State.LastWakeStatus != "" {
			fmt.Fprintf(out, "Last owner wake: %s at %s\n",
				result.State.LastWakeStatus, localTime(result.State.LastWakeAt))
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
func runPRWatchStop(out io.Writer, slug string, jsonOutput bool) (retErr error) {
	lock, err := acquirePRWatchLifecycleLock(slug, "relay pr watch stop")
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			retErr = errors.Join(retErr, releaseErr)
		}
	}()

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
	result.Closed, result.Warning = stopPRWatchTab(slug, state)
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

// stopPRWatchTab closes the exact tab or pane the stopped watcher recorded and
// then clears it from the runtime record.
//
// The record is re-read under the state lock first, after the watcher process
// has actually stopped, and the close only happens if it still names the same
// watcher instance — same pid, same start, same tab and pane. Herdr reuses tab
// and pane ids, so a watcher somebody restarted while this stop was running
// would otherwise have its brand-new pane closed out from under it by an id
// that no longer means what it meant.
//
// Clearing the ids is what makes a second stop a no-op instead of a second
// close of an id that now belongs to somebody else. A close that did not
// happen keeps the ids, so the tab can still be found and closed later.
func stopPRWatchTab(slug string, state prwatch.State) (bool, string) {
	if state.TabID == "" && state.PaneID == "" {
		return false, ""
	}
	current, err := prWatchReadStateLocked(slug)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Sprintf(
			"the watcher process stopped, but its runtime record could not be re-read (%v), so its "+
				"%s was left open; close it with `herdr %s`",
			err, prWatchTabLabel(state), herdrCloseCommand(state),
		)
	}
	if !samePRWatchInstance(state, current) {
		return false, replacedPRWatchWarning(slug, state, current)
	}
	closed, warning := closePRWatchTab(state)
	if !closed {
		return closed, warning
	}
	if err := clearPRWatchTab(slug, state); err != nil {
		return true, appendPatrolWarning(warning, fmt.Sprintf(
			"the watcher's %s was closed, but the runtime record still names it: %v",
			prWatchTabLabel(state), err,
		))
	}
	return true, warning
}

// samePRWatchInstance reports whether a runtime record still names the exact
// watcher instance a stop set out to clean up. A pid can be recycled and a tab
// id can be reused, so identity is all of them together plus the moment that
// watcher started.
func samePRWatchInstance(before, current prwatch.State) bool {
	return before.PID == current.PID &&
		before.StartedAt == current.StartedAt &&
		before.WorkspaceID == current.WorkspaceID &&
		before.TabID == current.TabID &&
		before.PaneID == current.PaneID &&
		before.TerminalID == current.TerminalID
}

func replacedPRWatchWarning(slug string, before, current prwatch.State) string {
	if current.PID == 0 && current.StartedAt == "" {
		return fmt.Sprintf(
			"the watcher runtime record for %s disappeared while it was being stopped, so its %s "+
				"was not closed; close it with `herdr %s`",
			slug, prWatchTabLabel(before), herdrCloseCommand(before),
		)
	}
	return fmt.Sprintf(
		"a different watcher for %s is recorded now (pid %d started %s, was pid %d started %s), so "+
			"its %s was not closed — closing a reused id would take down the pane that is running "+
			"now; run `relay pr watch stop %s` again to stop and clean up the current watcher",
		slug, current.PID, current.StartedAt, before.PID, before.StartedAt,
		prWatchTabLabel(current), slug,
	)
}

// clearPRWatchTab blanks the closed tab and pane under the state lock, leaving
// every other field — including why the watcher stopped — exactly as it was.
// The identity is checked again inside the lock, so a watcher that started in
// the meantime never has its own tab erased from the record.
func clearPRWatchTab(slug string, closed prwatch.State) error {
	_, err := prWatchUpdateState(slug, func(state prwatch.State) (prwatch.State, error) {
		if !samePRWatchInstance(closed, state) {
			return state, fmt.Errorf(
				"the runtime record now names pid %d started %s, not the watcher that was stopped",
				state.PID, state.StartedAt,
			)
		}
		state.TabID = ""
		state.PaneID = ""
		state.WorkspaceID = ""
		state.TerminalID = ""
		state.UpdatedAt = prWatchNow().UTC().Format(time.RFC3339)
		return state, nil
	})
	return err
}

// closePRWatchTab closes the exact Herdr tab or pane the watcher recorded. It
// never guesses at one, and it never claims a close it did not make: with no
// recorded target or no Herdr, it returns the warning that names what is left.
func closePRWatchTab(state prwatch.State) (bool, string) {
	if state.TabID == "" && state.PaneID == "" {
		return false, ""
	}
	target := prWatchTabLabel(state)
	if !herdrAvailable() {
		return false, fmt.Sprintf(
			"Herdr is not available here, so the watcher's %s is still open; close it with "+
				"`herdr %s` from the workspace that hosts it",
			target, herdrCloseCommand(state),
		)
	}
	client := newHerdrClient()
	_, reason, err := recordedPRWatchTab(client, state.WorkspaceID, state.Project, state)
	if err != nil {
		return false, fmt.Sprintf(
			"the watcher process stopped, but its recorded %s could not be revalidated: %v; "+
				"it was preserved to avoid closing a reused id",
			target, err,
		)
	}
	if reason != "" {
		return false, fmt.Sprintf(
			"the watcher process stopped, but its recorded %s was preserved because %s; "+
				"close it manually with `herdr %s`",
			target, reason, herdrCloseCommand(state),
		)
	}
	var closeErr error
	if state.TabID != "" {
		closeErr = client.CloseTab(state.TabID)
	} else {
		closeErr = client.ClosePane(state.PaneID)
	}
	if closeErr != nil {
		return false, fmt.Sprintf(
			"the watcher process stopped, but its %s is still open: %v; close it with `herdr %s`",
			target, closeErr, herdrCloseCommand(state),
		)
	}
	return true, ""
}

func prWatchTabLabel(state prwatch.State) string {
	if state.TabID == "" {
		return "pane " + state.PaneID
	}
	return "tab " + state.TabID
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
