package project

// AllPhases lists the canonical phase progression for a new project.
var AllPhases = []string{
	"plan", "discuss", "implement", "simplify", "review",
	"fix", "validate", "rebase", "pr", "ci", "code-review",
}

// PhaseToBatch maps a phase to the user-facing batch name used to resume
// the project. Unknown phases default to "plan".
func PhaseToBatch(phase string) string {
	switch phase {
	case "init", "plan", "discuss":
		return "plan"
	case "implement":
		return "implement"
	case "simplify", "review", "fix":
		return "improve"
	case "validate":
		return "validate"
	case "rebase", "pr", "ci", "code-review":
		return "ship"
	case "done":
		return "done"
	default:
		return "plan"
	}
}
