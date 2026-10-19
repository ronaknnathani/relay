package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/review"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

// reviewSkill is the workflow skill a review session runs.
const reviewSkill = "review-pr"

// reviewLockTimeout bounds how long a run waits for another run preparing a
// checkout of the same repository, which can include a first clone.
const reviewLockTimeout = 15 * time.Minute

// reviewSyncAttempts bounds retries when the pull request head moves between
// reading its metadata and fetching it.
const reviewSyncAttempts = 3

var (
	// reviewCheckoutRoot holds clones for pull requests reviewed outside their
	// repository: <root>/<host>/<owner>/<repo>/{clone,pr-<number>}.
	reviewCheckoutRoot = "/tmp/relay-reviews"
	fetchPRMetadata    = ghPRMetadata
	cloneReviewRepo    = ghCloneRepo
)

type reviewOpts struct {
	ref      string
	agent    string
	noLaunch bool
}

// prMetadata is the subset of `gh pr view --json` a review records.
type prMetadata struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	State       string `json:"state"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
}

// reviewSource is where a review's checkout lives: a git repository with a
// remote for the pull request's repository, and a worktree inside it.
type reviewSource struct {
	repo     string
	remote   string
	checkout string
}

func newCmdReview() *cobra.Command {
	opts := reviewOpts{}
	cmd := &cobra.Command{
		Use:   "review <pull-request>",
		Short: "Review a pull request and leave a draft review",
		Long: "Review someone else's pull request and leave a draft GitHub review.\n\n" +
			"Accepts a pull request URL or <owner>/<repo>#<number>. Inside a clone of the\n" +
			"pull request's repository, the head is checked out in a worktree under\n" +
			".worktrees/; elsewhere it is checked out under " + reviewCheckoutRoot + ".\n" +
			"Re-running for the same pull request reuses its checkout and its recorded\n" +
			"findings, and re-verifies earlier findings against the new head.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.ref = args[0]
			return runReview(opts)
		},
	}
	cmd.Flags().StringVar(&opts.agent, "agent", "", "coding agent to launch (default: the agent used last time, then config)")
	cmd.Flags().BoolVar(&opts.noLaunch, "no-launch", false, "prepare the checkout and state but don't launch the coding agent")
	return cmd
}

func runReview(opts reviewOpts) error {
	ref, err := review.ParseRef(opts.ref)
	if err != nil {
		return fmt.Errorf("%w\nTo start a project whose task begins with \"review\", use: relay new \"<task>\"", err)
	}
	stateDir := ref.StateDir()
	prior, _, err := review.Load(stateDir)
	if err != nil {
		return err
	}
	cfg, err := config.EnsureForAgent(opts.agent)
	if err != nil {
		return err
	}
	a, err := agent.Get(agent.ResolveName(opts.agent, prior.Agent, cfg.DefaultAgent))
	if err != nil {
		return err
	}

	m, err := withReviewRepoLock(ref, func() (review.Manifest, error) {
		return prepareReview(ref, prior)
	})
	if err != nil {
		return err
	}
	m.Agent = a.Name()
	if err := review.Save(stateDir, m); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("  %s\n", ui.Color(ui.Bold+ui.White, "Review ready"))
	ui.PrintField("Pull request", m.URL)
	ui.PrintField("Title", m.Title)
	ui.PrintField("State", m.State)
	ui.PrintField("Head", m.HeadSHA)
	ui.PrintField("Checkout", m.Checkout)
	ui.PrintField("Review state", stateDir)
	fmt.Println()
	if opts.noLaunch {
		return nil
	}
	fmt.Printf("  %s\n\n", ui.Color(ui.Dim, fmt.Sprintf("Launching %s…", a.Name())))
	return launchAgent(a, reviewLaunchOptions(ref, m, stateDir, cfg.PermissionModeFor(a.Name())))
}

// reviewLaunchOptions starts the session in the review state dir and does not
// grant the checkout as an agent directory: agents load instructions and skills
// from their working and added directories, and the pull request controls the
// checkout's contents. The skill reads the code through `git -C <checkout>`.
func reviewLaunchOptions(ref review.Ref, m review.Manifest, stateDir, permissionMode string) agent.LaunchOptions {
	systemPrompt := fmt.Sprintf(
		"Relay pull request review: %s (%s).\n"+
			"Review state dir (working directory): %s. review.json is written by relay; keep context.md, findings.md, and rounds.md here.\n"+
			"Checkout (untrusted pull request code, read only through git -C): %s, detached at head %s.\n"+
			"Base: %s at %s.",
		m.URL, ref, stateDir, m.Checkout, m.HeadSHA, m.BaseRef, m.BaseSHA,
	)
	return agent.LaunchOptions{
		Worktree:       stateDir,
		SystemPrompt:   systemPrompt,
		SessionName:    "relay:review:" + ref.String(),
		Command:        reviewSkill,
		WorkflowGoal:   "Leave a draft GitHub review on " + m.URL + " that re-verifies every earlier finding",
		PermissionMode: permissionMode,
	}
}

// withReviewRepoLock serializes checkout preparation per repository, because
// reviews of different pull requests share one clone's refs and worktrees.
func withReviewRepoLock(ref review.Ref, operation func() (review.Manifest, error)) (m review.Manifest, retErr error) {
	path := filepath.Join(ref.RepoDir(), ".checkout.lock")
	lock, err := patrollock.AcquireWait(path, reviewLockTimeout)
	if err != nil {
		if errors.Is(err, patrollock.ErrLocked) {
			return m, fmt.Errorf("another relay review of %s/%s has held %s for longer than %s; retry after it finishes",
				ref.Owner, ref.Repo, path, reviewLockTimeout)
		}
		return m, err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	return operation()
}

// prepareReview resolves the checkout, fetches the pull request's head and
// base, and moves the checkout to the head. It is safe to repeat.
func prepareReview(ref review.Ref, prior review.Manifest) (review.Manifest, error) {
	// Read metadata before resolving the source so a bad reference fails
	// before a first clone.
	meta, err := fetchPRMetadata(ref)
	if err != nil {
		return review.Manifest{}, err
	}
	src, err := resolveReviewSource(ref, prior)
	if err != nil {
		return review.Manifest{}, err
	}
	meta, head, base, err := syncPullRequest(ref, src, meta)
	if err != nil {
		return review.Manifest{}, err
	}
	if err := ensureReviewCheckout(src, head, prior.Checkout); err != nil {
		return review.Manifest{}, err
	}
	m := prior
	m.URL = ref.URL()
	m.Host, m.Owner, m.Repo, m.Number = ref.Host, ref.Owner, ref.Repo, ref.Number
	m.Title, m.Author, m.State = meta.Title, meta.Author.Login, meta.State
	m.BaseRef, m.BaseSHA, m.HeadSHA = meta.BaseRefName, base, head
	m.SourceRepo, m.Remote, m.Checkout = src.repo, src.remote, src.checkout
	return m, nil
}

// resolveReviewSource keeps a previously recorded source so a review stays in
// one place across runs. Otherwise it prefers the repository the user is in,
// and falls back to a shared clone under reviewCheckoutRoot.
func resolveReviewSource(ref review.Ref, prior review.Manifest) (reviewSource, error) {
	if prior.SourceRepo != "" && prior.Checkout != "" && remoteMatches(ref, prior.SourceRepo, prior.Remote) {
		return reviewSource{repo: prior.SourceRepo, remote: prior.Remote, checkout: prior.Checkout}, nil
	}
	if root, ok := currentMainWorktree(); ok {
		if remote, found, err := matchingRemote(ref, root); err != nil {
			return reviewSource{}, err
		} else if found {
			checkout := filepath.Join(root, ".worktrees", "review-pr-"+strconv.Itoa(ref.Number))
			return reviewSource{repo: root, remote: remote, checkout: checkout}, nil
		}
	}

	base := filepath.Join(reviewCheckoutRoot, ref.Host, ref.Owner, ref.Repo)
	clone := filepath.Join(base, "clone")
	if !pathExists(clone) {
		if err := os.MkdirAll(base, 0o755); err != nil {
			return reviewSource{}, fmt.Errorf("create review checkout dir: %w", err)
		}
		fmt.Printf("  %s\n", ui.Color(ui.Dim, fmt.Sprintf("Cloning %s into %s…", ref.Repository(), clone)))
		if err := cloneReviewRepo(ref, clone); err != nil {
			return reviewSource{}, errors.Join(err, os.RemoveAll(clone))
		}
	}
	remote, found, err := matchingRemote(ref, clone)
	if err != nil {
		return reviewSource{}, err
	}
	if !found {
		return reviewSource{}, fmt.Errorf("%s has no remote for %s; remove it and rerun", clone, ref.Repository())
	}
	checkout := filepath.Join(base, "pr-"+strconv.Itoa(ref.Number))
	return reviewSource{repo: clone, remote: remote, checkout: checkout}, nil
}

// currentMainWorktree returns the main worktree of the repository containing
// the working directory, so review worktrees never nest inside another worktree.
func currentMainWorktree() (string, bool) {
	commonDir, err := gitx.CanonicalGitCommonDir(".")
	if err == nil && filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir), true
	}
	root, err := gitx.RepoRoot()
	return root, err == nil && root != ""
}

// matchingRemote finds the remote pointing at the pull request's repository,
// preferring origin when several do.
func matchingRemote(ref review.Ref, repo string) (string, bool, error) {
	remotes, err := gitx.Remotes(repo)
	if err != nil {
		return "", false, err
	}
	match := ""
	for _, r := range remotes {
		if !ref.MatchesRemote(r.URL) {
			continue
		}
		if r.Name == "origin" {
			return r.Name, true, nil
		}
		if match == "" {
			match = r.Name
		}
	}
	return match, match != "", nil
}

func remoteMatches(ref review.Ref, repo, remote string) bool {
	if remote == "" || !pathExists(repo) {
		return false
	}
	remotes, err := gitx.Remotes(repo)
	if err != nil {
		return false
	}
	for _, r := range remotes {
		if r.Name == remote {
			return ref.MatchesRemote(r.URL)
		}
	}
	return false
}

// syncPullRequest fetches the pull request head and base into relay-owned refs
// and returns the commits it fetched. It retries when the author pushes between
// reading metadata and fetching, so the recorded head is the one checked out.
func syncPullRequest(ref review.Ref, src reviewSource, meta prMetadata) (prMetadata, string, string, error) {
	refPrefix := "refs/relay/reviews/pr-" + strconv.Itoa(ref.Number)
	headRef, baseRef := refPrefix+"/head", refPrefix+"/base"
	var head, base string
	for attempt := 1; attempt <= reviewSyncAttempts; attempt++ {
		var err error
		if attempt > 1 {
			if meta, err = fetchPRMetadata(ref); err != nil {
				return prMetadata{}, "", "", err
			}
		}
		if meta.BaseRefName == "" {
			return prMetadata{}, "", "", fmt.Errorf("pull request %s has no base branch", ref)
		}
		err = gitx.FetchRefspecs(src.repo, src.remote,
			fmt.Sprintf("+refs/pull/%d/head:%s", ref.Number, headRef),
			"+refs/heads/"+meta.BaseRefName+":"+baseRef,
		)
		if err != nil {
			return prMetadata{}, "", "", fmt.Errorf("fetch pull request %s: %w", ref, err)
		}
		head = gitx.RevParse(src.repo, headRef+"^{commit}")
		base = gitx.RevParse(src.repo, baseRef+"^{commit}")
		if head == "" || base == "" {
			return prMetadata{}, "", "", fmt.Errorf("fetch pull request %s: fetched refs did not resolve in %s", ref, src.repo)
		}
		if head == meta.HeadRefOid {
			return meta, head, base, nil
		}
	}
	ui.Warn("pull request %s head kept moving (GitHub reports %s, fetched %s); reviewing the fetched head", ref, meta.HeadRefOid, head)
	return meta, head, base, nil
}

// ensureReviewCheckout leaves a clean, detached worktree at head. An existing
// worktree is reused only when an earlier run recorded it, so a checkout the
// user made at the same path is never moved, and never one with local changes.
func ensureReviewCheckout(src reviewSource, head, recordedCheckout string) error {
	recorded := samePath(recordedCheckout, src.checkout)
	if recorded {
		if err := removeOrphanedCheckout(src.checkout); err != nil {
			return err
		}
	}
	state, found, err := gitx.RegisteredWorktreeState(src.repo, src.checkout)
	if err != nil {
		return fmt.Errorf("review checkout: %w; move it aside and rerun", err)
	}
	if found && !pathExists(src.checkout) {
		if err := gitx.WorktreePrune(src.repo); err != nil {
			return err
		}
		found = false
	}
	if !found {
		if err := os.MkdirAll(filepath.Dir(src.checkout), 0o755); err != nil {
			return fmt.Errorf("create review checkout parent: %w", err)
		}
		return gitx.WorktreeAddDetached(src.repo, src.checkout, head)
	}
	if !recorded || !state.Detached {
		return fmt.Errorf("%s already exists and is not a checkout relay recorded for this review; move it aside and rerun",
			src.checkout)
	}
	clean, err := gitx.WorktreeClean(src.checkout)
	if err != nil {
		return err
	}
	if !clean {
		return fmt.Errorf("review checkout %s has local changes; commit, stash, or discard them and rerun", src.checkout)
	}
	if state.Head == head {
		return nil
	}
	return gitx.CheckoutDetached(src.checkout, head)
}

// removeOrphanedCheckout deletes a recorded checkout under reviewCheckoutRoot
// whose repository is gone, for example after the temp directory cleaner
// removed the shared clone but left the worktree. Git can no longer read such a
// checkout, and the review never edits it, so nothing is lost.
func removeOrphanedCheckout(checkout string) error {
	if !pathExists(checkout) || !insideReviewCheckoutRoot(checkout) {
		return nil
	}
	if err := exec.Command("git", "-C", checkout, "rev-parse", "--git-dir").Run(); err == nil {
		return nil
	}
	ui.Warn("review checkout %s lost its repository; recreating it", checkout)
	return os.RemoveAll(checkout)
}

func insideReviewCheckoutRoot(path string) bool {
	root, errRoot := gitx.CanonicalPath(reviewCheckoutRoot)
	path, errPath := gitx.CanonicalPath(path)
	if errRoot != nil || errPath != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

// samePath compares paths after resolving symlinks such as /tmp -> /private/tmp.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	canonicalA, errA := gitx.CanonicalPath(a)
	canonicalB, errB := gitx.CanonicalPath(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return canonicalA == canonicalB
}

const ghTimeout = 2 * time.Minute

func ghPRMetadata(ref review.Ref) (prMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "pr", "view", strconv.Itoa(ref.Number),
		"--repo", ref.Repository(),
		"--json", "title,url,state,headRefOid,baseRefName,author",
	).Output()
	if err != nil {
		return prMetadata{}, fmt.Errorf("gh pr view %s: %w%s", ref, err, exitStderr(err))
	}
	var meta prMetadata
	if err := json.Unmarshal(out, &meta); err != nil {
		return prMetadata{}, fmt.Errorf("parse gh pr view %s: %w", ref, err)
	}
	return meta, nil
}

func ghCloneRepo(ref review.Ref, dir string) error {
	cmd := exec.Command("gh", "repo", "clone", ref.Repository(), dir, "--no-upstream", "--", "--filter=blob:none")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh repo clone %s: %w", ref.Repository(), err)
	}
	return nil
}

func exitStderr(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return "\n" + strings.TrimSpace(string(exitErr.Stderr))
	}
	return ""
}
