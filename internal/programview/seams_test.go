package programview

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

type prIndexFunc func(string) (PRState, bool)

func (f prIndexFunc) Lookup(ref string) (PRState, bool) {
	return f(ref)
}

func TestNextCommand(t *testing.T) {
	p := program.Program{
		Slug: "relay-v1",
		Decisions: []program.Decision{{
			ID:          "d1",
			Kind:        program.DecisionContract,
			ContractRef: "api@v1",
		}},
	}

	got := NextCommand(p, program.View{NextAction: "resolve d1"})
	want := "relay program contract approve relay-v1 api@v1 --by ceo"
	if got != want {
		t.Fatalf("NextCommand() = %q, want %q", got, want)
	}
}

func TestProjectViewsIgnoresMalformedUnrelatedState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newProgramViewTestRepo(t)
	prNumber := 17
	manifest := project.Manifest{
		Slug: "unrelated", Repo: repo, Branch: "feature", BaseBranch: "main",
		PR:              project.PRInfo{Number: &prNumber},
		PhasesCompleted: []string{}, PhasesRemaining: []string{},
	}
	if err := os.MkdirAll(filepath.Dir(project.ManifestPath(project.ActiveDir(), manifest.Slug)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project.StatePath(manifest.Slug), []byte(`{"slug":"unrelated"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := program.New("program", "Program", repo, "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}

	views, warnings, err := ProjectViews(p)
	if err != nil {
		t.Fatalf("ProjectViews rejected unrelated state: %v", err)
	}
	if len(views) != 0 || len(warnings) != 0 {
		t.Fatalf("unrelated views = %+v, warnings = %+v; want none", views, warnings)
	}
}

func TestProjectViewsIgnoreUnrelatedStaleDirectories(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	staleDir := filepath.Join(project.ActiveDir(), "old-stack-child")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := program.New("program", "Program", t.TempDir(), "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}

	views, warnings, err := ProjectViews(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 0 || len(warnings) != 0 {
		t.Fatalf("views = %+v, warnings = %+v; want unrelated directory ignored", views, warnings)
	}
}

func TestProjectViewsDegradeUnreadableLinkedStateWithoutFailing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newProgramViewTestRepo(t)
	prNumber := 42
	manifest := project.Manifest{
		Slug: "linked", Repo: repo, Branch: "feature", BaseBranch: "main",
		Program:         "program",
		ProgramItem:     "w1",
		PR:              project.PRInfo{Number: &prNumber},
		PhasesCompleted: []string{}, PhasesRemaining: []string{},
	}
	if err := os.MkdirAll(filepath.Dir(project.ManifestPath(project.ActiveDir(), manifest.Slug)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), manifest.Slug), manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project.StatePath(manifest.Slug), []byte(`{"slug":"linked"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := program.New("program", "Program", repo, "copilot", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "Linked", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(item.ID, manifest.Slug); err != nil {
		t.Fatal(err)
	}

	views, warnings, err := ProjectViews(p)
	if err != nil {
		t.Fatalf("ProjectViews hard-failed on an unreadable linked child: %v", err)
	}
	want := []program.ProjectView{{
		Slug: "linked", Repo: repo, HasPR: true, PRRef: "#42", Unavailable: true,
	}}
	if !reflect.DeepEqual(views, want) {
		t.Fatalf("views = %+v, want %+v", views, want)
	}
	if len(warnings) != 1 || warnings[0].ProjectSlug != "linked" ||
		!strings.Contains(warnings[0].Message, `active project "linked"`) {
		t.Fatalf("warnings = %+v", warnings)
	}
	if capacity := p.Plan(views).Capacity; capacity.Open != 1 || capacity.Available != 1 {
		t.Fatalf("capacity = %+v, want the recorded PR to still consume capacity", capacity)
	}
	result, err := p.Reconcile(views)
	if err != nil {
		t.Fatalf("Reconcile with an unavailable child: %v", err)
	}
	if result.Changed || len(result.OrphanIDs) != 0 {
		t.Fatalf("reconcile result = %+v, want no orphan or merge decision", result)
	}
}

func TestProjectViewsReportUnavailableChildWithoutRecordedPRAsProgramRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newProgramViewTestRepo(t)
	dir := filepath.Join(project.ActiveDir(), "broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(project.ManifestPath(project.ActiveDir(), "broken"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := program.New("program", "Program", repo, "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "Broken", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(item.ID, "broken"); err != nil {
		t.Fatal(err)
	}

	views, warnings, err := ProjectViews(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []program.ProjectView{{Slug: "broken", Repo: repo, Unavailable: true}}
	if !reflect.DeepEqual(views, want) {
		t.Fatalf("views = %+v, want %+v", views, want)
	}
	if len(warnings) != 1 || warnings[0].ProjectSlug != "broken" {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestProjectViewsUseItemRepositoryAndRepositoryScopedPRIndexes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	primary := newProgramViewTestRepo(t)
	secondary := newProgramViewTestRepo(t)
	p, err := program.New("program", "Program", primary, "copilot", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	primaryItem, err := p.AddItem(program.WorkItem{Title: "Primary", Priority: program.PriorityP0})
	if err != nil {
		t.Fatal(err)
	}
	secondaryItem, err := p.AddItem(program.WorkItem{
		Title: "Secondary", Priority: program.PriorityP0, Repo: secondary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(primaryItem.ID, "primary-child"); err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(secondaryItem.ID, "secondary-child"); err != nil {
		t.Fatal(err)
	}
	number := 42
	saveProjectManifest(t, project.ActiveDir(), project.Manifest{
		Slug: "primary-child", Repo: primary, Branch: "primary",
		Program: p.Slug, ProgramItem: primaryItem.ID, PR: project.PRInfo{Number: &number},
	})
	saveProjectManifest(t, project.ActiveDir(), project.Manifest{
		Slug: "secondary-child", Repo: secondary, Branch: "secondary",
		Program: p.Slug, ProgramItem: secondaryItem.ID, PR: project.PRInfo{Number: &number},
	})

	loadedRepos := map[string][]string{}
	views, warnings, err := projectViews(p, func(repo string, refs []string) PRIndexLoadResult {
		loadedRepos[repo] = append([]string(nil), refs...)
		state := PRStateOpen
		if repo == secondary {
			state = PRStateMerged
		}
		return PRIndexLoadResult{
			Index: prIndexFunc(func(string) (PRState, bool) { return state, true }),
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %+v", warnings)
	}
	want := []program.ProjectView{
		{Slug: "primary-child", Repo: primary, HasPR: true, PRRef: "#42"},
		{Slug: "secondary-child", Repo: secondary, PRRef: "#42", Merged: true},
	}
	if !reflect.DeepEqual(views, want) {
		t.Fatalf("views = %+v, want %+v", views, want)
	}
	if !reflect.DeepEqual(loadedRepos, map[string][]string{
		primary: {"#42"}, secondary: {"#42"},
	}) {
		t.Fatalf("loaded repositories = %+v", loadedRepos)
	}
}

func TestProjectViewsWarnsPerRepositoryLookupFailureAndPreservesCapacity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	primary := newProgramViewTestRepo(t)
	secondary := newProgramViewTestRepo(t)
	p, err := program.New("program", "Program", primary, "copilot", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	primaryItem, err := p.AddItem(program.WorkItem{Title: "Primary", Priority: program.PriorityP0})
	if err != nil {
		t.Fatal(err)
	}
	secondaryItem, err := p.AddItem(program.WorkItem{
		Title: "Secondary", Priority: program.PriorityP0, Repo: secondary,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, linked := range []struct {
		item program.WorkItem
		slug string
		repo string
	}{
		{item: primaryItem, slug: "primary-child", repo: primary},
		{item: secondaryItem, slug: "secondary-child", repo: secondary},
	} {
		if err := p.DispatchItem(linked.item.ID, linked.slug); err != nil {
			t.Fatal(err)
		}
		number := 42
		saveProjectManifest(t, project.ActiveDir(), project.Manifest{
			Slug: linked.slug, Repo: linked.repo, Branch: linked.slug,
			Program: p.Slug, ProgramItem: linked.item.ID, PR: project.PRInfo{Number: &number},
		})
	}

	views, warnings, err := projectViews(p, func(repo string, _ []string) PRIndexLoadResult {
		if repo == secondary {
			return PRIndexLoadResult{Errors: []PRIndexLoadError{{
				Ref: "#42", Err: errors.New(`load pull request "#42": authentication failed`),
			}}}
		}
		return PRIndexLoadResult{Index: prIndexFunc(func(string) (PRState, bool) {
			return PRStateMerged, true
		})}
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []program.ProjectView{
		{Slug: "primary-child", Repo: primary, PRRef: "#42", Merged: true},
		{Slug: "secondary-child", Repo: secondary, HasPR: true, PRRef: "#42"},
	}
	if !reflect.DeepEqual(views, want) {
		t.Fatalf("views = %+v, want %+v", views, want)
	}
	if len(warnings) != 1 || warnings[0].ProjectSlug != "secondary-child" ||
		!strings.Contains(warnings[0].Message, "authentication failed") {
		t.Fatalf("warnings = %+v", warnings)
	}
	if capacity := p.Plan(views).Capacity; capacity.Open != 1 {
		t.Fatalf("capacity = %+v, want failed repository PR to remain open", capacity)
	}
}

func TestProjectViewsRejectChildOwnershipMismatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	primary := newProgramViewTestRepo(t)
	secondary := newProgramViewTestRepo(t)
	p, err := program.New("program", "Program", primary, "copilot", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{
		Title: "Secondary", Priority: program.PriorityP0, Repo: secondary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(item.ID, "secondary-child"); err != nil {
		t.Fatal(err)
	}
	saveProjectManifest(t, project.ActiveDir(), project.Manifest{
		Slug: "secondary-child", Repo: primary, Branch: "wrong",
		Program: p.Slug, ProgramItem: item.ID,
	})

	views, warnings, err := projectViews(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 0 {
		t.Fatalf("views = %+v", views)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "does not match linked item") {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestProjectViewsRejectManifestSlugMismatch(t *testing.T) {
	for _, archived := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "archived"}[archived], func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repo := newProgramViewTestRepo(t)
			p, err := program.New("program", "Program", repo, "copilot", 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Transition(program.StatePendingApproval, ""); err != nil {
				t.Fatal(err)
			}
			if err := p.Transition(program.StateActive, "ceo"); err != nil {
				t.Fatal(err)
			}
			item, err := p.AddItem(program.WorkItem{Title: "Child", Priority: program.PriorityP0})
			if err != nil {
				t.Fatal(err)
			}
			if err := p.DispatchItem(item.ID, "linked-child"); err != nil {
				t.Fatal(err)
			}
			root := project.ActiveDir()
			if archived {
				root = project.ArchivedDir()
			}
			path := project.ManifestPath(root, "linked-child")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := project.Save(path, project.Manifest{
				Slug: "different-child", Repo: repo, Branch: "feature",
				Program: p.Slug, ProgramItem: item.ID,
			}); err != nil {
				t.Fatal(err)
			}

			views, warnings, err := projectViews(p, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(views) != 0 || len(warnings) != 1 ||
				warnings[0].ProjectSlug != "linked-child" ||
				!strings.Contains(warnings[0].Message, `slug="different-child"`) {
				t.Fatalf("views = %+v, warnings = %+v", views, warnings)
			}
		})
	}
}

func TestProjectViewsMarksDeletedItemRepositoryUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	primary := newProgramViewTestRepo(t)
	secondary := newProgramViewTestRepo(t)
	p, err := program.New("program", "Program", primary, "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{
		Title: "Secondary", Priority: program.PriorityP0, Repo: secondary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(item.ID, "secondary-child"); err != nil {
		t.Fatal(err)
	}
	number := 42
	saveProjectManifest(t, project.ActiveDir(), project.Manifest{
		Slug: "secondary-child", Repo: secondary, Branch: "feature",
		Program: p.Slug, ProgramItem: item.ID, PR: project.PRInfo{Number: &number},
	})
	if err := os.RemoveAll(secondary); err != nil {
		t.Fatal(err)
	}

	views, warnings, err := ProjectViewsWithPRIndex(p, prIndexFunc(func(string) (PRState, bool) {
		return PRStateMerged, true
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := []program.ProjectView{{
		Slug: "secondary-child", Repo: secondary, HasPR: true, PRRef: "#42", Unavailable: true,
	}}
	if !reflect.DeepEqual(views, want) {
		t.Fatalf("views = %+v, want %+v", views, want)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, secondary) {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestProjectViewsDefaultBranchFailureDoesNotDisableRepositorySiblings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	runProgramViewTestGit(t, repo, "init", "-b", "develop")
	runProgramViewTestGit(t, repo, "config", "user.name", "Relay Test")
	runProgramViewTestGit(t, repo, "config", "user.email", "relay@example.com")
	runProgramViewTestGit(t, repo, "commit", "--allow-empty", "-m", "initial")

	p, err := program.New("program", "Program", repo, "copilot", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	legacy, err := p.AddItem(program.WorkItem{Title: "Legacy", Priority: program.PriorityP0})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := p.AddItem(program.WorkItem{Title: "Explicit", Priority: program.PriorityP0})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(legacy.ID, "a-legacy"); err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(explicit.ID, "z-explicit"); err != nil {
		t.Fatal(err)
	}
	saveProjectManifest(t, project.ActiveDir(), project.Manifest{
		Slug: "a-legacy", Repo: repo, Branch: "develop",
		Program: p.Slug, ProgramItem: legacy.ID,
	})
	saveProjectManifest(t, project.ActiveDir(), project.Manifest{
		Slug: "z-explicit", Repo: repo, Branch: "develop", BaseBranch: "develop",
		Program: p.Slug, ProgramItem: explicit.ID,
	})

	views, warnings, err := projectViews(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []program.ProjectView{
		{Slug: "a-legacy", Repo: repo, Unavailable: true},
		{Slug: "z-explicit", Repo: repo},
	}
	if !reflect.DeepEqual(views, want) {
		t.Fatalf("views = %+v, want %+v", views, want)
	}
	if len(warnings) != 1 || warnings[0].ProjectSlug != "a-legacy" ||
		!strings.Contains(warnings[0].Message, "cannot determine default branch") {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestProjectViewsWithPRIndexUsesGitHubLifecycleForLinkedCapacity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newProgramViewTestRepo(t)
	refs := map[string]string{"closed": "https://github.example/acme/repo/pull/103", "merged": "#102", "open": "#101"}
	p, err := program.New("program", "Program", repo, "copilot", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"closed", "merged", "open"} {
		item, err := p.AddItem(program.WorkItem{Title: slug, Priority: program.PriorityP1})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.DispatchItem(item.ID, slug); err != nil {
			t.Fatal(err)
		}
		ref := refs[slug]
		number, ok := PullRequestNumber(ref)
		if !ok {
			t.Fatalf("invalid test PR ref %q", ref)
		}
		manifest := project.Manifest{
			Slug: slug, Repo: repo, Branch: "feature-" + slug,
			Program: p.Slug, ProgramItem: item.ID, PR: project.PRInfo{Number: &number},
		}
		if strings.HasPrefix(ref, "http") {
			manifest.PR.URL = &ref
		}
		saveProjectManifest(t, project.ActiveDir(), manifest)
	}
	states := map[string]PRState{
		refs["open"]:   PRStateOpen,
		refs["merged"]: PRStateMerged,
		refs["closed"]: PRStateClosed,
	}
	views, warnings, err := ProjectViewsWithPRIndex(p, prIndexFunc(func(ref string) (PRState, bool) {
		state, ok := states[ref]
		return state, ok
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %+v", warnings)
	}
	want := []program.ProjectView{
		{Slug: "closed", Repo: repo, PRRef: refs["closed"], PRClosed: true},
		{Slug: "merged", Repo: repo, PRRef: refs["merged"], Merged: true},
		{Slug: "open", Repo: repo, HasPR: true, PRRef: refs["open"]},
	}
	if !reflect.DeepEqual(views, want) {
		t.Fatalf("views = %+v, want %+v", views, want)
	}
	if capacity := p.Plan(views).Capacity; capacity != (program.Capacity{Limit: 3, Open: 1, Available: 2}) {
		t.Fatalf("capacity = %+v", capacity)
	}
}

func TestProjectViewsWithPRIndexKeepsRecordedPROpenWhenUnavailableOrAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newProgramViewTestRepo(t)
	number := 17
	saveProjectManifest(t, project.ActiveDir(), project.Manifest{
		Slug: "child", Repo: repo, Branch: "feature",
		Program: "program", ProgramItem: "w1",
		PR: project.PRInfo{Number: &number},
	})
	p, err := program.New("program", "Program", repo, "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "Child", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(item.ID, "child"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		index PRIndex
	}{
		{name: "unavailable"},
		{name: "absent", index: prIndexFunc(func(string) (PRState, bool) {
			return "", false
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			views, _, err := ProjectViewsWithPRIndex(p, test.index)
			if err != nil {
				t.Fatal(err)
			}
			want := []program.ProjectView{{Slug: "child", Repo: repo, HasPR: true, PRRef: "#17"}}
			if !reflect.DeepEqual(views, want) {
				t.Fatalf("views = %+v, want %+v", views, want)
			}
			if capacity := p.Plan(views).Capacity; capacity.Open != 1 || capacity.Available != 0 {
				t.Fatalf("capacity = %+v", capacity)
			}
		})
	}
}

func TestProjectViewsWithPRIndexReconcilesMergedAndClosedLinkedItems(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newProgramViewTestRepo(t)
	p, err := program.New("program", "Program", repo, "copilot", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	mergedItem, err := p.AddItem(program.WorkItem{Title: "Squash merged", Priority: program.PriorityP0})
	if err != nil {
		t.Fatal(err)
	}
	closedItem, err := p.AddItem(program.WorkItem{Title: "Closed", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(mergedItem.ID, "merged-child"); err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(closedItem.ID, "closed-child"); err != nil {
		t.Fatal(err)
	}
	if err := p.GrantOpenPR(closedItem.ID, "tl", nil); err != nil {
		t.Fatal(err)
	}
	for slug, number := range map[string]int{"merged-child": 201, "closed-child": 202} {
		itemID := mergedItem.ID
		if slug == "closed-child" {
			itemID = closedItem.ID
		}
		saveProjectManifest(t, project.ActiveDir(), project.Manifest{
			Slug: slug, Repo: repo, Branch: "missing-" + slug,
			Program: p.Slug, ProgramItem: itemID,
			PR: project.PRInfo{Number: &number},
		})
	}
	views, _, err := ProjectViewsWithPRIndex(p, prIndexFunc(func(ref string) (PRState, bool) {
		switch ref {
		case "#201":
			return PRStateMerged, true
		case "#202":
			return PRStateClosed, true
		default:
			return "", false
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	capacity := p.Plan(views).Capacity
	if capacity.Open != 0 || capacity.Reserved != 1 || capacity.Available != 1 {
		t.Fatalf("capacity = %+v, want the closed PR freed and the outstanding grant reserved", capacity)
	}
	if _, err := p.Reconcile(views); err != nil {
		t.Fatal(err)
	}
	gotMerged, _ := p.Item(mergedItem.ID)
	gotClosed, _ := p.Item(closedItem.ID)
	if gotMerged.Status != program.ItemMerged || gotMerged.PRRef != "#201" {
		t.Fatalf("merged item = %+v", gotMerged)
	}
	if gotClosed.Status != program.ItemDispatched || gotClosed.PRRef != "" {
		t.Fatalf("closed item = %+v, want a cleared reference for a replacement PR", gotClosed)
	}
}

func saveProjectManifest(t *testing.T, root string, manifest project.Manifest) {
	t.Helper()
	manifest.PhasesCompleted = []string{}
	manifest.PhasesRemaining = []string{}
	if err := os.MkdirAll(filepath.Dir(project.ManifestPath(root, manifest.Slug)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(project.ManifestPath(root, manifest.Slug), manifest); err != nil {
		t.Fatal(err)
	}
}

func newProgramViewTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runProgramViewTestGit(t, repo, "init", "-b", "main")
	runProgramViewTestGit(t, repo, "config", "user.name", "Relay Test")
	runProgramViewTestGit(t, repo, "config", "user.email", "relay@example.com")
	runProgramViewTestGit(t, repo, "commit", "--allow-empty", "-m", "initial")
	return repo
}

func initProgramViewTestRepo(t *testing.T, repo string) {
	t.Helper()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runProgramViewTestGit(t, repo, "init", "-b", "main")
	runProgramViewTestGit(t, repo, "config", "user.name", "Relay Test")
	runProgramViewTestGit(t, repo, "config", "user.email", "relay@example.com")
	runProgramViewTestGit(t, repo, "commit", "--allow-empty", "-m", "initial")
}

func runProgramViewTestGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", repo}, args...)
	if output, err := exec.Command("git", commandArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func archivedChildProgram(t *testing.T, repo string, merged bool, number int) (program.Program, program.WorkItem) {
	t.Helper()
	p, err := program.New("program", "Program", repo, "copilot", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{Title: "Archived", Priority: program.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(item.ID, "archived-child"); err != nil {
		t.Fatal(err)
	}
	saveProjectManifest(t, project.ArchivedDir(), project.Manifest{
		Slug: "archived-child", Repo: repo, Branch: "feature",
		Program: p.Slug, ProgramItem: item.ID,
		PR: project.PRInfo{Number: &number}, Merged: merged,
	})
	loaded, _ := p.Item(item.ID)
	return p, loaded
}

func TestArchivedLinkedChildReconcilesFromGitHubPullRequestState(t *testing.T) {
	tests := []struct {
		name         string
		index        PRIndex
		wantMerged   bool
		wantOrphaned bool
		wantStatus   program.ItemStatus
	}{
		{
			name: "merged pull request with a pruned branch",
			index: prIndexFunc(func(ref string) (PRState, bool) {
				if ref == "#301" {
					return PRStateMerged, true
				}
				return "", false
			}),
			wantMerged: true,
			wantStatus: program.ItemMerged,
		},
		{
			name: "closed pull request stays unmerged",
			index: prIndexFunc(func(ref string) (PRState, bool) {
				if ref == "#301" {
					return PRStateClosed, true
				}
				return "", false
			}),
			wantOrphaned: true,
			wantStatus:   program.ItemDispatched,
		},
		{
			name:         "absent index falls back to the manifest",
			index:        prIndexFunc(func(string) (PRState, bool) { return "", false }),
			wantOrphaned: true,
			wantStatus:   program.ItemDispatched,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repo := newProgramViewTestRepo(t)
			p, item := archivedChildProgram(t, repo, false, 301)

			views, warnings, err := ProjectViewsWithPRIndex(p, test.index)
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) != 0 || len(views) != 1 {
				t.Fatalf("views = %+v, warnings = %+v", views, warnings)
			}
			if views[0].Merged != test.wantMerged || views[0].Orphaned != test.wantOrphaned || !views[0].Archived {
				t.Fatalf("archived view = %+v", views[0])
			}
			result, err := p.Reconcile(views)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := p.Item(item.ID)
			if got.Status != test.wantStatus {
				t.Fatalf("item status = %s, want %s", got.Status, test.wantStatus)
			}
			wantOrphanIDs := 0
			if test.wantOrphaned {
				wantOrphanIDs = 1
			}
			if len(result.OrphanIDs) != wantOrphanIDs {
				t.Fatalf("orphan IDs = %v, want %d", result.OrphanIDs, wantOrphanIDs)
			}
		})
	}
}

func TestArchivedLinkedChildKeepsManifestMergeWhenPullRequestWasClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newProgramViewTestRepo(t)
	p, item := archivedChildProgram(t, repo, true, 302)

	views, _, err := ProjectViewsWithPRIndex(p, prIndexFunc(func(string) (PRState, bool) {
		return PRStateClosed, true
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !views[0].Merged || views[0].Orphaned || views[0].PRClosed {
		t.Fatalf("archived view = %+v, want a verified merge to survive a closed PR", views[0])
	}
	if _, err := p.Reconcile(views); err != nil {
		t.Fatal(err)
	}
	got, _ := p.Item(item.ID)
	if got.Status != program.ItemMerged {
		t.Fatalf("item status = %s, want merged", got.Status)
	}
}

func TestArchivedSecondaryChildUsesRepositoryScopedPullRequestState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	primary := newProgramViewTestRepo(t)
	secondary := newProgramViewTestRepo(t)
	p, err := program.New("program", "Program", primary, "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StatePendingApproval, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(program.StateActive, "ceo"); err != nil {
		t.Fatal(err)
	}
	item, err := p.AddItem(program.WorkItem{
		Title: "Archived secondary", Priority: program.PriorityP0, Repo: secondary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DispatchItem(item.ID, "archived-secondary"); err != nil {
		t.Fatal(err)
	}
	number := 42
	saveProjectManifest(t, project.ArchivedDir(), project.Manifest{
		Slug: "archived-secondary", Repo: secondary, Branch: "feature",
		Program: p.Slug, ProgramItem: item.ID, PR: project.PRInfo{Number: &number},
	})

	loadedRepo := ""
	views, warnings, err := projectViews(p, func(repo string, refs []string) PRIndexLoadResult {
		loadedRepo = repo
		if !reflect.DeepEqual(refs, []string{"#42"}) {
			t.Fatalf("refs = %v", refs)
		}
		return PRIndexLoadResult{Index: prIndexFunc(func(string) (PRState, bool) {
			return PRStateMerged, true
		})}
	})
	if err != nil {
		t.Fatal(err)
	}
	if loadedRepo != secondary || len(warnings) != 0 || len(views) != 1 ||
		!views[0].Archived || !views[0].Merged || views[0].Repo != secondary {
		t.Fatalf("repo = %q, views = %+v, warnings = %+v", loadedRepo, views, warnings)
	}
}

func TestReadOnlySnapshotMatchesStrictCLICapacity(t *testing.T) {
	tests := []struct {
		name  string
		state PRState
		open  int
	}{
		{name: "open", state: PRStateOpen, open: 1},
		{name: "squash merged", state: PRStateMerged},
		{name: "closed", state: PRStateClosed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repo := newProgramViewTestRepo(t)
			number := 404
			saveProjectManifest(t, project.ActiveDir(), project.Manifest{
				Slug: "child", Repo: repo, Branch: "feature",
				Program: "program", ProgramItem: "w1", PR: project.PRInfo{Number: &number},
			})
			p, err := program.New("program", "Program", repo, "copilot", 2)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Transition(program.StatePendingApproval, ""); err != nil {
				t.Fatal(err)
			}
			if err := p.Transition(program.StateActive, "ceo"); err != nil {
				t.Fatal(err)
			}
			item, err := p.AddItem(program.WorkItem{Title: "Child", Priority: program.PriorityP1})
			if err != nil {
				t.Fatal(err)
			}
			if err := p.DispatchItem(item.ID, "child"); err != nil {
				t.Fatal(err)
			}
			if err := program.Create(p); err != nil {
				t.Fatal(err)
			}
			index := prIndexFunc(func(string) (PRState, bool) { return test.state, true })

			cliViews, _, err := ProjectViewsWithPRIndex(p, index)
			if err != nil {
				t.Fatal(err)
			}
			cliCapacity := p.Plan(cliViews).Capacity
			snapshot, err := Build(p.Slug, Options{PRIndex: index})
			if err != nil {
				t.Fatal(err)
			}
			uiCapacity := snapshot.Plan.Capacity
			if cliCapacity.Open != test.open {
				t.Fatalf("CLI capacity = %+v, want %d open", cliCapacity, test.open)
			}
			if uiCapacity.Open != cliCapacity.Open ||
				uiCapacity.Reserved != cliCapacity.Reserved ||
				uiCapacity.Available != cliCapacity.Available ||
				uiCapacity.Limit != cliCapacity.Limit {
				t.Fatalf("UI capacity = %+v, CLI capacity = %+v", uiCapacity, cliCapacity)
			}
			if test.state == PRStateMerged {
				if snapshot.Progress.Merged != 1 {
					t.Fatalf("snapshot progress = %+v, want the merged PR reconciled", snapshot.Progress)
				}
			}
		})
	}
}
