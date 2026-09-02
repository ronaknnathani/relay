package prwatch

import (
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

// The watcher pane is read by a person, so an event is stamped with the wall
// clock that person had, offset and all. The runtime record stays UTC; only
// this line is translated, and a scheduled time printed inside a line is
// translated with it so one line never mixes two zones.
func TestEventLogStampsEventsInTheReadersZone(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, errOut := newTestEventLog()
	if err := log.started(at, "demo", ModeStandalone, "demo", 42); err != nil {
		t.Fatal(err)
	}
	if err := log.nextCheck(at, "2026-09-01T05:00:00Z", 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := log.complete(at.Add(time.Second), "demo", 42); err != nil {
		t.Fatal(err)
	}
	if err := log.failure(at.Add(2*time.Second), "boom"); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"2026-09-01T00:45:00-04:00 pr watch started project=demo mode=standalone owner=demo pr=#42",
		"2026-09-01T00:45:00-04:00 next check at=2026-09-01T01:00:00-04:00 cadence=15m",
		"2026-09-01T00:45:01-04:00 pr watch complete project=demo pr=#42 reason=merged",
		"",
	}, "\n")
	if out.String() != want {
		t.Errorf("operational events =\n%q\nwant\n%q", out.String(), want)
	}
	if errOut.String() != "2026-09-01T00:45:02-04:00 error: boom\n" {
		t.Errorf("stderr = %q, want the reader's zone", errOut.String())
	}
}

// A scheduled time that is not RFC3339 — a record written by hand or by a much
// older build — is printed exactly as it was recorded. A pane that blanks a
// field it cannot parse tells the reader less than one that shows it.
func TestEventLogPrintsAnUnparsableScheduledTimeVerbatim(t *testing.T) {
	at := time.Date(2026, 9, 1, 4, 45, 0, 0, time.UTC)
	log, out, _ := newTestEventLog()
	if err := log.nextCheck(at, "soon", time.Minute); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "next check at=soon ") {
		t.Errorf("event line = %q, want the recorded value verbatim", out.String())
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
	want := at.In(time.Local).Format("2006-01-02T15:04:05-07:00") + " pr watch stopped project=demo\n"
	if out.String() != want {
		t.Errorf("event line = %q, want %q", out.String(), want)
	}
}
