package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/prwatch"
	"github.com/spf13/cobra"
)

// Change request routes. One route is chosen from current GitHub truth, and
// each one has exactly one owner, one branch, and one pull request.
const (
	// changeRouteSameWorker sends the request back to the worker that already
	// owns the pull request. It is used while the pull request is still open
	// and unapproved, and after a pull request was closed without merging.
	changeRouteSameWorker = "same-worker"
	// changeRouteFollowUpPending records a follow-up item that must wait. The
	// original pull request is approved or in GitHub's merge queue, so nothing
	// may touch its branch and nothing may start until it merges.
	changeRouteFollowUpPending = "follow-up-pending"
	// changeRouteFollowUpDispatched records a follow-up item and starts its own
	// worker, because the original pull request already merged.
	changeRouteFollowUpDispatched = "follow-up-dispatched"
)

// inspectProjectPR reads current GitHub truth for one child project's recorded
// pull request. It is a read: it never writes watcher runtime state.
var inspectProjectPR = func(ctx context.Context, slug string) (prwatch.Inspection, error) {
	return prwatch.Inspect(ctx, slug, prwatch.InspectOptions{})
}

type programWorkerChangeOutput struct {
	Program         string             `json:"program"`
	Item            string             `json:"item"`
	Route           string             `json:"route"`
	RequestHash     string             `json:"request_hash"`
	Reused          bool               `json:"reused"`
	PR              prwatch.Inspection `json:"pr"`
	MessageID       string             `json:"message_id,omitempty"`
	Notified        bool               `json:"notified"`
	NotifyOutcome   string             `json:"notify_outcome,omitempty"`
	FollowUpItem    string             `json:"follow_up_item,omitempty"`
	FollowUpProject string             `json:"follow_up_project,omitempty"`
	FollowUpStarted bool               `json:"follow_up_started"`
	WorkerPane      string             `json:"worker_pane,omitempty"`
	NextCommand     string             `json:"next_command,omitempty"`
	Warning         string             `json:"warning,omitempty"`
}

// programChangeTarget is one work item, its child project, and the current
// GitHub truth about the pull request that item recorded.
type programChangeTarget struct {
	program    program.Program
	path       string
	item       program.WorkItem
	manifest   project.Manifest
	projectDir string
	archived   bool
	inspection prwatch.Inspection
}

func newCmdProgramWorkerRequestChange() *cobra.Command {
	var body string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "request-change <program> <item>",
		Short: "Route a CEO change request for a managed pull request",
		Long: "Route a CEO-requested code change to the one place it can safely be made.\n\n" +
			"Current GitHub state decides the route. An open, unapproved pull request keeps its\n" +
			"existing worker and receives durable feedback. An approved or merge-queued pull request\n" +
			"is never rewritten: the request becomes a follow-up work item that waits for the merge.\n" +
			"A merged pull request produces a follow-up item with its own project, branch, and worker.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramWorkerRequestChange(cmd.OutOrStdout(), args[0], args[1], body, jsonOutput)
		},
	}
	cmd.Flags().StringVar(&body, "body", "", "the change the CEO asked for")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

// runProgramWorkerRequestChange reads GitHub before it writes anything. A
// request whose pull request, project, or GitHub state cannot be determined
// leaves no message, no item, and no worker behind: routing a change to the
// wrong branch is worse than not routing it at all.
func runProgramWorkerRequestChange(out io.Writer, programSlug, itemID, request string, jsonOutput bool) error {
	if _, err := requireHerdrPane("relay program worker request-change"); err != nil {
		return err
	}
	requestHash, err := program.RequestHash(request)
	if err != nil {
		return fmt.Errorf("request change for %s/%s: %w", programSlug, itemID, err)
	}
	target, err := loadProgramChangeTarget(programSlug, itemID)
	if err != nil {
		return err
	}
	result := programWorkerChangeOutput{
		Program:     target.program.Slug,
		Item:        target.item.ID,
		RequestHash: requestHash,
		PR:          target.inspection,
	}
	switch {
	case target.item.Status == program.ItemMerged || target.inspection.Merged():
		err = routeChangeToStartedFollowUp(&result, target, request, requestHash)
	case target.inspection.Protected():
		err = routeChangeToPendingFollowUp(&result, target, request, requestHash)
	default:
		err = routeChangeToSameWorker(&result, target, request, requestHash)
	}
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeProgramJSON(out, result)
	}
	renderProgramWorkerChange(out, result)
	return nil
}

// loadProgramChangeTarget resolves everything a route decision depends on and
// fails closed if any part of it is unavailable.
func loadProgramChangeTarget(programSlug, itemID string) (programChangeTarget, error) {
	path, p, err := loadActiveProgram(programSlug)
	if err != nil {
		return programChangeTarget{}, err
	}
	if p.State != program.StateActive {
		return programChangeTarget{}, fmt.Errorf(
			"request change for %s/%s: program is %s, want active", programSlug, itemID, p.State,
		)
	}
	item, ok := p.Item(itemID)
	if !ok {
		return programChangeTarget{}, fmt.Errorf("request change for %s/%s: item not found", programSlug, itemID)
	}
	if item.ProjectSlug == "" {
		return programChangeTarget{}, fmt.Errorf(
			"request change for %s/%s: item is not linked to a child project, so it has no pull request to change",
			programSlug, itemID,
		)
	}
	if strings.TrimSpace(item.PRRef) == "" {
		return programChangeTarget{}, fmt.Errorf(
			"request change for %s/%s: item records no pull request; send the worker durable feedback with "+
				"`relay program message notify %s %s` instead",
			programSlug, itemID, programSlug, itemID,
		)
	}
	manifestPath, err := project.Find(item.ProjectSlug)
	if err != nil {
		return programChangeTarget{}, fmt.Errorf(
			"request change for %s/%s: child project %q: %w", programSlug, itemID, item.ProjectSlug, err,
		)
	}
	manifest, err := project.Load(manifestPath)
	if err != nil {
		return programChangeTarget{}, err
	}
	if manifest.Program != p.Slug || manifest.ProgramItem != item.ID {
		return programChangeTarget{}, fmt.Errorf(
			"request change for %s/%s: child project %q is linked to %q/%q",
			programSlug, itemID, manifest.Slug, manifest.Program, manifest.ProgramItem,
		)
	}
	inspection, err := inspectProjectPR(context.Background(), item.ProjectSlug)
	if err != nil {
		return programChangeTarget{}, fmt.Errorf(
			"request change for %s/%s: current GitHub state for the recorded pull request could not be read, "+
				"so nothing was written: %w",
			programSlug, itemID, err,
		)
	}
	// The item records a pull request URL or "#<n>" while the inspection names
	// the number GitHub answered for. Comparing the numbers is what confirms
	// the read describes the pull request this item actually owns.
	recorded, ok := programview.PullRequestNumber(item.PRRef)
	if !ok {
		return programChangeTarget{}, fmt.Errorf(
			"request change for %s/%s: recorded pull request %q is not a pull request URL or #<number>; "+
				"reconcile with `relay program tick %s` and retry",
			programSlug, itemID, item.PRRef, programSlug,
		)
	}
	if inspection.Number != recorded {
		return programChangeTarget{}, fmt.Errorf(
			"request change for %s/%s: GitHub was read for pull request #%d, but the item records %q; "+
				"reconcile with `relay program tick %s` and retry",
			programSlug, itemID, inspection.Number, item.PRRef, programSlug,
		)
	}
	return programChangeTarget{
		program:    p,
		path:       path,
		item:       item,
		manifest:   manifest,
		projectDir: filepath.Dir(manifestPath),
		archived:   pathWithinDir(manifestPath, project.ArchivedDir()),
		inspection: inspection,
	}, nil
}

// routeChangeToSameWorker keeps one work item, one branch, and one worker. The
// pull request is still the worker's to change, so the request goes into its
// durable inbox and the doorbell rings once.
//
// The inbox message id is derived from the request itself, so a retry of the
// same request finds the message the first attempt already wrote instead of
// stacking a second copy of it in the worker's inbox.
func routeChangeToSameWorker(
	result *programWorkerChangeOutput,
	target programChangeTarget,
	request, requestHash string,
) error {
	result.Route = changeRouteSameWorker
	if target.archived {
		return fmt.Errorf(
			"request change for %s/%s: child project %q is archived, so its worker cannot make this change; "+
				"reconcile with `relay program tick %s` and retry",
			target.program.Slug, target.item.ID, target.manifest.Slug, target.program.Slug,
		)
	}
	if !activeProgramWorkerStatus(target.item.Status) {
		return fmt.Errorf(
			"request change for %s/%s: item status is %q, want dispatched, in-review, or blocked",
			target.program.Slug, target.item.ID, target.item.Status,
		)
	}
	if err := mailbox.Ensure(target.projectDir); err != nil {
		return fmt.Errorf(
			"request change for %s/%s: ensure mailbox for child project %q: %w",
			target.program.Slug, target.item.ID, target.manifest.Slug, err,
		)
	}
	messageID := changeMessageID(requestHash)
	present, err := mailbox.Exists(target.projectDir, mailbox.Inbox, messageID)
	if err != nil {
		return fmt.Errorf("request change for %s/%s: %w", target.program.Slug, target.item.ID, err)
	}
	result.MessageID = messageID
	if present {
		result.Reused = true
	} else {
		_, sendErr := mailbox.Send(target.projectDir, mailbox.Inbox, mailbox.Message{
			ID:      messageID,
			Kind:    mailbox.KindFeedback,
			Program: target.program.Slug,
			Item:    target.item.ID,
			From:    mailbox.ActorTL,
			To:      mailbox.ActorWorker,
			Body:    changeRequestBody(target, request),
		})
		switch {
		case errors.Is(sendErr, os.ErrExist):
			result.Reused = true
		case sendErr != nil:
			return fmt.Errorf(
				"request change for %s/%s: write durable feedback: %w",
				target.program.Slug, target.item.ID, sendErr,
			)
		}
	}
	notify, notifyErr := notifyLiveProgramWorker(target.program.Slug, target.item.ID)
	result.NotifyOutcome = string(notify.outcome)
	result.WorkerPane = notify.agent.PaneID
	if notifyErr != nil {
		result.Warning = fmt.Sprintf(
			"the change request is durable in the worker's inbox, but the doorbell did not ring: %v", notifyErr,
		)
		result.NotifyOutcome = string(programWorkerNotifyError)
		return nil
	}
	switch notify.outcome {
	case programWorkerNotified:
		result.Notified = true
	case programWorkerNoMessage:
		// The worker was already rung for this exact message.
		result.Notified = false
	case programWorkerBusy:
		result.Warning = fmt.Sprintf(
			"worker %s is %s, so it was not interrupted; it reads the durable request when it next checks its inbox",
			target.item.ID, notify.agent.Status,
		)
	case programWorkerNoLiveWorker:
		result.NextCommand = fmt.Sprintf(
			"relay program worker start %s %s", target.program.Slug, target.item.ID,
		)
		result.Warning = fmt.Sprintf(
			"the change request is durable in the worker's inbox, but item %s has no live Herdr worker; "+
				"start its one owner with the reported command",
			target.item.ID,
		)
	}
	return nil
}

// routeChangeToPendingFollowUp records a follow-up item and stops. The original
// pull request is approved or in GitHub's merge queue: pushing to its branch
// would invalidate a human's approval or break the queue, so the follow-up
// waits on the original item and nothing is dispatched or started here.
func routeChangeToPendingFollowUp(
	result *programWorkerChangeOutput,
	target programChangeTarget,
	request, requestHash string,
) error {
	result.Route = changeRouteFollowUpPending
	followUp, reused, err := ensureChangeFollowUp(target, request, requestHash)
	if err != nil {
		return err
	}
	result.Reused = reused
	result.FollowUpItem = followUp.ID
	result.NextCommand = fmt.Sprintf(
		"relay program worker request-change %s %s --body <request>",
		target.program.Slug, target.item.ID,
	)
	reason := "is approved"
	if target.inspection.Queued {
		reason = "is in GitHub's merge queue"
	}
	result.Warning = fmt.Sprintf(
		"pull request %s %s, so follow-up %s stays pending until item %s merges; re-run this command then",
		target.item.PRRef, reason, followUp.ID, target.item.ID,
	)
	return nil
}

// routeChangeToStartedFollowUp records a follow-up item and, when it is ready,
// gives it its own project, branch, and worker. The item is made durable
// first: a dispatch or start that fails afterwards is reported with the exact
// command that finishes it, and never rolled back.
func routeChangeToStartedFollowUp(
	result *programWorkerChangeOutput,
	target programChangeTarget,
	request, requestHash string,
) error {
	result.Route = changeRouteFollowUpDispatched
	followUp, reused, err := ensureChangeFollowUp(target, request, requestHash)
	if err != nil {
		return err
	}
	result.Reused = reused
	result.FollowUpItem = followUp.ID
	result.NextCommand = fmt.Sprintf(
		"relay program worker start %s %s", target.program.Slug, followUp.ID,
	)

	_, current, err := loadActiveProgram(target.program.Slug)
	if err != nil {
		return changeFollowUpPartialError(target, followUp.ID, err)
	}
	stored, ok := current.Item(followUp.ID)
	if !ok {
		return changeFollowUpPartialError(target, followUp.ID,
			errors.New("the follow-up item is missing from the reloaded program"))
	}
	if stored.Status == program.ItemPending {
		if blocked := changeFollowUpBlockers(current, followUp.ID); len(blocked) > 0 {
			result.Warning = fmt.Sprintf(
				"follow-up %s is durable but not ready: %s; dispatch it once it is",
				followUp.ID, strings.Join(blocked, "; "),
			)
			result.NextCommand = fmt.Sprintf(
				"relay program dispatch %s %s", target.program.Slug, followUp.ID,
			)
			return nil
		}
		var dispatchOut bytes.Buffer
		if err := runProgramDispatch(&dispatchOut, target.program.Slug, followUp.ID, programDispatchOpts{}); err != nil {
			return changeFollowUpPartialError(target, followUp.ID, err)
		}
	}
	var startOut bytes.Buffer
	if err := runProgramWorkerStart(&startOut, target.program.Slug, followUp.ID, true); err != nil {
		return changeFollowUpPartialError(target, followUp.ID, err)
	}
	var started programWorkerOutput
	if err := json.Unmarshal(startOut.Bytes(), &started); err == nil {
		result.FollowUpProject = started.Project
		result.WorkerPane = started.PaneID
	}
	result.FollowUpStarted = true
	result.NextCommand = ""
	return nil
}

// changeFollowUpPartialError reports a durable follow-up whose dispatch or
// start did not finish. The item is never removed: a second run of the printed
// command completes it, and deleting durable work to make an error look clean
// would lose the CEO's request.
func changeFollowUpPartialError(target programChangeTarget, followUpID string, cause error) error {
	return fmt.Errorf(
		"request change for %s/%s: follow-up %s is durably recorded, but starting it did not finish: %w; "+
			"it was not rolled back — finish it with: relay program dispatch %s %s && relay program worker start %s %s",
		target.program.Slug, target.item.ID, followUpID, cause,
		target.program.Slug, followUpID, target.program.Slug, followUpID,
	)
}

// changeFollowUpBlockers returns why a pending follow-up is not ready yet.
func changeFollowUpBlockers(p program.Program, itemID string) []string {
	_, blocked := p.Readiness()
	for _, candidate := range blocked {
		if candidate.Item.ID == itemID {
			return candidate.Reasons
		}
	}
	return nil
}

// ensureChangeFollowUp reuses the follow-up this exact request already created,
// or records a new one. The request is stored durably in the item's notes and
// its hash in the item itself, so a retry is recognized after a crash.
func ensureChangeFollowUp(
	target programChangeTarget,
	request, requestHash string,
) (program.WorkItem, bool, error) {
	if existing, found := target.program.FindFollowUp(target.item.ID, requestHash); found {
		return existing, true, nil
	}
	next := target.program
	followUp, err := next.AddItem(program.WorkItem{
		Title:        changeFollowUpTitle(target.item, request),
		Priority:     target.item.Priority,
		Dependencies: []string{target.item.ID},
		ContractRefs: append([]string(nil), target.item.ContractRefs...),
		Repo:         target.item.Repo,
		FollowUpOf:   target.item.ID,
		RequestHash:  requestHash,
		Notes: []string{fmt.Sprintf(
			"Follow-up requested for %s (pull request %s): %s",
			target.item.ID, target.item.PRRef, program.NormalizeRequest(request),
		)},
	})
	if err != nil {
		return program.WorkItem{}, false, fmt.Errorf(
			"request change for %s/%s: record follow-up: %w", target.program.Slug, target.item.ID, err,
		)
	}
	progress := fmt.Sprintf(
		"Recorded follow-up %s for %s after a change request against pull request %s",
		followUp.ID, target.item.ID, target.item.PRRef,
	)
	if err := saveProgramMutation(target.path, next, progress); err != nil {
		return program.WorkItem{}, false, fmt.Errorf(
			"request change for %s/%s: save follow-up: %w", target.program.Slug, target.item.ID, err,
		)
	}
	return followUp, false, nil
}

// changeFollowUpTitle names the follow-up after the request that created it, so
// the roadmap reads as work rather than as a bookkeeping artifact.
func changeFollowUpTitle(item program.WorkItem, request string) string {
	const limit = 72
	summary := program.NormalizeRequest(request)
	prefix := "Follow-up to " + item.ID + ": "
	runes := []rune(summary)
	if len(runes) > limit {
		summary = strings.TrimSpace(string(runes[:limit-3])) + "..."
	}
	return prefix + summary
}

// changeMessageID derives one filename-safe inbox id from a request hash.
func changeMessageID(requestHash string) string {
	return "change-" + requestHash[:32]
}

func changeRequestBody(target programChangeTarget, request string) string {
	return fmt.Sprintf(
		"The CEO asked for a change to the pull request you already have open (%s).\n\n"+
			"Requested change:\n%s\n\n"+
			"Make it on your existing branch and pull request. Do not open a second pull request "+
			"for this item, and do not start new work outside it.",
		target.item.PRRef, program.NormalizeRequest(request),
	)
}

func renderProgramWorkerChange(out io.Writer, result programWorkerChangeOutput) {
	fmt.Fprintf(out, "Item: %s\n", result.Item)
	fmt.Fprintf(out, "Pull request: %s (%s", result.PR.Ref, result.PR.State)
	if result.PR.ReviewDecision != "" {
		fmt.Fprintf(out, ", %s", result.PR.ReviewDecision)
	}
	if result.PR.Queued {
		fmt.Fprint(out, ", merge queue")
	}
	fmt.Fprintln(out, ")")
	fmt.Fprintf(out, "Route: %s\n", result.Route)
	if result.MessageID != "" {
		verb := "Wrote"
		if result.Reused {
			verb = "Reused"
		}
		fmt.Fprintf(out, "%s durable feedback: %s\n", verb, result.MessageID)
	}
	if result.Notified {
		fmt.Fprintf(out, "Notified worker in pane %s\n", result.WorkerPane)
	}
	if result.FollowUpItem != "" {
		verb := "Created"
		if result.Reused {
			verb = "Reused"
		}
		fmt.Fprintf(out, "%s follow-up: %s\n", verb, result.FollowUpItem)
	}
	if result.FollowUpStarted {
		fmt.Fprintf(out, "Started follow-up worker: %s (pane %s)\n", result.FollowUpProject, result.WorkerPane)
	}
	if result.Warning != "" {
		fmt.Fprintf(out, "Warning: %s\n", result.Warning)
	}
	if result.NextCommand != "" {
		fmt.Fprintf(out, "Next: %s\n", result.NextCommand)
	}
}

func pathWithinDir(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
