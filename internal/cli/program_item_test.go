package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/program"
)

func createCLIProgram(t *testing.T, slug string) program.Program {
	t.Helper()
	repo := newTestRepo(t)
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	p, err := program.New(slug, "Program "+slug, repoRoot, "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProgramItemAddUpdateCycleAndList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := createCLIProgram(t, "governance")

	out, err := runProgramCommand(t, "item", "add", p.Slug, "First change", "--priority", "P1", "--notes", "first note")
	if err != nil {
		t.Fatalf("item add first: %v", err)
	}
	if strings.TrimSpace(out) != "w1" {
		t.Fatalf("first item output = %q", out)
	}
	out, err = runProgramCommand(t, "item", "add", p.Slug, "Second change", "--depends-on", "w1")
	if err != nil {
		t.Fatalf("item add second: %v", err)
	}
	if strings.TrimSpace(out) != "w2" {
		t.Fatalf("second item output = %q", out)
	}

	before, err := os.ReadFile(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCommand(t, "item", "update", p.Slug, "w1", "--add-dep", "w2"); err == nil ||
		!strings.Contains(err.Error(), "dependency cycle") {
		t.Fatalf("cycle update error = %v", err)
	}
	after, err := os.ReadFile(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed cycle update modified manifest")
	}

	if _, err := runProgramCommand(t, "item", "update", p.Slug, "w2",
		"--title", "Updated second", "--priority", "P0", "--remove-dep", "w1", "--note", "replanned"); err != nil {
		t.Fatalf("item update: %v", err)
	}
	if _, err := runProgramCommand(t, "item", "update", p.Slug, "w2"); err == nil ||
		!strings.Contains(err.Error(), "no changes") {
		t.Fatalf("no-op update error = %v", err)
	}

	out, err = runProgramCommand(t, "item", "list", p.Slug, "--json", "--status", "pending")
	if err != nil {
		t.Fatalf("item list: %v", err)
	}
	var items []program.WorkItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("decode items: %v\n%s", err, out)
	}
	if len(items) != 2 || items[1].Title != "Updated second" || items[1].Priority != program.PriorityP0 ||
		len(items[1].Dependencies) != 0 || len(items[1].Notes) != 1 || items[1].Notes[0] != "replanned" {
		t.Fatalf("items = %+v", items)
	}
}

func TestProgramItemLifecycleAndLink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := createCLIProgram(t, "governance")
	if _, err := runProgramCommand(t, "item", "add", p.Slug, "First change"); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{
		{"item", "link", p.Slug, "w1", "--project", "child-project"},
		{"item", "block", p.Slug, "w1", "--reason", "Waiting for input"},
		{"item", "unblock", p.Slug, "w1"},
		{"item", "cancel", p.Slug, "w1", "--reason", "Superseded"},
	} {
		if _, err := runProgramCommand(t, command...); err != nil {
			t.Fatalf("%s: %v", strings.Join(command, " "), err)
		}
	}
	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	item, ok := loaded.Item("w1")
	if !ok || item.ProjectSlug != "child-project" || item.Status != program.ItemCancelled ||
		len(item.Notes) != 1 || item.Notes[0] != "Superseded" {
		t.Fatalf("item = %+v", item)
	}
	progress, err := os.ReadFile(program.ProgressPath(program.ProgramDir(program.ActiveDir(), p.Slug)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Linked item w1", "Blocked item w1", "Unblocked item w1", "Canceled item w1"} {
		if !strings.Contains(string(progress), want) {
			t.Errorf("progress %q missing %q", progress, want)
		}
	}
}
