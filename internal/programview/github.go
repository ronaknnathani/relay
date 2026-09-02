package programview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ronaknnathani/relay/internal/gitx"
)

const (
	githubTimeout      = 6 * time.Second
	githubPRRefTimeout = 8 * time.Second
	// githubPRStateTTL bounds how long one process reuses an authoritative
	// pull request state before asking GitHub again.
	githubPRStateTTL = 30 * time.Second
	// githubPRRefParallelism bounds concurrent gh subprocesses.
	githubPRRefParallelism = 4
)

// PRState is the GitHub lifecycle state of a pull request.
type PRState string

const (
	PRStateOpen   PRState = "OPEN"
	PRStateMerged PRState = "MERGED"
	PRStateClosed PRState = "CLOSED"
)

// PRIndex resolves recorded pull request references to live GitHub state.
type PRIndex interface {
	Lookup(ref string) (PRState, bool)
}

// PRIndexLoader resolves authoritative state for the recorded pull request
// references a program actually links.
type PRIndexLoader func(repo string, refs []string) PRIndex

type githubPRIndex struct {
	byNumber map[int]PRState
	byURL    map[string]PRState
}

type prStateCacheEntry struct {
	state   PRState
	expires time.Time
}

// prStateCache is a short-lived per-process cache of authoritative pull request
// states so repeated reads in one command do not re-run gh.
type prStateCache struct {
	mu      sync.Mutex
	entries map[string]prStateCacheEntry
	ttl     time.Duration
	now     func() time.Time
}

func newPRStateCache(ttl time.Duration, now func() time.Time) *prStateCache {
	if now == nil {
		now = time.Now
	}
	return &prStateCache{entries: map[string]prStateCacheEntry{}, ttl: ttl, now: now}
}

func (c *prStateCache) get(repo, ref string) (PRState, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[prStateCacheKey(repo, ref)]
	if !found || !c.now().Before(entry.expires) {
		return "", false
	}
	return entry.state, true
}

func (c *prStateCache) put(repo, ref string, state PRState) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[prStateCacheKey(repo, ref)] = prStateCacheEntry{
		state:   state,
		expires: c.now().Add(c.ttl),
	}
}

func prStateCacheKey(repo, ref string) string {
	return repo + "\x00" + strings.TrimSpace(ref)
}

var defaultPRStateCache = newPRStateCache(githubPRStateTTL, time.Now)

type ghPRIndexLoader struct {
	hasOrigin   func(string) bool
	lookPath    func(string) (string, error)
	run         GHCommandRunner
	cache       *prStateCache
	parallelism int
}

// Load resolves only the supplied recorded references, so programs whose
// repositories carry more than one page of history stay correct and cheap.
func (l ghPRIndexLoader) Load(repo string, refs []string) PRIndex {
	wanted := uniqueRefs(refs)
	if len(wanted) == 0 {
		return nil
	}
	if l.hasOrigin == nil || !l.hasOrigin(repo) || l.lookPath == nil || l.run == nil {
		return nil
	}
	if _, err := l.lookPath("gh"); err != nil {
		return nil
	}
	index := githubPRIndex{byNumber: map[int]PRState{}, byURL: map[string]PRState{}}
	var mutex sync.Mutex
	var wait sync.WaitGroup
	limit := l.parallelism
	if limit <= 0 {
		limit = githubPRRefParallelism
	}
	slots := make(chan struct{}, limit)
	for _, ref := range wanted {
		if state, found := l.cache.get(repo, ref); found {
			index.record(ref, "", 0, state)
			continue
		}
		wait.Add(1)
		go func(ref string) {
			defer wait.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			state, url, number, err := fetchPRState(context.Background(), repo, ref, l.run)
			if err != nil {
				return
			}
			l.cache.put(repo, ref, state)
			mutex.Lock()
			defer mutex.Unlock()
			index.record(ref, url, number, state)
		}(ref)
	}
	wait.Wait()
	if len(index.byNumber) == 0 && len(index.byURL) == 0 {
		return nil
	}
	return index
}

func (i githubPRIndex) record(ref, url string, number int, state PRState) {
	if parsed, ok := PullRequestNumber(ref); ok {
		i.byNumber[parsed] = state
	}
	if number > 0 {
		i.byNumber[number] = state
	}
	if trimmed := strings.TrimSpace(ref); trimmed != "" {
		i.byURL[trimmed] = state
	}
	if trimmed := strings.TrimSpace(url); trimmed != "" {
		i.byURL[trimmed] = state
	}
}

func uniqueRefs(refs []string) []string {
	seen := make(map[string]bool, len(refs))
	result := make([]string, 0, len(refs))
	for _, ref := range refs {
		trimmed := strings.TrimSpace(ref)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		result = append(result, trimmed)
	}
	sort.Strings(result)
	return result
}

func (i githubPRIndex) Lookup(ref string) (PRState, bool) {
	trimmed := strings.TrimSpace(ref)
	if state, found := i.byURL[trimmed]; found {
		return state, true
	}
	if number, ok := PullRequestNumber(trimmed); ok {
		state, found := i.byNumber[number]
		return state, found
	}
	return "", false
}

// PullRequestNumber extracts the pull request number from one recorded
// reference, which Relay writes either as a pull request URL or as "#<n>".
func PullRequestNumber(ref string) (int, bool) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "#") {
		return positiveNumber(strings.TrimPrefix(ref, "#"))
	}
	parsed, err := url.Parse(ref)
	if err != nil {
		return 0, false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+1 < len(segments); i++ {
		if segments[i] == "pull" {
			return positiveNumber(segments[i+1])
		}
	}
	return 0, false
}

func positiveNumber(value string) (int, bool) {
	number, err := strconv.Atoi(value)
	return number, err == nil && number > 0
}

// fetchPRState reads one recorded pull request. Referencing the pull request
// directly keeps repositories with long histories correct.
func fetchPRState(ctx context.Context, repo, ref string, runner GHCommandRunner) (PRState, string, int, error) {
	timeoutContext, cancel := context.WithTimeout(ctx, githubPRRefTimeout)
	defer cancel()
	output, err := runner(timeoutContext, repo, "gh", "pr", "view", ref, "--json", "number,state,url")
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return "", "", 0, fmt.Errorf("view pull request %q in %s: %w", ref, repo, err)
		}
		return "", "", 0, fmt.Errorf("view pull request %q in %s: %w: %s", ref, repo, err, detail)
	}
	var response struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return "", "", 0, fmt.Errorf("parse pull request %q JSON: %w", ref, err)
	}
	state, ok := parsePRState(response.State)
	if !ok {
		return "", "", 0, fmt.Errorf("parse pull request %q JSON: unknown state %q", ref, response.State)
	}
	return state, response.URL, response.Number, nil
}

func parsePRState(value string) (PRState, bool) {
	switch PRState(strings.ToUpper(strings.TrimSpace(value))) {
	case PRStateOpen:
		return PRStateOpen, true
	case PRStateMerged:
		return PRStateMerged, true
	case PRStateClosed:
		return PRStateClosed, true
	default:
		return "", false
	}
}

// Fetcher loads live pull request state.
type Fetcher interface {
	Fetch(ctx context.Context, repo, ref string) (PullRequestDTO, error)
}

// memoFetcher serves one pull request read per reference for the lifetime of a
// snapshot, so the lifecycle overlay and the item detail share one GitHub call.
type memoFetcher struct {
	inner   Fetcher
	mu      sync.Mutex
	results map[string]memoResult
}

type memoResult struct {
	pullRequest PullRequestDTO
	err         error
}

func newMemoFetcher(inner Fetcher) Fetcher {
	if inner == nil {
		return nil
	}
	return &memoFetcher{inner: inner, results: map[string]memoResult{}}
}

func (m *memoFetcher) Fetch(ctx context.Context, repo, ref string) (PullRequestDTO, error) {
	key := prStateCacheKey(repo, ref)
	m.mu.Lock()
	result, found := m.results[key]
	m.mu.Unlock()
	if found {
		return result.pullRequest, result.err
	}
	pullRequest, err := m.inner.Fetch(ctx, repo, ref)
	m.mu.Lock()
	m.results[key] = memoResult{pullRequest: pullRequest, err: err}
	m.mu.Unlock()
	return pullRequest, err
}

// fetcherPRIndex resolves lifecycle state through an existing pull request
// fetcher, so a snapshot's authoritative overlay reuses already-fetched or
// cached GitHub data instead of running extra subprocesses.
type fetcherPRIndex struct {
	ctx     context.Context
	repo    string
	fetcher Fetcher
}

// NewFetcherPRIndex returns a PRIndex backed by fetcher.
func NewFetcherPRIndex(ctx context.Context, repo string, fetcher Fetcher) PRIndex {
	if fetcher == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return fetcherPRIndex{ctx: ctx, repo: repo, fetcher: fetcher}
}

func (i fetcherPRIndex) Lookup(ref string) (PRState, bool) {
	pullRequest, err := i.fetcher.Fetch(i.ctx, i.repo, strings.TrimSpace(ref))
	if err != nil {
		return "", false
	}
	return parsePRState(pullRequest.State)
}

// GHCommandRunner executes a gh command in a repository directory.
type GHCommandRunner func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

// GHFetcher loads pull request state with the installed gh CLI.
type GHFetcher struct {
	run GHCommandRunner
}

// GitHubPRIndex resolves authoritative lifecycle state for the supplied
// recorded pull request references, returning nil when GitHub is unavailable.
func GitHubPRIndex(repo string, refs []string) PRIndex {
	return githubPRIndexForRefs(repo, refs)
}

func githubPRIndexForRefs(repo string, refs []string) PRIndex {
	return ghPRIndexLoader{
		hasOrigin: gitx.HasOrigin,
		lookPath:  exec.LookPath,
		run:       runGHCommand,
		cache:     defaultPRStateCache,
	}.Load(repo, refs)
}

func runGHCommand(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	return command.CombinedOutput()
}

// NewGHFetcher creates a Fetcher backed by the installed gh CLI.
func NewGHFetcher() *GHFetcher {
	return NewGHFetcherWithRunner(runGHCommand)
}

// NewGHFetcherWithRunner creates a GHFetcher backed by runner.
func NewGHFetcherWithRunner(runner GHCommandRunner) *GHFetcher {
	return &GHFetcher{run: runner}
}

// Fetch loads and normalizes one pull request.
func (f *GHFetcher) Fetch(ctx context.Context, repo, ref string) (PullRequestDTO, error) {
	if f == nil || f.run == nil {
		return PullRequestDTO{}, fmt.Errorf("GitHub command runner is not configured")
	}
	timeoutContext, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	output, err := f.run(
		timeoutContext,
		repo,
		"gh",
		"pr", "view", ref, "--json",
		"number,url,state,isDraft,mergeable,reviewDecision,statusCheckRollup,title,updatedAt",
	)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return PullRequestDTO{}, fmt.Errorf("fetch pull request %q in %s: %w", ref, repo, err)
		}
		return PullRequestDTO{}, fmt.Errorf("fetch pull request %q in %s: %w: %s", ref, repo, err, detail)
	}
	var response struct {
		Number            int              `json:"number"`
		URL               string           `json:"url"`
		State             string           `json:"state"`
		IsDraft           bool             `json:"isDraft"`
		Mergeable         string           `json:"mergeable"`
		ReviewDecision    string           `json:"reviewDecision"`
		StatusCheckRollup []map[string]any `json:"statusCheckRollup"`
		Title             string           `json:"title"`
		UpdatedAt         string           `json:"updatedAt"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return PullRequestDTO{}, fmt.Errorf("parse pull request %q JSON: %w", ref, err)
	}
	return PullRequestDTO{
		Number:         response.Number,
		Ref:            ref,
		URL:            response.URL,
		State:          strings.ToLower(response.State),
		Draft:          response.IsDraft,
		Mergeable:      strings.ToLower(response.Mergeable),
		ReviewDecision: strings.ToLower(response.ReviewDecision),
		Checks:         normalizeChecks(response.StatusCheckRollup),
		Title:          response.Title,
		UpdatedAt:      response.UpdatedAt,
		FetchedAt:      time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func normalizeChecks(checks []map[string]any) string {
	if len(checks) == 0 {
		return "none"
	}
	passing := false
	pending := false
	unknown := false
	for _, check := range checks {
		value := firstCheckValue(check, "conclusion", "state", "status")
		switch value {
		case "FAILURE", "ERROR", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE": //nolint:misspell // GitHub enum spelling.
			return "failing"
		case "PENDING", "QUEUED", "IN_PROGRESS", "EXPECTED", "WAITING":
			pending = true
		case "SUCCESS", "NEUTRAL", "SKIPPED", "COMPLETED":
			passing = true
		default:
			unknown = true
		}
	}
	switch {
	case pending:
		return "pending"
	case unknown:
		return "unknown"
	case passing:
		return "passing"
	default:
		return "unknown"
	}
}

func firstCheckValue(check map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := check[key].(string)
		if ok && value != "" {
			return strings.ToUpper(value)
		}
	}
	return ""
}
