package cli

import "testing"

func TestWorkflowGoal(t *testing.T) {
	tests := []struct {
		workflow string
		want     string
	}{
		{
			workflow: "deliver-pr",
			want:     "Deliver the Relay deliver-pr workflow for project \"demo\". Use Relay project artifacts and `relay state` as the durable source of truth. Stop when the pull request is open.",
		},
		{
			workflow: "stack-ship",
			want:     "Deliver the Relay stack-ship workflow for project \"demo\". Use Relay project artifacts and `relay state` as the durable source of truth. Stop only when all acceptance criteria are met and all pull requests are merged.",
		},
		{workflow: "clarify"},
		{workflow: "plan"},
		{workflow: "review"},
		{workflow: "validate"},
		{workflow: "ship"},
		{workflow: ""},
	}

	for _, tt := range tests {
		t.Run(tt.workflow, func(t *testing.T) {
			if got := workflowGoal(tt.workflow, "demo"); got != tt.want {
				t.Errorf("workflowGoal(%q, %q) = %q, want %q", tt.workflow, "demo", got, tt.want)
			}
		})
	}
}
