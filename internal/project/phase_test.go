package project

import "testing"

func TestPhaseToBatch(t *testing.T) {
	cases := map[string]string{
		"init":        "plan",
		"plan":        "plan",
		"discuss":     "plan",
		"implement":   "implement",
		"simplify":    "improve",
		"review":      "improve",
		"fix":         "improve",
		"validate":    "validate",
		"rebase":      "ship",
		"pr":          "ship",
		"ci":          "ship",
		"code-review": "ship",
		"done":        "done",
		"unknown":     "plan",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := PhaseToBatch(in); got != want {
				t.Errorf("PhaseToBatch(%q) = %q, want %q", in, got, want)
			}
		})
	}
}
