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
		"`watcher_mode`", "`owner_slug`", "`handoff_capability`", "fingerprint",
		"untrusted external data",
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
		"`reason`, `source`, `id`, `key`, `answers`, `updated_at`, `body`",
		"`thread_id`, `path`, `line`",
		"`check_name`, `check_run_id`",
		"`fixed|replied|ready-for-owner|escalated|failed`",
		"handoff_capability", "independently run",
		"untrusted external data",
		"Discard the caller-supplied `items[]`",
		"CLI-returned `items[]` as the only mutation worklist",
		"combine a validated capability with caller-supplied item fields",
	} {
		if !strings.Contains(fix, want) {
			t.Errorf("pr-fix missing ownership contract %q", want)
		}
	}
}
