package prwatch

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// testDisplayZone is a fixed reader zone. Pane events are rendered where the
// reader is, so the tests inject a zone rather than moving the process into
// one: nothing here touches time.Local.
var testDisplayZone = time.FixedZone("TEST", -4*60*60)

func newTestEventLog() (eventLog, *strings.Builder, *strings.Builder) {
	out := &strings.Builder{}
	errOut := &strings.Builder{}
	return newEventLog(out, errOut, testDisplayZone), out, errOut
}

type failingWriter struct {
	err   error
	after int
	calls int
}

func (w *failingWriter) Write(data []byte) (int, error) {
	w.calls++
	if w.calls > w.after {
		return 0, w.err
	}
	return len(data), nil
}

// checkedDigest is one actionable observation to print a check line from.
func checkedDigest() Digest {
	return Digest{
		PR:          PullRequest{Number: 42, State: "OPEN"},
		Items:       []Item{{Key: "comment:1", Reason: ReasonNewComment}},
		Fingerprint: "1a2b3c4d5e6f7a8b",
	}
}

// One line per event, each opening with the reader's wall clock in brackets and
// an aligned label. The check line is the whole story of one observation: what
// the pull request looks like, the cadence and wall clock the watcher will
// keep, why, and which digest said so.
func TestEventLogFormatsOneLinePerEvent(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, errOut := newTestEventLog()
	if err := log.started(at, "demo", ModeStandalone, "demo", 42); err != nil {
		t.Fatal(err)
	}
	if err := log.check(
		at, "start", checkedDigest(), 900, at.Add(15*time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	if err := log.wake(at.Add(time.Second), "demo", WakeOutcome{
		Kind: WakeDelivered, Owner: "demo", PaneID: "pane-owner", Status: "idle",
	}, "1a2b3c4d5e6f7a8b"); err != nil {
		t.Fatal(err)
	}
	if err := log.complete(at.Add(2*time.Second), "demo", 42); err != nil {
		t.Fatal(err)
	}
	if err := log.stopped(at.Add(3*time.Second), "demo", "context canceled"); err != nil {
		t.Fatal(err)
	}

	want := strings.Join([]string{
		"[2026-09-01 00:45:00 -0400] START project=demo mode=standalone owner=demo pr=#42",
		"[2026-09-01 00:45:00 -0400] CHECK start pr=#42 state=OPEN actionable=1 " +
			"cadence=15m next=01:00:00 reasons=new-comment fp=1a2b3c4d",
		"[2026-09-01 00:45:01 -0400] WAKE  delivered owner=demo pane=pane-owner status=idle " +
			"fp=1a2b3c4d",
		"[2026-09-01 00:45:02 -0400] DONE  project=demo pr=#42 reason=merged",
		"[2026-09-01 00:45:03 -0400] STOP  project=demo reason=context canceled",
		"",
	}, "\n")
	if out.String() != want {
		t.Errorf("operational events =\n%q\nwant\n%q", out.String(), want)
	}
	if errOut.String() != "" {
		t.Errorf("routine events wrote to stderr: %q", errOut.String())
	}
}

// A scheduled check is labeled by its position in the backoff, and everything
// it decided is on that one line. There is no separate event announcing the next
// check, so nothing can print a schedule the rest of the check then changed.
func TestEventLogHasNoStandaloneNextCheckEvent(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, errOut := newTestEventLog()
	if err := log.check(
		at, "n=3", checkedDigest(), 1800, at.Add(30*time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	printed := out.String() + errOut.String()
	if strings.Count(printed, "\n") != 1 {
		t.Errorf("one check printed %d lines:\n%s", strings.Count(printed, "\n"), printed)
	}
	if strings.Contains(printed, "next check at=") {
		t.Errorf("the check printed a standalone next check event:\n%s", printed)
	}
	want := "[2026-09-01 00:45:00 -0400] CHECK n=3 pr=#42 state=OPEN actionable=1 " +
		"cadence=30m next=01:15:00 reasons=new-comment fp=1a2b3c4d\n"
	if out.String() != want {
		t.Errorf("check line = %q, want %q", out.String(), want)
	}
}

// A next check later the same local day needs no date; one on any other day
// carries the whole compact stamp, so a reader never mistakes tomorrow's check
// for one twenty minutes from now.
func TestEventLogPrintsTheNextCheckAgainstTheDayItIsPrintedOn(t *testing.T) {
	for _, test := range []struct {
		name, at, next, want string
	}{
		{
			name: "same local day",
			at:   "2026-09-01T04:45:00Z", next: "2026-09-01T05:45:00Z",
			want: "next=01:45:00",
		},
		{
			name: "next local day",
			at:   "2026-09-01T23:50:00Z", next: "2026-09-02T04:50:00Z",
			want: "next=2026-09-02 00:50:00 -0400",
		},
		{
			// A record written by hand or by a much older build is printed
			// exactly as it was recorded: a pane that blanks a field it cannot
			// read tells the reader less than one that shows it.
			name: "unparsable record",
			at:   "2026-09-01T04:45:00Z", next: "soon",
			want: "next=soon",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, test.at)
			if err != nil {
				t.Fatal(err)
			}
			log, out, _ := newTestEventLog()
			if err := log.check(at, "n=1", checkedDigest(), 3600, test.next); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), " "+test.want+" ") {
				t.Errorf("check line = %q, want %q", out.String(), test.want)
			}
		})
	}
}

// A watch that finished has no next check. It says so by omitting the field,
// never by printing an empty one.
func TestEventLogOmitsAnAbsentNextCheck(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	merged := Digest{PR: PullRequest{Number: 42, State: "MERGED"}, Complete: true}
	log, out, errOut := newTestEventLog()
	if err := log.check(at, "n=2", merged, 900, ""); err != nil {
		t.Fatal(err)
	}
	if err := log.complete(at, "demo", 42); err != nil {
		t.Fatal(err)
	}
	if err := log.stopped(at, "demo", ""); err != nil {
		t.Fatal(err)
	}
	printed := out.String() + errOut.String()
	if strings.Contains(printed, "next=") {
		t.Errorf("an event with no schedule printed a next check:\n%s", printed)
	}
	want := strings.Join([]string{
		"[2026-09-01 00:45:00 -0400] CHECK n=2 pr=#42 state=MERGED actionable=0 " +
			"cadence=15m reasons=none fp=none",
		"[2026-09-01 00:45:00 -0400] DONE  project=demo pr=#42 reason=merged",
		"[2026-09-01 00:45:00 -0400] STOP  project=demo",
		"",
	}, "\n")
	if out.String() != want {
		t.Errorf("events =\n%q\nwant\n%q", out.String(), want)
	}
}

// An undelivered wake carries a WARN label and the command that shows the full
// recorded detail; a failure carries ERROR. Neither embeds its severity in the
// prose after the timestamp, and neither reaches stdout.
func TestEventLogWritesUndeliveredWakesAndErrorsToStderr(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		outcome WakeOutcome
		want    string
	}{
		{
			name: "busy owner",
			outcome: WakeOutcome{
				Kind: WakeOwnerBusy, Owner: "demo", PaneID: "pane-owner", Status: "working",
			},
			want: "WARN  owner-busy owner=demo pane=pane-owner status=working fp=1a2b3c4d",
		},
		{
			name:    "missing owner",
			outcome: WakeOutcome{Kind: WakeOwnerMissing, Owner: "demo"},
			want:    "WARN  owner-missing owner=demo fp=1a2b3c4d",
		},
		{
			name: "duplicated owner",
			outcome: WakeOutcome{
				Kind: WakeOwnerDuplicated, Owner: "demo", Panes: []string{"p1", "p2"},
			},
			want: "WARN  owner-duplicated owner=demo panes=p1,p2 fp=1a2b3c4d",
		},
		{
			name:    "suppressed",
			outcome: WakeOutcome{Kind: WakeSuppressed, Owner: "demo"},
			want:    "WARN  suppressed owner=demo fp=1a2b3c4d",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			log, out, errOut := newTestEventLog()
			if err := log.wake(at, "demo", test.outcome, "1a2b3c4d5e6f7a8b"); err != nil {
				t.Fatal(err)
			}
			want := "[2026-09-01 00:45:00 -0400] " + test.want +
				"; attention remains pending, see `relay pr watch status demo`\n"
			if errOut.String() != want {
				t.Errorf("stderr =\n%q\nwant\n%q", errOut.String(), want)
			}
			if out.String() != "" {
				t.Errorf("an undelivered wake wrote to stdout: %q", out.String())
			}
		})
	}

	log, out, errOut := newTestEventLog()
	err := log.failure(at, "pr watch failed project=demo after 3 consecutive observation errors")
	if err != nil {
		t.Fatal(err)
	}
	want := "[2026-09-01 00:45:00 -0400] ERROR pr watch failed project=demo " +
		"after 3 consecutive observation errors\n"
	if errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
	if out.String() != "" {
		t.Errorf("an error wrote to stdout: %q", out.String())
	}
}

// A failed observation prints no check, so its error line is the only place the
// retry cadence and the wall clock the watcher comes back at can appear.
func TestEventLogRetryCarriesTheCadenceAndTheNextAttempt(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, errOut := newTestEventLog()
	if err := log.retry(
		at, `observe pull request #42 for project "demo": gh: HTTP 500`, 900,
		at.Add(15*time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	want := "[2026-09-01 00:45:00 -0400] ERROR observe pull request #42 for project \"demo\": " +
		"gh: HTTP 500; cadence=15m next=01:00:00\n"
	if errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
	if out.String() != "" {
		t.Errorf("a retry wrote to stdout: %q", out.String())
	}
}

// A wake abandoned because the watcher moved on is routine, not a warning:
// nobody was owed the attention that observation carried.
func TestEventLogPrintsASkippedWakeToStdout(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, errOut := newTestEventLog()
	if err := log.wakeSkipped(at, "demo", "1a2b3c4d5e6f7a8b"); err != nil {
		t.Fatal(err)
	}
	want := "[2026-09-01 00:45:00 -0400] WAKE  skipped owner=demo fp=1a2b3c4d " +
		"reason=observation-superseded\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if errOut.String() != "" {
		t.Errorf("a skipped wake wrote to stderr: %q", errOut.String())
	}
}

// A pull request closed without merging ends the watch once its owner has the
// escalation, and the line says who took it.
func TestEventLogPrintsTheClosedEscalation(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, _ := newTestEventLog()
	if err := log.closed(at, "demo", 42, "demo"); err != nil {
		t.Fatal(err)
	}
	want := "[2026-09-01 00:45:00 -0400] DONE  project=demo pr=#42 " +
		"reason=closed-unmerged escalated-to=demo\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}

func TestEventLogReportsWriteFailures(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	broken := &failingWriter{err: errors.New("broken pipe")}
	log := newEventLog(broken, &strings.Builder{}, testDisplayZone)
	err := log.started(at, "demo", ModeStandalone, "demo", 42)
	if err == nil {
		t.Fatal("a broken watcher writer reported success")
	}
	for _, want := range []string{"write pr watch event", "START project=demo", "broken pipe"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("write error %q is missing %q", err, want)
		}
	}

	log = newEventLog(&strings.Builder{}, broken, testDisplayZone)
	if err := log.failure(at, "boom"); err == nil {
		t.Fatal("a broken watcher error writer reported success")
	}
}

// A watcher that is given no writers must stay silent rather than panic: the
// pane log is an output seam, not a correctness dependency.
func TestEventLogWithoutWritersIsSilent(t *testing.T) {
	log := newEventLog(nil, nil, testDisplayZone)
	if err := log.started(time.Now(), "demo", ModeStandalone, "demo", 42); err != nil {
		t.Fatal(err)
	}
	if err := log.failure(time.Now(), "boom"); err != nil {
		t.Fatal(err)
	}
}

// A watcher given no reader zone stamps events where the process runs, so a
// caller that has nothing to inject still gets local time.
func TestEventLogWithoutAZoneUsesTheHostZone(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	out := &strings.Builder{}
	if err := newEventLog(out, nil, nil).stopped(at, "demo", ""); err != nil {
		t.Fatal(err)
	}
	want := "[" + at.In(time.Local).Format("2006-01-02 15:04:05 -0700") + "] STOP  project=demo\n"
	if out.String() != want {
		t.Errorf("event line = %q, want %q", out.String(), want)
	}
}
