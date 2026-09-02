package ui

import (
	"fmt"
	"time"
)

// localTimeLayout is RFC3339 with the numeric offset always spelled out. The
// standard layout collapses a zero offset to "Z", which is exactly the shape of
// the stored record; a displayed timestamp says "+00:00" instead so a reader
// can always tell a rendered wall clock from the UTC value on disk.
const localTimeLayout = "2006-01-02T15:04:05-07:00"

// compactTimeLayout is the same wall clock written for a person scanning a
// pane: a space instead of the "T", and no colon in the offset, so the eye
// separates date, time, and zone without decoding anything.
const compactTimeLayout = "2006-01-02 15:04:05 -0700"

// compactClockLayout is that instant with the date and offset dropped, which is
// everything a scheduled time needs when it falls on the day it is printed on.
const compactClockLayout = "15:04:05"

// LocalTime renders an instant as the wall clock a reader in loc has, with the
// UTC offset in force at that instant. A nil loc means the host's own zone.
//
// Relay stores every timestamp in UTC. This is display only: nothing that is
// written to a runtime record, a digest, GitHub, or --json output goes through
// here.
func LocalTime(t time.Time, loc *time.Location) string {
	return t.In(displayLocation(loc)).Format(localTimeLayout)
}

// LocalTimeText renders one stored RFC3339 timestamp in loc. A value that does
// not parse — an empty field, a legacy format, a hand-edited record — is
// returned exactly as it was given, because a status line that hides what it
// cannot read is worse than one that shows it verbatim.
func LocalTimeText(value string, loc *time.Location) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return LocalTime(parsed, loc)
}

// CompactTime renders an instant as the compact local wall clock a pane event
// carries. A nil loc means the host's own zone. Like LocalTime this is display
// only: nothing written to a runtime record, a digest, GitHub, or --json output
// goes through here.
func CompactTime(t time.Time, loc *time.Location) string {
	return t.In(displayLocation(loc)).Format(compactTimeLayout)
}

// CompactScheduledText renders one stored RFC3339 timestamp as the compact form
// a reader needs beside an event stamped at `at`. A time later the same local
// day prints as a bare wall clock, because the date is the one the reader is
// already looking at; a time on any other day prints in full, date and offset
// included, so tomorrow's check is never read as today's.
//
// A value that does not parse is returned exactly as it was given, and an empty
// value stays empty so a caller can drop the field rather than print a blank.
func CompactScheduledText(value string, at time.Time, loc *time.Location) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	zone := displayLocation(loc)
	scheduled, printed := parsed.In(zone), at.In(zone)
	if sameCalendarDay(scheduled, printed) {
		return scheduled.Format(compactClockLayout)
	}
	return scheduled.Format(compactTimeLayout)
}

func sameCalendarDay(first, second time.Time) bool {
	firstYear, firstMonth, firstDay := first.Date()
	secondYear, secondMonth, secondDay := second.Date()
	return firstYear == secondYear && firstMonth == secondMonth && firstDay == secondDay
}

func displayLocation(loc *time.Location) *time.Location {
	if loc == nil {
		return time.Local
	}
	return loc
}

// RelativeTime formats an RFC3339 timestamp as a human-readable relative
// duration ("just now", "5m ago", "3h ago", "2d ago", "4w ago"). If the
// input doesn't parse, the original string is returned.
func RelativeTime(ts string) string {
	return relativeTimeAt(ts, time.Now())
}

func relativeTimeAt(ts string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	diff := now.Sub(t)
	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		return fmt.Sprintf("%dm ago", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(diff.Hours()))
	case diff < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(diff.Hours()/24))
	default:
		return fmt.Sprintf("%dw ago", int(diff.Hours()/(24*7)))
	}
}
