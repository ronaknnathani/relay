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

func TestEventLogFormatsOneLinePerEvent(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, errOut := newTestEventLog()
	if err := log.started(at, "foo"); err != nil {
		t.Fatal(err)
	}
	if err := log.tick(at, []Reason{
		{Code: "ready-item:w2", Text: "Item w2 is ready to dispatch."},
		{Code: "unread-worker-outbox:w1", Text: "Item w1 has 1 unread worker outbox message(s)."},
	}, 900); err != nil {
		t.Fatal(err)
	}
	if err := log.wake(at.Add(time.Second), "foo", turnOutcome{
		Kind: wakeDelivered, Panes: []string{"w2:pC"}, Status: "idle",
	}); err != nil {
		t.Fatal(err)
	}
	if err := log.nextTick(at.Add(time.Second), at.Add(15*time.Minute).Format(time.RFC3339), 900); err != nil {
		t.Fatal(err)
	}
	if err := log.stopped(at.Add(2*time.Second), "foo", "context canceled"); err != nil {
		t.Fatal(err)
	}

	want := strings.Join([]string{
		"2026-09-01T00:45:00-04:00 patrol started program=foo",
		"2026-09-01T00:45:00-04:00 tick reasons=ready-item:w2,unread-worker-outbox:w1 cadence=15m",
		"2026-09-01T00:45:01-04:00 TL wake delivered program=foo pane=w2:pC status=idle",
		"2026-09-01T00:45:01-04:00 next tick at=2026-09-01T01:00:00-04:00 cadence=15m",
		"2026-09-01T00:45:02-04:00 patrol stopped program=foo reason=context canceled",
		"",
	}, "\n")
	if out.String() != want {
		t.Errorf("operational events =\n%q\nwant\n%q", out.String(), want)
	}
	if errOut.String() != "" {
		t.Errorf("routine events wrote to stderr: %q", errOut.String())
	}
}

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
			want:    "warning: TL wake busy program=foo pane=pA status=working; attention remains pending",
		},
		{
			name:    "absent",
			outcome: turnOutcome{Kind: wakeAbsent},
			want:    "warning: TL wake absent program=foo; attention remains pending",
		},
		{
			name:    "duplicate",
			outcome: turnOutcome{Kind: wakeDuplicate, Panes: []string{"p1", "p2"}},
			want:    "warning: TL wake duplicate program=foo panes=p1,p2; attention remains pending",
		},
		{
			name:    "suppressed",
			outcome: turnOutcome{Kind: wakeSuppressed, Panes: []string{"pA"}, Status: "idle"},
			want:    "warning: TL wake suppressed program=foo pane=pA status=idle; attention remains pending",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			log, out, errOut := newTestEventLog()
			if err := log.wake(at, "foo", test.outcome); err != nil {
				t.Fatal(err)
			}
			want := "2026-09-01T00:45:00-04:00 " + test.want +
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
	want := "2026-09-01T00:45:00-04:00 error: build patrol snapshot for program \"foo\": boom\n"
	if errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
	if out.String() != "" {
		t.Errorf("error wrote to stdout: %q", out.String())
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
	}, 1800); err != nil {
		t.Fatal(err)
	}
	printed := out.String() + errOut.String()
	want := "2026-09-01T00:45:00-04:00 tick reasons=contract-drift:api@v1,project-warning,ready-item:w2 cadence=30m\n"
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
	if err := log.tick(at, reasons, 900); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimSpace(out.String()), "+3 more cadence=15m") {
		t.Errorf("bounded tick line = %q", out.String())
	}
	if strings.Count(out.String(), "ready-item:") != maxLoggedReasons {
		t.Errorf("tick line printed %d codes, want %d", strings.Count(out.String(), "ready-item:"), maxLoggedReasons)
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
	for _, want := range []string{"write patrol event", "patrol started program=foo", "broken pipe"} {
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

// The pane is read by a person, so an event is stamped with the wall clock
// that person had, offset and all. The stored record stays UTC; only this line
// is translated, and a scheduled time printed inside a line is translated with
// it so one line never mixes two zones.
func TestEventLogStampsEventsInTheReadersZone(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, _ := newTestEventLog()
	if err := log.nextTick(at, "2026-09-01T05:00:00Z", 900); err != nil {
		t.Fatal(err)
	}
	want := "2026-09-01T00:45:00-04:00 next tick at=2026-09-01T01:00:00-04:00 cadence=15m\n"
	if out.String() != want {
		t.Errorf("event line = %q, want %q", out.String(), want)
	}
}

// A scheduled time that is not RFC3339 — a record written by hand or by a much
// older build — is printed exactly as it was recorded. A pane that blanks a
// field it cannot parse tells the reader less than one that shows it.
func TestEventLogPrintsAnUnparsableScheduledTimeVerbatim(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, _ := newTestEventLog()
	if err := log.nextTick(at, "soon", 900); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "next tick at=soon ") {
		t.Errorf("event line = %q, want the recorded value verbatim", out.String())
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
	want := at.In(time.Local).Format("2006-01-02T15:04:05-07:00") + " patrol started program=foo\n"
	if out.String() != want {
		t.Errorf("event line = %q, want %q", out.String(), want)
	}
}
