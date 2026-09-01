package patrol

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/project"
)

func TestTickBuildsOneReadOnlyObservationWithoutLockOrState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := 0
	observation, err := Tick("diagnostic", Options{
		Now: func() time.Time { return time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC) },
		BuildSnapshot: func(slug string, options programview.Options) (programview.Snapshot, error) {
			calls++
			if slug != "diagnostic" || !options.Now().Equal(time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)) {
				t.Fatalf("builder input = %q, %s", slug, options.Now())
			}
			return programview.Snapshot{
				Program: programview.ProgramDTO{Slug: slug, State: string(program.StateActive)},
				Plan: programview.PlanDTO{
					Ready: []string{}, Blocked: []programview.BlockedPlanDTO{},
					Orphaned: []string{}, OpenDecisions: []string{},
				},
				Items:         []programview.ItemDTO{},
				OpenDecisions: []programview.DecisionDTO{},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || observation.ProgramSlug != "diagnostic" || observation.DelaySeconds != 1800 {
		t.Fatalf("Tick = calls %d observation %+v", calls, observation)
	}
	if _, err := ReadState("diagnostic"); err == nil {
		t.Fatal("Tick wrote patrol state")
	}
	if running, err := IsRunning("diagnostic"); err != nil || running {
		t.Fatalf("Tick acquired patrol lock: running=%t err=%v", running, err)
	}
}

type patrolAgentListerFunc func() ([]herdr.Agent, error)

func (f patrolAgentListerFunc) Agents() ([]herdr.Agent, error) {
	return f()
}

type observedFile struct {
	data    []byte
	modTime time.Time
}

func TestTickNeverMutatesProgramProjectOrMailboxAcrossObservedChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := program.New("readonly-patrol", "Observe only", repo, "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "child", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(item.ID, "readonly-child"); err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(repo, ".worktrees", "readonly-child")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := project.Manifest{
		Slug: "readonly-child", Title: "Child", Repo: repo, Branch: "feature",
		Worktree: &worktree, Status: "active", Workflow: "deliver-pr", Phase: "implement",
		Created: p.CreatedAt, Updated: p.UpdatedAt,
		PhasesCompleted: []string{}, PhasesRemaining: []string{},
	}
	manifestPath := project.ManifestPath(project.ActiveDir(), manifest.Slug)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	state, err := project.NewState(manifest.Slug, manifest.Workflow, []string{"implement"})
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SaveState(project.StatePath(manifest.Slug), state); err != nil {
		t.Fatal(err)
	}
	childDir := filepath.Dir(project.ManifestPath(project.ActiveDir(), manifest.Slug))
	if _, err := mailbox.Send(childDir, mailbox.Outbox, mailbox.Message{
		ID: "out-1", Kind: mailbox.KindQuestion, Program: p.Slug, Item: item.ID,
		From: mailbox.ActorWorker, To: mailbox.ActorTL, Body: "question",
		Options: []string{}, CreatedAt: p.CreatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	agents := patrolAgentListerFunc(func() ([]herdr.Agent, error) {
		return []herdr.Agent{{
			Status: herdr.StatusWorking, PaneID: "worker",
			TerminalTitle: "relay:" + manifest.Slug, CWD: worktree,
		}}, nil
	})

	assertReadOnlyTick := func() {
		t.Helper()
		before := captureObservedFiles(t, program.ProgramsDir(), project.ProjectsDir())
		if _, err := Tick(p.Slug, Options{Agents: agents}); err != nil {
			t.Fatal(err)
		}
		after := captureObservedFiles(t, program.ProgramsDir(), project.ProjectsDir())
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("patrol tick mutated observed files:\n before: %#v\n after: %#v", before, after)
		}
	}

	assertReadOnlyTick()
	state.SetPR(42, "https://github.example/pull/42")
	if err := project.SaveState(project.StatePath(manifest.Slug), state); err != nil {
		t.Fatal(err)
	}
	assertReadOnlyTick()
	manifest.Merged = true
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	archivedDir := filepath.Dir(project.ManifestPath(project.ArchivedDir(), manifest.Slug))
	if err := os.MkdirAll(project.ArchivedDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(childDir, archivedDir); err != nil {
		t.Fatal(err)
	}
	assertReadOnlyTick()

	if entries, err := os.ReadDir(filepath.Join(archivedDir, "mail", "notified")); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("patrol wrote notified markers: %v", entries)
	}
}

func captureObservedFiles(t *testing.T, roots ...string) map[string]observedFile {
	t.Helper()
	result := make(map[string]observedFile)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] = observedFile{data: bytes.Clone(data), modTime: info.ModTime()}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}
