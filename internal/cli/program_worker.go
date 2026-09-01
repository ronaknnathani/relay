package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/spf13/cobra"
)

const (
	workerPollInitialInterval = 250 * time.Millisecond
	workerPollMaxInterval     = time.Second
	workerPollTimeout         = 30 * time.Second
	workerStartLockTimeout    = 30 * time.Second
	workerLabelLimit          = 80
	workerNotRunning          = herdr.Status("not-running")
)

type herdrRuntimeClient interface {
	Agents() ([]herdr.Agent, error)
	CreateTab(workspaceID, cwd, label string) (herdr.Tab, error)
	CloseTab(tabID string) error
	ClosePane(paneID string) error
	RunPane(paneID, command string) error
	RenameAgent(target, name string) error
	PromptAgent(target, text string) error
	FocusAgent(target string) error
	ShowNotification(title, body string) error
}

var (
	newHerdrClient = func() herdrRuntimeClient {
		return herdr.NewClientWithCommandTimeout(context.Background(), herdrCommandTimeout)
	}
	herdrAvailable = herdr.Available
	workerNow      = time.Now
	workerSleep    = time.Sleep
)

type programWorkerOutput struct {
	Item            string       `json:"item"`
	Project         string       `json:"project"`
	Worktree        string       `json:"worktree"`
	WorkspaceID     string       `json:"workspace_id,omitempty"`
	TabID           string       `json:"tab_id,omitempty"`
	PaneID          string       `json:"pane_id,omitempty"`
	WorkerName      string       `json:"worker_name"`
	NativeSessionID string       `json:"native_session_id,omitempty"`
	Status          herdr.Status `json:"status"`
	Adopted         bool         `json:"adopted"`
	FocusCommand    string       `json:"focus_command"`
}

type programWorkerListEntry struct {
	Item            string             `json:"item"`
	ItemStatus      program.ItemStatus `json:"item_status"`
	Project         string             `json:"project"`
	Worktree        string             `json:"worktree"`
	WorkerName      string             `json:"worker_name"`
	WorkspaceID     string             `json:"workspace_id,omitempty"`
	TabID           string             `json:"tab_id,omitempty"`
	PaneID          string             `json:"pane_id,omitempty"`
	NativeSessionID string             `json:"native_session_id,omitempty"`
	Status          herdr.Status       `json:"status"`
	Live            bool               `json:"live"`
}

type programWorkerListOutput struct {
	Entries  []programWorkerListEntry `json:"entries"`
	Warnings []programItemWarning     `json:"warnings"`
}

type programWorkerTarget struct {
	item     program.WorkItem
	manifest project.Manifest
}

type programWorkerNotifyOutcome string

const (
	programWorkerNotified     programWorkerNotifyOutcome = "notified"
	programWorkerBusy         programWorkerNotifyOutcome = "busy"
	programWorkerNoMessage    programWorkerNotifyOutcome = "no-message"
	programWorkerNoLiveWorker programWorkerNotifyOutcome = "no-live-worker"
	programWorkerNotifyError  programWorkerNotifyOutcome = "error"
)

type programWorkerNotifyResult struct {
	outcome    programWorkerNotifyOutcome
	target     programWorkerTarget
	agent      herdr.Agent
	messageIDs []string
}

func newCmdProgramWorker() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Manage Herdr worker sessions for program items",
	}
	cmd.AddCommand(
		newCmdProgramWorkerStart(),
		newCmdProgramWorkerList(),
		newCmdProgramWorkerFocus(),
		newCmdProgramWorkerNotify(),
	)
	return cmd
}

func newCmdProgramWorkerFocus() *cobra.Command {
	return &cobra.Command{
		Use:   "focus <program> <item>",
		Short: "Focus the live Herdr worker for a program item",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramWorkerFocus(cmd.OutOrStdout(), args[0], args[1])
		},
	}
}

func newCmdProgramWorkerNotify() *cobra.Command {
	return &cobra.Command{
		Use:   "notify <program> <item>",
		Short: "Ring the live Herdr worker for new durable inbox messages",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramWorkerNotify(cmd.OutOrStdout(), args[0], args[1])
		},
	}
}

func newCmdProgramWorkerList() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list <program>",
		Short: "List Herdr workers for linked program items",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramWorkerList(cmd.OutOrStdout(), args[0], jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func newCmdProgramWorkerStart() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "start <program> <item>",
		Short: "Start or adopt a Herdr worker for a program item",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramWorkerStart(cmd.OutOrStdout(), args[0], args[1], jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func runProgramWorkerStart(out io.Writer, programSlug, itemID string, jsonOutput bool) (retErr error) {
	target, p, err := loadProgramWorkerStartTarget(programSlug, itemID)
	if err != nil {
		return err
	}
	if err := requireManagedAgent(p.Agent, fmt.Sprintf("program %q", p.Slug)); err != nil {
		return err
	}
	if _, err := requireHerdrPane("relay program worker start"); err != nil {
		return err
	}
	// One child project has exactly one owner, so discovery, tab creation, the
	// resume command, Herdr recognition, and renaming all run under a per-child
	// kernel lock that a crashed holder releases automatically.
	lock, err := acquireWorkerStartLock(target.manifest.Slug)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			retErr = errors.Join(retErr, releaseErr)
		}
	}()

	// The server probe and its agent list run inside the lock so a concurrent
	// start that already created the owner is adopted instead of duplicated.
	readiness, err := requireHerdrRuntime("relay program worker start", true)
	if err != nil {
		return err
	}
	client := newHerdrClient()
	if agent, ok := herdr.FindLiveWorker(
		readiness.Agents, target.manifest.Slug, target.manifest.Repo, *target.manifest.Worktree,
	); ok {
		return renderProgramWorker(out, target, agent, herdr.WorkerName(programSlug, itemID), true, jsonOutput)
	}

	tab, err := client.CreateTab(readiness.WorkspaceID, *target.manifest.Worktree, workerTabLabel(target.item))
	if err != nil {
		return err
	}
	if err := client.RunPane(tab.RootPaneID, "relay resume "+shellQuote(target.manifest.Slug)); err != nil {
		return err
	}
	agent, err := waitForProgramWorker(client, target.manifest)
	if err != nil {
		return err
	}
	if agent.WorkspaceID == "" {
		agent.WorkspaceID = readiness.WorkspaceID
	}
	if agent.TabID == "" {
		agent.TabID = tab.ID
	}
	if agent.PaneID == "" {
		agent.PaneID = tab.RootPaneID
	}
	name := herdr.WorkerName(programSlug, itemID)
	if err := client.RenameAgent(agent.PaneID, name); err != nil {
		return err
	}
	return renderProgramWorker(out, target, agent, name, false, jsonOutput)
}

// WorkerRuntimeDir returns ~/.relay/run/workers/<child-slug>.
func workerRuntimeDir(childSlug string) string {
	return filepath.Join(program.RelayDir(), "run", "workers", childSlug)
}

func workerStartLockPath(childSlug string) string {
	return filepath.Join(workerRuntimeDir(childSlug), "start.lock")
}

func acquireWorkerStartLock(childSlug string) (*patrollock.Lock, error) {
	path := workerStartLockPath(childSlug)
	lock, err := patrollock.AcquireWait(path, workerStartLockTimeout)
	if err != nil {
		if errors.Is(err, patrollock.ErrLocked) {
			return nil, fmt.Errorf(
				"another `relay program worker start` for child project %q has held %s for longer than %s; "+
					"wait for it to finish, then retry",
				childSlug, path, workerStartLockTimeout,
			)
		}
		return nil, err
	}
	return lock, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func runProgramWorkerList(out io.Writer, programSlug string, jsonOutput bool) error {
	if _, err := requireHerdrPane("relay program worker list"); err != nil {
		return err
	}
	_, p, err := loadActiveProgram(programSlug)
	if err != nil {
		return err
	}
	client := newHerdrClient()
	agents, err := client.Agents()
	if err != nil {
		return err
	}
	result := programWorkerListOutput{
		Entries:  make([]programWorkerListEntry, 0),
		Warnings: make([]programItemWarning, 0),
	}
	for _, item := range p.Items {
		if item.ProjectSlug == "" || !activeProgramWorkerStatus(item.Status) {
			continue
		}
		manifest, err := loadProgramWorkerManifest(p, item)
		if err != nil {
			result.Warnings = append(result.Warnings, programItemWarning{
				Item: item.ID, Project: item.ProjectSlug, Error: err.Error(),
			})
			continue
		}
		entry := programWorkerListEntry{
			Item:       item.ID,
			ItemStatus: item.Status,
			Project:    manifest.Slug,
			Worktree:   *manifest.Worktree,
			WorkerName: herdr.WorkerName(p.Slug, item.ID),
			Status:     workerNotRunning,
		}
		if agent, ok := herdr.FindLiveWorker(agents, manifest.Slug, manifest.Repo, *manifest.Worktree); ok {
			entry.WorkspaceID = agent.WorkspaceID
			entry.TabID = agent.TabID
			entry.PaneID = agent.PaneID
			entry.NativeSessionID = agent.NativeSessionID
			entry.Status = agent.Status
			entry.Live = true
		}
		result.Entries = append(result.Entries, entry)
	}
	if jsonOutput {
		return writeProgramJSON(out, result)
	}
	for _, entry := range result.Entries {
		fmt.Fprintf(out, "%s  %-10s %-12s %s", entry.Item, entry.ItemStatus, entry.Status, entry.Project)
		if entry.PaneID != "" {
			fmt.Fprintf(out, "  %s", entry.PaneID)
		}
		fmt.Fprintln(out)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(out, "Warning: %s (%s): %s\n", warning.Item, warning.Project, warning.Error)
	}
	return nil
}

func activeProgramWorkerStatus(status program.ItemStatus) bool {
	switch status {
	case program.ItemDispatched, program.ItemInReview, program.ItemBlocked:
		return true
	default:
		return false
	}
}

func runProgramWorkerFocus(out io.Writer, programSlug, itemID string) error {
	target, agent, client, err := findProgramWorker(programSlug, itemID)
	if err != nil {
		return err
	}
	if err := client.FocusAgent(agent.PaneID); err != nil {
		return err
	}
	fmt.Fprintf(out, "Focused %s in pane %s\n", target.item.ID, agent.PaneID)
	return nil
}

func runProgramWorkerNotify(out io.Writer, programSlug, itemID string) error {
	result, err := notifyLiveProgramWorker(programSlug, itemID)
	if err != nil {
		return err
	}
	switch result.outcome {
	case programWorkerNotified:
		fmt.Fprintf(out, "Notified %s in pane %s for %d inbox message(s)\n",
			result.target.item.ID, result.agent.PaneID, len(result.messageIDs))
		return nil
	case programWorkerBusy:
		fmt.Fprintf(out, "Worker %s is %s; durable inbox remains pending with %d unnotified message(s)\n",
			result.target.item.ID, result.agent.Status, len(result.messageIDs))
		return nil
	case programWorkerNoMessage:
		fmt.Fprintf(out, "Worker %s has no unnotified inbox messages\n", result.target.item.ID)
		return nil
	case programWorkerNoLiveWorker:
		return fmt.Errorf(
			"no live Herdr owner found for item %q project %q; run: relay program worker start %s %s",
			itemID, result.target.manifest.Slug, programSlug, itemID,
		)
	default:
		return fmt.Errorf("notify program worker %q: unexpected outcome %q", itemID, result.outcome)
	}
}

func notifyLiveProgramWorker(programSlug, itemID string) (programWorkerNotifyResult, error) {
	result := programWorkerNotifyResult{outcome: programWorkerNotifyError}
	if _, err := requireHerdrPane("relay program worker notify"); err != nil {
		return result, err
	}
	messageTarget, err := loadProgramMessageTarget(programSlug, itemID)
	if err != nil {
		return result, err
	}
	result.target = programWorkerTarget{item: messageTarget.item, manifest: messageTarget.manifest}
	unread, err := mailbox.List(messageTarget.projectDir, mailbox.Inbox)
	if err != nil {
		return result, fmt.Errorf("notify program worker %q: list inbox: %w", itemID, err)
	}
	for _, message := range unread {
		if err := validateProgramMessageAssociation(messageTarget, message); err != nil {
			return result, err
		}
		notified, err := mailbox.IsNotified(messageTarget.projectDir, message.ID)
		if err != nil {
			return result, fmt.Errorf(
				"notify program worker %q: inspect notification marker for message %q: %w",
				itemID, message.ID, err,
			)
		}
		if !notified {
			result.messageIDs = append(result.messageIDs, message.ID)
		}
	}
	if len(result.messageIDs) == 0 {
		result.outcome = programWorkerNoMessage
		return result, nil
	}
	client := newHerdrClient()
	agents, err := client.Agents()
	if err != nil {
		return result, err
	}
	agent, ok := herdr.FindLiveWorker(
		agents, result.target.manifest.Slug, result.target.manifest.Repo, *result.target.manifest.Worktree,
	)
	if !ok {
		result.outcome = programWorkerNoLiveWorker
		return result, nil
	}
	result.agent = agent
	switch agent.Status {
	case herdr.StatusWorking, herdr.StatusBlocked:
		result.outcome = programWorkerBusy
		return result, nil
	case herdr.StatusIdle, herdr.StatusDone:
	default:
		return result, fmt.Errorf(
			"notify program worker %q: Herdr status is %q; focus pane %s and verify the agent before retrying",
			itemID, agent.Status, agent.PaneID,
		)
	}
	promptErr := client.PromptAgent(agent.PaneID, "Check your Relay inbox.")
	if promptErr != nil && !errors.Is(promptErr, herdr.ErrPromptDeliveryUncertain) {
		return result, fmt.Errorf("notify program worker %q in pane %s: %w", itemID, agent.PaneID, promptErr)
	}
	for _, messageID := range result.messageIDs {
		if _, err := mailbox.MarkNotified(messageTarget.projectDir, messageID); err != nil {
			return result, fmt.Errorf(
				"notify program worker %q: Herdr prompt succeeded, but inbox message %q could not be marked notified: %w; "+
					"the durable inbox remains pending and retrying may ring the doorbell again",
				itemID, messageID, err,
			)
		}
	}
	if promptErr != nil {
		return result, fmt.Errorf(
			"notify program worker %q in pane %s: %w; inbox messages were marked to suppress duplicate input",
			itemID, agent.PaneID, promptErr,
		)
	}
	result.outcome = programWorkerNotified
	return result, nil
}

func findProgramWorker(programSlug, itemID string) (programWorkerTarget, herdr.Agent, herdrRuntimeClient, error) {
	if _, err := requireHerdrPane("relay program worker focus"); err != nil {
		return programWorkerTarget{}, herdr.Agent{}, nil, err
	}
	target, err := loadProgramWorkerTarget(programSlug, itemID)
	if err != nil {
		return programWorkerTarget{}, herdr.Agent{}, nil, err
	}
	client := newHerdrClient()
	agents, err := client.Agents()
	if err != nil {
		return programWorkerTarget{}, herdr.Agent{}, nil, err
	}
	agent, ok := herdr.FindLiveWorker(
		agents, target.manifest.Slug, target.manifest.Repo, *target.manifest.Worktree,
	)
	if !ok {
		return programWorkerTarget{}, herdr.Agent{}, nil, fmt.Errorf(
			"no live Herdr owner found for item %q project %q; run: relay program worker start %s %s",
			itemID, target.manifest.Slug, programSlug, itemID,
		)
	}
	return target, agent, client, nil
}

func loadProgramWorkerStartTarget(programSlug, itemID string) (programWorkerTarget, program.Program, error) {
	_, p, err := loadActiveProgram(programSlug)
	if err != nil {
		return programWorkerTarget{}, program.Program{}, err
	}
	target, err := programWorkerTargetFor(p, itemID)
	if err != nil {
		return programWorkerTarget{}, program.Program{}, err
	}
	return target, p, nil
}

func loadProgramWorkerTarget(programSlug, itemID string) (programWorkerTarget, error) {
	_, p, err := loadActiveProgram(programSlug)
	if err != nil {
		return programWorkerTarget{}, err
	}
	return programWorkerTargetFor(p, itemID)
}

func programWorkerTargetFor(p program.Program, itemID string) (programWorkerTarget, error) {
	if p.State != program.StateActive {
		return programWorkerTarget{}, fmt.Errorf("program worker %q: program %q is %s, want active", itemID, p.Slug, p.State)
	}
	item, ok := p.Item(itemID)
	if !ok {
		return programWorkerTarget{}, fmt.Errorf("program worker %q: item not found", itemID)
	}
	switch item.Status {
	case program.ItemDispatched, program.ItemInReview, program.ItemBlocked:
	default:
		return programWorkerTarget{}, fmt.Errorf(
			"program worker %q: item status is %q, want dispatched, in-review, or blocked",
			itemID, item.Status,
		)
	}
	if item.ProjectSlug == "" {
		return programWorkerTarget{}, fmt.Errorf("program worker %q: item is not linked to a child project; dispatch or link it first", itemID)
	}
	manifest, err := loadProgramWorkerManifest(p, item)
	if err != nil {
		return programWorkerTarget{}, err
	}
	return programWorkerTarget{item: item, manifest: manifest}, nil
}

func loadProgramWorkerManifest(p program.Program, item program.WorkItem) (project.Manifest, error) {
	manifestPath := project.ManifestPath(project.ActiveDir(), item.ProjectSlug)
	manifest, err := project.Load(manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return project.Manifest{}, fmt.Errorf(
				"program worker %q: linked child project %q is not active at %s",
				item.ID, item.ProjectSlug, manifestPath,
			)
		}
		return project.Manifest{}, fmt.Errorf("program worker %q: load child project %q: %w", item.ID, item.ProjectSlug, err)
	}
	if manifest.Program != p.Slug || manifest.ProgramItem != item.ID {
		return project.Manifest{}, fmt.Errorf(
			"program worker %q: child project %q is linked to %q/%q, want %q/%q",
			item.ID, manifest.Slug, manifest.Program, manifest.ProgramItem, p.Slug, item.ID,
		)
	}
	if manifest.Worktree == nil || strings.TrimSpace(*manifest.Worktree) == "" {
		return project.Manifest{}, fmt.Errorf("program worker %q: child project %q has no worktree", item.ID, manifest.Slug)
	}
	if info, err := os.Stat(*manifest.Worktree); err != nil {
		return project.Manifest{}, fmt.Errorf("program worker %q: stat child worktree %s: %w", item.ID, *manifest.Worktree, err)
	} else if !info.IsDir() {
		return project.Manifest{}, fmt.Errorf("program worker %q: child worktree %s is not a directory", item.ID, *manifest.Worktree)
	}
	return manifest, nil
}

// waitForProgramWorker polls Herdr with exponential backoff so a slow agent
// launch costs a bounded number of `herdr agent list` subprocesses.
func waitForProgramWorker(client herdrRuntimeClient, manifest project.Manifest) (herdr.Agent, error) {
	deadline := workerNow().Add(workerPollTimeout)
	interval := workerPollInitialInterval
	for {
		agents, err := client.Agents()
		if err != nil {
			return herdr.Agent{}, err
		}
		if agent, ok := herdr.FindLiveWorker(agents, manifest.Slug, manifest.Repo, *manifest.Worktree); ok {
			return agent, nil
		}
		if !workerNow().Before(deadline) {
			return herdr.Agent{}, fmt.Errorf(
				"timed out waiting for Herdr to recognize worker %q in %s; inspect the new tab and retry",
				"relay:"+manifest.Slug, filepath.Clean(*manifest.Worktree),
			)
		}
		workerSleep(interval)
		if interval < workerPollMaxInterval {
			interval *= 2
			if interval > workerPollMaxInterval {
				interval = workerPollMaxInterval
			}
		}
	}
}

func workerTabLabel(item program.WorkItem) string {
	label := item.ID + ": " + item.Title
	runes := []rune(label)
	if len(runes) <= workerLabelLimit {
		return label
	}
	return strings.TrimSpace(string(runes[:workerLabelLimit-3])) + "..."
}

func renderProgramWorker(
	out io.Writer,
	target programWorkerTarget,
	agent herdr.Agent,
	name string,
	adopted, jsonOutput bool,
) error {
	output := programWorkerOutput{
		Item:            target.item.ID,
		Project:         target.manifest.Slug,
		Worktree:        *target.manifest.Worktree,
		WorkspaceID:     agent.WorkspaceID,
		TabID:           agent.TabID,
		PaneID:          agent.PaneID,
		WorkerName:      name,
		NativeSessionID: agent.NativeSessionID,
		Status:          agent.Status,
		Adopted:         adopted,
		FocusCommand:    "herdr agent focus " + agent.PaneID,
	}
	if jsonOutput {
		return writeProgramJSON(out, output)
	}
	fmt.Fprintf(out, "Item: %s\n", output.Item)
	fmt.Fprintf(out, "Project: %s\n", output.Project)
	fmt.Fprintf(out, "Worktree: %s\n", output.Worktree)
	fmt.Fprintf(out, "Workspace: %s\n", output.WorkspaceID)
	fmt.Fprintf(out, "Tab: %s\n", output.TabID)
	fmt.Fprintf(out, "Pane: %s\n", output.PaneID)
	fmt.Fprintf(out, "Worker: %s\n", output.WorkerName)
	if output.NativeSessionID != "" {
		fmt.Fprintf(out, "Native session: %s\n", output.NativeSessionID)
	}
	fmt.Fprintf(out, "Status: %s\n", output.Status)
	fmt.Fprintf(out, "Adopted: %t\n", output.Adopted)
	fmt.Fprintf(out, "Focus: %s\n", output.FocusCommand)
	return nil
}
