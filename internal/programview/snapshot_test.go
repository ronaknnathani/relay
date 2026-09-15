package programview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/prwatch"
)

func artifactText(artifact ArtifactDTO) string {
	if artifact.Text == nil {
		return ""
	}
	return *artifact.Text
}

func TestProgressDTOUsesMergedRatio(t *testing.T) {
	tests := []struct {
		name     string
		items    []program.WorkItem
		expected ProgressDTO
	}{
		{
			name:     "zero items",
			expected: ProgressDTO{},
		},
		{
			name:  "one of every status",
			items: progressItems(1, 1, 1, 1, 1, 1),
			expected: ProgressDTO{
				Total: 6, Pending: 1, Dispatched: 1, InReview: 1, Blocked: 1,
				Merged: 1, Canceled: 1, Completed: 2, Percent: 16,
			},
		},
		{
			name:  "reference distribution",
			items: progressItems(14, 1, 0, 0, 21, 3),
			expected: ProgressDTO{
				Total: 39, Pending: 14, Dispatched: 1, Merged: 21,
				Canceled: 3, Completed: 24, Percent: 53,
			},
		},
		{
			name:  "all canceled",
			items: progressItems(0, 0, 0, 0, 0, 4),
			expected: ProgressDTO{
				Total: 4, Canceled: 4, Completed: 4,
			},
		},
		{
			name:  "all merged",
			items: progressItems(0, 0, 0, 0, 4, 0),
			expected: ProgressDTO{
				Total: 4, Merged: 4, Completed: 4, Percent: 100,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := progressDTO(test.items); got != test.expected {
				t.Fatalf("progressDTO() = %+v, want %+v", got, test.expected)
			}
		})
	}
}

func progressItems(pending, dispatched, inReview, blocked, merged, canceled int) []program.WorkItem {
	var items []program.WorkItem
	for status, count := range map[program.ItemStatus]int{
		program.ItemPending:    pending,
		program.ItemDispatched: dispatched,
		program.ItemInReview:   inReview,
		program.ItemBlocked:    blocked,
		program.ItemMerged:     merged,
		program.ItemCancelled:  canceled,
	} {
		for range count {
			items = append(items, program.WorkItem{Status: status})
		}
	}
	return items
}

func TestBuildPopulatesProgramDetailAndDegradesPerSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	initProgramViewTestRepo(t, repo)

	at := "2026-08-25T16:00:00Z"
	p := program.Program{
		Revision:            1,
		Slug:                "relay-v1",
		Title:               "Relay V1",
		Repo:                repo,
		State:               program.StateActive,
		Agent:               "copilot",
		MaxOpenPRs:          3,
		CreatedAt:           at,
		UpdatedAt:           at,
		ApprovalRequestedAt: at,
		ApprovedAt:          at,
		ApprovedBy:          "ceo",
		Items: []program.WorkItem{
			{ID: "w1", Kind: program.ItemKindChange, Title: "merged", Priority: program.PriorityP0, Status: program.ItemMerged, Repo: repo, ProjectSlug: "child-merged", PRRef: "#1", Notes: []string{}, CreatedAt: at, UpdatedAt: at, DispatchedAt: at, InReviewAt: at, MergedAt: at, Dependencies: []string{}, ContractRefs: []string{}},
			{ID: "w2", Kind: program.ItemKindChange, Title: "review", Priority: program.PriorityP1, Status: program.ItemInReview, Repo: repo, ProjectSlug: "child-review", PRRef: "#42", Notes: []string{"watch CI"}, CreatedAt: at, UpdatedAt: at, DispatchedAt: at, InReviewAt: at, Dependencies: []string{"w1"}, ContractRefs: []string{"api@v1"}},
			{ID: "w3", Kind: program.ItemKindChange, Title: "orphan", Priority: program.PriorityP2, Status: program.ItemDispatched, Repo: repo, ProjectSlug: "child-missing", Notes: []string{}, CreatedAt: at, UpdatedAt: at, DispatchedAt: at, Dependencies: []string{}, ContractRefs: []string{}},
			{ID: "w4", Kind: program.ItemKindChange, Title: "blocked", Priority: program.PriorityP2, Status: program.ItemBlocked, Repo: repo, BlockedReason: "owner needed", Notes: []string{}, CreatedAt: at, UpdatedAt: at, Dependencies: []string{}, ContractRefs: []string{}},
			{ID: "w5", Kind: program.ItemKindChange, Title: "ready", Priority: program.PriorityP1, Status: program.ItemPending, Repo: repo, Notes: []string{}, CreatedAt: at, UpdatedAt: at, Dependencies: []string{"w1"}, ContractRefs: []string{"api@v1"}},
			{ID: "w6", Kind: program.ItemKindChange, Title: "canceled", Priority: program.PriorityP3, Status: program.ItemCancelled, Repo: repo, Notes: []string{}, CreatedAt: at, UpdatedAt: at, CancelledAt: at, Dependencies: []string{}, ContractRefs: []string{}},
		},
		Contracts: []program.Contract{{
			Name: "api", Version: 1, Ref: "api@v1", Path: "contracts/api/v1.md",
			SHA256: "abc", Status: program.ContractApproved, PublishedAt: at, ApprovedAt: at, ApprovedBy: "ceo",
		}},
		Decisions: []program.Decision{
			{ID: "d1", Kind: program.DecisionQuestion, RaisedBy: program.RaisedByWorker, ItemID: "w4", Question: "Who owns this?", Options: []string{"platform"}, CreatedAt: at},
			{ID: "d2", Kind: program.DecisionQuestion, RaisedBy: program.RaisedByTL, Question: "Ship?", Options: []string{"yes"}, Answer: "yes", ResolvedBy: "ceo", CreatedAt: at, ResolvedAt: at},
		},
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	programDir := program.ProgramDir(program.ActiveDir(), p.Slug)
	writeTestFile(t, filepath.Join(programDir, "goal.md"), "Ship Relay.\n")
	writeTestFile(t, filepath.Join(programDir, "contracts", "api", "v1.md"), "contract text\n")

	childDir := filepath.Join(project.ActiveDir(), "child-review")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(repo, ".worktrees", "child-review")
	prNumber := 42
	prURL := "https://github.example/pr/42"
	manifest := project.Manifest{
		Slug: "child-review", Title: "Review", Repo: repo, Branch: "feature", BaseBranch: "main",
		Program: p.Slug, ProgramItem: "w2",
		Worktree: &worktree, Status: "active", Workflow: "deliver-pr", Phase: "validate",
		Created: at, Updated: at, PR: project.PRInfo{Number: &prNumber, URL: &prURL},
		PhasesCompleted: []string{}, PhasesRemaining: []string{},
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest); err != nil {
		t.Fatal(err)
	}
	state, err := project.NewState(manifest.Slug, manifest.Workflow, []string{"plan", "validate"})
	if err != nil {
		t.Fatal(err)
	}
	state.Phases["plan"] = project.PhaseState{Status: project.PhaseDone, Artifact: "plan.md"}
	state.Phases["validate"] = project.PhaseState{Status: project.PhaseInProgress, Task: "2/3"}
	state.SetPR(prNumber, prURL)
	if err := project.SaveState(filepath.Join(childDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(childDir, "plan.md"), "selected plan\n")
	if _, err := mailbox.Send(childDir, mailbox.Inbox, mailbox.Message{
		ID: "in-1", Kind: mailbox.KindInstruction, Program: p.Slug, Item: "w2",
		From: mailbox.ActorTL, To: mailbox.ActorWorker, Body: "continue", Options: []string{}, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mailbox.Send(childDir, mailbox.Outbox, mailbox.Message{
		ID: "out-1", Kind: mailbox.KindQuestion, Program: p.Slug, Item: "w2",
		From: mailbox.ActorWorker, To: mailbox.ActorTL, Body: "ready?", Options: []string{}, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}

	github := fetcherFunc(func(_ context.Context, _, ref string) (PullRequestDTO, error) {
		return PullRequestDTO{Number: 42, Ref: ref, URL: prURL, State: "open", Checks: "passing"}, nil
	})
	agents := agentListerFunc(func() ([]herdr.Agent, error) {
		return []herdr.Agent{{Status: herdr.StatusWorking, PaneID: "p1", CWD: worktree}}, nil
	})

	got, err := Build(p.Slug, Options{
		Now:    func() time.Time { return time.Date(2026, 8, 25, 17, 0, 0, 0, time.UTC) },
		GitHub: github, Agents: agents, DetailItem: "w2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.DetailItem != "w2" || got.Progress != (ProgressDTO{
		Total: 6, Pending: 1, Dispatched: 1, InReview: 1, Blocked: 1, Merged: 1,
		Canceled: 1, Completed: 2, Percent: 16,
	}) {
		t.Fatalf("snapshot progress/detail = %+v / %q", got.Progress, got.DetailItem)
	}
	if strings.Join(got.Plan.Ready, ",") != "w5" ||
		strings.Join(got.Plan.InFlight, ",") != "w2,w3" ||
		strings.Join(got.Plan.Orphaned, ",") != "w3" {
		t.Fatalf("plan = %+v", got.Plan)
	}
	if !reflectLayers(got.Graph.Layers, [][]string{{"w1", "w3", "w4", "w6"}, {"w2", "w5"}}) {
		t.Fatalf("graph layers = %v", got.Graph.Layers)
	}
	detail := findSnapshotItem(t, got.Items, "w2")
	if detail.Child == nil || detail.Child.Workflow == nil ||
		detail.Child.Workflow.CurrentPhase != "validate" ||
		detail.RecordedPR == nil || detail.LivePR == nil || detail.Worker == nil ||
		!reflect.DeepEqual(detail.Mailbox, MailboxDTO{
			Available: true, Inbox: 1, Outbox: 1,
			InboxIDs: []string{"in-1"}, OutboxIDs: []string{"out-1"},
		}) {
		t.Fatalf("detail item = %+v", detail)
	}
	if detail.Artifacts[4].Name != "plan.md" || artifactText(detail.Artifacts[4]) != "selected plan\n" {
		t.Fatalf("selected artifact = %+v", detail.Artifacts[4])
	}
	nonDetail := findSnapshotItem(t, got.Items, "w3")
	for _, artifact := range nonDetail.Artifacts {
		if artifact.Text != nil {
			t.Fatalf("non-detail artifact included text: %+v", artifact)
		}
	}
	if artifactText(got.Contracts[0].Artifact) != "contract text\n" ||
		len(got.OpenDecisions) != 1 || len(got.ResolvedDecisions) != 1 {
		t.Fatalf("contracts/decisions = %+v / %+v / %+v", got.Contracts, got.OpenDecisions, got.ResolvedDecisions)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), ":null") {
		t.Fatalf("snapshot contains null JSON values:\n%s", data)
	}
}

func TestBuildLocalOnlySkipsExternalSourcesAndKeepsLocalProgramArtifacts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "repo")
	initProgramViewTestRepo(t, repo)

	at := "2026-09-14T20:00:00Z"
	p := program.Program{
		Revision: 1, Slug: "local-only", Title: "Local only", Repo: repo,
		State: program.StateActive, Agent: "copilot", MaxOpenPRs: 2,
		CreatedAt: at, UpdatedAt: at, ApprovalRequestedAt: at, ApprovedAt: at, ApprovedBy: "test",
		Items: []program.WorkItem{{
			ID: "w1", Kind: program.ItemKindChange, Title: "task",
			Priority: program.PriorityP1, Status: program.ItemDispatched,
			Dependencies: []string{}, ContractRefs: []string{"api@v1"},
			Repo: repo, ProjectSlug: "local-child", PRRef: "#42", Notes: []string{"local note"},
			CreatedAt: at, UpdatedAt: at, DispatchedAt: at,
		}},
		Contracts: []program.Contract{{
			Name: "api", Version: 1, Ref: "api@v1", Path: "contracts/api/v1.md",
			SHA256: "abc", Status: program.ContractApproved, PublishedAt: at,
			ApprovedAt: at, ApprovedBy: "test",
		}},
		Decisions: []program.Decision{{
			ID: "d1", Kind: program.DecisionQuestion, RaisedBy: program.RaisedByTL,
			ItemID: "w1", Question: "Use the local seed?", Options: []string{"yes"}, CreatedAt: at,
		}},
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	childDir := filepath.Join(project.ActiveDir(), "local-child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(repo, ".worktrees", "local-child")
	if err := project.Save(project.ManifestPath(project.ActiveDir(), "local-child"), project.Manifest{
		Slug: "local-child", Title: "Local child", Repo: repo, Branch: "feature",
		BaseBranch: "main", Worktree: &worktree, Status: "active", Workflow: "deliver-pr",
		Program: p.Slug, ProgramItem: "w1", Phase: "implement", Created: at, Updated: at,
		PhasesCompleted: []string{}, PhasesRemaining: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(childDir, "plan.md"), "task body")
	programDir := program.ProgramDir(program.ActiveDir(), p.Slug)
	writeTestFile(t, filepath.Join(programDir, "goal.md"), "goal")
	writeTestFile(t, filepath.Join(programDir, "contracts", "api", "v1.md"), "contract body")

	var githubCalls, agentCalls int
	got, err := Build(p.Slug, Options{
		LocalOnly: true,
		GitHub: fetcherFunc(func(context.Context, string, string) (PullRequestDTO, error) {
			githubCalls++
			return PullRequestDTO{}, nil
		}),
		PRIndex: prIndexFunc(func(string) (PRState, bool) {
			githubCalls++
			return PRStateOpen, true
		}),
		Agents: agentListerFunc(func() ([]herdr.Agent, error) {
			agentCalls++
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if githubCalls != 0 || agentCalls != 0 {
		t.Fatalf("external calls = GitHub %d, Herdr %d; want zero", githubCalls, agentCalls)
	}
	if got.SourceHealth.GitHub.Status != "loading" || got.SourceHealth.Herdr.Status != "loading" {
		t.Fatalf("source health = %+v", got.SourceHealth)
	}
	item := findSnapshotItem(t, got.Items, "w1")
	if item.Repo != repo || item.ProjectSlug != "local-child" ||
		len(item.Contracts) != 1 || item.Contracts[0] != "api@v1" ||
		len(item.Notes) != 1 || item.Notes[0] != "local note" ||
		item.Timestamps.DispatchedAt != at || item.Child == nil ||
		item.Child.Manifest.Slug != "local-child" || len(item.Decisions) != 1 {
		t.Fatalf("local task metadata = %+v", item)
	}
	for _, artifact := range item.Artifacts {
		if artifact.Text != nil {
			t.Fatalf("task artifact included text: %+v", artifact)
		}
		if artifact.Path != artifact.Name {
			t.Fatalf("task artifact path = %q, want %q", artifact.Path, artifact.Name)
		}
	}
	if len(got.ProgramArtifacts) != 3 || !got.ProgramArtifacts[0].Present {
		t.Fatalf("program artifact metadata = %+v", got.ProgramArtifacts)
	}
	if artifactText(got.ProgramArtifacts[0]) != "goal" {
		t.Fatalf("local goal artifact = %+v", got.ProgramArtifacts[0])
	}
	if got.Program.DisplayTitle != "Local only" || got.Program.Summary != "goal" {
		t.Fatalf("local program identity = %+v", got.Program)
	}
	if len(got.OpenDecisions) != 1 || len(got.Contracts) != 1 {
		t.Fatalf("local decisions/contracts = %+v / %+v", got.OpenDecisions, got.Contracts)
	}
	if got.Contracts[0].Artifact.Text != nil {
		t.Fatalf("contract artifact included text: %+v", got.Contracts[0].Artifact)
	}
	if got.Items == nil || got.Contracts == nil || got.Warnings == nil {
		t.Fatalf("local snapshot contains nil arrays: %+v", got)
	}

	data, err := json.Marshal(ArtifactDTO{Name: "missing.md", Path: "missing.md"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"name":"missing.md","path":"missing.md","present":false,"size":0,"updated_at":"","truncated":false}` {
		t.Fatalf("artifact JSON = %s", data)
	}
}

func TestBuildRejectsUnownedLinkedChildDetails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newProgramViewTestRepo(t)
	p, err := program.New("ownership", "Ownership", repo, "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "Child", Priority: program.PriorityP0})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(item.ID, "linked-child"); err != nil {
		t.Fatal(err)
	}
	for i := range p.Items {
		if p.Items[i].ID == item.ID {
			p.Items[i].PRRef = "#42"
		}
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	foreignPR := "https://github.example/acme/foreign/pull/99"
	saveProjectManifest(t, project.ActiveDir(), project.Manifest{
		Slug: "linked-child", Repo: repo, Branch: "feature",
		Program: "other-program", ProgramItem: item.ID,
		PR: project.PRInfo{URL: &foreignPR},
	})
	writeTestFile(t, filepath.Join(project.ActiveDir(), "linked-child", "assignment.md"), "foreign child")

	snapshot, err := Build(p.Slug, Options{LocalOnly: true, DetailItem: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	got := findSnapshotItem(t, snapshot.Items, item.ID)
	if got.ChildAvailable || got.Child != nil || got.Worker != nil ||
		got.RecordedPR == nil || got.RecordedPR.Ref != "#42" {
		t.Fatalf("item = %+v, want only the item-recorded pull request", got)
	}
	if len(got.Warnings) == 0 || !strings.Contains(got.Warnings[0], "does not match linked item") {
		t.Fatalf("warnings = %+v", got.Warnings)
	}
	for _, artifact := range got.Artifacts {
		if artifact.Text != nil {
			t.Fatalf("unowned artifact exposed: %+v", artifact)
		}
	}
}

func TestBuildPrefetchesPullRequestsWithBoundedConcurrency(t *testing.T) {
	release := make(chan struct{})
	var active, maximum atomic.Int32
	fetcher := fetcherFunc(func(context.Context, string, string) (PullRequestDTO, error) {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		<-release
		active.Add(-1)
		return PullRequestDTO{State: "open"}, nil
	})
	done := make(chan struct{})
	var results map[PullRequestKey]memoResult
	go func() {
		keys := make([]PullRequestKey, 0, 8)
		for _, ref := range []string{"#1", "#2", "#3", "#4", "#5", "#6", "#7", "#8"} {
			keys = append(keys, PullRequestKey{Repo: "/repo", Ref: ref})
		}
		results = prefetchPullRequests(context.Background(), keys, fetcher)
		close(done)
	}()
	deadline := time.Now().Add(250 * time.Millisecond)
	for maximum.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	<-done
	if got := maximum.Load(); got < 2 || got > 4 {
		t.Fatalf("maximum concurrent fetches = %d, want 2-4", got)
	}
	if len(results) != 8 {
		t.Fatalf("prefetched results = %d, want 8", len(results))
	}
}

type fetcherFunc func(context.Context, string, string) (PullRequestDTO, error)

func (f fetcherFunc) Fetch(ctx context.Context, repo, ref string) (PullRequestDTO, error) {
	return f(ctx, repo, ref)
}

type agentListerFunc func() ([]herdr.Agent, error)

func (f agentListerFunc) Agents() ([]herdr.Agent, error) {
	return f()
}

type provenanceAgentLister struct {
	result AgentSnapshot
	err    error
}

func (l provenanceAgentLister) Agents() ([]herdr.Agent, error) {
	return l.result.Agents, l.err
}

func (l provenanceAgentLister) AgentsWithProvenance() (AgentSnapshot, error) {
	return l.result, l.err
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findSnapshotItem(t *testing.T, items []ItemDTO, id string) ItemDTO {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("item %s not found in %+v", id, items)
	return ItemDTO{}
}

func reflectLayers(got, want [][]string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if strings.Join(got[i], ",") != strings.Join(want[i], ",") {
			return false
		}
	}
	return true
}

func TestBuildUsesStalePRAndReportsDegradedSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	initProgramViewTestRepo(t, repo)
	at := "2026-08-25T16:00:00Z"
	p := program.Program{
		Revision: 1, Slug: "degraded", Title: "Degraded", Repo: repo,
		State: program.StateAbandoned, Agent: "copilot", MaxOpenPRs: 1,
		CreatedAt: at, UpdatedAt: at, AbandonedAt: at,
		Items: []program.WorkItem{{
			ID: "w1", Kind: program.ItemKindChange, Title: "worker", Priority: program.PriorityP1,
			Status: program.ItemDispatched, Repo: repo, ProjectSlug: "child",
			PRRef: "#7", Notes: []string{}, Dependencies: []string{}, ContractRefs: []string{},
			CreatedAt: at, UpdatedAt: at, DispatchedAt: at,
		}},
		Contracts: []program.Contract{}, Decisions: []program.Decision{},
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	childDir := filepath.Join(project.ActiveDir(), "child")
	if err := os.MkdirAll(filepath.Join(childDir, "mail", "inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(childDir, "mail", "outbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(repo, ".worktrees", "child")
	number := 7
	if err := project.Save(project.ManifestPath(project.ActiveDir(), "child"), project.Manifest{
		Slug: "child", Title: "Child", Repo: repo, Branch: "feature", Worktree: &worktree,
		Program: p.Slug, ProgramItem: "w1",
		Status: "active", Created: at, Updated: at, PR: project.PRInfo{Number: &number},
		PhasesCompleted: []string{}, PhasesRemaining: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(childDir, "assignment.md"), "123456789")
	writeTestFile(t, filepath.Join(childDir, "mail", "outbox", "bad.json"), "{")

	got, err := Build(p.Slug, Options{
		DetailItem: "w1", ArtifactLimit: 5,
		GitHub: fetcherFunc(func(context.Context, string, string) (PullRequestDTO, error) {
			return PullRequestDTO{
				Number: 7, Ref: "#7", State: "open", Checks: "passing",
				Stale: true, FetchedAt: at, StaleReason: "gh unavailable",
			}, errors.New("gh unavailable")
		}),
		Agents: provenanceAgentLister{
			result: AgentSnapshot{Agents: []herdr.Agent{{
				Status: herdr.StatusWorking, PaneID: "pane-stale", CWD: worktree,
			}}, Stale: true, FetchedAt: at, StaleReason: "herdr unavailable"},
			err: errors.New("herdr unavailable"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	item := findSnapshotItem(t, got.Items, "w1")
	if item.RecordedPR == nil || item.RecordedPR.Number != 7 ||
		item.LivePR == nil || !item.LivePR.Stale || item.LivePR.FetchedAt != at {
		t.Fatalf("PR provenance = recorded %+v live %+v", item.RecordedPR, item.LivePR)
	}
	if item.Worker == nil || item.Worker.PaneID != "pane-stale" ||
		!item.Worker.Stale || item.Worker.FetchedAt != at ||
		item.Worker.StaleReason != "herdr unavailable" {
		t.Fatalf("stale Herdr worker = %+v", item.Worker)
	}
	if artifactText(item.Artifacts[0]) != "12345" || !item.Artifacts[0].Truncated {
		t.Fatalf("truncated artifact = %+v", item.Artifacts[0])
	}
	if got.SourceHealth.GitHub.Status != "degraded" ||
		!got.SourceHealth.GitHub.Stale || got.SourceHealth.GitHub.FetchedAt != at ||
		got.SourceHealth.Herdr.Status != "degraded" ||
		!got.SourceHealth.Herdr.Stale || got.SourceHealth.Herdr.FetchedAt != at ||
		got.SourceHealth.Mailbox.Status != "degraded" {
		t.Fatalf("source health = %+v", got.SourceHealth)
	}
	if len(item.Warnings) < 3 {
		t.Fatalf("item warnings = %v", item.Warnings)
	}

	recordedOnly, err := Build(p.Slug, Options{
		GitHub: fetcherFunc(func(context.Context, string, string) (PullRequestDTO, error) {
			return PullRequestDTO{}, errors.New("stale cache expired")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	recordedItem := findSnapshotItem(t, recordedOnly.Items, "w1")
	if recordedItem.RecordedPR == nil || recordedItem.LivePR != nil ||
		recordedOnly.SourceHealth.GitHub.Status != "degraded" {
		t.Fatalf("expired fallback = item %+v, source %+v", recordedItem, recordedOnly.SourceHealth.GitHub)
	}

	if err := program.Archive(p.Slug); err != nil {
		t.Fatal(err)
	}
	archived, err := Build(p.Slug, Options{DetailItem: "w99"})
	if err != nil {
		t.Fatal(err)
	}
	if !archived.Program.Archived || archived.DetailItem != "" ||
		!strings.Contains(strings.Join(archived.Warnings, "\n"), `detail item "w99" not found`) {
		t.Fatalf("archived snapshot = %+v", archived)
	}
	if archived.Progress != got.Progress {
		t.Fatalf("archived progress = %+v, want %+v", archived.Progress, got.Progress)
	}
}

func TestBuildSkipsMalformedSiblingProjectStateWithoutFabricatingOrphansOrCapacity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	initProgramViewTestRepo(t, repo)
	at := "2026-08-25T16:00:00Z"
	p := program.Program{
		Revision: 1, Slug: "tolerant", Title: "Tolerant", Repo: repo,
		State: program.StateActive, Agent: "copilot", MaxOpenPRs: 2,
		CreatedAt: at, UpdatedAt: at, ApprovalRequestedAt: at, ApprovedAt: at, ApprovedBy: "ceo",
		Items: []program.WorkItem{
			{
				ID: "w1", Kind: program.ItemKindChange, Title: "healthy", Priority: program.PriorityP1,
				Status: program.ItemDispatched, Repo: repo, ProjectSlug: "healthy-child",
				Notes: []string{}, Dependencies: []string{}, ContractRefs: []string{},
				CreatedAt: at, UpdatedAt: at, DispatchedAt: at,
			},
			{
				ID: "w2", Kind: program.ItemKindChange, Title: "broken", Priority: program.PriorityP1,
				Status: program.ItemDispatched, Repo: repo, ProjectSlug: "broken-child",
				PRRef: "#8",
				Notes: []string{}, Dependencies: []string{}, ContractRefs: []string{},
				CreatedAt: at, UpdatedAt: at, DispatchedAt: at,
			},
		},
		Contracts: []program.Contract{}, Decisions: []program.Decision{},
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	for index, slug := range []string{"healthy-child", "broken-child"} {
		childDir := filepath.Join(project.ActiveDir(), slug)
		if err := os.MkdirAll(childDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := project.Save(project.ManifestPath(project.ActiveDir(), slug), project.Manifest{
			Slug: slug, Title: slug, Repo: repo, Branch: slug, BaseBranch: "main",
			Program: p.Slug, ProgramItem: fmt.Sprintf("w%d", index+1),
			Status: "active", Created: at, Updated: at, PhasesCompleted: []string{}, PhasesRemaining: []string{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	unlinkedNumber := 43
	if err := os.MkdirAll(filepath.Join(project.ActiveDir(), "unlinked-child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), "unlinked-child"), project.Manifest{
		Slug: "unlinked-child", Title: "unlinked-child", Repo: repo, Branch: "unlinked-child", BaseBranch: "main",
		PR:     project.PRInfo{Number: &unlinkedNumber},
		Status: "active", Created: at, Updated: at, PhasesCompleted: []string{}, PhasesRemaining: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	healthyState, err := project.NewState("healthy-child", "deliver-pr", []string{"validate"})
	if err != nil {
		t.Fatal(err)
	}
	healthyState.SetPR(42, "https://github.example/pr/42")
	if err := project.SaveState(project.StatePath("healthy-child"), healthyState); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, project.StatePath("broken-child"), "{")

	got, err := Build(p.Slug, Options{})
	if err != nil {
		t.Fatal(err)
	}
	healthy := findSnapshotItem(t, got.Items, "w1")
	broken := findSnapshotItem(t, got.Items, "w2")
	if healthy.Status != string(program.ItemInReview) || broken.Status != string(program.ItemDispatched) {
		t.Fatalf("item statuses = healthy %q, broken %q", healthy.Status, broken.Status)
	}
	if healthy.Orphaned || broken.Orphaned || len(got.Plan.Orphaned) != 0 {
		t.Fatalf("fabricated orphan state: plan %+v, healthy %+v, broken %+v", got.Plan, healthy, broken)
	}
	if got.Plan.Capacity != (CapacityDTO{Limit: 2, Open: 2}) {
		t.Fatalf("capacity = %+v", got.Plan.Capacity)
	}
	projectWarnings := strings.Join(got.SourceHealth.Projects.Warnings, "\n")
	if got.SourceHealth.Projects.Status != "degraded" ||
		!strings.Contains(projectWarnings, "broken-child") ||
		strings.Contains(projectWarnings, "healthy-child") {
		t.Fatalf("project source health = %+v", got.SourceHealth.Projects)
	}
}

func TestGraphDTOFlagsCyclesStably(t *testing.T) {
	graph := graphDTO([]program.WorkItem{
		{ID: "w2", Title: "two", Status: program.ItemPending, Dependencies: []string{"w1"}},
		{ID: "w1", Title: "one", Status: program.ItemPending, Dependencies: []string{"w2"}},
	})
	if !graph.Cyclic || len(graph.Nodes) != 2 ||
		graph.Nodes[0].ID != "w1" || graph.Nodes[1].ID != "w2" ||
		!reflectLayers(graph.Layers, [][]string{{"w1", "w2"}}) {
		t.Fatalf("cyclic graph = %+v", graph)
	}
}

func TestBuildReturnsReadOnlySnapshotWithNonNilArrays(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, err := program.New("relay-v1", "Relay V1", filepath.Join(home, "repo"), "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}

	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	programDir := program.ProgramDir(program.ActiveDir(), p.Slug)
	if err := os.WriteFile(filepath.Join(programDir, "goal.md"), []byte("Ship Relay.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 16, 0, 0, 0, time.UTC)

	got, err := Build(p.Slug, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != SchemaVersion || got.GeneratedAt != now.Format(time.RFC3339) {
		t.Fatalf("snapshot identity = %#v", got)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"items":[]`, `"contracts":[]`, `"open_decisions":[]`,
		`"resolved_decisions":[]`, `"warnings":[]`,
	} {
		if !strings.Contains(string(data), field) {
			t.Errorf("snapshot JSON missing %s:\n%s", field, data)
		}
	}
	if got.ProgramArtifacts[0].Name != "goal.md" || artifactText(got.ProgramArtifacts[0]) != "Ship Relay.\n" {
		t.Fatalf("goal artifact = %+v, warnings = %v", got.ProgramArtifacts[0], got.Warnings)
	}
}

func TestBuildMapsRoutedWorkflowState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, childDir := newMailboxSnapshotProgram(t)
	manifestPath := project.ManifestPath(project.ActiveDir(), "mail-child")
	manifest, err := project.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	head := initProgramViewGitRepo(t, *manifest.Worktree)
	manifest.StartSHA = head
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	state, err := project.NewState("mail-child", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	current, err := gitx.Snapshot(*manifest.Worktree, head)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := project.RepositorySnapshot{
		BaseSHA: current.BaseSHA, HeadSHA: current.HeadSHA, Fingerprint: current.Fingerprint,
		FileCount: current.FileCount, ChangedLines: current.ChangedLines,
	}
	snapshot.InputRevision, err = project.ProjectInputRevision(childDir)
	if err != nil {
		t.Fatal(err)
	}
	reasons := make(map[string]string, len(project.AdaptiveDeliveryPhases))
	for _, phase := range project.AdaptiveDeliveryPhases {
		reasons[phase] = "test route decision"
	}
	state.Route = &project.RouteDecision{
		Class:           project.RouteEasy,
		SelectedPhases:  []string{"route", "implement", "open-pr"},
		PhaseReasons:    reasons,
		ReviewRoles:     []string{project.ReviewRoleCodeReviewer},
		ReviewOwner:     project.EvidenceOwnerImplement,
		ValidationOwner: "implement",
		Snapshot:        snapshot,
		EvaluatedAt:     "2026-09-15T00:00:00Z",
		Facts: project.RouteFacts{
			RequestedBehaviorExplicit: true,
			GatePolicy:                project.GatePolicy{Mode: project.GatePolicyNone},
			RiskAssessmentComplete:    true,
			AssessmentFingerprint:     snapshot.Revision(),
			PredictedSizeKnown:        true,
			PredictedFileCount:        1,
			PredictedChangedLines:     10,
		},
	}
	state.Route.Revision = 1
	digest, err := project.RouteDigest(*state.Route)
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Digest = digest
	state.SubagentCount = 1
	state.Phases["route"] = project.PhaseState{
		Status: project.PhaseDone, Outcome: project.PhaseOutcomeMaterial,
		StartedAt: "2026-09-15T00:00:00Z", EndedAt: "2026-09-15T00:01:00Z",
	}
	state.Phases["clarify"] = project.PhaseState{
		Status: project.PhaseSkipped, Reason: "requirements are explicit",
		Outcome: project.PhaseOutcomeNoOp,
	}
	state.Evidence.Review = &project.EvidenceRecord{
		Snapshot: snapshot, Result: project.EvidencePassed,
		RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
		DispatchID:  "review-dispatch",
		Owner:       project.EvidenceOwnerImplement,
		CompletedAt: "2026-09-15T00:02:00Z",
		Roles:       []string{project.ReviewRoleCodeReviewer},
	}
	state.Evidence.Validation = &project.EvidenceRecord{
		Snapshot: project.RepositorySnapshot{
			BaseSHA: "base", HeadSHA: "head", Fingerprint: "older",
		},
		RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
		DispatchID: "validation-dispatch",
		Result:     project.EvidencePassed, Owner: project.EvidenceOwnerImplement,
		CompletedAt: "2026-09-15T00:03:00Z",
		NoGates:     true,
	}
	if err := project.SaveState(filepath.Join(childDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}

	item := findSnapshotItem(t, mustBuild(t, p.Slug).Items, "w1")
	workflow := item.Child.Workflow
	if workflow == nil || workflow.RouteClass != project.RouteEasy || workflow.SubagentCount != 1 ||
		!workflow.ReviewFresh || workflow.ValidationFresh || workflow.CurrentPhase != "implement" {
		t.Fatalf("routed workflow = %+v", workflow)
	}
	if workflow.Phases[1].Reason != "requirements are explicit" ||
		workflow.Phases[1].Outcome != project.PhaseOutcomeNoOp {
		t.Fatalf("routed clarify phase = %+v", workflow.Phases[1])
	}

	if err := os.WriteFile(filepath.Join(*manifest.Worktree, "new.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workflow = findSnapshotItem(t, mustBuild(t, p.Slug).Items, "w1").Child.Workflow
	if workflow.ReviewFresh || workflow.ValidationFresh {
		t.Fatalf("workflow freshness ignored current worktree changes: %+v", workflow)
	}
}

func TestChildDTOSkipsFreshnessSnapshotWithoutEvidenceOrWhenArchived(t *testing.T) {
	for _, test := range []struct {
		name     string
		archived bool
		evidence bool
	}{
		{name: "no evidence"},
		{name: "archived with evidence", archived: true, evidence: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			childDir := t.TempDir()
			missingWorktree := filepath.Join(t.TempDir(), "removed")
			manifest := project.Manifest{
				Slug: "child", Worktree: &missingWorktree, Status: "active",
				Workflow: "deliver-pr",
			}
			state, err := project.NewState("child", "deliver-pr", project.AdaptiveDeliveryPhases)
			if err != nil {
				t.Fatal(err)
			}
			reasons := make(map[string]string, len(project.AdaptiveDeliveryPhases))
			for _, phase := range project.AdaptiveDeliveryPhases {
				reasons[phase] = "test route"
			}
			state.Route = &project.RouteDecision{
				Class:           project.RouteEasy,
				SelectedPhases:  []string{"route", "implement", "open-pr"},
				PhaseReasons:    reasons,
				ReviewRoles:     []string{project.ReviewRoleCodeReviewer},
				ReviewOwner:     project.EvidenceOwnerImplement,
				ValidationOwner: "implement",
				Snapshot: project.RepositorySnapshot{
					BaseSHA: "base", HeadSHA: "head", Fingerprint: "fingerprint",
				},
				EvaluatedAt: "2026-09-15T00:00:00Z",
				Facts: project.RouteFacts{
					RequestedBehaviorExplicit: true,
					GatePolicy:                project.GatePolicy{Mode: project.GatePolicyNone},
					RiskAssessmentComplete:    true,
					AssessmentFingerprint:     "fingerprint",
					PredictedSizeKnown:        true,
				},
			}
			state.Route.Revision = 1
			digest, err := project.RouteDigest(*state.Route)
			if err != nil {
				t.Fatal(err)
			}
			state.Route.Digest = digest
			if test.evidence {
				state.Evidence.Review = &project.EvidenceRecord{
					Snapshot: state.Route.Snapshot, Result: project.EvidencePassed,
					RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
					DispatchID:  "review-dispatch",
					Owner:       project.EvidenceOwnerImplement,
					CompletedAt: "2026-09-15T00:01:00Z",
					Roles:       []string{project.ReviewRoleCodeReviewer},
				}
			}
			if err := project.SaveState(filepath.Join(childDir, "state.json"), state); err != nil {
				t.Fatal(err)
			}

			var warnings []string
			child := childDTO(manifest, childDir, test.archived, &warnings)
			if child.Workflow == nil || len(warnings) != 0 {
				t.Fatalf("child = %+v, warnings = %v", child, warnings)
			}
		})
	}
}

func initProgramViewGitRepo(t *testing.T, repo string) string {
	t.Helper()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repo}, args...)...)
		command.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
			"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
		)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("relay\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-q", "-m", "init")
	return run("rev-parse", "HEAD")
}

// countingFetcher records how many GitHub subprocesses one snapshot would run.
type countingFetcher struct {
	state string
	calls map[string]int
}

func (f *countingFetcher) Fetch(_ context.Context, _, ref string) (PullRequestDTO, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[ref]++
	return PullRequestDTO{Number: 42, Ref: ref, State: f.state, Checks: "passing"}, nil
}

func TestBuildReusesOneGitHubFetchPerRecordedPullRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	initProgramViewTestRepo(t, repo)

	number := 42
	saveProjectManifest(t, project.ActiveDir(), project.Manifest{
		Slug: "child", Repo: repo, Branch: "feature", Program: "relay-v1", ProgramItem: "w1",
		PR: project.PRInfo{Number: &number},
	})
	p, err := program.New("relay-v1", "Relay V1", repo, "copilot", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "Child", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(item.ID, "child"); err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	fetcher := &countingFetcher{state: "merged"}

	snapshot, err := Build(p.Slug, Options{GitHub: fetcher})
	if err != nil {
		t.Fatal(err)
	}
	if calls := fetcher.calls["#42"]; calls != 1 {
		t.Fatalf("GitHub fetches for #42 = %d, want exactly 1 shared fetch", calls)
	}
	if len(fetcher.calls) != 1 {
		t.Fatalf("GitHub fetches = %#v, want only the recorded pull request", fetcher.calls)
	}
	if snapshot.Progress.Merged != 1 || snapshot.Plan.Capacity.Open != 0 {
		t.Fatalf("snapshot progress = %+v capacity = %+v", snapshot.Progress, snapshot.Plan.Capacity)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].LivePR == nil {
		t.Fatalf("snapshot items = %+v", snapshot.Items)
	}
}

func TestBuildMergesSecondaryRepositoryItemAndReadiesPrimaryDependent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	primary := newProgramViewTestRepo(t)
	secondary := newProgramViewTestRepo(t)
	p, err := program.New("multi-repo", "Multi repo", primary, "copilot", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	dependency, err := p.AddItem(program.WorkItem{
		Title: "Secondary dependency", Priority: program.PriorityP0, Repo: secondary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(dependency.ID, "secondary-child"); err != nil {
		t.Fatal(err)
	}
	dependent, err := p.AddItem(program.WorkItem{
		Title: "Primary dependent", Priority: program.PriorityP0, Dependencies: []string{dependency.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	number := 42
	saveProjectManifest(t, project.ActiveDir(), project.Manifest{
		Slug: "secondary-child", Repo: secondary, Branch: "feature",
		Program: p.Slug, ProgramItem: dependency.ID, PR: project.PRInfo{Number: &number},
	})
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	var wrongRepo atomic.Bool
	fetcher := fetcherFunc(func(_ context.Context, repo, ref string) (PullRequestDTO, error) {
		if repo != secondary {
			wrongRepo.Store(true)
		}
		return PullRequestDTO{Number: 42, Ref: ref, State: "merged"}, nil
	})

	snapshot, err := Build(p.Slug, Options{GitHub: fetcher})
	if err != nil {
		t.Fatal(err)
	}
	gotDependency := findSnapshotItem(t, snapshot.Items, dependency.ID)
	gotDependent := findSnapshotItem(t, snapshot.Items, dependent.ID)
	if gotDependency.Status != string(program.ItemMerged) || !gotDependent.Ready {
		t.Fatalf("dependency = %+v, dependent = %+v", gotDependency, gotDependent)
	}
	if !reflect.DeepEqual(snapshot.Plan.Ready, []string{dependent.ID}) {
		t.Fatalf("ready items = %v", snapshot.Plan.Ready)
	}
	if wrongRepo.Load() {
		t.Fatalf("GitHub fetch used a repository other than %q", secondary)
	}
}

// The patrol fingerprints unread worker mail by identifier, so the snapshot must
// expose the exact unread ids and not only their count.
func TestBuildExposesSortedUnreadMailboxIdentifiers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, childDir := newMailboxSnapshotProgram(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	for _, id := range []string{"out-2", "out-1"} {
		if _, err := mailbox.Send(childDir, mailbox.Outbox, mailbox.Message{
			ID: id, Kind: mailbox.KindQuestion, Program: p.Slug, Item: "w1",
			From: mailbox.ActorWorker, To: mailbox.ActorTL, Body: "ready?",
			Options: []string{}, CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := Build(p.Slug, Options{})
	if err != nil {
		t.Fatal(err)
	}
	item := findSnapshotItem(t, snapshot.Items, "w1")
	if !reflect.DeepEqual(item.Mailbox.OutboxIDs, []string{"out-1", "out-2"}) {
		t.Fatalf("outbox ids = %v, want sorted [out-1 out-2]", item.Mailbox.OutboxIDs)
	}
	if !reflect.DeepEqual(item.Mailbox.InboxIDs, []string{}) {
		t.Fatalf("inbox ids = %v, want empty", item.Mailbox.InboxIDs)
	}
	if item.Mailbox.Outbox != 2 || !item.Mailbox.Available {
		t.Fatalf("mailbox = %+v", item.Mailbox)
	}
}

func newMailboxSnapshotProgram(t *testing.T) (program.Program, string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "repo")
	initProgramViewTestRepo(t, repo)
	at := "2026-08-26T12:00:00Z"
	p := program.Program{
		Revision: 1, Slug: "mail-ids", Title: "Mail ids", Repo: repo,
		State: program.StateActive, Agent: "copilot", MaxOpenPRs: 3,
		CreatedAt: at, UpdatedAt: at, ApprovalRequestedAt: at, ApprovedAt: at, ApprovedBy: "ceo",
		Items: []program.WorkItem{{
			ID: "w1", Kind: program.ItemKindChange, Title: "dispatched",
			Priority: program.PriorityP1, Status: program.ItemDispatched, Repo: repo,
			ProjectSlug: "mail-child", Notes: []string{}, CreatedAt: at, UpdatedAt: at,
			DispatchedAt: at, Dependencies: []string{}, ContractRefs: []string{},
		}},
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	childDir := filepath.Join(project.ActiveDir(), "mail-child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(repo, ".worktrees", "mail-child")
	manifest := project.Manifest{
		Slug: "mail-child", Title: "Mail child", Repo: repo, Branch: "feature",
		BaseBranch: "main", Worktree: &worktree, Status: "active",
		Program: p.Slug, ProgramItem: "w1",
		Workflow: "deliver-pr", Phase: "implement", Created: at, Updated: at,
		PhasesCompleted: []string{}, PhasesRemaining: []string{},
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest); err != nil {
		t.Fatal(err)
	}
	return p, childDir
}

func TestBuildReportsChildWorktreePresenceAndItsWatcher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, _ := newMailboxSnapshotProgram(t)
	item := findSnapshotItem(t, mustBuild(t, p.Slug).Items, "w1")
	if item.Child == nil {
		t.Fatal("the linked child is missing from the snapshot")
	}
	if item.Child.Manifest.WorktreePresent {
		t.Fatal("a worktree that was never created is reported as present")
	}
	if item.Child.Watcher != nil {
		t.Fatalf("a project with no watcher reported one: %+v", item.Child.Watcher)
	}

	if err := os.MkdirAll(item.Child.Manifest.Worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := prwatch.UpdateState("mail-child", func(state prwatch.State) (prwatch.State, error) {
		state.Project = "mail-child"
		state.Status = prwatch.StatusRunning
		state.TabID = "w1:t5"
		state.PaneID = "w1:p5"
		return state, nil
	}); err != nil {
		t.Fatalf("seed watcher state: %v", err)
	}
	before, err := os.ReadFile(prwatch.StatePath("mail-child"))
	if err != nil {
		t.Fatal(err)
	}

	item = findSnapshotItem(t, mustBuild(t, p.Slug).Items, "w1")
	if !item.Child.Manifest.WorktreePresent {
		t.Fatal("an existing worktree is reported as absent")
	}
	if item.Child.Watcher == nil {
		t.Fatal("a recorded watcher is missing from the snapshot")
	}
	if item.Child.Watcher.TabID != "w1:t5" || item.Child.Watcher.PaneID != "w1:p5" {
		t.Fatalf("watcher ids = %+v", item.Child.Watcher)
	}
	if item.Child.Watcher.Running {
		t.Fatal("a watcher with no live process is reported as running")
	}
	after, err := os.ReadFile(prwatch.StatePath("mail-child"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("the snapshot rewrote watcher runtime state:\nbefore %s\nafter  %s", before, after)
	}
}

func mustBuild(t *testing.T, slug string) Snapshot {
	t.Helper()
	snapshot, err := Build(slug, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// A watcher that finished on its own keeps its tab so its final lines stay
// readable, and archiving the child project does not close that tab. The
// snapshot has to keep reporting the completed watcher and its exact ids, or
// the one signal that says "this tab is still open" disappears at exactly the
// point cleanup is half done.
func TestBuildKeepsACompletedWatcherForAnArchivedChild(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, childDir := newMailboxSnapshotProgram(t)
	if _, err := prwatch.UpdateState("mail-child", func(state prwatch.State) (prwatch.State, error) {
		state.Project = "mail-child"
		state.Status = prwatch.StatusComplete
		state.StopReason = "pull request merged"
		state.TabID = "w1:t5"
		state.PaneID = "w1:p5"
		return state, nil
	}); err != nil {
		t.Fatalf("seed watcher state: %v", err)
	}
	archived := filepath.Join(project.ArchivedDir(), "mail-child")
	if err := os.MkdirAll(filepath.Dir(archived), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(childDir, archived); err != nil {
		t.Fatal(err)
	}

	item := findSnapshotItem(t, mustBuild(t, p.Slug).Items, "w1")
	if item.Child == nil {
		t.Fatal("an archived child is missing from the snapshot")
	}
	if !item.Child.Manifest.Archived {
		t.Fatal("an archived child is reported as active")
	}
	watcher := item.Child.Watcher
	if watcher == nil {
		t.Fatal("the completed watcher disappeared once the child was archived")
	}
	if watcher.Running {
		t.Fatal("a watcher with no live process is reported as running")
	}
	if watcher.Status != string(prwatch.StatusComplete) {
		t.Errorf("watcher status = %q, want the completed lifecycle", watcher.Status)
	}
	if watcher.TabID != "w1:t5" || watcher.PaneID != "w1:p5" {
		t.Errorf("watcher ids = %+v, want the recorded tab and pane", watcher)
	}
}
