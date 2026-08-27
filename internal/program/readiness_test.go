package program

import (
	"reflect"
	"testing"
)

func TestReadinessOrderAndReasons(t *testing.T) {
	p := newTestProgram(t)
	w1 := addTestItem(t, &p, "dependency", PriorityP2)
	w2 := addTestItem(t, &p, "later id P0", PriorityP0, w1.ID)
	w3 := addTestItem(t, &p, "first P0", PriorityP0)
	w4 := addTestItem(t, &p, "P1", PriorityP1)

	p.Contracts = append(p.Contracts, Contract{
		Name:        "api",
		Version:     1,
		Ref:         "api@v1",
		Path:        "contracts/api/v1.md",
		SHA256:      "abc",
		Status:      ContractPending,
		PublishedAt: p.CreatedAt,
	})
	p.Items[3].ContractRefs = []string{"api@v1"}
	activateTestProgram(t, &p)
	decision, _, err := p.OpenDecision(Decision{
		Kind:     DecisionQuestion,
		RaisedBy: RaisedByCTO,
		ItemID:   w3.ID,
		Question: "implementation?",
		Options:  []string{"a", "b"},
	})
	if err != nil {
		t.Fatal(err)
	}

	ready, blocked := p.Readiness()
	var readyIDs []string
	for _, item := range ready {
		readyIDs = append(readyIDs, item.ID)
	}
	if !reflect.DeepEqual(readyIDs, []string{w1.ID}) {
		t.Fatalf("ready IDs = %v, want [%s]", readyIDs, w1.ID)
	}
	reasons := map[string][]string{}
	for _, item := range blocked {
		reasons[item.Item.ID] = item.Reasons
	}
	if !reflect.DeepEqual(reasons[w2.ID], []string{"dependency w1 is pending"}) {
		t.Errorf("w2 reasons = %v", reasons[w2.ID])
	}
	if !reflect.DeepEqual(reasons[w3.ID], []string{"open decision " + decision.ID}) {
		t.Errorf("w3 reasons = %v", reasons[w3.ID])
	}
	if !reflect.DeepEqual(reasons[w4.ID], []string{"contract api@v1 is pending"}) {
		t.Errorf("w4 reasons = %v", reasons[w4.ID])
	}

	p.Items[0].ProjectSlug = "dependency-project"
	p.Items[0].Status = ItemMerged
	p.Items[0].DispatchedAt = p.UpdatedAt
	p.Items[0].MergedAt = p.UpdatedAt
	if err := p.ResolveDecision(decision.ID, "a", "ceo"); err != nil {
		t.Fatal(err)
	}
	p.Contracts[0].Status = ContractApproved
	p.Contracts[0].ApprovedAt = p.UpdatedAt
	p.Contracts[0].ApprovedBy = "ceo"
	ready, _ = p.Readiness()
	readyIDs = readyIDs[:0]
	for _, item := range ready {
		readyIDs = append(readyIDs, item.ID)
	}
	if !reflect.DeepEqual(readyIDs, []string{w2.ID, w3.ID, w4.ID}) {
		t.Fatalf("ordered ready IDs = %v", readyIDs)
	}
}

func TestProgramDecisionBlocksAllReadiness(t *testing.T) {
	p := newTestProgram(t)
	item := addTestItem(t, &p, "change", PriorityP0)
	activateTestProgram(t, &p)
	decision, _, err := p.OpenDecision(Decision{
		Kind:     DecisionConflict,
		RaisedBy: RaisedByWorker,
		Question: "which direction?",
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, blocked := p.Readiness()
	if len(ready) != 0 {
		t.Fatalf("ready = %v", ready)
	}
	if len(blocked) != 1 || blocked[0].Item.ID != item.ID ||
		!reflect.DeepEqual(blocked[0].Reasons, []string{"open program decision " + decision.ID}) {
		t.Fatalf("blocked = %+v", blocked)
	}
}
