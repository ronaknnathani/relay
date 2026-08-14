package program

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContractVersioningApprovalAndHashes(t *testing.T) {
	p := newTestProgram(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.md")
	if err := os.WriteFile(source, []byte("# API\n\nVersion one.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := p.PublishContract(dir, "API Contract", source)
	if err != nil {
		t.Fatalf("PublishContract v1: %v", err)
	}
	if first.Ref != "api-contract@v1" || first.Path != "contracts/api-contract/v1.md" {
		t.Fatalf("first contract = %+v", first)
	}
	if _, err := os.Stat(filepath.Join(dir, first.Path)); err != nil {
		t.Fatal(err)
	}
	open := p.OpenDecisions()
	if len(open) != 1 || open[0].Kind != DecisionContract || open[0].ContractRef != first.Ref {
		t.Fatalf("open decisions = %+v", open)
	}

	if err := os.WriteFile(source, []byte("# API\n\nVersion two.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := p.PublishContract(dir, "API Contract", source)
	if err != nil {
		t.Fatalf("PublishContract v2: %v", err)
	}
	if second.Ref != "api-contract@v2" {
		t.Fatalf("second ref = %s", second.Ref)
	}
	if err := p.VerifyHashes(dir); err != nil {
		t.Fatalf("VerifyHashes: %v", err)
	}
	if err := p.ApproveContract(first.Ref, "ceo"); err != nil {
		t.Fatalf("ApproveContract: %v", err)
	}
	if p.Contracts[0].Status != ContractApproved || p.Contracts[0].ApprovedBy != "ceo" {
		t.Fatalf("approved contract = %+v", p.Contracts[0])
	}
	for _, decision := range p.Decisions {
		if decision.ContractRef == first.Ref && decision.ResolvedAt == "" {
			t.Fatalf("contract decision still open: %+v", decision)
		}
	}
	if err := p.ApproveContract(first.Ref, "ceo"); err == nil {
		t.Fatal("second approval succeeded")
	}

	secondPath := filepath.Join(dir, second.Path)
	if err := os.Chmod(secondPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.VerifyHashes(dir); err == nil || !strings.Contains(err.Error(), second.Ref) {
		t.Fatalf("tamper error = %v", err)
	}
}

func TestParseContractRef(t *testing.T) {
	name, version, err := ParseContractRef("api-contract@v12")
	if err != nil || name != "api-contract" || version != 12 {
		t.Fatalf("ParseContractRef = %q, %d, %v", name, version, err)
	}
	for _, ref := range []string{"", "api", "API@v1", "api@v0", "api@vx", "../api@v1"} {
		if _, _, err := ParseContractRef(ref); err == nil {
			t.Errorf("ParseContractRef(%q) succeeded", ref)
		}
	}
}

func TestRejectContractAndSupersede(t *testing.T) {
	p := newTestProgram(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.md")
	if err := os.WriteFile(source, []byte("version one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := p.PublishContract(dir, "api", source)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.RejectContract(first.Ref, "ceo", "interface is incomplete"); err != nil {
		t.Fatalf("RejectContract: %v", err)
	}
	rejected := p.Contracts[0]
	if rejected.Status != ContractRejected || rejected.RejectedBy != "ceo" ||
		rejected.RejectedAt == "" || rejected.RejectionReason != "interface is incomplete" {
		t.Fatalf("rejected contract = %+v", rejected)
	}
	if len(p.OpenDecisions()) != 0 {
		t.Fatalf("open decisions = %+v", p.OpenDecisions())
	}

	if err := os.WriteFile(source, []byte("version two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := p.PublishContract(dir, "api", source)
	if err != nil {
		t.Fatal(err)
	}
	if second.Ref != "api@v2" {
		t.Fatalf("superseding contract ref = %q", second.Ref)
	}
	item := addTestItem(t, &p, "change", PriorityP0)
	p.Items[0].ContractRefs = []string{first.Ref}
	activateTestProgram(t, &p)
	_, blocked := p.Readiness()
	if len(blocked) != 1 || !strings.Contains(strings.Join(blocked[0].Reasons, "; "), "is rejected") {
		t.Fatalf("rejected readiness = %+v", blocked)
	}
	if err := p.ApproveContract(second.Ref, "ceo"); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateItem(item.ID, ItemUpdate{
		AddContractRefs:    []string{second.Ref},
		RemoveContractRefs: []string{first.Ref},
	}); err != nil {
		t.Fatal(err)
	}
	ready, _ := p.Readiness()
	if len(ready) != 1 || ready[0].ID != item.ID {
		t.Fatalf("ready after superseding = %+v", ready)
	}
}
