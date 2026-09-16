package generate

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidationOwnershipContracts(t *testing.T) {
	root := repoRoot(t)
	implement := readFile(t, filepath.Join(root, "skills", "implement", "SKILL.md"))
	for _, want := range []string{
		"validation owner", "easy route", "relay state evidence record", "relay route refresh",
		"review owner", "mandatory review axes", "without a separate review worker",
		"targeted checks", "legacy seven-phase", "standard route",
		"may legitimately omit `plan`", "--dispatch-token", "route-selected specialist lens",
		"reusable exploration handoff in `route.md`",
	} {
		if !strings.Contains(implement, want) {
			t.Errorf("implement missing validation ownership %q", want)
		}
	}
	if strings.Contains(implement, "Run the full build and test suite once") {
		t.Error("implement still unconditionally owns the full suite")
	}

	validate := readFile(t, filepath.Join(root, "skills", "validate", "SKILL.md"))
	for _, want := range []string{
		"exact repository snapshot", "stale or missing gates",
		"relay state evidence record", "does not edit", "--no-gates",
		"route-less legacy",
	} {
		if !strings.Contains(validate, want) {
			t.Errorf("validate missing evidence ownership %q", want)
		}
	}

}

func TestReviewUsesRouteSelectedRolesAndMandatoryAxes(t *testing.T) {
	root := repoRoot(t)
	review := readFile(t, filepath.Join(root, "skills", "review", "SKILL.md"))
	for _, want := range []string{
		"route-selected roles", "correctness", "acceptance-criteria compliance",
		"scope/minimality", "clarity", "one general reviewer", "relay state evidence record",
		"Critical or Important", "code-reviewer", "<!-- relay-agent-reply -->",
		"route-less legacy", "independent review owner", "Every summary and inline comment",
	} {
		if !strings.Contains(review, want) {
			t.Errorf("review missing proportional contract %q", want)
		}
	}
	if strings.Contains(review, "Always run **code-reviewer**, **silent-failure-hunter**, **git-history**, and **prior-pr-history**") {
		t.Error("review retains unconditional role contract")
	}
}

func TestRouteAndOpenPROwnershipContracts(t *testing.T) {
	root := repoRoot(t)
	route := readFile(t, filepath.Join(root, "skills", "route", "SKILL.md"))
	for _, want := range []string{
		"review owner", "validation owner", "snapshot-bound evidence",
		"reopens independent `review` and `validate`",
		"exact required", "--gate <id>", "--no-repository-gates",
		"--changes-tests", "--changes-documentation-comments", "--changes-type-design",
		"--history-sensitive", "--changes-repository-guidelines", "normalized `task.md`",
		"`--stack-rationale` is required whenever the target is `stack-candidate`",
		"valid local-only branch or",
		"rebound to a real remote PR base before `open-pr` dispatch",
	} {
		if !strings.Contains(route, want) {
			t.Errorf("route missing evidence ownership contract %q", want)
		}
		for _, forbidden := range []string{
			"historical-review-no-op", "historical-validation-no-op", "historical-reduction-bps",
		} {
			if strings.Contains(route, forbidden) {
				t.Errorf("route retains historical eligibility flag %q", forbidden)
			}
		}
	}

	openPR := readFile(t, filepath.Join(root, "skills", "open-pr", "SKILL.md"))
	for _, want := range []string{
		"canonical owners", "exact current",
		"relay state evidence fresh", "performs no new review",
		"standalone use with no Relay state", "FinalResult", "relay state final",
		"current-route `open-pr` dispatch", "superseded dispatch",
	} {
		if !strings.Contains(openPR, want) {
			t.Errorf("open-pr missing exact evidence gate %q", want)
		}
	}
}

func TestSimplifyRequiresAnExplicitRouteTrigger(t *testing.T) {
	root := repoRoot(t)
	body := readFile(t, filepath.Join(root, "skills", "simplify", "SKILL.md"))
	for _, want := range []string{
		"selected by the persisted route", "author request", "duplication",
		"generated churn", "review finding", "targeted checks", "relay route refresh",
		"legacy seven-phase",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("simplify missing trigger contract %q", want)
		}
	}
	if strings.Contains(body, "Establish a baseline") {
		t.Error("simplify still requires an unconditional baseline suite")
	}
}
