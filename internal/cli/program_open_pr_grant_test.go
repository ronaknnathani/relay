package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/project"
)

func TestProgramGrantOpenPRPersistsThenRepliesToOldestRequest(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	t.Setenv("HERDR_ENV", "")
	projectDir := messageProjectDir(manifest)
	for _, message := range []mailbox.Message{
		{
			ID: "plan-1", Kind: mailbox.KindPlan, Program: p.Slug, Item: item.ID,
			From: mailbox.ActorWorker, To: mailbox.ActorTL, Body: "Review plan.",
			CreatedAt: "2026-08-24T20:00:00Z",
		},
		{
			ID: "pr-open-1", Kind: mailbox.KindPROpen, Program: p.Slug, Item: item.ID,
			From: mailbox.ActorWorker, To: mailbox.ActorTL, Body: "Ready for PR.",
			CreatedAt: "2026-08-24T20:00:01Z",
		},
		{
			ID: "pr-open-2", Kind: mailbox.KindPROpen, Program: p.Slug, Item: item.ID,
			From: mailbox.ActorWorker, To: mailbox.ActorTL, Body: "Still ready.",
			CreatedAt: "2026-08-24T20:00:02Z",
		},
	} {
		if _, err := mailbox.Send(projectDir, mailbox.Outbox, message); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runProgramCommand(t, "grant-open-pr", p.Slug, item.ID, "--by", "tl", "--json")
	if err != nil {
		t.Fatalf("grant-open-pr: %v\n%s", err, out)
	}
	var got programOpenPRGrantOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	if got.Program != p.Slug || got.Item != item.ID || got.GrantedBy != "tl" ||
		got.GrantedAt == "" || got.MessageReplyTo != "pr-open-1" ||
		got.Capacity != (program.Capacity{Limit: 3, Reserved: 1, Available: 2}) {
		t.Fatalf("output = %+v", got)
	}

	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	granted, _ := loaded.Item(item.ID)
	if granted.PRGrantedAt != got.GrantedAt || granted.PRGrantedBy != got.GrantedBy {
		t.Fatalf("persisted grant = %+v, output = %+v", granted, got)
	}
	inbox, err := mailbox.List(projectDir, mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].ID != got.MessageID ||
		inbox[0].Kind != mailbox.KindInstruction || inbox[0].ReplyTo != "pr-open-1" ||
		!strings.Contains(inbox[0].Body, "relay program can-open-pr "+p.Slug+" "+item.ID) ||
		!strings.Contains(inbox[0].Body, "only after") {
		t.Fatalf("inbox = %#v", inbox)
	}
	outbox, err := mailbox.List(projectDir, mailbox.Outbox)
	if err != nil {
		t.Fatal(err)
	}
	if ids := []string{outbox[0].ID, outbox[1].ID}; !reflect.DeepEqual(ids, []string{"plan-1", "pr-open-2"}) {
		t.Fatalf("unread outbox IDs = %v", ids)
	}
	progress, err := os.ReadFile(program.ProgressPath(filepath.Dir(program.ManifestPath(program.ActiveDir(), p.Slug))))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(progress), "Granted open-PR capacity to item "+item.ID) {
		t.Fatalf("progress = %q", progress)
	}
}

func TestProgramGrantOpenPRNotifiesWithoutRequest(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	t.Setenv("HERDR_ENV", "")

	out, err := runProgramCommand(t, "grant-open-pr", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("grant-open-pr: %v\n%s", err, out)
	}
	var got programOpenPRGrantOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	inbox, err := mailbox.List(messageProjectDir(manifest), mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].ID != got.MessageID || inbox[0].ReplyTo != "" {
		t.Fatalf("unsolicited inbox = %#v", inbox)
	}
}

func TestProgramGrantOpenPRMailboxFailureLeavesRepairableGrant(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	t.Setenv("HERDR_ENV", "")
	inboxDir := filepath.Join(messageProjectDir(manifest), "mail", "inbox")
	if err := os.Remove(inboxDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inboxDir, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runProgramCommand(t, "grant-open-pr", p.Slug, item.ID)
	if err == nil || !strings.Contains(err.Error(), "grant exists durably") ||
		!strings.Contains(err.Error(), "relay program message notify "+p.Slug+" "+item.ID) {
		t.Fatalf("grant-open-pr error = %v", err)
	}
	loaded, loadErr := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	granted, _ := loaded.Item(item.ID)
	if granted.PRGrantedAt == "" || granted.PRGrantedBy != "tl" {
		t.Fatalf("repairable grant = %+v", granted)
	}
}

func TestProgramGrantOpenPRRejectsContractTamperBeforeSaving(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "")
	saveProgramTestConfig(t)
	p, item, contract := createDispatchProgram(t, "governance", 3)
	if _, err := runProgramCommand(t, "dispatch", p.Slug, item.ID); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(
		program.ProgramDir(program.ActiveDir(), p.Slug),
		filepath.FromSlash(contract.Path),
	)
	if err := os.Chmod(contractPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contractPath, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runProgramCommand(t, "grant-open-pr", p.Slug, item.ID)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("grant-open-pr error = %v", err)
	}
	loaded, loadErr := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	unchanged, _ := loaded.Item(item.ID)
	if unchanged.PRGrantedAt != "" || unchanged.PRGrantedBy != "" {
		t.Fatalf("tampered grant was saved: %+v", unchanged)
	}
}

func TestProgramGrantOpenPRAckFailureKeepsDurableGrantAndReply(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	t.Setenv("HERDR_ENV", "")
	projectDir := messageProjectDir(manifest)
	request, err := mailbox.Send(projectDir, mailbox.Outbox, mailbox.Message{
		ID: "pr-open-1", Kind: mailbox.KindPROpen, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorWorker, To: mailbox.ActorTL, Body: "Ready for PR.",
		CreatedAt: "2026-08-24T20:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	processed := filepath.Join(projectDir, "mail", "processed", "outbox", request.ID+".json")
	if err := os.WriteFile(processed, []byte("occupied\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "grant-open-pr", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("grant-open-pr: %v\n%s", err, out)
	}
	var got programOpenPRGrantOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.MessageReplyTo != request.ID || len(got.Warnings) != 1 ||
		!strings.Contains(got.Warnings[0], "could not be acknowledged") {
		t.Fatalf("output = %+v", got)
	}
	inbox, err := mailbox.List(projectDir, mailbox.Inbox)
	if err != nil || len(inbox) != 1 || inbox[0].ID != got.MessageID {
		t.Fatalf("durable inbox = %#v, %v", inbox, err)
	}
	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	granted, _ := loaded.Item(item.ID)
	if granted.PRGrantedAt == "" {
		t.Fatalf("grant missing after ack failure: %+v", granted)
	}
}

func TestProgramRevokeOpenPRPersistsAndNotifiesWorker(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	t.Setenv("HERDR_ENV", "")
	if err := p.GrantOpenPR(item.ID, "tl", nil); err != nil {
		t.Fatal(err)
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "revoke-open-pr", p.Slug, item.ID,
		"--by", "tl", "--reason", "capacity reassigned")
	if err != nil {
		t.Fatalf("revoke-open-pr: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Revoked open-PR grant") {
		t.Fatalf("output = %q", out)
	}
	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	revoked, _ := loaded.Item(item.ID)
	if revoked.PRGrantedAt != "" || revoked.PRGrantedBy != "" {
		t.Fatalf("revoked item = %+v", revoked)
	}
	inbox, err := mailbox.List(messageProjectDir(manifest), mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Kind != mailbox.KindInstruction ||
		!strings.Contains(inbox[0].Body, "capacity reassigned") ||
		!strings.Contains(inbox[0].Body, "request another grant") {
		t.Fatalf("revoke inbox = %#v", inbox)
	}
}

func TestProgramRevokeOpenPRBusyDoorbellRemainsPending(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	if err := p.GrantOpenPR(item.ID, "tl", nil); err != nil {
		t.Fatal(err)
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}
	client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{{
		Status: herdr.StatusBlocked, PaneID: "w7:p5",
		TerminalTitle: "relay:" + manifest.Slug, ForegroundCWD: *manifest.Worktree,
	}}}}
	installWorkerFakes(t, client)

	out, err := runProgramCommand(t, "revoke-open-pr", p.Slug, item.ID,
		"--by", "tl", "--reason", "capacity reassigned")
	if err != nil {
		t.Fatalf("revoke-open-pr: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Worker notification: worker is blocked; durable inbox remains pending") ||
		strings.Contains(out, "Warning:") {
		t.Fatalf("output = %q", out)
	}
	if len(client.prompted) != 0 {
		t.Fatalf("busy worker was prompted: %#v", client.prompted)
	}
	inbox, err := mailbox.List(messageProjectDir(manifest), mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	notified, err := mailbox.IsNotified(messageProjectDir(manifest), inbox[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if notified {
		t.Fatal("busy worker inbox was marked notified")
	}
}

func TestProgramGrantOpenPRHerdrDoorbellIsBestEffort(t *testing.T) {
	t.Run("busy worker remains pending", func(t *testing.T) {
		p, item, manifest, _ := createMessageFixture(t)
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_WORKSPACE_ID", "w7")
		client := &fakeHerdrClient{agentResponses: [][]herdr.Agent{{{
			Status: herdr.StatusWorking, PaneID: "w7:p5",
			TerminalTitle: "relay:" + manifest.Slug, ForegroundCWD: *manifest.Worktree,
		}}}}
		installWorkerFakes(t, client)

		out, err := runProgramCommand(t, "grant-open-pr", p.Slug, item.ID, "--json")
		if err != nil {
			t.Fatalf("grant-open-pr: %v\n%s", err, out)
		}
		var got programOpenPRGrantOutput
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if got.WorkerNotification != "worker is working; durable inbox remains pending" ||
			len(got.Warnings) != 0 {
			t.Fatalf("output = %+v", got)
		}
		if len(client.prompted) != 0 {
			t.Fatalf("busy worker was prompted: %#v", client.prompted)
		}
		notified, err := mailbox.IsNotified(messageProjectDir(manifest), got.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		if notified {
			t.Fatal("busy worker inbox was marked notified")
		}
	})

	t.Run("no live worker", func(t *testing.T) {
		p, item, manifest, _ := createMessageFixture(t)
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_WORKSPACE_ID", "w7")
		client := &fakeHerdrClient{}
		installWorkerFakes(t, client)

		out, err := runProgramCommand(t, "grant-open-pr", p.Slug, item.ID, "--json")
		if err != nil {
			t.Fatalf("grant-open-pr: %v\n%s", err, out)
		}
		var got programOpenPRGrantOutput
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got.WorkerNotification, "no live worker") || len(got.Warnings) != 0 {
			t.Fatalf("output = %+v", got)
		}
		notified, err := mailbox.IsNotified(messageProjectDir(manifest), got.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		if notified {
			t.Fatal("no-live-worker inbox was marked notified")
		}
	})

	t.Run("live prompt failure", func(t *testing.T) {
		p, item, manifest, _ := createMessageFixture(t)
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_WORKSPACE_ID", "w7")
		pluginDir := filepath.Join(*manifest.Worktree, "plugin")
		if err := os.MkdirAll(pluginDir, 0o755); err != nil {
			t.Fatal(err)
		}
		client := &fakeHerdrClient{
			agentResponses: [][]herdr.Agent{{{
				Status: herdr.StatusIdle, PaneID: "w7:p5",
				TerminalTitle: "relay:" + manifest.Slug, CWD: manifest.Repo, ForegroundCWD: pluginDir,
			}}},
			promptErr: errors.New("prompt unavailable"),
		}
		installWorkerFakes(t, client)

		out, err := runProgramCommand(t, "grant-open-pr", p.Slug, item.ID, "--json")
		if err != nil {
			t.Fatalf("grant-open-pr: %v\n%s", err, out)
		}
		var got programOpenPRGrantOutput
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if got.WorkerNotification != "not notified" || len(got.Warnings) != 1 ||
			!strings.Contains(got.Warnings[0], "grant and inbox message are durable") ||
			!strings.Contains(got.Warnings[0], "prompt unavailable") {
			t.Fatalf("output = %+v", got)
		}
		notified, notifyErr := mailbox.IsNotified(messageProjectDir(manifest), got.MessageID)
		if notifyErr != nil {
			t.Fatal(notifyErr)
		}
		if notified {
			t.Fatal("inbox was marked notified after prompt failure")
		}
		loaded, loadErr := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		granted, _ := loaded.Item(item.ID)
		if granted.PRGrantedAt == "" {
			t.Fatalf("grant was rolled back: %+v", granted)
		}
	})
}

func TestProgramStatusRendersReservedCapacity(t *testing.T) {
	p, item, _, _ := createMessageFixture(t)
	if err := p.GrantOpenPR(item.ID, "tl", nil); err != nil {
		t.Fatal(err)
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), p.Slug), p); err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "status", p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Capacity: 0/3 open, 1 reserved, 2 available") {
		t.Fatalf("status output = %q", out)
	}
}

func TestClosedPullRequestRecoversThroughTickGrantAndCanOpenPR(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	t.Setenv("HERDR_ENV", "")
	prNumber := 202
	manifest.PR = project.PRInfo{Number: &prNumber}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest); err != nil {
		t.Fatal(err)
	}
	prState := programview.PRStateOpen
	original := buildProgramProjectViews
	buildProgramProjectViews = func(p program.Program) ([]program.ProjectView, []programview.ProjectWarning, error) {
		return programview.ProjectViewsWithPRIndex(p, prIndexStub(func(string) (programview.PRState, bool) {
			return prState, true
		}))
	}
	t.Cleanup(func() { buildProgramProjectViews = original })

	if _, err := runProgramCommand(t, "tick", p.Slug); err != nil {
		t.Fatalf("tick with an open pull request: %v", err)
	}
	inReview, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	reviewed, _ := inReview.Item(item.ID)
	if reviewed.Status != program.ItemInReview || reviewed.PRRef != "#202" {
		t.Fatalf("in-review item = %+v", reviewed)
	}
	if _, err := runProgramCommand(t, "grant-open-pr", p.Slug, item.ID); err == nil {
		t.Fatal("grant succeeded while the pull request was still open")
	}

	prState = programview.PRStateClosed
	if _, err := runProgramCommand(t, "tick", p.Slug); err != nil {
		t.Fatalf("tick after the pull request closed: %v", err)
	}
	recovered, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	recoveredItem, _ := recovered.Item(item.ID)
	if recoveredItem.Status != program.ItemDispatched || recoveredItem.PRRef != "" {
		t.Fatalf("recovered item = %+v", recoveredItem)
	}
	if len(recoveredItem.Notes) != 1 || !strings.Contains(recoveredItem.Notes[0], "#202") {
		t.Fatalf("recovery notes = %#v", recoveredItem.Notes)
	}

	if _, err := runProgramCommand(t, "tick", p.Slug); err != nil {
		t.Fatalf("repeated tick: %v", err)
	}
	repeated, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	repeatedItem, _ := repeated.Item(item.ID)
	if len(repeatedItem.Notes) != 1 {
		t.Fatalf("repeated tick notes = %#v", repeatedItem.Notes)
	}

	grantOut, err := runProgramCommand(t, "grant-open-pr", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("grant-open-pr after the pull request closed: %v\n%s", err, grantOut)
	}
	var grant programOpenPRGrantOutput
	if err := json.Unmarshal([]byte(grantOut), &grant); err != nil {
		t.Fatal(err)
	}
	if grant.Capacity.Open != 0 || grant.Capacity.Reserved != 1 {
		t.Fatalf("grant capacity = %+v", grant.Capacity)
	}

	canOpen, err := runProgramCommand(t, "can-open-pr", p.Slug, item.ID, "--json")
	if err != nil {
		t.Fatalf("can-open-pr after the replacement grant: %v\n%s", err, canOpen)
	}
	var allowed programCanOpenPROutput
	if err := json.Unmarshal([]byte(canOpen), &allowed); err != nil {
		t.Fatal(err)
	}
	if !allowed.Allowed || allowed.Capacity.Reserved != 1 {
		t.Fatalf("can-open-pr = %+v", allowed)
	}
}

type prIndexStub func(string) (programview.PRState, bool)

func (f prIndexStub) Lookup(ref string) (programview.PRState, bool) { return f(ref) }
