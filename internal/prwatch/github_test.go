package prwatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeGH answers gh invocations from fixtures keyed by a stable prefix of the
// command line, so tests never touch a real repository or the network.
type fakeGH struct {
	responses map[string]string
	failures  map[string]error
	calls     []string
	attempts  map[string]int
}

func newFakeGH() *fakeGH {
	return &fakeGH{
		responses: map[string]string{},
		failures:  map[string]error{},
		attempts:  map[string]int{},
	}
}

func (f *fakeGH) runner() Runner {
	return RunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		key := fakeKey(args)
		f.calls = append(f.calls, key)
		f.attempts[key]++
		if err, failing := f.failures[key]; failing {
			return nil, err
		}
		response, known := f.responses[key]
		if !known {
			return nil, errors.New("unexpected gh command: " + strings.Join(args, " "))
		}
		return []byte(response), nil
	})
}

// fakeKey collapses one gh invocation to the fixture key: the subcommand plus
// the API path or graphql marker.
func fakeKey(args []string) string {
	switch {
	case len(args) >= 2 && args[0] == "repo" && args[1] == "view":
		return "repo view"
	case len(args) >= 2 && args[0] == "pr" && args[1] == "view":
		return "pr view"
	case len(args) >= 2 && args[0] == "api" && args[1] == "graphql":
		for _, arg := range args {
			if strings.Contains(arg, "statusCheckRollup") {
				return "graphql checks"
			}
		}
		return "graphql reviewThreads"
	case len(args) >= 2 && args[0] == "api":
		return "api " + args[1]
	}
	return strings.Join(args, " ")
}

func fixtureClient(t *testing.T, gh *fakeGH) *Client {
	t.Helper()
	client := NewClient(gh.runner(), t.TempDir())
	client.backoff = 0
	client.sleep = func(time.Duration) {}
	return client
}

const repoViewFixture = `{"nameWithOwner":"acme/widgets","defaultBranchRef":{"name":"main"}}`

const prViewFixture = `{
  "number": 42,
  "url": "https://github.com/acme/widgets/pull/42",
  "title": "Add widgets",
  "state": "OPEN",
  "isDraft": false,
  "baseRefName": "main",
  "baseRefOid": "base111",
  "headRefName": "feature",
  "headRefOid": "head222",
  "mergeStateStatus": "BLOCKED",
  "mergeable": "MERGEABLE",
  "reviewDecision": "CHANGES_REQUESTED",
  "autoMergeRequest": null,
  "author": {"login": "author-human", "is_bot": false}
}`

// Two concatenated GraphQL pages of check contexts, exactly as
// `gh api graphql --paginate` emits them.
const checksFixture = `{"data":{"repository":{"pullRequest":{"statusCheckRollup":{"nodes":[{"commit":{
  "statusCheckRollup":{"contexts":{
    "pageInfo":{"hasNextPage":true,"endCursor":"c1"},
    "nodes":[
      {"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"FAILURE",
       "detailsUrl":"https://github.com/acme/widgets/actions/runs/987654/job/12"},
      {"__typename":"CheckRun","name":"lint","status":"IN_PROGRESS","conclusion":"",
       "detailsUrl":"https://github.com/acme/widgets/actions/runs/987655/job/13"}]}}}}]}}}}}
{"data":{"repository":{"pullRequest":{"statusCheckRollup":{"nodes":[{"commit":{
  "statusCheckRollup":{"contexts":{
    "pageInfo":{"hasNextPage":false,"endCursor":"c2"},
    "nodes":[
      {"__typename":"StatusContext","context":"legacy/ci","state":"SUCCESS",
       "targetUrl":"https://ci.example.com/build/1"}]}}}}]}}}}}`

// Two concatenated pages, exactly as `gh api --paginate` emits them.
const issueCommentsFixture = `[
  {"id": 1, "user": {"login": "reviewer", "type": "User"}, "body": "page one comment",
   "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
   "html_url": "https://github.com/acme/widgets/pull/42#issuecomment-1"}
]
[
  {"id": 2, "user": {"login": "renovate[bot]", "type": "Bot"}, "body": "page two bot comment",
   "created_at": "2026-01-02T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z",
   "html_url": "https://github.com/acme/widgets/pull/42#issuecomment-2"}
]`

const reviewsFixture = `[
  {"id": 10, "user": {"login": "reviewer", "type": "User"}, "state": "changes_requested",
   "body": "please split this", "submitted_at": "2026-01-03T00:00:00Z",
   "html_url": "https://github.com/acme/widgets/pull/42#pullrequestreview-10"}
]`

const inlineCommentsFixture = `[
  {"id": 20, "user": {"login": "reviewer", "type": "User"}, "body": "rename this",
   "path": "main.go", "line": 12, "created_at": "2026-01-03T00:00:00Z",
   "updated_at": "2026-01-04T00:00:00Z",
   "html_url": "https://github.com/acme/widgets/pull/42#discussion_r20"}
]`

const reviewThreadsFixture = `{"data":{"repository":{"pullRequest":{"reviewThreads":{
  "pageInfo":{"hasNextPage":true,"endCursor":"c1"},
  "nodes":[{"id":"THREAD_1","isResolved":false,"isOutdated":false,"path":"main.go","line":12,
    "comments":{"totalCount":2,"nodes":[
      {"databaseId":20,"body":"rename this","createdAt":"2026-01-03T00:00:00Z",
       "updatedAt":"2026-01-04T00:00:00Z","url":"https://github.com/acme/widgets/pull/42#discussion_r20",
       "path":"main.go","line":12,"author":{"login":"reviewer","__typename":"User"}},
      {"databaseId":21,"body":"and this too","createdAt":"2026-01-05T00:00:00Z",
       "updatedAt":"2026-01-05T00:00:00Z","url":"https://github.com/acme/widgets/pull/42#discussion_r21",
       "path":"main.go","line":12,"author":{"login":"reviewer","__typename":"User"}}]}}]}}}}}
{"data":{"repository":{"pullRequest":{"reviewThreads":{
  "pageInfo":{"hasNextPage":false,"endCursor":"c2"},
  "nodes":[{"id":"THREAD_2","isResolved":true,"isOutdated":false,"path":"go.mod","line":3,
    "comments":{"totalCount":1,"nodes":[
      {"databaseId":22,"body":"resolved already","createdAt":"2026-01-06T00:00:00Z",
       "updatedAt":"2026-01-06T00:00:00Z","url":"https://github.com/acme/widgets/pull/42#discussion_r22",
       "path":"go.mod","line":3,"author":{"login":"reviewer","__typename":"User"}}]}}]}}}}}`

func fullFixtureGH() *fakeGH {
	gh := newFakeGH()
	gh.responses["repo view"] = repoViewFixture
	gh.responses["pr view"] = prViewFixture
	gh.responses["api repos/acme/widgets/issues/42/comments"] = issueCommentsFixture
	gh.responses["api repos/acme/widgets/pulls/42/reviews"] = reviewsFixture
	gh.responses["api repos/acme/widgets/pulls/42/comments"] = inlineCommentsFixture
	gh.responses["graphql reviewThreads"] = reviewThreadsFixture
	gh.responses["graphql checks"] = checksFixture
	return gh
}

func TestObserveReadsEveryPullRequestSurface(t *testing.T) {
	gh := fullFixtureGH()
	observation, err := fixtureClient(t, gh).Observe(context.Background(), 42)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	wantPR := PullRequest{
		Number: 42, URL: "https://github.com/acme/widgets/pull/42", Title: "Add widgets",
		State: "OPEN", BaseRef: "main", BaseSHA: "base111", HeadRef: "feature", HeadSHA: "head222",
		MergeStateStatus: "BLOCKED", Mergeable: "MERGEABLE", ReviewDecision: "CHANGES_REQUESTED",
		Author: "author-human", DefaultBranch: "main", Repo: "acme/widgets",
	}
	if observation.PR != wantPR {
		t.Errorf("PR = %+v, want %+v", observation.PR, wantPR)
	}

	wantChecks := []Check{
		{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE",
			DetailsURL: "https://github.com/acme/widgets/actions/runs/987654/job/12", RunID: "987654"},
		{Name: "lint", Status: "IN_PROGRESS", Conclusion: "",
			DetailsURL: "https://github.com/acme/widgets/actions/runs/987655/job/13", RunID: "987655"},
		{Name: "legacy/ci", Status: "SUCCESS", Conclusion: "SUCCESS",
			DetailsURL: "https://ci.example.com/build/1"},
	}
	if len(observation.Checks) != len(wantChecks) {
		t.Fatalf("checks = %+v, want %+v", observation.Checks, wantChecks)
	}
	for i, want := range wantChecks {
		if observation.Checks[i] != want {
			t.Errorf("check %d = %+v, want %+v", i, observation.Checks[i], want)
		}
	}

	if len(observation.Comments) != 2 {
		t.Fatalf("comments = %+v, want both paginated pages", observation.Comments)
	}
	if observation.Comments[0].Author.Bot {
		t.Error("human conversation comment was classified as a bot")
	}
	if !observation.Comments[1].Author.Bot {
		t.Error("renovate[bot] conversation comment was not classified as a bot")
	}
	if len(observation.Reviews) != 1 || observation.Reviews[0].State != "CHANGES_REQUESTED" {
		t.Errorf("reviews = %+v, want one CHANGES_REQUESTED review", observation.Reviews)
	}
	if observation.Reviews[0].UpdatedAt != "2026-01-03T00:00:00Z" {
		t.Errorf("review updated_at = %q, want the submitted_at fallback", observation.Reviews[0].UpdatedAt)
	}
	if len(observation.InlineComments) != 1 || observation.InlineComments[0].Path != "main.go" {
		t.Errorf("inline comments = %+v, want the main.go comment", observation.InlineComments)
	}

	if len(observation.Threads) != 2 {
		t.Fatalf("threads = %+v, want both graphql pages", observation.Threads)
	}
	first := observation.Threads[0]
	if first.ID != "THREAD_1" || first.IsResolved || first.CommentsTotal != 2 {
		t.Errorf("thread = %+v, want the unresolved two-comment thread", first)
	}
	if len(first.Comments) != 2 || first.Comments[1].ID != "21" {
		t.Errorf("thread comments = %+v, want the latest reply", first.Comments)
	}
	if !observation.Threads[1].IsResolved {
		t.Error("resolved thread lost its resolution state")
	}
}

func TestObserveRetriesThenFails(t *testing.T) {
	gh := fullFixtureGH()
	delete(gh.responses, "api repos/acme/widgets/pulls/42/reviews")
	gh.failures["api repos/acme/widgets/pulls/42/reviews"] = errors.New("HTTP 502")

	_, err := fixtureClient(t, gh).Observe(context.Background(), 42)
	if err == nil {
		t.Fatal("Observe = nil error, want the GitHub failure surfaced")
	}
	if !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("error %q does not carry the gh failure", err)
	}
	if got := gh.attempts["api repos/acme/widgets/pulls/42/reviews"]; got != defaultAttempts {
		t.Errorf("attempts = %d, want %d", got, defaultAttempts)
	}
}

func TestObserveRecoversFromATransientFailure(t *testing.T) {
	gh := fullFixtureGH()
	failing := "api repos/acme/widgets/issues/42/comments"
	runner := RunnerFunc(func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		if fakeKey(args) == failing && gh.attempts[failing] == 0 {
			gh.attempts[failing]++
			return nil, errors.New("HTTP 503")
		}
		return gh.runner().Run(ctx, dir, args...)
	})
	client := NewClient(runner, t.TempDir())
	client.backoff = 0
	client.sleep = func(time.Duration) {}

	observation, err := client.Observe(context.Background(), 42)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(observation.Comments) != 2 {
		t.Errorf("comments = %+v, want both pages after the retry", observation.Comments)
	}
}

func TestObserveStopsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fixtureClient(t, fullFixtureGH()).Observe(ctx, 42)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Observe = %v, want context.Canceled", err)
	}
}

func TestIsBotIdentity(t *testing.T) {
	for _, test := range []struct {
		login       string
		accountType string
		want        bool
	}{
		{login: "reviewer", accountType: "User"},
		{login: "renovate[bot]", accountType: "Bot", want: true},
		{login: "renovate[bot]", accountType: "User", want: true},
		{login: "copilot", accountType: "Bot", want: true},
		{login: "botanist", accountType: "User"},
	} {
		if got := isBotIdentity(test.login, test.accountType); got != test.want {
			t.Errorf("isBotIdentity(%q, %q) = %t, want %t", test.login, test.accountType, got, test.want)
		}
	}
}

// checkPage renders one GraphQL page of check contexts.
func checkPage(names []string, hasNextPage bool, cursor string) string {
	nodes := make([]string, 0, len(names))
	for _, name := range names {
		nodes = append(nodes, fmt.Sprintf(
			`{"__typename":"CheckRun","name":%q,"status":"COMPLETED","conclusion":"SUCCESS",`+
				`"detailsUrl":"https://github.com/acme/widgets/actions/runs/1/job/1"}`, name))
	}
	return fmt.Sprintf(
		`{"data":{"repository":{"pullRequest":{"statusCheckRollup":{"nodes":[{"commit":{`+
			`"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":%t,"endCursor":%q},`+
			`"nodes":[%s]}}}}]}}}}}`,
		hasNextPage, cursor, strings.Join(nodes, ","))
}

// A pull request with more required checks than one GraphQL page holds must be
// observed completely: a truncated list silently hides failing checks, and the
// watcher would report a red pull request as quiet.
func TestObserveReadsEveryPageOfChecks(t *testing.T) {
	first := make([]string, 100)
	for i := range first {
		first[i] = fmt.Sprintf("check-%03d", i)
	}
	second := []string{"check-100", "check-101", "check-102"}

	gh := fullFixtureGH()
	gh.responses["graphql checks"] = checkPage(first, true, "cursor-1") + "\n" +
		checkPage(second, false, "cursor-2")

	observation, err := fixtureClient(t, gh).Observe(context.Background(), 42)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(observation.Checks) != len(first)+len(second) {
		t.Fatalf("checks = %d, want %d across both pages",
			len(observation.Checks), len(first)+len(second))
	}
	if observation.Checks[0].Name != "check-000" ||
		observation.Checks[len(observation.Checks)-1].Name != "check-102" {
		t.Errorf("checks = %+v ... %+v, want both pages in order",
			observation.Checks[0], observation.Checks[len(observation.Checks)-1])
	}
	if !strings.Contains(strings.Join(gh.calls, " "), "graphql checks") {
		t.Errorf("gh calls = %v, want the paginated check query", gh.calls)
	}
}

// A last page that still reports another page means pagination stopped early.
// That is an observation error, never a shorter list quietly accepted.
func TestObserveRejectsATruncatedCheckList(t *testing.T) {
	gh := fullFixtureGH()
	gh.responses["graphql checks"] = checkPage([]string{"build"}, true, "cursor-1")

	_, err := fixtureClient(t, gh).Observe(context.Background(), 42)
	if err == nil {
		t.Fatal("Observe = nil error, want a truncated check list reported")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("error %q does not name the truncation", err)
	}
}

func TestObserveAcceptsAPullRequestWithNoChecks(t *testing.T) {
	gh := fullFixtureGH()
	gh.responses["graphql checks"] =
		`{"data":{"repository":{"pullRequest":{"statusCheckRollup":{"nodes":[{"commit":{}}]}}}}}`

	observation, err := fixtureClient(t, gh).Observe(context.Background(), 42)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(observation.Checks) != 0 {
		t.Errorf("checks = %+v, want none", observation.Checks)
	}
}

// threadPage renders one GraphQL page of review threads.
func threadPage(ids []string, hasNextPage bool, cursor string) string {
	nodes := make([]string, 0, len(ids))
	for _, id := range ids {
		nodes = append(nodes, fmt.Sprintf(
			`{"id":%q,"isResolved":false,"isOutdated":false,"path":"main.go","line":12,`+
				`"comments":{"totalCount":1,"nodes":[{"databaseId":20,"body":"rename this",`+
				`"createdAt":"2026-01-03T00:00:00Z","updatedAt":"2026-01-03T00:00:00Z",`+
				`"url":"https://github.com/acme/widgets/pull/42#discussion_r20","path":"main.go",`+
				`"line":12,"author":{"login":"reviewer","__typename":"User"}}]}}`, id))
	}
	return fmt.Sprintf(
		`{"data":{"repository":{"pullRequest":{"reviewThreads":{`+
			`"pageInfo":{"hasNextPage":%t,"endCursor":%q},"nodes":[%s]}}}}}`,
		hasNextPage, cursor, strings.Join(nodes, ","))
}

// A pull request with more review threads than one GraphQL page holds must be
// observed completely. A dropped thread is an unanswered reviewer the watcher
// reports as a quiet pull request.
func TestObserveReadsEveryPageOfReviewThreads(t *testing.T) {
	first := make([]string, 50)
	for i := range first {
		first[i] = fmt.Sprintf("THREAD_%03d", i)
	}
	second := []string{"THREAD_050", "THREAD_051"}

	gh := fullFixtureGH()
	gh.responses["graphql reviewThreads"] = threadPage(first, true, "cursor-1") + "\n" +
		threadPage(second, false, "cursor-2")

	observation, err := fixtureClient(t, gh).Observe(context.Background(), 42)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(observation.Threads) != len(first)+len(second) {
		t.Fatalf("threads = %d, want %d across both pages",
			len(observation.Threads), len(first)+len(second))
	}
	for i, want := range append(append([]string{}, first...), second...) {
		if observation.Threads[i].ID != want {
			t.Fatalf("thread %d = %q, want %q: every returned thread is preserved in order",
				i, observation.Threads[i].ID, want)
		}
	}
	if got := observation.Threads[0]; len(got.Comments) != 1 || got.Comments[0].ID != "20" {
		t.Errorf("thread = %+v, want its comments preserved", got)
	}
}

// A last page that still reports another page means pagination stopped early.
func TestObserveRejectsATruncatedReviewThreadList(t *testing.T) {
	gh := fullFixtureGH()
	gh.responses["graphql reviewThreads"] = threadPage([]string{"THREAD_1"}, false, "cursor-1") +
		"\n" + threadPage([]string{"THREAD_2"}, true, "cursor-2")

	_, err := fixtureClient(t, gh).Observe(context.Background(), 42)
	if err == nil {
		t.Fatal("Observe = nil error, want a truncated review thread list reported")
	}
	if !strings.Contains(err.Error(), "truncated review thread list") {
		t.Errorf("error %q does not name the truncation", err)
	}
}

func TestObserveAcceptsAnEmptyReviewThreadPage(t *testing.T) {
	gh := fullFixtureGH()
	gh.responses["graphql reviewThreads"] = threadPage(nil, false, "")

	observation, err := fixtureClient(t, gh).Observe(context.Background(), 42)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(observation.Threads) != 0 {
		t.Errorf("threads = %+v, want none", observation.Threads)
	}
}

// GitHub answers a partially failed GraphQL query with an `errors` list and a
// zero exit code, so a thread or a check it declined to return would otherwise
// look like one that does not exist.
func TestObserveRejectsAPartialGraphQLAnswer(t *testing.T) {
	for name, test := range map[string]struct {
		key      string
		response string
	}{
		"review threads": {
			key: "graphql reviewThreads",
			response: `{"data":{"repository":{"pullRequest":{"reviewThreads":` +
				`{"pageInfo":{"hasNextPage":false,"endCursor":"c1"},"nodes":[]}}}},` +
				`"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`,
		},
		"checks": {
			key: "graphql checks",
			response: `{"data":{"repository":{"pullRequest":{"statusCheckRollup":{"nodes":[]}}}},` +
				`"errors":[{"type":"FORBIDDEN","message":"Resource not accessible"}]}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			gh := fullFixtureGH()
			gh.responses[test.key] = test.response

			_, err := fixtureClient(t, gh).Observe(context.Background(), 42)
			if err == nil {
				t.Fatal("Observe = nil error, want the partial GraphQL answer reported")
			}
			if !strings.Contains(err.Error(), "error(s)") {
				t.Errorf("error %q does not name the GraphQL errors", err)
			}
		})
	}
}

// REST collections carry no cursor and no total, so their completeness rests
// entirely on `gh api --paginate`: it follows the Link header until GitHub
// stops sending one, and it exits nonzero the moment a page fails. These pin
// that contract — every page it emits is kept, a failing page fails the whole
// observation, and a stream that does not decode cleanly is an error rather
// than a short list.
func TestRestPaginationKeepsEveryPageItIsGiven(t *testing.T) {
	gh := fullFixtureGH()
	gh.responses["api repos/acme/widgets/issues/42/comments"] = `[
  {"id": 1, "user": {"login": "reviewer", "type": "User"}, "body": "one",
   "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}
]
[
  {"id": 2, "user": {"login": "reviewer", "type": "User"}, "body": "two",
   "created_at": "2026-01-02T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z"}
]
[]
[
  {"id": 3, "user": {"login": "reviewer", "type": "User"}, "body": "three",
   "created_at": "2026-01-03T00:00:00Z", "updated_at": "2026-01-03T00:00:00Z"}
]`

	observation, err := fixtureClient(t, gh).Observe(context.Background(), 42)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	want := []string{"1", "2", "3"}
	if len(observation.Comments) != len(want) {
		t.Fatalf("comments = %+v, want every page including the empty one", observation.Comments)
	}
	for i, id := range want {
		if observation.Comments[i].ID != id {
			t.Errorf("comment %d = %q, want %q in page order", i, observation.Comments[i].ID, id)
		}
	}
}

func TestRestPaginationFailureFailsTheObservation(t *testing.T) {
	gh := fullFixtureGH()
	failing := "api repos/acme/widgets/pulls/42/comments"
	delete(gh.responses, failing)
	gh.failures[failing] = errors.New("HTTP 422 on page 3")

	if _, err := fixtureClient(t, gh).Observe(context.Background(), 42); err == nil ||
		!strings.Contains(err.Error(), "HTTP 422 on page 3") {
		t.Fatalf("Observe = %v, want the failed page surfaced", err)
	}
}

func TestRestPaginationRejectsATruncatedStream(t *testing.T) {
	gh := fullFixtureGH()
	gh.responses["api repos/acme/widgets/issues/42/comments"] = `[
  {"id": 1, "user": {"login": "reviewer", "type": "User"}, "body": "one",
   "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}
]
[
  {"id": 2, "user": {"login": "reviewer", "type`

	if _, err := fixtureClient(t, gh).Observe(context.Background(), 42); err == nil ||
		!strings.Contains(err.Error(), "parse gh api") {
		t.Fatalf("Observe = %v, want a truncated page stream reported", err)
	}
}
