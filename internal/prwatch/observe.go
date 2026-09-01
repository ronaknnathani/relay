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

// AgentReplyMarker is the legacy bare marker: the hidden token automated Relay
// replies carried before they named what they answered. It is still recognized
// as an agent reply, but on its own it answers only the one activity GitHub
// itself chains it to — see legacyAnswers.
//
// The marker is a machine token, not prose: an emoji, a phrase, or an author
// login can all be typed by a human quoting or joking about a bot, and any of
// those mistaken for an agent reply would silence live review feedback.
// Requiring the marker to start its line means quoting an earlier agent reply —
// which markdown renders as "> <!-- relay-agent-reply -->" — is still human
// activity.
const AgentReplyMarker = "<!-- relay-agent-reply -->"

// The anchored marker every automated Relay reply carries on a line of its own,
// immediately before its visible "🤖 <agent> on behalf of <author>" disclosure:
//
//	<!-- relay-agent-reply answers=comment:200 -->
//
// The reference is the exact activity the reply answers, which is the source
// and id the digest item carries in its Answers token. Anchoring on an id
// rather than on a timestamp is what keeps a reply from hiding feedback the
// watcher never showed anybody: a reply to comment 200 says nothing about
// comment 201, whenever either was written.
const (
	agentReplyMarkerPrefix = "<!-- relay-agent-reply"
	agentReplyMarkerEnd    = "-->"
	agentReplyAnswersField = "answers="
)

// AgentReplyMarkerFor renders the exact marker line a reply answering one
// digest item's Answers token must carry.
func AgentReplyMarkerFor(answers string) string {
	return agentReplyMarkerPrefix + " " + agentReplyAnswersField + answers + " " + agentReplyMarkerEnd
}

// AnswerRef renders the marker reference that answers one source and id. It is
// what a digest item carries in Answers and what pr-fix copies verbatim into
// the marker it posts.
func AnswerRef(source, id string) string {
	return source + ":" + id
}

// threadAnswerRef renders the reference for one comment inside a review thread.
// It names the thread and the exact comment the digest reported, because a
// thread is one item whose identity moves with its newest unanswered comment: a
// reference to the thread alone would answer replies the digest never showed.
func threadAnswerRef(threadID, commentID string) string {
	return AnswerRef(SourceReviewThread, threadID+":"+commentID)
}

// failingConclusions are the check conclusions that always need attention. A
// watcher never decides whether a failure is an infrastructure flake or a real
// one; that judgment belongs to the woken owner.
//
// GitHub's canceled and stale conclusions are here because a required check
// that ends either way never reports a result, so the pull request cannot merge
// until somebody reruns it — a silent stall is exactly what a watcher exists to
// catch. NEUTRAL and SKIPPED are deliberately absent: GitHub counts both as
// satisfying a required check, so neither blocks a merge.
var failingConclusions = map[string]bool{
	"FAILURE":         true,
	"ERROR":           true,
	"TIMED_OUT":       true,
	"ACTION_REQUIRED": true,
	"STARTUP_FAILURE": true,
	"CANCELLED":       true, //nolint:misspell // GitHub check conclusion value.
	"STALE":           true,
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
	changes, changesWaiting := classifyReviewDecision(pr, observation.Reviews)
	items = append(items, changes...)
	waiting = append(waiting, changesWaiting...)
	items = append(items, classifyConversation(observation)...)
	items = append(items, classifyThreads(observation.Threads)...)
	merge := classifyMergeState(pr, len(failing) > 0, pending)
	items = append(items, merge...)

	if pr.MergeStateStatus == "BLOCKED" {
		if blockExplained(pr, len(failing) > 0, pending, merge) {
			waiting = append(waiting, WaitingBlocked)
		} else {
			items = append(items, Item{
				Reason: ReasonBlocked,
				Source: SourceMerge,
				ID:     strconv.Itoa(pr.Number),
				Key:    fmt.Sprintf("blocked:%s", pr.HeadSHA),
			})
		}
	}
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

// blockExplained reports whether something else the digest already names
// accounts for GitHub refusing to merge. A block nothing explains — approved,
// green, unconflicted, not a draft, and still unmergeable — is invisible
// otherwise: the owner sees a healthy pull request that never merges, and the
// watcher would keep reporting it as quiet forever.
func blockExplained(pr PullRequest, failingChecks, pendingChecks bool, mergeItems []Item) bool {
	if failingChecks || pendingChecks || pr.Draft {
		return true
	}
	switch pr.ReviewDecision {
	case "REVIEW_REQUIRED", "CHANGES_REQUESTED":
		return true
	}
	for _, item := range mergeItems {
		if item.Reason == ReasonMergeConflict || item.Reason == ReasonStale {
			return true
		}
	}
	return false
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

// classifyReviewDecision reports a CHANGES_REQUESTED review decision against
// the exact review that requested the changes.
//
// GitHub holds that decision until the same reviewer submits another review, so
// reporting the decision itself as actionable woke the owner — and started
// another writer — on every check, forever, for work already delivered. Once an
// anchored Relay review answers that exact review, the decision is reported as
// waiting instead: honestly still blocked, and nobody's turn but the reviewer's.
//
// Only current remote evidence takes it off waiting: a newer review, a human
// edit of that review, or a decision GitHub no longer reports as
// CHANGES_REQUESTED. Pushing a new head is not evidence — the reviewer has not
// looked at it yet.
func classifyReviewDecision(pr PullRequest, reviews []Activity) ([]Item, []string) {
	if pr.ReviewDecision != "CHANGES_REQUESTED" {
		return nil, nil
	}
	latest, found := latestReviewWithState(reviews, "CHANGES_REQUESTED")
	if !found {
		// GitHub reports the decision but not the review behind it, so there is
		// no identity to answer and nothing to reconcile against.
		return []Item{{
			Reason: ReasonChangesRequested,
			Source: SourceReview,
			ID:     strconv.Itoa(pr.Number),
			Key:    fmt.Sprintf("changes-requested:%d:%s", pr.Number, pr.HeadSHA),
		}}, nil
	}
	ref := AnswerRef(SourceReview, latest.ID)
	if answeredBy(answeredRefs(reviews)[ref], latest.UpdatedAt) {
		return nil, []string{WaitingChangesRequestedAnswered}
	}
	return []Item{{
		Reason:    ReasonChangesRequested,
		Source:    SourceReview,
		ID:        latest.ID,
		Key:       fmt.Sprintf("changes-requested:%s:%s", latest.ID, latest.UpdatedAt),
		Answers:   ref,
		Body:      latest.Body,
		Author:    latest.Author.Login,
		UpdatedAt: latest.UpdatedAt,
		URL:       latest.URL,
	}}, nil
}

// classifyConversation reports human conversation comments and review bodies
// the agent has not answered on that exact source.
//
// Each source is reconciled on its own, and each activity on its own id. A
// conversation comment is answered only by a later agent comment whose marker
// names that comment, and a review body only by a later agent review whose
// marker names that review. Reconciling a whole stream against one "latest
// agent reply" timestamp let a reply to one comment hide every other comment
// written before it — including feedback no digest had ever reported.
func classifyConversation(observation Observation) []Item {
	answered := answeredRefs(observation.Comments)
	var items []Item
	for _, comment := range observation.Comments {
		ref := AnswerRef(SourceComment, comment.ID)
		if !isHuman(comment) || answeredBy(answered[ref], comment.UpdatedAt) {
			continue
		}
		items = append(items, Item{
			Reason:    ReasonNewComment,
			Source:    SourceComment,
			ID:        comment.ID,
			Key:       fmt.Sprintf("comment:%s:%s", comment.ID, comment.UpdatedAt),
			Answers:   ref,
			Body:      comment.Body,
			Author:    comment.Author.Login,
			UpdatedAt: comment.UpdatedAt,
			URL:       comment.URL,
		})
	}
	answeredReviews := answeredRefs(observation.Reviews)
	for _, review := range observation.Reviews {
		if strings.TrimSpace(review.Body) == "" {
			continue
		}
		ref := AnswerRef(SourceReview, review.ID)
		if !isHuman(review) || answeredBy(answeredReviews[ref], review.UpdatedAt) {
			continue
		}
		items = append(items, Item{
			Reason:    ReasonNewReview,
			Source:    SourceReview,
			ID:        review.ID,
			Key:       fmt.Sprintf("review:%s:%s", review.ID, review.UpdatedAt),
			Answers:   ref,
			Body:      review.Body,
			Author:    review.Author.Login,
			UpdatedAt: review.UpdatedAt,
			URL:       review.URL,
		})
	}
	items = append(items, classifyThreadlessInline(observation)...)
	return items
}

// classifyThreadlessInline covers inline comments GitHub did not return in a
// review thread, so an inline comment is never silently dropped. Each inline
// comment is reconciled against the agent replies that name it, so answering
// one file's comment never answers another's.
func classifyThreadlessInline(observation Observation) []Item {
	threaded := make(map[string]bool)
	for _, thread := range observation.Threads {
		for _, comment := range thread.Comments {
			threaded[comment.ID] = true
		}
	}
	answered := answeredRefs(observation.InlineComments)
	legacy := legacyAnswers(observation.InlineComments)
	var items []Item
	for _, comment := range observation.InlineComments {
		if threaded[comment.ID] || !isHuman(comment) {
			continue
		}
		ref := AnswerRef(SourceInlineComment, comment.ID)
		if answeredBy(answered[ref], comment.UpdatedAt) ||
			answeredBy(legacy[comment.ID], comment.UpdatedAt) {
			continue
		}
		items = append(items, Item{
			Reason:    ReasonNewInlineComment,
			Source:    SourceInlineComment,
			ID:        comment.ID,
			Key:       fmt.Sprintf("inline-comment:%s:%s", comment.ID, comment.UpdatedAt),
			Answers:   ref,
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

// classifyThreads reports every unresolved review thread that still holds human
// activity no agent reply names.
//
// A resolved thread is never reported, whatever it contains. Resolution is
// GitHub's own current truth and the one signal a human controls directly:
// somebody decided that conversation is finished, and a reviewer who did not
// mean it — or who came back to it — leaves it unresolved, which brings the
// thread back with them. Reporting a resolved thread as unresolved sent the
// owner to argue with a settled conversation, and a thread a human resolved
// without any agent reply did exactly that on every check.
//
// An unresolved thread is reported against its newest unanswered human comment,
// so a reply that answers one comment never answers a second one posted beside
// it, and a new human reply moves the item's identity and wakes the owner
// again.
func classifyThreads(threads []ReviewThread) []Item {
	var items []Item
	for _, thread := range threads {
		if thread.IsResolved {
			continue
		}
		answered := answeredRefs(thread.Comments)
		latest, found := latestUnanswered(thread.Comments, func(comment Activity) bool {
			return answeredBy(answered[threadAnswerRef(thread.ID, comment.ID)], comment.UpdatedAt) ||
				answeredBy(answered[AnswerRef(SourceInlineComment, comment.ID)], comment.UpdatedAt)
		})
		if !found {
			continue
		}
		items = append(items, Item{
			Reason:        ReasonUnresolvedThread,
			Source:        SourceReviewThread,
			ID:            thread.ID,
			Key:           fmt.Sprintf("thread:%s:%s:%s", thread.ID, latest.ID, latest.UpdatedAt),
			Answers:       threadAnswerRef(thread.ID, latest.ID),
			Body:          latest.Body,
			Author:        latest.Author.Login,
			UpdatedAt:     latest.UpdatedAt,
			URL:           latest.URL,
			Path:          firstNonEmpty(latest.Path, thread.Path),
			Line:          firstPositive(latest.Line, thread.Line),
			ThreadID:      thread.ID,
			CommentsTotal: thread.CommentsTotal,
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
// true exactly when a marker starts one of its lines.
func isAgentReply(activity Activity) bool {
	return parseReplyMarker(activity).marked
}

// replyMarker is what one activity's marker lines claim. A marked reply is
// never actionable itself; answers names the exact activities it answers, and
// is empty for a legacy bare marker.
type replyMarker struct {
	marked  bool
	answers map[string]bool
}

// parseReplyMarker reads every marker line of one activity. A marker is a whole
// HTML comment starting its own line, so a quoted or indented one — which is a
// human talking about an agent reply — is not a marker.
func parseReplyMarker(activity Activity) replyMarker {
	parsed := replyMarker{}
	if activity.Author.Bot {
		return parsed
	}
	for _, line := range strings.Split(activity.Body, "\n") {
		rest, isMarker := strings.CutPrefix(line, agentReplyMarkerPrefix)
		if !isMarker || !strings.HasPrefix(rest, " ") {
			continue
		}
		fields, _, closed := strings.Cut(rest, agentReplyMarkerEnd)
		if !closed {
			continue
		}
		parsed.marked = true
		for _, field := range strings.Fields(fields) {
			ref, named := strings.CutPrefix(field, agentReplyAnswersField)
			if !named {
				continue
			}
			if ref = normalizeAnswerRef(ref); ref == "" {
				continue
			}
			if parsed.answers == nil {
				parsed.answers = map[string]bool{}
			}
			parsed.answers[ref] = true
		}
	}
	return parsed
}

// normalizeAnswerRef canonicalizes one marker reference so a reply and the
// digest item it answers agree on the same token. Only the source is case
// folded; an id is GitHub's and is compared exactly.
func normalizeAnswerRef(ref string) string {
	source, id, found := strings.Cut(strings.TrimSpace(ref), ":")
	if !found || source == "" || id == "" {
		return ""
	}
	return strings.ToLower(source) + ":" + id
}

// answeredRefs returns, for every reference the agent replies in one stream
// name, the newest reply that names it. Only the stream it is given is
// consulted, so an answer posted somewhere else on the pull request never
// counts as an answer here.
func answeredRefs(activities []Activity) map[string]string {
	answered := map[string]string{}
	for _, activity := range activities {
		for ref := range parseReplyMarker(activity).answers {
			if newerThan(activity.UpdatedAt, answered[ref]) {
				answered[ref] = activity.UpdatedAt
			}
		}
	}
	return answered
}

// legacyAnswers supports agent replies written before markers named what they
// answer. A bare marker answers exactly the one comment GitHub itself chained
// the reply to and nothing else: an id GitHub assigned cannot silently cover a
// sibling comment the reply never saw. There is no equivalent for conversation
// comments, review bodies, or thread replies, because nothing there ties a bare
// reply to a single activity — such a reply keeps waking the owner until an
// anchored one answers it, which is the safe direction to be wrong in.
func legacyAnswers(comments []Activity) map[string]string {
	answered := map[string]string{}
	for _, comment := range comments {
		parsed := parseReplyMarker(comment)
		if !parsed.marked || len(parsed.answers) > 0 || comment.InReplyTo == "" {
			continue
		}
		if newerThan(comment.UpdatedAt, answered[comment.InReplyTo]) {
			answered[comment.InReplyTo] = comment.UpdatedAt
		}
	}
	return answered
}

// answeredBy reports whether an agent reply at replyAt answers human activity
// at activityAt. Equal timestamps count: GitHub reports seconds, and a reply
// written in the same second as the comment it answers is still an answer. A
// human edit after the reply moves activityAt forward, so the source becomes
// actionable again. Activity GitHub gave no timestamp at all is never treated
// as answered — a watcher that cannot order two events keeps the feedback
// rather than hiding it.
func answeredBy(replyAt, activityAt string) bool {
	if replyAt == "" || activityAt == "" {
		return false
	}
	return !newerThan(activityAt, replyAt)
}

// latestUnanswered returns the newest human activity answered reports nothing
// answers. Ties keep the later one in stream order, which is the order GitHub
// returns a conversation in.
func latestUnanswered(activities []Activity, answered func(Activity) bool) (Activity, bool) {
	var latest Activity
	found := false
	for _, activity := range activities {
		if !isHuman(activity) || answered(activity) {
			continue
		}
		if !found || !newerThan(latest.UpdatedAt, activity.UpdatedAt) {
			latest = activity
			found = true
		}
	}
	return latest, found
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
		if review.State == state && isHuman(review) {
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
