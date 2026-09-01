package prwatch

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/project"
)

// agentDisclosureMarker is the automated-agent prefix every Relay reply
// carries. Combined with the pull request author's login it is the only signal
// used to tell an agent reply from a human one; nothing about the text is
// interpreted semantically.
const agentDisclosureMarker = "🤖"

// failingConclusions are the check conclusions that always need attention. A
// watcher never decides whether a failure is an infrastructure flake or a real
// one; that judgment belongs to the woken owner.
var failingConclusions = map[string]bool{
	"FAILURE":         true,
	"ERROR":           true,
	"TIMED_OUT":       true,
	"ACTION_REQUIRED": true,
	"STARTUP_FAILURE": true,
}

// pendingStatuses are the check statuses that are deliberately not actionable.
var pendingStatuses = map[string]bool{
	"QUEUED":      true,
	"IN_PROGRESS": true,
	"PENDING":     true,
	"WAITING":     true,
	"REQUESTED":   true,
	"EXPECTED":    true,
}

// Target names the project pull request one watcher observes.
type Target struct {
	Slug     string
	Dir      string
	PRNumber int
	PRURL    string
}

// LoadTarget resolves the pull request a project recorded and the directory gh
// runs in. Workflow state is the source of truth, with the manifest as the
// fallback, matching how the rest of Relay reads a project's pull request.
func LoadTarget(slug string) (Target, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return Target{}, fmt.Errorf("pr watch project slug: %w", err)
	}
	path, err := project.Find(slug)
	if err != nil {
		return Target{}, fmt.Errorf("pr watch target: %w", err)
	}
	manifest, err := project.Load(path)
	if err != nil {
		return Target{}, err
	}
	target := Target{Slug: slug, Dir: manifest.Repo}
	if manifest.Worktree != nil && strings.TrimSpace(*manifest.Worktree) != "" {
		target.Dir = *manifest.Worktree
	}
	state, err := project.LoadState(project.StatePath(slug))
	switch {
	case err == nil:
		target.PRNumber = state.PR.Number
		target.PRURL = state.PR.URL
	case !errors.Is(err, os.ErrNotExist):
		return Target{}, err
	}
	if target.PRNumber == 0 && manifest.PR.Number != nil {
		target.PRNumber = *manifest.PR.Number
	}
	if target.PRURL == "" && manifest.PR.URL != nil {
		target.PRURL = *manifest.PR.URL
	}
	if target.PRNumber <= 0 {
		return Target{}, fmt.Errorf(
			"project %q has no recorded pull request; open one with `deliver-pr` and record it with "+
				"`relay state pr %s --number <n> --url <url>` before watching it",
			slug, slug,
		)
	}
	if strings.TrimSpace(target.Dir) == "" {
		return Target{}, fmt.Errorf("project %q records neither a worktree nor a repository path", slug)
	}
	return target, nil
}

// BuildDigest classifies one observation into the deterministically actionable
// items a woken owner must handle. It is pure: the same observation, mode, and
// acknowledgement watermark always produce the same digest and fingerprint.
func BuildDigest(slug string, mode Mode, observation Observation, state State, now time.Time) Digest {
	digest := Digest{
		Schema:     SchemaVersion,
		Version:    1,
		Project:    slug,
		Mode:       mode,
		ObservedAt: now.UTC().Format(time.RFC3339),
		HeadSHA:    observation.PR.HeadSHA,
		PR:         observation.PR,
		Items:      []Item{},
		Waiting:    []string{},
	}
	items, waiting := classify(mode, observation)
	digest.Waiting = waiting
	digest.Complete = observation.PR.State == "MERGED" && mode != ModeStack

	unacknowledged := make([]Item, 0, len(items))
	for _, item := range items {
		if state.Acknowledged(item.Key) {
			continue
		}
		unacknowledged = append(unacknowledged, item)
	}
	sort.Slice(unacknowledged, func(i, j int) bool { return unacknowledged[i].Key < unacknowledged[j].Key })
	digest.Items = unacknowledged
	digest.Fingerprint = Fingerprint(unacknowledged)
	return digest
}

func classify(mode Mode, observation Observation) ([]Item, []string) {
	pr := observation.PR
	switch pr.State {
	case "MERGED":
		// A merged pull request ends the watch for a single project. Only a
		// stack front-merge is actionable, because the orchestrator has to
		// retarget the next pull request onto the default branch.
		if mode != ModeStack {
			return nil, []string{WaitingMerged}
		}
		return []Item{{
			Reason: ReasonStackFrontMerged,
			Source: SourceStack,
			ID:     strconv.Itoa(pr.Number),
			Key:    fmt.Sprintf("stack-front-merged:%d:%s", pr.Number, pr.HeadSHA),
		}}, []string{WaitingMerged}
	case "CLOSED":
		return []Item{{
			Reason: ReasonClosedUnmerged,
			Source: SourceMerge,
			ID:     strconv.Itoa(pr.Number),
			Key:    fmt.Sprintf("closed-unmerged:%d:%s", pr.Number, pr.HeadSHA),
		}}, nil
	}

	var items []Item
	var waiting []string
	failing, pending := classifyChecks(pr, observation.Checks)
	items = append(items, failing...)
	if pending {
		waiting = append(waiting, WaitingChecksPending)
	}
	items = append(items, classifyReviewDecision(pr, observation.Reviews)...)
	items = append(items, classifyConversation(pr, observation)...)
	items = append(items, classifyThreads(pr, observation.Threads)...)
	items = append(items, classifyMergeState(pr, len(failing) > 0, pending)...)

	if pr.Draft {
		waiting = append(waiting, WaitingDraft)
	}
	if pr.ReviewDecision == "REVIEW_REQUIRED" {
		waiting = append(waiting, WaitingReviewRequired)
	}
	if pr.AutoMerge {
		waiting = append(waiting, WaitingAutoMergeArmed)
	}
	return items, waiting
}

func classifyChecks(pr PullRequest, checks []Check) ([]Item, bool) {
	var items []Item
	pending := false
	for _, check := range checks {
		if pendingStatuses[check.Status] && !failingConclusions[check.Conclusion] {
			pending = true
			continue
		}
		if !failingConclusions[check.Conclusion] {
			continue
		}
		items = append(items, Item{
			Reason:          ReasonFailingCheck,
			Source:          SourceCheck,
			ID:              check.Name,
			Key:             fmt.Sprintf("check:%s:%s:%s:%s", check.Name, check.RunID, check.Conclusion, pr.HeadSHA),
			CheckName:       check.Name,
			CheckRunID:      check.RunID,
			CheckStatus:     check.Status,
			CheckConclusion: check.Conclusion,
			DetailsURL:      check.DetailsURL,
		})
	}
	return items, pending
}

func classifyReviewDecision(pr PullRequest, reviews []Activity) []Item {
	if pr.ReviewDecision != "CHANGES_REQUESTED" {
		return nil
	}
	item := Item{
		Reason: ReasonChangesRequested,
		Source: SourceReview,
		ID:     strconv.Itoa(pr.Number),
		Key:    fmt.Sprintf("changes-requested:%d:%s", pr.Number, pr.HeadSHA),
	}
	if latest, found := latestReviewWithState(reviews, "CHANGES_REQUESTED"); found {
		item.ID = latest.ID
		item.Key = fmt.Sprintf("changes-requested:%s:%s", latest.ID, latest.UpdatedAt)
		item.Body = latest.Body
		item.Author = latest.Author.Login
		item.UpdatedAt = latest.UpdatedAt
		item.URL = latest.URL
	}
	return []Item{item}
}

// classifyConversation reports human conversation comments and review bodies
// whose activity is newer than the agent's most recent reply. Anything the
// agent already answered is waiting on the human, not on Relay.
func classifyConversation(pr PullRequest, observation Observation) []Item {
	answered := latestAgentActivity(pr.Author, observation.Comments, observation.Reviews, observation.InlineComments)
	var items []Item
	for _, comment := range observation.Comments {
		if !isHuman(comment, pr.Author) || !newerThan(comment.UpdatedAt, answered) {
			continue
		}
		items = append(items, Item{
			Reason:    ReasonNewComment,
			Source:    SourceComment,
			ID:        comment.ID,
			Key:       fmt.Sprintf("comment:%s:%s", comment.ID, comment.UpdatedAt),
			Body:      comment.Body,
			Author:    comment.Author.Login,
			UpdatedAt: comment.UpdatedAt,
			URL:       comment.URL,
		})
	}
	for _, review := range observation.Reviews {
		if strings.TrimSpace(review.Body) == "" {
			continue
		}
		if !isHuman(review, pr.Author) || !newerThan(review.UpdatedAt, answered) {
			continue
		}
		items = append(items, Item{
			Reason:    ReasonNewReview,
			Source:    SourceReview,
			ID:        review.ID,
			Key:       fmt.Sprintf("review:%s:%s", review.ID, review.UpdatedAt),
			Body:      review.Body,
			Author:    review.Author.Login,
			UpdatedAt: review.UpdatedAt,
			URL:       review.URL,
		})
	}
	items = append(items, classifyThreadlessInline(pr, observation)...)
	return items
}

// classifyThreadlessInline covers inline comments GitHub did not return in a
// review thread, so an inline comment is never silently dropped.
func classifyThreadlessInline(pr PullRequest, observation Observation) []Item {
	threaded := make(map[string]bool)
	for _, thread := range observation.Threads {
		for _, comment := range thread.Comments {
			threaded[comment.ID] = true
		}
	}
	answered := latestAgentActivity(pr.Author, observation.Comments, observation.Reviews, observation.InlineComments)
	var items []Item
	for _, comment := range observation.InlineComments {
		if threaded[comment.ID] {
			continue
		}
		if !isHuman(comment, pr.Author) || !newerThan(comment.UpdatedAt, answered) {
			continue
		}
		items = append(items, Item{
			Reason:    ReasonNewInlineComment,
			Source:    SourceInlineComment,
			ID:        comment.ID,
			Key:       fmt.Sprintf("inline-comment:%s:%s", comment.ID, comment.UpdatedAt),
			Body:      comment.Body,
			Author:    comment.Author.Login,
			UpdatedAt: comment.UpdatedAt,
			URL:       comment.URL,
			Path:      comment.Path,
			Line:      comment.Line,
		})
	}
	return items
}

// classifyThreads reports review threads whose latest activity is human: every
// unresolved thread, and a resolved thread that received a new human reply
// after the agent answered it.
func classifyThreads(pr PullRequest, threads []ReviewThread) []Item {
	var items []Item
	for _, thread := range threads {
		latest, found := latestActivity(thread.Comments)
		if !found || !isHuman(latest, pr.Author) {
			continue
		}
		if thread.IsResolved && !newerThan(latest.UpdatedAt, latestAgentActivity(pr.Author, thread.Comments)) {
			continue
		}
		items = append(items, Item{
			Reason:         ReasonUnresolvedThread,
			Source:         SourceReviewThread,
			ID:             thread.ID,
			Key:            fmt.Sprintf("thread:%s:%s:%s", thread.ID, latest.ID, latest.UpdatedAt),
			Body:           latest.Body,
			Author:         latest.Author.Login,
			UpdatedAt:      latest.UpdatedAt,
			URL:            latest.URL,
			Path:           firstNonEmpty(latest.Path, thread.Path),
			Line:           firstPositive(latest.Line, thread.Line),
			ThreadID:       thread.ID,
			ThreadResolved: thread.IsResolved,
			CommentsTotal:  thread.CommentsTotal,
		})
	}
	return items
}

func classifyMergeState(pr PullRequest, failingChecks, pendingChecks bool) []Item {
	var items []Item
	if pr.MergeStateStatus == "DIRTY" || pr.Mergeable == "CONFLICTING" {
		items = append(items, Item{
			Reason: ReasonMergeConflict,
			Source: SourceMerge,
			ID:     strconv.Itoa(pr.Number),
			Key:    fmt.Sprintf("merge-conflict:%s:%s", pr.HeadSHA, pr.BaseSHA),
		})
	}
	if pr.MergeStateStatus == "BEHIND" {
		items = append(items, Item{
			Reason: ReasonStale,
			Source: SourceMerge,
			ID:     strconv.Itoa(pr.Number),
			Key:    fmt.Sprintf("stale-base:%s:%s", pr.HeadSHA, pr.BaseSHA),
		})
	}
	readyToArm := pr.ReviewDecision == "APPROVED" &&
		!pr.Draft &&
		!failingChecks &&
		!pendingChecks &&
		!pr.AutoMerge &&
		pr.MergeStateStatus == "CLEAN" &&
		pr.BaseRef != "" &&
		pr.BaseRef == pr.DefaultBranch
	if readyToArm {
		items = append(items, Item{
			Reason: ReasonAutoMergeNotArmed,
			Source: SourceMerge,
			ID:     strconv.Itoa(pr.Number),
			Key:    fmt.Sprintf("auto-merge-not-armed:%s", pr.HeadSHA),
		})
	}
	return items
}

// isHuman reports whether one activity needs a response. A GitHub app is not a
// human, and neither is a reply the agent posted on the author's behalf, which
// is identified by the automated-agent disclosure the reply must carry.
func isHuman(activity Activity, prAuthor string) bool {
	return !activity.Author.Bot && !isAgentOwned(activity, prAuthor)
}

func isAgentOwned(activity Activity, prAuthor string) bool {
	if activity.Author.Bot {
		return false
	}
	return prAuthor != "" &&
		activity.Author.Login == prAuthor &&
		strings.Contains(activity.Body, agentDisclosureMarker)
}

// latestAgentActivity returns the timestamp of the newest agent-owned reply
// across the given activity lists.
func latestAgentActivity(prAuthor string, lists ...[]Activity) string {
	latest := ""
	for _, list := range lists {
		for _, activity := range list {
			if isAgentOwned(activity, prAuthor) && newerThan(activity.UpdatedAt, latest) {
				latest = activity.UpdatedAt
			}
		}
	}
	return latest
}

func latestActivity(activities []Activity) (Activity, bool) {
	var latest Activity
	found := false
	for _, activity := range activities {
		if !found || newerThan(activity.UpdatedAt, latest.UpdatedAt) {
			latest = activity
			found = true
		}
	}
	return latest, found
}

func latestReviewWithState(reviews []Activity, state string) (Activity, bool) {
	matching := make([]Activity, 0, len(reviews))
	for _, review := range reviews {
		if review.State == state {
			matching = append(matching, review)
		}
	}
	return latestActivity(matching)
}

// newerThan compares two GitHub timestamps. Both are RFC 3339; an unparseable
// value falls back to a lexical comparison so an unexpected format still yields
// a deterministic answer.
func newerThan(left, right string) bool {
	if right == "" {
		return left != ""
	}
	if left == "" {
		return false
	}
	leftTime, leftErr := time.Parse(time.RFC3339, left)
	rightTime, rightErr := time.Parse(time.RFC3339, right)
	if leftErr == nil && rightErr == nil {
		return leftTime.After(rightTime)
	}
	return left > right
}
