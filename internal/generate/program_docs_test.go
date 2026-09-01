package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tech lead skill and program guide describe the focusless live-pane
// doorbell implemented by the patrol and worker notification paths.
func TestProgramDocsDescribeLiveTLDoorbells(t *testing.T) {
	root := repoRoot(t)
	for _, test := range []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path: filepath.Join("skills", "tl", "SKILL.md"),
			required: []string{
				"payload-free doorbell to this exact live pane",
				"terminal-session control stream",
				"never focuses the pane",
				"another tech lead session",
				"suppresses all further doorbells",
			},
			forbidden: []string{
				"/loop", "/every", "program worker prompt",
				"fresh, bounded tech lead-role session",
				"RELAY_AUTOMATED_TURN=1",
			},
		},
		{
			path: filepath.Join("docs", "programs.md"),
			required: []string{
				"Live CTO doorbells",
				"herdr agent prompt <cto-pane>",
				"herdr terminal session control <cto-pane> --takeover",
				`{"type":"terminal.input","bytes":"DQ=="}`,
				"still idle after the grace period",
				"current dimensions",
				"currently focused",
				"records an `uncertain` wake",
				"duplicated CTOs",
				"sorted unread worker-outbox message ids",
			},
			forbidden: []string{
				"relay program cto turn <slug>",
				"fresh, bounded, same-role",
				"RELAY_AUTOMATED_TURN_SESSION_ID",
			},
		},
		{
			path: "README.md",
			required: []string{
				"existing idle CTO pane",
				"changing the user's focus",
			},
			forbidden: []string{"bounded CTO-role session", "verified headless turn"},
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
