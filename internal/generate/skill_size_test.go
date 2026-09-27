package generate

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestSkillWordBudgets(t *testing.T) {
	root := repoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "skills"))
	if err != nil {
		t.Fatal(err)
	}
	excluded := map[string]bool{"write-like-me": true, "build-write-like-me": true}
	limits := map[string]int{
		"deliver-pr": 1656,
		"review":     1071,
		"pr-monitor": 1342,
		"pr-fix":     1502,
		"tl":         4000,
	}
	counts := map[string]int{}
	total := 0
	for _, entry := range entries {
		if !entry.IsDir() || excluded[entry.Name()] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, "skills", entry.Name(), "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		count := len(strings.Fields(string(data)))
		counts[entry.Name()] = count
		total += count
		if limit, ok := limits[entry.Name()]; ok && count > limit {
			t.Errorf("%s words = %d, budget = %d", entry.Name(), count, limit)
		}
	}
	if _, ok := counts["route"]; !ok {
		t.Error("route is missing from the aggregate skill budget")
	}
	if total > 16200 {
		names := make([]string, 0, len(counts))
		for name := range counts {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			t.Logf("%s: %d", name, counts[name])
		}
		t.Fatalf("non-writing skill words = %d, budget = 16200", total)
	}
}
