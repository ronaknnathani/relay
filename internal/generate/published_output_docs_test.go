package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	publishedOutputRule = "**Published output names no internal program bookkeeping.**"
	publishedOutputFlag = "- Published text that names a work-item id, decision id, derived worker name, " +
		"or a Relay program / tech lead / CEO / managed-worker origin."
)

// Every skill that writes something a reviewer outside the program will read
// must carry the same rule against leaking program bookkeeping into it.
func TestPublishedOutputSurfacesForbidInternalIdentifiers(t *testing.T) {
	root := repoRoot(t)
	for _, test := range []struct {
		path     string
		required []string
	}{
		{
			path: filepath.Join("skills", "open-pr", "SKILL.md"),
			required: []string{
				publishedOutputRule, publishedOutputFlag,
				`Part 3 "PR summaries"`,
				"rejected alternatives",
			},
		},
		{
			path:     filepath.Join("skills", "commit", "SKILL.md"),
			required: []string{publishedOutputRule, publishedOutputFlag},
		},
		{
			path:     filepath.Join("skills", "pr-fix", "SKILL.md"),
			required: []string{publishedOutputRule, publishedOutputFlag},
		},
		{
			path:     filepath.Join("skills", "implement", "SKILL.md"),
			required: []string{publishedOutputRule, publishedOutputFlag},
		},
		{
			path: filepath.Join("skills", "tl", "SKILL.md"),
			required: []string{
				"never ask a worker to cite them",
				"- Asking a worker to cite an item or decision id in a published artifact.",
			},
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
		})
	}
}
