package gitx

import (
	"crypto/sha256"
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

func TestSnapshotFramesNULContainingUntrackedContentUnambiguously(t *testing.T) {
	repo := initRepo(t)
	base := RevParse(repo, "HEAD")
	header := func(path string) []byte {
		return []byte("\x00untracked\x00" + path + "\x00regular\x00")
	}
	if err := os.WriteFile(filepath.Join(repo, "a"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	firstB := append([]byte("B"), header("c")...)
	firstB = append(firstB, 'C')
	if err := os.WriteFile(filepath.Join(repo, "b"), firstB, 0o644); err != nil {
		t.Fatal(err)
	}
	first := mustSnapshot(t, repo, base)

	if err := os.Remove(filepath.Join(repo, "b")); err != nil {
		t.Fatal(err)
	}
	secondA := append([]byte("A"), header("b")...)
	secondA = append(secondA, 'B')
	if err := os.WriteFile(filepath.Join(repo, "a"), secondA, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "c"), []byte("C"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := mustSnapshot(t, repo, base)

	if first.FileCount != second.FileCount || first.ChangedLines != second.ChangedLines {
		t.Fatalf("collision fixtures changed aggregate counts: first=%+v second=%+v", first, second)
	}
	assertFingerprintChanged(t, first, second)
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
		len(file.content) != sha256.Size*2 ||
		strings.Contains(string(file.content), "private content") ||
		strings.Contains(string(file.content), "secret.txt") {
		t.Fatalf("nested repository fingerprint input = kind %q content %q", file.kind, file.content)
	}

	if err := os.WriteFile(filepath.Join(nested, "secret.txt"), []byte("different private content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unstaged := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, first, unstaged)

	if err := os.WriteFile(filepath.Join(nested, "secret.txt"), []byte("different private content again\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repeatedUnstaged := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, unstaged, repeatedUnstaged)

	runGit(t, nested, "add", "secret.txt")
	staged := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, repeatedUnstaged, staged)

	if err := os.WriteFile(filepath.Join(nested, "secret.txt"), []byte("staged plus worktree content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stagedAndUnstaged := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, staged, stagedAndUnstaged)

	runGit(t, nested, "add", "secret.txt")
	restaged := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, stagedAndUnstaged, restaged)

	if err := os.WriteFile(filepath.Join(nested, "local.txt"), []byte("untracked private content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	untracked := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, restaged, untracked)

	if err := os.WriteFile(filepath.Join(nested, "local.txt"), []byte("different untracked private content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repeatedUntracked := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, untracked, repeatedUntracked)

	runGit(t, nested, "commit", "-q", "-m", "nested change")
	committed := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, repeatedUntracked, committed)
}

func TestSnapshotDoesNotExecuteTextconv(t *testing.T) {
	repo := initRepo(t)
	marker := filepath.Join(repo, "textconv-ran")
	script := filepath.Join(repo, "textconv.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch \"$1\"\ncat \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "config", "diff.relay.textconv", script+" "+marker)
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("README diff=relay\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".gitattributes")
	runGit(t, repo, "commit", "-q", "-m", "configure textconv")
	base := RevParse(repo, "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Snapshot(repo, base); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("snapshot executed configured textconv command: %v", err)
	}
}

func TestSnapshotDoesNotExecuteExternalDiff(t *testing.T) {
	repo := initRepo(t)
	marker := filepath.Join(repo, "external-diff-ran")
	script := filepath.Join(repo, "external-diff.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "config", "diff.external", script+" "+marker)
	t.Setenv("GIT_EXTERNAL_DIFF", script+" "+marker)
	base := RevParse(repo, "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Snapshot(repo, base); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("snapshot executed external diff command: %v", err)
	}
}

func TestSnapshotChangesForRepeatedTrackedSubmoduleEdits(t *testing.T) {
	repo := initRepo(t)
	nestedSource := t.TempDir()
	runGit(t, nestedSource, "init", "-q")
	if err := os.WriteFile(filepath.Join(nestedSource, "tracked.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, nestedSource, "add", "tracked.txt")
	runGit(t, nestedSource, "commit", "-q", "-m", "initial")
	runGit(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", nestedSource, "nested")
	runGit(t, repo, "commit", "-q", "-am", "add submodule")
	base := RevParse(repo, "HEAD")
	nested := filepath.Join(repo, "nested")

	initial := mustSnapshot(t, repo, base)
	if err := os.WriteFile(filepath.Join(nested, "tracked.txt"), []byte("first edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, initial, first)

	if err := os.WriteFile(filepath.Join(nested, "tracked.txt"), []byte("second edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := mustSnapshot(t, repo, base)
	assertFingerprintChanged(t, first, second)
}

func TestSnapshotAllowsUninitializedTrackedSubmodule(t *testing.T) {
	repo := initRepo(t)
	nestedSource := t.TempDir()
	runGit(t, nestedSource, "init", "-q")
	if err := os.WriteFile(filepath.Join(nestedSource, "tracked.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, nestedSource, "add", "tracked.txt")
	runGit(t, nestedSource, "commit", "-q", "-m", "initial")
	runGit(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", nestedSource, "nested")
	runGit(t, repo, "commit", "-q", "-am", "add submodule")
	base := RevParse(repo, "HEAD")
	runGit(t, repo, "submodule", "deinit", "-q", "-f", "nested")

	first := mustSnapshot(t, repo, base)
	second := mustSnapshot(t, repo, base)
	if first != second {
		t.Fatalf("unchanged uninitialized submodule snapshot differs:\nfirst %+v\nsecond %+v", first, second)
	}
}

func TestSnapshotBaseRefPrefersCurrentBaseOverStartSHA(t *testing.T) {
	repo := initRepo(t)
	start := RevParse(repo, "HEAD")
	runGit(t, repo, "branch", "-M", "main")
	runGit(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "feature.txt")
	runGit(t, repo, "commit", "-q", "-m", "feature")
	runGit(t, repo, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "base.txt")
	runGit(t, repo, "commit", "-q", "-m", "advance base")
	runGit(t, repo, "checkout", "-q", "feature")
	runGit(t, repo, "rebase", "-q", "main")

	baseRef := SnapshotBaseRef(repo, "main", start)
	if baseRef != "main" {
		t.Fatalf("snapshot base ref = %q, want main", baseRef)
	}
	snapshot := mustSnapshot(t, repo, baseRef)
	if snapshot.FileCount != 1 || snapshot.ChangedLines != 1 {
		t.Fatalf("rebased snapshot = %+v, want only feature change", snapshot)
	}
}

func TestSnapshotBaseRefPrefersRemoteWhenLocalBaseDiverges(t *testing.T) {
	repo := initRepo(t)
	runGit(t, repo, "branch", "-M", "main")
	remote := filepath.Join(t.TempDir(), "origin.git")
	if output, err := exec.Command("git", "init", "--bare", "-q", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, output)
	}
	runGit(t, repo, "remote", "add", "origin", remote)
	runGit(t, repo, "push", "-q", "-u", "origin", "main")
	remoteSHA := RevParse(repo, "origin/main")

	if err := os.WriteFile(filepath.Join(repo, "local-only.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "local-only.txt")
	runGit(t, repo, "commit", "-q", "-m", "local main diverges")

	if got := SnapshotBaseRef(repo, "main", remoteSHA); got != "origin/main" {
		t.Fatalf("snapshot base ref = %q, want origin/main", got)
	}
}

func TestSnapshotBaseRefResolvesHEADToStartSHA(t *testing.T) {
	repo := initRepo(t)
	start := RevParse(repo, "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "later.txt")
	runGit(t, repo, "commit", "-q", "-m", "later")

	if got := SnapshotBaseRef(repo, "HEAD", start); got != start {
		t.Fatalf("snapshot base ref = %q, want immutable start %q", got, start)
	}
	snapshot := mustSnapshot(t, repo, SnapshotBaseRef(repo, "HEAD", start))
	if snapshot.FileCount != 1 || snapshot.ChangedLines != 1 {
		t.Fatalf("HEAD-based snapshot = %+v, want committed work since start", snapshot)
	}
}

func TestSnapshotFingerprintTracksBaseTipBeyondMergeBase(t *testing.T) {
	repo := initRepo(t)
	runGit(t, repo, "branch", "-M", "main")
	runGit(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "feature.txt")
	runGit(t, repo, "commit", "-q", "-m", "feature")
	before := mustSnapshot(t, repo, "main")

	runGit(t, repo, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "base.txt")
	runGit(t, repo, "commit", "-q", "-m", "advance base")
	runGit(t, repo, "checkout", "-q", "feature")
	after := mustSnapshot(t, repo, "main")

	if before.BaseSHA != after.BaseSHA {
		t.Fatalf("merge base changed unexpectedly: before=%s after=%s", before.BaseSHA, after.BaseSHA)
	}
	if before.BaseTipSHA == after.BaseTipSHA {
		t.Fatal("base tip advancement was not recorded")
	}
	assertFingerprintChanged(t, before, after)
}

func TestSnapshotIgnoresConfiguredSubmoduleExclusion(t *testing.T) {
	repo := initRepo(t)
	nestedSource := t.TempDir()
	runGit(t, nestedSource, "init", "-q")
	if err := os.WriteFile(filepath.Join(nestedSource, "tracked.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, nestedSource, "add", "tracked.txt")
	runGit(t, nestedSource, "commit", "-q", "-m", "initial")
	runGit(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", nestedSource, "nested")
	runGit(t, repo, "commit", "-q", "-am", "add submodule")
	base := RevParse(repo, "HEAD")
	runGit(t, repo, "config", "diff.ignoreSubmodules", "all")

	nested := filepath.Join(repo, "nested")
	if err := os.WriteFile(filepath.Join(nested, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, nested, "add", "tracked.txt")
	runGit(t, nested, "commit", "-q", "-m", "change")
	runGit(t, repo, "add", "nested")

	snapshot := mustSnapshot(t, repo, base)
	if snapshot.FileCount != 1 || snapshot.ChangedLines != 2 {
		t.Fatalf("submodule snapshot = %+v, want one changed gitlink", snapshot)
	}
}

func TestSnapshotRejectsInvalidRepository(t *testing.T) {
	if _, err := Snapshot(t.TempDir(), "HEAD"); err == nil {
		t.Fatal("Snapshot accepted a non-git directory")
	}
}

func TestSnapshotPropagatesTrackedSubmoduleStatFailure(t *testing.T) {
	repo := initRepo(t)
	nestedSource := t.TempDir()
	runGit(t, nestedSource, "init", "-q")
	if err := os.WriteFile(filepath.Join(nestedSource, "tracked.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, nestedSource, "add", "tracked.txt")
	runGit(t, nestedSource, "commit", "-q", "-m", "initial")
	runGit(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", nestedSource, "nested")
	runGit(t, repo, "commit", "-q", "-am", "add nested")

	original := snapshotStat
	snapshotStat = func(path string) (os.FileInfo, error) {
		if filepath.Base(path) == "nested" {
			return nil, os.ErrPermission
		}
		return original(path)
	}
	t.Cleanup(func() { snapshotStat = original })
	if _, err := Snapshot(repo, RevParse(repo, "HEAD")); err == nil ||
		!strings.Contains(err.Error(), "inspect tracked") {
		t.Fatalf("tracked submodule stat error = %v", err)
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
