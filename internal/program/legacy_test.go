package program

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// legacyManifest is a program manifest exactly as it was written before the
// tech-lead rename: every actor-bearing field carries a retired identity, and
// the free-text question and answer mention the retired role by name.
const legacyManifest = `{
  "revision": 4,
  "slug": "legacy-program",
  "title": "Legacy Program",
  "repo": "/repo",
  "state": "active",
  "agent": "copilot",
  "max_open_prs": 2,
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": "2026-01-01T00:00:00Z",
  "approval_requested_at": "2026-01-01T00:00:00Z",
  "approved_at": "2026-01-01T00:00:00Z",
  "approved_by": "cto",
  "items": [
    {
      "id": "w1",
      "kind": "change",
      "title": "First change",
      "priority": "P1",
      "status": "dispatched",
      "dependencies": [],
      "contract_refs": [
        "architecture@v1"
      ],
      "repo": "/repo",
      "project_slug": "legacy-program-w1",
      "pr_granted_at": "2026-01-01T00:00:00Z",
      "pr_granted_by": "cto",
      "notes": [],
      "created_at": "2026-01-01T00:00:00Z",
      "updated_at": "2026-01-01T00:00:00Z",
      "dispatched_at": "2026-01-01T00:00:00Z"
    }
  ],
  "contracts": [
    {
      "name": "architecture",
      "version": 1,
      "ref": "architecture@v1",
      "path": "contracts/architecture/v1.md",
      "sha256": "abc123",
      "status": "approved",
      "published_at": "2026-01-01T00:00:00Z",
      "approved_at": "2026-01-01T00:00:00Z",
      "approved_by": "cto"
    },
    {
      "name": "storage",
      "version": 1,
      "ref": "storage@v1",
      "path": "contracts/storage/v1.md",
      "sha256": "def456",
      "status": "rejected",
      "published_at": "2026-01-01T00:00:00Z",
      "rejected_at": "2026-01-01T00:00:00Z",
      "rejected_by": "cto",
      "rejection_reason": "scope too wide"
    }
  ],
  "decisions": [
    {
      "id": "d1",
      "kind": "question",
      "raised_by": "cto",
      "question": "Should the cto grant capacity now?",
      "options": [],
      "answer": "Yes, keep the existing cto grant.",
      "resolved_by": "cto",
      "created_at": "2026-01-01T00:00:00Z",
      "resolved_at": "2026-01-01T00:00:00Z"
    },
    {
      "id": "d2",
      "kind": "question",
      "raised_by": "cto-automated:3f2504e0",
      "question": "Retry the push?",
      "options": [],
      "created_at": "2026-01-01T00:00:00Z"
    },
    {
      "id": "d3",
      "kind": "question",
      "raised_by": "worker",
      "question": "Which storage engine?",
      "options": [],
      "answer": "Postgres",
      "resolved_by": "ceo",
      "created_at": "2026-01-01T00:00:00Z",
      "resolved_at": "2026-01-01T00:00:00Z"
    }
  ]
}
`

func writeLegacyManifest(t *testing.T) string {
	t.Helper()
	setTestHome(t)
	dir := ProgramDir(ActiveDir(), "legacy-program")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := ManifestPath(ActiveDir(), "legacy-program")
	if err := os.WriteFile(path, []byte(legacyManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A manifest written before the tech-lead rename must still load, and every
// actor-bearing field must read as the canonical identity.
func TestLoadNormalizesLegacyActorsInEveryField(t *testing.T) {
	path := writeLegacyManifest(t)
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load legacy manifest: %v", err)
	}
	if loaded.ApprovedBy != "tl" {
		t.Errorf("approved_by = %q, want %q", loaded.ApprovedBy, "tl")
	}
	if loaded.Items[0].PRGrantedBy != "tl" {
		t.Errorf("item pr_granted_by = %q, want %q", loaded.Items[0].PRGrantedBy, "tl")
	}
	if loaded.Contracts[0].ApprovedBy != "tl" {
		t.Errorf("contract approved_by = %q, want %q", loaded.Contracts[0].ApprovedBy, "tl")
	}
	if loaded.Contracts[1].RejectedBy != "tl" {
		t.Errorf("contract rejected_by = %q, want %q", loaded.Contracts[1].RejectedBy, "tl")
	}
	if loaded.Decisions[0].RaisedBy != RaisedByTL {
		t.Errorf("decision raised_by = %q, want %q", loaded.Decisions[0].RaisedBy, RaisedByTL)
	}
	if loaded.Decisions[0].ResolvedBy != "tl" {
		t.Errorf("decision resolved_by = %q, want %q", loaded.Decisions[0].ResolvedBy, "tl")
	}
	if loaded.Decisions[1].RaisedBy != "tl-automated:3f2504e0" {
		t.Errorf("automated raised_by = %q, want %q", loaded.Decisions[1].RaisedBy, "tl-automated:3f2504e0")
	}
	if loaded.Decisions[2].RaisedBy != RaisedByWorker || loaded.Decisions[2].ResolvedBy != "ceo" {
		t.Errorf("unrelated actors changed: raised_by=%q resolved_by=%q",
			loaded.Decisions[2].RaisedBy, loaded.Decisions[2].ResolvedBy)
	}
}

// Normalization rewrites identities, never content. Historical questions and
// answers that mention the retired role are durable record, not actor fields.
func TestLoadLeavesLegacyFreeTextUnchanged(t *testing.T) {
	path := writeLegacyManifest(t)
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load legacy manifest: %v", err)
	}
	if loaded.Decisions[0].Question != "Should the cto grant capacity now?" {
		t.Errorf("decision question was rewritten: %q", loaded.Decisions[0].Question)
	}
	if loaded.Decisions[0].Answer != "Yes, keep the existing cto grant." {
		t.Errorf("decision answer was rewritten: %q", loaded.Decisions[0].Answer)
	}
}

// Reading a legacy manifest must not touch it: only an ordinary write does.
func TestLoadDoesNotRewriteLegacyManifestBytes(t *testing.T) {
	path := writeLegacyManifest(t)
	if _, err := Load(path); err != nil {
		t.Fatalf("Load legacy manifest: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != legacyManifest {
		t.Fatalf("Load rewrote the manifest:\n%s", data)
	}
}

// The first ordinary save of loaded legacy state is what makes the manifest
// canonical on disk.
func TestSaveEmitsCanonicalActorsForLoadedLegacyState(t *testing.T) {
	path := writeLegacyManifest(t)
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load legacy manifest: %v", err)
	}
	if err := Save(path, loaded); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{`"approved_by": "cto"`, `"pr_granted_by": "cto"`,
		`"rejected_by": "cto"`, `"raised_by": "cto"`, `"resolved_by": "cto"`,
		`"raised_by": "cto-automated:3f2504e0"`} {
		if strings.Contains(string(data), legacy) {
			t.Errorf("saved manifest still contains %s", legacy)
		}
	}
	for _, canonical := range []string{`"approved_by": "tl"`, `"pr_granted_by": "tl"`,
		`"rejected_by": "tl"`, `"raised_by": "tl"`, `"resolved_by": "tl"`,
		`"raised_by": "tl-automated:3f2504e0"`} {
		if !strings.Contains(string(data), canonical) {
			t.Errorf("saved manifest is missing %s", canonical)
		}
	}
	if !strings.Contains(string(data), "Should the cto grant capacity now?") ||
		!strings.Contains(string(data), "Yes, keep the existing cto grant.") {
		t.Error("saving rewrote historical free text")
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload saved manifest: %v", err)
	}
	if reloaded.Revision != loaded.Revision+1 {
		t.Errorf("revision = %d, want %d", reloaded.Revision, loaded.Revision+1)
	}
}

// Save also normalizes an in-memory legacy actor a caller supplies directly,
// so no path can persist a retired identity.
func TestSaveNormalizesLegacyActorSuppliedInMemory(t *testing.T) {
	setTestHome(t)
	p := newTestProgram(t)
	activateTestProgram(t, &p)
	item := dispatchedTestItem(t, &p, "first", "first-child")
	if err := Create(p); err != nil {
		t.Fatal(err)
	}
	path := ManifestPath(ActiveDir(), p.Slug)
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.GrantOpenPR(item.ID, "cto", nil); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, loaded); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	granted, ok := reloaded.Item(item.ID)
	if !ok || granted.PRGrantedBy != "tl" {
		t.Fatalf("pr_granted_by = %q, want %q", granted.PRGrantedBy, "tl")
	}
}

// Create is the other durable write boundary, so it normalizes too.
func TestCreateEmitsCanonicalActors(t *testing.T) {
	setTestHome(t)
	p := newTestProgram(t)
	activateTestProgram(t, &p)
	p.ApprovedBy = "cto"
	if err := Create(p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	data, err := os.ReadFile(ManifestPath(ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"approved_by": "cto"`) {
		t.Fatalf("Create persisted a retired identity:\n%s", data)
	}
}

// Normalize is a pure read of the receiver: callers keep the value they passed.
func TestNormalizeDoesNotMutateReceiver(t *testing.T) {
	p := Program{
		ApprovedBy: "cto",
		Items:      []WorkItem{{ID: "w1", PRGrantedBy: "cto"}},
		Contracts:  []Contract{{Ref: "a@v1", ApprovedBy: "cto", RejectedBy: "cto"}},
		Decisions:  []Decision{{ID: "d1", RaisedBy: "cto", ResolvedBy: "cto"}},
	}
	normalized := p.Normalize()
	if p.ApprovedBy != "cto" || p.Items[0].PRGrantedBy != "cto" ||
		p.Contracts[0].ApprovedBy != "cto" || p.Contracts[0].RejectedBy != "cto" ||
		p.Decisions[0].RaisedBy != "cto" || p.Decisions[0].ResolvedBy != "cto" {
		t.Fatalf("Normalize mutated its receiver: %+v", p)
	}
	if normalized.ApprovedBy != "tl" || normalized.Items[0].PRGrantedBy != "tl" ||
		normalized.Contracts[0].ApprovedBy != "tl" || normalized.Contracts[0].RejectedBy != "tl" ||
		normalized.Decisions[0].RaisedBy != "tl" || normalized.Decisions[0].ResolvedBy != "tl" {
		t.Fatalf("Normalize left a retired identity: %+v", normalized)
	}
}

// A retired raiser is not a valid new write: compatibility lives at the decode
// and CLI admission boundaries, never in the low-level validator.
func TestValidRaisedByRejectsRetiredIdentities(t *testing.T) {
	for _, valid := range []RaisedBy{RaisedByTL, RaisedByWorker, "tl-automated:3f2504e0"} {
		if !ValidRaisedBy(valid) {
			t.Errorf("ValidRaisedBy(%q) = false, want true", valid)
		}
	}
	for _, invalid := range []RaisedBy{"cto", "cto-automated:3f2504e0", "ceo", ""} {
		if ValidRaisedBy(invalid) {
			t.Errorf("ValidRaisedBy(%q) = true, want false", invalid)
		}
	}
}

// Only canonical JSON identifiers may leave the encoder.
func TestMarshalledProgramUsesCanonicalIdentities(t *testing.T) {
	p := Program{
		ApprovedBy: "tl",
		Items:      []WorkItem{{ID: "w1", PRGrantedBy: "tl"}},
		Decisions:  []Decision{{ID: "d1", RaisedBy: RaisedByTL, ResolvedBy: "tl"}},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "cto") {
		t.Fatalf("encoded program contains a retired identity:\n%s", data)
	}
}
