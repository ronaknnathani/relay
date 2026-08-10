// Package project defines the relay project data model and on-disk storage.
package project

// Manifest is the on-disk representation of a relay project. JSON tags
// match the existing schema; do not rename without migrating existing files
// under ~/.relay.
type Manifest struct {
	Slug            string   `json:"slug"`
	Title           string   `json:"title"`
	Repo            string   `json:"repo"`
	Branch          string   `json:"branch"`
	Agent           string   `json:"agent,omitempty"`
	BaseBranch      string   `json:"base_branch,omitempty"`
	StartSHA        string   `json:"start_sha,omitempty"`
	Worktree        *string  `json:"worktree"`
	Status          string   `json:"status"`
	Workflow        string   `json:"workflow,omitempty"`
	Phase           string   `json:"phase"`
	Created         string   `json:"created"`
	Updated         string   `json:"updated"`
	Archived        *string  `json:"archived"`
	PR              PRInfo   `json:"pr"`
	PhasesCompleted []string `json:"phases_completed"`
	PhasesRemaining []string `json:"phases_remaining"`
}

type PRInfo struct {
	Number   *int    `json:"number"`
	URL      *string `json:"url"`
	CIStatus *string `json:"ci_status"`
}
