package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

func createMessageFixture(t *testing.T) (program.Program, program.WorkItem, project.Manifest, []byte) {
	t.Helper()
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	projectDir := filepath.Dir(project.ManifestPath(project.ActiveDir(), manifest.Slug))
	if err := mailbox.Ensure(projectDir); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	return p, item, manifest, before
}

func assertProgramBytes(t *testing.T, slug string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(program.ManifestPath(program.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("program.json changed")
	}
}

func TestProgramMessageSendWritesOnlyWorkerOutbox(t *testing.T) {
	p, item, manifest, before := createMessageFixture(t)

	out, err := runProgramCommand(t, "message", "send", p.Slug, item.ID,
		"--kind", "question", "--body", "Which API?", "--options", "A | B")
	if err != nil {
		t.Fatalf("message send: %v", err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		t.Fatal("message send printed no id")
	}
	messages, err := mailbox.List(filepath.Dir(project.ManifestPath(project.ActiveDir(), manifest.Slug)), mailbox.Outbox)
	if err != nil {
		t.Fatal(err)
	}
	want := []mailbox.Message{{
		ID: id, Kind: mailbox.KindQuestion, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorWorker, To: mailbox.ActorCTO, Body: "Which API?",
		Options: []string{"A", "B"}, CreatedAt: messages[0].CreatedAt,
	}}
	if !reflect.DeepEqual(messages, want) {
		t.Fatalf("outbox:\n got: %#v\nwant: %#v", messages, want)
	}
	assertProgramBytes(t, p.Slug, before)
}

func TestProgramMessageSendRepairsMissingMailbox(t *testing.T) {
	p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
	projectDir := messageProjectDir(manifest)

	if _, err := runProgramCommand(t, "message", "send", p.Slug, item.ID,
		"--kind", "question", "--body", "Which API?"); err != nil {
		t.Fatalf("message send: %v", err)
	}
	assertMailboxLayout(t, projectDir)
}

func TestProgramMessageReadCommandsRepairMissingMailbox(t *testing.T) {
	t.Run("aggregate list", func(t *testing.T) {
		p, _, manifest := createWorkerFixture(t, program.ItemDispatched)
		out, err := runProgramCommand(t, "message", "list", p.Slug, "--json")
		if err != nil {
			t.Fatalf("message list: %v", err)
		}
		var got programMessageListOutput
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("decode output %q: %v", out, err)
		}
		want := programMessageListOutput{
			Messages: []programMessageOutput{},
			Warnings: []programItemWarning{},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("message list = %#v, want %#v", got, want)
		}
		assertMailboxLayout(t, messageProjectDir(manifest))
	})

	t.Run("inbox", func(t *testing.T) {
		p, item, manifest := createWorkerFixture(t, program.ItemDispatched)
		out, err := runProgramCommand(t, "message", "inbox", p.Slug, item.ID, "--json")
		if err != nil {
			t.Fatalf("message inbox: %v", err)
		}
		var got []programMessageOutput
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("decode output %q: %v", out, err)
		}
		if want := []programMessageOutput{}; !reflect.DeepEqual(got, want) {
			t.Fatalf("message inbox = %#v, want %#v", got, want)
		}
		assertMailboxLayout(t, messageProjectDir(manifest))
	})
}

func TestProgramMessageRejectsPendingLinkedItem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	p, err := program.New("governance", "Ship governed changes", repo, "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "Linked pending work", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	childSlug := "governance-" + item.ID
	if err := p.LinkItem(item.ID, childSlug); err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(repo, ".worktrees", childSlug)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := project.Manifest{
		Slug: childSlug, Repo: repo, Worktree: &worktree,
		Program: p.Slug, ProgramItem: item.ID,
	}
	manifestPath := project.ManifestPath(project.ActiveDir(), childSlug)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}

	_, err = runProgramCommand(t, "message", "inbox", p.Slug, item.ID, "--json")
	if err == nil || !strings.Contains(err.Error(), `status is "pending"`) {
		t.Fatalf("message inbox error = %v", err)
	}
}

func TestProgramMessageListAggregatesLinkedNonterminalOutboxes(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	second, err := p.AddItem(program.WorkItem{Title: "Second item", Priority: program.PriorityP2})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(second.ID, "governance-"+second.ID); err != nil {
		t.Fatal(err)
	}
	second, _ = p.Item(second.ID)
	secondManifest := saveMessageChild(t, p, second)
	canceled, err := p.AddItem(program.WorkItem{Title: "Canceled item", Priority: program.PriorityP3})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(canceled.ID, "governance-"+canceled.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.CancelItem(canceled.ID, "no longer needed"); err != nil {
		t.Fatal(err)
	}
	canceled, _ = p.Item(canceled.ID)
	canceledManifest := saveMessageChild(t, p, canceled)
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}

	firstMessage, err := mailbox.Send(messageProjectDir(manifest), mailbox.Outbox, mailbox.Message{
		ID: "later", Kind: mailbox.KindQuestion, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorWorker, To: mailbox.ActorCTO, Body: "Later",
		CreatedAt: "2026-08-24T20:00:01Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondMessage, err := mailbox.Send(messageProjectDir(secondManifest), mailbox.Outbox, mailbox.Message{
		ID: "earlier", Kind: mailbox.KindPlan, Program: p.Slug, Item: second.ID,
		From: mailbox.ActorWorker, To: mailbox.ActorCTO, Body: "Earlier",
		CreatedAt: "2026-08-24T20:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mailbox.Send(messageProjectDir(canceledManifest), mailbox.Outbox, mailbox.Message{
		ID: "ignored", Kind: mailbox.KindConflict, Program: p.Slug, Item: canceled.ID,
		From: mailbox.ActorWorker, To: mailbox.ActorCTO, Body: "Ignored",
		CreatedAt: "2026-08-24T19:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "message", "list", p.Slug, "--json")
	if err != nil {
		t.Fatalf("message list: %v", err)
	}
	var got programMessageListOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	want := programMessageListOutput{
		Messages: []programMessageOutput{
			newProgramMessageOutput(second, secondManifest, secondMessage),
			newProgramMessageOutput(item, manifest, firstMessage),
		},
		Warnings: []programItemWarning{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list:\n got: %#v\nwant: %#v", got, want)
	}
	assertProgramBytes(t, p.Slug, before)
}

func TestProgramMessageListContinuesPastUnavailableItemsAndSkipsPendingLinks(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	message, err := mailbox.Send(messageProjectDir(manifest), mailbox.Outbox, mailbox.Message{
		ID: "question-1", Kind: mailbox.KindQuestion, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorWorker, To: mailbox.ActorCTO, Body: "Which API?",
		CreatedAt: "2026-08-24T20:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	missing, err := p.AddItem(program.WorkItem{Title: "Missing child", Priority: program.PriorityP2})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(missing.ID, "governance-"+missing.ID); err != nil {
		t.Fatal(err)
	}
	archived, err := p.AddItem(program.WorkItem{Title: "Archived child", Priority: program.PriorityP2})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(archived.ID, "governance-"+archived.ID); err != nil {
		t.Fatal(err)
	}
	saveArchivedWorkerChild(t, p, archived)
	pending, err := p.AddItem(program.WorkItem{Title: "Linked but pending", Priority: program.PriorityP3})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.LinkItem(pending.ID, "governance-"+pending.ID); err != nil {
		t.Fatal(err)
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "message", "list", p.Slug, "--json")
	if err != nil {
		t.Fatalf("message list: %v", err)
	}
	var got struct {
		Messages []programMessageOutput `json:"messages"`
		Warnings []struct {
			Item    string `json:"item"`
			Project string `json:"project"`
			Error   string `json:"error"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	wantMessages := []programMessageOutput{newProgramMessageOutput(item, manifest, message)}
	if !reflect.DeepEqual(got.Messages, wantMessages) {
		t.Fatalf("messages = %#v, want %#v", got.Messages, wantMessages)
	}
	if len(got.Warnings) != 2 || got.Warnings[0].Item != missing.ID ||
		got.Warnings[0].Project != "governance-"+missing.ID ||
		!strings.Contains(got.Warnings[0].Error, "is not active") ||
		got.Warnings[1].Item != archived.ID ||
		got.Warnings[1].Project != "governance-"+archived.ID ||
		!strings.Contains(got.Warnings[1].Error, "is not active") {
		t.Fatalf("warnings = %#v", got.Warnings)
	}
	text, err := runProgramCommand(t, "message", "list", p.Slug)
	if err != nil {
		t.Fatalf("message list text: %v", err)
	}
	if !strings.Contains(text, message.ID) || !strings.Contains(text, "Warning: "+missing.ID) {
		t.Fatalf("message list text = %q", text)
	}
}

func TestProgramMessageInboxIsReadOnly(t *testing.T) {
	p, item, manifest, before := createMessageFixture(t)
	message, err := mailbox.Send(messageProjectDir(manifest), mailbox.Inbox, mailbox.Message{
		ID: "decision-1", Kind: mailbox.KindDecision, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorCTO, To: mailbox.ActorWorker, Body: "Use the adapter.",
		ReplyTo: "question-1", DecisionID: "d1", CreatedAt: "2026-08-24T20:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "message", "inbox", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("message inbox: %v", err)
	}
	var got []programMessageOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	want := []programMessageOutput{newProgramMessageOutput(item, manifest, message)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inbox:\n got: %#v\nwant: %#v", got, want)
	}
	assertProgramBytes(t, p.Slug, before)
}

func TestProgramMessageOutboxIsReadOnly(t *testing.T) {
	p, item, manifest, before := createMessageFixture(t)
	message, err := mailbox.Send(messageProjectDir(manifest), mailbox.Outbox, mailbox.Message{
		ID: "pr-open-1", Kind: mailbox.KindPROpen, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorWorker, To: mailbox.ActorCTO, Body: "Ready for PR.",
		CreatedAt: "2026-08-24T20:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "message", "outbox", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("message outbox: %v", err)
	}
	var got []programMessageOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	want := []programMessageOutput{newProgramMessageOutput(item, manifest, message)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outbox:\n got: %#v\nwant: %#v", got, want)
	}
	assertProgramBytes(t, p.Slug, before)
	if unread, err := mailbox.List(messageProjectDir(manifest), mailbox.Outbox); err != nil || len(unread) != 1 {
		t.Fatalf("outbox after read = %#v, %v", unread, err)
	}
}

func TestProgramMessageReplyWritesInboxThenAcknowledgesOutbox(t *testing.T) {
	p, item, manifest, before := createMessageFixture(t)
	outbox, err := mailbox.Send(messageProjectDir(manifest), mailbox.Outbox, mailbox.Message{
		ID: "question-1", Kind: mailbox.KindQuestion, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorWorker, To: mailbox.ActorCTO, Body: "Which API?",
		CreatedAt: "2026-08-24T20:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "message", "reply", p.Slug, item.ID, outbox.ID,
		"--kind", "decision", "--body", "Use the adapter.", "--decision", "d2")
	if err != nil {
		t.Fatalf("message reply: %v", err)
	}
	replyID := strings.TrimSpace(out)
	inbox, err := mailbox.List(messageProjectDir(manifest), mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	want := []mailbox.Message{{
		ID: replyID, Kind: mailbox.KindDecision, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorCTO, To: mailbox.ActorWorker, Body: "Use the adapter.",
		Options: []string{}, ReplyTo: outbox.ID, DecisionID: "d2", CreatedAt: inbox[0].CreatedAt,
	}}
	if !reflect.DeepEqual(inbox, want) {
		t.Fatalf("inbox:\n got: %#v\nwant: %#v", inbox, want)
	}
	if unread, err := mailbox.List(messageProjectDir(manifest), mailbox.Outbox); err != nil || len(unread) != 0 {
		t.Fatalf("outbox after reply = %#v, %v", unread, err)
	}
	assertProgramBytes(t, p.Slug, before)
}

func TestProgramMessageNotifyWritesUnsolicitedInbox(t *testing.T) {
	p, item, manifest, before := createMessageFixture(t)

	out, err := runProgramCommand(t, "message", "notify", p.Slug, item.ID,
		"--kind", "instruction", "--body", "Rebase before continuing.")
	if err != nil {
		t.Fatalf("message notify: %v", err)
	}
	id := strings.TrimSpace(out)
	inbox, err := mailbox.List(messageProjectDir(manifest), mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].ID != id || inbox[0].Kind != mailbox.KindInstruction ||
		inbox[0].ReplyTo != "" || inbox[0].Body != "Rebase before continuing." {
		t.Fatalf("inbox = %#v", inbox)
	}
	assertProgramBytes(t, p.Slug, before)
}

func TestProgramMessageAckMovesWorkerInboxWithoutProgramWrite(t *testing.T) {
	p, item, manifest, before := createMessageFixture(t)
	message, err := mailbox.Send(messageProjectDir(manifest), mailbox.Inbox, mailbox.Message{
		ID: "feedback-1", Kind: mailbox.KindFeedback, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorCTO, To: mailbox.ActorWorker, Body: "Add the edge case.",
		CreatedAt: "2026-08-24T20:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "message", "ack", p.Slug, item.ID, message.ID)
	if err != nil {
		t.Fatalf("message ack: %v", err)
	}
	if strings.TrimSpace(out) != message.ID {
		t.Fatalf("ack output = %q", out)
	}
	if unread, err := mailbox.List(messageProjectDir(manifest), mailbox.Inbox); err != nil || len(unread) != 0 {
		t.Fatalf("inbox after ack = %#v, %v", unread, err)
	}
	assertProgramBytes(t, p.Slug, before)
}

func TestProgramMessageReplyAckFailurePreservesInspectableInbox(t *testing.T) {
	p, item, manifest, before := createMessageFixture(t)
	projectDir := messageProjectDir(manifest)
	outbox, err := mailbox.Send(projectDir, mailbox.Outbox, mailbox.Message{
		ID: "question-1", Kind: mailbox.KindQuestion, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorWorker, To: mailbox.ActorCTO, Body: "Which API?",
		CreatedAt: "2026-08-24T20:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(projectDir, "mail", "outbox", outbox.ID+".json")
	target := filepath.Join(projectDir, "mail", "processed", "outbox", outbox.ID+".json")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = runProgramCommand(t, "message", "reply", p.Slug, item.ID, outbox.ID,
		"--body", "Use the adapter.")
	if err == nil || !strings.Contains(err.Error(), "inbox message") ||
		!strings.Contains(err.Error(), "relay program message inbox "+p.Slug+" "+item.ID+" --json") {
		t.Fatalf("reply error = %v", err)
	}
	inbox, listErr := mailbox.List(projectDir, mailbox.Inbox)
	if listErr != nil || len(inbox) != 1 || inbox[0].ReplyTo != outbox.ID {
		t.Fatalf("inspectable inbox = %#v, %v", inbox, listErr)
	}
	if _, findErr := mailbox.Find(projectDir, mailbox.Outbox, outbox.ID); findErr != nil {
		t.Fatalf("outbox was removed after ack failure: %v", findErr)
	}
	assertProgramBytes(t, p.Slug, before)
}

func saveMessageChild(t *testing.T, p program.Program, item program.WorkItem) project.Manifest {
	t.Helper()
	worktree := filepath.Join(p.Repo, ".worktrees", "message-"+item.ID)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := project.Manifest{
		Slug: "governance-" + item.ID, Title: item.Title, Repo: p.Repo,
		Worktree: &worktree, Program: p.Slug, ProgramItem: item.ID,
	}
	path := project.ManifestPath(project.ActiveDir(), manifest.Slug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(path, manifest); err != nil {
		t.Fatal(err)
	}
	if err := mailbox.Ensure(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func messageProjectDir(manifest project.Manifest) string {
	return filepath.Dir(project.ManifestPath(project.ActiveDir(), manifest.Slug))
}

func assertMailboxLayout(t *testing.T, projectDir string) {
	t.Helper()
	for _, relative := range []string{
		"mail/inbox",
		"mail/outbox",
		"mail/notified",
		"mail/processed/inbox",
		"mail/processed/outbox",
	} {
		info, err := os.Stat(filepath.Join(projectDir, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("mailbox directory %s: %v", relative, err)
		}
		if !info.IsDir() {
			t.Fatalf("mailbox path %s is not a directory", relative)
		}
	}
}
