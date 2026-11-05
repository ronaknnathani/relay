package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/spf13/cobra"
)

// Kinds of Relay-created tab the sweep inspects.
const (
	sweepKindPRWatch = "pr-watch"
	sweepKindWorker  = "worker"
	sweepKindPatrol  = "patrol"
)

// Reasons the sweep reports for a closed or preserved tab.
const (
	sweepReasonWatcherNotRunning = "watcher not running"
	sweepReasonWatcherRunning    = "watcher running"
	sweepReasonNoWatcherRecord   = "no watcher record"
	sweepReasonOtherWatcherTab   = "watcher record names a different tab"
	sweepReasonPatrolNotRunning  = "patrol not running"
	sweepReasonPatrolRunning     = "patrol running"
	sweepReasonTechLeadTab       = "tech lead tab"
	sweepReasonWorkerBusy        = "worker became busy"
	sweepReasonPaneReplaced      = "pane now hosts a different session"
	sweepReasonForeignSession    = "tab hosts a session that is not the item's Relay worker"
	sweepReasonOutsideWorktree   = "pane is not rooted in the item's worktree"
	sweepReasonNoPanes           = "tab lists no panes"
	sweepReasonInvalidSlug       = "label names no valid slug"
)

// programSweepEntry is one tab the sweep closed, kept, or failed to close.
type programSweepEntry struct {
	// TabID is empty only for a worker error that names no single tab, such as
	// several sessions claiming one project.
	TabID string `json:"tab_id,omitempty"`
	Kind  string `json:"kind"`
	// Subject is the project slug, item id, or program slug the tab belongs to.
	Subject string `json:"subject"`
	// Reason says why the tab was closed or preserved, or what went wrong.
	Reason string `json:"reason"`
}

type programSweepOutput struct {
	Program     string              `json:"program"`
	WorkspaceID string              `json:"workspace_id"`
	Closed      []programSweepEntry `json:"closed"`
	Preserved   []programSweepEntry `json:"preserved"`
	Errors      []programSweepEntry `json:"errors"`
}

// programSweep holds what one sweep run needs while it walks the workspace.
type programSweep struct {
	client      herdrRuntimeClient
	workspaceID string
	tlTabID     string
	command     string
	output      *programSweepOutput
}

func newCmdProgramSweep() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "sweep <program>",
		Short: "Close Relay tabs in this workspace that are no longer needed",
		Long: "Close the Relay-created tabs in the current Herdr workspace that nothing needs anymore.\n\n" +
			"A pull request watcher's tab is closed once its watcher has stopped, through the exact\n" +
			"tab its runtime record names. A worker's tab, found by the label Relay gave it, is\n" +
			"closed once its item is merged or canceled and it hosts either the item's own idle\n" +
			"or done session, which is asked to exit first, or no agent in the item's worktree.\n" +
			"A patrol tab is closed once no patrol runs for its program. The tech lead's own tab, any busy\n" +
			"agent, running watchers and patrols, tabs Relay did not create, and other workspaces\n" +
			"are never touched. The sweep never archives anything.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramSweep(cmd.OutOrStdout(), args[0], jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

// runProgramSweep closes every finished Relay tab in the current workspace and
// reports each tab it closed, preserved, or failed on. One tab's failure never
// stops the rest; all failures are reported together.
func runProgramSweep(out io.Writer, programSlug string, jsonOutput bool) error {
	_, p, err := loadActiveProgram(programSlug)
	if err != nil {
		return err
	}
	command := "relay program sweep " + p.Slug
	readiness, err := requireHerdrRuntime(command, true)
	if err != nil {
		return err
	}
	sweep := programSweep{
		client:      newHerdrClient(),
		workspaceID: readiness.WorkspaceID,
		command:     command,
		output: &programSweepOutput{
			Program: p.Slug, WorkspaceID: readiness.WorkspaceID,
			Closed: []programSweepEntry{}, Preserved: []programSweepEntry{}, Errors: []programSweepEntry{},
		},
	}
	tl, err := herdr.FindLiveTL(readiness.Agents, p.Slug)
	switch {
	case err == nil:
		sweep.tlTabID = tl.TabID
	case !errors.Is(err, herdr.ErrNoLiveTL):
		return fmt.Errorf("sweep %s: %w; nothing was closed", p.Slug, err)
	}
	tabs, err := sweep.client.Tabs(sweep.workspaceID)
	if err != nil {
		return fmt.Errorf("sweep %s: list tabs in workspace %s: %w", p.Slug, sweep.workspaceID, err)
	}

	for _, tab := range tabs {
		if tab.WorkspaceID != sweep.workspaceID {
			continue
		}
		switch {
		case strings.HasPrefix(tab.Label, prWatchTabLabelPrefix):
			sweep.sweepLabeledTab(tab, sweepKindPRWatch, prWatchTabLabelPrefix, sweep.closeFinishedPRWatchTab)
		case strings.HasPrefix(tab.Label, patrolTabLabelPrefix):
			sweep.sweepLabeledTab(tab, sweepKindPatrol, patrolTabLabelPrefix, sweep.closeDeadPatrolTab)
		}
	}
	sweep.sweepWorkerTabs(p, tabs, readiness.Agents)

	if err := renderProgramSweep(out, *sweep.output, jsonOutput); err != nil {
		return err
	}
	if failed := len(sweep.output.Errors); failed > 0 {
		return fmt.Errorf("sweep %s: %d error(s), each listed under errors", p.Slug, failed)
	}
	return nil
}

// record files one tab under closed, preserved, or errors. A tab can be both
// closed and in error when the close happened but a follow-up step failed.
func (s *programSweep) record(entry programSweepEntry, closed bool, reason string, err error) {
	if err != nil {
		failed := entry
		failed.Reason = err.Error()
		s.output.Errors = append(s.output.Errors, failed)
	}
	entry.Reason = reason
	switch {
	case closed:
		s.output.Closed = append(s.output.Closed, entry)
	case err == nil:
		s.output.Preserved = append(s.output.Preserved, entry)
	}
}

// sweepLabeledTab applies the checks every label-identified kind shares, then
// hands the tab to closeTab, which proves the tab is finished before closing it.
func (s *programSweep) sweepLabeledTab(
	tab herdr.TabInfo, kind, prefix string,
	closeTab func(tab herdr.TabInfo, slug string) (bool, string, error),
) {
	slug := strings.TrimPrefix(tab.Label, prefix)
	entry := programSweepEntry{TabID: tab.ID, Kind: kind, Subject: slug}
	switch {
	case s.isTechLeadTab(tab.ID):
		s.record(entry, false, sweepReasonTechLeadTab, nil)
	case project.ValidateSlug(slug) != nil:
		s.record(entry, false, sweepReasonInvalidSlug, nil)
	case occupiedHerdrStatus(tab.Status):
		s.record(entry, false, "agent "+string(tab.Status), nil)
	default:
		closed, reason, err := closeTab(tab, slug)
		s.record(entry, closed, reason, err)
	}
}

func (s *programSweep) isTechLeadTab(tabID string) bool {
	return s.tlTabID != "" && tabID == s.tlTabID
}

// closeFinishedPRWatchTab closes a watcher tab only when its watcher is not
// running and the watcher's runtime record names this exact tab. The close
// goes through the same identity check `relay pr watch stop` uses, and a
// running watcher is never signaled.
func (s *programSweep) closeFinishedPRWatchTab(tab herdr.TabInfo, slug string) (_ bool, _ string, retErr error) {
	lock, err := acquirePRWatchLifecycleLock(slug, s.command)
	if err != nil {
		return false, "", err
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf(
				"release pr watch lifecycle lock for project %q: %w", slug, releaseErr,
			))
		}
	}()
	running, err := prWatchIsRunning(slug)
	if err != nil {
		return false, "", fmt.Errorf("check pr watcher for project %q: %w", slug, err)
	}
	if running {
		return false, sweepReasonWatcherRunning, nil
	}
	state, err := prWatchReadState(slug)
	if errors.Is(err, os.ErrNotExist) {
		return false, sweepReasonNoWatcherRecord, nil
	}
	if err != nil {
		return false, "", fmt.Errorf("read pr watcher record for project %q: %w", slug, err)
	}
	if state.TabID != tab.ID || state.WorkspaceID != s.workspaceID {
		return false, sweepReasonOtherWatcherTab, nil
	}
	closed, warning := stopPRWatchTab(slug, state)
	if warning != "" {
		return closed, sweepReasonWatcherNotRunning, errors.New(warning)
	}
	return closed, sweepReasonWatcherNotRunning, nil
}

// closeDeadPatrolTab closes a patrol tab only when no patrol holds its
// program's lock. Liveness is checked immediately before the close so a patrol
// that started during the sweep keeps its tab.
func (s *programSweep) closeDeadPatrolTab(tab herdr.TabInfo, slug string) (bool, string, error) {
	if tab.PaneCount != 1 {
		return false, fmt.Sprintf("tab holds %d panes", tab.PaneCount), nil
	}
	running, err := patrolIsRunning(slug)
	if err != nil {
		return false, "", fmt.Errorf("check patrol for program %q: %w", slug, err)
	}
	if running {
		return false, sweepReasonPatrolRunning, nil
	}
	if err := s.client.CloseTab(tab.ID); err != nil {
		return false, "", fmt.Errorf("close patrol tab %s for program %q: %w", tab.ID, slug, err)
	}
	return true, sweepReasonPatrolNotRunning, nil
}

// sweepWorkerTabs closes the worker tabs of this program's merged and canceled
// items. A tab qualifies only when it carries the label Relay gave the item's
// worker tab at launch, and then only when it hosts the item's own Relay
// session or no agent at all; closeWorkerTab decides which.
func (s *programSweep) sweepWorkerTabs(p program.Program, tabs []herdr.TabInfo, agents []herdr.Agent) {
	local := make([]herdr.Agent, 0, len(agents))
	for _, agent := range agents {
		if agent.WorkspaceID == s.workspaceID {
			local = append(local, agent)
		}
	}
	for _, item := range p.Items {
		if item.ProjectSlug == "" || item.ProjectWorktree == "" ||
			(item.Status != program.ItemMerged && item.Status != program.ItemCancelled) {
			continue
		}
		label := workerTabLabel(item)
		var labeled []herdr.TabInfo
		for _, tab := range tabs {
			if tab.WorkspaceID == s.workspaceID && tab.Label == label {
				labeled = append(labeled, tab)
			}
		}
		if len(labeled) == 0 {
			continue
		}
		owner, err := herdr.FindLiveProjectOwner(local, item.ProjectSlug)
		if err != nil && !errors.Is(err, herdr.ErrNoLiveProjectOwner) {
			s.record(programSweepEntry{Kind: sweepKindWorker, Subject: item.ID}, false, "", err)
			continue
		}
		for _, tab := range labeled {
			entry := programSweepEntry{TabID: tab.ID, Kind: sweepKindWorker, Subject: item.ID}
			closed, reason, err := s.closeWorkerTab(item, tab, owner, local)
			s.record(entry, closed, reason, err)
		}
	}
}

// closeWorkerTab closes one labeled worker tab. When the tab hosts the item's
// own Relay session, that session must be idle or done and is asked to exit
// first. When it hosts no agent, every pane must still be rooted in the item's
// worktree, which is what a worker that already exited leaves behind. Any
// other session, such as the user's own agent in the worktree, keeps its tab.
func (s *programSweep) closeWorkerTab(
	item program.WorkItem, tab herdr.TabInfo, owner herdr.Agent, agents []herdr.Agent,
) (_ bool, _ string, retErr error) {
	if s.isTechLeadTab(tab.ID) {
		return false, sweepReasonTechLeadTab, nil
	}
	var hosted []herdr.Agent
	for _, agent := range agents {
		if agent.TabID == tab.ID {
			hosted = append(hosted, agent)
		}
	}
	ownsTab := len(hosted) == 1 && hosted[0].PaneID == owner.PaneID
	if len(hosted) > 0 && !ownsTab {
		return false, sweepReasonForeignSession, nil
	}
	if ownsTab && (owner.Status == herdr.StatusWorking || owner.Status == herdr.StatusBlocked) {
		return false, "agent " + string(owner.Status), nil
	}
	lock, err := acquireWorkerStartLock(item.ProjectSlug, s.command)
	if err != nil {
		return false, "", err
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf(
				"release worker start lock for project %q: %w", item.ProjectSlug, releaseErr,
			))
		}
	}()
	if ownsTab {
		return s.closeIdleWorker(item, owner)
	}
	return s.closeExitedWorkerTab(item, tab)
}

// closeIdleWorker asks an idle or done worker to exit and then closes its exact
// tab. The caller holds the item's worker start lock.
func (s *programSweep) closeIdleWorker(item program.WorkItem, worker herdr.Agent) (bool, string, error) {
	identity := worker.Identity()
	exit, err := s.client.ExitAgent(identity)
	if errors.Is(err, herdr.ErrAgentBusy) {
		return false, sweepReasonWorkerBusy, nil
	}
	if err != nil {
		return false, "", fmt.Errorf("exit worker for item %s in pane %s: %w", item.ID, worker.PaneID, err)
	}
	if exit.Outcome == herdr.ExitedReplaced {
		return false, sweepReasonPaneReplaced, nil
	}
	if _, err := closeCleanupWorkerTab(s.client, identity); err != nil {
		return false, "", fmt.Errorf(
			"the worker for item %s exited, but its tab %s could not be closed: %w",
			item.ID, identity.TabID, err,
		)
	}
	return true, fmt.Sprintf("item %s; worker %s", item.Status, worker.Status), nil
}

// closeExitedWorkerTab closes a worker tab whose agent is gone, but only when
// every pane in it is still rooted in the item's worktree. Herdr is re-read
// before the close, so an agent that started there meanwhile keeps the tab.
// The caller holds the item's worker start lock.
func (s *programSweep) closeExitedWorkerTab(item program.WorkItem, tab herdr.TabInfo) (bool, string, error) {
	panes, err := s.client.Panes(s.workspaceID)
	if err != nil {
		return false, "", fmt.Errorf("list panes of worker tab %s for item %s: %w", tab.ID, item.ID, err)
	}
	rooted := 0
	for _, pane := range panes {
		if pane.TabID != tab.ID {
			continue
		}
		if !pane.RootedIn(item.ProjectWorktree) {
			return false, sweepReasonOutsideWorktree, nil
		}
		rooted++
	}
	if rooted == 0 {
		return false, sweepReasonNoPanes, nil
	}
	if _, err := closeCleanupWorkerTab(s.client, herdr.SessionIdentity{TabID: tab.ID}); err != nil {
		return false, "", fmt.Errorf("close exited worker tab %s for item %s: %w", tab.ID, item.ID, err)
	}
	return true, fmt.Sprintf("item %s; worker exited", item.Status), nil
}

func renderProgramSweep(out io.Writer, result programSweepOutput, jsonOutput bool) error {
	if jsonOutput {
		return writeProgramJSON(out, result)
	}
	for _, group := range []struct {
		label   string
		entries []programSweepEntry
	}{
		{"closed", result.Closed},
		{"preserved", result.Preserved},
		{"error", result.Errors},
	} {
		for _, entry := range group.entries {
			tab := ""
			if entry.TabID != "" {
				tab = " tab " + entry.TabID
			}
			fmt.Fprintf(out, "%s %s %s%s: %s\n", group.label, entry.Kind, entry.Subject, tab, entry.Reason)
		}
	}
	return nil
}
