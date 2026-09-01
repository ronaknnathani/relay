// Package prwatch observes one Relay project's pull request deterministically
// and wakes that project's exact live owner session when the pull request needs
// attention. It is strictly read-only toward git, GitHub, project workflow
// state, and program state: it never reruns a check, rebases, pushes, replies,
// resolves a thread, arms auto-merge, approves, or merges.
package prwatch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// SchemaVersion identifies the PR watcher runtime JSON contract.
const SchemaVersion = "relay.prwatch.v1"

// Mode selects which session owns the watcher's wakes.
type Mode string

// Watcher modes.
const (
	// ModeStandalone wakes the project's own session.
	ModeStandalone Mode = "standalone"
	// ModeManaged wakes the managed program's project worker, never the tech lead.
	ModeManaged Mode = "managed"
	// ModeStack wakes the stack orchestrator that owns the front project.
	ModeStack Mode = "stack"
)

// ParseMode validates one watcher mode.
func ParseMode(value string) (Mode, error) {
	switch Mode(value) {
	case ModeStandalone, ModeManaged, ModeStack:
		return Mode(value), nil
	}
	return "", fmt.Errorf(
		"invalid watcher mode %q: want standalone, managed, or stack", value,
	)
}

// Status is the watcher process lifecycle state.
type Status string

// Watcher lifecycle states.
const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	// StatusComplete means the pull request merged and the watcher finished.
	StatusComplete Status = "complete"
	StatusFailed   Status = "failed"
)

// Actionable reason codes. They are stable, payload-free, and safe to print.
const (
	ReasonFailingCheck     = "failing-check"
	ReasonChangesRequested = "changes-requested"
	ReasonNewComment       = "new-comment"
	ReasonNewReview        = "new-review"
	ReasonNewInlineComment = "new-inline-comment"
	ReasonUnresolvedThread = "unresolved-thread"
	ReasonMergeConflict    = "merge-conflict"
	ReasonStale            = "stale-base"
	// ReasonBlocked names a pull request GitHub refuses to merge for a reason
	// nothing else in the digest accounts for.
	ReasonBlocked           = "blocked"
	ReasonAutoMergeNotArmed = "auto-merge-not-armed"
	ReasonClosedUnmerged    = "closed-unmerged"
	ReasonStackFrontMerged  = "stack-front-merged"
)

// Waiting codes name a state the watcher observed that is deliberately not
// actionable, so a digest explains a quiet pull request instead of looking
// empty.
const (
	WaitingChecksPending  = "checks-pending"
	WaitingReviewRequired = "review-required"
	WaitingDraft          = "draft"
	WaitingAutoMergeArmed = "auto-merge-armed"
	WaitingMerged         = "merged"
	// WaitingBlocked reports a merge GitHub is holding for a reason the digest
	// already names, so a blocked pull request never looks unexplained twice.
	WaitingBlocked = "merge-blocked"
	// WaitingChangesRequestedAnswered reports a CHANGES_REQUESTED decision an
	// anchored Relay reply already answered on the exact review that requested
	// the changes. The pull request is still blocked and says so, but the next
	// move is the reviewer's: waking a writer again would rewrite work that is
	// already delivered and waiting to be looked at.
	WaitingChangesRequestedAnswered = "changes-requested-awaiting-rereview"
)

// Item sources name where one actionable item was observed.
const (
	SourceCheck         = "check"
	SourceComment       = "comment"
	SourceReview        = "review"
	SourceInlineComment = "inline-comment"
	SourceReviewThread  = "review-thread"
	SourceMerge         = "merge"
	SourceStack         = "stack"
)

// Item is one deterministically actionable observation. Key carries the exact
// source identity the fingerprint uses, so newer activity on a source the agent
// already answered is never hidden behind older activity.
type Item struct {
	Reason string `json:"reason"`
	Source string `json:"source"`
	ID     string `json:"id"`
	Key    string `json:"key"`
	// Answers is the exact reference an agent reply must name in its
	// `<!-- relay-agent-reply answers=... -->` marker to answer this item. It
	// is the item's own source and id, so a reply covers this item and nothing
	// else — not a sibling comment the digest never reported. Items nobody
	// replies to, such as a check or a merge state, carry none.
	Answers string `json:"answers,omitempty"`
	// Body is the human-authored text the woken owner needs. It lives only in
	// the mode 0600 digest and is never printed to watcher stdout or stderr.
	Body            string `json:"body,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	Author          string `json:"author,omitempty"`
	URL             string `json:"url,omitempty"`
	Path            string `json:"path,omitempty"`
	Line            int    `json:"line,omitempty"`
	ThreadID        string `json:"thread_id,omitempty"`
	ThreadResolved  bool   `json:"thread_resolved,omitempty"`
	CommentsTotal   int    `json:"comments_total,omitempty"`
	CheckName       string `json:"check_name,omitempty"`
	CheckRunID      string `json:"check_run_id,omitempty"`
	CheckStatus     string `json:"check_status,omitempty"`
	CheckConclusion string `json:"check_conclusion,omitempty"`
	DetailsURL      string `json:"details_url,omitempty"`
}

// PullRequest is the pull request metadata one observation recorded.
type PullRequest struct {
	Number           int    `json:"number"`
	URL              string `json:"url"`
	Title            string `json:"title"`
	State            string `json:"state"`
	Draft            bool   `json:"draft"`
	BaseRef          string `json:"base_ref"`
	BaseSHA          string `json:"base_sha,omitempty"`
	HeadRef          string `json:"head_ref"`
	HeadSHA          string `json:"head_sha"`
	MergeStateStatus string `json:"merge_state_status"`
	Mergeable        string `json:"mergeable"`
	ReviewDecision   string `json:"review_decision"`
	AutoMerge        bool   `json:"auto_merge"`
	Author           string `json:"author,omitempty"`
	DefaultBranch    string `json:"default_branch,omitempty"`
	Repo             string `json:"repo,omitempty"`
}

// Digest is the record of the newest observation that produced one fingerprint.
// It is the only place bodies are kept, and it is written mode 0600. Its item
// set is fixed by its fingerprint; every other field is refreshed on every
// observation so a reader never acts on stale pull request truth.
type Digest struct {
	Schema      string      `json:"schema"`
	Version     int         `json:"version"`
	Project     string      `json:"project"`
	Mode        Mode        `json:"mode"`
	Fingerprint string      `json:"fingerprint"`
	ObservedAt  string      `json:"observed_at"`
	HeadSHA     string      `json:"head_sha"`
	PR          PullRequest `json:"pr"`
	Items       []Item      `json:"items"`
	Waiting     []string    `json:"waiting"`
	// Complete reports that the pull request merged and no further attention is
	// possible for this project.
	Complete bool `json:"complete"`
}

// State is the watcher runtime record stored outside project directories.
type State struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	// Revision increases on every state write, so a running watcher can see
	// that another process changed its schedule.
	Revision  int    `json:"revision"`
	Project   string `json:"project"`
	Mode      Mode   `json:"mode"`
	OwnerSlug string `json:"owner_slug"`
	PID       int    `json:"pid"`
	// TabID and PaneID name the Herdr tab and pane hosting the watcher, so
	// stopping one closes the exact pane it started instead of guessing at one.
	TabID               string `json:"tab_id,omitempty"`
	PaneID              string `json:"pane_id,omitempty"`
	RelayVersion        string `json:"relay_version"`
	Status              Status `json:"status"`
	StartedAt           string `json:"started_at"`
	ScheduledChecks     int    `json:"scheduled_checks"`
	LastCheckAt         string `json:"last_check_at"`
	NextCheckAt         string `json:"next_check_at"`
	DelaySeconds        int64  `json:"delay_seconds"`
	PRNumber            int    `json:"pr_number"`
	PRURL               string `json:"pr_url,omitempty"`
	PRState             string `json:"pr_state,omitempty"`
	HeadSHA             string `json:"head_sha,omitempty"`
	CurrentFingerprint  string `json:"current_fingerprint"`
	ActionableCount     int    `json:"actionable_count"`
	AttentionPending    bool   `json:"attention_pending"`
	LastWakeAt          string `json:"last_wake_at,omitempty"`
	LastWakeStatus      string `json:"last_wake_status,omitempty"`
	LastWakeFingerprint string `json:"last_wake_fingerprint,omitempty"`
	// WakesSuppressed records an uncertain prompt delivery. Automatic wakes stay
	// suppressed until the watcher is restarted, because a retry can duplicate
	// text in the owner's composer.
	WakesSuppressed   bool   `json:"wakes_suppressed"`
	ConsecutiveErrors int    `json:"consecutive_errors"`
	Error             string `json:"error,omitempty"`
	Warning           string `json:"warning,omitempty"`
	StopReason        string `json:"stop_reason,omitempty"`
	UpdatedAt         string `json:"updated_at"`
}

// Cadence delays. Scheduled checks 1-4 run every 15 minutes, 5-6 every 30, and
// 7 onward every 60. The immediate observation a watcher runs at start is not a
// scheduled check.
const (
	FastCadence   = 15 * time.Minute
	MediumCadence = 30 * time.Minute
	SlowCadence   = 60 * time.Minute
)

// CadenceFor returns the delay before scheduled check number check, counting
// from one.
func CadenceFor(check int) time.Duration {
	switch {
	case check <= 4:
		return FastCadence
	case check <= 6:
		return MediumCadence
	default:
		return SlowCadence
	}
}

// ItemKeys returns the sorted unique fingerprint keys of items.
func ItemKeys(items []Item) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		if item.Key == "" {
			continue
		}
		keys = append(keys, item.Key)
	}
	sort.Strings(keys)
	return slices.Compact(keys)
}

// Fingerprint is the SHA-256 of the sorted unique item keys. It never covers a
// body, so it is stable across re-observation of the same activity. No
// actionable item produces the empty fingerprint.
func Fingerprint(items []Item) string {
	keys := ItemKeys(items)
	if len(keys) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])
}

var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidateFingerprint accepts exactly one lowercase 64-character hex digest.
// A fingerprint names a runtime file, so anything else is rejected before it
// reaches the filesystem.
func ValidateFingerprint(fingerprint string) error {
	if fingerprint == "" {
		return fmt.Errorf("fingerprint is empty: want 64 lowercase hex characters")
	}
	if !fingerprintPattern.MatchString(fingerprint) {
		return fmt.Errorf(
			"invalid fingerprint %q: want exactly 64 lowercase hex characters", fingerprint,
		)
	}
	return nil
}
