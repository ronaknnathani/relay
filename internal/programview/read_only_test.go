package programview

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

func TestBuildDoesNotCreateMissingMailboxDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := program.New("readonly", "Read only", repo, "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	p.Items = append(p.Items, program.WorkItem{
		ID: "w1", Kind: program.ItemKindChange, Title: "child", Priority: program.PriorityP1,
		Status: program.ItemDispatched, Dependencies: []string{}, ContractRefs: []string{},
		Repo: repo, ProjectSlug: "child", Notes: []string{},
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, DispatchedAt: p.UpdatedAt,
	})
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	childDir := filepath.Join(project.ActiveDir(), "child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(repo, ".worktrees", "child")
	if err := project.Save(project.ManifestPath(project.ActiveDir(), "child"), project.Manifest{
		Slug: "child", Repo: repo, Branch: "feature", Worktree: &worktree,
		PhasesCompleted: []string{}, PhasesRemaining: []string{},
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := Build(p.Slug, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(childDir, "mail")); !os.IsNotExist(err) {
		t.Fatalf("Build created mailbox directory or returned unexpected stat error: %v", err)
	}
	item := findSnapshotItem(t, snapshot.Items, "w1")
	if !reflect.DeepEqual(item.Mailbox, MailboxDTO{InboxIDs: []string{}, OutboxIDs: []string{}}) ||
		snapshot.SourceHealth.Mailbox.Status != "ok" ||
		len(snapshot.SourceHealth.Mailbox.Warnings) != 0 {
		t.Fatalf("missing mailbox health = item %+v, source %+v", item.Mailbox, snapshot.SourceHealth.Mailbox)
	}
	for _, warning := range append(snapshot.Warnings, item.Warnings...) {
		if strings.Contains(warning, "mailbox") {
			t.Fatalf("missing mailbox warning = %q", warning)
		}
	}
}
