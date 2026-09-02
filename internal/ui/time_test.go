package ui

import (
	"testing"
	"time"
)

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name, ts, want string
	}{
		{"just now", "2026-05-12T11:59:30Z", "just now"},
		{"minutes", "2026-05-12T11:30:00Z", "30m ago"},
		{"hours", "2026-05-12T09:00:00Z", "3h ago"},
		{"days", "2026-05-10T12:00:00Z", "2d ago"},
		{"weeks", "2026-04-21T12:00:00Z", "3w ago"},
		{"invalid", "not a timestamp", "not a timestamp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := relativeTimeAt(tc.ts, now)
			if got != tc.want {
				t.Errorf("relativeTimeAt(%q) = %q, want %q", tc.ts, got, tc.want)
			}
		})
	}
}

// A human reading a pane or a status line reads it where they are, so a stored
// UTC timestamp is displayed in the reader's own zone with its offset spelled
// out. The offset is always numeric — a UTC reader sees "+00:00", never a bare
// "Z" — so a displayed timestamp is never mistaken for the stored record.
func TestLocalTimeRendersTheReadersOffset(t *testing.T) {
	at := time.Date(2026, 9, 2, 14, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		loc  *time.Location
		want string
	}{
		{"west of utc", time.FixedZone("EDT", -4*60*60), "2026-09-02T10:30:00-04:00"},
		{"east of utc", time.FixedZone("IST", 5*60*60+30*60), "2026-09-02T20:00:00+05:30"},
		{"utc", time.UTC, "2026-09-02T14:30:00+00:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LocalTime(at, tc.loc); got != tc.want {
				t.Errorf("LocalTime(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// A location that observes daylight saving prints the offset in force at that
// instant, not a fixed one, so a timestamp read in winter and one read in
// summer both name the wall clock the reader actually had.
func TestLocalTimeFollowsDaylightSaving(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("America/New_York is unavailable: %v", err)
	}
	summer := LocalTime(time.Date(2026, 7, 1, 16, 0, 0, 0, time.UTC), loc)
	winter := LocalTime(time.Date(2026, 1, 1, 16, 0, 0, 0, time.UTC), loc)
	if summer != "2026-07-01T12:00:00-04:00" {
		t.Errorf("summer = %q, want the daylight offset", summer)
	}
	if winter != "2026-01-01T11:00:00-05:00" {
		t.Errorf("winter = %q, want the standard offset", winter)
	}
}

// A nil location means the host's own zone, so a caller that has nothing to
// inject renders where the process runs.
func TestLocalTimeWithoutALocationUsesTheHostZone(t *testing.T) {
	at := time.Date(2026, 9, 2, 14, 30, 0, 0, time.UTC)
	if got, want := LocalTime(at, nil), LocalTime(at, time.Local); got != want {
		t.Errorf("LocalTime(nil) = %q, want the host zone %q", got, want)
	}
}

func TestLocalTimeTextRendersStoredTimestamps(t *testing.T) {
	loc := time.FixedZone("EDT", -4*60*60)
	for _, tc := range []struct {
		name, value, want string
	}{
		{"utc record", "2026-09-02T14:30:00Z", "2026-09-02T10:30:00-04:00"},
		{"offset record", "2026-09-02T20:00:00+05:30", "2026-09-02T10:30:00-04:00"},
		// Display never destroys what it cannot read: a legacy or hand-edited
		// value is shown exactly as recorded rather than blanked.
		{"not a timestamp", "shortly", "shortly"},
		{"date only", "2026-09-02", "2026-09-02"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LocalTimeText(tc.value, loc); got != tc.want {
				t.Errorf("LocalTimeText(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}
