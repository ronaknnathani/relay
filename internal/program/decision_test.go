package program

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecisionLifecycleAndQueries(t *testing.T) {
	p := newTestProgram(t)
	item := addTestItem(t, &p, "change", PriorityP0)
	programDecision, err := p.OpenDecision(Decision{
		Kind:     DecisionQuestion,
		RaisedBy: RaisedByCTO,
		Question: "ship?",
		Options:  []string{"yes", "no"},
	})
	if err != nil {
		t.Fatal(err)
	}

	itemDecision, err := p.OpenDecision(Decision{
		Kind:     DecisionConflict,
		RaisedBy: RaisedByWorker,
		ItemID:   item.ID,
		Question: "which API?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.OpenProgramDecisions()) != 1 || p.OpenProgramDecisions()[0].ID != programDecision.ID {
		t.Fatalf("program decisions = %+v", p.OpenProgramDecisions())
	}
	if len(p.OpenItemDecisions(item.ID)) != 1 || p.OpenItemDecisions(item.ID)[0].ID != itemDecision.ID {
		t.Fatalf("item decisions = %+v", p.OpenItemDecisions(item.ID))
	}
	if err := p.ResolveDecision(itemDecision.ID, "", "ceo"); err == nil {
		t.Fatal("empty answer accepted")
	}
	if err := p.ResolveDecision(itemDecision.ID, "new API", ""); err == nil {
		t.Fatal("empty resolver accepted")
	}
	if err := p.ResolveDecision(itemDecision.ID, "new API", "ceo"); err != nil {
		t.Fatalf("ResolveDecision: %v", err)
	}
	if err := p.ResolveDecision(itemDecision.ID, "old API", "ceo"); err == nil {
		t.Fatal("second resolution succeeded")
	}
}

func TestResolveDecisionRejectsContractDecision(t *testing.T) {
	p := newTestProgram(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "contract.md")
	if err := os.WriteFile(source, []byte("contract\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contract, err := p.PublishContract(dir, "api", source)
	if err != nil {
		t.Fatal(err)
	}
	decision := p.OpenDecisions()[0]

	err = p.ResolveDecision(decision.ID, "approve", "ceo")
	if err == nil {
		t.Fatal("generic resolution accepted a contract decision")
	}
	for _, want := range []string{
		"contract approve " + p.Slug + " " + contract.Ref,
		"contract reject " + p.Slug + " " + contract.Ref,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ResolveDecision error %q missing %q", err, want)
		}
	}
}
