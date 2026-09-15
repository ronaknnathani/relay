package programui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/project"
)

func TestReferenceProgramFixture(t *testing.T) {
	fixture := newReferenceProgramFixture(t)

	if len(fixture.program.Items) != 100 {
		t.Fatalf("items = %d, want 100", len(fixture.program.Items))
	}
	edges := 0
	for _, item := range fixture.program.Items {
		edges += len(item.Dependencies)
	}
	if edges != 200 {
		t.Fatalf("dependency edges = %d, want 200", edges)
	}
	if len(fixture.program.Contracts) != 20 {
		t.Fatalf("contracts = %d, want 20", len(fixture.program.Contracts))
	}
	if len(fixture.program.Decisions) != 10 {
		t.Fatalf("decisions = %d, want 10", len(fixture.program.Decisions))
	}

	selectedDir := filepath.Join(fixture.projectsDir, fixture.program.Items[0].ProjectSlug)
	for name, size := range map[string]int64{
		"task.md":       0,
		"assignment.md": 128 * 1024,
		"plan.md":       128*1024 + 1,
	} {
		info, err := os.Stat(filepath.Join(selectedDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != size {
			t.Fatalf("%s size = %d, want %d", name, info.Size(), size)
		}
	}
}

type referenceProgramFixture struct {
	home        string
	projectsDir string
	program     program.Program
}

func newReferenceProgramFixture(t *testing.T) referenceProgramFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	at := "2026-09-14T20:00:00Z"
	p := program.Program{
		Revision:            1,
		Slug:                "reference-program",
		Title:               "Reference Program",
		Repo:                repo,
		State:               program.StateActive,
		Agent:               "copilot",
		MaxOpenPRs:          20,
		CreatedAt:           at,
		UpdatedAt:           at,
		ApprovalRequestedAt: at,
		ApprovedAt:          at,
		ApprovedBy:          "test",
		Items:               make([]program.WorkItem, 0, 100),
		Contracts:           make([]program.Contract, 0, 20),
		Decisions:           make([]program.Decision, 0, 10),
	}
	for index := 1; index <= 20; index++ {
		name := fmt.Sprintf("contract-%02d", index)
		ref := name + "@v1"
		relative := filepath.ToSlash(filepath.Join("contracts", name, "v1.md"))
		p.Contracts = append(p.Contracts, program.Contract{
			Name: name, Version: 1, Ref: ref, Path: relative,
			SHA256: strings.Repeat(fmt.Sprintf("%x", index%16), 64),
			Status: program.ContractApproved, PublishedAt: at,
			ApprovedAt: at, ApprovedBy: "test",
		})
	}
	for index := 1; index <= 10; index++ {
		p.Decisions = append(p.Decisions, program.Decision{
			ID: fmt.Sprintf("d%d", index), Kind: program.DecisionQuestion,
			RaisedBy: program.RaisedByTL, ItemID: fmt.Sprintf("w%d", index),
			Question: fmt.Sprintf("Decision %d?", index),
			Options:  []string{"yes", "no"}, CreatedAt: at,
		})
	}
	for index := 1; index <= 100; index++ {
		dependencies := referenceDependencies(index)
		item := program.WorkItem{
			ID: fmt.Sprintf("w%d", index), Kind: program.ItemKindChange,
			Title:    fmt.Sprintf("Reference task %03d", index),
			Priority: program.PriorityP1, Status: program.ItemDispatched,
			Dependencies: dependencies,
			ContractRefs: []string{p.Contracts[(index-1)%len(p.Contracts)].Ref},
			Repo:         repo, ProjectSlug: fmt.Sprintf("reference-task-%03d", index),
			Notes:     []string{fmt.Sprintf("Deterministic note %03d", index)},
			CreatedAt: at, UpdatedAt: at, DispatchedAt: at,
		}
		p.Items = append(p.Items, item)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}

	programDir := program.ProgramDir(program.ActiveDir(), p.Slug)
	writeReferenceFile(t, filepath.Join(programDir, "goal.md"), []byte("Load the complete reference program quickly.\n"))
	writeReferenceFile(t, filepath.Join(programDir, "decisions.md"), []byte("Reference decisions.\n"))
	writeReferenceFile(t, filepath.Join(programDir, "progress.md"), []byte("Reference progress.\n"))
	for _, contract := range p.Contracts {
		writeReferenceFile(t, filepath.Join(programDir, filepath.FromSlash(contract.Path)), []byte(contract.Ref+"\n"))
	}

	for index, item := range p.Items {
		childDir := filepath.Join(project.ActiveDir(), item.ProjectSlug)
		if err := os.MkdirAll(childDir, 0o755); err != nil {
			t.Fatal(err)
		}
		worktree := filepath.Join(repo, ".worktrees", item.ProjectSlug)
		manifest := project.Manifest{
			Slug: item.ProjectSlug, Title: item.Title, Repo: repo,
			Branch: "feature/" + item.ProjectSlug, BaseBranch: "main",
			Worktree: &worktree, Status: "active", Workflow: "deliver-pr",
			Program: p.Slug, ProgramItem: item.ID, Phase: "implement",
			Created: at, Updated: at, PhasesCompleted: []string{},
			PhasesRemaining: []string{},
		}
		if err := project.Save(project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest); err != nil {
			t.Fatal(err)
		}
		for _, name := range referenceArtifactNames {
			content := []byte(fmt.Sprintf("%s for %s\n", name, item.ID))
			if index == 0 {
				switch name {
				case "task.md":
					content = []byte{}
				case "assignment.md":
					content = []byte(strings.Repeat("a", 128*1024))
				case "plan.md":
					content = []byte(strings.Repeat("p", 128*1024+1))
				}
			}
			writeReferenceFile(t, filepath.Join(childDir, name), content)
		}
	}

	return referenceProgramFixture{
		home: home, projectsDir: project.ActiveDir(), program: p,
	}
}

func referenceDependencies(index int) []string {
	if index == 1 {
		return []string{}
	}
	if index == 2 {
		return []string{"w1"}
	}
	dependencies := []string{fmt.Sprintf("w%d", index-1), fmt.Sprintf("w%d", index-2)}
	if index >= 4 && index <= 6 {
		dependencies = append(dependencies, "w1")
	}
	return dependencies
}

func writeReferenceFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

var referenceArtifactNames = []string{
	"assignment.md", "task.md", "requirements.md", "clarify.md", "plan.md",
	"notes.md", "todos.md", "progress.md", "tradeoffs.md", "questions.md",
	"follow-ups.md", "review.md", "validation.md", "pr-body.md", "context.md",
}

type controlledAgentLister struct {
	release <-chan struct{}
	agents  []herdr.Agent
	err     error
	once    sync.Once
	started chan struct{}
}

func (l *controlledAgentLister) Agents() ([]herdr.Agent, error) {
	if l.started != nil {
		l.once.Do(func() { close(l.started) })
	}
	if l.release != nil {
		<-l.release
	}
	return append([]herdr.Agent(nil), l.agents...), l.err
}

type controlledFetcher struct {
	release <-chan struct{}
	result  programview.PullRequestDTO
	err     error
	once    sync.Once
	started chan struct{}
	delay   time.Duration
}

func (f *controlledFetcher) Fetch(ctx context.Context, _, _ string) (programview.PullRequestDTO, error) {
	if f.started != nil {
		f.once.Do(func() { close(f.started) })
	}
	if f.delay > 0 {
		timer := time.NewTimer(f.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return programview.PullRequestDTO{}, ctx.Err()
		case <-timer.C:
		}
	}
	if f.release != nil {
		select {
		case <-ctx.Done():
			return programview.PullRequestDTO{}, ctx.Err()
		case <-f.release:
		}
	}
	return f.result, f.err
}
