package mailbox

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/patrollock"
)

func TestEnsureCreatesMailboxLayout(t *testing.T) {
	projectDir := t.TempDir()

	if err := Ensure(projectDir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	for _, path := range []string{
		"mail/inbox",
		"mail/outbox",
		"mail/notified",
		"mail/processed/inbox",
		"mail/processed/outbox",
	} {
		info, err := os.Stat(filepath.Join(projectDir, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", path)
		}
	}
}

func TestNotificationMarkers(t *testing.T) {
	projectDir := t.TempDir()

	notified, err := IsNotified(projectDir, "message-1")
	if err != nil {
		t.Fatalf("IsNotified before mark: %v", err)
	}
	if notified {
		t.Fatal("IsNotified before mark = true, want false")
	}
	marked, err := MarkNotified(projectDir, "message-1")
	if err != nil {
		t.Fatalf("first MarkNotified: %v", err)
	}
	if !marked {
		t.Fatal("first MarkNotified = false, want true")
	}
	notified, err = IsNotified(projectDir, "message-1")
	if err != nil {
		t.Fatalf("IsNotified after mark: %v", err)
	}
	if !notified {
		t.Fatal("IsNotified after mark = false, want true")
	}
	marked, err = MarkNotified(projectDir, "message-1")
	if err != nil {
		t.Fatalf("second MarkNotified: %v", err)
	}
	if marked {
		t.Fatal("second MarkNotified = true, want false")
	}
}

func TestNotificationMarkersRejectInvalidIDs(t *testing.T) {
	projectDir := t.TempDir()

	for _, id := range []string{"../escape", "nested/file", `nested\file`, ".."} {
		if _, err := IsNotified(projectDir, id); err == nil {
			t.Errorf("IsNotified(%q) succeeded", id)
		}
		if _, err := MarkNotified(projectDir, id); err == nil {
			t.Errorf("MarkNotified(%q) succeeded", id)
		}
	}
}

func TestSendAndListRoundTripInCreatedOrder(t *testing.T) {
	projectDir := t.TempDir()
	times := []time.Time{
		time.Date(2026, time.August, 24, 20, 0, 1, 0, time.UTC),
		time.Date(2026, time.August, 24, 20, 0, 0, 0, time.UTC),
	}
	index := 0
	previousNow := now
	previousRandom := randomReader
	now = func() time.Time {
		value := times[index]
		index++
		return value
	}
	randomReader = bytes.NewReader(bytes.Repeat([]byte{0x2a}, 24))
	t.Cleanup(func() {
		now = previousNow
		randomReader = previousRandom
	})

	later, err := Send(projectDir, Outbox, Message{
		Kind: KindQuestion, Program: "governance", Item: "w1",
		From: ActorWorker, To: ActorTL, Body: "Which API?", Options: []string{"A", "B"},
	})
	if err != nil {
		t.Fatalf("Send later: %v", err)
	}
	earlier, err := Send(projectDir, Outbox, Message{
		Kind: KindPlan, Program: "governance", Item: "w1",
		From: ActorWorker, To: ActorTL, Body: "Review the plan.", Options: []string{},
	})
	if err != nil {
		t.Fatalf("Send earlier: %v", err)
	}

	got, err := List(projectDir, Outbox)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []Message{earlier, later}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestListOrdersCreatedAtChronologically(t *testing.T) {
	projectDir := t.TempDir()
	later, err := Send(projectDir, Outbox, Message{
		ID: "later", Kind: KindQuestion, Program: "governance", Item: "w1",
		From: ActorWorker, To: ActorTL, Body: "Later",
		CreatedAt: "2026-08-24T20:00:00-07:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	earlier, err := Send(projectDir, Outbox, Message{
		ID: "earlier", Kind: KindQuestion, Program: "governance", Item: "w1",
		From: ActorWorker, To: ActorTL, Body: "Earlier",
		CreatedAt: "2026-08-25T02:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := List(projectDir, Outbox)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Message{earlier, later}; !reflect.DeepEqual(got, want) {
		t.Fatalf("List:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestFindAndAcknowledgeMoveMessageToProcessed(t *testing.T) {
	projectDir := t.TempDir()
	sent, err := Send(projectDir, Inbox, Message{
		Kind: KindDecision, Program: "governance", Item: "w2",
		From: ActorTL, To: ActorWorker, Body: "Use the adapter.",
		ReplyTo: "question-1", DecisionID: "d2",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	found, err := Find(projectDir, Inbox, sent.ID)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !reflect.DeepEqual(found, sent) {
		t.Fatalf("Find:\n got: %#v\nwant: %#v", found, sent)
	}
	unreadPath := filepath.Join(projectDir, "mail", "inbox", sent.ID+".json")
	before, err := os.ReadFile(unreadPath)
	if err != nil {
		t.Fatalf("read unread message: %v", err)
	}
	if err := Acknowledge(projectDir, Inbox, sent.ID); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if _, err := Find(projectDir, Inbox, sent.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Find unread after acknowledge error = %v, want not exist", err)
	}

	processedPath := filepath.Join(projectDir, "mail", "processed", "inbox", sent.ID+".json")
	data, err := os.ReadFile(processedPath)
	if err != nil {
		t.Fatalf("read processed message: %v", err)
	}
	if _, err := os.Stat(unreadPath); !os.IsNotExist(err) {
		t.Fatalf("unread message still exists: %v", err)
	}
	if !bytes.Equal(data, before) {
		t.Fatal("acknowledged message bytes changed")
	}
}

func TestRejectsDuplicateAndPathTraversalIDs(t *testing.T) {
	projectDir := t.TempDir()
	message := Message{
		ID: "fixed-id", Kind: KindQuestion, Program: "governance", Item: "w1",
		From: ActorWorker, To: ActorTL, Body: "Which API?",
		CreatedAt: "2026-08-24T20:00:00Z",
	}
	if _, err := Send(projectDir, Outbox, message); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if _, err := Send(projectDir, Outbox, message); !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate Send error = %v, want already exists", err)
	}
	for _, id := range []string{"../escape", "nested/file", `nested\file`, ".."} {
		if _, err := Find(projectDir, Outbox, id); err == nil {
			t.Errorf("Find(%q) succeeded", id)
		}
		if err := Acknowledge(projectDir, Outbox, id); err == nil {
			t.Errorf("Acknowledge(%q) succeeded", id)
		}
	}

}

func TestListRejectsMessageWhoseIDDoesNotMatchFilename(t *testing.T) {
	projectDir := t.TempDir()
	message, err := Send(projectDir, Outbox, Message{
		ID: "original", Kind: KindQuestion, Program: "governance", Item: "w1",
		From: ActorWorker, To: ActorTL, Body: "Which API?",
		CreatedAt: "2026-08-24T20:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(projectDir, "mail", "outbox")
	if err := os.Rename(
		filepath.Join(dir, message.ID+".json"),
		filepath.Join(dir, "different.json"),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := List(projectDir, Outbox); err == nil || !strings.Contains(err.Error(), "does not match filename") {
		t.Fatalf("List error = %v", err)
	}
}

func TestAcknowledgeRejectsInvalidMessageWithoutMovingIt(t *testing.T) {
	projectDir := t.TempDir()
	if err := Ensure(projectDir); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(projectDir, "mail", "inbox", "invalid.json")
	if err := os.WriteFile(source, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Acknowledge(projectDir, Inbox, "invalid"); err == nil {
		t.Fatal("Acknowledge succeeded")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("invalid unread message was moved: %v", err)
	}
}

func TestSendValidatesMessages(t *testing.T) {
	validOutbox := Message{
		Kind: KindQuestion, Program: "governance", Item: "w1",
		From: ActorWorker, To: ActorTL, Body: "Which API?",
	}
	tests := []struct {
		name    string
		box     Box
		message Message
	}{
		{name: "box", box: Box("other"), message: validOutbox},
		{name: "id", box: Outbox, message: withMessage(validOutbox, func(m *Message) { m.ID = "../escape" })},
		{name: "program", box: Outbox, message: withMessage(validOutbox, func(m *Message) { m.Program = "../program" })},
		{name: "item", box: Outbox, message: withMessage(validOutbox, func(m *Message) { m.Item = "item-one" })},
		{name: "body", box: Outbox, message: withMessage(validOutbox, func(m *Message) { m.Body = " " })},
		{name: "option", box: Outbox, message: withMessage(validOutbox, func(m *Message) { m.Options = []string{"A", ""} })},
		{name: "reply", box: Outbox, message: withMessage(validOutbox, func(m *Message) { m.ReplyTo = "../reply" })},
		{name: "decision", box: Outbox, message: withMessage(validOutbox, func(m *Message) { m.DecisionID = "decision-1" })},
		{name: "created", box: Outbox, message: withMessage(validOutbox, func(m *Message) { m.CreatedAt = "yesterday" })},
		{name: "kind", box: Outbox, message: withMessage(validOutbox, func(m *Message) { m.Kind = KindDecision })},
		{name: "actors", box: Outbox, message: withMessage(validOutbox, func(m *Message) {
			m.From, m.To = ActorTL, ActorWorker
		})},
		{name: "inbox kind", box: Inbox, message: Message{
			Kind: KindQuestion, Program: "governance", Item: "w1",
			From: ActorTL, To: ActorWorker, Body: "Wrong kind.",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Send(t.TempDir(), test.box, test.message); err == nil {
				t.Fatal("Send succeeded")
			}
		})
	}
}

func TestSendReportsRandomFailure(t *testing.T) {
	previousRandom := randomReader
	randomReader = io.LimitReader(bytes.NewReader(nil), 0)
	t.Cleanup(func() { randomReader = previousRandom })

	_, err := Send(t.TempDir(), Outbox, Message{
		Kind: KindQuestion, Program: "governance", Item: "w1",
		From: ActorWorker, To: ActorTL, Body: "Which API?",
	})
	if err == nil || !strings.Contains(err.Error(), "generate mailbox message id") {
		t.Fatalf("Send error = %v", err)
	}
}

func withMessage(message Message, update func(*Message)) Message {
	update(&message)
	return message
}

// A writer that is interrupted or killed must not leave a marker that blocks
// every later send and acknowledge. The kernel releases a flock when the
// holding process dies, and a leftover lock file alone means nothing.
func TestSendAndAcknowledgeIgnoreALeftoverLockFile(t *testing.T) {
	projectDir := t.TempDir()
	if err := Ensure(projectDir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, box := range []Box{Outbox, Inbox} {
		for _, processed := range []bool{false, true} {
			dir, err := boxDir(projectDir, box, processed)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(lockPath(dir), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	message, err := Send(projectDir, Outbox, Message{
		Kind: KindQuestion, Program: "governance", Item: "w1",
		From: ActorWorker, To: ActorTL, Body: "Which API?",
	})
	if err != nil {
		t.Fatalf("Send with a leftover lock file: %v", err)
	}
	if err := Acknowledge(projectDir, Outbox, message.ID); err != nil {
		t.Fatalf("Acknowledge with a leftover lock file: %v", err)
	}
}

// The reservation is mutually exclusive across processes while a holder lives,
// and becomes available again the moment a killed holder's process exits.
func TestReserveExcludesALiveHolderAndRecoversFromAKilledOne(t *testing.T) {
	projectDir := t.TempDir()
	if err := Ensure(projectDir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	dir, err := boxDir(projectDir, Outbox, false)
	if err != nil {
		t.Fatal(err)
	}
	previousTimeout := reserveTimeout
	reserveTimeout = 150 * time.Millisecond
	t.Cleanup(func() { reserveTimeout = previousTimeout })

	holder := startMailboxLockHolder(t, lockPath(dir))
	send := func() (Message, error) {
		return Send(projectDir, Outbox, Message{
			Kind: KindQuestion, Program: "governance", Item: "w1",
			From: ActorWorker, To: ActorTL, Body: "Which API?",
		})
	}
	if _, err := send(); err == nil {
		t.Fatal("Send succeeded while another process held the mailbox lock")
	} else {
		for _, want := range []string{"another mailbox writer", lockPath(dir), reserveTimeout.String()} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Send error %q is missing %q", err, want)
			}
		}
		if !errors.Is(err, patrollock.ErrLocked) {
			t.Errorf("Send error %v does not wrap patrollock.ErrLocked", err)
		}
	}

	if err := holder.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Wait(); err != nil && !strings.Contains(err.Error(), "killed") {
		t.Fatalf("wait for killed lock holder: %v", err)
	}
	if _, err := send(); err != nil {
		t.Fatalf("Send after the lock holder was killed: %v", err)
	}
}

// startMailboxLockHolder runs this test binary as a child process that holds
// the kernel lock at path until it is killed.
func startMailboxLockHolder(t *testing.T, path string) *os.Process {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(os.Args[0], "-test.run=TestMailboxLockHolderProcess", "-test.timeout=60s")
	command.Env = append(os.Environ(),
		"RELAY_MAILBOX_LOCK_HOLDER="+path,
		"RELAY_MAILBOX_LOCK_HOLDER_READY="+ready,
	)
	if err := command.Start(); err != nil {
		t.Fatalf("start mailbox lock holder: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return command.Process
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("wait for mailbox lock holder: %v", err)
		}
		if !time.Now().Before(deadline) {
			t.Fatal("mailbox lock holder never acquired the lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestMailboxLockHolderProcess is not a test: it is the child process
// startMailboxLockHolder runs. It exits only when it is killed.
func TestMailboxLockHolderProcess(t *testing.T) {
	path := os.Getenv("RELAY_MAILBOX_LOCK_HOLDER")
	if path == "" {
		t.Skip("helper process for TestReserveExcludesALiveHolderAndRecoversFromAKilledOne")
	}
	if _, err := patrollock.Acquire(path); err != nil {
		t.Fatalf("hold mailbox lock %s: %v", path, err)
	}
	if err := os.WriteFile(os.Getenv("RELAY_MAILBOX_LOCK_HOLDER_READY"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(55 * time.Second)
}

func TestExistsFindsUnreadAndProcessedMessages(t *testing.T) {
	projectDir := t.TempDir()
	if err := Ensure(projectDir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	present, err := Exists(projectDir, Inbox, "change-abc123")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if present {
		t.Fatal("Exists reported an unwritten message")
	}
	if _, err := Send(projectDir, Inbox, Message{
		ID: "change-abc123", Kind: KindFeedback, Program: "governance", Item: "w1",
		From: ActorTL, To: ActorWorker, Body: "Rename the token field",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	present, err = Exists(projectDir, Inbox, "change-abc123")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !present {
		t.Fatal("Exists missed an unread message")
	}
	if err := Acknowledge(projectDir, Inbox, "change-abc123"); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	present, err = Exists(projectDir, Inbox, "change-abc123")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !present {
		t.Fatal("Exists missed a processed message")
	}
	present, err = Exists(projectDir, Outbox, "change-abc123")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if present {
		t.Fatal("Exists matched the wrong mailbox")
	}
}

func TestExistsRejectsAnUnsafeMessageID(t *testing.T) {
	projectDir := t.TempDir()
	for _, id := range []string{"", "../escape", "with/slash"} {
		if _, err := Exists(projectDir, Inbox, id); err == nil {
			t.Fatalf("Exists(%q) succeeded, want a filename-safety error", id)
		}
	}
}

func TestSendRefusesToOverwriteAnExistingMessageID(t *testing.T) {
	projectDir := t.TempDir()
	message := Message{
		ID: "change-abc123", Kind: KindFeedback, Program: "governance", Item: "w1",
		From: ActorTL, To: ActorWorker, Body: "Rename the token field",
	}
	if _, err := Send(projectDir, Inbox, message); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, err := Send(projectDir, Inbox, message)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("second Send = %v, want os.ErrExist", err)
	}
	unread, err := List(projectDir, Inbox)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(unread) != 1 {
		t.Fatalf("unread messages = %d, want 1", len(unread))
	}
}
