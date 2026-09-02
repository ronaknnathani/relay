package ui

import (
	"testing"
	"time"
)

// A pane line is scanned, not parsed. The timestamp it opens with is the
// reader's own wall clock in a compact shape — space-separated date, time, and
// numeric offset — while everything Relay stores stays RFC3339 UTC.
func TestCompactTimeRendersTheReadersWallClock(t *testing.T) {
	at := time.Date(2026, 9, 2, 18, 36, 46, 0, time.UTC)
	for _, tc := range []struct {
		name string
		loc  *time.Location
		want string
	}{
		{"west of utc", time.FixedZone("EDT", -4*60*60), "2026-09-02 14:36:46 -0400"},
		{"east of utc", time.FixedZone("IST", 5*60*60+30*60), "2026-09-03 00:06:46 +0530"},
		{"utc", time.UTC, "2026-09-02 18:36:46 +0000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompactTime(at, tc.loc); got != tc.want {
				t.Errorf("CompactTime(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestCompactTimeWithoutALocationUsesTheHostZone(t *testing.T) {
	at := time.Date(2026, 9, 2, 18, 36, 46, 0, time.UTC)
	if got, want := CompactTime(at, nil), CompactTime(at, time.Local); got != want {
		t.Errorf("CompactTime(nil) = %q, want the host zone %q", got, want)
	}
}

// A scheduled time is read against the line it sits on. Later today needs no
// date; any other day needs the whole thing, because a bare "00:06:46" on a
// line stamped the previous evening reads as a time that has already passed.
func TestCompactScheduledTextDropsTheDateOnlyForTheSameDay(t *testing.T) {
	zone := time.FixedZone("EDT", -4*60*60)
	at := time.Date(2026, 9, 2, 18, 36, 46, 0, time.UTC)
	for _, tc := range []struct {
		name, value, want string
	}{
		{"later the same day", "2026-09-02T18:51:46Z", "14:51:46"},
		{"earlier the same day", "2026-09-02T12:00:00Z", "08:00:00"},
		{"the next local day", "2026-09-03T05:30:00Z", "2026-09-03 01:30:00 -0400"},
		{"an hour later but the next local day", "2026-09-03T04:06:46Z", "2026-09-03 00:06:46 -0400"},
		{"a previous day", "2026-09-01T18:51:46Z", "2026-09-01 14:51:46 -0400"},
		// Display never destroys what it cannot read, and an absent schedule
		// stays absent so a caller can omit the field entirely.
		{"not a timestamp", "soon", "soon"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompactScheduledText(tc.value, at, zone); got != tc.want {
				t.Errorf("CompactScheduledText(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// The calendar day that matters is the reader's, not UTC's: the same pair of
// instants is one day in one zone and two in another.
func TestCompactScheduledTextComparesDaysInTheReadersZone(t *testing.T) {
	at := time.Date(2026, 9, 2, 23, 30, 0, 0, time.UTC)
	scheduled := "2026-09-03T00:30:00Z"
	if got := CompactScheduledText(scheduled, at, time.FixedZone("BST", -3*60*60)); got != "21:30:00" {
		t.Errorf("west of the date line = %q, want the same local day", got)
	}
	if got := CompactScheduledText(scheduled, at, time.UTC); got != "2026-09-03 00:30:00 +0000" {
		t.Errorf("in UTC = %q, want the next day spelled out", got)
	}
}

func TestEventLineAlignsTheLabelAfterTheTimestamp(t *testing.T) {
	at := time.Date(2026, 9, 2, 18, 36, 46, 0, time.UTC)
	zone := time.FixedZone("EDT", -4*60*60)
	for _, tc := range []struct {
		name, label, fields, want string
	}{
		{
			name: "five column label", label: EventStart, fields: "program=workload-mp",
			want: "[2026-09-02 14:36:46 -0400] START program=workload-mp",
		},
		{
			name: "padded label keeps the columns", label: EventTick, fields: "cadence=15m",
			want: "[2026-09-02 14:36:46 -0400] TICK  cadence=15m",
		},
		{
			name: "no fields", label: EventStop, fields: "",
			want: "[2026-09-02 14:36:46 -0400] STOP ",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EventLine(at, zone, tc.label, tc.fields); got != tc.want {
				t.Errorf("EventLine = %q, want %q", got, tc.want)
			}
		})
	}
}

// Every label occupies the same width, which is what makes a column of events
// scannable.
func TestEventLabelsShareOneWidth(t *testing.T) {
	for _, label := range []string{
		EventStart, EventTick, EventCheck, EventWake, EventDone, EventStop, EventWarn, EventError,
	} {
		if len(label) != 5 {
			t.Errorf("label %q is %d columns, want 5", label, len(label))
		}
	}
}
