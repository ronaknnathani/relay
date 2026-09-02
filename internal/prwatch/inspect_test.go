package prwatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeInspectProject(t *testing.T, slug string, prNumber int) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	worktree := filepath.Join(home, "worktrees", slug)
	for _, dir := range []string{repo, worktree} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	projectDir := filepath.Join(home, ".relay", "projects", "active", slug)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "slug": "` + slug + `",
  "title": "Demo",
  "repo": "` + repo + `",
  "branch": "feature",
  "base_branch": "main",
  "worktree": "` + worktree + `",
  "status": "active",
  "workflow": "deliver-pr",
  "agent": "copilot",
  "created": "2026-01-01T00:00:00Z",
  "updated": "2026-01-01T00:00:00Z",
  "pr": {"number": ` + itoa(prNumber) + `, "url": "https://github.com/acme/widgets/pull/` + itoa(prNumber) + `"}
}`
	if err := os.WriteFile(filepath.Join(projectDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return worktree
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func inspectFixture(state, reviewDecision, mergeState string) string {
	return `{
  "number": 42,
  "url": "https://github.com/acme/widgets/pull/42",
  "title": "Add widgets",
  "state": "` + state + `",
  "isDraft": false,
  "baseRefName": "main",
  "baseRefOid": "base111",
  "headRefName": "feature",
  "headRefOid": "head222",
  "mergeStateStatus": "` + mergeState + `",
  "mergeable": "MERGEABLE",
  "reviewDecision": "` + reviewDecision + `",
  "autoMergeRequest": null,
  "author": {"login": "author-human", "is_bot": false}
}`
}

func TestInspectReadsOnlyTheRequestedPullRequestFields(t *testing.T) {
	writeInspectProject(t, "demo", 42)
	gh := newFakeGH()
	gh.responses["repo view"] = repoViewFixture
	gh.responses["pr view"] = inspectFixture("OPEN", "CHANGES_REQUESTED", "BLOCKED")

	inspection, err := Inspect(context.Background(), "demo", InspectOptions{Runner: gh.runner()})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if inspection.Project != "demo" || inspection.Number != 42 {
		t.Fatalf("inspection identity = %q/%d", inspection.Project, inspection.Number)
	}
	if inspection.State != "OPEN" || inspection.ReviewDecision != "CHANGES_REQUESTED" {
		t.Fatalf("state = %q/%q", inspection.State, inspection.ReviewDecision)
	}
	if inspection.MergeStateStatus != "BLOCKED" || inspection.Queued {
		t.Fatalf("merge state = %q queued=%t", inspection.MergeStateStatus, inspection.Queued)
	}
	if inspection.HeadSHA != "head222" {
		t.Fatalf("head sha = %q", inspection.HeadSHA)
	}
	if inspection.Ref != "42" {
		t.Fatalf("ref = %q, want the recorded pull request number", inspection.Ref)
	}
	if inspection.URL != "https://github.com/acme/widgets/pull/42" {
		t.Fatalf("url = %q", inspection.URL)
	}
	if inspection.Repo != "acme/widgets" {
		t.Fatalf("repo = %q", inspection.Repo)
	}
	for _, call := range gh.calls {
		if call != "repo view" && call != "pr view" {
			t.Fatalf("inspection ran extra gh command %q", call)
		}
	}
}

func TestInspectReportsGitHubMergeQueueState(t *testing.T) {
	writeInspectProject(t, "demo", 42)
	gh := newFakeGH()
	gh.responses["repo view"] = repoViewFixture
	gh.responses["pr view"] = inspectFixture("OPEN", "APPROVED", "QUEUED")

	inspection, err := Inspect(context.Background(), "demo", InspectOptions{Runner: gh.runner()})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !inspection.Queued {
		t.Fatal("a QUEUED merge state was not reported as queued")
	}
	if !inspection.Protected() {
		t.Fatal("a queued pull request is not protected")
	}
}

func TestInspectDoesNotTreatAutoMergeAsAMergeQueue(t *testing.T) {
	writeInspectProject(t, "demo", 42)
	gh := newFakeGH()
	gh.responses["repo view"] = repoViewFixture
	gh.responses["pr view"] = strings.Replace(
		inspectFixture("OPEN", "REVIEW_REQUIRED", "BLOCKED"),
		`"autoMergeRequest": null`, `"autoMergeRequest": {"enabledAt": "2026-01-01T00:00:00Z"}`, 1,
	)

	inspection, err := Inspect(context.Background(), "demo", InspectOptions{Runner: gh.runner()})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if inspection.Queued {
		t.Fatal("auto-merge was mistaken for a merge queue")
	}
	if !inspection.AutoMerge {
		t.Fatal("auto-merge was not reported")
	}
	if inspection.Protected() {
		t.Fatal("an unapproved auto-merge pull request is protected")
	}
}

func TestInspectClassifiesEveryRoutingState(t *testing.T) {
	for name, test := range map[string]struct {
		state          string
		reviewDecision string
		mergeState     string
		open           bool
		merged         bool
		approved       bool
		queued         bool
		protected      bool
	}{
		"open unapproved": {
			state: "OPEN", reviewDecision: "REVIEW_REQUIRED", mergeState: "BLOCKED", open: true,
		},
		"open changes requested": {
			state: "OPEN", reviewDecision: "CHANGES_REQUESTED", mergeState: "BLOCKED", open: true,
		},
		"approved": {
			state: "OPEN", reviewDecision: "APPROVED", mergeState: "CLEAN",
			open: true, approved: true, protected: true,
		},
		"queued": {
			state: "OPEN", reviewDecision: "APPROVED", mergeState: "QUEUED",
			open: true, approved: true, queued: true, protected: true,
		},
		"queued without approval": {
			state: "OPEN", reviewDecision: "REVIEW_REQUIRED", mergeState: "QUEUED",
			open: true, queued: true, protected: true,
		},
		"merged": {
			state: "MERGED", reviewDecision: "APPROVED", mergeState: "UNKNOWN",
			merged: true, approved: true,
		},
		"closed unmerged": {
			state: "CLOSED", reviewDecision: "REVIEW_REQUIRED", mergeState: "UNKNOWN",
		},
	} {
		t.Run(name, func(t *testing.T) {
			writeInspectProject(t, "demo", 42)
			gh := newFakeGH()
			gh.responses["repo view"] = repoViewFixture
			gh.responses["pr view"] = inspectFixture(test.state, test.reviewDecision, test.mergeState)

			inspection, err := Inspect(context.Background(), "demo", InspectOptions{Runner: gh.runner()})
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if inspection.Open() != test.open {
				t.Errorf("Open = %t, want %t", inspection.Open(), test.open)
			}
			if inspection.Merged() != test.merged {
				t.Errorf("Merged = %t, want %t", inspection.Merged(), test.merged)
			}
			if inspection.Approved() != test.approved {
				t.Errorf("Approved = %t, want %t", inspection.Approved(), test.approved)
			}
			if inspection.Queued != test.queued {
				t.Errorf("Queued = %t, want %t", inspection.Queued, test.queued)
			}
			if inspection.Protected() != test.protected {
				t.Errorf("Protected = %t, want %t", inspection.Protected(), test.protected)
			}
			if inspection.ClosedUnmerged() != (test.state == "CLOSED") {
				t.Errorf("ClosedUnmerged = %t, want %t", inspection.ClosedUnmerged(), test.state == "CLOSED")
			}
		})
	}
}

func TestInspectFailsClosedWhenGitHubIsUnavailable(t *testing.T) {
	writeInspectProject(t, "demo", 42)
	gh := newFakeGH()
	gh.responses["repo view"] = repoViewFixture
	gh.failures["pr view"] = errors.New("gh: network is unreachable")

	if _, err := Inspect(context.Background(), "demo", InspectOptions{
		Runner: gh.runner(),
		tune:   func(client *Client) { client.backoff = 0; client.sleep = func(time.Duration) {} },
	}); err == nil {
		t.Fatal("Inspect succeeded with an unreachable GitHub")
	}
	if gh.attempts["pr view"] < 2 {
		t.Fatalf("Inspect attempted the failing read %d time(s); want the client's own retries",
			gh.attempts["pr view"])
	}
}

func TestInspectRejectsAnUnknownPullRequestState(t *testing.T) {
	writeInspectProject(t, "demo", 42)
	for name, fixture := range map[string]string{
		"empty state":   inspectFixture("", "APPROVED", "CLEAN"),
		"unknown state": inspectFixture("SOMETHING_ELSE", "APPROVED", "CLEAN"),
		"null response": `null`,
	} {
		t.Run(name, func(t *testing.T) {
			gh := newFakeGH()
			gh.responses["repo view"] = repoViewFixture
			gh.responses["pr view"] = fixture
			_, err := Inspect(context.Background(), "demo", InspectOptions{Runner: gh.runner()})
			if err == nil {
				t.Fatal("Inspect accepted an unusable pull request state")
			}
		})
	}
}

func TestInspectRefusesAProjectWithNoRecordedPullRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectDir := filepath.Join(home, ".relay", "projects", "active", "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"slug":"demo","title":"Demo","repo":"` + repo + `","status":"active","workflow":"deliver-pr","agent":"copilot","created":"2026-01-01T00:00:00Z","updated":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(projectDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	gh := newFakeGH()
	if _, err := Inspect(context.Background(), "demo", InspectOptions{Runner: gh.runner()}); err == nil {
		t.Fatal("Inspect succeeded for a project with no recorded pull request")
	}
	if len(gh.calls) != 0 {
		t.Fatalf("Inspect called gh %v before resolving a pull request", gh.calls)
	}
}

func TestInspectWritesNoWatcherRuntimeState(t *testing.T) {
	writeInspectProject(t, "demo", 42)
	gh := newFakeGH()
	gh.responses["repo view"] = repoViewFixture
	gh.responses["pr view"] = inspectFixture("OPEN", "APPROVED", "CLEAN")

	if _, err := Inspect(context.Background(), "demo", InspectOptions{Runner: gh.runner()}); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	runtime := RuntimeDir("demo")
	if _, err := os.Stat(runtime); !errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(runtime)
		t.Fatalf("Inspect created watcher runtime state at %s (%v, entries %v)", runtime, err, entriesNames(entries, readErr))
	}
}

func entriesNames(entries []os.DirEntry, err error) []string {
	if err != nil {
		return []string{err.Error()}
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestInspectLeavesAnExistingWatcherRecordUntouched(t *testing.T) {
	writeInspectProject(t, "demo", 42)
	if _, err := UpdateState("demo", func(state State) (State, error) {
		state.Project = "demo"
		state.Status = StatusRunning
		state.PRNumber = 42
		state.CurrentFingerprint = "before"
		return state, nil
	}); err != nil {
		t.Fatalf("seed watcher state: %v", err)
	}
	before, err := os.ReadFile(StatePath("demo"))
	if err != nil {
		t.Fatalf("read seeded state: %v", err)
	}
	gh := newFakeGH()
	gh.responses["repo view"] = repoViewFixture
	gh.responses["pr view"] = inspectFixture("MERGED", "APPROVED", "UNKNOWN")

	if _, err := Inspect(context.Background(), "demo", InspectOptions{Runner: gh.runner()}); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	after, err := os.ReadFile(StatePath("demo"))
	if err != nil {
		t.Fatalf("read state after inspect: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("Inspect rewrote watcher runtime state:\nbefore %s\nafter  %s", before, after)
	}
}
