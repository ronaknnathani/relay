package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/program"
)

func TestProgramDecisionOpenIsIdempotentAndDoesNotDuplicateTheDecisionLog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := createCLIProgram(t, "governance")

	first, err := runProgramCommand(t, "decision", "open", p.Slug,
		"--question", "Which rollout strategy?", "--options", "canary|all-at-once")
	if err != nil {
		t.Fatal(err)
	}
	second, err := runProgramCommand(t, "decision", "open", p.Slug,
		"--question", "  which   ROLLOUT strategy? ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(first) != strings.TrimSpace(second) {
		t.Fatalf("decision ids = %q and %q, want the same reused decision", first, second)
	}

	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Decisions) != 1 {
		t.Fatalf("decisions = %d, want 1", len(loaded.Decisions))
	}
	log, err := os.ReadFile(program.DecisionLogPath(program.ProgramDir(program.ActiveDir(), p.Slug)))
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(log), "Which rollout strategy?"); count != 1 {
		t.Fatalf("decision log recorded the question %d times:\n%s", count, log)
	}
}

// Every CEO-only mutation must fail closed inside a bounded automated turn. The
// automated CTO may raise decisions; it may never answer them on the CEO's
// behalf or move the program through an approval gate.
func TestCEOOnlyCommandsAreBlockedDuringAnAutomatedTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(agent.AutomatedTurnEnvVar, "1")
	p := createCLIProgram(t, "governance")

	blocked := [][]string{
		{"submit", p.Slug},
		{"approve", p.Slug},
		{"finish", p.Slug},
		{"abandon", p.Slug, "--reason", "no longer needed"},
		{"set-max-open-prs", p.Slug, "2"},
		{"contract", "approve", p.Slug, "api@v1"},
		{"contract", "reject", p.Slug, "api@v1", "--reason", "bad"},
		{"decision", "resolve", p.Slug, "d1", "--answer", "yes"},
	}
	for _, args := range blocked {
		out, err := runProgramCommand(t, args...)
		if err == nil {
			t.Fatalf("%v succeeded during an automated turn: %s", args, out)
		}
		message := err.Error()
		for _, want := range []string{agent.AutomatedTurnEnvVar, "CEO"} {
			if !strings.Contains(message, want) {
				t.Errorf("%v error %q is missing %q", args, message, want)
			}
		}
	}

	// Plan shaping is a CEO conversation, not routine automated governance.
	for _, args := range [][]string{
		{"item", "add", p.Slug, "New scope"},
		{"item", "update", p.Slug, "w1", "--priority", "P0"},
		{"item", "cancel", p.Slug, "w1", "--reason", "dropped"},
	} {
		out, err := runProgramCommand(t, args...)
		if err == nil {
			t.Fatalf("%v reshaped the plan during an automated turn: %s", args, out)
		}
		for _, want := range []string{agent.AutomatedTurnEnvVar, "reshapes the program plan"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v error %q is missing %q", args, err, want)
			}
		}
	}

	// The routine actions a bounded turn is allowed to take still work.
	if _, err := runProgramCommand(t, "decision", "open", p.Slug, "--question", "Which rollout?"); err != nil {
		t.Fatalf("decision open was blocked during an automated turn: %v", err)
	}
	if _, err := runProgramCommand(t, "status", p.Slug, "--json"); err != nil {
		t.Fatalf("status was blocked during an automated turn: %v", err)
	}
	if _, err := runProgramCommand(t, "queue", p.Slug, "--json"); err != nil {
		t.Fatalf("queue was blocked during an automated turn: %v", err)
	}
}

func TestCEOOnlyCommandsRunNormallyOutsideAnAutomatedTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := createCLIProgram(t, "governance")
	if _, err := runProgramCommand(t, "set-max-open-prs", p.Slug, "2", "--by", "ceo"); err != nil {
		t.Fatalf("set-max-open-prs outside an automated turn: %v", err)
	}
	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MaxOpenPRs != 2 {
		t.Fatalf("max open PRs = %d, want 2", loaded.MaxOpenPRs)
	}
}

const (
	testAutomatedSessionID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	testAutomatedActor     = "cto-automated:3f2504e0"
	testAutomatedNote      = "[automated CTO turn 3f2504e0, on behalf of CEO]"
)

func startAutomatedTurn(t *testing.T, sessionID string) {
	t.Helper()
	t.Setenv(agent.AutomatedTurnEnvVar, "1")
	t.Setenv(agent.AutomatedTurnSessionEnvVar, sessionID)
}

// Everything a bounded automated turn writes durably must read as automated.
// A CEO scanning decisions.md, progress.md, or a worker inbox has to be able to
// tell an unattended turn from the human CTO without opening a transcript.
func TestAutomatedTurnStampsEveryDurableEntryItCanWrite(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	t.Setenv("HERDR_ENV", "")
	projectDir := messageProjectDir(manifest)
	startAutomatedTurn(t, testAutomatedSessionID)

	// A flag can never make an automated turn sign as a human.
	out, err := runProgramCommand(t, "grant-open-pr", p.Slug, item.ID, "--by", "ceo", "--json")
	if err != nil {
		t.Fatalf("grant-open-pr: %v\n%s", err, out)
	}
	var grant programOpenPRGrantOutput
	if err := json.Unmarshal([]byte(out), &grant); err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCommand(t, "decision", "open", p.Slug,
		"--question", "Which rollout strategy?", "--raised-by", "worker"); err != nil {
		t.Fatalf("decision open: %v", err)
	}
	if grant.GrantedBy != testAutomatedActor {
		t.Errorf("granted_by = %q, want %q", grant.GrantedBy, testAutomatedActor)
	}

	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Decisions) != 1 || loaded.Decisions[0].RaisedBy != program.RaisedBy(testAutomatedActor) {
		t.Fatalf("decisions = %+v, want raised_by %q", loaded.Decisions, testAutomatedActor)
	}
	granted, _ := loaded.Item(item.ID)
	if granted.PRGrantedBy != testAutomatedActor {
		t.Errorf("durable pr_granted_by = %q, want %q", granted.PRGrantedBy, testAutomatedActor)
	}

	programDir := program.ProgramDir(program.ActiveDir(), p.Slug)
	assertFileContains(t, program.DecisionLogPath(programDir),
		"Opened decision d1: Which rollout strategy? "+testAutomatedNote)
	assertFileContains(t, program.ProgressPath(programDir),
		"Granted open-PR capacity to item "+item.ID+" by "+testAutomatedActor+" "+testAutomatedNote)

	inbox, err := mailbox.List(projectDir, mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Fatalf("inbox = %#v, want one grant message", inbox)
	}
	if inbox[0].AutomatedBy != testAutomatedActor {
		t.Errorf("inbox automated_by = %q, want %q", inbox[0].AutomatedBy, testAutomatedActor)
	}
	if !strings.HasSuffix(inbox[0].Body, testAutomatedNote) {
		t.Errorf("inbox body %q does not end with %q", inbox[0].Body, testAutomatedNote)
	}
	if inbox[0].From != mailbox.ActorCTO || inbox[0].To != mailbox.ActorWorker {
		t.Errorf("inbox routing = %q -> %q, want cto -> worker", inbox[0].From, inbox[0].To)
	}

	// Replies, unsolicited notifications, revocations, and dispatch-side
	// progress carry the same attribution.
	request, err := mailbox.Send(projectDir, mailbox.Outbox, mailbox.Message{
		Kind: mailbox.KindQuestion, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorWorker, To: mailbox.ActorCTO, Body: "Which API?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCommand(t, "message", "reply", p.Slug, item.ID, request.ID,
		"--kind", "feedback", "--body", "Use the v2 API."); err != nil {
		t.Fatalf("message reply: %v", err)
	}
	if _, err := runProgramCommand(t, "message", "notify", p.Slug, item.ID,
		"--kind", "instruction", "--body", "Rebase first."); err != nil {
		t.Fatalf("message notify: %v", err)
	}
	if _, err := runProgramCommand(t, "revoke-open-pr", p.Slug, item.ID,
		"--by", "ceo", "--reason", "capacity is needed elsewhere"); err != nil {
		t.Fatalf("revoke-open-pr: %v", err)
	}
	inbox, err = mailbox.List(projectDir, mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 4 {
		t.Fatalf("inbox = %d messages, want 4", len(inbox))
	}
	for _, message := range inbox {
		if message.AutomatedBy != testAutomatedActor || !strings.HasSuffix(message.Body, testAutomatedNote) {
			t.Errorf("message %s = %+v, want automated attribution", message.ID, message)
		}
	}
	assertFileContains(t, program.ProgressPath(programDir),
		"Revoked open-PR capacity for item "+item.ID+" by "+testAutomatedActor+
			": capacity is needed elsewhere "+testAutomatedNote)
}

// A worker message an automated turn writes must not look like a human worker.
func TestAutomatedTurnStampsWorkerOutboxMessages(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	startAutomatedTurn(t, testAutomatedSessionID)

	if _, err := runProgramCommand(t, "message", "send", p.Slug, item.ID,
		"--kind", "question", "--body", "Which API?"); err != nil {
		t.Fatalf("message send: %v", err)
	}
	outbox, err := mailbox.List(messageProjectDir(manifest), mailbox.Outbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox) != 1 {
		t.Fatalf("outbox = %#v", outbox)
	}
	if outbox[0].AutomatedBy != testAutomatedActor ||
		outbox[0].Body != "Which API? "+testAutomatedNote {
		t.Fatalf("outbox message = %+v, want automated attribution", outbox[0])
	}
}

// Human CTO and CEO commands are untouched: attribution appears only when the
// process really is a bounded automated turn.
func TestHumanTurnsRecordNoAutomatedAttribution(t *testing.T) {
	p, item, manifest, _ := createMessageFixture(t)
	t.Setenv("HERDR_ENV", "")

	if _, err := runProgramCommand(t, "grant-open-pr", p.Slug, item.ID, "--by", "ceo", "--json"); err != nil {
		t.Fatalf("grant-open-pr: %v", err)
	}
	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	granted, _ := loaded.Item(item.ID)
	if granted.PRGrantedBy != "ceo" {
		t.Errorf("pr_granted_by = %q, want ceo", granted.PRGrantedBy)
	}
	progress, err := os.ReadFile(program.ProgressPath(program.ProgramDir(program.ActiveDir(), p.Slug)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(progress), "automated CTO turn") {
		t.Errorf("human progress carries automated attribution:\n%s", progress)
	}
	inbox, err := mailbox.List(messageProjectDir(manifest), mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].AutomatedBy != "" ||
		strings.Contains(inbox[0].Body, "automated CTO turn") {
		t.Fatalf("human inbox message = %+v", inbox)
	}
}

// The identity is derived from the exported session id, never from a flag, and
// it degrades to a still-automated identity when the id is missing.
func TestAutomatedActorUsesAShortSessionPrefix(t *testing.T) {
	for _, test := range []struct {
		name      string
		automated string
		session   string
		want      string
		wantOK    bool
	}{
		{name: "uuid", automated: "1", session: testAutomatedSessionID, want: testAutomatedActor, wantOK: true},
		{name: "short", automated: "1", session: "ab12", want: "cto-automated:ab12", wantOK: true},
		{name: "unsafe", automated: "1", session: "../../escape", want: "cto-automated:escape", wantOK: true},
		{name: "missing", automated: "1", session: "", want: "cto-automated:unknown", wantOK: true},
		{name: "human", automated: "", session: testAutomatedSessionID, want: "", wantOK: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(agent.AutomatedTurnEnvVar, test.automated)
			t.Setenv(agent.AutomatedTurnSessionEnvVar, test.session)
			actor, ok := automatedActor()
			if actor != test.want || ok != test.wantOK {
				t.Fatalf("automatedActor() = %q, %t, want %q, %t", actor, ok, test.want, test.wantOK)
			}
			entry := attributeProgramEntry("Opened decision d1")
			if !test.wantOK {
				if entry != "Opened decision d1" {
					t.Fatalf("human entry = %q", entry)
				}
				return
			}
			prefix := strings.TrimPrefix(test.want, "cto-automated:")
			want := "Opened decision d1 [automated CTO turn " + prefix + ", on behalf of CEO]"
			if entry != want {
				t.Fatalf("entry = %q, want %q", entry, want)
			}
			if len(prefix) > 12 {
				t.Fatalf("published session prefix %q is longer than 12 characters", prefix)
			}
			if strings.Contains(entry, testAutomatedSessionID) {
				t.Fatalf("entry %q exposes the full session id", entry)
			}
		})
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), want) {
		t.Errorf("%s does not contain %q:\n%s", path, want, data)
	}
}

// Pausing and resuming a program is a CEO judgment about whether the work
// should continue at all. A bounded automated turn may recommend it, but it may
// never stop or restart a program while nobody is watching.
func TestProgramHoldAndReleaseAreBlockedDuringAnAutomatedTurn(t *testing.T) {
	p := createPatrolProgram(t, "governance", program.StateActive)
	programDir := program.ProgramDir(program.ActiveDir(), p.Slug)
	before, err := os.ReadFile(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(agent.AutomatedTurnEnvVar, "1")

	for _, args := range [][]string{
		{"hold", p.Slug, "--reason", "waiting on the CEO"},
		// Identity fails closed before flag validation: a missing --reason must
		// not mask why the command is refused.
		{"hold", p.Slug},
		{"release", p.Slug},
	} {
		out, err := runProgramCommand(t, args...)
		if err == nil {
			t.Fatalf("%v succeeded during an automated turn: %s", args, out)
		}
		for _, want := range []string{agent.AutomatedTurnEnvVar, "CEO"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v error %q is missing %q", args, err, want)
			}
		}
	}

	assertProgramBytes(t, p.Slug, before)
	if data := readOptionalProgramFile(t, program.ProgressPath(programDir)); data != nil {
		t.Errorf("a blocked hold/release wrote progress:\n%s", data)
	}
}

func TestProgramHoldAndReleaseRunNormallyOutsideAnAutomatedTurn(t *testing.T) {
	p := createPatrolProgram(t, "governance", program.StateActive)

	if _, err := runProgramCommand(t, "hold", p.Slug, "--reason", "waiting on the CEO"); err != nil {
		t.Fatalf("hold outside an automated turn: %v", err)
	}
	if state := loadProgramState(t, p.Slug); state != program.StateHeld {
		t.Fatalf("state after hold = %q, want %q", state, program.StateHeld)
	}
	if _, err := runProgramCommand(t, "release", p.Slug); err != nil {
		t.Fatalf("release outside an automated turn: %v", err)
	}
	if state := loadProgramState(t, p.Slug); state != program.StateActive {
		t.Fatalf("state after release = %q, want %q", state, program.StateActive)
	}
}

// Publishing an immutable contract version shapes the plan and immediately
// opens a CEO approval decision. It is governance the CEO must witness, so a
// bounded automated turn must not even read the source file.
func TestProgramContractPublishIsBlockedDuringAnAutomatedTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := createCLIProgram(t, "governance")
	source := filepath.Join(p.Repo, "api-contract.md")
	if err := os.WriteFile(source, []byte("# API contract\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	programDir := program.ProgramDir(program.ActiveDir(), p.Slug)
	before, err := os.ReadFile(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(agent.AutomatedTurnEnvVar, "1")

	out, err := runProgramCommand(t, "contract", "publish", p.Slug, "Public API", "--file", source)
	if err == nil {
		t.Fatalf("contract publish succeeded during an automated turn: %s", out)
	}
	for _, want := range []string{agent.AutomatedTurnEnvVar, "reshapes the program plan"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("contract publish error %q is missing %q", err, want)
		}
	}

	assertProgramBytes(t, p.Slug, before)
	if entries, err := os.ReadDir(filepath.Join(programDir, "contracts")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("blocked publish created contract files: %v, %v", entries, err)
	}
	for _, path := range []string{
		program.DecisionLogPath(programDir),
		program.ProgressPath(programDir),
	} {
		if data := readOptionalProgramFile(t, path); data != nil {
			t.Errorf("blocked publish wrote %s:\n%s", path, data)
		}
	}
}

func TestProgramContractPublishRunsNormallyOutsideAnAutomatedTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := createCLIProgram(t, "governance")
	source := filepath.Join(p.Repo, "api-contract.md")
	if err := os.WriteFile(source, []byte("# API contract\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runProgramCommand(t, "contract", "publish", p.Slug, "Public API", "--file", source)
	if err != nil {
		t.Fatalf("contract publish outside an automated turn: %v", err)
	}
	if fields := strings.Fields(out); len(fields) != 2 || fields[0] != "public-api@v1" {
		t.Fatalf("publish output = %q", out)
	}
}

// Attribution is metadata, not content. An empty or whitespace-only body must
// stay invalid inside a bounded automated turn: appending the automated note
// would fabricate a message the CTO never wrote and slip it past validation.
func TestAutomatedAttributionNeverTurnsAnEmptyBodyIntoAMessage(t *testing.T) {
	p, item, manifest, before := createMessageFixture(t)
	projectDir := messageProjectDir(manifest)
	request, err := mailbox.Send(projectDir, mailbox.Outbox, mailbox.Message{
		Kind: mailbox.KindQuestion, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorWorker, To: mailbox.ActorCTO, Body: "Which API?",
	})
	if err != nil {
		t.Fatal(err)
	}
	startAutomatedTurn(t, testAutomatedSessionID)

	for _, args := range [][]string{
		{"message", "reply", p.Slug, item.ID, request.ID, "--kind", "feedback", "--body", ""},
		{"message", "reply", p.Slug, item.ID, request.ID, "--kind", "feedback", "--body", "   "},
		{"message", "notify", p.Slug, item.ID, "--kind", "instruction", "--body", " \t\n "},
		{"message", "send", p.Slug, item.ID, "--kind", "question", "--body", "  "},
	} {
		out, err := runProgramCommand(t, args...)
		if err == nil {
			t.Fatalf("%v wrote a message with an empty body: %s", args, out)
		}
		if !strings.Contains(err.Error(), "message body is required") {
			t.Errorf("%v error %q is missing the empty-body rejection", args, err)
		}
	}

	inbox, err := mailbox.List(projectDir, mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 0 {
		t.Fatalf("inbox = %#v, want no message", inbox)
	}
	outbox, err := mailbox.List(projectDir, mailbox.Outbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox) != 1 || outbox[0].ID != request.ID {
		t.Fatalf("outbox = %#v, want only the unanswered worker question", outbox)
	}
	assertProgramBytes(t, p.Slug, before)
	if entry := attributeProgramEntry("   "); entry != "   " {
		t.Errorf("attributeProgramEntry(%q) = %q, want it unchanged", "   ", entry)
	}

	// A real body still carries exact attribution.
	if _, err := runProgramCommand(t, "message", "reply", p.Slug, item.ID, request.ID,
		"--kind", "feedback", "--body", "Use the v2 API."); err != nil {
		t.Fatalf("message reply: %v", err)
	}
	inbox, err = mailbox.List(projectDir, mailbox.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Body != "Use the v2 API. "+testAutomatedNote ||
		inbox[0].AutomatedBy != testAutomatedActor {
		t.Fatalf("inbox = %#v, want one exactly attributed reply", inbox)
	}
}

func loadProgramState(t *testing.T, slug string) program.State {
	t.Helper()
	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	return loaded.State
}

// readOptionalProgramFile returns nil when the durable file was never written,
// so a test can assert that a blocked command left no trace at all.
func readOptionalProgramFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return data
}
