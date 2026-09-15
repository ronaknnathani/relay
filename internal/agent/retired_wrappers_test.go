package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRetiredWrapperCleanupPreservesUserOwnedEntries(t *testing.T) {
	retired := []string{"relay-status", "relay-archive", "todo"}
	for _, name := range retired {
		for _, kind := range []string{"file", "directory", "foreign symlink"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				installed := installedSkillsDir(t, home)
				path := filepath.Join(installed, name)
				foreign := filepath.Join(t.TempDir(), name)
				switch kind {
				case "file":
					if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
						t.Fatal(err)
					}

				case "directory":
					if err := os.MkdirAll(path, 0o755); err != nil {
						t.Fatal(err)
					}
				case "foreign symlink":
					if err := os.MkdirAll(foreign, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(foreign, path); err != nil {
						t.Fatal(err)
					}
				}

				if err := RemoveRetiredSkill(copilot{}, name, SkillSyncOptions{
					PackageDir: t.TempDir(), ManagedRoots: []string{t.TempDir()}, Stdout: &bytes.Buffer{},
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("user-owned %s %s was removed: %v", name, kind, err)
				}
			})
		}
	}
}

func TestRetiredWrapperCleanupPreservesUserSymlinkInsideBroadManagedRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installed := installedSkillsDir(t, home)
	custom := filepath.Join(home, ".relay", "custom", "todo")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(installed, "todo")
	if err := os.Symlink(custom, link); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRetiredSkill(copilot{}, "todo", SkillSyncOptions{
		PackageDir:   filepath.Join(home, ".relay", "agents", "copilot"),
		ManagedRoots: []string{filepath.Join(home, ".relay")},
		Stdout:       &bytes.Buffer{},
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(link); err != nil || got != custom {
		t.Fatalf("user-owned symlink under managed root changed: got %q, err %v", got, err)
	}
}
