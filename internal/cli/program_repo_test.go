package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func createTestRepoAt(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
			"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(path, "README"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-q", "-m", "init")
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResolveProgramItemRepo(t *testing.T) {
	parent := t.TempDir()
	primary := createTestRepoAt(t, filepath.Join(parent, "primary"))
	secondary := createTestRepoAt(t, filepath.Join(parent, "secondary"))
	cwd := filepath.Join(parent, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	relative := createTestRepoAt(t, filepath.Join(cwd, "repos", "relative"))
	symlink := filepath.Join(parent, "secondary-link")
	if err := os.Symlink(secondary, symlink); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		requested string
		want      string
	}{
		{name: "default", want: primary},
		{name: "absolute", requested: secondary, want: secondary},
		{name: "relative path", requested: filepath.Join("repos", "relative"), want: relative},
		{name: "sibling name", requested: "secondary", want: secondary},
		{name: "symlink", requested: symlink, want: secondary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveProgramItemRepo(primary, cwd, tt.requested)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("repo = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveProgramItemRepoRejectsNestedAndMissingPaths(t *testing.T) {
	parent := t.TempDir()
	primary := createTestRepoAt(t, filepath.Join(parent, "primary"))
	nested := filepath.Join(primary, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, requested := range []string{nested, "missing"} {
		t.Run(filepath.Base(requested), func(t *testing.T) {
			_, err := resolveProgramItemRepo(primary, parent, requested)
			if err == nil {
				t.Fatal("resolve succeeded")
			}
			if !strings.Contains(err.Error(), "must be checked out") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
