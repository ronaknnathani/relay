package patrol

import (
	"errors"
	"strings"
	"testing"
	"time"
)

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

// testDisplayZone is a fixed reader zone. Pane events are rendered where the
// reader is, so the tests inject a zone rather than moving the process into
// one: nothing here touches time.Local.
var testDisplayZone = time.FixedZone("TEST", -4*60*60)

func newTestEventLog() (eventLog, *strings.Builder, *strings.Builder) {
	out := &strings.Builder{}
	errOut := &strings.Builder{}
	return newEventLog(out, errOut, testDisplayZone), out, errOut
}

// One line per event, each opening with the reader's wall clock in brackets and
// an aligned label. The tick line is the whole story of one observation: the
// cadence it chose, the wall clock the next one is due at, and why.
func TestEventLogFormatsOneLinePerEvent(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, errOut := newTestEventLog()
	if err := log.started(at, "foo"); err != nil {
		t.Fatal(err)
	}
	if err := log.tick(at, []Reason{
		{Code: "ready-item:w2", Text: "Item w2 is ready to dispatch."},
		{Code: "unread-worker-outbox:w1", Text: "Item w1 has 1 unread worker outbox message(s)."},
	}, 900, at.Add(15*time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := log.wake(at.Add(time.Second), "foo", turnOutcome{
		Kind: wakeDelivered, Panes: []string{"w2:pC"}, Status: "idle",
	}); err != nil {
		t.Fatal(err)
	}
	if err := log.stopped(at.Add(2*time.Second), "foo", "context canceled"); err != nil {
		t.Fatal(err)
	}

	want := strings.Join([]string{
		"[2026-09-01 00:45:00 -0400] START program=foo",
		"[2026-09-01 00:45:00 -0400] TICK  cadence=15m next=01:00:00 " +
			"reasons=ready-item:w2,unread-worker-outbox:w1",
		"[2026-09-01 00:45:01 -0400] WAKE  TL delivered pane=w2:pC status=idle",
		"[2026-09-01 00:45:02 -0400] STOP  program=foo reason=context canceled",
		"",
	}, "\n")
	if out.String() != want {
		t.Errorf("operational events =\n%q\nwant\n%q", out.String(), want)
	}
	if errOut.String() != "" {
		t.Errorf("routine events wrote to stderr: %q", errOut.String())
	}
}

// A tick is one line. There is no separate event announcing the next tick, so
// nothing can print a predicted time that the rest of the tick then changed.
func TestEventLogHasNoStandaloneNextTickEvent(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, errOut := newTestEventLog()
	if err := log.tick(at, nil, 1800, at.Add(30*time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	printed := out.String() + errOut.String()
	if strings.Count(printed, "\n") != 1 {
		t.Errorf("one tick printed %d lines:\n%s", strings.Count(printed, "\n"), printed)
	}
	if strings.Contains(printed, "next tick at=") {
		t.Errorf("the tick printed a standalone next tick event:\n%s", printed)
	}
	want := "[2026-09-01 00:45:00 -0400] TICK  cadence=30m next=01:15:00 reasons=none\n"
	if out.String() != want {
		t.Errorf("tick line = %q, want %q", out.String(), want)
	}
}

// A next tick later the same local day needs no date; one on any other day
// carries the whole compact stamp, so a reader never mistakes tomorrow's tick
// for one twenty minutes from now.
func TestEventLogPrintsTheNextTickAgainstTheDayItIsPrintedOn(t *testing.T) {
	for _, test := range []struct {
		name, at, next, want string
	}{
		{
			name: "same local day",
			at:   "2026-09-01T04:45:00Z", next: "2026-09-01T05:15:00Z",
			want: "next=01:15:00",
		},
		{
			name: "next local day",
			at:   "2026-09-01T23:50:00Z", next: "2026-09-02T04:20:00Z",
			want: "next=2026-09-02 00:20:00 -0400",
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
			if err := log.tick(at, nil, 900, test.next); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), " "+test.want+" ") {
				t.Errorf("tick line = %q, want %q", out.String(), test.want)
			}
		})
	}
}

// A terminal event has no next tick. It says so by omitting the field, never by
// printing an empty one.
func TestEventLogOmitsAnAbsentNextTick(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, errOut := newTestEventLog()
	if err := log.stopped(at, "foo", "program manifest absent"); err != nil {
		t.Fatal(err)
	}
	if err := log.tick(at, nil, 900, ""); err != nil {
		t.Fatal(err)
	}
	if err := log.failure(at, "patrol failed program=foo after 3 consecutive errors"); err != nil {
		t.Fatal(err)
	}
	printed := out.String() + errOut.String()
	if strings.Contains(printed, "next=") {
		t.Errorf("an event with no schedule printed a next tick:\n%s", printed)
	}
	want := strings.Join([]string{
		"[2026-09-01 00:45:00 -0400] STOP  program=foo reason=program manifest absent",
		"[2026-09-01 00:45:00 -0400] TICK  cadence=15m reasons=none",
		"",
	}, "\n")
	if out.String() != want {
		t.Errorf("events =\n%q\nwant\n%q", out.String(), want)
	}
}

// A degraded outcome carries a WARN label and the command that shows the full
// recorded detail; a failure carries ERROR. Neither embeds its severity in the
// prose after the timestamp.
func TestEventLogWritesDegradedWakesAndErrorsToStderr(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		outcome turnOutcome
		want    string
	}{
		{
			name:    "busy",
			outcome: turnOutcome{Kind: wakeBusy, Panes: []string{"pA"}, Status: "working"},
			want:    "WARN  TL busy pane=pA status=working; attention remains pending",
		},
		{
			name:    "absent",
			outcome: turnOutcome{Kind: wakeAbsent},
			want:    "WARN  TL absent; attention remains pending",
		},
		{
			name:    "duplicate",
			outcome: turnOutcome{Kind: wakeDuplicate, Panes: []string{"p1", "p2"}},
			want:    "WARN  TL duplicate panes=p1,p2; attention remains pending",
		},
		{
			name:    "suppressed",
			outcome: turnOutcome{Kind: wakeSuppressed, Panes: []string{"pA"}, Status: "idle"},
			want:    "WARN  TL suppressed pane=pA status=idle; attention remains pending",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			log, out, errOut := newTestEventLog()
			if err := log.wake(at, "foo", test.outcome); err != nil {
				t.Fatal(err)
			}
			want := "[2026-09-01 00:45:00 -0400] " + test.want +
				", see `relay program patrol status foo`\n"
			if errOut.String() != want {
				t.Errorf("stderr =\n%q\nwant\n%q", errOut.String(), want)
			}
			if out.String() != "" {
				t.Errorf("degraded wake wrote to stdout: %q", out.String())
			}
		})
	}

	log, out, errOut := newTestEventLog()
	if err := log.failure(at, "build patrol snapshot for program \"foo\": boom"); err != nil {
		t.Fatal(err)
	}
	want := "[2026-09-01 00:45:00 -0400] ERROR build patrol snapshot for program \"foo\": boom\n"
	if errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
	if out.String() != "" {
		t.Errorf("error wrote to stdout: %q", out.String())
	}
}

// A failed observation prints no tick, so its error line is the only place the
// retry cadence and the wall clock the patrol comes back at can appear.
func TestEventLogRetryCarriesTheCadenceAndTheNextAttempt(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, errOut := newTestEventLog()
	if err := log.retry(
		at, "patrol observation failed: source unavailable", 900,
		at.Add(15*time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	want := "[2026-09-01 00:45:00 -0400] ERROR patrol observation failed: source unavailable; " +
		"cadence=15m next=01:00:00\n"
	if errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
	if out.String() != "" {
		t.Errorf("a retry wrote to stdout: %q", out.String())
	}
}

// Reason text can quote a filesystem path, a mailbox body, or an error detail.
// Only the code is printable, and only when its detail is a durable Relay
// identifier rather than a slug derived from that same free text.
func TestEventLogPrintsReasonCodesAndRedactsTextDerivedDetail(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, errOut := newTestEventLog()
	if err := log.tick(at, []Reason{
		{Code: "ready-item:w2", Text: "Item w2 is ready to dispatch."},
		{
			Code: "project-warning:load-active-project-home-ceo-secrets-token-txt-denied",
			Text: "load active project: open /home/ceo/secrets/token.txt: permission denied",
		},
		{Code: "contract-drift:api@v1", Text: "Contract api@v1 content no longer matches."},
	}, 1800, at.Add(30*time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	printed := out.String() + errOut.String()
	want := "[2026-09-01 00:45:00 -0400] TICK  cadence=30m next=01:15:00 " +
		"reasons=contract-drift:api@v1,project-warning,ready-item:w2\n"
	if out.String() != want {
		t.Errorf("tick line = %q, want %q", out.String(), want)
	}
	for _, leak := range []string{
		"permission denied", "/home/ceo/secrets", "token", "ready to dispatch", "no longer matches",
	} {
		if strings.Contains(printed, leak) {
			t.Errorf("patrol event leaked %q: %s", leak, printed)
		}
	}
}

// One tick line stays one readable line: a program with many attention codes
// prints a bounded prefix and a count of the rest.
func TestEventLogBoundsReasonCodesPerTick(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	reasons := make([]Reason, 0, maxLoggedReasons+3)
	for index := 0; index < maxLoggedReasons+3; index++ {
		reasons = append(reasons, Reason{Code: "ready-item:w" + string(rune('a'+index))})
	}
	log, out, _ := newTestEventLog()
	if err := log.tick(at, reasons, 900, at.Add(15*time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimSpace(out.String()), "+3 more") {
		t.Errorf("bounded tick line = %q", out.String())
	}
	if strings.Count(out.String(), "ready-item:") != maxLoggedReasons {
		t.Errorf("tick line printed %d codes, want %d",
			strings.Count(out.String(), "ready-item:"), maxLoggedReasons)
	}
}

func TestEventLogReportsWriteFailures(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	broken := &failingWriter{err: errors.New("broken pipe")}
	log := newEventLog(broken, &strings.Builder{}, testDisplayZone)
	err := log.started(at, "foo")
	if err == nil {
		t.Fatal("a broken patrol writer reported success")
	}
	for _, want := range []string{"write patrol event", "START program=foo", "broken pipe"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("write error %q is missing %q", err, want)
		}
	}

	log = newEventLog(&strings.Builder{}, broken, testDisplayZone)
	if err := log.failure(at, "boom"); err == nil {
		t.Fatal("a broken patrol error writer reported success")
	}
}

// A patrol that is given no writers must stay silent rather than panic: the
// pane log is an output seam, not a correctness dependency.
func TestEventLogWithoutWritersIsSilent(t *testing.T) {
	log := newEventLog(nil, nil, testDisplayZone)
	if err := log.started(time.Now(), "foo"); err != nil {
		t.Fatal(err)
	}
	if err := log.failure(time.Now(), "boom"); err != nil {
		t.Fatal(err)
	}
}

// A patrol given no reader zone stamps events where the process runs, so a
// caller that has nothing to inject still gets local time.
func TestEventLogWithoutAZoneUsesTheHostZone(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	out := &strings.Builder{}
	if err := newEventLog(out, nil, nil).started(at, "foo"); err != nil {
		t.Fatal(err)
	}
	want := "[" + at.In(time.Local).Format("2006-01-02 15:04:05 -0700") + "] START program=foo\n"
	if out.String() != want {
		t.Errorf("event line = %q, want %q", out.String(), want)
	}
}
