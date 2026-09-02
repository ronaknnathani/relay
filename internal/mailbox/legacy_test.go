package mailbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	legacyOutboxID = "20260826T212439.770968000Z-ab8ce4bbdcb25aae8617fa70"
	legacyInboxID  = "20260826T212539.770968000Z-b70420262993e005c3c3ad30"
)

// legacyOutboxMessage is a worker->tech-lead message exactly as it was written
// before the tech-lead rename, including a body that mentions the retired role.
func legacyOutboxMessage(id string) string {
	return fmt.Sprintf(`{
  "id": %q,
  "kind": "pr-open",
  "program": "workload-mp",
  "item": "w2",
  "from": "worker",
  "to": "cto",
  "body": "Requesting the cto grant to open a pull request.",
  "options": [],
  "created_at": "2026-08-26T21:24:39.770968Z"
}
`, id)
}

// legacyInboxMessage is a tech-lead->worker message written by a bounded
// automated turn before the rename.
func legacyInboxMessage(id string) string {
	return fmt.Sprintf(`{
  "id": %q,
  "kind": "decision",
  "program": "workload-mp",
  "item": "w2",
  "from": "cto",
  "to": "worker",
  "body": "Proceed. [automated CTO turn 3f2504e0, on behalf of CEO]",
  "options": [],
  "automated_by": "cto-automated:3f2504e0",
  "reply_to": %q,
  "decision_id": "d2",
  "created_at": "2026-08-26T21:25:39.770968Z"
}
`, id, legacyOutboxID)
}

func writeLegacyMailbox(t *testing.T) string {
	t.Helper()
	projectDir := t.TempDir()
	if err := Ensure(projectDir); err != nil {
		t.Fatal(err)
	}
	write := func(box Box, id, content string) {
		t.Helper()
		dir, err := boxDir(projectDir, box, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(Outbox, legacyOutboxID, legacyOutboxMessage(legacyOutboxID))
	write(Inbox, legacyInboxID, legacyInboxMessage(legacyInboxID))
	return projectDir
}

// A mailbox written before the tech-lead rename still routes: list and find
// return the message with canonical endpoints.
func TestListAndFindNormalizeLegacyEndpoints(t *testing.T) {
	projectDir := writeLegacyMailbox(t)

	outbox, err := List(projectDir, Outbox)
	if err != nil {
		t.Fatalf("List outbox: %v", err)
	}
	if len(outbox) != 1 {
		t.Fatalf("outbox = %d messages, want 1", len(outbox))
	}
	if outbox[0].To != ActorTL {
		t.Errorf("outbox to = %q, want %q", outbox[0].To, ActorTL)
	}

	inbox, err := Find(projectDir, Inbox, legacyInboxID)
	if err != nil {
		t.Fatalf("Find inbox: %v", err)
	}
	if inbox.From != ActorTL {
		t.Errorf("inbox from = %q, want %q", inbox.From, ActorTL)
	}
	if inbox.AutomatedBy != "tl-automated:3f2504e0" {
		t.Errorf("inbox automated_by = %q, want %q", inbox.AutomatedBy, "tl-automated:3f2504e0")
	}
}

// The historical body is durable record, not an identity: normalization never
// edits it.
func TestReadLeavesLegacyBodyUnchanged(t *testing.T) {
	projectDir := writeLegacyMailbox(t)
	found, err := Find(projectDir, Outbox, legacyOutboxID)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if found.Body != "Requesting the cto grant to open a pull request." {
		t.Fatalf("body was rewritten: %q", found.Body)
	}
}

// Reading a legacy mailbox must never rewrite it. The exact file bytes are the
// only thing that proves it.
func TestReadOnlyOperationsDoNotRewriteLegacyMailboxBytes(t *testing.T) {
	projectDir := writeLegacyMailbox(t)
	outboxDir, err := boxDir(projectDir, Outbox, false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outboxDir, legacyOutboxID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := List(projectDir, Outbox); err != nil {
		t.Fatal(err)
	}
	if _, err := Find(projectDir, Outbox, legacyOutboxID); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("reading rewrote the message:\nbefore: %s\nafter:  %s", before, after)
	}
	if !strings.Contains(string(after), `"to": "cto"`) {
		t.Fatalf("legacy bytes were normalized on disk:\n%s", after)
	}
}

// Acknowledge is an ordinary move, not a rewrite: a legacy message keeps its
// exact bytes as it lands in the processed mailbox.
func TestAcknowledgeMovesLegacyMessageWithoutRewritingIt(t *testing.T) {
	projectDir := writeLegacyMailbox(t)
	if err := Acknowledge(projectDir, Outbox, legacyOutboxID); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	processedDir, err := boxDir(projectDir, Outbox, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(processedDir, legacyOutboxID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != legacyOutboxMessage(legacyOutboxID) {
		t.Fatalf("acknowledged message was rewritten:\n%s", data)
	}
}

// A reply to a legacy request is a new durable write, so it must be canonical.
func TestReplyToLegacyMessageWritesCanonicalEndpoints(t *testing.T) {
	projectDir := writeLegacyMailbox(t)
	request, err := Find(projectDir, Outbox, legacyOutboxID)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := Send(projectDir, Inbox, Message{
		Kind: KindDecision, Program: request.Program, Item: request.Item,
		From: ActorTL, To: ActorWorker, Body: "Granted.", ReplyTo: request.ID,
	})
	if err != nil {
		t.Fatalf("Send reply: %v", err)
	}
	inboxDir, err := boxDir(projectDir, Inbox, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(inboxDir, reply.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"cto"`) || strings.Contains(string(data), "cto-automated:") {
		t.Fatalf("reply persisted a retired identity:\n%s", data)
	}
	if !strings.Contains(string(data), `"from": "tl"`) {
		t.Fatalf("reply is missing the canonical sender:\n%s", data)
	}
}

// Send is the low-level write boundary and never accepts a retired endpoint or
// automated marker: compatibility belongs at decode and CLI admission.
func TestSendRejectsRetiredIdentities(t *testing.T) {
	projectDir := t.TempDir()
	tests := []struct {
		name    string
		box     Box
		message Message
	}{
		{
			name: "legacy outbox recipient",
			box:  Outbox,
			message: Message{
				Kind: KindQuestion, Program: "governance", Item: "w1",
				From: ActorWorker, To: "cto", Body: "Which API?",
			},
		},
		{
			name: "legacy inbox sender",
			box:  Inbox,
			message: Message{
				Kind: KindDecision, Program: "governance", Item: "w1",
				From: "cto", To: ActorWorker, Body: "Use the adapter.",
			},
		},
		{
			name: "legacy automated marker",
			box:  Inbox,
			message: Message{
				Kind: KindDecision, Program: "governance", Item: "w1",
				From: ActorTL, To: ActorWorker, Body: "Use the adapter.",
				AutomatedBy: "cto-automated:3f2504e0",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Send(projectDir, test.box, test.message); err == nil {
				t.Fatal("Send accepted a retired identity")
			}
		})
	}
}

// Every new durable message uses only canonical identifiers.
func TestSendPersistsOnlyCanonicalIdentities(t *testing.T) {
	projectDir := t.TempDir()
	sent, err := Send(projectDir, Inbox, Message{
		Kind: KindDecision, Program: "governance", Item: "w1",
		From: ActorTL, To: ActorWorker, Body: "Use the adapter.",
		AutomatedBy: "tl-automated:3f2504e0",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	dir, err := boxDir(projectDir, Inbox, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, sent.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "cto") {
		t.Fatalf("message contains a retired identity:\n%s", data)
	}
}
