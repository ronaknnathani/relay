package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installedSkillsDir(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".copilot", "skills")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir installed skills: %v", err)
	}
	return dir
}

// A retired skill Relay itself installed is removed, so the agent is not left
// with two rival entry points for the same role.
func TestRemoveRetiredSkillRemovesRelayManagedLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pkgDir := t.TempDir()
	writeSkillPackage(t, pkgDir, "cto")
	installed := installedSkillsDir(t, home)
	link := filepath.Join(installed, "cto")
	if err := os.Symlink(filepath.Join(pkgDir, "skills", "cto"), link); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	if err := RemoveRetiredSkill(copilot{}, "cto", SkillSyncOptions{
		PackageDir: pkgDir, ManagedRoots: []string{pkgDir}, Stdout: out,
	}); err != nil {
		t.Fatalf("RemoveRetiredSkill: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("retired link still exists: %v", err)
	}
	if !strings.Contains(out.String(), "removed retired cto") {
		t.Errorf("output = %q, want the removal reported", out)
	}
}

// A symlink pointing outside every managed root belongs to the user. Removal is
// surgical, never a sweep of everything Relay does not recognize.
func TestRemoveRetiredSkillKeepsForeignLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pkgDir := t.TempDir()
	foreign := t.TempDir()
	if err := os.MkdirAll(filepath.Join(foreign, "cto"), 0755); err != nil {
		t.Fatal(err)
	}
	installed := installedSkillsDir(t, home)
	link := filepath.Join(installed, "cto")
	if err := os.Symlink(filepath.Join(foreign, "cto"), link); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	if err := RemoveRetiredSkill(copilot{}, "cto", SkillSyncOptions{
		PackageDir: pkgDir, ManagedRoots: []string{pkgDir}, Stdout: out,
	}); err != nil {
		t.Fatalf("RemoveRetiredSkill: %v", err)
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("foreign link was removed: %v", err)
	}
	if target != filepath.Join(foreign, "cto") {
		t.Errorf("foreign link = %q, want it untouched", target)
	}
	if !strings.Contains(out.String(), "not managed by relay") {
		t.Errorf("output = %q, want the skip explained", out)
	}
}

// A real directory or file of the same name is the user's own skill.
func TestRemoveRetiredSkillKeepsForeignDirectoryAndFile(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.MkdirAll(path, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("# mine\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "file",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("mine\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			pkgDir := t.TempDir()
			installed := installedSkillsDir(t, home)
			path := filepath.Join(installed, "cto")
			test.setup(t, path)

			out := &bytes.Buffer{}
			if err := RemoveRetiredSkill(copilot{}, "cto", SkillSyncOptions{
				PackageDir: pkgDir, ManagedRoots: []string{pkgDir}, Stdout: out,
			}); err != nil {
				t.Fatalf("RemoveRetiredSkill: %v", err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("foreign %s was removed: %v", test.name, err)
			}
			if !strings.Contains(out.String(), "not a relay-managed symlink") {
				t.Errorf("output = %q, want the skip explained", out)
			}
		})
	}
}

// Nothing to remove is not an error: setup must stay idempotent.
func TestRemoveRetiredSkillIsIdempotentWhenAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pkgDir := t.TempDir()
	installedSkillsDir(t, home)

	out := &bytes.Buffer{}
	if err := RemoveRetiredSkill(copilot{}, "cto", SkillSyncOptions{
		PackageDir: pkgDir, ManagedRoots: []string{pkgDir}, Stdout: out,
	}); err != nil {
		t.Fatalf("RemoveRetiredSkill: %v", err)
	}
	if out.String() != "" {
		t.Errorf("output = %q, want nothing reported", out)
	}
}

// A missing skills directory is also not an error.
func TestRemoveRetiredSkillToleratesMissingSkillsDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := RemoveRetiredSkill(copilot{}, "cto", SkillSyncOptions{
		PackageDir: t.TempDir(), ManagedRoots: []string{t.TempDir()},
	}); err != nil {
		t.Fatalf("RemoveRetiredSkill: %v", err)
	}
}
