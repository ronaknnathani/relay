package program

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestRequestHashNormalizesWhitespaceAndIsStable(t *testing.T) {
	base, err := RequestHash("Rename  the token\nfield")
	if err != nil {
		t.Fatalf("RequestHash: %v", err)
	}
	want := sha256.Sum256([]byte("Rename the token field"))
	if base != hex.EncodeToString(want[:]) {
		t.Fatalf("RequestHash = %q, want %q", base, hex.EncodeToString(want[:]))
	}
	for _, equivalent := range []string{
		"  Rename  the token\nfield  ",
		"Rename the token\tfield",
		"Rename\tthe\r\ntoken field",
	} {
		got, err := RequestHash(equivalent)
		if err != nil {
			t.Fatalf("RequestHash(%q): %v", equivalent, err)
		}
		if got != base {
			t.Fatalf("RequestHash(%q) = %q, want %q", equivalent, got, base)
		}
	}
	different, err := RequestHash("Rename the token fields")
	if err != nil {
		t.Fatalf("RequestHash: %v", err)
	}
	if different == base {
		t.Fatal("a different request produced the same hash")
	}
}

func TestRequestHashRejectsAnEmptyRequest(t *testing.T) {
	for _, empty := range []string{"", "   ", "\n\t "} {
		if _, err := RequestHash(empty); err == nil {
			t.Fatalf("RequestHash(%q) succeeded, want an error", empty)
		}
	}
}

func TestRequestHashIsCaseSensitive(t *testing.T) {
	lower, err := RequestHash("rename the field")
	if err != nil {
		t.Fatalf("RequestHash: %v", err)
	}
	upper, err := RequestHash("Rename The Field")
	if err != nil {
		t.Fatalf("RequestHash: %v", err)
	}
	if lower == upper {
		t.Fatal("case-different requests collapsed onto one hash")
	}
}

func TestAddItemPreservesFollowUpFields(t *testing.T) {
	p := newTestProgram(t)
	original := addTestItem(t, &p, "Original", PriorityP1)
	hash, err := RequestHash("Please rename the token field")
	if err != nil {
		t.Fatalf("RequestHash: %v", err)
	}
	followUp, err := p.AddItem(WorkItem{
		Title:        "Follow-up: rename the token field",
		Priority:     PriorityP1,
		Dependencies: []string{original.ID},
		FollowUpOf:   original.ID,
		RequestHash:  hash,
	})
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if followUp.FollowUpOf != original.ID || followUp.RequestHash != hash {
		t.Fatalf("follow-up fields = %q/%q, want %q/%q",
			followUp.FollowUpOf, followUp.RequestHash, original.ID, hash)
	}
	stored, ok := p.Item(followUp.ID)
	if !ok {
		t.Fatal("follow-up item is missing from the program")
	}
	if stored.FollowUpOf != original.ID || stored.RequestHash != hash {
		t.Fatalf("stored follow-up fields = %q/%q", stored.FollowUpOf, stored.RequestHash)
	}
}

func TestValidateRejectsUnusableFollowUpReferences(t *testing.T) {
	hash, err := RequestHash("Please rename the token field")
	if err != nil {
		t.Fatalf("RequestHash: %v", err)
	}
	for name, mutate := range map[string]func(*WorkItem){
		"missing original": func(item *WorkItem) {
			item.FollowUpOf = "w99"
			item.RequestHash = hash
		},
		"self reference": func(item *WorkItem) {
			item.FollowUpOf = item.ID
			item.RequestHash = hash
		},
		"hash without reference": func(item *WorkItem) {
			item.RequestHash = hash
		},
		"reference without hash": func(item *WorkItem) {
			item.FollowUpOf = "w1"
		},
		"malformed hash": func(item *WorkItem) {
			item.FollowUpOf = "w1"
			item.RequestHash = "NOT-A-HASH"
		},
		"uppercase hash": func(item *WorkItem) {
			item.FollowUpOf = "w1"
			item.RequestHash = strings.ToUpper(hash)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := newTestProgram(t)
			addTestItem(t, &p, "Original", PriorityP1)
			second := addTestItem(t, &p, "Second", PriorityP1)
			index := -1
			for i, item := range p.Items {
				if item.ID == second.ID {
					index = i
				}
			}
			mutate(&p.Items[index])
			if err := p.Validate(); err == nil {
				t.Fatal("Validate accepted an unusable follow-up reference")
			}
		})
	}
}

func TestValidateRejectsFollowUpChainCycles(t *testing.T) {
	p := newTestProgram(t)
	first := addTestItem(t, &p, "First", PriorityP1)
	second := addTestItem(t, &p, "Second", PriorityP1)
	hash, err := RequestHash("mutual")
	if err != nil {
		t.Fatalf("RequestHash: %v", err)
	}
	p.Items[0].FollowUpOf, p.Items[0].RequestHash = second.ID, hash
	p.Items[1].FollowUpOf, p.Items[1].RequestHash = first.ID, hash
	if err := p.Validate(); err == nil {
		t.Fatal("Validate accepted a follow-up cycle")
	}
}

func TestAddItemRejectsDuplicateFollowUpRequests(t *testing.T) {
	p := newTestProgram(t)
	original := addTestItem(t, &p, "Original", PriorityP1)
	hash, err := RequestHash("Please rename the token field")
	if err != nil {
		t.Fatalf("RequestHash: %v", err)
	}
	candidate := WorkItem{
		Title:        "Follow-up",
		Priority:     PriorityP1,
		Dependencies: []string{original.ID},
		FollowUpOf:   original.ID,
		RequestHash:  hash,
	}
	if _, err := p.AddItem(candidate); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if _, err := p.AddItem(candidate); err == nil {
		t.Fatal("AddItem created a duplicate follow-up for the same request")
	}
}

func TestFindFollowUpReusesOnlyTheSameOriginalAndRequest(t *testing.T) {
	p := newTestProgram(t)
	original := addTestItem(t, &p, "Original", PriorityP1)
	other := addTestItem(t, &p, "Other", PriorityP1)
	hash, err := RequestHash("Please rename the token field")
	if err != nil {
		t.Fatalf("RequestHash: %v", err)
	}
	otherHash, err := RequestHash("Please add a metric")
	if err != nil {
		t.Fatalf("RequestHash: %v", err)
	}
	followUp, err := p.AddItem(WorkItem{
		Title: "Follow-up", Priority: PriorityP1,
		Dependencies: []string{original.ID}, FollowUpOf: original.ID, RequestHash: hash,
	})
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	found, ok := p.FindFollowUp(original.ID, hash)
	if !ok || found.ID != followUp.ID {
		t.Fatalf("FindFollowUp = %q/%t, want %q/true", found.ID, ok, followUp.ID)
	}
	if _, ok := p.FindFollowUp(original.ID, otherHash); ok {
		t.Fatal("FindFollowUp matched a different request")
	}
	if _, ok := p.FindFollowUp(other.ID, hash); ok {
		t.Fatal("FindFollowUp matched a different original item")
	}
}

func TestFindFollowUpIgnoresCancelledFollowUps(t *testing.T) {
	p := newTestProgram(t)
	original := addTestItem(t, &p, "Original", PriorityP1)
	activateTestProgram(t, &p)
	hash, err := RequestHash("Please rename the token field")
	if err != nil {
		t.Fatalf("RequestHash: %v", err)
	}
	followUp, err := p.AddItem(WorkItem{
		Title: "Follow-up", Priority: PriorityP1,
		Dependencies: []string{original.ID}, FollowUpOf: original.ID, RequestHash: hash,
	})
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if err := p.CancelItem(followUp.ID, "superseded"); err != nil {
		t.Fatalf("CancelItem: %v", err)
	}
	if _, ok := p.FindFollowUp(original.ID, hash); ok {
		t.Fatal("FindFollowUp reused a cancelled follow-up")
	}
	replacement, err := p.AddItem(WorkItem{
		Title: "Follow-up again", Priority: PriorityP1,
		Dependencies: []string{original.ID}, FollowUpOf: original.ID, RequestHash: hash,
	})
	if err != nil {
		t.Fatalf("AddItem after cancel: %v", err)
	}
	found, ok := p.FindFollowUp(original.ID, hash)
	if !ok || found.ID != replacement.ID {
		t.Fatalf("FindFollowUp = %q/%t, want %q/true", found.ID, ok, replacement.ID)
	}
}

func TestLegacyProgramJSONWithoutFollowUpFieldsStaysValid(t *testing.T) {
	const legacy = `{
  "revision": 2,
  "slug": "legacy-followup",
  "title": "Legacy",
  "repo": "/repo",
  "state": "active",
  "agent": "copilot",
  "max_open_prs": 2,
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": "2026-01-01T00:00:00Z",
  "approval_requested_at": "2026-01-01T00:00:00Z",
  "approved_at": "2026-01-01T00:00:00Z",
  "approved_by": "ceo",
  "items": [
    {
      "id": "w1",
      "kind": "change",
      "title": "First change",
      "priority": "P1",
      "status": "pending",
      "dependencies": [],
      "contract_refs": [],
      "repo": "/repo",
      "notes": [],
      "created_at": "2026-01-01T00:00:00Z",
      "updated_at": "2026-01-01T00:00:00Z"
    }
  ],
  "contracts": [],
  "decisions": []
}`
	var p Program
	if err := json.Unmarshal([]byte(legacy), &p); err != nil {
		t.Fatalf("decode legacy program: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("legacy program is invalid: %v", err)
	}
	if p.Items[0].FollowUpOf != "" || p.Items[0].RequestHash != "" {
		t.Fatalf("legacy item invented follow-up fields %q/%q",
			p.Items[0].FollowUpOf, p.Items[0].RequestHash)
	}
	encoded, err := json.Marshal(p.Items[0])
	if err != nil {
		t.Fatalf("encode item: %v", err)
	}
	for _, absent := range []string{"follow_up_of", "request_hash"} {
		if strings.Contains(string(encoded), absent) {
			t.Fatalf("legacy item re-encoded with %q: %s", absent, encoded)
		}
	}
}

func TestFollowUpFieldsSurviveACopyingMutation(t *testing.T) {
	p := newTestProgram(t)
	original := addTestItem(t, &p, "Original", PriorityP1)
	hash, err := RequestHash("Please rename the token field")
	if err != nil {
		t.Fatalf("RequestHash: %v", err)
	}
	followUp, err := p.AddItem(WorkItem{
		Title: "Follow-up", Priority: PriorityP1,
		Dependencies: []string{original.ID}, FollowUpOf: original.ID, RequestHash: hash,
	})
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	note := "recorded request"
	if err := p.UpdateItem(followUp.ID, ItemUpdate{Note: note}); err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}
	stored, ok := p.Item(followUp.ID)
	if !ok {
		t.Fatal("follow-up item disappeared")
	}
	if stored.FollowUpOf != original.ID || stored.RequestHash != hash {
		t.Fatalf("follow-up fields after update = %q/%q", stored.FollowUpOf, stored.RequestHash)
	}
	if len(stored.Notes) != 1 || stored.Notes[0] != note {
		t.Fatalf("notes = %v", stored.Notes)
	}
}
