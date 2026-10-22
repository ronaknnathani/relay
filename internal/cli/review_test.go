package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/review"
)

const reviewTestRemote = "https://github.com/acme/widget.git"

// reviewFixture is a bare repository standing in for github.com/acme/widget,
// with pull request 7 published at refs/pull/7/head.
type reviewFixture struct {
	t      *testing.T
	bare   string
	author string
	clones int
}

func newReviewFixture(t *testing.T) *reviewFixture {
	t.Helper()
	f := &reviewFixture{t: t, bare: filepath.Join(t.TempDir(), "widget.git"), author: newTestRepo(t)}
	reviewGit(t, "", "init", "-q", "--bare", "-b", "main", f.bare)
	reviewGit(t, f.author, "push", "-q", f.bare, "main")
	reviewGit(t, f.author, "checkout", "-q", "-b", "feature")
	f.pushPRCommit("first")

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := config.Save(config.Config{BranchPrefix: "test/", DefaultAgent: "copilot"}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	previousRoot, previousMeta, previousClone := reviewCheckoutRoot, fetchPRMetadata, cloneReviewRepo
	reviewCheckoutRoot = filepath.Join(t.TempDir(), "relay-reviews")
	fetchPRMetadata = func(ref review.Ref) (prMetadata, error) {
		meta := prMetadata{Title: "Add widget", State: "OPEN", BaseRefName: "main"}
		meta.HeadRefOid = f.bareRevParse("refs/pull/7/head")
		meta.Author.Login = "octo"
		return meta, nil
	}
	cloneReviewRepo = func(_ review.Ref, dir string) error {
		f.clones++
		reviewGit(t, "", "clone", "-q", f.bare, dir)
		f.pointAtGitHub(dir)
		return nil
	}
	t.Cleanup(func() {
		reviewCheckoutRoot, fetchPRMetadata, cloneReviewRepo = previousRoot, previousMeta, previousClone
	})
	return f
}

// pushPRCommit adds a commit to the pull request and returns its SHA.
func (f *reviewFixture) pushPRCommit(name string) string {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.author, name), []byte(name+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	reviewGit(f.t, f.author, "add", name)
	reviewGit(f.t, f.author, "commit", "-q", "-m", name)
	reviewGit(f.t, f.author, "push", "-q", "-f", f.bare, "HEAD:refs/pull/7/head")
	return gitx.RevParse(f.author, "HEAD")
}

// pointAtGitHub makes repo's origin look like the GitHub remote while git
// actually fetches from the bare fixture.
func (f *reviewFixture) pointAtGitHub(repo string) {
	f.t.Helper()
	reviewGit(f.t, repo, "remote", "set-url", "origin", reviewTestRemote)
	reviewGit(f.t, repo, "config", "url."+f.bare+".insteadOf", reviewTestRemote)
}

// userClone is the user's own clone of acme/widget.
func (f *reviewFixture) userClone() string {
	f.t.Helper()
	dir := filepath.Join(f.t.TempDir(), "widget")
	reviewGit(f.t, "", "clone", "-q", f.bare, dir)
	f.pointAtGitHub(dir)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		f.t.Fatal(err)
	}
	return resolved
}

// bareRevParse resolves ref in the bare fixture. It names the git dir
// explicitly because safe.bareRepository=explicit rejects `git -C <bare>`.
func (f *reviewFixture) bareRevParse(ref string) string {
	f.t.Helper()
	out, err := exec.Command("git", "--git-dir", f.bare, "rev-parse", ref).Output()
	if err != nil {
		f.t.Fatalf("rev-parse %s in fixture: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

func reviewGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func loadReview(t *testing.T) review.Manifest {
	t.Helper()
	ref, _ := review.ParseRef("acme/widget#7")
	m, found, err := review.Load(ref.StateDir())
	if err != nil || !found {
		t.Fatalf("load review: found %v, err %v", found, err)
	}
	return m
}

func assertDetachedAt(t *testing.T, repo, checkout, want string) {
	t.Helper()
	state, found, err := gitx.RegisteredWorktreeState(repo, checkout)
	if err != nil || !found {
		t.Fatalf("worktree %s: found %v, err %v", checkout, found, err)
	}
	if !state.Detached || state.Head != want {
		t.Fatalf("worktree %s = %+v, want detached at %s", checkout, state, want)
	}
}

func TestRunReviewInRepoUsesWorktreeAndFollowsNewHead(t *testing.T) {
	f := newReviewFixture(t)
	repo := f.userClone()
	t.Chdir(repo)

	if err := runReview(reviewOpts{ref: "https://github.com/acme/widget/pull/7/files", noLaunch: true}); err != nil {
		t.Fatalf("first runReview: %v", err)
	}
	first := loadReview(t)
	wantCheckout := filepath.Join(repo, ".worktrees", "review-pr-7")
	if first.Checkout != wantCheckout || first.SourceRepo != repo || first.Remote != "origin" {
		t.Fatalf("review = %+v, want checkout %s in %s via origin", first, wantCheckout, repo)
	}
	if first.Title != "Add widget" || first.Author != "octo" || first.BaseRef != "main" ||
		first.BaseSHA != f.bareRevParse("main") {
		t.Errorf("review metadata = %+v", first)
	}
	assertDetachedAt(t, repo, wantCheckout, first.HeadSHA)
	if f.clones != 0 {
		t.Errorf("cloned %d times inside the repository, want 0", f.clones)
	}

	second := f.pushPRCommit("second")
	if err := runReview(reviewOpts{ref: "acme/widget#7", noLaunch: true}); err != nil {
		t.Fatalf("second runReview: %v", err)
	}
	updated := loadReview(t)
	if updated.HeadSHA != second || updated.Created != first.Created {
		t.Errorf("after push review = %+v, want head %s and created %s", updated, second, first.Created)
	}
	assertDetachedAt(t, repo, wantCheckout, second)
}

func TestRunReviewOutsideRepoClonesOnceAndRecreatesMissingCheckout(t *testing.T) {
	f := newReviewFixture(t)
	t.Chdir(t.TempDir())

	for i := 0; i < 2; i++ {
		if err := runReview(reviewOpts{ref: "acme/widget#7", noLaunch: true}); err != nil {
			t.Fatalf("runReview %d: %v", i, err)
		}
	}
	m := loadReview(t)
	base := filepath.Join(reviewCheckoutRoot, "github.com", "acme", "widget")
	if f.clones != 1 || m.SourceRepo != filepath.Join(base, "clone") || m.Checkout != filepath.Join(base, "pr-7") {
		t.Fatalf("clones = %d, review = %+v; want one clone with checkout under %s", f.clones, m, base)
	}

	if err := os.RemoveAll(m.Checkout); err != nil {
		t.Fatal(err)
	}
	if err := runReview(reviewOpts{ref: "acme/widget#7", noLaunch: true}); err != nil {
		t.Fatalf("runReview after checkout removal: %v", err)
	}
	assertDetachedAt(t, m.SourceRepo, m.Checkout, m.HeadSHA)
	if f.clones != 1 {
		t.Errorf("clones = %d after recreating the checkout, want 1", f.clones)
	}
}

func TestRunReviewRecoversCheckoutWhoseCloneWasRemoved(t *testing.T) {
	f := newReviewFixture(t)
	t.Chdir(t.TempDir())
	if err := runReview(reviewOpts{ref: "acme/widget#7", noLaunch: true}); err != nil {
		t.Fatalf("runReview: %v", err)
	}
	m := loadReview(t)
	if err := os.RemoveAll(m.SourceRepo); err != nil {
		t.Fatal(err)
	}

	if err := runReview(reviewOpts{ref: "acme/widget#7", noLaunch: true}); err != nil {
		t.Fatalf("runReview after the clone was removed: %v", err)
	}
	if f.clones != 2 {
		t.Errorf("clones = %d, want a fresh clone after removal", f.clones)
	}
	assertDetachedAt(t, m.SourceRepo, m.Checkout, m.HeadSHA)
}

func TestRunReviewRefusesToMoveDirtyCheckout(t *testing.T) {
	f := newReviewFixture(t)
	t.Chdir(f.userClone())
	if err := runReview(reviewOpts{ref: "acme/widget#7", noLaunch: true}); err != nil {
		t.Fatalf("runReview: %v", err)
	}
	m := loadReview(t)
	if err := os.WriteFile(filepath.Join(m.Checkout, "scratch"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runReview(reviewOpts{ref: "acme/widget#7", noLaunch: true})
	if err == nil || !strings.Contains(err.Error(), "has local changes") {
		t.Fatalf("runReview with dirty checkout at the same head = %v, want local changes error", err)
	}

	f.pushPRCommit("second")
	err = runReview(reviewOpts{ref: "acme/widget#7", noLaunch: true})
	if err == nil || !strings.Contains(err.Error(), "has local changes") {
		t.Fatalf("runReview with dirty checkout after a push = %v, want local changes error", err)
	}
	assertDetachedAt(t, m.SourceRepo, m.Checkout, m.HeadSHA)
}

func TestRunReviewRefusesUnrecordedWorktreeAtReviewPath(t *testing.T) {
	for name, args := range map[string][]string{
		"branch":   {"-b", "mine"},
		"detached": {"--detach"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newReviewFixture(t)
			repo := f.userClone()
			t.Chdir(repo)
			checkout := filepath.Join(repo, ".worktrees", "review-pr-7")
			reviewGit(t, repo, append([]string{"worktree", "add", "-q"}, append(args, checkout)...)...)
			before := gitx.RevParse(checkout, "HEAD")

			err := runReview(reviewOpts{ref: "acme/widget#7", noLaunch: true})
			if err == nil || !strings.Contains(err.Error(), "not a checkout relay recorded") {
				t.Fatalf("runReview over a %s worktree = %v, want refusal", name, err)
			}
			if after := gitx.RevParse(checkout, "HEAD"); after != before {
				t.Errorf("worktree moved from %s to %s", before, after)
			}
		})
	}
}

func TestRunReviewLaunchesReviewSkill(t *testing.T) {
	f := newReviewFixture(t)
	t.Chdir(f.userClone())
	var got agent.LaunchOptions
	previousLaunch := launchAgent
	launchAgent = func(_ agent.Agent, o agent.LaunchOptions) error {
		got = o
		return nil
	}
	t.Cleanup(func() { launchAgent = previousLaunch })

	if err := runReview(reviewOpts{ref: "acme/widget#7"}); err != nil {
		t.Fatalf("runReview: %v", err)
	}
	m := loadReview(t)
	ref, _ := review.ParseRef("acme/widget#7")
	if got.Command != "review-pr" || got.CommandArgs != "" || got.Worktree != ref.StateDir() ||
		got.ProjectDir != "" || got.SessionName != "relay:review:acme/widget#7" {
		t.Errorf("launch options = %+v", got)
	}
	for _, want := range []string{m.URL, m.Checkout, m.HeadSHA, m.BaseSHA} {
		if !strings.Contains(got.SystemPrompt, want) {
			t.Errorf("system prompt %q is missing %q", got.SystemPrompt, want)
		}
	}
	if m.Agent != "copilot" {
		t.Errorf("recorded agent = %q, want copilot", m.Agent)
	}
}

func TestRunReviewRejectsNonPullRequest(t *testing.T) {
	err := runReview(reviewOpts{ref: "the auth code"})
	if err == nil || !strings.Contains(err.Error(), `relay new "<task>"`) {
		t.Fatalf("runReview(task) = %v, want a hint to use relay new", err)
	}
}
