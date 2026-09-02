package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/prwatch"
)

// installFakePRInspection replaces the GitHub read with a fixture. Every
// request-change test runs against a fake pull request: no test mutates or
// even reads a real GitHub pull request.
func installFakePRInspection(t *testing.T, inspection prwatch.Inspection, err error) *int {
	t.Helper()
	calls := 0
	previous := inspectProjectPR
	inspectProjectPR = func(_ context.Context, slug string) (prwatch.Inspection, error) {
		calls++
		if err != nil {
			return prwatch.Inspection{}, err
		}
		result := inspection
		result.Project = slug
		return result, nil
	}
	t.Cleanup(func() { inspectProjectPR = previous })
	return &calls
}

func openPRInspection(reviewDecision string) prwatch.Inspection {
	return prwatch.Inspection{
		Number: 7, Ref: "7", URL: "https://github.com/acme/widgets/pull/7",
		State: prwatch.StateOpen, ReviewDecision: reviewDecision,
		MergeStateStatus: "BLOCKED", HeadSHA: "head777",
	}
}

// recordItemPR gives an item the recorded pull request every change request
// routes against.
func recordItemPR(t *testing.T, p program.Program, itemID, ref string) program.Program {
	t.Helper()
	path := program.ManifestPath(program.ActiveDir(), p.Slug)
	loaded, err := program.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range loaded.Items {
		if loaded.Items[i].ID != itemID {
			continue
		}
		loaded.Items[i].PRRef = ref
		loaded.Items[i].PRGrantedAt = ""
		loaded.Items[i].PRGrantedBy = ""
		if loaded.Items[i].Status == program.ItemDispatched {
			loaded.Items[i].Status = program.ItemInReview
			loaded.Items[i].InReviewAt = loaded.Items[i].DispatchedAt
		}
	}
	if err := program.Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	return loaded
}

func liveWorkerAgent(manifest project.Manifest, status herdr.Status) herdr.Agent {
	return herdr.Agent{
		Status: status, PaneID: "w7:p9", TabID: "w7:t9", WorkspaceID: "w7",
		TerminalTitle: "relay:" + manifest.Slug + " - GitHub Copilot",
		ForegroundCWD: *manifest.Worktree, NativeSessionID: "session-9",
	}
}

func childProjectDir(slug string) string {
	return filepath.Dir(project.ManifestPath(project.ActiveDir(), slug))
}

// inboxMessages reads a child project's unread inbox and treats a mailbox that
// was never created as empty, so a test can assert that nothing was written.
func inboxMessages(t *testing.T, slug string) []mailbox.Message {
	t.Helper()
	messages, err := mailbox.List(childProjectDir(slug), mailbox.Inbox)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatal(err)
	}
	return messages
}

// createChangeFixture builds a managed program whose child project lives in a
// real git repository, so a merged change request can dispatch a second child
// with its own worktree and branch.
func createChangeFixture(t *testing.T, status program.ItemStatus) (program.Program, program.WorkItem, project.Manifest) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	saveProgramTestConfig(t)
	repo, err := filepath.EvalSymlinks(newTestRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	p, err := program.New("governance", "Ship governed changes", repo, "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "Build managed runtime", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	childSlug := "governance-" + item.ID
	if err := p.DispatchItem(item.ID, childSlug); err != nil {
		t.Fatal(err)
	}
	if status == program.ItemBlocked {
		if err := p.BlockItem(item.ID, "waiting for decision"); err != nil {
			t.Fatal(err)
		}
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	loadedItem, _ := p.Item(item.ID)
	worktree := filepath.Join(repo, ".worktrees", childSlug)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := project.Manifest{
		Slug: childSlug, Title: item.Title, Repo: repo, Agent: "copilot",
		Worktree: &worktree, Program: p.Slug, ProgramItem: item.ID, Phase: "implement",
	}
	manifestPath := project.ManifestPath(project.ActiveDir(), childSlug)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	return p, loadedItem, manifest
}

// loadFollowUpWorktree returns a dispatched follow-up child's worktree once
// Relay has actually created it.
func loadFollowUpWorktree(t *testing.T, slug string) (string, bool) {
	t.Helper()
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		return "", false
	}
	if manifest.Worktree == nil {
		return "", false
	}
	return *manifest.Worktree, true
}

// mergeProgramItem records the merge the reconciler would record, so a test can
// route a change request against an item whose pull request already landed.
func mergeProgramItem(t *testing.T, programSlug, itemID string) {
	t.Helper()
	path := program.ManifestPath(program.ActiveDir(), programSlug)
	loaded, err := program.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range loaded.Items {
		if loaded.Items[i].ID != itemID {
			continue
		}
		loaded.Items[i].Status = program.ItemMerged
		loaded.Items[i].MergedAt = loaded.Items[i].UpdatedAt
		if loaded.Items[i].InReviewAt == "" {
			loaded.Items[i].InReviewAt = loaded.Items[i].DispatchedAt
		}
	}
	if err := program.Save(path, loaded); err != nil {
		t.Fatal(err)
	}
}

func decodeChangeOutput(t *testing.T, out string) programWorkerChangeOutput {
	t.Helper()
	var result programWorkerChangeOutput
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	return result
}

func TestRequestChangeSendsDurableFeedbackToTheExistingWorker(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	client := installManagedHerdrFakes(t, &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, herdr.StatusIdle)}},
	})
	installFakePRInspection(t, openPRInspection("REVIEW_REQUIRED"), nil)

	out, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field to access_token", "--json")
	if err != nil {
		t.Fatalf("request-change: %v", err)
	}
	result := decodeChangeOutput(t, out)
	if result.Route != changeRouteSameWorker {
		t.Fatalf("route = %q, want %q", result.Route, changeRouteSameWorker)
	}
	if result.FollowUpItem != "" {
		t.Fatalf("an open unapproved pull request created follow-up %q", result.FollowUpItem)
	}
	if !result.Notified || result.WorkerPane != "w7:p9" {
		t.Fatalf("notified=%t pane=%q", result.Notified, result.WorkerPane)
	}
	unread, err := mailbox.List(childProjectDir(manifest.Slug), mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 1 {
		t.Fatalf("inbox messages = %d, want 1", len(unread))
	}
	if unread[0].ID != result.MessageID || unread[0].Kind != mailbox.KindFeedback {
		t.Fatalf("message = %q/%q", unread[0].ID, unread[0].Kind)
	}
	if unread[0].From != mailbox.ActorTL || unread[0].To != mailbox.ActorWorker {
		t.Fatalf("message route = %q -> %q", unread[0].From, unread[0].To)
	}
	if !strings.Contains(unread[0].Body, "Rename the token field to access_token") {
		t.Fatalf("message body lost the request: %q", unread[0].Body)
	}
	if len(client.prompted) != 1 || client.prompted[0].target != "w7:p9" {
		t.Fatalf("prompts = %#v, want exactly one doorbell to the worker pane", client.prompted)
	}
	if len(client.focused) != 0 {
		t.Fatalf("request-change stole focus: %v", client.focused)
	}
	after, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 1 {
		t.Fatalf("program items = %d, want the original item only", len(after.Items))
	}
}

func TestRequestChangeRetryDoesNotDuplicateFeedbackOrDoorbells(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	client := installManagedHerdrFakes(t, &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, herdr.StatusIdle)}},
	})
	installFakePRInspection(t, openPRInspection("REVIEW_REQUIRED"), nil)

	first, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err != nil {
		t.Fatalf("first request-change: %v", err)
	}
	second, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "  Rename   the token\nfield  ", "--json")
	if err != nil {
		t.Fatalf("retry request-change: %v", err)
	}
	firstResult := decodeChangeOutput(t, first)
	secondResult := decodeChangeOutput(t, second)
	if firstResult.MessageID != secondResult.MessageID {
		t.Fatalf("message ids = %q and %q, want one durable message",
			firstResult.MessageID, secondResult.MessageID)
	}
	if !secondResult.Reused {
		t.Fatal("the retry did not report reusing the existing request")
	}
	unread, err := mailbox.List(childProjectDir(manifest.Slug), mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 1 {
		t.Fatalf("inbox messages = %d after a retry, want 1", len(unread))
	}
	if len(client.prompted) != 1 {
		t.Fatalf("doorbells = %d after a retry, want 1", len(client.prompted))
	}
}

func TestRequestChangeAfterAWorkerAcknowledgedTheSameRequestWritesNothingNew(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	installManagedHerdrFakes(t, &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, herdr.StatusIdle)}},
	})
	installFakePRInspection(t, openPRInspection("REVIEW_REQUIRED"), nil)

	first, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err != nil {
		t.Fatalf("first request-change: %v", err)
	}
	messageID := decodeChangeOutput(t, first).MessageID
	projectDir := childProjectDir(manifest.Slug)
	if err := mailbox.Acknowledge(projectDir, mailbox.Inbox, messageID); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	second, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err != nil {
		t.Fatalf("retry request-change: %v", err)
	}
	if !decodeChangeOutput(t, second).Reused {
		t.Fatal("a request the worker already processed was written again")
	}
	unread, err := mailbox.List(projectDir, mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 0 {
		t.Fatalf("unread inbox messages = %d, want none", len(unread))
	}
}

func TestRequestChangeWithADifferentRequestWritesASecondMessage(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	installManagedHerdrFakes(t, &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, herdr.StatusIdle)}},
	})
	installFakePRInspection(t, openPRInspection("REVIEW_REQUIRED"), nil)

	if _, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json"); err != nil {
		t.Fatalf("first request-change: %v", err)
	}
	if _, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Also add a latency metric", "--json"); err != nil {
		t.Fatalf("second request-change: %v", err)
	}
	unread, err := mailbox.List(childProjectDir(manifest.Slug), mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 2 {
		t.Fatalf("inbox messages = %d, want one per distinct request", len(unread))
	}
}

func TestRequestChangeDoesNotInterruptABusyWorker(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	client := installManagedHerdrFakes(t, &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, herdr.StatusWorking)}},
	})
	installFakePRInspection(t, openPRInspection("REVIEW_REQUIRED"), nil)

	out, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err != nil {
		t.Fatalf("request-change: %v", err)
	}
	result := decodeChangeOutput(t, out)
	if result.Notified {
		t.Fatal("a working worker was interrupted")
	}
	if len(client.prompted) != 0 {
		t.Fatalf("prompts = %#v, want none for a working worker", client.prompted)
	}
	if !strings.Contains(result.Warning, "working") {
		t.Fatalf("warning = %q, want the busy worker explained", result.Warning)
	}
	unread, err := mailbox.List(childProjectDir(manifest.Slug), mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 1 {
		t.Fatalf("inbox messages = %d, want the durable request to remain", len(unread))
	}
}

func TestRequestChangeWithNoLiveWorkerKeepsTheMessageAndReportsStart(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	client := installManagedHerdrFakes(t, &fakeHerdrClient{agentResponses: [][]herdr.Agent{nil}})
	installFakePRInspection(t, openPRInspection("REVIEW_REQUIRED"), nil)

	out, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err != nil {
		t.Fatalf("request-change: %v", err)
	}
	result := decodeChangeOutput(t, out)
	if result.NextCommand != "relay program worker start "+p.Slug+" "+item.ID {
		t.Fatalf("next command = %q", result.NextCommand)
	}
	if len(client.created) != 0 {
		t.Fatalf("request-change created a second owner tab: %#v", client.created)
	}
	unread, err := mailbox.List(childProjectDir(manifest.Slug), mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 1 {
		t.Fatalf("inbox messages = %d, want the durable request to remain", len(unread))
	}
}

func TestRequestChangeKeepsAClosedUnmergedPullRequestOnItsOriginalWorker(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	installManagedHerdrFakes(t, &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, herdr.StatusIdle)}},
	})
	inspection := openPRInspection("REVIEW_REQUIRED")
	inspection.State = prwatch.StateClosed
	inspection.HeadSHA = ""
	installFakePRInspection(t, inspection, nil)

	out, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Reopen with the renamed field", "--json")
	if err != nil {
		t.Fatalf("request-change: %v", err)
	}
	result := decodeChangeOutput(t, out)
	if result.Route != changeRouteSameWorker {
		t.Fatalf("route = %q, want the original worker", result.Route)
	}
	if result.FollowUpItem != "" {
		t.Fatalf("a closed unmerged pull request created follow-up %q", result.FollowUpItem)
	}
	after, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 1 {
		t.Fatalf("program items = %d, want no new item", len(after.Items))
	}
}

func TestRequestChangeOnAnApprovedPullRequestCreatesAPendingFollowUp(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	client := installManagedHerdrFakes(t, &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, herdr.StatusIdle)}},
	})
	inspection := openPRInspection(prwatch.ReviewApproved)
	inspection.MergeStateStatus = "CLEAN"
	installFakePRInspection(t, inspection, nil)

	out, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err != nil {
		t.Fatalf("request-change: %v", err)
	}
	result := decodeChangeOutput(t, out)
	if result.Route != changeRouteFollowUpPending {
		t.Fatalf("route = %q, want %q", result.Route, changeRouteFollowUpPending)
	}
	if result.FollowUpStarted {
		t.Fatal("an approved pull request started a follow-up worker")
	}
	if len(client.prompted) != 0 {
		t.Fatalf("prompts = %#v, want none for a protected pull request", client.prompted)
	}
	if len(client.created) != 0 {
		t.Fatalf("created tabs = %#v, want none", client.created)
	}
	if unread := inboxMessages(t, manifest.Slug); len(unread) != 0 {
		t.Fatalf("inbox messages = %d, want the old worker left alone", len(unread))
	}
	after, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	followUp, ok := after.Item(result.FollowUpItem)
	if !ok {
		t.Fatalf("follow-up %q is missing from the program", result.FollowUpItem)
	}
	if followUp.Status != program.ItemPending {
		t.Fatalf("follow-up status = %q, want pending", followUp.Status)
	}
	if followUp.FollowUpOf != item.ID {
		t.Fatalf("follow-up of = %q, want %q", followUp.FollowUpOf, item.ID)
	}
	if followUp.RequestHash != result.RequestHash {
		t.Fatalf("follow-up hash = %q, want %q", followUp.RequestHash, result.RequestHash)
	}
	if len(followUp.Dependencies) != 1 || followUp.Dependencies[0] != item.ID {
		t.Fatalf("follow-up dependencies = %v, want the original item", followUp.Dependencies)
	}
	if followUp.Repo != item.Repo || followUp.Priority != item.Priority {
		t.Fatalf("follow-up inherited %q/%q, want %q/%q",
			followUp.Repo, followUp.Priority, item.Repo, item.Priority)
	}
	if len(followUp.Notes) == 0 || !strings.Contains(followUp.Notes[0], "Rename the token field") {
		t.Fatalf("follow-up notes lost the request: %v", followUp.Notes)
	}
	if followUp.ProjectSlug != "" {
		t.Fatalf("follow-up was dispatched to project %q", followUp.ProjectSlug)
	}
}

func TestRequestChangeOnAQueuedPullRequestCreatesAPendingFollowUp(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	client := installManagedHerdrFakes(t, &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, herdr.StatusIdle)}},
	})
	inspection := openPRInspection("REVIEW_REQUIRED")
	inspection.MergeStateStatus = prwatch.MergeStateQueued
	inspection.Queued = true
	installFakePRInspection(t, inspection, nil)

	out, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err != nil {
		t.Fatalf("request-change: %v", err)
	}
	result := decodeChangeOutput(t, out)
	if result.Route != changeRouteFollowUpPending {
		t.Fatalf("route = %q, want %q", result.Route, changeRouteFollowUpPending)
	}
	if !strings.Contains(result.Warning, "merge queue") {
		t.Fatalf("warning = %q, want the merge queue named", result.Warning)
	}
	if len(client.prompted) != 0 {
		t.Fatalf("prompts = %#v, want none for a queued pull request", client.prompted)
	}
}

func TestRequestChangeReusesAPendingFollowUpForTheSameRequest(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	installManagedHerdrFakes(t, &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, herdr.StatusIdle)}},
	})
	inspection := openPRInspection(prwatch.ReviewApproved)
	installFakePRInspection(t, inspection, nil)

	first, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err != nil {
		t.Fatalf("first request-change: %v", err)
	}
	second, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the  token   field", "--json")
	if err != nil {
		t.Fatalf("retry request-change: %v", err)
	}
	firstResult := decodeChangeOutput(t, first)
	secondResult := decodeChangeOutput(t, second)
	if firstResult.FollowUpItem != secondResult.FollowUpItem {
		t.Fatalf("follow-ups = %q and %q, want one", firstResult.FollowUpItem, secondResult.FollowUpItem)
	}
	if !secondResult.Reused {
		t.Fatal("the retry did not report reusing the existing follow-up")
	}
	third, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Also add a latency metric", "--json")
	if err != nil {
		t.Fatalf("different request-change: %v", err)
	}
	thirdResult := decodeChangeOutput(t, third)
	if thirdResult.FollowUpItem == firstResult.FollowUpItem {
		t.Fatal("a different request reused the first follow-up")
	}
	after, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 3 {
		t.Fatalf("program items = %d, want the original plus two follow-ups", len(after.Items))
	}
}

func TestRequestChangeOnAMergedPullRequestDispatchesAndStartsAFollowUp(t *testing.T) {
	p, item, manifest := createChangeFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	mergeProgramItem(t, p.Slug, item.ID)
	worker := liveWorkerAgent(manifest, herdr.StatusIdle)
	client := &fakeHerdrClient{tab: herdr.Tab{ID: "w7:t10", RootPaneID: "w7:p10"}}
	client.agentsHook = func() ([]herdr.Agent, error) {
		agents := []herdr.Agent{worker}
		if followUp, ok := loadFollowUpWorktree(t, "governance-w2"); ok {
			agents = append(agents, herdr.Agent{
				Status: herdr.StatusIdle, PaneID: "w7:p10", TabID: "w7:t10", WorkspaceID: "w7",
				TerminalTitle: "relay:governance-w2 - GitHub Copilot",
				ForegroundCWD: followUp, NativeSessionID: "session-10",
			})
		}
		return agents, nil
	}
	installManagedHerdrFakes(t, client)
	installWorkerFakes(t, client)
	inspection := openPRInspection(prwatch.ReviewApproved)
	inspection.State = prwatch.StateMerged
	installFakePRInspection(t, inspection, nil)

	out, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err != nil {
		t.Fatalf("request-change: %v", err)
	}
	result := decodeChangeOutput(t, out)
	if result.Route != changeRouteFollowUpDispatched {
		t.Fatalf("route = %q, want %q", result.Route, changeRouteFollowUpDispatched)
	}
	if !result.FollowUpStarted {
		t.Fatalf("follow-up %q was not started: %+v", result.FollowUpItem, result)
	}
	if result.WorkerPane != "w7:p10" {
		t.Fatalf("worker pane = %q, want the new follow-up pane", result.WorkerPane)
	}
	after, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	followUp, ok := after.Item(result.FollowUpItem)
	if !ok {
		t.Fatalf("follow-up %q is missing", result.FollowUpItem)
	}
	if followUp.Status != program.ItemDispatched {
		t.Fatalf("follow-up status = %q, want dispatched", followUp.Status)
	}
	if followUp.ProjectSlug == "" {
		t.Fatal("follow-up has no child project")
	}
	if followUp.ProjectSlug == item.ProjectSlug {
		t.Fatal("follow-up reused the original item's project")
	}
	if unread := inboxMessages(t, manifest.Slug); len(unread) != 0 {
		t.Fatalf("merged item's old worker received %d message(s)", len(unread))
	}
}

func TestRequestChangeReportsAPartialFollowUpWithoutRollingItBack(t *testing.T) {
	p, item, manifest := createChangeFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	mergeProgramItem(t, p.Slug, item.ID)
	worker := liveWorkerAgent(manifest, herdr.StatusIdle)
	client := installManagedHerdrFakes(t, &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{worker}},
		tab:            herdr.Tab{ID: "w7:t10", RootPaneID: "w7:p10"},
	})
	installWorkerFakes(t, client)
	inspection := openPRInspection(prwatch.ReviewApproved)
	inspection.State = prwatch.StateMerged
	installFakePRInspection(t, inspection, nil)

	_, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err == nil {
		t.Fatal("request-change succeeded although the worker never appeared")
	}
	if !strings.Contains(err.Error(), "not rolled back") {
		t.Fatalf("error = %v, want the durable follow-up explained", err)
	}
	after, loadErr := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(after.Items) != 2 {
		t.Fatalf("program items = %d, want the follow-up retained", len(after.Items))
	}
	followUp := after.Items[1]
	if followUp.FollowUpOf != item.ID {
		t.Fatalf("retained item is not a follow-up of %q: %+v", item.ID, followUp)
	}
}

func TestRequestChangeWritesNothingWhenGitHubCannotBeRead(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	client := installManagedHerdrFakes(t, &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, herdr.StatusIdle)}},
	})
	installFakePRInspection(t, prwatch.Inspection{}, errors.New("gh: network is unreachable"))
	programBefore, err := os.ReadFile(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json"); err == nil {
		t.Fatal("request-change succeeded with an unreadable GitHub")
	}
	programAfter, err := os.ReadFile(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if string(programBefore) != string(programAfter) {
		t.Fatal("request-change wrote program state after a failed GitHub read")
	}
	if unread := inboxMessages(t, manifest.Slug); len(unread) != 0 {
		t.Fatalf("inbox messages = %d after a failed GitHub read", len(unread))
	}
	if len(client.prompted) != 0 {
		t.Fatalf("prompts = %#v after a failed GitHub read", client.prompted)
	}
}

func TestRequestChangeRefusesAPullRequestMismatch(t *testing.T) {
	p, item, _ := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	installManagedHerdrFakes(t, &fakeHerdrClient{})
	inspection := openPRInspection("REVIEW_REQUIRED")
	inspection.Number, inspection.Ref = 9, "9"
	installFakePRInspection(t, inspection, nil)

	_, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err == nil {
		t.Fatal("request-change accepted a pull request the item does not record")
	}
	if !strings.Contains(err.Error(), "program tick") {
		t.Fatalf("error = %v, want reconciliation guidance", err)
	}
}

func TestRequestChangeRefusesAnItemWithNoRecordedPullRequest(t *testing.T) {
	p, item, _ := createWorkerFixture(t, program.ItemDispatched)
	installManagedHerdrFakes(t, &fakeHerdrClient{})
	calls := installFakePRInspection(t, openPRInspection("REVIEW_REQUIRED"), nil)

	_, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err == nil {
		t.Fatal("request-change accepted an item with no pull request")
	}
	if *calls != 0 {
		t.Fatalf("GitHub was read %d time(s) for an item with no pull request", *calls)
	}
}

func TestRequestChangeRefusesAnEmptyRequest(t *testing.T) {
	p, item, _ := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	installManagedHerdrFakes(t, &fakeHerdrClient{})
	calls := installFakePRInspection(t, openPRInspection("REVIEW_REQUIRED"), nil)

	for _, body := range []string{"", "   "} {
		if _, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
			"--body", body, "--json"); err == nil {
			t.Fatalf("request-change accepted body %q", body)
		}
	}
	if *calls != 0 {
		t.Fatalf("GitHub was read %d time(s) for an empty request", *calls)
	}
}

func TestRequestChangeKeepsTheRequestDurableWhenTheDoorbellIsUncertain(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "https://github.com/acme/widgets/pull/7")
	client := installManagedHerdrFakes(t, &fakeHerdrClient{
		agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, herdr.StatusIdle)}},
		promptErr:      herdr.ErrPromptDeliveryUncertain,
	})
	installFakePRInspection(t, openPRInspection("REVIEW_REQUIRED"), nil)

	out, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err != nil {
		t.Fatalf("request-change: %v", err)
	}
	result := decodeChangeOutput(t, out)
	if result.Notified {
		t.Fatal("an uncertain doorbell was reported as delivered")
	}
	if !strings.Contains(result.Warning, "durable") {
		t.Fatalf("warning = %q, want the durable request explained", result.Warning)
	}
	if len(inboxMessages(t, manifest.Slug)) != 1 {
		t.Fatal("the durable request did not survive an uncertain doorbell")
	}
	if len(client.prompted) != 1 {
		t.Fatalf("doorbells = %d, want exactly one attempt", len(client.prompted))
	}

	retry, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err != nil {
		t.Fatalf("retry request-change: %v", err)
	}
	if !decodeChangeOutput(t, retry).Reused {
		t.Fatal("the retry after an uncertain doorbell wrote a second request")
	}
	if len(inboxMessages(t, manifest.Slug)) != 1 {
		t.Fatal("the retry duplicated the durable request")
	}
	if len(client.prompted) != 1 {
		t.Fatalf("doorbells = %d after a retry, want the marked message to suppress a second one",
			len(client.prompted))
	}
}

func TestRequestChangeAcceptsBothRecordedPullRequestReferenceForms(t *testing.T) {
	for name, ref := range map[string]string{
		"url":    "https://github.com/acme/widgets/pull/7",
		"number": "#7",
	} {
		t.Run(name, func(t *testing.T) {
			p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
			p = recordItemPR(t, p, item.ID, ref)
			installManagedHerdrFakes(t, &fakeHerdrClient{
				agentResponses: [][]herdr.Agent{{liveWorkerAgent(manifest, herdr.StatusIdle)}},
			})
			installFakePRInspection(t, openPRInspection("REVIEW_REQUIRED"), nil)

			out, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
				"--body", "Rename the token field", "--json")
			if err != nil {
				t.Fatalf("request-change with recorded ref %q: %v", ref, err)
			}
			if decodeChangeOutput(t, out).Route != changeRouteSameWorker {
				t.Fatalf("route for recorded ref %q is wrong", ref)
			}
		})
	}
}

func TestRequestChangeRefusesAnUnparsableRecordedReference(t *testing.T) {
	p, item, _ := createWorkerFixture(t, program.ItemDispatched)
	p = recordItemPR(t, p, item.ID, "not-a-pull-request")
	installManagedHerdrFakes(t, &fakeHerdrClient{})
	calls := installFakePRInspection(t, openPRInspection("REVIEW_REQUIRED"), nil)

	_, err := runProgramCommand(t, "worker", "request-change", p.Slug, item.ID,
		"--body", "Rename the token field", "--json")
	if err == nil {
		t.Fatal("request-change accepted an unparsable recorded pull request reference")
	}
	if *calls != 1 {
		t.Fatalf("GitHub reads = %d, want exactly the one fail-closed read", *calls)
	}
}
