package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/program"
)

// The tech-lead session that is already running was started before the rename
// and still types `--by cto`. The CLI admits it, converts it once, and every
// durable record and printed line carries the canonical identity.
func TestGrantOpenPRAdmitsRetiredByFlagAndWritesCanonicalActor(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	t.Setenv("HERDR_ENV", "")
	projectDir := messageProjectDir(manifest)
	if _, err := mailbox.Send(projectDir, mailbox.Outbox, mailbox.Message{
		ID: "pr-open-1", Kind: mailbox.KindPROpen, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorWorker, To: mailbox.ActorTL, Body: "Ready for PR.",
		CreatedAt: "2026-08-24T20:00:01Z",
	}); err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "grant-open-pr", p.Slug, item.ID, "--by", "cto", "--json")
	if err != nil {
		t.Fatalf("grant-open-pr: %v\n%s", err, out)
	}
	var got programOpenPRGrantOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	if got.GrantedBy != "tl" {
		t.Errorf("granted_by = %q, want %q", got.GrantedBy, "tl")
	}
	manifestPath := program.ManifestPath(program.ActiveDir(), p.Slug)
	loaded, err := program.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	granted, _ := loaded.Item(item.ID)
	if granted.PRGrantedBy != "tl" {
		t.Errorf("durable pr_granted_by = %q, want %q", granted.PRGrantedBy, "tl")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"pr_granted_by": "cto"`) {
		t.Errorf("manifest persisted a retired identity:\n%s", data)
	}
}

// The revocation message names the actor, so the retired identity must be
// converted before it reaches worker-visible text.
func TestRevokeOpenPRAdmitsRetiredByFlagInWorkerMessage(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	t.Setenv("HERDR_ENV", "")
	if err := p.GrantOpenPR(item.ID, "tl", nil); err != nil {
		t.Fatal(err)
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "revoke-open-pr", p.Slug, item.ID,
		"--by", "cto", "--reason", "capacity reassigned")
	if err != nil {
		t.Fatalf("revoke-open-pr: %v\n%s", err, out)
	}
	if !strings.Contains(out, "by tl:") {
		t.Errorf("output does not name the canonical actor: %q", out)
	}
	inbox, err := mailbox.List(messageProjectDir(manifest), mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || !strings.Contains(inbox[0].Body, "revoked by tl:") {
		t.Fatalf("revoke inbox = %#v", inbox)
	}
}

// A decision raised with the retired identity is stored canonically, so the
// low-level validator never sees a legacy raiser.
func TestDecisionOpenAdmitsRetiredRaisedBy(t *testing.T) {
	p, _, _, _ := createMessageFixture(t)

	if _, err := runProgramCommand(t, "decision", "open", p.Slug,
		"--kind", "question", "--raised-by", "cto", "--question", "Which rollout?"); err != nil {
		t.Fatalf("decision open: %v", err)
	}
	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Decisions) != 1 || loaded.Decisions[0].RaisedBy != program.RaisedByTL {
		t.Fatalf("decisions = %+v, want raised_by %q", loaded.Decisions, program.RaisedByTL)
	}
}

// A decision resolved with the retired identity is stored canonically too.
func TestDecisionResolveAdmitsRetiredBy(t *testing.T) {
	p, _, _, _ := createMessageFixture(t)
	if _, err := runProgramCommand(t, "decision", "open", p.Slug,
		"--kind", "question", "--question", "Which rollout?"); err != nil {
		t.Fatalf("decision open: %v", err)
	}

	if _, err := runProgramCommand(t, "decision", "resolve", p.Slug, "d1",
		"--by", "cto", "--answer", "Blue-green"); err != nil {
		t.Fatalf("decision resolve: %v", err)
	}
	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Decisions[0].ResolvedBy != "tl" {
		t.Fatalf("resolved_by = %q, want %q", loaded.Decisions[0].ResolvedBy, "tl")
	}
}

// Unrelated actors are content, not identities: admission leaves them alone.
func TestActorAdmissionLeavesUnrelatedActorsUnchanged(t *testing.T) {
	for _, actor := range []string{"ceo", "board", "rnathani", "cto-approved", ""} {
		if got := admitProgramActor(actor); got != actor {
			t.Errorf("admitProgramActor(%q) = %q, want it unchanged", actor, got)
		}
	}
}
