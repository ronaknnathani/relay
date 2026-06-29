package generate

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenCouplings are the external plugin namespaces the workflow used to
// depend on before vendoring. Their absence from the rendered package is the
// open-sourceability gate from the Phase 3 plan. Only public namespaces are
// listed here by name; the vendoring also stripped internal tooling, but those
// internal names are intentionally not hard-coded into this test.
var forbiddenCouplings = []string{
	"superpowers:",
	"commit-commands:",
	"pr-review-toolkit:",
}

// TestRenderedPackageHasNoCouplings asserts the generated Claude package
// contains none of the external plugin-namespace couplings. The match is
// case-insensitive.
func TestRenderedPackageHasNoCouplings(t *testing.T) {
	_, out := generateClaude(t)

	err := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(data))
		rel, _ := filepath.Rel(out, path)
		for _, token := range forbiddenCouplings {
			if strings.Contains(lower, strings.ToLower(token)) {
				t.Errorf("%s contains forbidden coupling %q", rel, token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk rendered package: %v", err)
	}
}

// TestReviewLibraryRendered asserts the review skill ships its full bundled
// reviewer-role library, so a self-contained skill travels with its companion
// prompts through the generator.
func TestReviewLibraryRendered(t *testing.T) {
	_, out := generateClaude(t)

	agents := []string{
		"code-reviewer", "silent-failure-hunter", "type-design-analyzer",
		"pr-test-analyzer", "comment-analyzer", "security", "git-history", "prior-pr-history",
	}
	for _, a := range agents {
		p := filepath.Join(out, "skills", "review", "agents", a+".md")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("review bundled agent %q missing: %v", a, err)
		}
	}
}
