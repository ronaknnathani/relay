package programview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// PullRequestProof contains the GitHub fields needed to bind a merged pull
// request to the recorded repository, base branch, head branch, and head SHA.
type PullRequestProof struct {
	State      PRState
	Repository string
	BaseBranch string
	HeadBranch string
	HeadSHA    string
}

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
	originURL   func(string) (string, error)
	lookPath    func(string) (string, error)
	run         GHCommandRunner
	cache       *prStateCache
	parallelism int
}

type ghPullRequestLookup struct {
	originURL func(string) (string, error)
	lookPath  func(string) (string, error)
	run       GHCommandRunner
}

func (l ghPullRequestLookup) validate(repo, operation string) (string, error) {
	if l.originURL == nil {
		return "", fmt.Errorf("%s in %s: origin URL lookup is not configured", operation, repo)
	}
	rawOrigin, err := l.originURL(repo)
	if err != nil {
		return "", fmt.Errorf("%s in %s: resolve origin URL: %w", operation, repo, err)
	}
	repository, err := gitHubRepositoryFromRemote(rawOrigin)
	if err != nil {
		return "", fmt.Errorf("%s in %s: resolve origin repository: %w", operation, repo, err)
	}
	if l.lookPath == nil {
		return "", fmt.Errorf("%s in %s: gh lookup is not configured", operation, repo)
	}
	if _, err := l.lookPath("gh"); err != nil {
		return "", fmt.Errorf("%s in %s: find gh: %w", operation, repo, err)
	}
	if l.run == nil {
		return "", fmt.Errorf("%s in %s: GitHub command runner is not configured", operation, repo)
	}
	return repository, nil
}

// Load resolves only the supplied recorded references, so programs whose
// repositories carry more than one page of history stay correct and cheap.
func (l ghPRIndexLoader) Load(repo string, refs []string) PRIndex {
	wanted := uniqueRefs(refs)
	if len(wanted) == 0 {
		return nil
	}
	if l.originURL == nil || l.lookPath == nil || l.run == nil {
		return nil
	}
	rawOrigin, err := l.originURL(repo)
	if err != nil {
		return nil
	}
	repository, err := gitHubRepositoryFromRemote(rawOrigin)
	if err != nil {
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
		selector, err := normalizePullRequestReference(ref, repository)
		if err != nil {
			continue
		}
		if state, found := l.cache.get(repo, ref); found {
			index.record(ref, "", 0, state)
			continue
		}
		wait.Add(1)
		go func(ref, selector string) {
			defer wait.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			state, url, number, err := fetchPRState(
				context.Background(), repo, repository, selector, ref, l.run,
			)
			if err != nil {
				return
			}
			l.cache.put(repo, ref, state)
			mutex.Lock()
			defer mutex.Unlock()
			index.record(ref, url, number, state)
		}(ref, selector)
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
	if err != nil || parsed.Scheme != "https" || parsed.User != nil ||
		parsed.Hostname() == "" || parsed.Port() != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return 0, false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 4 || segments[0] == "" || segments[1] == "" || segments[2] != "pull" {
		return 0, false
	}
	return positiveNumber(segments[3])
}

func positiveNumber(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	number, err := strconv.Atoi(value)
	if err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}

// Lookup resolves one pull request and returns the repository and head commit
// needed to verify that it belongs to the local project branch.
func (l ghPullRequestLookup) Lookup(repo, ref string) (PullRequestProof, error) {
	safeRef := gitx.SanitizeDiagnostic(ref)
	repository, err := l.validate(repo, fmt.Sprintf("lookup pull request %q", safeRef))
	if err != nil {
		return PullRequestProof{}, err
	}
	selector, err := normalizePullRequestReference(ref, repository)
	if err != nil {
		return PullRequestProof{}, fmt.Errorf("lookup pull request %q in %s: %w", safeRef, repo, err)
	}

	timeoutContext, cancel := context.WithTimeout(context.Background(), githubPRRefTimeout)
	defer cancel()
	output, err := l.run(
		timeoutContext, repo, "gh", "pr", "view", selector,
		"--repo", repository,
		"--json", "state,url,baseRefName,headRefName,headRefOid",
	)
	if err != nil {
		detail := gitx.SanitizeDiagnostic(string(output))
		if detail == "" {
			return PullRequestProof{}, fmt.Errorf("view pull request %q in %s: %w", safeRef, repo, err)
		}
		return PullRequestProof{}, fmt.Errorf("view pull request %q in %s: %w: %s", safeRef, repo, err, detail)
	}
	var response struct {
		State       string `json:"state"`
		URL         string `json:"url"`
		BaseRefName string `json:"baseRefName"`
		HeadRefName string `json:"headRefName"`
		HeadRefOID  string `json:"headRefOid"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return PullRequestProof{}, fmt.Errorf("parse pull request %q JSON: %w", safeRef, err)
	}
	state, ok := parsePRState(response.State)
	if !ok {
		return PullRequestProof{}, fmt.Errorf("parse pull request %q JSON: unknown state %q", safeRef, response.State)
	}
	responseRepository, err := pullRequestRepository(response.URL)
	if err != nil {
		return PullRequestProof{}, fmt.Errorf("parse pull request %q repository: %w", safeRef, err)
	}
	if !strings.EqualFold(responseRepository, repository) {
		return PullRequestProof{}, fmt.Errorf(
			"pull request %q belongs to repository %q, want %q",
			safeRef, responseRepository, repository,
		)
	}
	return PullRequestProof{
		State:      state,
		Repository: responseRepository,
		BaseBranch: strings.TrimSpace(response.BaseRefName),
		HeadBranch: strings.TrimSpace(response.HeadRefName),
		HeadSHA:    strings.TrimSpace(response.HeadRefOID),
	}, nil
}

func (l ghPullRequestLookup) Repository(repo string) (string, error) {
	if l.originURL == nil {
		return "", fmt.Errorf("resolve GitHub repository in %s: origin URL lookup is not configured", repo)
	}
	rawOrigin, err := l.originURL(repo)
	if err != nil {
		return "", fmt.Errorf("resolve GitHub repository in %s: resolve origin URL: %w", repo, err)
	}
	repository, err := gitHubRepositoryFromRemote(rawOrigin)
	if err != nil {
		return "", fmt.Errorf("resolve GitHub repository in %s: %w", repo, err)
	}
	return repository, nil
}

func pullRequestRepository(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil ||
		parsed.Hostname() == "" || parsed.Port() != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		if err == nil {
			err = errors.New("URL is not a canonical HTTPS pull request URL")
		}
		return "", fmt.Errorf("parse URL %q: %w", gitx.SanitizeDiagnostic(rawURL), err)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 4 || segments[0] == "" || segments[1] == "" || segments[2] != "pull" {
		return "", fmt.Errorf(
			"URL %q does not identify a pull request repository",
			gitx.SanitizeDiagnostic(rawURL),
		)
	}
	if _, ok := positiveNumber(segments[3]); !ok {
		return "", fmt.Errorf("URL %q has an invalid pull request number", gitx.SanitizeDiagnostic(rawURL))
	}
	return repositoryIdentity(rawURL, segments[0]+"/"+segments[1])
}

func normalizePullRequestReference(ref, repository string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if number, ok := PullRequestNumber(trimmed); ok {
		if strings.HasPrefix(trimmed, "#") {
			return strconv.Itoa(number), nil
		}
		refRepository, err := pullRequestRepository(trimmed)
		if err != nil {
			return "", err
		}
		if !strings.EqualFold(refRepository, repository) {
			return "", fmt.Errorf(
				"pull request URL belongs to repository %q, want %q",
				sanitizeRepositoryValue(refRepository), sanitizeRepositoryValue(repository),
			)
		}
		return strconv.Itoa(number), nil
	}
	return "", fmt.Errorf(
		"reference must be #<positive-number> or a canonical pull request URL for %s",
		sanitizeRepositoryValue(repository),
	)
}

func gitHubRepositoryFromRemote(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", errors.New("origin URL is empty")
	}
	if strings.Contains(trimmed, "://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", fmt.Errorf("parse origin URL %q: %w", gitx.SanitizeDiagnostic(trimmed), err)
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "ssh", "git":
		default:
			return "", fmt.Errorf("origin URL %q uses unsupported scheme %q", gitx.SanitizeDiagnostic(trimmed), parsed.Scheme)
		}
		if parsed.Hostname() == "" {
			return "", fmt.Errorf("origin URL %q has no host", gitx.SanitizeDiagnostic(trimmed))
		}
		return repositoryIdentity(trimmed, strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git"))
	}

	at := strings.LastIndexByte(trimmed, '@')
	hostPath := trimmed
	if at >= 0 {
		hostPath = trimmed[at+1:]
	}
	host, repoPath, ok := strings.Cut(hostPath, ":")
	if !ok || host == "" || repoPath == "" {
		return "", fmt.Errorf("origin URL %q is not a supported GitHub remote URL", gitx.SanitizeDiagnostic(trimmed))
	}
	repoPath = stripURLSuffix(repoPath)
	return repositoryIdentity("ssh://"+host+"/"+repoPath, strings.TrimSuffix(strings.Trim(repoPath, "/"), ".git"))
}

func repositoryIdentity(rawURL, nameWithOwner string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("parse URL %q: %w", gitx.SanitizeDiagnostic(rawURL), err)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return "", fmt.Errorf("URL %q has no host", gitx.SanitizeDiagnostic(rawURL))
	}
	nameWithOwner = strings.Trim(strings.TrimSpace(nameWithOwner), "/")
	nameParts := strings.Split(nameWithOwner, "/")
	if len(nameParts) != 2 || nameParts[0] == "" || nameParts[1] == "" {
		return "", fmt.Errorf(
			"repository name %q is not owner/repository",
			sanitizeRepositoryValue(nameWithOwner),
		)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 2 {
		return "", fmt.Errorf("URL %q has no owner/repository path", gitx.SanitizeDiagnostic(rawURL))
	}
	urlName := segments[0] + "/" + strings.TrimSuffix(segments[1], ".git")
	if !strings.EqualFold(urlName, nameWithOwner) {
		return "", fmt.Errorf(
			"URL %q identifies repository %q, want %q",
			gitx.SanitizeDiagnostic(rawURL),
			sanitizeRepositoryValue(urlName),
			sanitizeRepositoryValue(nameWithOwner),
		)
	}
	return host + "/" + urlName, nil
}

func stripURLSuffix(value string) string {
	if suffix := strings.IndexAny(value, "?#"); suffix >= 0 {
		return value[:suffix]
	}
	return value
}

func sanitizeRepositoryValue(value string) string {
	return gitx.SanitizeDiagnostic(stripURLSuffix(value))
}

// fetchPRState reads one recorded pull request. Referencing the pull request
// directly keeps repositories with long histories correct.
func fetchPRState(
	ctx context.Context,
	repo, repository, selector, ref string,
	runner GHCommandRunner,
) (PRState, string, int, error) {
	safeRef := gitx.SanitizeDiagnostic(ref)
	timeoutContext, cancel := context.WithTimeout(ctx, githubPRRefTimeout)
	defer cancel()
	output, err := runner(
		timeoutContext, repo, "gh", "pr", "view", selector,
		"--repo", repository,
		"--json", "number,state,url",
	)
	if err != nil {
		detail := gitx.SanitizeDiagnostic(string(output))
		if detail == "" {
			return "", "", 0, fmt.Errorf("view pull request %q in %s: %w", safeRef, repo, err)
		}
		return "", "", 0, fmt.Errorf("view pull request %q in %s: %w: %s", safeRef, repo, err, detail)
	}
	var response struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return "", "", 0, fmt.Errorf("parse pull request %q JSON: %w", safeRef, err)
	}
	state, ok := parsePRState(response.State)
	if !ok {
		return "", "", 0, fmt.Errorf("parse pull request %q JSON: unknown state %q", safeRef, response.State)
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

// GitHubPullRequestProof resolves one pull request with repository and head
// identity metadata. Lookup failures are returned to callers.
func GitHubPullRequestProof(repo, ref string) (PullRequestProof, error) {
	return ghPullRequestLookup{
		originURL: gitx.OriginURL,
		lookPath:  exec.LookPath,
		run:       runGHCommand,
	}.Lookup(repo, ref)
}

// GitHubRepository resolves the GitHub owner/name associated with repo.
func GitHubRepository(repo string) (string, error) {
	return ghPullRequestLookup{
		originURL: gitx.OriginURL,
	}.Repository(repo)
}

func githubPRIndexForRefs(repo string, refs []string) PRIndex {
	return ghPRIndexLoader{
		originURL: gitx.OriginURL,
		lookPath:  exec.LookPath,
		run:       runGHCommand,
		cache:     defaultPRStateCache,
	}.Load(repo, refs)
}

func runGHCommand(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return stderr.Bytes(), err
	}
	return stdout.Bytes(), nil
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
	safeRef := gitx.SanitizeDiagnostic(ref)
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
		detail := gitx.SanitizeDiagnostic(string(output))
		if detail == "" {
			return PullRequestDTO{}, fmt.Errorf("fetch pull request %q in %s: %w", safeRef, repo, err)
		}
		return PullRequestDTO{}, fmt.Errorf("fetch pull request %q in %s: %w: %s", safeRef, repo, err, detail)
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
		return PullRequestDTO{}, fmt.Errorf("parse pull request %q JSON: %w", safeRef, err)
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
