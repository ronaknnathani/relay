package programview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

func artifactText(artifact ArtifactDTO) string {
	if artifact.Text == nil {
		return ""
	}
	return *artifact.Text
}

func TestBuildPopulatesProgramDetailAndDegradesPerSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

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
		Canceled: 1, Completed: 2, Percent: 33,
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

type fetcherFunc func(context.Context, string, string) (PullRequestDTO, error)

func (f fetcherFunc) Fetch(ctx context.Context, repo, ref string) (PullRequestDTO, error) {
	return f(ctx, repo, ref)
}

type agentListerFunc func() ([]herdr.Agent, error)

func (f agentListerFunc) Agents() ([]herdr.Agent, error) {
	return f()
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
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
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
		Agents: agentListerFunc(func() ([]herdr.Agent, error) {
			return nil, errors.New("herdr unavailable")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	item := findSnapshotItem(t, got.Items, "w1")
	if item.RecordedPR == nil || item.RecordedPR.Number != 7 ||
		item.LivePR == nil || !item.LivePR.Stale || item.LivePR.FetchedAt != at {
		t.Fatalf("PR provenance = recorded %+v live %+v", item.RecordedPR, item.LivePR)
	}
	if artifactText(item.Artifacts[0]) != "12345" || !item.Artifacts[0].Truncated {
		t.Fatalf("truncated artifact = %+v", item.Artifacts[0])
	}
	if got.SourceHealth.GitHub.Status != "degraded" ||
		got.SourceHealth.Herdr.Status != "degraded" ||
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
}

func TestBuildSkipsMalformedSiblingProjectStateWithoutFabricatingOrphansOrCapacity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
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
	for _, slug := range []string{"healthy-child", "broken-child"} {
		childDir := filepath.Join(project.ActiveDir(), slug)
		if err := os.MkdirAll(childDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := project.Save(project.ManifestPath(project.ActiveDir(), slug), project.Manifest{
			Slug: slug, Title: slug, Repo: repo, Branch: slug, BaseBranch: "main",
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
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	number := 42
	saveProjectManifest(t, project.ActiveDir(), project.Manifest{
		Slug: "child", Repo: repo, Branch: "feature", PR: project.PRInfo{Number: &number},
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
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
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
		Workflow: "deliver-pr", Phase: "implement", Created: at, Updated: at,
		PhasesCompleted: []string{}, PhasesRemaining: []string{},
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest); err != nil {
		t.Fatal(err)
	}
	return p, childDir
}
