package program

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDecisionLifecycleAndQueries(t *testing.T) {
	p := newTestProgram(t)
	item := addTestItem(t, &p, "change", PriorityP0)
	programDecision, _, err := p.OpenDecision(Decision{
		Kind:     DecisionQuestion,
		RaisedBy: RaisedByTL,
		Question: "ship?",
		Options:  []string{"yes", "no"},
	})
	if err != nil {
		t.Fatal(err)
	}

	itemDecision, _, err := p.OpenDecision(Decision{
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

func TestOpenDecisionDedupesCurrentlyOpenDecisions(t *testing.T) {
	p := newTestProgram(t)
	item := addTestItem(t, &p, "change", PriorityP0)
	first, created, err := p.OpenDecision(Decision{
		Kind:     DecisionQuestion,
		RaisedBy: RaisedByTL,
		ItemID:   item.ID,
		Question: "Which storage engine should item w1 use?",
		Options:  []string{"postgres", "sqlite"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first open reported an existing decision")
	}
	before := p.UpdatedAt
	decisionCount := len(p.Decisions)

	// Whitespace, case, and option differences must not create a duplicate.
	duplicate, created, err := p.OpenDecision(Decision{
		Kind:     DecisionQuestion,
		RaisedBy: RaisedByWorker,
		ItemID:   item.ID,
		Question: "  which STORAGE   engine should item w1 use? ",
		Options:  []string{"postgres"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("duplicate question opened a second decision")
	}
	if duplicate.ID != first.ID || !reflect.DeepEqual(duplicate.Options, first.Options) {
		t.Fatalf("reused decision = %+v, want %+v", duplicate, first)
	}
	if len(p.Decisions) != decisionCount || p.UpdatedAt != before {
		t.Fatalf("reuse mutated the program: decisions %d updated %q", len(p.Decisions), p.UpdatedAt)
	}

	// A different item, kind, or question is a genuinely new decision.
	for _, next := range []Decision{
		{Kind: DecisionQuestion, RaisedBy: RaisedByTL, Question: "Which storage engine should item w1 use?"},
		{Kind: DecisionConflict, RaisedBy: RaisedByTL, ItemID: item.ID, Question: "Which storage engine should item w1 use?"},
		{Kind: DecisionQuestion, RaisedBy: RaisedByTL, ItemID: item.ID, Question: "Something else entirely?"},
	} {
		_, created, err := p.OpenDecision(next)
		if err != nil {
			t.Fatal(err)
		}
		if !created {
			t.Fatalf("distinct decision %+v was deduped", next)
		}
	}

	// A resolved decision no longer blocks reopening the same question.
	if err := p.ResolveDecision(first.ID, "postgres", "ceo"); err != nil {
		t.Fatal(err)
	}
	reopened, created, err := p.OpenDecision(Decision{
		Kind: DecisionQuestion, RaisedBy: RaisedByTL, ItemID: item.ID,
		Question: "Which storage engine should item w1 use?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created || reopened.ID == first.ID {
		t.Fatalf("resolved decision was reused: %+v created=%t", reopened, created)
	}
}

func TestOpenDecisionDedupesContractDecisionsByRef(t *testing.T) {
	p := newTestProgram(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "contract.md")
	if err := os.WriteFile(source, []byte("v1 body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contract, err := p.PublishContract(dir, "api", source)
	if err != nil {
		t.Fatal(err)
	}
	first := p.OpenDecisions()[0]
	same, created, err := p.OpenDecision(Decision{
		Kind: DecisionContract, RaisedBy: RaisedByTL, ContractRef: contract.Ref,
		Question: "Approve contract " + contract.Ref + "?", Options: []string{"approve", "reject"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created || same.ID != first.ID {
		t.Fatalf("duplicate contract decision = %+v created=%t", same, created)
	}
	if got := len(p.OpenDecisions()); got != 1 {
		t.Fatalf("open decisions after duplicate = %d, want 1", got)
	}
}

// Publishing successive contract versions must still open one approval decision
// per version even though decisions are now deduped.
func TestPublishContractStillOpensOneDecisionPerVersion(t *testing.T) {
	p := newTestProgram(t)
	dir := t.TempDir()
	for i, body := range []string{"v1 body\n", "v2 body\n"} {
		source := filepath.Join(dir, "contract.md")
		if err := os.WriteFile(source, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		contract, err := p.PublishContract(dir, "api", source)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(p.OpenDecisions()); got != i+1 {
			t.Fatalf("after publishing %s open decisions = %d, want %d", contract.Ref, got, i+1)
		}
	}
}
