package project

import (
	"strings"
	"testing"
)

func TestDeriveSlug(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"basic", "Add retry logic to HTTP client", "add-retry-logic-http-client"},
		{"fillers stripped", "the quick brown fox", "quick-brown-fox"},
		{"punctuation collapsed", "Fix bug: server crashes!", "fix-bug-server-crashes"},
		{"length capped at 40", strings.Repeat("a", 80), strings.Repeat("a", 40)},
		{"all fillers", "the and of", ""},
		{"mixed case", "REFACTOR THE Cli", "refactor-cli"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveSlug(tc.in)
			if got != tc.want {
				t.Errorf("DeriveSlug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
