package program

import (
	"reflect"
	"strings"
	"testing"
)

func dispatchedTestItem(t *testing.T, p *Program, title, projectSlug string) WorkItem {
	t.Helper()
	item := addTestItem(t, p, title, PriorityP0)
	if err := p.LinkItem(item.ID, projectSlug); err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(item.ID); err != nil {
		t.Fatal(err)
	}
	return item
}

func recordItemPR(t *testing.T, p *Program, itemID, ref string) {
	t.Helper()
	for i := range p.Items {
		if p.Items[i].ID == itemID {
			p.Items[i].PRRef = ref
			if err := p.Validate(); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("item %q not found", itemID)
}

func TestReconcilePRMergedOrphanAndIdempotency(t *testing.T) {
	p := newTestProgram(t)
	activateTestProgram(t, &p)
	withPR := dispatchedTestItem(t, &p, "review", "review-project")
	merged := dispatchedTestItem(t, &p, "merged", "merged-project")
	orphan := dispatchedTestItem(t, &p, "orphan", "orphan-project")

	views := []ProjectView{
		{
			Slug:   "review-project",
			Repo:   p.Repo,
			HasPR:  true,
			PRRef:  "https://github.example/pull/1",
			Merged: false,
		},
		{
			Slug:   "merged-project",
			Repo:   p.Repo,
			HasPR:  true,
			PRRef:  "https://github.example/pull/2",
			Merged: true,
		},
		{
			Slug:     "orphan-project",
			Repo:     p.Repo,
			Orphaned: true,
		},
	}

	result, err := p.Reconcile(views)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !result.Changed || !reflect.DeepEqual(result.OrphanIDs, []string{orphan.ID}) {
		t.Fatalf("result = %+v", result)
	}
	gotWithPR, _ := p.Item(withPR.ID)
	if gotWithPR.Status != ItemInReview || gotWithPR.PRRef != views[0].PRRef {
		t.Fatalf("PR item = %+v", gotWithPR)
	}
	gotMerged, _ := p.Item(merged.ID)
	if gotMerged.Status != ItemMerged || gotMerged.PRRef != views[1].PRRef {
		t.Fatalf("merged item = %+v", gotMerged)
	}
	gotOrphan, _ := p.Item(orphan.ID)
	if gotOrphan.Status != ItemDispatched {
		t.Fatalf("orphan status = %s", gotOrphan.Status)
	}

	second, err := p.Reconcile(views)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if second.Changed || !reflect.DeepEqual(second.OrphanIDs, []string{orphan.ID}) {
		t.Fatalf("second result = %+v", second)
	}
}

func TestReconcileIgnoresLinkedBlockedOrphanWithoutMutation(t *testing.T) {
	p := newTestProgram(t)
	activateTestProgram(t, &p)
	item := dispatchedTestItem(t, &p, "blocked orphan", "blocked-project")
	if err := p.BlockItem(item.ID, "manual hold"); err != nil {
		t.Fatal(err)
	}
	result, err := p.Reconcile(nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || len(result.OrphanIDs) != 0 {
		t.Fatalf("result = %+v", result)
	}
	got, _ := p.Item(item.ID)
	if got.Status != ItemBlocked || got.BlockedReason != "manual hold" {
		t.Fatalf("blocked orphan mutated: %+v", got)
	}
}

func TestReconcileRecordsPRWithoutUnblockingItem(t *testing.T) {
	p := newTestProgram(t)
	activateTestProgram(t, &p)
	item := dispatchedTestItem(t, &p, "blocked review", "blocked-project")
	if err := p.BlockItem(item.ID, "manual hold"); err != nil {
		t.Fatal(err)
	}
	result, err := p.Reconcile([]ProjectView{{
		Slug:  "blocked-project",
		Repo:  p.Repo,
		HasPR: true,
		PRRef: "https://github.example/pull/3",
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := p.Item(item.ID)
	if !result.Changed || got.Status != ItemBlocked || got.PRRef == "" {
		t.Fatalf("result = %+v, item = %+v", result, got)
	}
}

func TestPlanCapacityCountsOnlyLinkedRecordedOpenPRs(t *testing.T) {
	p := newTestProgram(t)
	activateTestProgram(t, &p)
	ready := addTestItem(t, &p, "ready", PriorityP0)
	inFlight := dispatchedTestItem(t, &p, "in flight", "child")

	views := []ProjectView{
		{Slug: "child", Repo: p.Repo, HasPR: true, PRRef: "#1"},
		{Slug: "standalone", Repo: p.Repo, HasPR: true, PRRef: "#2"},
		{Slug: "other-program", Repo: p.Repo, HasPR: true, PRRef: "#3"},
		{Slug: "other-repo", Repo: "other/repo", HasPR: true, PRRef: "other"},
	}
	plan := p.Plan(views)
	if plan.Capacity != (Capacity{Limit: 2, Open: 1, Available: 1}) {
		t.Fatalf("capacity = %+v", plan.Capacity)
	}
	if len(plan.Ready) != 1 || plan.Ready[0].ID != ready.ID {
		t.Fatalf("ready = %+v", plan.Ready)
	}
	if len(plan.InFlight) != 1 || plan.InFlight[0].ID != inFlight.ID {
		t.Fatalf("in flight = %+v", plan.InFlight)
	}
	if plan.NextAction != "dispatch "+ready.ID {
		t.Fatalf("next action = %q", plan.NextAction)
	}
}

func TestPlanCapacityIsEmptyWithoutProgramItems(t *testing.T) {
	p := newTestProgram(t)
	activateTestProgram(t, &p)

	plan := p.Plan([]ProjectView{
		{Slug: "standalone", Repo: p.Repo, HasPR: true, PRRef: "#1"},
		{Slug: "other-program", Repo: p.Repo, HasPR: true, PRRef: "#2"},
	})
	if plan.Capacity != (Capacity{Limit: 2, Available: 2}) {
		t.Fatalf("capacity = %+v", plan.Capacity)
	}
}

func TestGrantOpenPRSerializesReservedCapacity(t *testing.T) {
	p := newTestProgram(t)
	p.MaxOpenPRs = 3
	activateTestProgram(t, &p)
	dispatchedTestItem(t, &p, "open one", "open-one")
	dispatchedTestItem(t, &p, "open two", "open-two")
	first := dispatchedTestItem(t, &p, "first", "first-child")
	second := dispatchedTestItem(t, &p, "second", "second-child")
	projects := []ProjectView{
		{Slug: "open-one", Repo: p.Repo, HasPR: true, PRRef: "#1"},
		{Slug: "open-two", Repo: p.Repo, HasPR: true, PRRef: "#2"},
	}

	if err := p.GrantOpenPR(first.ID, "tl", projects); err != nil {
		t.Fatalf("GrantOpenPR first: %v", err)
	}
	granted, _ := p.Item(first.ID)
	if granted.PRGrantedAt == "" || granted.PRGrantedBy != "tl" {
		t.Fatalf("first grant = %+v", granted)
	}
	if capacity := p.Plan(projects).Capacity; capacity != (Capacity{
		Limit: 3, Open: 2, Reserved: 1, Available: 0,
	}) {
		t.Fatalf("capacity after first grant = %+v", capacity)
	}
	if err := p.GrantOpenPR(second.ID, "tl", projects); err == nil {
		t.Fatal("second grant succeeded without capacity")
	}

	if err := p.RevokeOpenPR(first.ID, "tl", "worker paused"); err != nil {
		t.Fatalf("RevokeOpenPR first: %v", err)
	}
	if err := p.GrantOpenPR(second.ID, "tl", projects); err != nil {
		t.Fatalf("GrantOpenPR second after revoke: %v", err)
	}
}

func TestReconcileConvertsOpenPRGrantWithoutDoubleCounting(t *testing.T) {
	p := newTestProgram(t)
	p.MaxOpenPRs = 3
	activateTestProgram(t, &p)
	dispatchedTestItem(t, &p, "open one", "open-one")
	dispatchedTestItem(t, &p, "open two", "open-two")
	item := dispatchedTestItem(t, &p, "review", "review-child")
	openProjects := []ProjectView{
		{Slug: "open-one", Repo: p.Repo, HasPR: true, PRRef: "#1"},
		{Slug: "open-two", Repo: p.Repo, HasPR: true, PRRef: "#2"},
	}
	if err := p.GrantOpenPR(item.ID, "tl", openProjects); err != nil {
		t.Fatal(err)
	}
	projects := append(append([]ProjectView(nil), openProjects...), ProjectView{
		Slug: "review-child", Repo: p.Repo, HasPR: true, PRRef: "#42",
	})
	if capacity := p.Plan(projects).Capacity; capacity != (Capacity{
		Limit: 3, Open: 3, Reserved: 0, Available: 0,
	}) {
		t.Fatalf("capacity before reconcile = %+v", capacity)
	}

	result, err := p.Reconcile(projects)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, _ := p.Item(item.ID)
	if !result.Changed || got.Status != ItemInReview || got.PRRef != "#42" ||
		got.PRGrantedAt != "" || got.PRGrantedBy != "" {
		t.Fatalf("result = %+v, item = %+v", result, got)
	}
	if capacity := p.Plan(projects).Capacity; capacity != (Capacity{
		Limit: 3, Open: 3, Reserved: 0, Available: 0,
	}) {
		t.Fatalf("capacity after reconcile = %+v", capacity)
	}
}

func TestReconcileClearsClosedPRAndReturnsItemToDispatched(t *testing.T) {
	p := newTestProgram(t)
	activateTestProgram(t, &p)
	item := dispatchedTestItem(t, &p, "closed", "closed-project")
	open := []ProjectView{{Slug: "closed-project", Repo: p.Repo, HasPR: true, PRRef: "#202"}}
	if _, err := p.Reconcile(open); err != nil {
		t.Fatal(err)
	}
	inReview, _ := p.Item(item.ID)
	if inReview.Status != ItemInReview || inReview.PRRef != "#202" {
		t.Fatalf("in-review item = %+v", inReview)
	}

	closed := []ProjectView{{Slug: "closed-project", Repo: p.Repo, PRRef: "#202", PRClosed: true}}
	result, err := p.Reconcile(closed)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || len(result.OrphanIDs) != 0 {
		t.Fatalf("result = %+v", result)
	}
	got, _ := p.Item(item.ID)
	if got.Status != ItemDispatched || got.PRRef != "" || got.InReviewAt != "" {
		t.Fatalf("recovered item = %+v", got)
	}
	if len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "#202") {
		t.Fatalf("recovery notes = %#v", got.Notes)
	}

	second, err := p.Reconcile(closed)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed {
		t.Fatal("closed pull request reconciliation is not idempotent")
	}
	repeated, _ := p.Item(item.ID)
	if len(repeated.Notes) != 1 {
		t.Fatalf("repeated notes = %#v", repeated.Notes)
	}
}

func TestReconcileKeepsMergedWorkWhenPullRequestWasClosed(t *testing.T) {
	p := newTestProgram(t)
	activateTestProgram(t, &p)
	item := dispatchedTestItem(t, &p, "squashed elsewhere", "squashed-project")

	result, err := p.Reconcile([]ProjectView{{
		Slug: "squashed-project", Repo: p.Repo, PRRef: "#7", Merged: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := p.Item(item.ID)
	if !result.Changed || got.Status != ItemMerged || got.PRRef != "#7" {
		t.Fatalf("merged item = %+v", got)
	}
}

func TestReconcileSkipsUnavailableProjectsWithoutOrphaningOrMerging(t *testing.T) {
	p := newTestProgram(t)
	activateTestProgram(t, &p)
	item := dispatchedTestItem(t, &p, "unreadable", "unreadable-project")

	result, err := p.Reconcile([]ProjectView{{
		Slug: "unreadable-project", Repo: p.Repo, HasPR: true, PRRef: "#5", Unavailable: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || len(result.OrphanIDs) != 0 {
		t.Fatalf("unavailable result = %+v", result)
	}
	got, _ := p.Item(item.ID)
	if got.Status != ItemDispatched || got.PRRef != "" {
		t.Fatalf("unavailable item = %+v", got)
	}
}

func TestPlanCapacityCountsUnavailableRecordedPRsAsOpen(t *testing.T) {
	p := newTestProgram(t)
	p.MaxOpenPRs = 2
	activateTestProgram(t, &p)
	dispatchedTestItem(t, &p, "unreadable", "unreadable-project")

	capacity := p.Plan([]ProjectView{{
		Slug: "unreadable-project", Repo: p.Repo, HasPR: true, PRRef: "#5", Unavailable: true,
	}}).Capacity
	if capacity.Open != 1 || capacity.Available != 1 {
		t.Fatalf("capacity = %+v", capacity)
	}
}

func TestPlanCapacityFreesClosedPullRequestAndCountsReplacementGrant(t *testing.T) {
	p := newTestProgram(t)
	p.MaxOpenPRs = 1
	activateTestProgram(t, &p)
	item := dispatchedTestItem(t, &p, "closed", "closed-project")
	views := []ProjectView{{Slug: "closed-project", Repo: p.Repo, PRRef: "#202", PRClosed: true}}
	if _, err := p.Reconcile(views); err != nil {
		t.Fatal(err)
	}
	if capacity := p.Plan(views).Capacity; capacity.Open != 0 || capacity.Available != 1 {
		t.Fatalf("closed capacity = %+v", capacity)
	}

	if err := p.GrantOpenPR(item.ID, "tl", views); err != nil {
		t.Fatalf("grant after close: %v", err)
	}
	if capacity := p.Plan(views).Capacity; capacity.Reserved != 1 || capacity.Available != 0 {
		t.Fatalf("reserved capacity = %+v", capacity)
	}
}

func TestGrantOpenPRClearsKnownClosedPullRequestReference(t *testing.T) {
	p := newTestProgram(t)
	p.MaxOpenPRs = 1
	activateTestProgram(t, &p)
	item := dispatchedTestItem(t, &p, "closed", "closed-project")
	recordItemPR(t, &p, item.ID, "#202")
	closed := []ProjectView{{Slug: "closed-project", Repo: p.Repo, PRRef: "#202", PRClosed: true}}

	if err := p.GrantOpenPR(item.ID, "tl", closed); err != nil {
		t.Fatalf("grant with closed recorded PR: %v", err)
	}
	got, _ := p.Item(item.ID)
	if got.PRRef != "" || got.PRGrantedBy != "tl" {
		t.Fatalf("granted item = %+v", got)
	}
}

func TestGrantOpenPRStillRejectsOpenRecordedPullRequest(t *testing.T) {
	p := newTestProgram(t)
	p.MaxOpenPRs = 2
	activateTestProgram(t, &p)
	item := dispatchedTestItem(t, &p, "open", "open-project")
	recordItemPR(t, &p, item.ID, "#9")
	open := []ProjectView{{Slug: "open-project", Repo: p.Repo, HasPR: true, PRRef: "#9"}}

	err := p.GrantOpenPR(item.ID, "tl", open)
	if err == nil || !strings.Contains(err.Error(), "already recorded") {
		t.Fatalf("grant with open recorded PR error = %v", err)
	}
}
