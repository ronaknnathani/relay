package prwatch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Inspection is the read-only pull request truth a caller needs to decide
// where a change request may safely go. It is deliberately small: it carries
// the exact GitHub state fields that routing depends on and nothing else, and
// producing it never touches watcher runtime state or a digest.
type Inspection struct {
	Project          string `json:"project"`
	Number           int    `json:"number"`
	Ref              string `json:"ref"`
	URL              string `json:"url"`
	Repo             string `json:"repo,omitempty"`
	State            string `json:"state"`
	ReviewDecision   string `json:"review_decision"`
	MergeStateStatus string `json:"merge_state_status"`
	// Queued reports GitHub's own merge queue state. It is true only when
	// GitHub says the pull request is QUEUED — a pull request with auto-merge
	// merely armed has not entered a queue and is not reported as queued.
	Queued    bool   `json:"queued"`
	AutoMerge bool   `json:"auto_merge"`
	Draft     bool   `json:"draft"`
	HeadSHA   string `json:"head_sha"`
	HeadRef   string `json:"head_ref,omitempty"`
	BaseRef   string `json:"base_ref,omitempty"`
	Title     string `json:"title,omitempty"`
}

// GitHub pull request states Relay routes on.
const (
	StateOpen   = "OPEN"
	StateMerged = "MERGED"
	StateClosed = "CLOSED"
)

// ReviewApproved is GitHub's approved review decision.
const ReviewApproved = "APPROVED"

// MergeStateQueued is GitHub's merge queue state.
const MergeStateQueued = "QUEUED"

// Open reports a pull request GitHub still has open.
func (i Inspection) Open() bool { return i.State == StateOpen }

// Merged reports a pull request GitHub merged.
func (i Inspection) Merged() bool { return i.State == StateMerged }

// ClosedUnmerged reports a pull request closed without merging.
func (i Inspection) ClosedUnmerged() bool { return i.State == StateClosed }

// Approved reports a human-approved review decision.
func (i Inspection) Approved() bool { return i.ReviewDecision == ReviewApproved }

// Protected reports a pull request no worker may rewrite: an approved review
// decision is a human's judgement of an exact diff, and a queued pull request
// is being merged by GitHub right now. Either way, a new push would silently
// invalidate the approval or break the queue, so the change belongs on a
// follow-up branch instead.
func (i Inspection) Protected() bool {
	if !i.Open() {
		return false
	}
	return i.Approved() || i.Queued
}

// InspectOptions supplies the seams one inspection needs.
type InspectOptions struct {
	// Runner executes gh. It defaults to the bounded read-only CLI runner.
	Runner Runner
	// Locate resolves the project's recorded pull request. It defaults to
	// LoadTarget, the same resolution the watcher uses.
	Locate func(slug string) (Target, error)
	// tune adjusts the read-only client before it runs. Only this package's
	// tests set it, so a retry-and-backoff test costs no wall clock.
	tune func(*Client)
}

// Inspect reads one project's recorded pull request and returns the exact
// GitHub state a caller routes on.
//
// It reuses the watcher's own read-only gh client and pull request model
// rather than parsing gh output a second way, so one description of a pull
// request cannot drift from the other. It reads and writes no watcher runtime
// state and records no digest, so it is safe to run beside a live watcher and
// never rewrites what that watcher already observed.
func Inspect(ctx context.Context, slug string, options InspectOptions) (Inspection, error) {
	locate := options.Locate
	if locate == nil {
		locate = LoadTarget
	}
	target, err := locate(slug)
	if err != nil {
		return Inspection{}, err
	}
	runner := options.Runner
	if runner == nil {
		runner = NewCLIRunner(0)
	}
	client := NewClient(runner, target.Dir)
	if options.tune != nil {
		options.tune(client)
	}
	pr, err := client.PullRequest(ctx, target.PRNumber)
	if err != nil {
		return Inspection{}, fmt.Errorf("inspect pull request for project %q: %w", slug, err)
	}
	inspection := Inspection{
		Project:          slug,
		Number:           pr.Number,
		Ref:              strconv.Itoa(target.PRNumber),
		URL:              firstNonEmpty(pr.URL, target.PRURL),
		Repo:             pr.Repo,
		State:            pr.State,
		ReviewDecision:   pr.ReviewDecision,
		MergeStateStatus: pr.MergeStateStatus,
		Queued:           pr.MergeStateStatus == MergeStateQueued,
		AutoMerge:        pr.AutoMerge,
		Draft:            pr.Draft,
		HeadSHA:          pr.HeadSHA,
		HeadRef:          pr.HeadRef,
		BaseRef:          pr.BaseRef,
		Title:            pr.Title,
	}
	if err := inspection.validate(target); err != nil {
		return Inspection{}, fmt.Errorf("inspect pull request for project %q: %w", slug, err)
	}
	return inspection, nil
}

// validate refuses an observation nothing can be routed on. A caller that
// cannot tell whether a pull request is open, merged, or closed must write
// nothing at all, so an unusable read is an error rather than a default.
func (i Inspection) validate(target Target) error {
	switch i.State {
	case StateOpen, StateMerged, StateClosed:
	default:
		return fmt.Errorf(
			"GitHub reported pull request state %q for #%d: want OPEN, MERGED, or CLOSED",
			i.State, target.PRNumber,
		)
	}
	if i.Number != target.PRNumber {
		return fmt.Errorf(
			"GitHub returned pull request #%d, but the project records #%d",
			i.Number, target.PRNumber,
		)
	}
	if i.Open() && strings.TrimSpace(i.HeadSHA) == "" {
		return fmt.Errorf("GitHub reported no head commit for open pull request #%d", target.PRNumber)
	}
	return nil
}

// PullRequest reads one pull request's metadata and nothing else. It is the
// smallest read the watcher's GitHub client offers: no checks, comments,
// reviews, or threads are fetched.
func (c *Client) PullRequest(ctx context.Context, number int) (PullRequest, error) {
	repo, defaultBranch, err := c.repo(ctx)
	if err != nil {
		return PullRequest{}, err
	}
	pr, err := c.pullRequest(ctx, number)
	if err != nil {
		return PullRequest{}, err
	}
	pr.Repo = repo
	pr.DefaultBranch = defaultBranch
	return pr, nil
}
