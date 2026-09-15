package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/project"
)

// newTestRepo creates a throwaway git repo with one commit on main and returns
// its root.
func newTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
			"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "relay")
	run("config", "user.email", "relay@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README")
	run("commit", "-q", "-m", "init")
	return repo
}

func TestRunNewCreatesExpectedProjectFilesWithoutLaunch(t *testing.T) {
	repo := newTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	if err := config.Save(config.Config{
		BranchPrefix: "test/",
		DefaultAgent: "copilot",
		PermissionModes: map[string]string{
			"copilot": "allow-all",
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := runNew(newOpts{task: "demo task", name: "demo", noLaunch: true}); err != nil {
		t.Fatalf("runNew: %v", err)
	}
	projectDir := filepath.Join(project.ActiveDir(), "demo")
	for name, want := range map[string]string{
		"task.md":  "# Task\n\ndemo task\n",
		"notes.md": "# demo — Notes\n\nScratchpad for ideas, context, and observations.\n",
		"todos.md": "# demo — TODOs\n\n- [ ] ...\n",
	} {
		got, err := os.ReadFile(filepath.Join(projectDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	manifest, err := project.Load(filepath.Join(projectDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest.PhasesCompleted, []string{"init"}) ||
		!reflect.DeepEqual(manifest.PhasesRemaining, project.AllPhases) ||
		manifest.Slug != "demo" || manifest.Title != "demo task" ||
		manifest.Agent != "copilot" || manifest.Workflow != defaultWorkflow ||
		manifest.Status != "initialized" || manifest.Phase != "plan" {
		t.Fatalf("manifest = %+v", manifest)
	}
}

func TestCreateProjectRejectsSlugAfterConcurrentArchive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "concurrent-recreate"
	branch := "test/" + slug
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	if err := config.Save(config.Config{
		BranchPrefix: "test/",
		DefaultAgent: "copilot",
		PermissionModes: map[string]string{
			"copilot": "allow-all",
		},
	}); err != nil {
		t.Fatal(err)
	}

	removalStarted := make(chan struct{})
	continueRemoval := make(chan struct{})
	previous := archiveWorktreeRemove
	archiveWorktreeRemove = func(
		repo, worktree string, expected gitx.WorktreeState, force bool,
	) error {
		close(removalStarted)
		<-continueRemoval
		return previous(repo, worktree, expected, force)
	}
	t.Cleanup(func() { archiveWorktreeRemove = previous })

	archiveErr := make(chan error, 1)
	go func() {
		_, err := archiveProject(slug, true)
		archiveErr <- err
	}()
	<-removalStarted

	createErr := make(chan error, 1)
	go func() {
		_, err := createProject(projectCreateOpts{
			task: "replacement project", name: slug, repo: repo,
		})
		createErr <- err
	}()
	close(continueRemoval)
	if err := <-archiveErr; err != nil {
		t.Fatalf("archiveProject: %v", err)
	}
	if err := <-createErr; err == nil || !strings.Contains(err.Error(), "archived metadata still exists") {
		t.Fatalf("createProject error = %v, want archived slug rejection", err)
	}
	if _, err := project.Load(project.ManifestPath(project.ArchivedDir(), slug)); err != nil {
		t.Fatalf("load archived predecessor manifest: %v", err)
	}
}

func TestCreateProjectRejectsAnyArchivedMetadataDirectory(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "directory only"},
		{name: "malformed manifest", content: "{"},
		{name: "legacy manifest", content: `{"slug":"archived-slug"}`},
		{name: "completed cleanup", content: `{"slug":"archived-slug","archive_cleanup":{"branch_state":"done","worktree_state":"done"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			slug := "archived-slug"
			archivedDir := filepath.Join(project.ArchivedDir(), slug)
			if err := os.MkdirAll(archivedDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if test.content != "" {
				if err := os.WriteFile(
					filepath.Join(archivedDir, "manifest.json"), []byte(test.content), 0o644,
				); err != nil {
					t.Fatal(err)
				}
			}

			_, err := createProject(projectCreateOpts{
				task: "replacement project", name: slug, repo: t.TempDir(),
			})
			if err == nil {
				t.Fatal("createProject succeeded with archived metadata")
			}
			for _, want := range []string{
				"archived metadata still exists",
				archivedDir,
				"choose a different project name",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("createProject error %q is missing %q", err, want)
				}
			}
			if pathExists(filepath.Join(project.ActiveDir(), slug)) {
				t.Fatal("blocked creation installed an active project")
			}
		})
	}
}

func TestRunNewPersistsForcedFullDeliveryMode(t *testing.T) {
	repo := newTestRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(repo)
	if err := config.Save(config.Config{
		BranchPrefix: "test/", DefaultAgent: "copilot",
		PermissionModes: map[string]string{"copilot": "allow-all"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := runNew(newOpts{task: "full task", name: "full", full: true, noLaunch: true}); err != nil {
		t.Fatal(err)
	}
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), "full"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.DeliveryMode != project.DeliveryModeFull {
		t.Fatalf("delivery mode = %q, want full", manifest.DeliveryMode)
	}
}

func TestRunNewQuickAliasPersistsAdaptiveDeliveryMode(t *testing.T) {
	repo := newTestRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(repo)
	if err := config.Save(config.Config{
		BranchPrefix: "test/", DefaultAgent: "copilot",
		PermissionModes: map[string]string{"copilot": "allow-all"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := runNew(newOpts{task: "quick task", name: "quick", quick: true, noLaunch: true}); err != nil {
		t.Fatal(err)
	}
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), "quick"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.DeliveryMode != project.DeliveryModeAdaptive {
		t.Fatalf("delivery mode = %q, want adaptive", manifest.DeliveryMode)
	}
}

func TestFreshStackProjectCanLogProgressWithoutWorkflowState(t *testing.T) {
	repo := newTestRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(repo)
	if err := config.Save(config.Config{
		BranchPrefix: "test/", DefaultAgent: "copilot",
		PermissionModes: map[string]string{"copilot": "allow-all"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := runNew(newOpts{
		task: "stack task", name: "stack", workflow: "stack-ship", noLaunch: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "log", "stack", "planned first child"); err != nil {
		t.Fatalf("fresh stack progress log: %v", err)
	}
	data, err := os.ReadFile(project.ProgressPath("stack"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "planned first child") {
		t.Fatalf("progress = %q", data)
	}
}

func TestRunNewCreatesStackChildFromExplicitParentBase(t *testing.T) {
	repo := newTestRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(repo)
	if err := config.Save(config.Config{
		BranchPrefix: "test/", DefaultAgent: "copilot",
		PermissionModes: map[string]string{"copilot": "allow-all"},
	}); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "checkout", "-q", "-b", "stack/parent").CombinedOutput(); err != nil {
		t.Fatalf("create parent branch: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "parent.txt"), []byte("parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "add", "parent.txt").CombinedOutput(); err != nil {
		t.Fatalf("add parent change: %v\n%s", err, out)
	}
	command := exec.Command("git", "-C", repo, "commit", "-q", "-m", "parent")
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
		"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
	)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("commit parent change: %v\n%s", err, out)
	}
	parentSHA := gitx.RevParse(repo, "stack/parent")
	if err := runNew(newOpts{
		task: "child task", name: "child", base: "stack/parent", noLaunch: true,
	}); err != nil {
		t.Fatal(err)
	}
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), "child"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.BaseBranch != "stack/parent" || manifest.StartSHA != parentSHA ||
		manifest.Worktree == nil || gitx.RevParse(*manifest.Worktree, "HEAD") != parentSHA {
		t.Fatalf("stack child manifest = %+v", manifest)
	}
}

func TestReclaimLeftoversRemovesBranchWorktreeAndDir(t *testing.T) {
	repo := newTestRepo(t)
	worktreeDir := filepath.Join(repo, ".worktrees", "wt")
	branch := "user/demo"
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", worktreeDir, "-b", branch, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	projDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projDir, "task.md"), []byte("x"), 0644); err != nil {
		t.Fatalf("seed projDir: %v", err)
	}

	if err := reclaimLeftovers(repo, branch, worktreeDir, projDir); err != nil {
		t.Fatalf("reclaimLeftovers: %v", err)
	}
	if gitx.BranchExists(repo, branch) {
		t.Errorf("branch %q still exists after reclaim", branch)
	}
	if pathExists(worktreeDir) {
		t.Errorf("worktree dir still present after reclaim")
	}
	if pathExists(projDir) {
		t.Errorf("project dir still present after reclaim")
	}
}

func TestReclaimLeftoversHandlesUnregisteredWorktree(t *testing.T) {
	repo := newTestRepo(t)
	// A leftover directory that was never registered as a worktree, plus a
	// branch — mimics an interrupted setup.
	worktreeDir := filepath.Join(repo, ".worktrees", "orphan")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}
	branch := "user/orphan"
	if out, err := exec.Command("git", "-C", repo, "branch", branch).CombinedOutput(); err != nil {
		t.Fatalf("create branch: %v\n%s", err, out)
	}

	if err := reclaimLeftovers(repo, branch, worktreeDir, ""); err != nil {
		t.Fatalf("reclaimLeftovers: %v", err)
	}
	if gitx.BranchExists(repo, branch) {
		t.Errorf("branch %q still exists after reclaim", branch)
	}
	if pathExists(worktreeDir) {
		t.Errorf("orphan dir still present after reclaim")
	}
}

func TestReclaimLeftoversRemovesDanglingWorktreeSymlink(t *testing.T) {
	repo := newTestRepo(t)
	worktreeDir := filepath.Join(repo, ".worktrees", "dangling")
	if err := os.MkdirAll(filepath.Dir(worktreeDir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), worktreeDir); err != nil {
		t.Fatal(err)
	}

	if err := reclaimLeftovers(repo, "user/missing", worktreeDir, ""); err != nil {
		t.Fatalf("reclaimLeftovers: %v", err)
	}
	if _, err := os.Lstat(worktreeDir); !os.IsNotExist(err) {
		t.Fatalf("dangling worktree symlink still exists: %v", err)
	}
}

func TestReclaimLeftoversReturnsBranchProbeFailure(t *testing.T) {
	repo := t.TempDir()

	err := reclaimLeftovers(repo, "user/feature", "", "")
	if err == nil {
		t.Fatal("reclaimLeftovers error = nil")
	}
	for _, want := range []string{"inspect reclaim branch", "not a git repository"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("reclaimLeftovers error %q is missing %q", err, want)
		}
	}
}

func TestBranchMerged(t *testing.T) {
	repo := newTestRepo(t)
	// A branch at HEAD is reachable from main (merged / no unique work).
	if out, err := exec.Command("git", "-C", repo, "branch", "merged", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("branch merged: %v\n%s", err, out)
	}
	if !branchMerged(repo, "merged", "main") {
		t.Error("branchMerged(merged) = false, want true")
	}

	// A branch with an extra commit is NOT reachable from main.
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", filepath.Join(repo, ".worktrees", "u"), "-b", "unmerged", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("worktree add unmerged: %v\n%s", err, out)
	}
	wt := filepath.Join(repo, ".worktrees", "u")
	if err := os.WriteFile(filepath.Join(wt, "new.txt"), []byte("y"), 0644); err != nil {
		t.Fatalf("write new file: %v", err)
	}
	commit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", wt}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
			"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit("add", "new.txt")
	commit("commit", "-q", "-m", "extra")
	if branchMerged(repo, "unmerged", "main") {
		t.Error("branchMerged(unmerged) = true, want false")
	}
}
