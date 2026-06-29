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
