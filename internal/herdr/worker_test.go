package herdr

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestFindLiveWorkerMatchesExactTitleAndRepositoryOrWorktreeIdentity(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	worktree := filepath.Join(repo, ".worktrees", "child")
	pluginDir := filepath.Join(worktree, "plugins", "example")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked-worktree")
	if err := os.Symlink(worktree, link); err != nil {
		t.Fatal(err)
	}
	stableOwner := Agent{
		Status:        StatusIdle,
		PaneID:        "stable",
		TerminalTitle: "relay:child - GitHub Copilot",
		CWD:           repo,
		ForegroundCWD: pluginDir,
	}
	got, ok := FindLiveWorker([]Agent{stableOwner}, "child", repo, worktree)
	if !ok || got.PaneID != "stable" {
		t.Fatalf("stable repo owner = %#v, %t", got, ok)
	}

	foregroundOwner := Agent{
		Status:        StatusWorking,
		PaneID:        "foreground",
		TerminalTitle: "relay:child",
		CWD:           root,
		ForegroundCWD: filepath.Join(link, "plugins", "example"),
	}
	got, ok = FindLiveWorker([]Agent{foregroundOwner}, "child", repo, worktree)
	if !ok || got.PaneID != "foreground" {
		t.Fatalf("foreground worktree owner = %#v, %t", got, ok)
	}

	tabOwner := Agent{
		Status:        StatusWorking,
		PaneID:        "tab-cwd",
		TerminalTitle: "relay:child - GitHub Copilot",
		CWD:           worktree,
		ForegroundCWD: filepath.Join(root, "copilot-plugin"),
	}
	got, ok = FindLiveWorker([]Agent{tabOwner}, "child", repo, worktree)
	if !ok || got.PaneID != "tab-cwd" {
		t.Fatalf("tab worktree owner = %#v, %t", got, ok)
	}

	untitledWorktreeOwner := Agent{
		Status:        StatusWorking,
		PaneID:        "codex-tab",
		TerminalTitle: "Codex",
		CWD:           worktree,
		ForegroundCWD: filepath.Join(root, "codex-plugin"),
	}
	got, ok = FindLiveWorker([]Agent{untitledWorktreeOwner}, "child", repo, worktree)
	if !ok || got.PaneID != "codex-tab" {
		t.Fatalf("untitled worktree owner = %#v, %t", got, ok)
	}

	nonOwners := []Agent{
		{
			PaneID:        "wrong-title",
			TerminalTitle: "relay:child-other - GitHub Copilot",
			CWD:           repo,
			ForegroundCWD: filepath.Join(root, "other-worktree"),
		},
		{
			PaneID:        "wrong-paths",
			TerminalTitle: "relay:child - GitHub Copilot",
			CWD:           filepath.Join(root, "other-repo"),
			ForegroundCWD: filepath.Join(root, "other-worktree"),
		},
	}
	if got, ok := FindLiveWorker(nonOwners, "child", repo, worktree); ok {
		t.Fatalf("FindLiveWorker matched non-owner %#v", got)
	}
}

func TestFindLiveTLMatchesOnlyExactProgramIdentity(t *testing.T) {
	agents := []Agent{
		{PaneID: "alpha", TerminalTitle: "relay:program:alpha - GitHub Copilot", CWD: "/same/repo"},
		{PaneID: "beta", TerminalTitle: "relay:program:beta", CWD: "/same/repo"},
		{PaneID: "patrol", TerminalTitle: "relay-patrol:alpha", CWD: "/same/repo"},
		{PaneID: "near", TerminalTitle: "relay:program:alpha-other", CWD: "/same/repo"},
	}

	got, err := FindLiveTL(agents, "alpha")
	if err != nil || got.PaneID != "alpha" {
		t.Fatalf("FindLiveTL(alpha) = %#v, %v", got, err)
	}
	got, err = FindLiveTL(agents, "beta")
	if err != nil || got.PaneID != "beta" {
		t.Fatalf("FindLiveTL(beta) = %#v, %v", got, err)
	}
	if got, err := FindLiveTL(agents, "missing"); !errors.Is(err, ErrNoLiveTL) {
		t.Fatalf("FindLiveTL(missing) = %#v, %v, want ErrNoLiveTL", got, err)
	}
	if got, err := FindLiveTL(nil, "alpha"); !errors.Is(err, ErrNoLiveTL) {
		t.Fatalf("FindLiveTL(no agents) = %#v, %v, want ErrNoLiveTL", got, err)
	}
	if got, err := FindLiveTL(agents, ""); !errors.Is(err, ErrNoLiveTL) {
		t.Fatalf("FindLiveTL(no slug) = %#v, %v, want ErrNoLiveTL", got, err)
	}
}

// Two live panes claiming one program is an ambiguous ownership condition, not
// a first-match win: acting on either one could collide with the other tech lead.
func TestFindLiveTLRejectsDuplicateOwners(t *testing.T) {
	agents := []Agent{
		{PaneID: "p1", TerminalTitle: "relay:program:alpha - GitHub Copilot", Status: StatusIdle},
		{PaneID: "near", TerminalTitle: "relay:program:alpha-other", Status: StatusIdle},
		{PaneID: "p2", TerminalTitle: "relay:program:alpha", Status: StatusWorking},
	}

	got, err := FindLiveTL(agents, "alpha")
	if err == nil {
		t.Fatalf("FindLiveTL matched %#v with two live owners", got)
	}
	var duplicate *DuplicateTLError
	if !errors.As(err, &duplicate) {
		t.Fatalf("FindLiveTL error = %v, want *DuplicateTLError", err)
	}
	if !reflect.DeepEqual(duplicate.PaneIDs, []string{"p1", "p2"}) {
		t.Errorf("duplicate panes = %v, want [p1 p2]", duplicate.PaneIDs)
	}
	if duplicate.ProgramSlug != "alpha" {
		t.Errorf("duplicate program = %q, want alpha", duplicate.ProgramSlug)
	}
	for _, want := range []string{`program "alpha"`, "2 live tech lead sessions", "p1", "p2", "herdr agent focus"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("duplicate error %q is missing %q", err, want)
		}
	}
}

func TestWorkerNameIsStableValidAndCollisionResistantWhenTruncated(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	if got := WorkerName("governance", "w1"); got != "governance-w1" {
		t.Fatalf("WorkerName short = %q, want governance-w1", got)
	}

	first := WorkerName("a-very-long-program-name-with-a-shared-prefix-alpha", "w123")
	again := WorkerName("a-very-long-program-name-with-a-shared-prefix-alpha", "w123")
	second := WorkerName("a-very-long-program-name-with-a-shared-prefix-beta", "w123")
	for _, name := range []string{first, again, second, WorkerName("123 strange/slug", "W 9")} {
		if !valid.MatchString(name) {
			t.Errorf("WorkerName = %q, want %s", name, valid)
		}
		if len(name) > 32 {
			t.Errorf("WorkerName length = %d, want <= 32", len(name))
		}
	}
	if first != again {
		t.Fatalf("WorkerName is unstable: %q != %q", first, again)
	}
	if first == second {
		t.Fatalf("truncated worker names collided: %q", first)
	}
}
