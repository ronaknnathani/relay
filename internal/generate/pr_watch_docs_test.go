package generate

import (
	"path/filepath"
	"strings"
	"testing"
)

// The PR watcher owns deterministic observation and cadence. These tests pin
// the source skills to that split so a future edit cannot quietly restore a
// hand-rolled monitoring loop or a broad GitHub poll inside a skill.
func TestPRWatchDocsSplitObservationFromRemediation(t *testing.T) {
	root := repoRoot(t)
	for _, test := range []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path: filepath.Join("skills", "pr-monitor", "SKILL.md"),
			required: []string{
				`relay pr watch digest "$SLUG" --fingerprint "$FP" --json`,
				`relay pr watch status "$SLUG" --json`,
				`relay pr watch tick "$SLUG" --json`,
				`relay pr watch acknowledge "$SLUG" --fingerprint "$FP" --outcome handled`,
				"--outcome escalated",
				"--outcome obsolete",
				"This skill has **no loop**",
				"do not re-derive it with your own `gh` sweep",
				"**do not acknowledge**",
				"delegated mode",
				"One digest, one run, one exit",
			},
			forbidden: []string{
				"/loop", "/every", "nextTickAfter", "native loop", "CronCreate", "ScheduleWakeup",
				"gh api \"repos/$OWNER/$REPO/issues/$N/comments\"",
				"continuous monitoring you can't provide",
			},
		},
		{
			path: filepath.Join("skills", "pr-fix", "SKILL.md"),
			required: []string{
				"Two modes — check your input first",
				"Delegated mode",
				"Direct mode",
				"Skip step 1's broad assessment",
				"no reassessment loop",
				"Return a structured result, one entry per supplied item",
				"`check_run_id`",
				"`thread_id`",
			},
			forbidden: []string{"relay pr watch acknowledge", "relay pr watch start"},
		},
		{
			path: filepath.Join("skills", "deliver-pr", "SKILL.md"),
			required: []string{
				`relay pr watch start "$SLUG"`,
				`relay pr watch start "$SLUG" --mode managed`,
				"never the tech lead",
				"must never fail the delivery",
				"adopts an already-running watcher",
				"`stack-ship` sub-agent",
			},
			forbidden: []string{"relay pr watch acknowledge"},
		},
		{
			path: filepath.Join("skills", "stack-ship", "SKILL.md"),
			required: []string{
				"relay pr watch start <front-project-slug> --mode stack --owner <stack-orchestrator-slug>",
				"never a watcher on a\nnon-front PR",
				"stop that watcher",
			},
		},
		{
			path: filepath.Join("skills", "stack-ship", "references", "monitor-loop.md"),
			required: []string{
				"relay pr watch start <front-project-slug> --mode stack --owner <stack-orchestrator-slug>",
				"relay pr watch stop <front-project-slug>",
				"stack-front-merged",
				"`--owner` is required in stack mode",
				"A `deliver-pr` sub-agent must not start one either",
			},
			forbidden: []string{"native-loop", "fire-and-forget native loop"},
		},
	} {
		t.Run(test.path, func(t *testing.T) {
			body := readFile(t, filepath.Join(root, test.path))
			for _, want := range test.required {
				if !strings.Contains(body, want) {
					t.Errorf("%s is missing %q", test.path, want)
				}
			}
			for _, forbidden := range test.forbidden {
				if strings.Contains(body, forbidden) {
					t.Errorf("%s still contains %q", test.path, forbidden)
				}
			}
		})
	}
}
