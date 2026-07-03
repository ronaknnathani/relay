package ui

import (
	"fmt"
	"time"
)

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
