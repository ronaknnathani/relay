package program

import (
	"reflect"
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

func TestPlanCapacityCountsOnlyRecordedOpenPRs(t *testing.T) {
	p := newTestProgram(t)
	activateTestProgram(t, &p)
	ready := addTestItem(t, &p, "ready", PriorityP0)
	inFlight := dispatchedTestItem(t, &p, "in flight", "child")

	views := []ProjectView{
		{Slug: "child", Repo: p.Repo},
		{Slug: "draft-pr", Repo: p.Repo, HasPR: true, PRRef: "draft-pr"},
		{Slug: "second-pr", Repo: p.Repo, HasPR: true, PRRef: "second-pr"},
		{Slug: "merged-pr", Repo: p.Repo, HasPR: true, PRRef: "merged-pr", Merged: true},
		{Slug: "other-repo", Repo: "other/repo", HasPR: true, PRRef: "other"},
	}
	plan := p.Plan(views)
	if plan.Capacity.Limit != 2 || plan.Capacity.Open != 2 || plan.Capacity.Available != 0 {
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
