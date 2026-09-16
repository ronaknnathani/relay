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
		if !strings.Contains(stack, "--name <child-project-slug> --base <parent-branch-or-commit> --no-launch") {
			t.Error("stack-ship does not pin the registered child creation command")
		}
		if strings.Contains(stack, "child owns its route, evidence, commits, PR, and watcher") {
			t.Error("stack child is incorrectly assigned watcher ownership")
		}
	}
	if strings.Contains(stack, "clarify → plan → implement → simplify → review → validate → open-pr") {
		t.Error("stack-ship hard-codes the retired fixed phase sequence")
	}

	cycle := readFile(t, filepath.Join(root, "skills", "stack-ship", "references", "pr-build-cycle.md"))
	if !strings.Contains(cycle, "route contract") || strings.Contains(cycle, "full single-PR pipeline") {
		t.Error("stack build cycle does not delegate adaptive routing")
	}
	for _, want := range []string{"terminal child status", "handoff_capability", "route advance"} {
		switch want {
		case "handoff_capability":
			monitor := readFile(t, filepath.Join(root, "skills", "pr-monitor", "SKILL.md"))
			if !strings.Contains(monitor, want) {
				t.Errorf("pr-monitor missing watcher source %q", want)
			}
		case "route advance":
			monitor := readFile(t, filepath.Join(root, "skills", "stack-ship", "references", "monitor-loop.md"))
			if !strings.Contains(monitor, "relay route advance") {
				t.Error("stack monitor loop does not atomically update the child manifest base")
			}
			advance := strings.LastIndex(monitor, "relay route advance")
			resume := strings.LastIndex(monitor, "relay resume <next-project-slug>")
			watch := strings.LastIndex(
				monitor,
				"relay pr watch start <next-project-slug> --mode stack --owner <stack-orchestrator-slug>",
			)
			if advance < 0 || resume < advance || watch < resume {
				t.Error("stack monitor loop starts watching before route-advance evidence is refreshed")
			}
		default:
			if !strings.Contains(cycle, want) {
				t.Errorf("stack build cycle missing %q", want)
			}
		}
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
