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
	repo := t.TempDir()
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
	repo := t.TempDir()
	prNumber := 42
	manifest := project.Manifest{
		Slug: "linked", Repo: repo, Branch: "feature", BaseBranch: "main",
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
	repo := t.TempDir()
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

func TestProjectViewsWithPRIndexUsesGitHubLifecycleForLinkedCapacity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	refs := map[string]string{
		"closed": "https://github.example/acme/repo/pull/103",
		"merged": "#102",
		"open":   "#101",
	}
	for slug, ref := range refs {
		number, ok := PullRequestNumber(ref)
		if !ok {
			t.Fatalf("invalid test PR ref %q", ref)
		}
		saveProjectManifest(t, project.ActiveDir(), project.Manifest{
			Slug: slug, Repo: repo, Branch: "feature-" + slug,
			PR: project.PRInfo{Number: &number},
		})
		if strings.HasPrefix(ref, "http") {
			manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
			if err != nil {
				t.Fatal(err)
			}
			manifest.PR.URL = &ref
			if err := project.Save(project.ManifestPath(project.ActiveDir(), slug), manifest); err != nil {
				t.Fatal(err)
			}
		}
	}
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
	for slug := range refs {
		item, err := p.AddItem(program.WorkItem{Title: slug, Priority: program.PriorityP1})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.DispatchItem(item.ID, slug); err != nil {
			t.Fatal(err)
		}
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
	repo := t.TempDir()
	number := 17
	saveProjectManifest(t, project.ActiveDir(), project.Manifest{
		Slug: "child", Repo: repo, Branch: "feature",
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
	repo := t.TempDir()
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
		saveProjectManifest(t, project.ActiveDir(), project.Manifest{
			Slug: slug, Repo: repo, Branch: "missing-" + slug,
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
			repo := t.TempDir()
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
	repo := t.TempDir()
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
			repo := t.TempDir()
			number := 404
			saveProjectManifest(t, project.ActiveDir(), project.Manifest{
				Slug: "child", Repo: repo, Branch: "feature", PR: project.PRInfo{Number: &number},
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
