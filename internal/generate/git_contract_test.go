package generate

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGitAndOpenPRContractOwnership(t *testing.T) {
	root := repoRoot(t)
	checks := map[string][]string{
		filepath.Join("skills", "commit", "SKILL.md"): {
			"default branch", "specific files", "secrets", "Co-authored-by",
		},
		filepath.Join("skills", "rebase", "SKILL.md"): {
			"origin/HEAD", "resolve forward", "intended diff", "force-with-lease",
			"validation evidence stale", "route-less legacy",
		},
		filepath.Join("skills", "open-pr", "SKILL.md"): {
			"`commit` contract", "`rebase` contract", "evidence fresh",
			"performs no new review", "clean, fully committed", "after the final commit",
			"legacy seven-phase",
		},
	}
	for path, required := range checks {
		body := readFile(t, filepath.Join(root, path))
		for _, want := range required {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing ownership contract %q", path, want)
			}
		}
	}
}
