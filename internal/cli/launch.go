package cli

import "fmt"

func workflowGoal(workflow, slug string) string {
	switch workflow {
	case "deliver-pr":
		return fmt.Sprintf("Deliver the Relay deliver-pr workflow for project %q. Use Relay project artifacts and `relay state` as the durable source of truth. Stop when the pull request is open.", slug)
	case "stack-ship":
		return fmt.Sprintf("Deliver the Relay stack-ship workflow for project %q. Use Relay project artifacts and `relay state` as the durable source of truth. Stop only when all acceptance criteria are met and all pull requests are merged.", slug)
	default:
		return ""
	}
}
