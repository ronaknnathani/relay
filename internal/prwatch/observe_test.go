package prwatch

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/project"
)

var observedAt = time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)

func openPR() PullRequest {
	return PullRequest{
		Number: 42, URL: "https://github.com/acme/widgets/pull/42", Title: "Add widgets",
		State: "OPEN", BaseRef: "main", BaseSHA: "base111", HeadRef: "feature", HeadSHA: "head222",
		MergeStateStatus: "BLOCKED", Mergeable: "MERGEABLE", ReviewDecision: "REVIEW_REQUIRED",
		Author: "author-human", DefaultBranch: "main", Repo: "acme/widgets",
	}
}

func human(id, login, body, updated string) Activity {
	return Activity{ID: id, Author: Actor{Login: login}, Body: body, UpdatedAt: updated, CreatedAt: updated}
}

func reasons(digest Digest) []string {
	codes := make([]string, 0, len(digest.Items))
	for _, item := range digest.Items {
		codes = append(codes, item.Reason)
	}
	sort.Strings(codes)
	return codes
}

func assertReasons(t *testing.T, digest Digest, want ...string) {
	t.Helper()
	got := reasons(digest)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("digest reasons = %v, want %v", got, want)
	}
}

func TestFailingChecksAreActionableAndPendingChecksAreNot(t *testing.T) {
	observation := Observation{
		PR: openPR(),
		Checks: []Check{
			{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE", RunID: "987654",
				DetailsURL: "https://github.com/acme/widgets/actions/runs/987654/job/12"},
			{Name: "lint", Status: "IN_PROGRESS", Conclusion: ""},
			{Name: "unit", Status: "COMPLETED", Conclusion: "SUCCESS"},
			{Name: "docs", Status: "COMPLETED", Conclusion: "SKIPPED"},
		},
	}
	digest := BuildDigest("demo", ModeStandalone, observation, observedAt)

	assertReasons(t, digest, ReasonFailingCheck)
	item := digest.Items[0]
	if item.CheckRunID != "987654" || item.CheckConclusion != "FAILURE" {
		t.Errorf("failing check item = %+v, want the run id and conclusion", item)
	}
	if item.Key != "check:build:987654:FAILURE:head222" {
		t.Errorf("failing check key = %q", item.Key)
	}
	if !containsString(digest.Waiting, WaitingChecksPending) {
		t.Errorf("waiting = %v, want %q", digest.Waiting, WaitingChecksPending)
	}
}

func TestPendingChecksAloneProduceNoFingerprint(t *testing.T) {
	observation := Observation{
		PR:     openPR(),
		Checks: []Check{{Name: "build", Status: "IN_PROGRESS"}},
	}
	digest := BuildDigest("demo", ModeStandalone, observation, observedAt)
	if digest.Fingerprint != "" || len(digest.Items) != 0 {
		t.Fatalf("digest = %+v, want no actionable items", digest)
	}
	if !containsString(digest.Waiting, WaitingChecksPending) {
		t.Errorf("waiting = %v, want checks-pending", digest.Waiting)
	}
}

func TestChangesRequestedIsActionableWithTheReviewBody(t *testing.T) {
	pr := openPR()
	pr.ReviewDecision = "CHANGES_REQUESTED"
	review := human("10", "reviewer", "please split this", "2026-01-03T00:00:00Z")
	review.State = "CHANGES_REQUESTED"
	digest := BuildDigest("demo", ModeStandalone, Observation{
		PR: pr, Reviews: []Activity{review},
	}, observedAt)

	assertReasons(t, digest, ReasonChangesRequested, ReasonNewReview)
	for _, item := range digest.Items {
		if item.Reason == ReasonChangesRequested && item.Body != "please split this" {
			t.Errorf("changes-requested item = %+v, want the review body", item)
		}
	}
}

func TestDraftAndReviewRequiredAloneAreNotActionable(t *testing.T) {
	pr := openPR()
	pr.Draft = true
	digest := BuildDigest("demo", ModeStandalone, Observation{PR: pr}, observedAt)

	if len(digest.Items) != 0 {
		t.Fatalf("digest items = %+v, want none for a draft awaiting review", digest.Items)
	}
	for _, code := range []string{WaitingDraft, WaitingReviewRequired} {
		if !containsString(digest.Waiting, code) {
			t.Errorf("waiting = %v, want %q", digest.Waiting, code)
		}
	}
}

func TestHumanCommentIsActionableUntilTheAgentReplies(t *testing.T) {
	pr := openPR()
	observation := Observation{
		PR:       pr,
		Comments: []Activity{human("1", "reviewer", "please rename this", "2026-01-01T00:00:00Z")},
	}
	digest := BuildDigest("demo", ModeStandalone, observation, observedAt)
	assertReasons(t, digest, ReasonNewComment)
	if digest.Items[0].Body != "please rename this" {
		t.Errorf("item = %+v, want the comment body", digest.Items[0])
	}

	answered := observation
	answered.Comments = append(append([]Activity{}, observation.Comments...),
		agentReply("2", pr.Author, "renamed", "2026-01-02T00:00:00Z"))
	if got := BuildDigest("demo", ModeStandalone, answered, observedAt); len(got.Items) != 0 {
		t.Fatalf("digest items = %+v, want none once the agent replied", got.Items)
	}

	replied := answered
	replied.Comments = append(append([]Activity{}, answered.Comments...),
		human("3", "reviewer", "still wrong", "2026-01-03T00:00:00Z"))
	newer := BuildDigest("demo", ModeStandalone, replied, observedAt)
	assertReasons(t, newer, ReasonNewComment)
	if newer.Items[0].ID != "3" {
		t.Errorf("item = %+v, want the newest human reply", newer.Items[0])
	}
}

func TestBotCommentsAreNotActionable(t *testing.T) {
	bot := Activity{ID: "9", Author: Actor{Login: "renovate[bot]", Bot: true}, Body: "bumped",
		UpdatedAt: "2026-01-01T00:00:00Z"}
	digest := BuildDigest("demo", ModeStandalone, Observation{
		PR: openPR(), Comments: []Activity{bot},
	}, observedAt)
	if len(digest.Items) != 0 {
		t.Fatalf("digest items = %+v, want none for a bot comment", digest.Items)
	}
}

func TestUnresolvedThreadsAndNewRepliesAreActionable(t *testing.T) {
	pr := openPR()
	unresolved := ReviewThread{
		ID: "THREAD_1", Path: "main.go", Line: 12, CommentsTotal: 1,
		Comments: []Activity{human("20", "reviewer", "rename this", "2026-01-03T00:00:00Z")},
	}
	answeredResolved := ReviewThread{
		ID: "THREAD_2", IsResolved: true, Path: "go.mod", Line: 3, CommentsTotal: 2,
		Comments: []Activity{
			human("21", "reviewer", "bump this", "2026-01-01T00:00:00Z"),
			agentReply("22", pr.Author, "bumped", "2026-01-02T00:00:00Z"),
		},
	}
	digest := BuildDigest("demo", ModeStandalone, Observation{
		PR: pr, Threads: []ReviewThread{unresolved, answeredResolved},
	}, observedAt)
	assertReasons(t, digest, ReasonUnresolvedThread)
	if digest.Items[0].ThreadID != "THREAD_1" || digest.Items[0].Path != "main.go" {
		t.Errorf("item = %+v, want the unresolved thread", digest.Items[0])
	}

	reopened := answeredResolved
	reopened.Comments = append(append([]Activity{}, answeredResolved.Comments...),
		human("23", "reviewer", "not quite", "2026-01-04T00:00:00Z"))
	withReply := BuildDigest("demo", ModeStandalone, Observation{
		PR: pr, Threads: []ReviewThread{reopened},
	}, observedAt)
	assertReasons(t, withReply, ReasonUnresolvedThread)
	if withReply.Items[0].ID != "THREAD_2" || !withReply.Items[0].ThreadResolved {
		t.Errorf("item = %+v, want the new reply on the resolved thread", withReply.Items[0])
	}
}

func TestInlineCommentOutsideAThreadIsStillReported(t *testing.T) {
	pr := openPR()
	inline := human("20", "reviewer", "rename this", "2026-01-03T00:00:00Z")
	inline.Path, inline.Line = "main.go", 12
	threaded := Observation{
		PR:             pr,
		InlineComments: []Activity{inline},
		Threads: []ReviewThread{{
			ID: "THREAD_1", Comments: []Activity{inline},
		}},
	}
	if got := reasons(BuildDigest("demo", ModeStandalone, threaded, observedAt)); len(got) != 1 ||
		got[0] != ReasonUnresolvedThread {
		t.Fatalf("reasons = %v, want the thread to own the inline comment", got)
	}

	orphan := Observation{PR: pr, InlineComments: []Activity{inline}}
	digest := BuildDigest("demo", ModeStandalone, orphan, observedAt)
	assertReasons(t, digest, ReasonNewInlineComment)
	if digest.Items[0].Path != "main.go" || digest.Items[0].Line != 12 {
		t.Errorf("item = %+v, want the file and line", digest.Items[0])
	}
}

func TestConflictAndStaleAreActionable(t *testing.T) {
	dirty := openPR()
	dirty.MergeStateStatus = "DIRTY"
	dirty.Mergeable = "CONFLICTING"
	assertReasons(t, BuildDigest("demo", ModeStandalone, Observation{PR: dirty}, observedAt),
		ReasonMergeConflict)

	behind := openPR()
	behind.MergeStateStatus = "BEHIND"
	assertReasons(t, BuildDigest("demo", ModeStandalone, Observation{PR: behind}, observedAt),
		ReasonStale)
}

func TestApprovedGreenCleanPRWithoutAutoMergeIsActionable(t *testing.T) {
	pr := openPR()
	pr.ReviewDecision = "APPROVED"
	pr.MergeStateStatus = "CLEAN"
	observation := Observation{PR: pr, Checks: []Check{{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"}}}
	assertReasons(t, BuildDigest("demo", ModeStandalone, observation, observedAt),
		ReasonAutoMergeNotArmed)

	armed := observation
	armed.PR.AutoMerge = true
	digest := BuildDigest("demo", ModeStandalone, armed, observedAt)
	if len(digest.Items) != 0 {
		t.Fatalf("digest items = %+v, want none once auto-merge is armed", digest.Items)
	}
	if !containsString(digest.Waiting, WaitingAutoMergeArmed) {
		t.Errorf("waiting = %v, want auto-merge-armed", digest.Waiting)
	}

	nonDefaultBase := observation
	nonDefaultBase.PR.BaseRef = "stack/api"
	if got := BuildDigest("demo", ModeStandalone, nonDefaultBase, observedAt); len(got.Items) != 0 {
		t.Errorf("digest items = %+v, want none for a non-default base", got.Items)
	}

	pendingChecks := observation
	pendingChecks.Checks = append(pendingChecks.Checks, Check{Name: "lint", Status: "IN_PROGRESS"})
	if got := BuildDigest("demo", ModeStandalone, pendingChecks, observedAt); len(got.Items) != 0 {
		t.Errorf("digest items = %+v, want none while checks are pending", got.Items)
	}
}

func TestMergedPullRequestCompletesUnlessItIsAStackFront(t *testing.T) {
	pr := openPR()
	pr.State = "MERGED"
	for _, mode := range []Mode{ModeStandalone, ModeManaged} {
		digest := BuildDigest("demo", mode, Observation{PR: pr}, observedAt)
		if !digest.Complete || len(digest.Items) != 0 || digest.Fingerprint != "" {
			t.Errorf("%s digest = %+v, want a silent complete record", mode, digest)
		}
		if !containsString(digest.Waiting, WaitingMerged) {
			t.Errorf("%s waiting = %v, want merged", mode, digest.Waiting)
		}
	}

	stack := BuildDigest("demo", ModeStack, Observation{PR: pr}, observedAt)
	assertReasons(t, stack, ReasonStackFrontMerged)
	if stack.Complete {
		t.Error("stack front digest reported complete; the orchestrator still has to retarget")
	}
}

func TestClosedUnmergedIsActionable(t *testing.T) {
	pr := openPR()
	pr.State = "CLOSED"
	digest := BuildDigest("demo", ModeStandalone, Observation{PR: pr}, observedAt)
	assertReasons(t, digest, ReasonClosedUnmerged)
	if digest.Complete {
		t.Error("closed-unmerged digest reported complete")
	}
}

func TestFingerprintIsStableAcrossReobservation(t *testing.T) {
	observation := Observation{
		PR:       openPR(),
		Comments: []Activity{human("1", "reviewer", "please rename this", "2026-01-01T00:00:00Z")},
		Checks:   []Check{{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE", RunID: "1"}},
	}
	first := BuildDigest("demo", ModeStandalone, observation, observedAt)
	later := BuildDigest("demo", ModeStandalone, observation, observedAt.Add(time.Hour))
	if first.Fingerprint != later.Fingerprint {
		t.Errorf("fingerprint changed across re-observation: %q vs %q", first.Fingerprint, later.Fingerprint)
	}
	if first.ObservedAt == later.ObservedAt {
		t.Error("observed_at did not advance between observations")
	}
}

func TestNewHeadRefreshesCheckAndConflictKeys(t *testing.T) {
	pr := openPR()
	pr.MergeStateStatus = "DIRTY"
	observation := Observation{
		PR:     pr,
		Checks: []Check{{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE", RunID: "1"}},
	}
	first := BuildDigest("demo", ModeStandalone, observation, observedAt)

	pushed := observation
	pushed.PR.HeadSHA = "head333"
	after := BuildDigest("demo", ModeStandalone, pushed, observedAt)
	assertReasons(t, after, ReasonFailingCheck, ReasonMergeConflict)
	if after.Fingerprint == first.Fingerprint {
		t.Error("a new head kept the previous fingerprint; check and conflict keys must carry the head")
	}
}

func hasItemID(digest Digest, id string) bool {
	for _, item := range digest.Items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestLoadTargetPrefersWorkflowStateOverTheManifest(t *testing.T) {
	home := withRuntimeHome(t)
	worktree := filepath.Join(home, "code", "widgets", ".worktrees", "demo")
	manifestNumber := 7
	dir := filepath.Join(project.ActiveDir(), "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	manifest := project.Manifest{
		Slug: "demo", Repo: filepath.Join(home, "code", "widgets"), Worktree: &worktree,
		PR: project.PRInfo{Number: &manifestNumber},
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), "demo"), manifest); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	target, err := LoadTarget("demo")
	if err != nil {
		t.Fatalf("LoadTarget: %v", err)
	}
	if target.PRNumber != manifestNumber || target.Dir != worktree {
		t.Errorf("target = %+v, want the manifest pull request in the worktree", target)
	}

	state, err := project.NewState("demo", "deliver-pr", []string{"implement", "open-pr"})
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	state.SetPR(42, "https://github.com/acme/widgets/pull/42")
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	target, err = LoadTarget("demo")
	if err != nil {
		t.Fatalf("LoadTarget: %v", err)
	}
	if target.PRNumber != 42 || target.PRURL != "https://github.com/acme/widgets/pull/42" {
		t.Errorf("target = %+v, want the workflow state pull request", target)
	}
}

func TestLoadTargetWithoutAPullRequestNamesTheRecordingCommand(t *testing.T) {
	home := withRuntimeHome(t)
	dir := filepath.Join(project.ActiveDir(), "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), "demo"), project.Manifest{
		Slug: "demo", Repo: filepath.Join(home, "code", "widgets"),
	}); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	_, err := LoadTarget("demo")
	if err == nil || !strings.Contains(err.Error(), "relay state pr demo") {
		t.Fatalf("LoadTarget = %v, want an error naming the recording command", err)
	}
}

// agentReply builds the exact shape every automated Relay pull request reply
// carries: the hidden machine marker, then the visible disclosure.
func agentReply(id, login, body, updated string) Activity {
	return human(id, login,
		AgentReplyMarker+"\n🤖 copilot on behalf of author-human\n\n"+body, updated)
}

func TestOnlyTheExactMarkerClassifiesAnAgentReply(t *testing.T) {
	pr := openPR()
	question := human("1", "reviewer", "please rename this", "2026-01-01T00:00:00Z")

	// A human who types the robot emoji, or quotes the marker inside a
	// blockquote, has not answered anything.
	for name, reply := range map[string]Activity{
		"emoji only": human("2", "reviewer",
			"🤖 nice bot, but this is still wrong", "2026-01-02T00:00:00Z"),
		"quoted marker": human("2", "reviewer",
			"> "+AgentReplyMarker+"\n> earlier reply\n\nstill wrong", "2026-01-02T00:00:00Z"),
		"emoji disclosure without the marker": human("2", pr.Author,
			"🤖 copilot on behalf of author-human: renamed", "2026-01-02T00:00:00Z"),
	} {
		t.Run(name, func(t *testing.T) {
			digest := BuildDigest("demo", ModeStandalone, Observation{
				PR: pr, Comments: []Activity{question, reply},
			}, observedAt)
			if !hasItemID(digest, "1") {
				t.Fatalf("digest items = %+v, want the original review comment still actionable",
					digest.Items)
			}
		})
	}

	answered := BuildDigest("demo", ModeStandalone, Observation{
		PR:       pr,
		Comments: []Activity{question, agentReply("2", pr.Author, "renamed", "2026-01-02T00:00:00Z")},
	}, observedAt)
	if len(answered.Items) != 0 {
		t.Fatalf("digest items = %+v, want none once the marked agent reply landed", answered.Items)
	}
}

func TestANewHumanReplyAfterTheMarkerReactivatesTheSource(t *testing.T) {
	pr := openPR()
	digest := BuildDigest("demo", ModeStandalone, Observation{
		PR: pr,
		Comments: []Activity{
			human("1", "reviewer", "please rename this", "2026-01-01T00:00:00Z"),
			agentReply("2", pr.Author, "renamed", "2026-01-02T00:00:00Z"),
			human("3", "reviewer", "still wrong", "2026-01-03T00:00:00Z"),
		},
	}, observedAt)
	assertReasons(t, digest, ReasonNewComment)
	if digest.Items[0].ID != "3" {
		t.Errorf("item = %+v, want the newest human reply", digest.Items[0])
	}
}

// An agent reply posted at the same second as the human activity it answers
// still counts: GitHub timestamps are second-resolution.
func TestAnAgentReplyAtTheSameInstantAnswersTheSource(t *testing.T) {
	pr := openPR()
	digest := BuildDigest("demo", ModeStandalone, Observation{
		PR: pr,
		Comments: []Activity{
			human("1", "reviewer", "please rename this", "2026-01-01T00:00:00Z"),
			agentReply("2", pr.Author, "renamed", "2026-01-01T00:00:00Z"),
		},
	}, observedAt)
	if len(digest.Items) != 0 {
		t.Fatalf("digest items = %+v, want none for a same-second agent reply", digest.Items)
	}
}

// The exact regression a single global "answered at" timestamp caused: one
// agent reply on one inline thread marked the whole pull request answered, and
// a review body and a second inline thread left at the same moment vanished
// from every later digest. Every source is reconciled on its own.
func TestAnAgentReplyOnOneSourceDoesNotAnswerAnother(t *testing.T) {
	pr := openPR()
	review := human("100", "reviewer", "this needs a design note", "2026-01-01T10:00:00Z")
	review.State = "COMMENTED"
	conversation := human("200", "reviewer", "and please update the README", "2026-01-01T10:00:00Z")
	firstInline := human("300", "reviewer", "rename this", "2026-01-01T10:00:00Z")
	firstInline.Path, firstInline.Line = "main.go", 12
	secondInline := human("301", "reviewer", "and this one too", "2026-01-01T10:00:00Z")
	secondInline.Path, secondInline.Line = "store.go", 40

	// Twenty minutes later the agent replies to exactly one inline comment.
	answer := agentReply("302", pr.Author, "renamed", "2026-01-01T10:20:00Z")
	answer.Path, answer.Line = "main.go", 12
	answer.InReplyTo = "300"

	digest := BuildDigest("demo", ModeStandalone, Observation{
		PR:             pr,
		Reviews:        []Activity{review},
		Comments:       []Activity{conversation},
		InlineComments: []Activity{firstInline, secondInline, answer},
	}, observedAt)

	assertReasons(t, digest, ReasonNewReview, ReasonNewComment, ReasonNewInlineComment)
	for _, item := range digest.Items {
		if item.Source == SourceInlineComment && item.ID != "301" {
			t.Errorf("inline item = %+v, want only the unanswered inline comment", item)
		}
	}
}

func TestAnAgentReplyAnswersOnlyItsOwnInlineThread(t *testing.T) {
	pr := openPR()
	first := human("300", "reviewer", "rename this", "2026-01-01T10:00:00Z")
	first.Path, first.Line = "main.go", 12
	answer := agentReply("302", pr.Author, "renamed", "2026-01-01T10:20:00Z")
	answer.Path, answer.Line = "main.go", 12
	answer.InReplyTo = "300"

	digest := BuildDigest("demo", ModeStandalone, Observation{
		PR: pr, InlineComments: []Activity{first, answer},
	}, observedAt)
	if len(digest.Items) != 0 {
		t.Fatalf("digest items = %+v, want none once the inline thread was answered", digest.Items)
	}

	// A reply chained onto the same root answers the root too.
	chained := agentReply("303", pr.Author, "and again", "2026-01-01T10:30:00Z")
	chained.InReplyTo = "302"
	followUp := human("304", "reviewer", "still wrong", "2026-01-01T10:40:00Z")
	followUp.InReplyTo = "300"
	followUp.Path, followUp.Line = "main.go", 12
	reopened := BuildDigest("demo", ModeStandalone, Observation{
		PR: pr, InlineComments: []Activity{first, answer, chained, followUp},
	}, observedAt)
	assertReasons(t, reopened, ReasonNewInlineComment)
	if reopened.Items[0].ID != "304" {
		t.Errorf("item = %+v, want the newest human reply on the thread", reopened.Items[0])
	}
}

func TestAnAgentReviewAnswersOnlyReviewBodies(t *testing.T) {
	pr := openPR()
	review := human("100", "reviewer", "this needs a design note", "2026-01-01T10:00:00Z")
	review.State = "COMMENTED"
	conversation := human("200", "reviewer", "and please update the README", "2026-01-01T10:00:00Z")
	answeringReview := agentReply("101", pr.Author, "added the design note", "2026-01-01T10:20:00Z")
	answeringReview.State = "COMMENTED"

	digest := BuildDigest("demo", ModeStandalone, Observation{
		PR: pr, Reviews: []Activity{review, answeringReview}, Comments: []Activity{conversation},
	}, observedAt)
	assertReasons(t, digest, ReasonNewComment)
	if digest.Items[0].ID != "200" {
		t.Errorf("item = %+v, want the still-unanswered conversation comment", digest.Items[0])
	}
}

func TestCancelledAndStaleChecksAreActionableAndNeutralIsNot(t *testing.T) {
	for _, test := range []struct {
		conclusion string
		actionable bool
	}{
		{conclusion: "FAILURE", actionable: true},
		{conclusion: "ERROR", actionable: true},
		{conclusion: "TIMED_OUT", actionable: true},
		{conclusion: "ACTION_REQUIRED", actionable: true},
		{conclusion: "STARTUP_FAILURE", actionable: true},
		// A canceled or stale required check never reports a result, so the
		// pull request cannot merge until somebody reruns it.
		{conclusion: "CANCELLED", actionable: true}, //nolint:misspell // GitHub check conclusion value.
		{conclusion: "STALE", actionable: true},
		// GitHub treats neutral and skipped as satisfying a required check, so
		// neither blocks a merge and neither is the watcher's business.
		{conclusion: "NEUTRAL"},
		{conclusion: "SKIPPED"},
		{conclusion: "SUCCESS"},
	} {
		t.Run(test.conclusion, func(t *testing.T) {
			digest := BuildDigest("demo", ModeStandalone, Observation{
				PR:     openPR(),
				Checks: []Check{{Name: "build", Status: "COMPLETED", Conclusion: test.conclusion}},
			}, observedAt)
			if test.actionable {
				assertReasons(t, digest, ReasonFailingCheck)
				return
			}
			if len(digest.Items) != 0 {
				t.Fatalf("digest items = %+v, want none for a %s check", digest.Items, test.conclusion)
			}
		})
	}
}

func TestBlockedWithNothingExplainingItIsActionable(t *testing.T) {
	blocked := openPR()
	blocked.ReviewDecision = "APPROVED"
	green := []Check{{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"}}

	// Approved, green, no conflict, not a draft — and GitHub still will not
	// merge it. Nothing else in the digest says why, so the block itself is the
	// thing the owner has to look at.
	digest := BuildDigest("demo", ModeStandalone, Observation{PR: blocked, Checks: green}, observedAt)
	assertReasons(t, digest, ReasonBlocked)
	item := digest.Items[0]
	if item.Key != "blocked:head222" || item.Source != SourceMerge {
		t.Errorf("blocked item = %+v, want a head-scoped merge item", item)
	}

	for name, observation := range map[string]Observation{
		"a failing check explains it": {
			PR:     blocked,
			Checks: []Check{{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE"}},
		},
		"a pending check explains it": {
			PR:     blocked,
			Checks: []Check{{Name: "build", Status: "IN_PROGRESS"}},
		},
		"review required explains it":     {PR: openPR(), Checks: green},
		"changes requested explains it":   {PR: withReviewDecision(blocked, "CHANGES_REQUESTED"), Checks: green},
		"a draft explains it":             {PR: withDraft(blocked), Checks: green},
		"a merge conflict explains it":    {PR: withMergeState(blocked, "DIRTY"), Checks: green},
		"a clean merge state is no block": {PR: withMergeState(blocked, "CLEAN"), Checks: green},
	} {
		t.Run(name, func(t *testing.T) {
			digest := BuildDigest("demo", ModeStandalone, observation, observedAt)
			for _, item := range digest.Items {
				if item.Reason == ReasonBlocked {
					t.Fatalf("digest items = %+v, want no unexplained block", digest.Items)
				}
			}
		})
	}

	explained := BuildDigest("demo", ModeStandalone, Observation{PR: openPR(), Checks: green}, observedAt)
	if !containsString(explained.Waiting, WaitingBlocked) {
		t.Errorf("waiting = %v, want an explained block reported as waiting", explained.Waiting)
	}
}

func withReviewDecision(pr PullRequest, decision string) PullRequest {
	pr.ReviewDecision = decision
	return pr
}

func withDraft(pr PullRequest) PullRequest {
	pr.Draft = true
	return pr
}

func withMergeState(pr PullRequest, status string) PullRequest {
	pr.MergeStateStatus = status
	return pr
}
