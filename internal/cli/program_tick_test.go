package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/project"
)

func runProgramTestGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repo}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
		"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func saveProgramTestProject(t *testing.T, dir string, manifest project.Manifest) {
	t.Helper()
	path := project.ManifestPath(dir, manifest.Slug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(path, manifest); err != nil {
		t.Fatal(err)
	}
}

func TestProgramTickIdempotencyAndCapacityMatrix(t *testing.T) {
	repo := newTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	startSHA := gitOutput(t, repoRoot, "rev-parse", "HEAD")

	p, err := program.New("governance", "Ship governance", repoRoot, "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	child, err := p.AddItem(program.WorkItem{Title: "Child review", Priority: program.PriorityP0})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.LinkItem(child.ID, "child-review"); err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(child.ID); err != nil {
		t.Fatal(err)
	}
	archived, err := p.AddItem(program.WorkItem{Title: "Archived child", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.LinkItem(archived.ID, "archived-child"); err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(archived.ID); err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	progressPath := program.ProgressPath(program.ProgramDir(program.ActiveDir(), p.Slug))
	if err := os.WriteFile(progressPath, []byte("# Progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pr999 := 999
	pr999URL := "https://example.test/pull/999"
	saveProgramTestProject(t, project.ActiveDir(), project.Manifest{
		Slug:       "child-review",
		Repo:       repoRoot,
		Branch:     "child-review",
		BaseBranch: "main",
		StartSHA:   startSHA,
		PR:         project.PRInfo{Number: &pr999, URL: &pr999URL},
	})
	state, err := project.NewState("child-review", "deliver-pr", []string{"implement"})
	if err != nil {
		t.Fatal(err)
	}
	state.SetPR(101, "https://example.test/pull/101")
	if err := project.SaveState(project.StatePath("child-review"), state); err != nil {
		t.Fatal(err)
	}

	pr202 := 202
	saveProgramTestProject(t, project.ActiveDir(), project.Manifest{
		Slug:       "standalone-pr",
		Repo:       repoRoot,
		Branch:     "standalone-pr",
		BaseBranch: "main",
		StartSHA:   startSHA,
		PR:         project.PRInfo{Number: &pr202},
	})
	saveProgramTestProject(t, project.ActiveDir(), project.Manifest{
		Slug:       "branch-without-pr",
		Repo:       repoRoot,
		Branch:     "branch-without-pr",
		BaseBranch: "main",
		StartSHA:   startSHA,
	})
	pr303 := 303
	saveProgramTestProject(t, project.ActiveDir(), project.Manifest{
		Slug: "different-repo",
		Repo: "/different/repo",
		PR:   project.PRInfo{Number: &pr303},
	})

	runProgramTestGit(t, repoRoot, "checkout", "-q", "-b", "merged-pr")
	if err := os.WriteFile(filepath.Join(repoRoot, "merged.txt"), []byte("merged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runProgramTestGit(t, repoRoot, "add", "merged.txt")
	runProgramTestGit(t, repoRoot, "commit", "-q", "-m", "merged work")
	runProgramTestGit(t, repoRoot, "checkout", "-q", "main")
	runProgramTestGit(t, repoRoot, "merge", "-q", "--ff-only", "merged-pr")
	pr404 := 404
	saveProgramTestProject(t, project.ActiveDir(), project.Manifest{
		Slug:       "merged-pr",
		Repo:       repoRoot,
		Branch:     "merged-pr",
		BaseBranch: "main",
		StartSHA:   startSHA,
		PR:         project.PRInfo{Number: &pr404},
	})
	saveProgramTestProject(t, project.ArchivedDir(), project.Manifest{
		Slug:   "archived-child",
		Repo:   repoRoot,
		Merged: true,
	})

	first, err := runProgramCommand(t, "tick", p.Slug, "--json")
	if err != nil {
		t.Fatalf("first tick: %v", err)
	}
	manifestPath := program.ManifestPath(program.ActiveDir(), p.Slug)
	manifestAfterFirst, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	progressAfterFirst, err := os.ReadFile(progressPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runProgramCommand(t, "tick", p.Slug, "--json")
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	manifestAfterSecond, _ := os.ReadFile(manifestPath)
	progressAfterSecond, _ := os.ReadFile(progressPath)
	if first != second {
		t.Fatalf("tick output changed:\nfirst: %s\nsecond: %s", first, second)
	}
	if !bytes.Equal(manifestAfterFirst, manifestAfterSecond) {
		t.Fatal("second tick changed program.json")
	}
	if !bytes.Equal(progressAfterFirst, progressAfterSecond) {
		t.Fatal("second tick grew progress.md")
	}

	var output programQueueOutput
	if err := json.Unmarshal([]byte(first), &output); err != nil {
		t.Fatalf("decode tick: %v\n%s", err, first)
	}
	if output.View.Capacity.Open != 1 || output.View.Capacity.Available != 2 {
		t.Fatalf("capacity = %+v", output.View.Capacity)
	}
	loaded, err := program.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	gotChild, _ := loaded.Item(child.ID)
	gotArchived, _ := loaded.Item(archived.ID)
	if gotChild.Status != program.ItemInReview || gotChild.PRRef != "https://example.test/pull/101" {
		t.Fatalf("child item = %+v", gotChild)
	}
	if gotArchived.Status != program.ItemMerged {
		t.Fatalf("archived item = %+v", gotArchived)
	}
}

func TestProgramTickUsesInjectedGitHubLifecycleForReconciliationAndCapacity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, err := filepath.EvalSymlinks(newTestRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	p, err := program.New("github-lifecycle", "GitHub lifecycle", repo, "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	merged, err := p.AddItem(program.WorkItem{Title: "Squash merged", Priority: program.PriorityP0})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := p.AddItem(program.WorkItem{Title: "Closed", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(merged.ID, "merged-child"); err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(closed.ID, "closed-child"); err != nil {
		t.Fatal(err)
	}
	if err := p.GrantOpenPR(closed.ID, "tl", nil); err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}

	original := buildProgramProjectViews
	buildProgramProjectViews = func(program.Program) ([]program.ProjectView, []programview.ProjectWarning, error) {
		return []program.ProjectView{
			{Slug: "merged-child", Repo: repo, PRRef: "#201", Merged: true},
			{Slug: "closed-child", Repo: repo, PRRef: "#202", PRClosed: true},
			{Slug: "open-unrelated", Repo: repo, HasPR: true, PRRef: "#203"},
		}, nil, nil
	}
	t.Cleanup(func() { buildProgramProjectViews = original })

	queueJSON, err := runProgramCommand(t, "queue", p.Slug, "--json")
	if err != nil {
		t.Fatalf("queue: %v\n%s", err, queueJSON)
	}
	var queue programQueueOutput
	if err := json.Unmarshal([]byte(queueJSON), &queue); err != nil {
		t.Fatalf("decode queue: %v\n%s", err, queueJSON)
	}
	if queue.View.Capacity != (program.Capacity{Limit: 3, Reserved: 1, Available: 2}) {
		t.Fatalf("queue capacity = %+v", queue.View.Capacity)
	}

	out, err := runProgramCommand(t, "tick", p.Slug, "--json")
	if err != nil {
		t.Fatalf("tick: %v\n%s", err, out)
	}
	var output programQueueOutput
	if err := json.Unmarshal([]byte(out), &output); err != nil {
		t.Fatalf("decode tick: %v\n%s", err, out)
	}
	if output.View.Capacity != (program.Capacity{Limit: 3, Reserved: 1, Available: 2}) {
		t.Fatalf("capacity = %+v", output.View.Capacity)
	}
	loaded, err := program.Load(program.ManifestPath(program.ActiveDir(), p.Slug))
	if err != nil {
		t.Fatal(err)
	}
	gotMerged, _ := loaded.Item(merged.ID)
	gotClosed, _ := loaded.Item(closed.ID)
	if gotMerged.Status != program.ItemMerged || gotMerged.PRRef != "#201" {
		t.Fatalf("merged item = %+v", gotMerged)
	}
	if gotClosed.Status != program.ItemDispatched || gotClosed.PRRef != "" {
		t.Fatalf("closed item = %+v, want no recorded closed pull request", gotClosed)
	}
}

func TestProgramTickVerifiesContractHashes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := createCLIProgram(t, "governance")
	source := filepath.Join(p.Repo, "contract.md")
	if err := os.WriteFile(source, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCommand(t, "contract", "publish", p.Slug, "api", "--file", source); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(program.ProgramDir(program.ActiveDir(), p.Slug), "contracts", "api", "v1.md")
	if err := os.Chmod(contractPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contractPath, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runProgramCommand(t, "tick", p.Slug); err == nil {
		t.Fatal("tick accepted a tampered contract")
	}
}

func TestArchivedProjectViewsDistinguishMergedFromDiscarded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := createCLIProgram(t, "governance")
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	merged, err := p.AddItem(program.WorkItem{Title: "Merged", Priority: program.PriorityP0})
	if err != nil {
		t.Fatal(err)
	}
	discarded, err := p.AddItem(program.WorkItem{Title: "Discarded", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	dependent, err := p.AddItem(program.WorkItem{
		Title: "Dependent", Priority: program.PriorityP2, Dependencies: []string{discarded.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(merged.ID, "merged-child"); err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(discarded.ID, "discarded-child"); err != nil {
		t.Fatal(err)
	}
	saveProgramTestProject(t, project.ArchivedDir(), project.Manifest{
		Slug: "merged-child", Repo: p.Repo, Merged: true,
	})
	saveProgramTestProject(t, project.ArchivedDir(), project.Manifest{
		Slug: "discarded-child", Repo: p.Repo,
	})

	views, _, err := buildProgramProjectViews(p)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Reconcile(views)
	if err != nil {
		t.Fatal(err)
	}
	gotMerged, _ := p.Item(merged.ID)
	gotDiscarded, _ := p.Item(discarded.ID)
	if gotMerged.Status != program.ItemMerged {
		t.Fatalf("merged archived item status = %s", gotMerged.Status)
	}
	if gotDiscarded.Status != program.ItemDispatched {
		t.Fatalf("discarded archived item status = %s", gotDiscarded.Status)
	}
	if len(result.OrphanIDs) != 1 || result.OrphanIDs[0] != discarded.ID {
		t.Fatalf("orphan IDs = %v", result.OrphanIDs)
	}
	_, blocked := p.Readiness()
	if len(blocked) != 1 || blocked[0].Item.ID != dependent.ID ||
		len(blocked[0].Reasons) != 1 || blocked[0].Reasons[0] != "dependency "+discarded.ID+" is dispatched" {
		t.Fatalf("dependent readiness = %+v", blocked)
	}
}

func TestProgramTickSurfacesMissingChildIdempotently(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := createCLIProgram(t, "governance")
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "Missing child", Priority: program.PriorityP0})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := p.AddItem(program.WorkItem{Title: "Ready work", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(item.ID, "missing-child"); err != nil {
		t.Fatal(err)
	}
	path := program.ManifestPath(program.ActiveDir(), p.Slug)
	if err := program.Save(path, p); err != nil {
		t.Fatal(err)
	}
	progressPath := program.ProgressPath(program.ProgramDir(program.ActiveDir(), p.Slug))
	if err := os.WriteFile(progressPath, []byte("# Progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := runProgramCommand(t, "tick", p.Slug, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var output programQueueOutput
	if err := json.Unmarshal([]byte(first), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.OrphanIDs) != 1 || output.OrphanIDs[0] != item.ID {
		t.Fatalf("orphan IDs = %v", output.OrphanIDs)
	}
	if !bytes.Contains([]byte(output.NextCommand), []byte("relay program item block "+p.Slug+" "+item.ID)) {
		t.Fatalf("orphan next command = %q", output.NextCommand)
	}
	if _, err := runProgramCommand(t, "item", "block", p.Slug, item.ID,
		"--reason", "linked child project is missing"); err != nil {
		t.Fatal(err)
	}
	manifestBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	progressBefore, err := os.ReadFile(progressPath)
	if err != nil {
		t.Fatal(err)
	}

	second, err := runProgramCommand(t, "tick", p.Slug, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var afterBlock programQueueOutput
	if err := json.Unmarshal([]byte(second), &afterBlock); err != nil {
		t.Fatal(err)
	}
	if len(afterBlock.OrphanIDs) != 0 {
		t.Fatalf("blocked item reported again as orphan: %v", afterBlock.OrphanIDs)
	}
	wantNext := "relay program dispatch " + p.Slug + " " + ready.ID
	if afterBlock.NextCommand != wantNext {
		t.Fatalf("next command after blocking orphan = %q, want %q", afterBlock.NextCommand, wantNext)
	}
	text, err := runProgramCommand(t, "tick", p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(text), []byte("Orphaned:")) || !bytes.Contains([]byte(text), []byte(wantNext)) {
		t.Fatalf("post-block tick output = %q", text)
	}
	manifestAfter, _ := os.ReadFile(path)
	progressAfter, _ := os.ReadFile(progressPath)
	if !bytes.Equal(manifestBefore, manifestAfter) || !bytes.Equal(progressBefore, progressAfter) {
		t.Fatal("unchanged orphan tick modified durable state")
	}
}

func gitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(bytes.TrimSpace(output))
}
