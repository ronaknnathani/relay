package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The CTO skill and the program guide describe the same transport the code
// implements: a fresh bounded CTO-role session, never a fixed recurrence
// command and never Herdr keystrokes into the CEO-facing pane.
func TestProgramDocsDescribeBoundedCTOTurns(t *testing.T) {
	root := repoRoot(t)
	for _, test := range []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path: filepath.Join("skills", "cto", "SKILL.md"),
			required: []string{
				"fresh, bounded CTO-role session",
				"never types into, focuses, or prompts this pane",
				"Reload durable state at the top of every CEO turn",
				"RELAY_AUTOMATED_TURN=1",
				"RELAY_AUTOMATED_TURN_SESSION_ID=<session id>",
				"cto-automated:<session-prefix>",
				"[automated CTO turn <session-prefix>, on behalf of CEO]",
				"three consecutive failures",
				"`program hold`",
				"`program release`",
				"`contract publish`",
			},
			forbidden: []string{
				"/loop", "/every", "program worker prompt",
				"Check Relay program mail and patrol state.",
				"herdr agent prompt \"",
			},
		},
		{
			path: filepath.Join("docs", "programs.md"),
			required: []string{
				"relay program cto turn <slug>",
				"writer.lock",
				"turns.json",
				"RELAY_AUTOMATED_TURN=1",
				"RELAY_AUTOMATED_TURN_SESSION_ID=<fresh session UUID>",
				"cto-automated:<session-prefix>",
				"[automated CTO turn <session-prefix>, on behalf of CEO]",
				"ambiguous ownership",
				"points at the live pane instead",
				"only after a turn exits zero",
				"sorted unread worker-outbox message ids",
				"`program hold`",
				"`program release`",
				"`contract publish`",
			},
			forbidden: []string{
				"Check Relay program mail and patrol state.",
				"payload-free doorbell, then performs one bounded",
			},
		},
		{
			path: "README.md",
			required: []string{
				"bounded CTO-role session",
				"never types into the",
			},
			forbidden: []string{"rings only the matching idle CTO"},
		},
	} {
		t.Run(test.path, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, test.path))
			if err != nil {
				t.Fatal(err)
			}
			body := string(data)
			for _, want := range test.required {
				if !strings.Contains(body, want) {
					t.Errorf("%s is missing %q", test.path, want)
				}
			}
			for _, forbidden := range test.forbidden {
				if strings.Contains(body, forbidden) {
					t.Errorf("%s still contains retired transport text %q", test.path, forbidden)
				}
			}
		})
	}
}
