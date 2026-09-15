package generate

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestProgramSkillsDelegateAdaptiveDeliveryContracts(t *testing.T) {
	root := repoRoot(t)
	stack := readFile(t, filepath.Join(root, "skills", "stack-ship", "SKILL.md"))
	for _, want := range []string{"adaptive `deliver-pr`", "can-open-pr", "human code-owner approval"} {
		if !strings.Contains(stack, want) {
			t.Errorf("stack-ship missing delegated contract %q", want)
		}
	}
	if strings.Contains(stack, "clarify → plan → implement → simplify → review → validate → open-pr") {
		t.Error("stack-ship hard-codes the retired fixed phase sequence")
	}

	cycle := readFile(t, filepath.Join(root, "skills", "stack-ship", "references", "pr-build-cycle.md"))
	if !strings.Contains(cycle, "route contract") || strings.Contains(cycle, "full single-PR pipeline") {
		t.Error("stack build cycle does not delegate adaptive routing")
	}

	tl := readFile(t, filepath.Join(root, "skills", "tl", "SKILL.md"))
	for _, want := range []string{
		"program governance", "`deliver-pr`", "`pr-monitor`", "`pr-fix`",
		"route-first", "only when selected", "plan-review handoff",
	} {
		if !strings.Contains(tl, want) {
			t.Errorf("tl missing delegated workflow reference %q", want)
		}
	}

	programs := readFile(t, filepath.Join(root, "docs", "programs.md"))
	for _, want := range []string{
		"route-first", "only when selected", "required plan-review message",
		"no synthetic handoff",
	} {
		if !strings.Contains(programs, want) {
			t.Errorf("program docs missing adaptive assignment contract %q", want)
		}
	}
}
