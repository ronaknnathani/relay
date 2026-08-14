package program

import (
	"reflect"
	"strings"
	"testing"
)

func newTestProgram(t *testing.T) Program {
	t.Helper()
	p, err := New("relay-v1", "Relay V1", "github.com/ronaknnathani/relay", "copilot", 2)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func addTestItem(t *testing.T, p *Program, title string, priority Priority, deps ...string) WorkItem {
	t.Helper()
	item, err := p.AddItem(WorkItem{
		Title:        title,
		Priority:     priority,
		Dependencies: deps,
	})
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	return item
}

func activateTestProgram(t *testing.T, p *Program) {
	t.Helper()
	if err := p.Transition(StatePendingApproval, ""); err != nil {
		t.Fatalf("request approval: %v", err)
	}
	if err := p.Transition(StateActive, "ceo"); err != nil {
		t.Fatalf("activate: %v", err)
	}
}

func TestProgramTransitions(t *testing.T) {
	p := newTestProgram(t)
	if err := p.Transition(StateActive, "ceo"); err == nil {
		t.Fatal("draft -> active succeeded")
	}
	if err := p.Transition(StatePendingApproval, ""); err != nil {
		t.Fatalf("draft -> pending approval: %v", err)
	}
	if err := p.Transition(StateActive, ""); err == nil {
		t.Fatal("approval without approver succeeded")
	}
	if err := p.Transition(StateActive, "ceo"); err != nil {
		t.Fatalf("pending approval -> active: %v", err)
	}
	if p.ApprovedBy != "ceo" || p.ApprovedAt == "" {
		t.Fatalf("approval fields = %q, %q", p.ApprovedBy, p.ApprovedAt)
	}
	if err := p.Transition(StateHeld, "cto"); err != nil {
		t.Fatalf("active -> held: %v", err)
	}
	if err := p.Transition(StateActive, "cto"); err != nil {
		t.Fatalf("held -> active: %v", err)
	}

	item := addTestItem(t, &p, "unfinished", PriorityP0)
	if err := p.Transition(StateCompleted, "cto"); err == nil {
		t.Fatal("completed program with unfinished item")
	}
	if err := p.CancelItem(item.ID, "no longer needed"); err != nil {
		t.Fatalf("CancelItem: %v", err)
	}
	if err := p.Transition(StateCompleted, "cto"); err != nil {
		t.Fatalf("active -> completed: %v", err)
	}
	if err := p.Transition(StateHeld, "cto"); err == nil {
		t.Fatal("transition from terminal state succeeded")
	}
}

func TestAbandonAnyNonterminalState(t *testing.T) {
	for _, state := range []State{StateDraft, StatePendingApproval, StateActive, StateHeld} {
		t.Run(string(state), func(t *testing.T) {
			p := newTestProgram(t)
			switch state {
			case StatePendingApproval:
				if err := p.Transition(StatePendingApproval, ""); err != nil {
					t.Fatal(err)
				}
			case StateActive:
				activateTestProgram(t, &p)
			case StateHeld:
				activateTestProgram(t, &p)
				if err := p.Transition(StateHeld, "cto"); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.Transition(StateAbandoned, "cto"); err != nil {
				t.Fatalf("%s -> abandoned: %v", state, err)
			}
		})
	}
}

func TestIDsUseMaxPlusOne(t *testing.T) {
	p := newTestProgram(t)
	first := addTestItem(t, &p, "first", PriorityP1)
	second := addTestItem(t, &p, "second", PriorityP1)
	p.Items[1].ID = "w4"
	third := addTestItem(t, &p, "third", PriorityP1)
	if first.ID != "w1" || second.ID != "w2" || third.ID != "w5" {
		t.Fatalf("item IDs = %s, %s, %s", first.ID, second.ID, third.ID)
	}

	d1, err := p.OpenDecision(Decision{
		Kind:     DecisionQuestion,
		RaisedBy: RaisedByCTO,
		Question: "one?",
	})
	if err != nil {
		t.Fatal(err)
	}
	d2, err := p.OpenDecision(Decision{
		Kind:     DecisionQuestion,
		RaisedBy: RaisedByCTO,
		Question: "two?",
	})
	if err != nil {
		t.Fatal(err)
	}
	p.Decisions[1].ID = "d4"
	d3, err := p.OpenDecision(Decision{
		Kind:     DecisionQuestion,
		RaisedBy: RaisedByCTO,
		Question: "three?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d1.ID != "d1" || d2.ID != "d2" || d3.ID != "d5" {
		t.Fatalf("decision IDs = %s, %s, %s", d1.ID, d2.ID, d3.ID)
	}
}

func TestValidationAggregatesAndDetectsCycles(t *testing.T) {
	p := newTestProgram(t)
	p.Title = ""
	p.Agent = ""
	p.Items = []WorkItem{
		{
			ID:           "w1",
			Kind:         ItemKindChange,
			Title:        "one",
			Priority:     PriorityP0,
			Status:       ItemPending,
			Dependencies: []string{"w2"},
			Repo:         p.Repo,
			CreatedAt:    p.CreatedAt,
			UpdatedAt:    p.UpdatedAt,
		},
		{
			ID:           "w2",
			Kind:         ItemKindChange,
			Title:        "two",
			Priority:     PriorityP1,
			Status:       ItemPending,
			Dependencies: []string{"w1", "w9"},
			ContractRefs: []string{"missing@v1"},
			Repo:         "other/repo",
			CreatedAt:    p.CreatedAt,
			UpdatedAt:    p.UpdatedAt,
		},
	}
	err := p.Validate()
	if err == nil {
		t.Fatal("Validate succeeded")
	}
	for _, want := range []string{
		"title is required",
		"agent is required",
		"dependency cycle",
		"dependency \"w9\" does not exist",
		"repo",
		"contract \"missing@v1\" does not resolve",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate error %q missing %q", err, want)
		}
	}
}

func TestValidationRequiresLifecycleTimestamps(t *testing.T) {
	p := newTestProgram(t)
	p.State = StatePendingApproval
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "approval_requested_at") {
		t.Fatalf("pending approval validation error = %v", err)
	}

	p = newTestProgram(t)
	item := addTestItem(t, &p, "change", PriorityP0)
	p.Items[0].ProjectSlug = "child"
	p.Items[0].Status = ItemDispatched
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "dispatched_at") {
		t.Fatalf("dispatched item validation error for %s = %v", item.ID, err)
	}
}

func TestItemMutations(t *testing.T) {
	p := newTestProgram(t)
	item := addTestItem(t, &p, "change", PriorityP0)
	linkedPending := addTestItem(t, &p, "linked pending", PriorityP1)
	activateTestProgram(t, &p)

	if err := p.LinkItem(linkedPending.ID, "pending-project"); err != nil {
		t.Fatalf("LinkItem pending: %v", err)
	}
	if err := p.BlockItem(linkedPending.ID, "waiting"); err != nil {
		t.Fatalf("BlockItem pending: %v", err)
	}
	if err := p.UnblockItem(linkedPending.ID); err != nil {
		t.Fatalf("UnblockItem pending: %v", err)
	}
	gotPending, _ := p.Item(linkedPending.ID)
	if gotPending.Status != ItemPending {
		t.Fatalf("linked pending unblocked status = %s, want %s", gotPending.Status, ItemPending)
	}

	if err := p.LinkItem(item.ID, "child-project"); err != nil {
		t.Fatalf("LinkItem: %v", err)
	}
	if err := p.DispatchItem(item.ID); err != nil {
		t.Fatalf("DispatchItem: %v", err)
	}
	if err := p.BlockItem(item.ID, "waiting for CI"); err != nil {
		t.Fatalf("BlockItem: %v", err)
	}
	if err := p.UnblockItem(item.ID); err != nil {
		t.Fatalf("UnblockItem: %v", err)
	}
	got, _ := p.Item(item.ID)
	if got.Status != ItemDispatched {
		t.Fatalf("unblocked status = %s, want %s", got.Status, ItemDispatched)
	}
	if err := p.CancelItem(item.ID, "superseded"); err != nil {
		t.Fatalf("CancelItem: %v", err)
	}
	if err := p.DispatchItem(item.ID); err == nil {
		t.Fatal("dispatched terminal item")
	}
}

func TestUnblockItemRestoresMissingInReviewTimestamp(t *testing.T) {
	p := newTestProgram(t)
	item := addTestItem(t, &p, "change", PriorityP0)
	activateTestProgram(t, &p)
	if err := p.DispatchItem(item.ID, "child-project"); err != nil {
		t.Fatal(err)
	}
	p.Items[0].PRRef = "#42"
	if err := p.BlockItem(item.ID, "awaiting decision"); err != nil {
		t.Fatal(err)
	}
	if p.Items[0].InReviewAt != "" {
		t.Fatalf("blocked fixture unexpectedly has in_review_at %q", p.Items[0].InReviewAt)
	}
	if err := p.UnblockItem(item.ID); err != nil {
		t.Fatalf("UnblockItem: %v", err)
	}
	got, _ := p.Item(item.ID)
	if got.Status != ItemInReview || got.InReviewAt == "" {
		t.Fatalf("unblocked item = %+v", got)
	}
}

func TestProjectSlugUniqueAcrossNonCancelledItems(t *testing.T) {
	p := newTestProgram(t)
	first := addTestItem(t, &p, "first", PriorityP0)
	second := addTestItem(t, &p, "second", PriorityP1)
	activateTestProgram(t, &p)
	if err := p.LinkItem(first.ID, "shared-child"); err != nil {
		t.Fatal(err)
	}
	if err := p.LinkItem(second.ID, "shared-child"); err == nil ||
		!strings.Contains(err.Error(), "already linked to item") {
		t.Fatalf("duplicate project link error = %v", err)
	}
	if err := p.CancelItem(first.ID, "superseded"); err != nil {
		t.Fatal(err)
	}
	if err := p.LinkItem(second.ID, "shared-child"); err != nil {
		t.Fatalf("reuse canceled project slug: %v", err)
	}

	p.Items[0].Status = ItemPending
	p.Items[0].CancelledAt = ""
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "already linked to item") {
		t.Fatalf("duplicate project slug validation error = %v", err)
	}
}

func TestUpdateItemIsAtomicAndValidatesDependencies(t *testing.T) {
	p := newTestProgram(t)
	first := addTestItem(t, &p, "first", PriorityP1)
	second := addTestItem(t, &p, "second", PriorityP2)

	title := "updated second"
	priority := PriorityP0
	if err := p.UpdateItem(second.ID, ItemUpdate{
		Title:           &title,
		Priority:        &priority,
		AddDependencies: []string{first.ID},
		Note:            "important",
	}); err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}
	got, _ := p.Item(second.ID)
	if got.Title != title || got.Priority != priority ||
		!reflect.DeepEqual(got.Dependencies, []string{first.ID}) ||
		!reflect.DeepEqual(got.Notes, []string{"important"}) {
		t.Fatalf("updated item = %+v", got)
	}

	before := p
	before.Items = append([]WorkItem(nil), p.Items...)
	if err := p.UpdateItem(first.ID, ItemUpdate{AddDependencies: []string{second.ID}}); err == nil {
		t.Fatal("dependency cycle update succeeded")
	}
	if !reflect.DeepEqual(p, before) {
		t.Fatalf("failed update mutated program:\n got: %+v\nwant: %+v", p, before)
	}
}

func TestTerminalProgramRejectsMutation(t *testing.T) {
	p := newTestProgram(t)
	activateTestProgram(t, &p)
	if err := p.Transition(StateCompleted, "cto"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AddItem(WorkItem{Title: "late", Priority: PriorityP0}); err == nil {
		t.Fatal("added item to completed program")
	}
	if _, err := p.OpenDecision(Decision{
		Kind:     DecisionQuestion,
		RaisedBy: RaisedByCTO,
		Question: "late?",
	}); err == nil {
		t.Fatal("opened decision on completed program")
	}
}
