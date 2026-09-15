// Package project defines the relay project data model and on-disk storage.
package project

// Manifest is the on-disk representation of a relay project. JSON tags
// match the existing schema; do not rename without migrating existing files
// under ~/.relay.
type Manifest struct {
	Slug            string               `json:"slug"`
	Title           string               `json:"title"`
	Repo            string               `json:"repo"`
	Branch          string               `json:"branch"`
	Agent           string               `json:"agent,omitempty"`
	BaseBranch      string               `json:"base_branch,omitempty"`
	StartSHA        string               `json:"start_sha,omitempty"`
	Worktree        *string              `json:"worktree"`
	Status          string               `json:"status"`
	Workflow        string               `json:"workflow,omitempty"`
	Program         string               `json:"program,omitempty"`
	ProgramItem     string               `json:"program_item,omitempty"`
	Merged          bool                 `json:"merged,omitempty"`
	ArchiveCleanup  *ArchiveCleanupProof `json:"archive_cleanup,omitempty"`
	Phase           string               `json:"phase"`
	Created         string               `json:"created"`
	Updated         string               `json:"updated"`
	Archived        *string              `json:"archived"`
	PR              PRInfo               `json:"pr"`
	PhasesCompleted []string             `json:"phases_completed"`
	PhasesRemaining []string             `json:"phases_remaining"`
}

// ArchiveCleanupProof records the resource identities authorized for cleanup
// after project metadata has moved to the archive.
type ArchiveCleanupProof struct {
	Repository             string              `json:"repository"`
	Branch                 string              `json:"branch"`
	Worktree               string              `json:"worktree,omitempty"`
	BranchPresent          bool                `json:"branch_present"`
	ExpectedBranchTip      string              `json:"expected_branch_tip,omitempty"`
	BranchState            ArchiveCleanupState `json:"branch_state,omitempty"`
	WorktreePresent        bool                `json:"worktree_present"`
	ExpectedWorktreeTip    string              `json:"expected_worktree_tip,omitempty"`
	ExpectedWorktreeBranch string              `json:"expected_worktree_branch,omitempty"`
	WorktreeDetached       bool                `json:"worktree_detached,omitempty"`
	AuthoritativeCommit    string              `json:"authoritative_commit,omitempty"`
	WorktreeState          ArchiveCleanupState `json:"worktree_state,omitempty"`
}

// ArchiveCleanupState records whether one destructive cleanup authorization
// is available, has been consumed by an attempt, or is complete.
type ArchiveCleanupState string

const (
	ArchiveCleanupPending ArchiveCleanupState = "pending"
	ArchiveCleanupClaimed ArchiveCleanupState = "claimed"
	ArchiveCleanupDone    ArchiveCleanupState = "done"
)

type PRInfo struct {
	Number   *int    `json:"number"`
	URL      *string `json:"url"`
	CIStatus *string `json:"ci_status"`
}
