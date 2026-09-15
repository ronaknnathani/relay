package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotChangesForEveryRepositoryMutation(t *testing.T) {
	repo := initRepo(t)
	base := RevParse(repo, "HEAD")
	initial, err := Snapshot(repo, base)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Snapshot(repo, base)
	if err != nil {
		t.Fatal(err)
	}
	if initial != again {
		t.Fatalf("unchanged snapshot differs:\nfirst %+v\nagain %+v", initial, again)
	}

	readme := filepath.Join(repo, "README")
	if err := os.WriteFile(readme, []byte("hi\nunstaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unstaged := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, initial, unstaged)
	if unstaged.FileCount != 1 || unstaged.ChangedLines != 1 {
		t.Fatalf("unstaged counts = %+v, want 1 file and 1 line", unstaged)
	}

	runGit(t, repo, "add", "README")
	staged := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, initial, staged)

	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	untracked := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, staged, untracked)
	if untracked.FileCount != 2 || untracked.ChangedLines != 3 {
		t.Fatalf("untracked counts = %+v, want 2 files and 3 lines", untracked)
	}
}

func TestSnapshotChangesAfterCommitWithoutWorktreeChanges(t *testing.T) {
	repo := initRepo(t)
	base := RevParse(repo, "HEAD")
	initial := mustSnapshot(t, repo, base)
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hi\ncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README")
	runGit(t, repo, "commit", "-q", "-m", "change")
	committed := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, initial, committed)
	if committed.HeadSHA == initial.HeadSHA {
		t.Fatal("commit did not change HEAD in snapshot")
	}
}

func TestSnapshotTracksUntrackedFileTypeAndExecutableMode(t *testing.T) {
	repo := initRepo(t)
	base := RevParse(repo, "HEAD")
	path := filepath.Join(repo, "tool")
	if err := os.WriteFile(path, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	regular := mustSnapshot(t, repo, base)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, regular, executable)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", path); err != nil {
		t.Fatal(err)
	}
	symlink := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, executable, symlink)
}

func TestSnapshotTreatsUntrackedNestedRepositoryAsOpaqueIdentity(t *testing.T) {
	repo := initRepo(t)
	base := RevParse(repo, "HEAD")
	nested := filepath.Join(repo, "private-repo")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, nested, "init", "-q")
	if err := os.WriteFile(filepath.Join(nested, "secret.txt"), []byte("private content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, nested, "add", "secret.txt")
	runGit(t, nested, "commit", "-q", "-m", "nested")

	first := mustSnapshot(t, repo, base)
	if first.FileCount != 1 || first.ChangedLines != 0 {
		t.Fatalf("nested repository counts = %+v, want 1 opaque entry and 0 content lines", first)
	}
	file, err := readUntracked(repo, "private-repo/")
	if err != nil {
		t.Fatal(err)
	}
	if file.kind != "nested-git-repository" ||
		strings.Contains(string(file.content), "private content") {
		t.Fatalf("nested repository fingerprint input = kind %q content %q", file.kind, file.content)
	}

	if err := os.WriteFile(filepath.Join(nested, "secret.txt"), []byte("different private content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, nested, "add", "secret.txt")
	runGit(t, nested, "commit", "-q", "-m", "nested change")
	second := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, first, second)
}

func TestSnapshotRejectsInvalidRepository(t *testing.T) {
	if _, err := Snapshot(t.TempDir(), "HEAD"); err == nil {
		t.Fatal("Snapshot accepted a non-git directory")
	}
}

func mustSnapshot(t *testing.T, repo, base string) RepoSnapshot {
	t.Helper()
	snapshot, err := Snapshot(repo, base)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertFingerprintChanged(t *testing.T, before, after RepoSnapshot) {
	t.Helper()
	if before.Fingerprint == after.Fingerprint {
		t.Fatalf("fingerprint did not change:\nbefore %+v\nafter %+v", before, after)
	}
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
		"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
