package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/project"
)

func TestArchiveRejectsNonGeneratedAgentsMDWithoutForce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "user-agents"
	branch := "user/user-agents"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	writeArchiveFile(t, worktree, "AGENTS.md", "# project\n\nPlease keep this.\n")
	manifestPath := project.ManifestPath(project.ActiveDir(), slug)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = captureStdout(t, func() error {
		return runArchive(slug, false)
	})
	if err == nil {
		t.Fatalf("runArchive succeeded, want non-generated AGENTS.md to be preserved")
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("active manifest changed during rollback: data=%q err=%v", after, readErr)
	}
}

func TestArchiveRollsBackAfterRealWorktreeRemovalFailureAndRetries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "real-worktree-removal-failure"
	branch := "user/real-worktree-removal-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	writeArchiveFile(t, worktree, "untracked.txt", "preserve until forced\n")
	manifestPath := project.ManifestPath(project.ActiveDir(), slug)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	previous := saveArchiveManifest
	sawPending, sawClaimed := false, false
	saveArchiveManifest = func(path string, manifest project.Manifest) error {
		if manifest.ArchiveCleanup != nil {
			sawPending = sawPending ||
				manifest.ArchiveCleanup.WorktreeState == project.ArchiveCleanupPending
			sawClaimed = sawClaimed ||
				manifest.ArchiveCleanup.WorktreeState == project.ArchiveCleanupClaimed
		}
		return project.Save(path, manifest)
	}
	t.Cleanup(func() { saveArchiveManifest = previous })

	result, err := archiveProject(slug, false)
	if err == nil || !strings.Contains(err.Error(), "use --force") {
		t.Fatalf("archiveProject error = %v, want real dirty-worktree removal failure", err)
	}
	if !sawPending || !sawClaimed {
		t.Fatalf("metadata staging states = pending:%t claimed:%t, want both", sawPending, sawClaimed)
	}
	if result.ArchivedPath != "" || result.WorktreeRemoved || result.BranchDeleted {
		t.Fatalf("archive result = %+v, want fully rolled-back metadata", result)
	}
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("active manifest changed during rollback: data=%q err=%v", after, readErr)
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)

	retried, err := archiveProject(slug, true)
	if err != nil {
		t.Fatalf("forced retry: %v", err)
	}
	if !retried.WorktreeRemoved || !retried.BranchDeleted {
		t.Fatalf("forced retry result = %+v, want complete cleanup", retried)
	}
}

func TestArchivePreservesProjectWhenArchivedDirectoryCannotBeCreated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archive-dir-failure"
	branch := "user/archive-dir-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	if err := os.WriteFile(project.ArchivedDir(), []byte("not a directory\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := captureStdout(t, func() error {
		return runArchive(slug, true)
	})
	if err == nil || !strings.Contains(err.Error(), "create archived dir") {
		t.Fatalf("runArchive error = %v, want archived directory creation failure", err)
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
}

func TestArchivePreservesProjectWhenMoveToArchivedFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archive-move-failure"
	branch := "user/archive-move-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	if err := os.MkdirAll(project.ArchivedDir(), 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(project.ArchivedDir(), 0755); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore archived directory permissions: %v", err)
		}
	})

	_, err := captureStdout(t, func() error {
		return runArchive(slug, true)
	})
	if err == nil || !strings.Contains(err.Error(), "move project to archived") {
		t.Fatalf("runArchive error = %v, want project move failure", err)
	}
	if chmodErr := os.Chmod(project.ArchivedDir(), 0755); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
}

func TestArchiveResultReportsActiveLocationForDestinationCollision(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archive-destination-collision"
	branch := "user/archive-destination-collision"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	srcDir := filepath.Join(project.ActiveDir(), slug)
	dstDir := filepath.Join(project.ArchivedDir(), slug)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := archiveProject(slug, true)
	if err == nil || !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("archiveProject error = %v, want destination collision", err)
	}
	if result.ProjectLocation != archiveLocationActive ||
		result.ProjectPath != srcDir ||
		result.MetadataTransition != archiveTransitionNone ||
		result.ArchivedPath != "" {
		t.Fatalf("archive result = %+v, want unchanged active location", result)
	}
}

func TestArchivePreservesProjectWhenArchivedManifestCannotBeStaged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archive-manifest-failure"
	branch := "user/archive-manifest-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	previous := saveArchiveManifest
	saveArchiveManifest = func(string, project.Manifest) error {
		return errors.New("injected manifest save failure")
	}
	t.Cleanup(func() { saveArchiveManifest = previous })

	_, err := captureStdout(t, func() error {
		return runArchive(slug, true)
	})
	if err == nil || !strings.Contains(err.Error(), "injected manifest save failure") {
		t.Fatalf("runArchive error = %v, want manifest staging failure", err)
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
}

func TestArchiveResultReportsActiveLocationWhenManifestRestoreFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "active-manifest-restore-failure"
	branch := "user/active-manifest-restore-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	srcDir := filepath.Join(project.ActiveDir(), slug)

	previousSave := saveArchiveManifest
	saveArchiveManifest = func(path string, manifest project.Manifest) error {
		if manifest.ArchiveCleanup != nil &&
			manifest.ArchiveCleanup.WorktreeState == project.ArchiveCleanupClaimed {
			return errors.New("injected cleanup claim save failure")
		}
		return project.Save(path, manifest)
	}
	previousRename := archiveRename
	archiveRename = func(oldPath, newPath string) error {
		if strings.HasPrefix(filepath.Base(oldPath), ".manifest.active-") &&
			filepath.Base(newPath) == "manifest.json" {
			return errors.New("injected active manifest restore failure")
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		saveArchiveManifest = previousSave
		archiveRename = previousRename
	})

	result, err := archiveProject(slug, true)
	if err == nil || !strings.Contains(err.Error(), "restore active manifest") {
		t.Fatalf("archiveProject error = %v, want active manifest restore failure", err)
	}
	if result.ProjectLocation != archiveLocationActive ||
		result.ProjectPath != srcDir ||
		result.MetadataTransition != archiveTransitionRollbackIncomplete ||
		result.ArchivedPath != "" {
		t.Fatalf("archive result = %+v, want active rollback-incomplete location", result)
	}
	if !pathExists(srcDir) || pathExists(filepath.Join(project.ArchivedDir(), slug)) {
		t.Fatal("archive result does not match the active project directory")
	}
}

func TestArchiveDoesNotRemoveWorktreeWhenCleanupClaimCannotBeSaved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "worktree-claim-save-failure"
	branch := "user/worktree-claim-save-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	previous := saveArchiveManifest
	saveArchiveManifest = func(path string, manifest project.Manifest) error {
		if manifest.ArchiveCleanup != nil &&
			manifest.ArchiveCleanup.WorktreeState == project.ArchiveCleanupClaimed {
			return errors.New("injected cleanup claim save failure")
		}
		return project.Save(path, manifest)
	}
	t.Cleanup(func() { saveArchiveManifest = previous })

	result, err := archiveProject(slug, true)
	if err == nil || !strings.Contains(err.Error(), "injected cleanup claim save failure") {
		t.Fatalf("archiveProject error = %v, want cleanup claim save failure", err)
	}
	if result.WorktreeRemoved || result.BranchDeleted {
		t.Fatalf("archive result = %+v, want no destructive cleanup", result)
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
}

func TestArchiveDoesNotInvokeWorktreeRemovalAfterAbsentProof(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "worktree-appeared-after-proof"
	branch := "user/worktree-appeared-after-proof"
	worktree := filepath.Join(repo, ".worktrees", slug)
	runArchiveGit(t, repo, "branch", branch, "HEAD")
	writeArchiveManifest(t, slug, repo, branch, worktree)

	previous := archiveWorktreeRemove
	removeCalls := 0
	archiveWorktreeRemove = func(string, string, gitx.WorktreeState, bool) error {
		removeCalls++
		return errors.New("destructive removal invoked for absent proof")
	}
	t.Cleanup(func() { archiveWorktreeRemove = previous })

	result, err := archiveProject(slug, true)
	if err != nil {
		t.Fatalf("archiveProject: %v", err)
	}
	if removeCalls != 0 || result.WorktreeRemoved {
		t.Fatalf("archive result = %+v, want absent-proof worktree preserved", result)
	}
}

func TestArchiveReturnsPartialResultWhenWorktreeCompletionCannotBeSaved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "worktree-completion-save-failure"
	branch := "user/worktree-completion-save-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	previous := saveArchiveManifest
	saveArchiveManifest = func(path string, manifest project.Manifest) error {
		if manifest.ArchiveCleanup != nil &&
			manifest.ArchiveCleanup.WorktreeState == project.ArchiveCleanupDone &&
			manifest.ArchiveCleanup.BranchState == project.ArchiveCleanupPending {
			return errors.New("injected cleanup completion save failure")
		}
		return project.Save(path, manifest)
	}
	t.Cleanup(func() { saveArchiveManifest = previous })

	result, err := archiveProject(slug, true)
	if err == nil || !strings.Contains(err.Error(), "injected cleanup completion save failure") {
		t.Fatalf("archiveProject error = %v, want cleanup completion save failure", err)
	}
	if !result.WorktreeRemoved || result.BranchDeleted ||
		result.ArchivedPath != filepath.Join(project.ArchivedDir(), slug) {
		t.Fatalf("archive result = %+v, want archived project with removed worktree", result)
	}
	if pathExists(worktree) {
		t.Fatal("worktree completion failure reported a removed worktree as present")
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(filepath.Join(project.ArchivedDir(), slug)) {
		t.Fatal("post-delete save failure did not leave project metadata archived")
	}
	archived := loadArchivedManifest(t, slug)
	if archived.ArchiveCleanup == nil ||
		archived.ArchiveCleanup.WorktreeState != project.ArchiveCleanupClaimed {
		t.Fatalf("cleanup proof = %+v, want durable claimed worktree state", archived.ArchiveCleanup)
	}
}

func TestArchiveDoesNotDeleteBranchWhenCleanupClaimCannotBeSaved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "branch-claim-save-failure"
	branch := "user/branch-claim-save-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	previous := saveArchiveManifest
	saveArchiveManifest = func(path string, manifest project.Manifest) error {
		if manifest.ArchiveCleanup != nil &&
			manifest.ArchiveCleanup.BranchState == project.ArchiveCleanupClaimed {
			return errors.New("injected branch claim save failure")
		}
		return project.Save(path, manifest)
	}
	t.Cleanup(func() { saveArchiveManifest = previous })

	result, err := archiveProject(slug, true)
	if err == nil || !strings.Contains(err.Error(), "injected branch claim save failure") {
		t.Fatalf("archiveProject error = %v, want branch claim save failure", err)
	}
	if !result.WorktreeRemoved || result.BranchDeleted {
		t.Fatalf("archive result = %+v, want worktree-only cleanup", result)
	}
	if !gitx.BranchExists(repo, branch) {
		t.Fatal("archive deleted the branch without persisting its cleanup claim")
	}
	archived := loadArchivedManifest(t, slug)
	if archived.ArchiveCleanup == nil ||
		archived.ArchiveCleanup.BranchState != project.ArchiveCleanupPending {
		t.Fatalf("cleanup proof = %+v, want pending branch state", archived.ArchiveCleanup)
	}
}

func TestArchiveReturnsPartialResultWhenBranchCompletionCannotBeSaved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "branch-completion-save-failure"
	branch := "user/branch-completion-save-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	previous := saveArchiveManifest
	saveArchiveManifest = func(path string, manifest project.Manifest) error {
		if manifest.ArchiveCleanup != nil &&
			manifest.ArchiveCleanup.BranchState == project.ArchiveCleanupDone {
			return errors.New("injected branch completion save failure")
		}
		return project.Save(path, manifest)
	}
	t.Cleanup(func() { saveArchiveManifest = previous })

	result, err := archiveProject(slug, true)
	if err == nil || !strings.Contains(err.Error(), "injected branch completion save failure") {
		t.Fatalf("archiveProject error = %v, want branch completion save failure", err)
	}
	if !result.WorktreeRemoved || !result.BranchDeleted {
		t.Fatalf("archive result = %+v, want both resources removed", result)
	}
	if gitx.BranchExists(repo, branch) {
		t.Fatal("branch completion failure reported a deleted branch as present")
	}
	archived := loadArchivedManifest(t, slug)
	if archived.ArchiveCleanup == nil ||
		archived.ArchiveCleanup.BranchState != project.ArchiveCleanupClaimed {
		t.Fatalf("cleanup proof = %+v, want durable claimed branch state", archived.ArchiveCleanup)
	}
}

func TestArchiveRemovesDeletedBranchConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "branch-config-cleanup"
	branch := "user/branch-config-cleanup"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	runArchiveGit(t, repo, "config", "--local", "branch."+branch+".remote", "origin")

	result, err := archiveProject(slug, true)
	if err != nil {
		t.Fatalf("archiveProject: %v", err)
	}
	if !result.BranchDeleted {
		t.Fatalf("archive result = %+v, want deleted branch", result)
	}
	cmd := exec.Command("git", "-C", repo, "config", "--local", "--get", "branch."+branch+".remote")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("branch config survived archive: %s", out)
	}
}

func TestArchiveReportsConfigFailureAfterBranchRefDeletion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "branch-config-failure"
	branch := "user/branch-config-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	runArchiveGit(t, repo, "config", "--local", "branch."+branch+".remote", "origin")
	previous := archiveForceDeleteBranchAt
	archiveForceDeleteBranchAt = func(repo, branch, expectedSHA string) error {
		runArchiveGit(t, repo, "update-ref", "-d", "refs/heads/"+branch, expectedSHA)
		return errors.New("injected branch config cleanup failure")
	}
	t.Cleanup(func() { archiveForceDeleteBranchAt = previous })

	result, err := archiveProject(slug, true)
	if err != nil {
		t.Fatalf("archiveProject: %v", err)
	}
	if !result.BranchDeleted || !strings.Contains(
		result.BranchDeletionWarning, "injected branch config cleanup failure",
	) || !strings.Contains(result.BranchDeletionWarning, "config --local --remove-section") {
		t.Fatalf("archive result = %+v, want partial branch config cleanup", result)
	}
	if gitx.BranchExists(repo, branch) {
		t.Fatal("archive recreated the branch ref after config cleanup failed")
	}
	archived := loadArchivedManifest(t, slug)
	if archived.ArchiveCleanup == nil ||
		archived.ArchiveCleanup.BranchState != project.ArchiveCleanupClaimed {
		t.Fatalf("cleanup proof = %+v, want claimed branch config cleanup", archived.ArchiveCleanup)
	}

	archiveForceDeleteBranchAt = gitx.ForceDeleteBranchAt
	retried, err := retryArchivedProjectCleanup(archived)
	if err != nil {
		t.Fatalf("retryArchivedProjectCleanup: %v", err)
	}
	if retried.BranchDeleted {
		t.Fatalf("retry replayed branch ref deletion: %+v", retried)
	}
	if gitx.BranchExists(repo, branch) {
		t.Fatal("retry recreated the deleted branch ref")
	}
	cmd := exec.Command("git", "-C", repo, "config", "--local", "--get", "branch."+branch+".remote")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("branch config survived retry: %s", out)
	}
}

func TestArchiveRestoresActiveProjectWhenArchivedManifestInstallFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archive-manifest-install-failure"
	branch := "user/archive-manifest-install-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifestPath := project.ManifestPath(project.ActiveDir(), slug)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	previous := archiveRename
	archiveRename = func(oldPath, newPath string) error {
		if strings.HasPrefix(filepath.Base(oldPath), ".manifest.archived-") &&
			filepath.Base(newPath) == "manifest.json" {
			return errors.New("injected archived manifest install failure")
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() { archiveRename = previous })

	_, err = captureStdout(t, func() error {
		return runArchive(slug, true)
	})
	if err == nil || !strings.Contains(err.Error(), "injected archived manifest install failure") {
		t.Fatalf("runArchive error = %v, want archived manifest install failure", err)
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("active manifest changed during rollback: data=%q err=%v", after, readErr)
	}
}

func TestArchiveReturnsArchivedPathWhenManifestInstallRollbackFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archive-manifest-install-rollback-failure"
	branch := "user/archive-manifest-install-rollback-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	srcDir := filepath.Join(project.ActiveDir(), slug)
	dstDir := filepath.Join(project.ArchivedDir(), slug)

	previous := archiveRename
	archiveRename = func(oldPath, newPath string) error {
		switch {
		case strings.HasPrefix(filepath.Base(oldPath), ".manifest.archived-") &&
			filepath.Base(newPath) == "manifest.json":
			return errors.New("injected archived manifest install failure")
		case oldPath == dstDir && newPath == srcDir:
			return errors.New("injected directory restore failure")
		default:
			return os.Rename(oldPath, newPath)
		}
	}
	t.Cleanup(func() { archiveRename = previous })

	result, err := archiveProject(slug, true)
	if err == nil || !strings.Contains(err.Error(), "rollback project directory") {
		t.Fatalf("archiveProject error = %v, want manifest install rollback failure", err)
	}
	if result.ArchivedPath != dstDir {
		t.Fatalf("archive result = %+v, want archived path %q", result, dstDir)
	}
	if pathExists(srcDir) || !pathExists(dstDir) {
		t.Fatal("archive result did not match metadata left in archived location")
	}
}

func TestArchiveRejectsInvalidRequestedSlugWithoutChangingVictim(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "victim"
	branch := "user/victim"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifestPath := project.ManifestPath(project.ActiveDir(), slug)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = captureStdout(t, func() error {
		return runArchive("nested/../victim", true)
	})
	if err == nil || !strings.Contains(err.Error(), "invalid slug") {
		t.Fatalf("runArchive error = %v, want invalid requested slug rejection", err)
	}
	assertArchiveManifestAndResourcesUnchanged(t, manifestPath, before, repo, branch, worktree)
}

func TestArchiveRejectsManifestSlugMismatchWithoutChangingVictim(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	victimSlug := "victim"
	victimBranch := "user/victim"
	victimWorktree := addArchiveWorktree(t, repo, victimSlug, victimBranch)
	writeArchiveManifest(t, victimSlug, repo, victimBranch, victimWorktree)
	victimPath := project.ManifestPath(project.ActiveDir(), victimSlug)
	before, err := os.ReadFile(victimPath)
	if err != nil {
		t.Fatal(err)
	}

	attackerSlug := "a-attacker"
	attackerDir := filepath.Join(project.ActiveDir(), attackerSlug)
	if err := os.MkdirAll(attackerDir, 0755); err != nil {
		t.Fatal(err)
	}
	victim, err := project.Load(victimPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Save(filepath.Join(attackerDir, "manifest.json"), victim); err != nil {
		t.Fatal(err)
	}

	_, err = captureStdout(t, func() error {
		return runArchive(attackerSlug, true)
	})
	if err == nil || !strings.Contains(err.Error(), `slug "victim" does not match requested slug "a-attacker"`) {
		t.Fatalf("runArchive error = %v, want manifest slug mismatch rejection", err)
	}
	assertArchiveManifestAndResourcesUnchanged(t, victimPath, before, repo, victimBranch, victimWorktree)
	if !pathExists(attackerDir) {
		t.Fatal("archive removed the mismatched project directory")
	}
}

func TestArchiveForcePreservesWorktreeFromStaleManifest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "stale-worktree"
	branch := "user/stale-worktree"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	runArchiveGit(t, repo, "worktree", "remove", "--force", worktree)
	runArchiveGit(t, repo, "branch", "-D", branch)

	victimBranch := "user/victim"
	runArchiveGit(t, repo, "worktree", "add", "-q", worktree, "-b", victimBranch, "main")

	_, err := archiveProject(slug, true)
	if err == nil || !strings.Contains(err.Error(), "attached to") ||
		!strings.Contains(err.Error(), "refs/heads/"+branch) {
		t.Fatalf("archiveProject error = %v, want stale worktree rejection", err)
	}
	if !pathExists(worktree) || !gitx.BranchExists(repo, victimBranch) {
		t.Fatal("forced archive removed the worktree or branch belonging to another project")
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("forced archive moved stale project metadata")
	}
}

func TestArchiveRechecksWorktreeIdentityImmediatelyBeforeRemoval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "worktree-replaced-before-removal"
	branch := "user/worktree-replaced-before-removal"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	victimBranch := "user/replacement-worktree"

	previous := archiveWorktreeRemove
	archiveWorktreeRemove = func(
		repo, worktree string, expected gitx.WorktreeState, force bool,
	) error {
		runArchiveGit(t, repo, "worktree", "remove", "--force", worktree)
		runArchiveGit(t, repo, "worktree", "add", "-q", worktree, "-b", victimBranch, "main")
		return gitx.WorktreeRemoveAt(repo, worktree, expected, force)
	}
	t.Cleanup(func() { archiveWorktreeRemove = previous })

	_, err := archiveProject(slug, true)
	if err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("archiveProject error = %v, want replacement worktree rejection", err)
	}
	state, found, stateErr := gitx.RegisteredWorktreeState(repo, worktree)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if !found || state.Branch != "refs/heads/"+victimBranch {
		t.Fatalf("replacement worktree state = %+v, found = %t", state, found)
	}
}

func TestArchiveForceRejectsDetachedWorktreeWithoutAuthoritativeCommit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "detached-without-proof"
	branch := "user/detached-without-proof"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	runArchiveGit(t, worktree, "checkout", "-q", "--detach")
	runArchiveGit(t, repo, "branch", "-D", branch)

	_, err := archiveProject(slug, true)
	if err == nil || !strings.Contains(err.Error(), "authoritative project commit") {
		t.Fatalf("archiveProject error = %v, want detached worktree proof rejection", err)
	}
	if !pathExists(worktree) {
		t.Fatal("forced archive removed a detached worktree without authoritative proof")
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("forced archive moved project metadata before validating detached ownership")
	}
}

func TestArchiveRejectsDanglingWorktreeSymlink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "dangling-worktree"
	branch := "user/dangling-worktree"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	runArchiveGit(t, repo, "worktree", "remove", "--force", worktree)
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), worktree); err != nil {
		t.Fatal(err)
	}

	_, err := captureStdout(t, func() error {
		return runArchive(slug, true)
	})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("runArchive error = %v, want dangling symlink rejection", err)
	}
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("archive moved project metadata after rejecting dangling symlink")
	}
	if _, statErr := os.Lstat(worktree); statErr != nil {
		t.Fatalf("archive removed dangling symlink: %v", statErr)
	}
}

func TestArchivePreservesProjectWhenBranchProbeFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "branch-probe-failure"
	branch := "user/branch-probe-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	previous := archiveBranchExists
	archiveBranchExists = func(string, string) (bool, error) {
		return false, errors.New("git branch probe failed")
	}
	t.Cleanup(func() { archiveBranchExists = previous })

	_, err := captureStdout(t, func() error {
		return runArchive(slug, true)
	})
	if err == nil || !strings.Contains(err.Error(), "git branch probe failed") {
		t.Fatalf("runArchive error = %v, want branch probe failure", err)
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
}

func TestArchiveProofRejectsManifestChangeBeforeCleanup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "manifest-proof-change"
	branch := "user/manifest-proof-change"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifestPath := project.ManifestPath(project.ActiveDir(), slug)
	manifest, err := project.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decideArchive(manifest, slug, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Title = "changed after proof"
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}

	_, err = archiveProjectWithProof(decision.proof, true)
	if err == nil || !strings.Contains(err.Error(), "manifest changed") {
		t.Fatalf("archiveProjectWithProof error = %v, want stale manifest rejection", err)
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
}

func TestArchiveProofReportsArchivedLocationAfterConcurrentArchive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "concurrent-archive-result"
	branch := "user/concurrent-archive-result"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decideArchive(manifest, slug, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archiveProject(slug, true); err != nil {
		t.Fatalf("concurrent archive: %v", err)
	}

	result, err := archiveProjectWithProof(decision.proof, true)
	if err == nil || !strings.Contains(err.Error(), "project not found in active") {
		t.Fatalf("archiveProjectWithProof error = %v, want stale active proof rejection", err)
	}
	archivedPath := filepath.Join(project.ArchivedDir(), slug)
	if result.ProjectLocation != archiveLocationArchived ||
		result.ProjectPath != archivedPath ||
		result.ArchivedPath != archivedPath {
		t.Fatalf("archive result = %+v, want final archived location %q", result, archivedPath)
	}
}

func TestArchiveProofReportsUnknownWhenProjectLocationCannotBeFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "missing-archive-result"
	branch := "user/missing-archive-result"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decideArchive(manifest, slug, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(project.ActiveDir(), slug)); err != nil {
		t.Fatal(err)
	}

	result, err := archiveProjectWithProof(decision.proof, true)
	if err == nil || !strings.Contains(err.Error(), "project not found in active") {
		t.Fatalf("archiveProjectWithProof error = %v, want missing active proof rejection", err)
	}
	if result.ProjectLocation != archiveLocationUnknown ||
		result.ProjectPath != "" ||
		result.ArchivedPath != "" {
		t.Fatalf("archive result = %+v, want explicit unknown location", result)
	}
}

func TestArchiveRollbackRetainsArchivedProofWhenDirectoryRestoreFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	slug := "rollback-cleanup-failure"
	srcDir := filepath.Join(project.ActiveDir(), slug)
	dstDir := filepath.Join(project.ArchivedDir(), slug)
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	active := project.Manifest{Slug: slug, Status: "active"}
	if err := project.Save(filepath.Join(srcDir, "manifest.json"), active); err != nil {
		t.Fatal(err)
	}
	archived := active
	archived.Status = "archived"
	archived.ArchiveCleanup = &project.ArchiveCleanupProof{
		Repository: "/repo", Branch: "user/branch", BranchPresent: true,
		ExpectedBranchTip: "0123456789012345678901234567890123456789",
	}

	stage, err := stageArchivedProject(srcDir, dstDir, archived)
	if err != nil {
		t.Fatal(err)
	}
	previousRename := archiveRename
	archiveRename = func(oldPath, newPath string) error {
		if oldPath == dstDir && newPath == srcDir {
			return errors.New("injected directory restore failure")
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() { archiveRename = previousRename })

	rollback := stage.rollback()
	if rollback.err == nil || !strings.Contains(rollback.err.Error(), "restore active project directory") ||
		!strings.Contains(rollback.err.Error(), "injected directory restore failure") {
		t.Fatalf("rollback error = %v, want directory restore failure", rollback.err)
	}
	if pathExists(srcDir) {
		t.Fatal("rollback recreated the active directory after its move failed")
	}
	retained, loadErr := project.Load(filepath.Join(dstDir, "manifest.json"))
	if loadErr != nil {
		t.Fatalf("load retained archived manifest: %v", loadErr)
	}
	if retained.Status != "archived" || retained.ArchiveCleanup == nil {
		t.Fatalf("retained manifest = %+v, want archived cleanup proof", retained)
	}
}

func TestArchiveReturnsArchivedPathWhenProofRevalidationRollbackFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "proof-revalidation-rollback-failure"
	branch := "user/proof-revalidation-rollback-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decideArchive(manifest, slug, true)
	if err != nil {
		t.Fatal(err)
	}

	previousSave := saveArchiveManifest
	advanced := false
	saveArchiveManifest = func(path string, candidate project.Manifest) error {
		if err := project.Save(path, candidate); err != nil {
			return err
		}
		if !advanced && candidate.ArchiveCleanup != nil {
			advanced = true
			commitArchiveFile(t, worktree, "late.txt", "late\n", "late change")
		}
		return nil
	}
	previousRename := archiveRename
	srcDir := filepath.Join(project.ActiveDir(), slug)
	dstDir := filepath.Join(project.ArchivedDir(), slug)
	archiveRename = func(oldPath, newPath string) error {
		if oldPath == dstDir && newPath == srcDir {
			return errors.New("injected directory restore failure")
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		saveArchiveManifest = previousSave
		archiveRename = previousRename
	})

	result, err := archiveProjectWithProof(decision.proof, true)
	if err == nil || !strings.Contains(err.Error(), "rollback archive metadata") {
		t.Fatalf("archiveProjectWithProof error = %v, want rollback failure", err)
	}
	if result.ArchivedPath != dstDir {
		t.Fatalf("archive result = %+v, want archived path %q", result, dstDir)
	}
	if pathExists(srcDir) || !pathExists(dstDir) {
		t.Fatal("archive result did not match metadata left in archived location")
	}
}

func TestArchiveRollbackIgnoresHostileFixedManifestEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	slug := "rollback-hostile-entry"
	srcDir := filepath.Join(project.ActiveDir(), slug)
	dstDir := filepath.Join(project.ArchivedDir(), slug)
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	active := project.Manifest{Slug: slug, Status: "active"}
	if err := project.Save(filepath.Join(srcDir, "manifest.json"), active); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(srcDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	archived := active
	archived.Status = "archived"
	archived.ArchiveCleanup = &project.ArchiveCleanupProof{
		Repository: "/repo", Branch: "user/branch", BranchPresent: true,
		ExpectedBranchTip: "0123456789012345678901234567890123456789",
	}

	stage, err := stageArchivedProject(srcDir, dstDir, archived)
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("do not overwrite\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hostile := filepath.Join(dstDir, ".manifest.active")
	if err := os.Symlink(victim, hostile); err != nil {
		t.Fatal(err)
	}

	if rollback := stage.rollback(); rollback.err != nil {
		t.Fatalf("rollback: %v", rollback.err)
	}
	after, err := os.ReadFile(filepath.Join(srcDir, "manifest.json"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("restored manifest = %q, err=%v, want original", after, err)
	}
	victimData, err := os.ReadFile(victim)
	if err != nil || string(victimData) != "do not overwrite\n" {
		t.Fatalf("hostile target = %q, err=%v, want unchanged", victimData, err)
	}
	info, err := os.Lstat(filepath.Join(srcDir, ".manifest.active"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("hostile entry was changed: info=%v err=%v", info, err)
	}
}

func TestArchiveRejectsUnmergedBranchBeforeDirtyWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "unmerged-dirty"
	branch := "user/unmerged-dirty"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "unique\n", "unique work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	writeArchiveFile(t, worktree, "notes.txt", "dirty\n")

	_, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	})
	if err == nil || !strings.Contains(err.Error(), "unmerged work") {
		t.Fatalf("runArchive error = %v, want unmerged branch protection", err)
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
}

func TestArchiveUnmergedBranchIncludesPullRequestLookupFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "unmerged-lookup-failure"
	branch := "user/unmerged-lookup-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "unique\n", "unique work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	recordArchiveManifestPR(t, slug, 410)
	installArchivePRLookupError(t, errors.New("GitHub unavailable"))

	_, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	})
	if err == nil {
		t.Fatal("runArchive succeeded for an unmerged branch")
	}
	for _, want := range []string{"unmerged work", "re-run with --force", "GitHub unavailable"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("runArchive error %q is missing %q", err, want)
		}
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
}

func TestArchiveForceKeepsDirtyUnmergedBehavior(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "force-dirty"
	branch := "user/force-dirty"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "unique\n", "unique work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	writeArchiveFile(t, worktree, "AGENTS.md", "# project\n\nPlease keep this.\n")

	if _, err := captureStdout(t, func() error {
		return runArchive(slug, true)
	}); err != nil {
		t.Fatalf("runArchive --force: %v", err)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatalf("active project dir still exists")
	}
	if pathExists(worktree) {
		t.Fatalf("worktree dir still exists")
	}
	if gitx.BranchExists(repo, branch) {
		t.Fatalf("branch %q still exists", branch)
	}
	archivedManifest := loadArchivedManifest(t, slug)
	if archivedManifest.Status != "archived" {
		t.Fatalf("archived status = %q, want archived", archivedManifest.Status)
	}
	if archivedManifest.Archived == nil || *archivedManifest.Archived == "" {
		t.Fatalf("archived timestamp was not set")
	}
	if archivedManifest.Merged {
		t.Fatal("force-archived unmerged work was recorded as merged")
	}
}

func TestArchiveRecordsVerifiedMergedBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "merged-work"
	branch := "user/merged-work"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "merged\n", "merged work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	runArchiveGit(t, repo, "merge", "-q", "--ff-only", branch)

	if _, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	}); err != nil {
		t.Fatalf("runArchive merged: %v", err)
	}
	if archived := loadArchivedManifest(t, slug); !archived.Merged {
		t.Fatalf("archived manifest merged = false, want true")
	}
}

func TestArchiveDoesNotMarkEmptyReachableBranchMerged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "empty-work"
	branch := "user/empty-work"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)

	if _, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	}); err != nil {
		t.Fatalf("runArchive empty branch: %v", err)
	}
	if archived := loadArchivedManifest(t, slug); archived.Merged {
		t.Fatal("empty branch was recorded as merged work")
	}
}

func TestArchiveWarnsWhenOptionalPullRequestLookupFailsForReachableBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "reachable-lookup-failure"
	branch := "user/reachable-lookup-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	recordArchiveManifestPR(t, slug, 409)
	installArchivePRLookupError(t, errors.New("GitHub unavailable"))

	stdout, stderr, err := captureGCOutput(t, func() error {
		return runArchive(slug, false)
	})
	if err != nil {
		t.Fatalf("runArchive reachable branch: %v", err)
	}
	if !strings.Contains(stderr, "GitHub unavailable") {
		t.Fatalf("stderr %q is missing the optional lookup warning", stderr)
	}
	if strings.Contains(stdout, "Branch still present:") {
		t.Fatalf("stdout %q falsely reports the deleted branch as present", stdout)
	}
	if gitx.BranchExists(repo, branch) {
		t.Fatalf("reachable branch %q survived archive", branch)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) ||
		!pathExists(filepath.Join(project.ArchivedDir(), slug)) {
		t.Fatal("optional pull request lookup failure prevented archive")
	}
}

func addArchiveWorktree(t *testing.T, repo, slug, branch string) string {
	t.Helper()
	worktree := filepath.Join(repo, ".worktrees", slug)
	runArchiveGit(t, repo, "worktree", "add", "-q", worktree, "-b", branch, "HEAD")
	return worktree
}

func writeArchiveManifest(t *testing.T, slug, repo, branch, worktree string) {
	t.Helper()
	projDir := filepath.Join(project.ActiveDir(), slug)
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	m := project.Manifest{
		Slug:       slug,
		Title:      slug,
		Repo:       repo,
		Branch:     branch,
		BaseBranch: "main",
		StartSHA:   gitOutput(t, repo, "rev-parse", "main"),
		Worktree:   &worktree,
		Status:     "active",
		Created:    now,
		Updated:    now,
	}
	if err := project.Save(filepath.Join(projDir, "manifest.json"), m); err != nil {
		t.Fatalf("save archive manifest: %v", err)
	}
}

func writeArchiveFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create parent dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func commitArchiveFile(t *testing.T, worktree, name, content, message string) {
	t.Helper()
	writeArchiveFile(t, worktree, name, content)
	runArchiveGit(t, worktree, "add", name)
	runArchiveGit(t, worktree, "commit", "-q", "-m", message)
}

func loadArchivedManifest(t *testing.T, slug string) project.Manifest {
	t.Helper()
	m, err := project.Load(filepath.Join(project.ArchivedDir(), slug, "manifest.json"))
	if err != nil {
		t.Fatalf("load archived manifest: %v", err)
	}
	return m
}

func saveArchivedManifest(t *testing.T, m project.Manifest) {
	t.Helper()
	path := project.ManifestPath(project.ArchivedDir(), m.Slug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create archived project dir: %v", err)
	}
	if err := project.Save(path, m); err != nil {
		t.Fatalf("save archived manifest: %v", err)
	}
}

func assertArchivePreserved(t *testing.T, repo, slug, branch, worktree string) {
	t.Helper()
	if !pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatalf("active project dir was removed")
	}
	if pathExists(filepath.Join(project.ArchivedDir(), slug)) {
		t.Fatalf("archived project dir exists")
	}
	if !pathExists(worktree) {
		t.Fatalf("worktree dir was removed")
	}
	if !gitx.BranchExists(repo, branch) {
		t.Fatalf("branch %q was removed", branch)
	}
}

func assertArchiveManifestAndResourcesUnchanged(
	t *testing.T, manifestPath string, before []byte, repo, branch, worktree string,
) {
	t.Helper()
	after, err := os.ReadFile(manifestPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("manifest changed: data=%q err=%v", after, err)
	}
	if !pathExists(worktree) {
		t.Fatal("worktree was removed")
	}
	if !gitx.BranchExists(repo, branch) {
		t.Fatalf("branch %q was removed", branch)
	}
}

func archiveWithFailedBranchDeletion(t *testing.T, slug string) project.Manifest {
	t.Helper()
	previous := archiveForceDeleteBranchAt
	archiveForceDeleteBranchAt = func(string, string, string) error {
		return errors.New("injected branch deletion failure")
	}
	result, err := archiveProject(slug, true)
	archiveForceDeleteBranchAt = previous
	if err != nil {
		t.Fatalf("archiveProject: %v", err)
	}
	if result.BranchDeletionWarning == "" {
		t.Fatal("archiveProject did not report the injected branch deletion failure")
	}
	archived := loadArchivedManifest(t, slug)
	if archived.ArchiveCleanup == nil {
		t.Fatal("archived manifest has no cleanup proof")
	}
	return archived
}

func runArchiveGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
		"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
}

func TestArchiveRecordsSquashMergedPullRequestFromGitHub(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "squashed-work"
	branch := "user/squashed-work"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "squashed\n", "squashed work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	recordArchiveManifestPR(t, slug, 404)
	installArchivePRIndex(t, map[string]programview.PRState{"#404": programview.PRStateMerged})

	if _, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	}); err != nil {
		t.Fatalf("runArchive squash-merged pull request: %v", err)
	}
	archived := loadArchivedManifest(t, slug)
	if !archived.Merged {
		t.Fatal("squash-merged pull request was not recorded as merged")
	}
	if gitx.BranchExists(repo, branch) {
		t.Fatal("squash-merged branch was left behind")
	}
}

func TestArchiveStillProtectsUnmergedPullRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "closed-work"
	branch := "user/closed-work"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "closed\n", "closed work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	recordArchiveManifestPR(t, slug, 405)
	installArchivePRIndex(t, map[string]programview.PRState{"#405": programview.PRStateClosed})

	_, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	})
	if err == nil || !strings.Contains(err.Error(), "unmerged work") {
		t.Fatalf("runArchive error = %v, want unmerged branch protection", err)
	}
	assertArchivePreserved(t, repo, slug, branch, worktree)
}

func TestArchiveRecordsMergedPullRequestWhenTheLocalBranchIsGone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "deleted-branch-work"
	branch := "user/deleted-branch-work"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "merged\n", "merged work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	recordArchiveManifestPR(t, slug, 406)
	installArchivePRIndex(t, map[string]programview.PRState{"#406": programview.PRStateMerged})
	runArchiveGit(t, repo, "worktree", "remove", "--force", worktree)
	runArchiveGit(t, repo, "branch", "-D", branch)

	if _, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	}); err != nil {
		t.Fatalf("runArchive with a deleted local branch: %v", err)
	}
	archived := loadArchivedManifest(t, slug)
	if !archived.Merged {
		t.Fatal("archive did not record the branch-matched pull request as merged")
	}
	if archived.Status != "archived" {
		t.Fatalf("archived status = %q, want archived", archived.Status)
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("an orphan active project directory was left behind")
	}
}

func TestArchiveRecordsMergedPullRequestWhenDeletedBranchLeavesDetachedWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "deleted-branch-detached-worktree"
	branch := "user/deleted-branch-detached-worktree"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "merged\n", "merged work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	recordArchiveManifestPR(t, slug, 411)
	installArchivePRIndex(t, map[string]programview.PRState{"#411": programview.PRStateMerged})
	runArchiveGit(t, worktree, "checkout", "-q", "--detach")
	runArchiveGit(t, repo, "branch", "-D", branch)

	if _, err := captureStdout(t, func() error {
		return runArchive(slug, false)
	}); err != nil {
		t.Fatalf("runArchive with detached worktree at merged PR head: %v", err)
	}
	if pathExists(worktree) {
		t.Fatal("archive left the verified detached worktree behind")
	}
	archived := loadArchivedManifest(t, slug)
	if !archived.Merged || archived.ArchiveCleanup == nil ||
		archived.ArchiveCleanup.AuthoritativeCommit == "" {
		t.Fatalf("archived manifest = %+v, want authoritative merged PR cleanup proof", archived)
	}
}

func TestArchiveWarnsAndContinuesWhenDeletedBranchProofFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "deleted-branch-proof-error"
	branch := "user/deleted-branch-proof-error"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "merged\n", "merged work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	ref := "https://ref-user:ref-secret@example.com/acme/widgets/pull/408?access_token=query-secret#fragment-secret"
	updateGCManifest(t, slug, func(manifest *project.Manifest) {
		manifest.PR = project.PRInfo{URL: &ref}
	})
	installArchivePRLookupError(t, errors.New("GitHub unavailable"))
	runArchiveGit(t, repo, "worktree", "remove", "--force", worktree)
	runArchiveGit(t, repo, "branch", "-D", branch)

	stdout, stderr, err := captureGCOutput(t, func() error {
		return runArchive(slug, false)
	})
	if err != nil {
		t.Fatalf("runArchive with unavailable proof lookup: %v", err)
	}
	if strings.Contains(stdout, "Branch still present:") {
		t.Fatalf("stdout %q falsely reports the already absent branch as present", stdout)
	}
	for _, secret := range []string{"ref-user", "ref-secret", "query-secret", "fragment-secret", "access_token"} {
		if strings.Contains(stderr, secret) {
			t.Fatalf("stderr %q leaked %q", stderr, secret)
		}
	}
	for _, want := range []string{
		"GitHub unavailable",
		"https://[redacted]@example.com/acme/widgets/pull/408",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing %q", stderr, want)
		}
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("archive left project metadata active after proof lookup failed")
	}
	archived := loadArchivedManifest(t, slug)
	if archived.Merged {
		t.Fatal("archive recorded unavailable pull request proof as merged")
	}
}

func TestArchiveReportsBranchStillPresentOnlyAfterDeletionFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "branch-deletion-failure"
	branch := "user/branch-deletion-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	missingWorktree := worktree + "-missing"
	writeArchiveManifest(t, slug, repo, branch, missingWorktree)
	expectedSHA := gitx.RevParse(repo, "refs/heads/"+branch)

	stdout, stderr, err := captureGCOutput(t, func() error {
		return runArchive(slug, false)
	})
	if err != nil {
		t.Fatalf("runArchive: %v", err)
	}
	if !strings.Contains(stdout, "Branch still present:") ||
		!strings.Contains(stdout, branch) {
		t.Fatalf("stdout %q is missing the branch deletion failure", stdout)
	}
	for _, want := range []string{
		"git -C " + shellQuote(repo) + " update-ref -d " + shellQuote("refs/heads/"+branch) + " " + shellQuote(expectedSHA),
		manualBranchConfigRemoveCommand(repo, branch),
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q is missing safe branch cleanup guidance %q", stderr, want)
		}
	}
	if strings.Contains(stderr, "branch -D") {
		t.Fatalf("stderr %q recommends unconditional branch deletion", stderr)
	}
	if !gitx.BranchExists(repo, branch) {
		t.Fatalf("branch %q was deleted despite being checked out", branch)
	}
}

func TestArchiveDoesNotDeleteBranchAdvancedImmediatelyBeforeDeletion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "branch-advanced-before-delete"
	branch := "user/branch-advanced-before-delete"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	previous := archiveForceDeleteBranchAt
	var advancedTip string
	archiveForceDeleteBranchAt = func(repo, branch, expectedSHA string) error {
		tree := gitOutput(t, repo, "rev-parse", expectedSHA+"^{tree}")
		cmd := exec.Command("git", "-C", repo, "commit-tree", tree, "-p", expectedSHA, "-m", "late commit")
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
			"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
		)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("create late commit: %v", err)
		}
		advancedTip = strings.TrimSpace(string(out))
		runArchiveGit(t, repo, "update-ref", "refs/heads/"+branch, advancedTip, expectedSHA)
		return gitx.ForceDeleteBranchAt(repo, branch, expectedSHA)
	}
	t.Cleanup(func() { archiveForceDeleteBranchAt = previous })

	result, err := archiveProject(slug, true)
	if err != nil {
		t.Fatalf("archiveProject: %v", err)
	}
	if !strings.Contains(result.BranchDeletionWarning, "changed from") {
		t.Fatalf("branch deletion warning = %q, want changed-tip rejection", result.BranchDeletionWarning)
	}
	if strings.Contains(result.BranchDeletionWarning, "branch -D") ||
		strings.Contains(result.BranchDeletionWarning, "hint:") {
		t.Fatalf("branch deletion warning %q includes destructive guidance for an advanced branch", result.BranchDeletionWarning)
	}
	tip, found, tipErr := gitx.LocalBranchTip(repo, branch)
	if tipErr != nil || !found || tip != advancedTip {
		t.Fatalf("branch tip = (%q, %t, %v), want (%q, true, nil)", tip, found, tipErr, advancedTip)
	}
}

func TestArchivedCleanupRetryPreservesAdvancedBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archived-retry-advanced-branch"
	branch := "user/archived-retry-advanced-branch"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	archived := archiveWithFailedBranchDeletion(t, slug)

	oldTip := archived.ArchiveCleanup.ExpectedBranchTip
	tree := gitOutput(t, repo, "rev-parse", oldTip+"^{tree}")
	cmd := exec.Command("git", "-C", repo, "commit-tree", tree, "-p", oldTip, "-m", "new work")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
		"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("create advanced branch commit: %v", err)
	}
	advancedTip := strings.TrimSpace(string(out))
	runArchiveGit(t, repo, "update-ref", "refs/heads/"+branch, advancedTip, oldTip)

	_, err = retryArchivedProjectCleanup(archived)
	if err == nil || !strings.Contains(err.Error(), "advanced from") {
		t.Fatalf("retryArchivedProjectCleanup error = %v, want advanced branch rejection", err)
	}
	tip, found, tipErr := gitx.LocalBranchTip(repo, branch)
	if tipErr != nil || !found || tip != advancedTip {
		t.Fatalf("advanced branch = (%q, %t, %v), want (%q, true, nil)", tip, found, tipErr, advancedTip)
	}
}

func TestArchivedCleanupRetryPreservesReusedWorktreePath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archived-retry-reused-path"
	branch := "user/archived-retry-reused-path"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	archived := archiveWithFailedBranchDeletion(t, slug)
	if archived.ArchiveCleanup.WorktreePresent {
		t.Fatal("successful worktree cleanup did not consume its durable proof")
	}

	replacementBranch := "user/replacement"
	runArchiveGit(t, repo, "worktree", "add", "-q", worktree, "-b", replacementBranch, "main")

	_, err := retryArchivedProjectCleanup(archived)
	if err == nil || !strings.Contains(err.Error(), "registered after") {
		t.Fatalf("retryArchivedProjectCleanup error = %v, want recreated worktree rejection", err)
	}
	if !pathExists(worktree) {
		t.Fatal("retry removed the replacement worktree")
	}
	if !gitx.BranchExists(repo, branch) {
		t.Fatal("retry removed the originally recorded branch")
	}
	if !gitx.BranchExists(repo, replacementBranch) {
		t.Fatal("retry removed the replacement branch")
	}
}

func TestArchivedCleanupRetryDoesNotReplayClaimedWorktreeRemoval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archived-retry-claimed-worktree"
	branch := "user/archived-retry-claimed-worktree"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decideArchive(manifest, slug, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Status = "archived"
	manifest.ArchiveCleanup = archiveCleanupProof(decision.proof)
	manifest.ArchiveCleanup.WorktreeState = project.ArchiveCleanupClaimed
	srcDir := filepath.Join(project.ActiveDir(), slug)
	dstDir := filepath.Join(project.ArchivedDir(), slug)
	if _, err := stageArchivedProject(srcDir, dstDir, manifest); err != nil {
		t.Fatal(err)
	}

	_, err = retryArchivedProjectCleanup(loadArchivedManifest(t, slug))
	if err == nil || !strings.Contains(err.Error(), "already claimed") ||
		!strings.Contains(err.Error(), "will not retry removal") {
		t.Fatalf("retryArchivedProjectCleanup error = %v, want claimed-state preservation", err)
	}
	if !pathExists(worktree) || !gitx.BranchExists(repo, branch) {
		t.Fatal("claimed-state retry replayed destructive cleanup")
	}
}

func TestArchivedCleanupRetryDoesNotReplayClaimedBranchDeletion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archived-retry-claimed-branch"
	branch := "user/archived-retry-claimed-branch"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	archived := archiveWithFailedBranchDeletion(t, slug)
	if archived.ArchiveCleanup.BranchState != project.ArchiveCleanupClaimed {
		t.Fatalf("branch cleanup state = %q, want claimed", archived.ArchiveCleanup.BranchState)
	}

	_, err := retryArchivedProjectCleanup(archived)
	if err == nil || !strings.Contains(err.Error(), "already claimed") ||
		!strings.Contains(err.Error(), "will not retry removal") {
		t.Fatalf("retryArchivedProjectCleanup error = %v, want claimed-state preservation", err)
	}
	if !gitx.BranchExists(repo, branch) {
		t.Fatal("claimed-state retry replayed branch deletion")
	}
}

func TestArchivedCleanupConcurrentRetryClaimsWorktreeOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archived-concurrent-worktree"
	branch := "user/archived-concurrent-worktree"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decideArchive(manifest, slug, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Status = "archived"
	manifest.ArchiveCleanup = archiveCleanupProof(decision.proof)
	if _, err := stageArchivedProject(
		filepath.Join(project.ActiveDir(), slug),
		filepath.Join(project.ArchivedDir(), slug),
		manifest,
	); err != nil {
		t.Fatal(err)
	}
	archived := loadArchivedManifest(t, slug)

	previous := archiveWorktreeRemove
	var removeCalls atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	archiveWorktreeRemove = func(
		repo, worktree string, expected gitx.WorktreeState, force bool,
	) error {
		removeCalls.Add(1)
		entered <- struct{}{}
		<-release
		return gitx.WorktreeRemoveAt(repo, worktree, expected, force)
	}
	t.Cleanup(func() { archiveWorktreeRemove = previous })

	results := make(chan error, 2)
	go func() {
		_, cleanupErr := retryArchivedProjectCleanup(archived)
		results <- cleanupErr
	}()
	<-entered
	go func() {
		_, cleanupErr := retryArchivedProjectCleanup(archived)
		results <- cleanupErr
	}()

	select {
	case <-entered:
		close(release)
		<-results
		<-results
		t.Fatal("concurrent cleanup entered worktree removal twice")
	case <-time.After(100 * time.Millisecond):
		close(release)
	}
	for range 2 {
		if cleanupErr := <-results; cleanupErr != nil {
			t.Fatalf("retryArchivedProjectCleanup: %v", cleanupErr)
		}
	}
	if removeCalls.Load() != 1 {
		t.Fatalf("worktree removal calls = %d, want 1", removeCalls.Load())
	}
}

func TestArchivedCleanupConcurrentRetryClaimsBranchOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archived-concurrent-branch"
	branch := "user/archived-concurrent-branch"
	runArchiveGit(t, repo, "branch", branch, "HEAD")
	now := time.Now().UTC().Format(time.RFC3339)
	manifest := project.Manifest{
		Slug: slug, Title: slug, Repo: repo, Branch: branch, BaseBranch: "main",
		StartSHA: gitOutput(t, repo, "rev-parse", "main"),
		Status:   "active", Created: now, Updated: now,
	}
	activeDir := filepath.Join(project.ActiveDir(), slug)
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Save(filepath.Join(activeDir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	decision, err := decideArchive(manifest, slug, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Status = "archived"
	manifest.ArchiveCleanup = archiveCleanupProof(decision.proof)
	if _, err := stageArchivedProject(
		activeDir, filepath.Join(project.ArchivedDir(), slug), manifest,
	); err != nil {
		t.Fatal(err)
	}
	archived := loadArchivedManifest(t, slug)

	previous := archiveForceDeleteBranchAt
	var deleteCalls atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	archiveForceDeleteBranchAt = func(repo, branch, expectedSHA string) error {
		deleteCalls.Add(1)
		entered <- struct{}{}
		<-release
		return gitx.ForceDeleteBranchAt(repo, branch, expectedSHA)
	}
	t.Cleanup(func() { archiveForceDeleteBranchAt = previous })

	results := make(chan error, 2)
	go func() {
		_, cleanupErr := retryArchivedProjectCleanup(archived)
		results <- cleanupErr
	}()
	<-entered
	go func() {
		_, cleanupErr := retryArchivedProjectCleanup(archived)
		results <- cleanupErr
	}()

	select {
	case <-entered:
		close(release)
		<-results
		<-results
		t.Fatal("concurrent cleanup entered branch deletion twice")
	case <-time.After(100 * time.Millisecond):
		close(release)
	}
	for range 2 {
		if cleanupErr := <-results; cleanupErr != nil {
			t.Fatalf("retryArchivedProjectCleanup: %v", cleanupErr)
		}
	}
	if deleteCalls.Load() != 1 {
		t.Fatalf("branch deletion calls = %d, want 1", deleteCalls.Load())
	}
}

func TestArchivedCleanupRetryRejectsRecreatedBranchAtConsumedTip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archived-retry-recreated-branch"
	branch := "user/archived-retry-recreated-branch"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	branchTip := gitx.RevParse(repo, "refs/heads/"+branch)

	if _, err := archiveProject(slug, true); err != nil {
		t.Fatalf("archiveProject: %v", err)
	}
	archived := loadArchivedManifest(t, slug)
	if archived.ArchiveCleanup == nil || archived.ArchiveCleanup.BranchPresent ||
		archived.ArchiveCleanup.ExpectedBranchTip != "" {
		t.Fatalf("branch cleanup proof was not consumed: %+v", archived.ArchiveCleanup)
	}
	runArchiveGit(t, repo, "branch", branch, branchTip)

	_, err := retryArchivedProjectCleanup(archived)
	if err == nil || !strings.Contains(err.Error(), "appeared after") {
		t.Fatalf("retryArchivedProjectCleanup error = %v, want recreated branch rejection", err)
	}
	tip, found, tipErr := gitx.LocalBranchTip(repo, branch)
	if tipErr != nil || !found || tip != branchTip {
		t.Fatalf("recreated branch = (%q, %t, %v), want (%q, true, nil)", tip, found, tipErr, branchTip)
	}
}

func TestArchivedCleanupRetryIsCleanAfterProofConsumption(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archived-retry-consumed"
	branch := "user/archived-retry-consumed"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)

	if _, err := archiveProject(slug, true); err != nil {
		t.Fatalf("archiveProject: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		archived := loadArchivedManifest(t, slug)
		if archived.ArchiveCleanup == nil || archived.ArchiveCleanup.WorktreePresent ||
			archived.ArchiveCleanup.BranchPresent {
			t.Fatalf("attempt %d cleanup proof = %+v, want fully consumed", attempt, archived.ArchiveCleanup)
		}
		result, err := retryArchivedProjectCleanup(archived)
		if err != nil {
			t.Fatalf("attempt %d retryArchivedProjectCleanup: %v", attempt, err)
		}
		if result.WorktreeRemoved || result.BranchDeleted || result.BranchDeletionWarning != "" {
			t.Fatalf("attempt %d replayed cleanup: %+v", attempt, result)
		}
	}
}

func TestArchivedCleanupRetryConsumesProofForAlreadyAbsentResources(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archived-retry-stale-proof"
	branch := "user/archived-retry-stale-proof"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decideArchive(manifest, slug, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archiveProjectWithProof(decision.proof, true); err != nil {
		t.Fatalf("archiveProjectWithProof: %v", err)
	}
	archived := loadArchivedManifest(t, slug)
	archived.ArchiveCleanup = archiveCleanupProof(decision.proof)
	if err := project.Save(
		project.ManifestPath(project.ArchivedDir(), slug), archived,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := retryArchivedProjectCleanup(archived); err != nil {
		t.Fatalf("retryArchivedProjectCleanup: %v", err)
	}
	consumed := loadArchivedManifest(t, slug)
	if consumed.ArchiveCleanup == nil || consumed.ArchiveCleanup.WorktreePresent ||
		consumed.ArchiveCleanup.BranchPresent {
		t.Fatalf("cleanup proof = %+v, want absent resources consumed", consumed.ArchiveCleanup)
	}

	runArchiveGit(t, repo, "branch", branch, decision.proof.ExpectedBranchTip)
	_, err = retryArchivedProjectCleanup(consumed)
	if err == nil || !strings.Contains(err.Error(), "appeared after") {
		t.Fatalf("retry after branch recreation error = %v, want consumed-proof rejection", err)
	}
}

func TestArchivedCleanupRetryRemovesMissingRegisteredWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "archived-retry-missing-registered-worktree"
	branch := "test/" + slug
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decideArchive(manifest, slug, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Status = "archived"
	manifest.ArchiveCleanup = archiveCleanupProof(decision.proof)
	if _, err := stageArchivedProject(
		filepath.Join(project.ActiveDir(), slug),
		filepath.Join(project.ArchivedDir(), slug),
		manifest,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	if registered, err := gitx.IsWorktree(repo, worktree); err != nil || !registered {
		t.Fatalf("missing worktree registration = (%t, %v), want (true, nil)", registered, err)
	}

	result, err := retryArchivedProjectCleanup(loadArchivedManifest(t, slug))
	if err != nil {
		t.Fatalf("retryArchivedProjectCleanup: %v", err)
	}
	if !result.WorktreeRemoved {
		t.Fatalf("archive result = %+v, want stale registration removed", result)
	}
	if registered, err := gitx.IsWorktree(repo, worktree); err != nil || registered {
		t.Fatalf("worktree registration after retry = (%t, %v), want (false, nil)", registered, err)
	}
	archived := loadArchivedManifest(t, slug)
	if archived.ArchiveCleanup == nil ||
		archived.ArchiveCleanup.WorktreeState != project.ArchiveCleanupDone ||
		archived.ArchiveCleanup.WorktreePresent {
		t.Fatalf("cleanup proof = %+v, want completed worktree cleanup", archived.ArchiveCleanup)
	}

	if err := config.Save(config.Config{
		BranchPrefix: "test/",
		DefaultAgent: "copilot",
		PermissionModes: map[string]string{
			"copilot": "allow-all",
		},
	}); err != nil {
		t.Fatal(err)
	}
	created, err := createProject(projectCreateOpts{
		task: "replacement project", name: slug, repo: repo,
	})
	if err != nil {
		t.Fatalf("createProject after stale registration cleanup: %v", err)
	}
	if !pathExists(created.worktreeDir) {
		t.Fatal("replacement worktree was not created")
	}
}

func TestLegacyArchivedCleanupIsCleanWhenResourcesAreAlreadyAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "legacy-absent-resources"
	branch := "user/legacy-absent-resources"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	runArchiveGit(t, repo, "worktree", "remove", "--force", worktree)
	runArchiveGit(t, repo, "branch", "-D", branch)
	manifest := project.Manifest{
		Slug: slug, Repo: repo, Branch: branch, Worktree: &worktree, Status: "archived",
	}
	saveArchivedManifest(t, manifest)

	result, err := retryArchivedProjectCleanup(manifest)
	if err != nil {
		t.Fatalf("retryArchivedProjectCleanup: %v", err)
	}
	if result.WorktreeRemoved || result.BranchDeleted || result.BranchDeletionWarning != "" {
		t.Fatalf("legacy cleanup replayed destructive work: %+v", result)
	}
}

func TestLegacyArchivedCleanupPreservesRemainingResources(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "legacy-present-resources"
	branch := "user/legacy-present-resources"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	manifest := project.Manifest{
		Slug: slug, Repo: repo, Branch: branch, Worktree: &worktree, Status: "archived",
	}
	saveArchivedManifest(t, manifest)

	_, err := retryArchivedProjectCleanup(manifest)
	if err == nil || !strings.Contains(err.Error(), "no durable cleanup proof") ||
		!strings.Contains(err.Error(), "manual inspection") {
		t.Fatalf("retryArchivedProjectCleanup error = %v, want manual inspection", err)
	}
	if !pathExists(worktree) || !gitx.BranchExists(repo, branch) {
		t.Fatal("legacy cleanup removed resources without durable proof")
	}
}

func TestLegacyArchivedCleanupReportsWorktreeProbeFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "legacy-worktree-probe-failure"
	branch := "user/legacy-worktree-probe-failure"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	runArchiveGit(t, repo, "worktree", "remove", "--force", worktree)
	runArchiveGit(t, repo, "branch", "-D", branch)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := project.Manifest{
		Slug: slug, Repo: repo, Branch: branch, Worktree: &worktree, Status: "archived",
	}
	saveArchivedManifest(t, manifest)

	_, err := retryArchivedProjectCleanup(manifest)
	if err == nil || !strings.Contains(err.Error(), "not registered") ||
		!strings.Contains(err.Error(), "manual inspection") {
		t.Fatalf("retryArchivedProjectCleanup error = %v, want worktree probe failure", err)
	}
	if !pathExists(worktree) {
		t.Fatal("legacy cleanup removed the unregistered worktree path")
	}
}

func TestResolveRecordedPullRequestMergeReturnsStateReadError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	slug := "malformed-state"
	statePath := project.StatePath(slug)
	if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}

	merged, err := resolveRecordedPullRequestMerge(project.Manifest{Slug: slug}, slug)
	if err == nil {
		t.Fatal("resolveRecordedPullRequestMerge error = nil")
	}
	if merged {
		t.Fatal("resolveRecordedPullRequestMerge reported malformed state as merged")
	}
	if !strings.Contains(err.Error(), statePath) {
		t.Fatalf("resolveRecordedPullRequestMerge error = %q, want state path", err)
	}
	if recordedPullRequestMerged(project.Manifest{Slug: slug}, slug) {
		t.Fatal("recordedPullRequestMerged should remain conservative on state errors")
	}
}

func TestResolveRecordedPullRequestMergeRequiresMatchingRepositoryAndHead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "verified-pr"
	branch := "user/verified-pr"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "verified\n", "verified work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	recordArchiveManifestPR(t, slug, 407)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	branchTip := gitx.RevParse(repo, "refs/heads/"+branch)

	tests := []struct {
		name            string
		repository      string
		localRepository string
		baseBranch      string
		headBranch      string
		headSHA         string
		wantMerged      bool
		wantError       string
	}{
		{
			name:            "matching github.com proof",
			repository:      "github.com/acme/widgets",
			localRepository: "github.com/acme/widgets",
			baseBranch:      "main",
			headBranch:      branch,
			headSHA:         branchTip,
			wantMerged:      true,
		},
		{
			name:            "matching enterprise proof",
			repository:      "github.example/acme/widgets",
			localRepository: "github.example/acme/widgets",
			baseBranch:      "main",
			headBranch:      branch,
			headSHA:         branchTip,
			wantMerged:      true,
		},
		{
			name:            "different host",
			repository:      "github.example/acme/widgets",
			localRepository: "github.com/acme/widgets",
			baseBranch:      "main",
			headBranch:      branch,
			headSHA:         branchTip,
			wantError:       "repository",
		},
		{
			name:            "different repository",
			repository:      "github.com/acme/other",
			localRepository: "github.com/acme/widgets",
			baseBranch:      "main",
			headBranch:      branch,
			headSHA:         branchTip,
			wantError:       "repository",
		},
		{
			name:            "different base branch",
			repository:      "github.com/acme/widgets",
			localRepository: "github.com/acme/widgets",
			baseBranch:      "release",
			headBranch:      branch,
			headSHA:         branchTip,
			wantError:       "base branch",
		},
		{
			name:            "different head branch with local branch",
			repository:      "github.com/acme/widgets",
			localRepository: "github.com/acme/widgets",
			baseBranch:      "main",
			headBranch:      "user/another-branch",
			headSHA:         branchTip,
			wantError:       "head branch",
		},
		{
			name:            "different head",
			repository:      "github.com/acme/widgets",
			localRepository: "github.com/acme/widgets",
			baseBranch:      "main",
			headBranch:      branch,
			headSHA:         manifest.StartSHA,
			wantError:       "head",
		},
		{
			name:            "missing head",
			repository:      "github.com/acme/widgets",
			localRepository: "github.com/acme/widgets",
			baseBranch:      "main",
			headBranch:      branch,
			wantError:       "head",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previousProof := loadArchivePullRequestProof
			previousRepository := loadArchiveRepository
			loadArchivePullRequestProof = func(string, string) (programview.PullRequestProof, error) {
				return programview.PullRequestProof{
					State:      programview.PRStateMerged,
					Repository: test.repository,
					BaseBranch: test.baseBranch,
					HeadBranch: test.headBranch,
					HeadSHA:    test.headSHA,
				}, nil
			}
			loadArchiveRepository = func(string) (string, error) {
				return test.localRepository, nil
			}
			t.Cleanup(func() {
				loadArchivePullRequestProof = previousProof
				loadArchiveRepository = previousRepository
			})

			merged, err := resolveRecordedPullRequestMerge(manifest, slug)
			if merged != test.wantMerged {
				t.Fatalf("merged = %t, want %t", merged, test.wantMerged)
			}
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("resolveRecordedPullRequestMerge: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q diagnostic", err, test.wantError)
			}
		})
	}
}

func TestResolveRecordedPullRequestMergeRejectsAttachedWorktreeOnAnotherBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "wrong-attached-branch"
	branch := "user/wrong-attached-branch"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	recordArchiveManifestPR(t, slug, 409)
	tip := gitx.RevParse(repo, "refs/heads/"+branch)
	replacement := "user/replacement-at-same-tip"
	runArchiveGit(t, repo, "branch", replacement, tip)
	runArchiveGit(t, worktree, "checkout", "-q", replacement)
	installArchivePRIndex(t, map[string]programview.PRState{"#409": programview.PRStateMerged})
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}

	merged, err := resolveRecordedPullRequestMerge(manifest, slug)
	if err == nil || !strings.Contains(err.Error(), "attached to") ||
		!strings.Contains(err.Error(), "refs/heads/"+branch) {
		t.Fatalf("resolveRecordedPullRequestMerge error = %v, want worktree branch rejection", err)
	}
	if merged {
		t.Fatal("resolveRecordedPullRequestMerge accepted another branch at the same commit")
	}
	if !pathExists(worktree) || !gitx.BranchExists(repo, branch) ||
		!gitx.BranchExists(repo, replacement) {
		t.Fatal("pull request proof validation changed worktree or branch resources")
	}
}

func TestResolveRecordedPullRequestMergeAllowsMatchingDetachedWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "matching-detached-worktree"
	branch := "user/matching-detached-worktree"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	recordArchiveManifestPR(t, slug, 410)
	installArchivePRIndex(t, map[string]programview.PRState{"#410": programview.PRStateMerged})
	runArchiveGit(t, worktree, "checkout", "-q", "--detach")
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}

	merged, err := resolveRecordedPullRequestMerge(manifest, slug)
	if err != nil {
		t.Fatalf("resolveRecordedPullRequestMerge: %v", err)
	}
	if !merged {
		t.Fatal("resolveRecordedPullRequestMerge rejected a detached worktree at the proven head")
	}
}

func TestArchiveForceDeletesBranchMergedOnlyIntoRemoteBase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	runArchiveGit(t, filepath.Dir(remote), "init", "-q", "--bare", "--initial-branch=main", remote)
	runArchiveGit(t, repo, "remote", "add", "origin", remote)
	runArchiveGit(t, repo, "push", "-q", "-u", "origin", "main")

	slug := "remote-only-merge"
	branch := "user/remote-only-merge"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "merged upstream\n", "merged upstream")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	runArchiveGit(t, repo, "push", "-q", "origin", branch+":main")

	result, err := archiveProject(slug, true)
	if err != nil {
		t.Fatalf("archiveProject --force: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("archive warnings = %v, want none", result.Warnings)
	}
	if !result.BranchDeleted || gitx.BranchExists(repo, branch) {
		t.Fatalf("branch %q survived forced archive", branch)
	}
}

func TestArchiveDeletesLocallyReachableBranchWhenOriginBaseIsStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	runArchiveGit(t, filepath.Dir(remote), "init", "-q", "--bare", "--initial-branch=main", remote)
	runArchiveGit(t, repo, "remote", "add", "origin", remote)
	runArchiveGit(t, repo, "push", "-q", "-u", "origin", "main")

	slug := "local-merge-stale-origin"
	branch := "user/local-merge-stale-origin"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "merged locally\n", "merged locally")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	runArchiveGit(t, repo, "merge", "-q", "--no-edit", branch)

	result, err := archiveProject(slug, false)
	if err != nil {
		t.Fatalf("archiveProject: %v", err)
	}
	if result.BranchDeletionWarning != "" {
		t.Fatalf("branch deletion warning = %q, want none", result.BranchDeletionWarning)
	}
	if !result.BranchDeleted || gitx.BranchExists(repo, branch) {
		t.Fatalf("locally reachable branch %q survived archive", branch)
	}
}

func recordArchiveManifestPR(t *testing.T, slug string, number int) {
	t.Helper()
	path := project.ManifestPath(project.ActiveDir(), slug)
	manifest, err := project.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest.PR = project.PRInfo{Number: &number}
	if err := project.Save(path, manifest); err != nil {
		t.Fatal(err)
	}
}

func installArchivePRIndex(t *testing.T, states map[string]programview.PRState) {
	t.Helper()
	proofs := make(map[string]programview.PullRequestProof, len(states))
	manifests, err := project.LoadAll(project.ActiveDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, manifest := range manifests {
		hasPR, ref, err := programview.RecordedPR(manifest, project.StatePath(manifest.Slug))
		if err != nil {
			t.Fatal(err)
		}
		state, found := states[ref]
		if !hasPR || !found {
			continue
		}
		proofs[ref] = programview.PullRequestProof{
			State:      state,
			Repository: "github.com/acme/widgets",
			BaseBranch: manifest.BaseBranch,
			HeadBranch: manifest.Branch,
			HeadSHA:    gitx.RevParse(manifest.Repo, "refs/heads/"+manifest.Branch),
		}
	}
	previousProof := loadArchivePullRequestProof
	previousRepository := loadArchiveRepository
	loadArchivePullRequestProof = func(_, ref string) (programview.PullRequestProof, error) {
		proof, found := proofs[ref]
		if !found {
			return programview.PullRequestProof{}, errors.New("pull request not found")
		}
		return proof, nil
	}
	loadArchiveRepository = func(string) (string, error) {
		return "github.com/acme/widgets", nil
	}
	t.Cleanup(func() {
		loadArchivePullRequestProof = previousProof
		loadArchiveRepository = previousRepository
	})
}

func installArchivePRLookupError(t *testing.T, lookupErr error) {
	t.Helper()
	previous := loadArchivePullRequestProof
	loadArchivePullRequestProof = func(string, string) (programview.PullRequestProof, error) {
		return programview.PullRequestProof{}, lookupErr
	}
	t.Cleanup(func() { loadArchivePullRequestProof = previous })
}

func TestArchiveProjectReturnsAResultWithoutWritingToStdout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "result-archive"
	branch := "user/result-archive"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)

	var result archiveResult
	out, err := captureStdout(t, func() error {
		var archiveErr error
		result, archiveErr = archiveProject(slug, false)
		return archiveErr
	})
	if err != nil {
		t.Fatalf("archiveProject: %v", err)
	}
	if out != "" {
		t.Fatalf("archiveProject wrote %q to stdout", out)
	}
	if result.Slug != slug || result.Branch != branch {
		t.Fatalf("result identity = %q/%q", result.Slug, result.Branch)
	}
	if !result.WorktreeRemoved || result.Worktree != worktree {
		t.Fatalf("worktree result = %t/%q", result.WorktreeRemoved, result.Worktree)
	}
	if !result.BranchDeleted {
		t.Fatalf("branch %q was not deleted: %v", branch, result.Warnings)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", result.Warnings)
	}
	if result.ArchivedPath != filepath.Join(project.ArchivedDir(), slug) {
		t.Fatalf("archived path = %q", result.ArchivedPath)
	}
	if pathExists(worktree) || pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("archiveProject left the worktree or active project behind")
	}
}

func TestRunArchiveRetriesStandaloneArchivedCleanupAndAllowsSlugReuse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "standalone-archived-retry"
	branch := "test/" + slug
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decideArchive(manifest, slug, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Status = "archived"
	manifest.ArchiveCleanup = archiveCleanupProof(decision.proof)
	if _, err := stageArchivedProject(
		filepath.Join(project.ActiveDir(), slug),
		filepath.Join(project.ArchivedDir(), slug),
		manifest,
	); err != nil {
		t.Fatal(err)
	}
	writeArchiveFile(t, worktree, "dirty.txt", "discard on authorized retry\n")
	archivedBeforeRetry := loadArchivedManifest(t, slug)
	if archivedBeforeRetry.ArchiveCleanup == nil ||
		!archivedBeforeRetry.ArchiveCleanup.ForceAuthorized {
		t.Fatalf(
			"cleanup proof = %+v, want persisted force authorization",
			archivedBeforeRetry.ArchiveCleanup,
		)
	}

	stdout, stderr, err := captureGCOutput(t, func() error {
		return runArchive(slug, false)
	})
	if err != nil {
		t.Fatalf("runArchive archived retry: %v", err)
	}
	if stderr != "" {
		t.Fatalf("runArchive archived retry stderr = %q, want empty", stderr)
	}
	for _, want := range []string{
		"Archived cleanup complete:", slug,
		"Worktree removed:", worktree,
		"Branch removed:", branch,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("runArchive archived retry output %q is missing %q", stdout, want)
		}
	}
	if pathExists(worktree) || gitx.BranchExists(repo, branch) {
		t.Fatal("standalone archived retry left project resources behind")
	}
	archived := loadArchivedManifest(t, slug)
	if archived.ArchiveCleanup == nil ||
		archived.ArchiveCleanup.WorktreeState != project.ArchiveCleanupDone ||
		archived.ArchiveCleanup.WorktreePresent ||
		archived.ArchiveCleanup.BranchState != project.ArchiveCleanupDone ||
		archived.ArchiveCleanup.BranchPresent {
		t.Fatalf("cleanup proof = %+v, want fully consumed", archived.ArchiveCleanup)
	}

	if err := config.Save(config.Config{
		BranchPrefix: "test/",
		DefaultAgent: "copilot",
		PermissionModes: map[string]string{
			"copilot": "allow-all",
		},
	}); err != nil {
		t.Fatal(err)
	}
	created, err := createProject(projectCreateOpts{
		task: "replacement project", name: slug, repo: repo,
	})
	if err != nil {
		t.Fatalf("createProject after standalone archived retry: %v", err)
	}
	if created.manifest.Slug != slug || !pathExists(created.worktreeDir) {
		t.Fatalf("created project = %+v, want reusable slug %q", created, slug)
	}
}

func TestRunArchiveArchivedRetryRequiresForceForDirtyWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "standalone-dirty-archived-retry"
	branch := "test/" + slug
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decideArchive(manifest, slug, false)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Status = "archived"
	manifest.ArchiveCleanup = archiveCleanupProof(decision.proof)
	if _, err := stageArchivedProject(
		filepath.Join(project.ActiveDir(), slug),
		filepath.Join(project.ArchivedDir(), slug),
		manifest,
	); err != nil {
		t.Fatal(err)
	}
	writeArchiveFile(t, worktree, "dirty.txt", "preserve me\n")

	_, _, err = captureGCOutput(t, func() error {
		return runArchive(slug, false)
	})
	if err == nil || !strings.Contains(err.Error(), "incomplete") ||
		!strings.Contains(err.Error(), "--force") {
		t.Fatalf("plain archived retry error = %v, want incomplete --force guidance", err)
	}
	if !pathExists(worktree) || !gitx.BranchExists(repo, branch) {
		t.Fatal("plain archived retry removed dirty project resources")
	}
	if data, readErr := os.ReadFile(filepath.Join(worktree, "dirty.txt")); readErr != nil ||
		string(data) != "preserve me\n" {
		t.Fatalf("dirty work changed: data=%q err=%v", data, readErr)
	}
	archived := loadArchivedManifest(t, slug)
	if archived.ArchiveCleanup == nil ||
		archived.ArchiveCleanup.WorktreeState != project.ArchiveCleanupPending {
		t.Fatalf("cleanup proof = %+v, want retryable pending worktree cleanup", archived.ArchiveCleanup)
	}

	if _, _, err := captureGCOutput(t, func() error {
		return runArchive(slug, true)
	}); err != nil {
		t.Fatalf("forced archived retry: %v", err)
	}
	if pathExists(worktree) || gitx.BranchExists(repo, branch) {
		t.Fatal("forced archived retry left project resources behind")
	}
}

func TestRunArchiveReportsIncompleteArchivedCleanupWithManualGuidance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "standalone-incomplete-retry"
	branch := "test/" + slug
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decideArchive(manifest, slug, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Status = "archived"
	manifest.ArchiveCleanup = archiveCleanupProof(decision.proof)
	if _, err := stageArchivedProject(
		filepath.Join(project.ActiveDir(), slug),
		filepath.Join(project.ArchivedDir(), slug),
		manifest,
	); err != nil {
		t.Fatal(err)
	}

	previousDelete := archiveForceDeleteBranchAt
	archiveForceDeleteBranchAt = func(string, string, string) error {
		return errors.New("injected branch retry failure")
	}
	t.Cleanup(func() { archiveForceDeleteBranchAt = previousDelete })
	stdout, stderr, err := captureGCOutput(t, func() error {
		return runArchive(slug, false)
	})
	archiveForceDeleteBranchAt = previousDelete
	if err != nil {
		t.Fatalf("runArchive incomplete archived retry: %v", err)
	}
	for _, want := range []string{
		"Archived cleanup incomplete:", slug, "Worktree removed:", "Branch still present:",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("runArchive incomplete output %q is missing %q", stdout, want)
		}
	}
	manualDelete := manualBranchDeleteAtCommand(repo, branch, decision.proof.ExpectedBranchTip)
	for _, want := range []string{"injected branch retry failure", manualDelete} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("runArchive incomplete stderr %q is missing %q", stderr, want)
		}
	}
	if pathExists(worktree) || !gitx.BranchExists(repo, branch) {
		t.Fatal("incomplete retry did not preserve only the ambiguous branch")
	}
	archived := loadArchivedManifest(t, slug)
	if archived.ArchiveCleanup == nil ||
		archived.ArchiveCleanup.WorktreeState != project.ArchiveCleanupDone ||
		archived.ArchiveCleanup.BranchState != project.ArchiveCleanupClaimed {
		t.Fatalf("cleanup proof = %+v, want completed worktree and claimed branch", archived.ArchiveCleanup)
	}

	_, _, err = captureGCOutput(t, func() error {
		return runArchive(slug, false)
	})
	if err == nil {
		t.Fatal("runArchive replayed an ambiguous claimed branch cleanup")
	}
	for _, want := range []string{"already claimed", "will not retry removal", manualDelete} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("runArchive retry error %q is missing %q", err, want)
		}
	}
	if !gitx.BranchExists(repo, branch) {
		t.Fatal("claimed retry removed the ambiguous branch")
	}
}

func TestRunArchiveLeavesArchivedProgramCleanupToWorkerLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "managed-archived-retry"
	branch := "test/" + slug
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), slug))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decideArchive(manifest, slug, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Status = "archived"
	manifest.Program = "release-train"
	manifest.ProgramItem = "w1"
	manifest.ArchiveCleanup = archiveCleanupProof(decision.proof)
	if _, err := stageArchivedProject(
		filepath.Join(project.ActiveDir(), slug),
		filepath.Join(project.ArchivedDir(), slug),
		manifest,
	); err != nil {
		t.Fatal(err)
	}

	_, _, err = captureGCOutput(t, func() error {
		return runArchive(slug, false)
	})
	if err == nil || !strings.Contains(
		err.Error(), "relay program worker cleanup release-train w1",
	) {
		t.Fatalf("runArchive managed retry error = %v, want worker cleanup guidance", err)
	}
	if !pathExists(worktree) || !gitx.BranchExists(repo, branch) {
		t.Fatal("standalone archive command cleaned managed project resources")
	}
	archived := loadArchivedManifest(t, slug)
	if archived.ArchiveCleanup == nil ||
		archived.ArchiveCleanup.WorktreeState != project.ArchiveCleanupPending ||
		archived.ArchiveCleanup.BranchState != project.ArchiveCleanupPending {
		t.Fatalf("managed cleanup proof changed: %+v", archived.ArchiveCleanup)
	}
}

func TestArchiveForceDiscardsDirtyAndUntrackedWork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "force-discard"
	branch := "user/force-discard"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	commitArchiveFile(t, worktree, "feature.txt", "unique\n", "unique work")
	writeArchiveManifest(t, slug, repo, branch, worktree)
	writeArchiveFile(t, worktree, "feature.txt", "dirty edit\n")
	writeArchiveFile(t, worktree, "scratch/notes.txt", "untracked\n")

	result, err := archiveProject(slug, true)
	if err != nil {
		t.Fatalf("archiveProject --force: %v", err)
	}
	if !result.WorktreeRemoved {
		t.Fatal("the dirty worktree was not removed")
	}
	if pathExists(worktree) {
		t.Fatalf("worktree %s survived a forced archive", worktree)
	}
	if gitx.BranchExists(repo, branch) {
		t.Fatalf("unmerged branch %s survived a forced archive", branch)
	}
	if !pathExists(filepath.Join(project.ArchivedDir(), slug)) {
		t.Fatal("the project was not moved to archived")
	}
	if pathExists(filepath.Join(project.ActiveDir(), slug)) {
		t.Fatal("the project is still active after archiving")
	}
}

func TestRunArchiveKeepsItsTextOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newTestRepo(t)
	slug := "text-archive"
	branch := "user/text-archive"
	worktree := addArchiveWorktree(t, repo, slug, branch)
	writeArchiveManifest(t, slug, repo, branch, worktree)

	out, err := captureStdout(t, func() error { return runArchive(slug, false) })
	if err != nil {
		t.Fatalf("runArchive: %v", err)
	}
	for _, want := range []string{"Archived:", slug, "Worktree removed:", worktree} {
		if !strings.Contains(out, want) {
			t.Fatalf("archive output %q is missing %q", out, want)
		}
	}
}
