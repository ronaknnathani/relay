package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const generatedAgentsMD = "# relay\n\nActive relay project: gitx-test. Workflow: deliver-pr. Mode: full.\n"

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

func TestWorktreeHasOnlyRelayGeneratedAgentsMD(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, repo string)
		want  bool
	}{
		{
			name: "untracked generated root agents",
			setup: func(t *testing.T, repo string) {
				writeGitxFile(t, repo, "AGENTS.md", generatedAgentsMD)
			},
			want: true,
		},
		{
			name:  "clean status",
			setup: func(t *testing.T, repo string) {},
			want:  false,
		},
		{
			name: "generated plus other dirty file",
			setup: func(t *testing.T, repo string) {
				writeGitxFile(t, repo, "AGENTS.md", generatedAgentsMD)
				writeGitxFile(t, repo, "notes.txt", "keep\n")
			},
			want: false,
		},
		{
			name: "subdirectory agents",
			setup: func(t *testing.T, repo string) {
				writeGitxFile(t, repo, filepath.Join("docs", "AGENTS.md"), generatedAgentsMD)
			},
			want: false,
		},
		{
			name: "non generated root agents",
			setup: func(t *testing.T, repo string) {
				writeGitxFile(t, repo, "AGENTS.md", "# project\n\nKeep this.\n")
			},
			want: false,
		},
		{
			name: "non generated head content",
			setup: func(t *testing.T, repo string) {
				writeGitxFile(t, repo, "AGENTS.md", "# project\n\nKeep this.\n")
				runGitx(t, repo, "add", "AGENTS.md")
				runGitx(t, repo, "commit", "-q", "-m", "add agents")
				writeGitxFile(t, repo, "AGENTS.md", generatedAgentsMD)
			},
			want: false,
		},
		{
			name: "non generated index content",
			setup: func(t *testing.T, repo string) {
				writeGitxFile(t, repo, "AGENTS.md", "# project\n\nKeep this.\n")
				runGitx(t, repo, "add", "AGENTS.md")
				writeGitxFile(t, repo, "AGENTS.md", generatedAgentsMD)
			},
			want: false,
		},
		{
			name: "staged generated content",
			setup: func(t *testing.T, repo string) {
				writeGitxFile(t, repo, "AGENTS.md", generatedAgentsMD)
				runGitx(t, repo, "add", "AGENTS.md")
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initRepo(t)
			tt.setup(t, repo)
			got, err := WorktreeHasOnlyRelayGeneratedAgentsMD(repo)
			if err != nil {
				t.Fatalf("WorktreeHasOnlyRelayGeneratedAgentsMD: %v", err)
			}
			if got != tt.want {
				t.Fatalf("WorktreeHasOnlyRelayGeneratedAgentsMD() = %v, want %v", got, tt.want)
			}
		})
	}
}

func writeGitxFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create parent dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGitx(t *testing.T, repo string, args ...string) {
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
