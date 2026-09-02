package prwatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

func TestRequireLiveOwnerAcceptsExactlyOneOwner(t *testing.T) {
	owner := liveAgent("relay:demo", "pane-demo", herdr.StatusIdle)
	agents := []herdr.Agent{
		liveAgent("relay:demo-other", "pane-other", herdr.StatusIdle),
		owner,
	}
	got, err := RequireLiveOwner(agents, ModeStandalone, "demo", "demo")
	if err != nil {
		t.Fatalf("RequireLiveOwner: %v", err)
	}
	if got.PaneID != "pane-demo" {
		t.Errorf("owner pane = %q, want pane-demo", got.PaneID)
	}
}

func TestRequireLiveOwnerRefusesZeroOwners(t *testing.T) {
	// The exact shape of a `deliver-pr` sub-agent inside a stack run: the
	// surrounding pane belongs to the orchestrator, so nothing is titled for
	// the child project and a watcher started here would wake nobody.
	agents := []herdr.Agent{liveAgent("relay:stack-run", "pane-stack", herdr.StatusIdle)}
	_, err := RequireLiveOwner(agents, ModeStandalone, "demo", "demo")
	if err == nil {
		t.Fatal("RequireLiveOwner accepted a project with no live owner")
	}
	for _, want := range []string{`relay:demo`, "hand its work to nobody", "relay pr watch tick demo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

func TestRequireLiveOwnerRefusesDuplicateOwners(t *testing.T) {
	agents := []herdr.Agent{
		liveAgent("relay:demo", "pane-a", herdr.StatusIdle),
		liveAgent("relay:demo", "pane-b", herdr.StatusIdle),
	}
	_, err := RequireLiveOwner(agents, ModeStandalone, "demo", "demo")
	if err == nil {
		t.Fatal("RequireLiveOwner accepted an ambiguous owner")
	}
	for _, want := range []string{"pane-a", "pane-b", "exactly one session owns a project"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

func TestRequireLiveOwnerNamesTheOrchestratorInStackMode(t *testing.T) {
	agents := []herdr.Agent{liveAgent("relay:demo", "pane-demo", herdr.StatusIdle)}
	_, err := RequireLiveOwner(agents, ModeStack, "demo", "stack-run")
	if err == nil {
		t.Fatal("RequireLiveOwner accepted a stack watcher with no orchestrator")
	}
	if !strings.Contains(err.Error(), `relay:stack-run`) {
		t.Errorf("error %q does not name the orchestrator identity", err)
	}
}

// managedItemID is the identifier program.AddItem assigns the first work item.
const managedItemID = "w1"

// managedFixture writes a dispatched managed project and the program that owns
// it, then returns the project directory so a test can break one invariant.
func managedFixture(t *testing.T) string {
	t.Helper()
	home := withRuntimeHome(t)
	projectDir := filepath.Join(project.ActiveDir(), "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), "demo"), project.Manifest{
		Slug: "demo", Repo: filepath.Join(home, "code", "widgets"),
		Program: "atlas", ProgramItem: managedItemID,
	}); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(projectDir, "assignment.md"), []byte("# Managed work assignment\n"), 0o644,
	); err != nil {
		t.Fatalf("write assignment: %v", err)
	}

	managing, err := program.New("atlas", "Atlas", filepath.Join(home, "code", "widgets"), "copilot", 1)
	if err != nil {
		t.Fatalf("program.New: %v", err)
	}
	if err := program.Create(managing); err != nil {
		t.Fatalf("program.Create: %v", err)
	}
	item, err := managing.AddItem(program.WorkItem{
		Kind: program.ItemKindChange, Title: "Add widgets",
		Priority: program.PriorityP1, Repo: filepath.Join(home, "code", "widgets"),
	})
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if err := managing.LinkItem(item.ID, "demo"); err != nil {
		t.Fatalf("LinkItem: %v", err)
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), "atlas"), managing); err != nil {
		t.Fatalf("save program: %v", err)
	}
	return projectDir
}

func TestRequireManagedProjectAcceptsADispatchedProject(t *testing.T) {
	managedFixture(t)
	if err := RequireManagedProject("demo"); err != nil {
		t.Fatalf("RequireManagedProject: %v", err)
	}
}

func TestRequireManagedProjectRefusesAMissingAssignment(t *testing.T) {
	projectDir := managedFixture(t)
	if err := os.Remove(filepath.Join(projectDir, "assignment.md")); err != nil {
		t.Fatalf("remove assignment: %v", err)
	}
	err := RequireManagedProject("demo")
	if err == nil || !strings.Contains(err.Error(), "no readable assignment") {
		t.Fatalf("RequireManagedProject = %v, want a missing assignment", err)
	}
}

func TestRequireManagedProjectRefusesAnEmptyAssignment(t *testing.T) {
	projectDir := managedFixture(t)
	if err := os.WriteFile(filepath.Join(projectDir, "assignment.md"), nil, 0o644); err != nil {
		t.Fatalf("truncate assignment: %v", err)
	}
	err := RequireManagedProject("demo")
	if err == nil || !strings.Contains(err.Error(), "unusable assignment") {
		t.Fatalf("RequireManagedProject = %v, want an unusable assignment", err)
	}
}

func TestRequireManagedProjectRefusesAnUnmanagedProject(t *testing.T) {
	managedFixture(t)
	if err := project.Save(project.ManifestPath(project.ActiveDir(), "demo"), project.Manifest{
		Slug: "demo", Repo: t.TempDir(),
	}); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	err := RequireManagedProject("demo")
	if err == nil || !strings.Contains(err.Error(), "is not a managed project") {
		t.Fatalf("RequireManagedProject = %v, want an unmanaged project rejection", err)
	}
}

func TestRequireManagedProjectRefusesAnUnknownProgram(t *testing.T) {
	managedFixture(t)
	if err := os.RemoveAll(filepath.Join(program.ActiveDir(), "atlas")); err != nil {
		t.Fatalf("remove program: %v", err)
	}
	err := RequireManagedProject("demo")
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("RequireManagedProject = %v, want a missing program", err)
	}
}

func TestRequireManagedProjectRefusesAnUnknownWorkItem(t *testing.T) {
	managedFixture(t)
	if err := project.Save(project.ManifestPath(project.ActiveDir(), "demo"), project.Manifest{
		Slug: "demo", Repo: t.TempDir(), Program: "atlas", ProgramItem: "item-404",
	}); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	err := RequireManagedProject("demo")
	if err == nil || !strings.Contains(err.Error(), "does not have") {
		t.Fatalf("RequireManagedProject = %v, want an unknown work item", err)
	}
}

func TestRequireManagedProjectRefusesAWorkItemForAnotherProject(t *testing.T) {
	managedFixture(t)
	managing, err := program.Load(program.ManifestPath(program.ActiveDir(), "atlas"))
	if err != nil {
		t.Fatalf("load program: %v", err)
	}
	for i := range managing.Items {
		managing.Items[i].ProjectSlug = "other-project"
	}
	if err := program.Save(program.ManifestPath(program.ActiveDir(), "atlas"), managing); err != nil {
		t.Fatalf("save program: %v", err)
	}
	err = RequireManagedProject("demo")
	if err == nil || !strings.Contains(err.Error(), "wake the worker that owns this exact project") {
		t.Fatalf("RequireManagedProject = %v, want a cross-project rejection", err)
	}
}
