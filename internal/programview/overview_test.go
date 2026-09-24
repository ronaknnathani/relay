package programview

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/program"
)

func TestBuildOverviewDerivesProgramsAndAttentionWork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	at := "2026-09-24T16:00:00Z"
	alpha := overviewTestProgram("alpha", "Alpha prompt. More detail.", repo, at)
	alpha.Items = []program.WorkItem{
		overviewTestItem("w1", "ready", program.PriorityP0, program.ItemPending, repo, at),
		overviewTestItem("w10", "dispatch ten", program.PriorityP0, program.ItemDispatched, repo, at),
		overviewTestItem("w2", "dispatch two", program.PriorityP0, program.ItemDispatched, repo, at),
		overviewTestItem("w12", "blocked", program.PriorityP2, program.ItemBlocked, repo, at),
	}
	alpha.Items[1].ProjectSlug = "alpha-ten"
	alpha.Items[1].DispatchedAt = at
	alpha.Items[2].ProjectSlug = "alpha-two"
	alpha.Items[2].DispatchedAt = at
	alpha.Items[3].BlockedReason = "owner needed"
	alpha.Decisions = []program.Decision{{
		ID: "d1", Kind: program.DecisionQuestion, RaisedBy: program.RaisedByTL,
		Question: "Ship?", Options: []string{"yes"}, CreatedAt: at,
	}}
	beta := overviewTestProgram("beta", "Beta", repo, at)
	beta.Items = []program.WorkItem{
		overviewTestItem("w2", "review", program.PriorityP0, program.ItemInReview, repo, at),
		overviewTestItem("w3", "dispatch first", program.PriorityP0, program.ItemDispatched, repo, at),
		overviewTestItem("w4", "done", program.PriorityP0, program.ItemMerged, repo, at),
		overviewTestItem("w5", "cancelled", program.PriorityP0, program.ItemCancelled, repo, at),
	}
	beta.Items[0].ProjectSlug = "beta-review"
	beta.Items[0].PRRef = "#2"
	beta.Items[0].DispatchedAt = at
	beta.Items[0].InReviewAt = at
	beta.Items[1].ProjectSlug = "beta-dispatch"
	beta.Items[1].DispatchedAt = at
	beta.Items[2].ProjectSlug = "beta-done"
	beta.Items[2].PRRef = "#4"
	beta.Items[2].DispatchedAt = at
	beta.Items[2].InReviewAt = at
	beta.Items[2].MergedAt = at
	beta.Items[3].CancelledAt = at

	for _, p := range []program.Program{alpha, beta} {
		if err := program.Create(p); err != nil {
			t.Fatalf("Create(%s): %v", p.Slug, err)
		}
	}
	writeTestFile(t, filepath.Join(program.ProgramDir(program.ActiveDir(), "alpha"), "goal.md"),
		"# Alpha display\n\nAlpha summary for the overview.\n")

	now := time.Date(2026, 9, 24, 16, 5, 0, 0, time.UTC)
	got := BuildOverview(
		[]program.Program{beta, alpha},
		[]program.DiscoveryDiagnostic{{Directory: "broken", Err: os.ErrPermission}},
		func() time.Time { return now },
	)

	if got.Schema != OverviewSchemaVersion || got.GeneratedAt != now.Format(time.RFC3339) {
		t.Fatalf("overview identity = (%q, %q)", got.Schema, got.GeneratedAt)
	}
	if got.Refresh != (RefreshDTO{Status: "fresh"}) {
		t.Fatalf("refresh = %+v", got.Refresh)
	}
	if len(got.Programs) != 2 || got.Programs[0].Slug != "alpha" || got.Programs[1].Slug != "beta" {
		t.Fatalf("programs = %+v", got.Programs)
	}
	if got.Programs[0].DisplayTitle != "Alpha display" ||
		got.Programs[0].Summary != "Alpha summary for the overview." {
		t.Fatalf("alpha identity = %+v", got.Programs[0])
	}
	if got.Programs[0].Ready != 0 || got.Programs[0].InFlight != 2 ||
		got.Programs[0].Blocked != 2 || got.Programs[0].OpenDecisions != 1 ||
		got.Programs[0].NextAction != "resolve d1" {
		t.Fatalf("alpha planning = %+v", got.Programs[0])
	}
	if got.Programs[1].Progress != (ProgressDTO{
		Total: 4, Dispatched: 1, InReview: 1, Merged: 1, Canceled: 1, Completed: 2, Percent: 25,
	}) {
		t.Fatalf("beta progress = %+v", got.Programs[1].Progress)
	}

	wantWork := []OverviewWorkItemDTO{
		{ProgramSlug: "alpha", ProgramTitle: "Alpha display", ID: "w2", Title: "dispatch two", Priority: "P0", Status: "dispatched"},
		{ProgramSlug: "alpha", ProgramTitle: "Alpha display", ID: "w10", Title: "dispatch ten", Priority: "P0", Status: "dispatched"},
		{ProgramSlug: "beta", ProgramTitle: "Beta", ID: "w3", Title: "dispatch first", Priority: "P0", Status: "dispatched"},
		{ProgramSlug: "beta", ProgramTitle: "Beta", ID: "w2", Title: "review", Priority: "P0", Status: "in-review"},
		{ProgramSlug: "alpha", ProgramTitle: "Alpha display", ID: "w12", Title: "blocked", Priority: "P2", Status: "blocked", Reasons: []string{"owner needed"}},
	}
	if !reflect.DeepEqual(got.Work, wantWork) {
		t.Fatalf("work = %#v, want %#v", got.Work, wantWork)
	}
	if len(got.Diagnostics) != 1 || got.Diagnostics[0].Directory != "broken" ||
		!strings.Contains(got.Diagnostics[0].Message, "permission denied") {
		t.Fatalf("diagnostics = %+v", got.Diagnostics)
	}
}

func TestBuildOverviewEmpty(t *testing.T) {
	got := BuildOverview(nil, nil, time.Now)
	if got.Schema != OverviewSchemaVersion || got.Programs == nil || got.Work == nil || got.Diagnostics == nil {
		t.Fatalf("empty overview = %+v", got)
	}
}

func TestBuildOverviewIsolatesProgramGoalFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	at := "2026-09-24T16:00:00Z"
	healthyRepo := filepath.Join(home, "healthy-repo")
	brokenRepo := filepath.Join(home, "broken-repo")
	for _, repo := range []string{healthyRepo, brokenRepo} {
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	healthy := overviewTestProgram("healthy", "Healthy", healthyRepo, at)
	broken := overviewTestProgram("broken", "Broken", brokenRepo, at)
	for _, current := range []program.Program{healthy, broken} {
		if err := program.Create(current); err != nil {
			t.Fatalf("Create(%s): %v", current.Slug, err)
		}
	}
	writeTestFile(
		t,
		filepath.Join(program.ProgramDir(program.ActiveDir(), healthy.Slug), "goal.md"),
		"# Healthy display\n\nHealthy summary.\n",
	)
	if err := os.Mkdir(
		filepath.Join(program.ProgramDir(program.ActiveDir(), broken.Slug), "goal.md"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}

	got := BuildOverview(
		[]program.Program{healthy, broken},
		nil,
		func() time.Time { return time.Date(2026, 9, 24, 16, 5, 0, 0, time.UTC) },
	)

	if len(got.Programs) != 2 ||
		got.Programs[0].Slug != "broken" ||
		got.Programs[1].Slug != "healthy" {
		t.Fatalf("programs = %+v", got.Programs)
	}
	if got.Programs[0].DisplayTitle != "Broken" || got.Programs[0].Summary != "" {
		t.Fatalf("broken fallback identity = %+v", got.Programs[0])
	}
	if got.Programs[1].DisplayTitle != "Healthy display" ||
		got.Programs[1].Summary != "Healthy summary." {
		t.Fatalf("healthy identity = %+v", got.Programs[1])
	}
	if len(got.Diagnostics) != 1 ||
		got.Diagnostics[0].Directory != "broken" ||
		!strings.Contains(got.Diagnostics[0].Message, "read program goal") {
		t.Fatalf("diagnostics = %+v", got.Diagnostics)
	}
}

func overviewTestProgram(slug, title, repo, at string) program.Program {
	return program.Program{
		Revision: 1, Slug: slug, Title: title, Repo: repo, State: program.StateActive,
		Agent: "copilot", MaxOpenPRs: 2, CreatedAt: at, UpdatedAt: at,
		ApprovalRequestedAt: at, ApprovedAt: at, ApprovedBy: "ceo",
		Items: []program.WorkItem{}, Contracts: []program.Contract{}, Decisions: []program.Decision{},
	}
}

func overviewTestItem(
	id, title string,
	priority program.Priority,
	status program.ItemStatus,
	repo, at string,
) program.WorkItem {
	return program.WorkItem{
		ID: id, Kind: program.ItemKindChange, Title: title, Priority: priority,
		Status: status, Repo: repo, CreatedAt: at, UpdatedAt: at,
		Dependencies: []string{}, ContractRefs: []string{}, Notes: []string{},
	}
}
