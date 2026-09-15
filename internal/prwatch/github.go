package prwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Runner executes one gh command inside a working directory and returns its
// standard output.
type Runner interface {
	Run(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(ctx context.Context, dir string, args ...string) ([]byte, error)

// Run executes the adapted function.
func (f RunnerFunc) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return f(ctx, dir, args...)
}

type recoverableGitHubAccessError struct {
	cause error
}

type ghCommandError struct {
	args   string
	cause  error
	detail string
}

func (e *ghCommandError) Error() string {
	if e.detail == "" {
		return fmt.Sprintf("run gh %s: %v", e.args, e.cause)
	}
	return fmt.Sprintf("run gh %s: %v: %s", e.args, e.cause, e.detail)
}

func (e *ghCommandError) Unwrap() error {
	return e.cause
}

func (e *recoverableGitHubAccessError) Error() string {
	return e.cause.Error()
}

func (e *recoverableGitHubAccessError) Unwrap() error {
	return e.cause
}

var (
	githubAllowListDiagnosticPattern = regexp.MustCompile(
		`(?i)^(?:(?:gh|graphql):\s*)?although you appear to have the correct authorization credentials, ` +
			"the `[^`\r\n]+` organization has an ip allow[ -]?list enabled, and your ip address is " +
			`not permitted to access this resource\.(?: \(repository\))?(?: \(HTTP 403\))?$`,
	)
	githubRateLimitDiagnosticPattern = regexp.MustCompile(
		`(?i)^(?:(?:gh|graphql):\s*)?(?:` +
			`api rate limit (?:already )?exceeded(?: for (?:user id [0-9]+|[0-9a-f:.]+))?` +
			`|you have exceeded a secondary rate limit` +
			`)(?:\.|\. please wait a few minutes before you try again\.)?(?: \(HTTP (?:403|429)\))?$`,
	)
)

func classifyGitHubAccessError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var accessErr *recoverableGitHubAccessError
	if errors.As(err, &accessErr) {
		return err
	}

	detail := err.Error()
	var commandErr *ghCommandError
	if errors.As(err, &commandErr) {
		detail = commandErr.detail
	}
	foundDiagnostic := false
	for _, line := range strings.Split(detail, "\n") {
		diagnostic := strings.TrimSpace(line)
		if diagnostic == "" {
			continue
		}
		foundDiagnostic = true
		if !githubAllowListDiagnosticPattern.MatchString(diagnostic) &&
			!githubRateLimitDiagnosticPattern.MatchString(diagnostic) {
			return err
		}
	}
	if foundDiagnostic {
		return &recoverableGitHubAccessError{cause: err}
	}
	return err
}

func isRecoverableGitHubAccessError(err error) bool {
	var accessErr *recoverableGitHubAccessError
	return errors.As(err, &accessErr)
}

// GitHub observation bounds. Every call is retried a small number of times
// because `gh` fails transiently on network and rate-limit errors, and a failed
// observation is an error rather than a quiet "nothing to do".
const (
	defaultCommandTimeout = 45 * time.Second
	defaultAttempts       = 3
	defaultBackoff        = 2 * time.Second
	// reviewThreadCommentPage bounds how many of a thread's most recent
	// comments one observation carries. The latest activity decides whether a
	// thread is actionable, so older replies beyond this bound are counted but
	// not fetched.
	reviewThreadCommentPage = 100
	reviewThreadPage        = 50
	// checkContextPage is the GraphQL page size for a commit's check contexts.
	// It is the API maximum, so a pull request with a normal number of checks
	// costs exactly one request.
	checkContextPage = 100
)

// NewCLIRunner returns a Runner backed by the installed gh binary. Each command
// is bounded by timeout and by the caller's context.
func NewCLIRunner(timeout time.Duration) Runner {
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	return RunnerFunc(func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		commandCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		command := exec.CommandContext(commandCtx, "gh", args...)
		command.Dir = dir
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		if commandCtx.Err() != nil {
			return nil, fmt.Errorf("run gh %s: %w", strings.Join(args, " "), commandCtx.Err())
		}
		if err != nil {
			detail := strings.TrimSpace(stderr.String())
			return nil, &ghCommandError{
				args:   strings.Join(args, " "),
				cause:  err,
				detail: detail,
			}
		}
		return stdout.Bytes(), nil
	})
}

// Client observes one pull request through the gh CLI. It never mutates
// anything: every command it runs is a read.
type Client struct {
	runner   Runner
	dir      string
	attempts int
	backoff  time.Duration
	sleep    func(time.Duration)
}

// NewClient creates a Client that runs gh inside dir.
func NewClient(runner Runner, dir string) *Client {
	return &Client{
		runner:   runner,
		dir:      dir,
		attempts: defaultAttempts,
		backoff:  defaultBackoff,
		sleep:    time.Sleep,
	}
}

// Actor is the GitHub identity that authored one observed activity.
type Actor struct {
	Login string `json:"login"`
	Bot   bool   `json:"bot"`
}

// Activity is one observed comment, review body, or thread reply.
type Activity struct {
	ID        string `json:"id"`
	Author    Actor  `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	URL       string `json:"url,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	// InReplyTo is the inline comment this one replies to, which chains an
	// inline comment onto the thread it belongs to.
	InReplyTo string `json:"in_reply_to,omitempty"`
	// State carries a review's decision (APPROVED, CHANGES_REQUESTED, ...) and
	// is empty for plain comments.
	State string `json:"state,omitempty"`
}

// Check is one observed status check or check run.
type Check struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	DetailsURL string `json:"details_url,omitempty"`
	// RunID is the Actions workflow run the check belongs to, parsed from the
	// details URL when GitHub exposes one.
	RunID string `json:"run_id,omitempty"`
}

// ReviewThread is one observed review thread with its resolution state and its
// most recent replies.
type ReviewThread struct {
	ID            string     `json:"id"`
	IsResolved    bool       `json:"is_resolved"`
	IsOutdated    bool       `json:"is_outdated"`
	Path          string     `json:"path,omitempty"`
	Line          int        `json:"line,omitempty"`
	Comments      []Activity `json:"comments"`
	CommentsTotal int        `json:"comments_total"`
}

// Observation is everything one GitHub read collected about a pull request.
type Observation struct {
	PR             PullRequest    `json:"pr"`
	Checks         []Check        `json:"checks"`
	Comments       []Activity     `json:"comments"`
	Reviews        []Activity     `json:"reviews"`
	InlineComments []Activity     `json:"inline_comments"`
	Threads        []ReviewThread `json:"threads"`
}

// Observe reads the pull request, its checks, every page of its conversation
// comments, review bodies, and inline comments, and every review thread with
// its resolution state.
func (c *Client) Observe(ctx context.Context, number int) (Observation, error) {
	repo, defaultBranch, err := c.repo(ctx)
	if err != nil {
		return Observation{}, err
	}
	owner, name, found := strings.Cut(repo, "/")
	if !found {
		return Observation{}, fmt.Errorf("parse GitHub repository %q: want owner/name", repo)
	}
	pr, err := c.pullRequest(ctx, number)
	if err != nil {
		return Observation{}, err
	}
	pr.Repo = repo
	pr.DefaultBranch = defaultBranch

	checks, err := c.checks(ctx, owner, name, number)
	if err != nil {
		return Observation{}, err
	}

	comments, err := c.restActivities(
		ctx, fmt.Sprintf("repos/%s/%s/issues/%d/comments", owner, name, number),
	)
	if err != nil {
		return Observation{}, err
	}
	reviews, err := c.restActivities(
		ctx, fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", owner, name, number),
	)
	if err != nil {
		return Observation{}, err
	}
	inline, err := c.restActivities(
		ctx, fmt.Sprintf("repos/%s/%s/pulls/%d/comments", owner, name, number),
	)
	if err != nil {
		return Observation{}, err
	}
	threads, err := c.reviewThreads(ctx, owner, name, number)
	if err != nil {
		return Observation{}, err
	}
	return Observation{
		PR:             pr,
		Checks:         checks,
		Comments:       comments,
		Reviews:        reviews,
		InlineComments: inline,
		Threads:        threads,
	}, nil
}

func (c *Client) repo(ctx context.Context) (string, string, error) {
	var response struct {
		NameWithOwner    string `json:"nameWithOwner"`
		DefaultBranchRef struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	}
	output, err := c.run(ctx, "repo", "view", "--json", "nameWithOwner,defaultBranchRef")
	if err != nil {
		return "", "", err
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return "", "", fmt.Errorf("parse gh repo view JSON: %w", err)
	}
	if response.NameWithOwner == "" {
		return "", "", errors.New("gh repo view returned no repository name")
	}
	return response.NameWithOwner, response.DefaultBranchRef.Name, nil
}

// pullRequestFields deliberately excludes statusCheckRollup. `gh pr view`
// returns only the first page of a commit's check contexts with no way to see
// that more exist, so a pull request with many required checks silently loses
// the rest — including failing ones. Checks are read through their own
// paginated connection instead.
const pullRequestFields = "number,url,title,state,isDraft,baseRefName,baseRefOid," +
	"headRefName,headRefOid,mergeStateStatus,mergeable,reviewDecision,autoMergeRequest,author"

func (c *Client) pullRequest(ctx context.Context, number int) (PullRequest, error) {
	var response struct {
		Number           int    `json:"number"`
		URL              string `json:"url"`
		Title            string `json:"title"`
		State            string `json:"state"`
		IsDraft          bool   `json:"isDraft"`
		BaseRefName      string `json:"baseRefName"`
		BaseRefOid       string `json:"baseRefOid"`
		HeadRefName      string `json:"headRefName"`
		HeadRefOid       string `json:"headRefOid"`
		MergeStateStatus string `json:"mergeStateStatus"`
		Mergeable        string `json:"mergeable"`
		ReviewDecision   string `json:"reviewDecision"`
		AutoMergeRequest *struct {
			EnabledAt string `json:"enabledAt"`
		} `json:"autoMergeRequest"`
		Author struct {
			Login string `json:"login"`
			IsBot bool   `json:"is_bot"`
		} `json:"author"`
	}
	output, err := c.run(ctx, "pr", "view", strconv.Itoa(number), "--json", pullRequestFields)
	if err != nil {
		return PullRequest{}, err
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return PullRequest{}, fmt.Errorf("parse gh pr view JSON for pull request %d: %w", number, err)
	}
	pr := PullRequest{
		Number:           response.Number,
		URL:              response.URL,
		Title:            response.Title,
		State:            strings.ToUpper(response.State),
		Draft:            response.IsDraft,
		BaseRef:          response.BaseRefName,
		BaseSHA:          response.BaseRefOid,
		HeadRef:          response.HeadRefName,
		HeadSHA:          response.HeadRefOid,
		MergeStateStatus: strings.ToUpper(response.MergeStateStatus),
		Mergeable:        strings.ToUpper(response.Mergeable),
		ReviewDecision:   strings.ToUpper(response.ReviewDecision),
		AutoMerge:        response.AutoMergeRequest != nil,
		Author:           response.Author.Login,
	}
	return pr, nil
}

// checkQuery reads every check context on the pull request's newest commit
// through its own connection, so `gh api graphql --paginate` follows the cursor
// until GitHub reports no further page.
const checkQuery = `query($owner:String!,$repo:String!,$number:Int!,$endCursor:String){
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      statusCheckRollup: commits(last:1){
        nodes{
          commit{
            oid
            statusCheckRollup{
              contexts(first:%d, after:$endCursor){
                pageInfo{hasNextPage endCursor}
                nodes{
                  __typename
                  ... on CheckRun{name status conclusion detailsUrl}
                  ... on StatusContext{context state targetUrl}
                }
              }
            }
          }
        }
      }
    }
  }
}`

// graphQLErrors is the error list a GraphQL response carries when GitHub
// answers only part of a query. Although current gh versions report these as
// command failures, every response is still inspected defensively so partial
// data cannot look like a quiet pull request.
type graphQLErrors struct {
	Errors []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"errors"`
}

func (e graphQLErrors) err(query string) error {
	if len(e.Errors) == 0 {
		return nil
	}
	messages := make([]string, 0, len(e.Errors))
	allRecoverable := true
	for _, entry := range e.Errors {
		message := firstNonEmpty(strings.TrimSpace(entry.Message), entry.Type, "unknown error")
		messages = append(messages, message)
		recoverable := strings.EqualFold(strings.TrimSpace(entry.Type), "RATE_LIMITED") ||
			isRecoverableGitHubAccessError(classifyGitHubAccessError(errors.New(message)))
		allRecoverable = allRecoverable && recoverable
	}
	err := fmt.Errorf(
		"gh api graphql answered the %s query with %d error(s): %s",
		query, len(e.Errors), strings.Join(messages, "; "),
	)
	if allRecoverable {
		return &recoverableGitHubAccessError{cause: err}
	}
	return err
}

// checkPageResponse is one page of the check-context GraphQL query.
type checkPageResponse struct {
	graphQLErrors
	Data struct {
		Repository struct {
			PullRequest struct {
				StatusCheckRollup struct {
					Nodes []struct {
						Commit struct {
							OID               string `json:"oid"`
							StatusCheckRollup *struct {
								Contexts struct {
									PageInfo struct {
										HasNextPage bool   `json:"hasNextPage"`
										EndCursor   string `json:"endCursor"`
									} `json:"pageInfo"`
									Nodes []struct {
										TypeName   string `json:"__typename"`
										Name       string `json:"name"`
										Context    string `json:"context"`
										Status     string `json:"status"`
										State      string `json:"state"`
										Conclusion string `json:"conclusion"`
										DetailsURL string `json:"detailsUrl"`
										TargetURL  string `json:"targetUrl"`
									} `json:"nodes"`
								} `json:"contexts"`
							} `json:"statusCheckRollup"`
						} `json:"commit"`
					} `json:"nodes"`
				} `json:"statusCheckRollup"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

func (c *Client) checks(ctx context.Context, owner, repo string, number int) ([]Check, error) {
	query := fmt.Sprintf(checkQuery, checkContextPage)
	output, err := c.run(
		ctx, "api", "graphql", "--paginate",
		"-F", "owner="+owner,
		"-F", "repo="+repo,
		"-F", "number="+strconv.Itoa(number),
		"-f", "query="+query,
	)
	if err != nil {
		return nil, err
	}
	pages, err := decodeJSONStream[checkPageResponse](output)
	if err != nil {
		return nil, fmt.Errorf("parse gh api graphql checks JSON: %w", err)
	}
	var checks []Check
	truncated := false
	for _, page := range pages {
		if err := page.err("check context"); err != nil {
			return nil, err
		}
		for _, node := range page.Data.Repository.PullRequest.StatusCheckRollup.Nodes {
			rollup := node.Commit.StatusCheckRollup
			if rollup == nil {
				continue
			}
			truncated = rollup.Contexts.PageInfo.HasNextPage
			for _, context := range rollup.Contexts.Nodes {
				check := Check{
					Name:       firstNonEmpty(context.Name, context.Context),
					Status:     strings.ToUpper(firstNonEmpty(context.Status, context.State)),
					Conclusion: strings.ToUpper(firstNonEmpty(context.Conclusion, context.State)),
					DetailsURL: firstNonEmpty(context.DetailsURL, context.TargetURL),
				}
				check.RunID = actionsRunID(check.DetailsURL)
				checks = append(checks, check)
			}
		}
	}
	// The last page still promising another one means pagination stopped early.
	// A short check list is indistinguishable from a green pull request, so this
	// is an observation error rather than a quieter answer.
	if truncated {
		return nil, fmt.Errorf(
			"gh api graphql returned a truncated check list for pull request %d: "+
				"%d contexts read and GitHub still reports another page",
			number, len(checks),
		)
	}
	return checks, nil
}

var actionsRunPattern = regexp.MustCompile(`/actions/runs/(\d+)`)

// actionsRunID extracts the Actions workflow run a check belongs to, so a
// woken owner can act on the exact run without re-deriving it.
func actionsRunID(detailsURL string) string {
	match := actionsRunPattern.FindStringSubmatch(detailsURL)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

// restActivity is one comment, review, or inline comment as GitHub's REST API
// returns it.
type restActivity struct {
	ID   int64 `json:"id"`
	User struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	Body        string `json:"body"`
	State       string `json:"state"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	SubmittedAt string `json:"submitted_at"`
	HTMLURL     string `json:"html_url"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	StartLine   int    `json:"start_line"`
	InReplyToID int64  `json:"in_reply_to_id"`
}

// restActivities reads every page of one REST collection.
//
// Completeness here rests on `gh api --paginate`, which follows the Link
// header's `rel="next"` until GitHub stops sending one and exits nonzero the
// moment any page fails — so a zero exit means every page was read. Unlike a
// GraphQL connection, the payload carries no cursor and no total to check
// afterwards, so there is nothing to verify and nothing worth inventing: a
// hand-rolled "looks like a full page" heuristic would fail exactly where
// GitHub's page size is not what it was guessed to be. What is enforced is that
// a failed command and a stream that does not decode cleanly are both
// observation errors, never a short list quietly accepted.
func (c *Client) restActivities(ctx context.Context, path string) ([]Activity, error) {
	output, err := c.run(ctx, "api", path, "--paginate")
	if err != nil {
		return nil, err
	}
	pages, err := decodeJSONStream[[]restActivity](output)
	if err != nil {
		return nil, fmt.Errorf("parse gh api %s JSON: %w", path, err)
	}
	var activities []Activity
	for _, page := range pages {
		for _, entry := range page {
			activities = append(activities, Activity{
				ID:        strconv.FormatInt(entry.ID, 10),
				Author:    Actor{Login: entry.User.Login, Bot: isBotIdentity(entry.User.Login, entry.User.Type)},
				Body:      entry.Body,
				CreatedAt: firstNonEmpty(entry.CreatedAt, entry.SubmittedAt),
				UpdatedAt: firstNonEmpty(entry.UpdatedAt, entry.SubmittedAt, entry.CreatedAt),
				URL:       entry.HTMLURL,
				Path:      entry.Path,
				Line:      firstPositive(entry.Line, entry.StartLine),
				InReplyTo: optionalID(entry.InReplyToID),
				State:     strings.ToUpper(entry.State),
			})
		}
	}
	return activities, nil
}

const reviewThreadQuery = `query($owner:String!,$repo:String!,$number:Int!,$endCursor:String){
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      reviewThreads(first:%d, after:$endCursor){
        pageInfo{hasNextPage endCursor}
        nodes{
          id isResolved isOutdated path line
          comments(last:%d){
            totalCount
            nodes{
              databaseId body createdAt updatedAt url path line
              author{login __typename}
            }
          }
        }
      }
    }
  }
}`

// reviewThreadPageResponse is one page of the review-thread GraphQL query.
type reviewThreadPageResponse struct {
	graphQLErrors
	Data struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						ID         string `json:"id"`
						IsResolved bool   `json:"isResolved"`
						IsOutdated bool   `json:"isOutdated"`
						Path       string `json:"path"`
						Line       int    `json:"line"`
						Comments   struct {
							TotalCount int `json:"totalCount"`
							Nodes      []struct {
								DatabaseID int64  `json:"databaseId"`
								Body       string `json:"body"`
								CreatedAt  string `json:"createdAt"`
								UpdatedAt  string `json:"updatedAt"`
								URL        string `json:"url"`
								Path       string `json:"path"`
								Line       int    `json:"line"`
								Author     struct {
									Login    string `json:"login"`
									TypeName string `json:"__typename"`
								} `json:"author"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

func (c *Client) reviewThreads(ctx context.Context, owner, repo string, number int) ([]ReviewThread, error) {
	query := fmt.Sprintf(reviewThreadQuery, reviewThreadPage, reviewThreadCommentPage)
	output, err := c.run(
		ctx, "api", "graphql", "--paginate",
		"-F", "owner="+owner,
		"-F", "repo="+repo,
		"-F", "number="+strconv.Itoa(number),
		"-f", "query="+query,
	)
	if err != nil {
		return nil, err
	}
	pages, err := decodeJSONStream[reviewThreadPageResponse](output)
	if err != nil {
		return nil, fmt.Errorf("parse gh api graphql review threads JSON: %w", err)
	}
	var threads []ReviewThread
	truncated := false
	for _, page := range pages {
		if err := page.err("review thread"); err != nil {
			return nil, err
		}
		reviewThreads := page.Data.Repository.PullRequest.ReviewThreads
		truncated = reviewThreads.PageInfo.HasNextPage
		for _, node := range reviewThreads.Nodes {
			thread := ReviewThread{
				ID:            node.ID,
				IsResolved:    node.IsResolved,
				IsOutdated:    node.IsOutdated,
				Path:          node.Path,
				Line:          node.Line,
				CommentsTotal: node.Comments.TotalCount,
				Comments:      make([]Activity, 0, len(node.Comments.Nodes)),
			}
			for _, comment := range node.Comments.Nodes {
				thread.Comments = append(thread.Comments, Activity{
					ID:        strconv.FormatInt(comment.DatabaseID, 10),
					Author:    Actor{Login: comment.Author.Login, Bot: isBotIdentity(comment.Author.Login, comment.Author.TypeName)},
					Body:      comment.Body,
					CreatedAt: comment.CreatedAt,
					UpdatedAt: firstNonEmpty(comment.UpdatedAt, comment.CreatedAt),
					URL:       comment.URL,
					Path:      firstNonEmpty(comment.Path, node.Path),
					Line:      firstPositive(comment.Line, node.Line),
				})
			}
			threads = append(threads, thread)
		}
	}
	// The last page still promising another one means pagination stopped early.
	// A short thread list is indistinguishable from a pull request whose review
	// conversations are all answered, so this is an observation error rather
	// than a quieter answer.
	if truncated {
		return nil, fmt.Errorf(
			"gh api graphql returned a truncated review thread list for pull request %d: "+
				"%d threads read and GitHub still reports another page",
			number, len(threads),
		)
	}
	return threads, nil
}

// isBotIdentity reports whether a GitHub identity is an app rather than a
// person. The rule is deliberately mechanical: GitHub's own account type, or
// the reserved `[bot]` login suffix. Nothing about the body is interpreted.
func isBotIdentity(login, accountType string) bool {
	return strings.EqualFold(accountType, "Bot") || strings.HasSuffix(login, "[bot]")
}

// decodeJSONStream decodes the concatenated JSON values `gh --paginate` emits,
// one value per page.
func decodeJSONStream[T any](output []byte) ([]T, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	var pages []T
	for {
		var page T
		err := decoder.Decode(&page)
		if errors.Is(err, io.EOF) {
			return pages, nil
		}
		if err != nil {
			return nil, err
		}
		pages = append(pages, page)
	}
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	attempts := c.attempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("observe GitHub: %w", ctx.Err())
		}
		output, err := c.runner.Run(ctx, c.dir, args...)
		if err == nil {
			return output, nil
		}
		lastErr = err
		if attempt < attempts {
			c.sleep(c.backoff)
		}
	}
	return nil, fmt.Errorf(
		"gh %s failed after %d attempts: %w",
		strings.Join(args, " "), attempts, classifyGitHubAccessError(lastErr),
	)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// optionalID renders an identifier GitHub omits as an empty string rather than
// as the literal zero, so an absent parent is never mistaken for comment 0.
func optionalID(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
