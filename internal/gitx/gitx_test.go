package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a throwaway git repo with one commit and returns its root.
func initRepo(t *testing.T) string {
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
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README")
	run("commit", "-q", "-m", "init")
	return repo
}

func TestWorktreeRemoveRegistered(t *testing.T) {
	repo := initRepo(t)
	dir := filepath.Join(repo, ".worktrees", "wt")
	cmd := exec.Command("git", "-C", repo, "worktree", "add", "-q", dir, "-b", "feature", "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	if registered, err := IsWorktree(repo, dir); err != nil || !registered {
		t.Fatalf("IsWorktree = %v, %v; want true, nil", registered, err)
	}
	if err := WorktreeRemove(repo, dir, false); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present: %v", err)
	}
}

// TestWorktreeRemoveMissingWorktree reproduces the interrupted-setup case: the
// manifest points at a path that git does not consider a working tree. Removal
// must succeed rather than failing with "is not a working tree".
func TestWorktreeRemoveMissingWorktree(t *testing.T) {
	repo := initRepo(t)
	dir := filepath.Join(repo, ".worktrees", "never-registered")

	// Case 1: the directory does not exist at all.
	if err := WorktreeRemove(repo, dir, false); err != nil {
		t.Fatalf("WorktreeRemove (absent dir): %v", err)
	}

	// Case 2: a leftover directory exists but was never registered as a worktree.
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir leftover: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale"), []byte("x"), 0644); err != nil {
		t.Fatalf("write leftover: %v", err)
	}
	if err := WorktreeRemove(repo, dir, false); err != nil {
		t.Fatalf("WorktreeRemove (leftover dir): %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("leftover dir still present: %v", err)
	}
}

func TestFetchUpdatesRemoteTrackingBase(t *testing.T) {
	for _, base := range []string{"main", "master"} {
		t.Run(base, func(t *testing.T) {
			_, source, repo := initRemoteRepo(t, base)
			initial := gitOutput(t, repo, "rev-parse", "origin/"+base)
			advanceRepo(t, source, "upstream.txt", "upstream\n", "advance upstream")
			upstream := gitOutput(t, source, "rev-parse", base)
			if initial == upstream {
				t.Fatal("upstream did not advance")
			}

			if _, err := Fetch(repo, base); err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if got := gitOutput(t, repo, "rev-parse", "origin/"+base); got != upstream {
				t.Fatalf("origin/%s = %s, want %s", base, got, upstream)
			}
		})
	}
}

func TestFetchReplacesStaleRemoteTrackingRefWithoutLocalBase(t *testing.T) {
	_, source, repo := initRemoteRepo(t, "main")
	initial := gitOutput(t, source, "rev-parse", "main")
	advanceRepo(t, source, "upstream.txt", "upstream\n", "advance upstream")
	if _, err := Fetch(repo, "main"); err != nil {
		t.Fatalf("initial Fetch: %v", err)
	}
	runGit(t, repo, "checkout", "-q", "--detach", "origin/main")
	runGit(t, repo, "branch", "-D", "main")
	runGit(t, source, "reset", "--hard", initial)
	runGit(t, source, "push", "--force", "origin", "main")

	if _, err := Fetch(repo, "main"); err != nil {
		t.Fatalf("Fetch after force-push: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", "origin/main"); got != initial {
		t.Fatalf("origin/main = %s, want force-updated %s", got, initial)
	}
	if BranchExists(repo, "main") {
		t.Fatal("Fetch recreated the absent local main branch")
	}
}

func TestOriginURLPreservesGitDiagnostic(t *testing.T) {
	repo := initRepo(t)

	_, err := OriginURL(repo)
	if err == nil {
		t.Fatal("OriginURL error = nil")
	}
	for _, want := range []string{"git remote get-url origin", "No such remote"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("OriginURL error %q is missing %q", err, want)
		}
	}
}

func TestSanitizeDiagnosticRedactsGitURLUserinfo(t *testing.T) {
	input := strings.Join([]string{
		"fatal: unable to access 'https://relay:secret@example.com/repo.git/': denied",
		"remote ftp://ftp-token@example.org/team/repo.git",
		"remote ssh://ssh-token@git.example.net/team/repo.git",
		"remote scp-token@git.example.io:team/repo.git",
	}, "\n")
	got := SanitizeDiagnostic(input)
	for _, secret := range []string{"relay", "secret", "ftp-token", "ssh-token", "scp-token"} {
		if strings.Contains(got, secret) {
			t.Fatalf("SanitizeDiagnostic(%q) leaked %q in %q", input, secret, got)
		}
	}
	for _, want := range []string{
		"https://[redacted]@example.com/repo.git",
		"ftp://[redacted]@example.org/team/repo.git",
		"ssh://[redacted]@git.example.net/team/repo.git",
		"[redacted]@git.example.io:team/repo.git",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("SanitizeDiagnostic(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSanitizeDiagnosticRedactsGitURLQueryAndFragment(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "https",
			input: "fatal: unable to access 'https://example.com/repo.git?access_token=secret#scope': denied",
			want:  "fatal: unable to access 'https://example.com/repo.git': denied",
		},
		{
			name:  "http",
			input: "remote: http://example.com/team/repo.git?token=secret#scope",
			want:  "remote: http://example.com/team/repo.git",
		},
		{
			name:  "ftp",
			input: "remote: ftp://example.org/team/repo.git?password=secret#scope",
			want:  "remote: ftp://example.org/team/repo.git",
		},
		{
			name:  "ftps",
			input: "remote: ftps://example.org/team/repo.git?password=secret#scope",
			want:  "remote: ftps://example.org/team/repo.git",
		},
		{
			name:  "git",
			input: "remote: git://git.example.net/team/repo.git?token=secret#scope",
			want:  "remote: git://git.example.net/team/repo.git",
		},
		{
			name:  "ssh",
			input: "remote: ssh://git.example.net/team/repo.git?identity=secret#scope",
			want:  "remote: ssh://git.example.net/team/repo.git",
		},
		{
			name:  "file",
			input: "remote: file:///tmp/repo.git?credential=secret#scope",
			want:  "remote: file:///tmp/repo.git",
		},
		{
			name:  "scp-like ssh",
			input: "remote: git@git.example.io:team/repo.git?identity=secret#scope",
			want:  "remote: [redacted]@git.example.io:team/repo.git",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SanitizeDiagnostic(test.input); got != test.want {
				t.Fatalf("SanitizeDiagnostic(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestWorkMerged(t *testing.T) {
	repo := initRepo(t)
	start := gitOutput(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", "-b", "merged-work")
	if err := os.WriteFile(filepath.Join(repo, "merged.txt"), []byte("merged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "merged.txt")
	runGit(t, repo, "commit", "-q", "-m", "merged work")
	runGit(t, repo, "checkout", "-q", "-")
	runGit(t, repo, "merge", "-q", "--ff-only", "merged-work")

	merged, err := WorkMerged(repo, "merged-work", "HEAD", start)
	if err != nil {
		t.Fatalf("WorkMerged: %v", err)
	}
	if !merged {
		t.Fatal("WorkMerged = false, want true")
	}
}

func TestWorkMergedReturnsFalseForNormalUnmergedStates(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	start := gitOutput(t, repo, "rev-parse", base)
	runGit(t, repo, "checkout", "-q", "-b", "unmerged-work")
	if err := os.WriteFile(filepath.Join(repo, "unmerged.txt"), []byte("unmerged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "unmerged.txt")
	runGit(t, repo, "commit", "-q", "-m", "unmerged work")
	runGit(t, repo, "checkout", "-q", base)

	for _, branch := range []string{"unmerged-work", "missing-work"} {
		t.Run(branch, func(t *testing.T) {
			merged, err := WorkMerged(repo, branch, base, start)
			if err != nil {
				t.Fatalf("WorkMerged: %v", err)
			}
			if merged {
				t.Fatal("WorkMerged = true, want false")
			}
		})
	}
}

func TestWorkMergedReturnsEvaluationError(t *testing.T) {
	repo := initRepo(t)
	base := currentBranchInRepo(t, repo)
	start := gitOutput(t, repo, "rev-parse", base)
	runGit(t, repo, "checkout", "-q", "-b", "work")
	if err := os.WriteFile(filepath.Join(repo, "work.txt"), []byte("work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "work.txt")
	runGit(t, repo, "commit", "-q", "-m", "work")
	runGit(t, repo, "checkout", "-q", base)

	merged, err := WorkMerged(repo, "work", "missing-base", start)
	if err == nil {
		t.Fatal("WorkMerged error = nil, want invalid base diagnostic")
	}
	if merged {
		t.Fatal("WorkMerged = true after evaluation error")
	}
	if !strings.Contains(err.Error(), "git merge-base --is-ancestor work missing-base") {
		t.Fatalf("WorkMerged error = %q", err)
	}
	if IsWorkMerged(repo, "work", "missing-base", start) {
		t.Fatal("IsWorkMerged should remain conservative on evaluation error")
	}
}

func currentBranchInRepo(t *testing.T, repo string) string {
	t.Helper()
	return gitOutput(t, repo, "branch", "--show-current")
}

func initRemoteRepo(t *testing.T, base string) (remote, source, repo string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "origin.git")
	source = filepath.Join(root, "source")
	repo = filepath.Join(root, "repo")
	runGit(t, root, "init", "-q", "--bare", "--initial-branch="+base, remote)
	runGit(t, root, "init", "-q", "-b", base, source)
	if err := os.WriteFile(filepath.Join(source, "README"), []byte("initial\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README")
	runGit(t, source, "commit", "-q", "-m", "initial")
	runGit(t, source, "remote", "add", "origin", remote)
	runGit(t, source, "push", "-q", "-u", "origin", base)
	runGit(t, root, "clone", "-q", remote, repo)
	return remote, source, repo
}

func advanceRepo(t *testing.T, repo, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", name)
	runGit(t, repo, "commit", "-q", "-m", message)
	runGit(t, repo, "push", "-q", "origin", "HEAD")
}

func runGit(t *testing.T, dir string, args ...string) {
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

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return strings.TrimSpace(string(out))
}
