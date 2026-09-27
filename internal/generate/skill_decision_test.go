package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillDecisionRecordsAreComplete(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs", "skill-decisions.md"))
	if err != nil {
		t.Fatal(err)
	}
	sections := strings.Split(string(data), "### `")
	records := make(map[string]string, len(sections))
	for _, section := range sections[1:] {
		name, body, ok := strings.Cut(section, "`")
		if !ok {
			t.Fatalf("malformed skill decision section: %.80s", section)
		}
		for _, field := range []string{
			"Observed aggregate use:",
			"Distinct contribution:",
			"Overlap:",
			"Feedback relevance:",
			"Safety role:",
			"Decision:",
			"Migration impact:",
		} {
			if !strings.Contains(body, field) {
				t.Errorf("skill %q decision is missing %q", name, field)
			}
		}
		records[name] = body
	}

	source, err := LoadSource(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range source.Entries {
		if _, ok := records[entry.Name]; !ok {
			t.Errorf("retained skill %q has no decision record", entry.Name)
		}
	}
	for _, name := range []string{"relay-status", "relay-archive", "todo"} {
		if _, ok := records[name]; !ok {
			t.Errorf("retired skill %q has no decision record", name)
		}
	}
	for _, sequence := range []string{
		"clarify + plan",
		"implement + validate",
		"simplify + review",
		"delivery review + open-pr review",
		"pr-monitor + pr-fix",
	} {
		if !strings.Contains(string(data), "| "+sequence+" |") {
			t.Errorf("missing sequence decision %q", sequence)
		}
	}
}
