package cli

import (
	"bytes"
	"encoding/json"
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

// Cleanup outcomes for one merged item's worker.
const (
	// cleanupClean means the watcher is stopped, the worker session is gone,
	// its tab is closed, and the child project is archived.
	cleanupClean = "clean"
	// cleanupWorkerBusy means the worker is still working or blocked. Nothing
	// was torn down: a busy session is never interrupted.
	cleanupWorkerBusy = "worker-busy"
)

// Worker exit outcomes cleanup reports.
const (
	cleanupWorkerExited  = "exited"
	cleanupWorkerAbsent  = "absent"
	cleanupWorkerRunning = "running"
)

type programWorkerCleanupOutput struct {
	Program         string         `json:"program"`
	Item            string         `json:"item"`
	Project         string         `json:"project"`
	Status          string         `json:"status"`
	WatcherStopped  bool           `json:"watcher_stopped"`
	WorkerExit      string         `json:"worker_exit"`
	WorkerStatus    string         `json:"worker_status,omitempty"`
	WorkerPane      string         `json:"worker_pane,omitempty"`
	TabClosed       bool           `json:"tab_closed"`
	Archived        bool           `json:"archived"`
	AlreadyArchived bool           `json:"already_archived"`
	Archive         *archiveResult `json:"archive,omitempty"`
	NextCommand     string         `json:"next_command,omitempty"`
	Warnings        []string       `json:"warnings"`
}

func newCmdProgramWorkerCleanup() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "cleanup <program> <item>",
		Short: "Retire a merged item's watcher, worker, tab, and child project",
		Long: "Retire everything a merged work item still holds open.\n\n" +
			"Only an item Relay records as merged qualifies. The pull request watcher is stopped\n" +
			"first and its recorded tab is closed — a watcher that already finished on its own keeps\n" +
			"that tab so its last lines stay readable, and this is the command that closes it. Then\n" +
			"the item's one worker session is asked to exit and its exact tab is closed, and finally\n" +
			"the child project is archived with --force, which discards any dirty or untracked files\n" +
			"left in its worktree and removes the branch.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramWorkerCleanup(cmd.OutOrStdout(), args[0], args[1], jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

// runProgramWorkerCleanup tears down a merged item's runtime in one order and
// stops at the first step it cannot confirm.
//
// The watcher goes first because it is the only piece that keeps polling on its
// own; stopping it after the worker exits would leave it observing a pull
// request whose owner is gone. The worker is then asked to exit and its exact
// tab is closed. Only then is the child project archived, because archiving
// force-removes the worktree the session was running in.
//
// Every step is idempotent: a watcher that already finished, a worker that is
// already gone, a tab that is already closed, and a project that is already
// archived are all successes, so a retry after a partial cleanup finishes the
// job instead of failing on what it already did.
func runProgramWorkerCleanup(out io.Writer, programSlug, itemID string, jsonOutput bool) error {
	if _, err := requireHerdrPane("relay program worker cleanup"); err != nil {
		return err
	}
	_, p, err := loadActiveProgram(programSlug)
	if err != nil {
		return err
	}
	item, manifest, archived, err := loadProgramCleanupTarget(p, itemID)
	if err != nil {
		return err
	}
	result := programWorkerCleanupOutput{
		Program: p.Slug, Item: item.ID, Project: manifest.Slug,
		WorkerExit: cleanupWorkerAbsent, Warnings: []string{},
	}

	stopped, warning, err := stopCleanupWatcher(manifest.Slug)
	if err != nil {
		return fmt.Errorf(
			"cleanup %s/%s: stop the child pull request watcher for %q: %w; nothing else was torn down",
			p.Slug, item.ID, manifest.Slug, err,
		)
	}
	result.WatcherStopped = stopped
	if warning != "" {
		result.Warnings = append(result.Warnings, warning)
		// A close that did not happen keeps the recorded tab and pane ids, so
		// the patrol keeps raising merged-worker-cleanup for this item. Name
		// the command that finishes it rather than leaving the tech lead to
		// infer one from a warning.
		result.NextCommand = fmt.Sprintf("relay program worker cleanup %s %s", p.Slug, item.ID)
	}

	exited, err := exitCleanupWorker(&result, manifest)
	if err != nil {
		return err
	}
	if !exited {
		result.Status = cleanupWorkerBusy
		result.NextCommand = fmt.Sprintf("relay program worker cleanup %s %s", p.Slug, item.ID)
		return renderProgramWorkerCleanup(out, result, jsonOutput)
	}

	if archived {
		result.AlreadyArchived = true
		result.Status = cleanupClean
		return renderProgramWorkerCleanup(out, result, jsonOutput)
	}
	archiveOutcome, err := archiveProject(manifest.Slug, true)
	if err != nil {
		return fmt.Errorf(
			"cleanup %s/%s: the watcher is stopped and the worker session is gone, but child project %q "+
				"could not be archived: %w; item %s is still merged and the project is still active — "+
				"retry with: relay program worker cleanup %s %s",
			p.Slug, item.ID, manifest.Slug, err, item.ID, p.Slug, item.ID,
		)
	}
	result.Archived = true
	result.Archive = &archiveOutcome
	result.Warnings = append(result.Warnings, archiveOutcome.Warnings...)
	result.Status = cleanupClean
	return renderProgramWorkerCleanup(out, result, jsonOutput)
}

// loadProgramCleanupTarget admits only a merged item. Cleanup discards a
// worktree and may force-delete a branch, so an item that is still pending,
// dispatched, in review, blocked, or canceled is refused outright: its work is
// either unfinished or was never delivered.
func loadProgramCleanupTarget(
	p program.Program, itemID string,
) (program.WorkItem, project.Manifest, bool, error) {
	item, ok := p.Item(itemID)
	if !ok {
		return program.WorkItem{}, project.Manifest{}, false, fmt.Errorf(
			"cleanup %s/%s: item not found", p.Slug, itemID,
		)
	}
	if item.Status != program.ItemMerged {
		return program.WorkItem{}, project.Manifest{}, false, fmt.Errorf(
			"cleanup %s/%s: item status is %q, want merged; cleanup discards the child worktree, so it "+
				"only ever runs on delivered work",
			p.Slug, itemID, item.Status,
		)
	}
	if item.ProjectSlug == "" {
		return program.WorkItem{}, project.Manifest{}, false, fmt.Errorf(
			"cleanup %s/%s: item is not linked to a child project", p.Slug, itemID,
		)
	}
	manifestPath, err := project.Find(item.ProjectSlug)
	if err != nil {
		return program.WorkItem{}, project.Manifest{}, false, fmt.Errorf(
			"cleanup %s/%s: child project %q: %w", p.Slug, itemID, item.ProjectSlug, err,
		)
	}
	manifest, err := project.Load(manifestPath)
	if err != nil {
		return program.WorkItem{}, project.Manifest{}, false, err
	}
	if manifest.Program != p.Slug || manifest.ProgramItem != item.ID {
		return program.WorkItem{}, project.Manifest{}, false, fmt.Errorf(
			"cleanup %s/%s: child project %q is linked to %q/%q",
			p.Slug, itemID, manifest.Slug, manifest.Program, manifest.ProgramItem,
		)
	}
	return item, manifest, pathWithinDir(manifestPath, project.ArchivedDir()), nil
}

// stopCleanupWatcher stops the child's pull request watcher and closes its
// recorded tab. A watcher that was never started, already stopped, or already
// finished on its own is a success: the point is that nothing is still polling
// and that no tab is left behind.
//
// A finished watcher is the common case here, not an edge one. A watcher whose
// pull request merged completes and exits by itself, and it deliberately keeps
// its tab rather than closing it from inside, which would race the flush of its
// own final lines. Cleanup is what closes that tab, so this step runs whether
// or not a process is still alive.
func stopCleanupWatcher(childSlug string) (bool, string, error) {
	running, err := prWatchIsRunning(childSlug)
	if err != nil {
		return false, "", err
	}
	if _, stateErr := prWatchReadState(childSlug); stateErr != nil {
		if errors.Is(stateErr, os.ErrNotExist) && !running {
			return false, "", nil
		}
	}
	var stopOut bytes.Buffer
	if err := runPRWatchStop(&stopOut, childSlug, true); err != nil {
		return false, "", err
	}
	var stop prWatchStopOutput
	if err := json.Unmarshal(stopOut.Bytes(), &stop); err != nil {
		return running, "", nil
	}
	return stop.Stopped, stop.Warning, nil
}

// exitCleanupWorker ends the item's one live worker session and closes its exact
// tab. It reports false when a working or blocked worker must be left alone.
func exitCleanupWorker(result *programWorkerCleanupOutput, manifest project.Manifest) (bool, error) {
	worktree := ""
	if manifest.Worktree != nil {
		worktree = *manifest.Worktree
	}
	client := newHerdrClient()
	agents, err := client.Agents()
	if err != nil {
		return false, fmt.Errorf(
			"cleanup child project %q: list Herdr agents: %w; the pull request watcher was stopped and "+
				"nothing else was torn down",
			manifest.Slug, err,
		)
	}
	matches := herdr.LiveWorkers(agents, manifest.Slug, manifest.Repo, worktree)
	if len(matches) > 1 {
		panes := make([]string, 0, len(matches))
		for _, match := range matches {
			panes = append(panes, match.PaneID)
		}
		return false, fmt.Errorf(
			"cleanup child project %q: %d live sessions claim it (panes %s); exactly one session owns a "+
				"project, so nothing was ended — focus each pane with `herdr agent focus <pane>`, exit all "+
				"but one, then retry",
			manifest.Slug, len(matches), strings.Join(panes, ", "),
		)
	}
	if len(matches) == 0 {
		result.WorkerExit = cleanupWorkerAbsent
		return true, nil
	}
	worker := matches[0]
	result.WorkerPane = worker.PaneID
	result.WorkerStatus = string(worker.Status)
	switch worker.Status {
	case herdr.StatusWorking, herdr.StatusBlocked:
		result.WorkerExit = cleanupWorkerRunning
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"worker in pane %s is %s, so it was not interrupted and the child project was left active",
			worker.PaneID, worker.Status,
		))
		return false, nil
	}

	identity := worker.Identity()
	exit, err := client.ExitAgent(identity)
	if err != nil {
		if errors.Is(err, herdr.ErrAgentBusy) {
			result.WorkerExit = cleanupWorkerRunning
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"worker in pane %s became busy before it could exit, so it was not interrupted", worker.PaneID,
			))
			return false, nil
		}
		return false, fmt.Errorf(
			"cleanup child project %q: %w; its tab, project, and worktree were left exactly as they are — "+
				"finish the exit by hand in %s, then retry",
			manifest.Slug, err, identity,
		)
	}
	result.WorkerExit = cleanupWorkerExited
	if exit.Outcome == herdr.ExitedReplaced {
		return false, fmt.Errorf(
			"cleanup child project %q: the worker session ended, but %s is now running in its pane; "+
				"closing those ids would take down a session that is not this item's worker. Nothing else "+
				"was torn down",
			manifest.Slug, exit.Replacement.Identity(),
		)
	}
	closed, err := closeCleanupWorkerTab(client, identity)
	if err != nil {
		return false, fmt.Errorf(
			"cleanup child project %q: the worker session ended, but its tab could not be closed: %w; "+
				"the project and worktree were left intact",
			manifest.Slug, err,
		)
	}
	result.TabClosed = closed
	return true, nil
}

// closeCleanupWorkerTab closes the exact tab the exited worker held, after
// re-reading Herdr one last time. Herdr reuses tab and pane ids, so a session
// that appeared in the moment between the exit and the close would otherwise
// have its brand-new pane closed by an id that no longer means what it meant.
func closeCleanupWorkerTab(client herdrRuntimeClient, identity herdr.SessionIdentity) (bool, error) {
	agents, err := client.Agents()
	if err != nil {
		return false, err
	}
	if occupant, found := herdr.PaneOccupant(agents, identity); found {
		return false, fmt.Errorf(
			"%s is running there now, so the recorded ids were not closed", occupant.Identity(),
		)
	}
	if identity.TabID != "" {
		if err := client.CloseTab(identity.TabID); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := client.ClosePane(identity.PaneID); err != nil {
		return false, err
	}
	return true, nil
}

func renderProgramWorkerCleanup(out io.Writer, result programWorkerCleanupOutput, jsonOutput bool) error {
	if jsonOutput {
		return writeProgramJSON(out, result)
	}
	fmt.Fprintf(out, "Item: %s\n", result.Item)
	fmt.Fprintf(out, "Project: %s\n", result.Project)
	fmt.Fprintf(out, "Status: %s\n", result.Status)
	if result.WatcherStopped {
		fmt.Fprintln(out, "Stopped the child pull request watcher")
	}
	fmt.Fprintf(out, "Worker: %s\n", result.WorkerExit)
	if result.TabClosed {
		fmt.Fprintln(out, "Closed the worker tab")
	}
	switch {
	case result.Archived:
		fmt.Fprintf(out, "Archived: %s\n", result.Project)
		if result.Archive != nil && result.Archive.WorktreeRemoved {
			fmt.Fprintf(out, "Worktree removed: %s\n", result.Archive.Worktree)
		}
	case result.AlreadyArchived:
		fmt.Fprintf(out, "Already archived: %s\n", result.Project)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(out, "Warning: %s\n", warning)
	}
	if result.NextCommand != "" {
		fmt.Fprintf(out, "Next: %s\n", result.NextCommand)
	}
	return nil
}
