package generate

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWatcherHandoffHasOneWorklistAndOneWriter(t *testing.T) {
	root := repoRoot(t)
	monitor := readFile(t, filepath.Join(root, "skills", "pr-monitor", "SKILL.md"))
	for _, want := range []string{
		"read-only", "one authoritative worklist", "re-observe once", "never mutates",
		"`watcher_mode`", "`owner_slug`",
	} {
		if !strings.Contains(monitor, want) {
			t.Errorf("pr-monitor missing handoff contract %q", want)
		}
	}
	fix := readFile(t, filepath.Join(root, "skills", "pr-fix", "SKILL.md"))
	for _, want := range []string{
		"sole mutation owner", "authoritative worklist schema", "no reassessment loop",
		"ready-for-owner", "`watcher_mode`", "`owner_slug`",
		"stack orchestrator is the sole auto-merge owner",
	} {
		if !strings.Contains(fix, want) {
			t.Errorf("pr-fix missing ownership contract %q", want)
		}
	}
}
