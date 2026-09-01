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

// AgentReplyMarker is the exact hidden marker every automated Relay reply to a
// pull request carries on a line of its own, immediately before its visible
// "🤖 <agent> on behalf of <author>" disclosure. It is the only signal that
// tells an agent reply from a human one.
//
// It is a machine token, not prose: an emoji, a phrase, or an author login can
// all be typed by a human quoting or joking about a bot, and any of those
// mistaken for an agent reply would silence live review feedback. Requiring the
// marker to start its line means quoting an earlier agent reply — which
// markdown renders as "> <!-- relay-agent-reply -->" — is still human activity.
const AgentReplyMarker = "<!-- relay-agent-reply -->"

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
// items a woken owner must handle. It is pure: the same observation and mode
// always produce the same items and fingerprint. Nothing about a previous
// observation is carried in, so an item disappears only when the current remote
// truth no longer shows it.
func BuildDigest(slug string, mode Mode, observation Observation, now time.Time) Digest {
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

	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	if items == nil {
		items = []Item{}
	}
	digest.Items = items
	digest.Fingerprint = Fingerprint(items)
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
	items = append(items, classifyConversation(observation)...)
	items = append(items, classifyThreads(observation.Threads)...)
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
// the agent has not answered on that exact source.
//
// Each source is reconciled on its own. A conversation comment is answered only
// by a later agent comment in the conversation, and a review body only by a
// later agent review; a reply left anywhere else on the pull request answers
// neither. Reconciling them together let one reply mark several independent
// pieces of feedback as handled, which silently dropped review comments nobody
// ever addressed.
func classifyConversation(observation Observation) []Item {
	answered := latestAgentReply(observation.Comments)
	var items []Item
	for _, comment := range observation.Comments {
		if !isHuman(comment) || answeredBy(answered, comment.UpdatedAt) {
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
	answeredReviews := latestAgentReply(observation.Reviews)
	for _, review := range observation.Reviews {
		if strings.TrimSpace(review.Body) == "" {
			continue
		}
		if !isHuman(review) || answeredBy(answeredReviews, review.UpdatedAt) {
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
	items = append(items, classifyThreadlessInline(observation)...)
	return items
}

// inlineThreadRoots maps every inline comment to the comment that opened its
// thread, following GitHub's in-reply-to chain. A comment that replies to
// nothing is its own root, and a chain that cannot be resolved — a reply whose
// parent was not returned, or a cycle — stops at the last id it reached.
func inlineThreadRoots(comments []Activity) map[string]string {
	parent := make(map[string]string, len(comments))
	for _, comment := range comments {
		if comment.InReplyTo != "" && comment.InReplyTo != comment.ID {
			parent[comment.ID] = comment.InReplyTo
		}
	}
	roots := make(map[string]string, len(comments))
	for _, comment := range comments {
		root := comment.ID
		for step := 0; step < len(comments); step++ {
			next, found := parent[root]
			if !found {
				break
			}
			root = next
		}
		roots[comment.ID] = root
	}
	return roots
}

// classifyThreadlessInline covers inline comments GitHub did not return in a
// review thread, so an inline comment is never silently dropped. Each inline
// thread is reconciled against the agent replies chained onto that same root
// comment, so answering one file's comment never answers another's.
func classifyThreadlessInline(observation Observation) []Item {
	threaded := make(map[string]bool)
	for _, thread := range observation.Threads {
		for _, comment := range thread.Comments {
			threaded[comment.ID] = true
		}
	}
	roots := inlineThreadRoots(observation.InlineComments)
	answered := make(map[string]string, len(roots))
	for _, comment := range observation.InlineComments {
		if !isAgentReply(comment) {
			continue
		}
		root := roots[comment.ID]
		if newerThan(comment.UpdatedAt, answered[root]) {
			answered[root] = comment.UpdatedAt
		}
	}
	var items []Item
	for _, comment := range observation.InlineComments {
		if threaded[comment.ID] {
			continue
		}
		if !isHuman(comment) || answeredBy(answered[roots[comment.ID]], comment.UpdatedAt) {
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
// after the agent answered it. A thread is reconciled only against its own
// replies.
func classifyThreads(threads []ReviewThread) []Item {
	var items []Item
	for _, thread := range threads {
		latest, found := latestActivity(thread.Comments)
		if !found || !isHuman(latest) {
			continue
		}
		if thread.IsResolved && answeredBy(latestAgentReply(thread.Comments), latest.UpdatedAt) {
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
// human, and neither is a reply Relay posted, which carries the exact marker.
func isHuman(activity Activity) bool {
	return !activity.Author.Bot && !isAgentReply(activity)
}

// isAgentReply reports whether one activity is a Relay agent reply, which is
// true exactly when the marker starts one of its lines.
func isAgentReply(activity Activity) bool {
	if activity.Author.Bot {
		return false
	}
	for _, line := range strings.Split(activity.Body, "\n") {
		if strings.HasPrefix(line, AgentReplyMarker) {
			return true
		}
	}
	return false
}

// latestAgentReply returns the timestamp of the newest Relay agent reply in one
// activity stream. Only the stream it is given is consulted, so an answer
// posted somewhere else on the pull request never counts as an answer here.
func latestAgentReply(activities []Activity) string {
	latest := ""
	for _, activity := range activities {
		if isAgentReply(activity) && newerThan(activity.UpdatedAt, latest) {
			latest = activity.UpdatedAt
		}
	}
	return latest
}

// answeredBy reports whether an agent reply at replyAt answers human activity
// at activityAt. Equal timestamps count: GitHub reports seconds, and a reply
// written in the same second as the comment it answers is still an answer.
func answeredBy(replyAt, activityAt string) bool {
	if replyAt == "" {
		return false
	}
	return !newerThan(activityAt, replyAt)
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
