package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeSkillPackage(t *testing.T, pkgDir string, names ...string) {
	t.Helper()
	for _, name := range names {
		skillDir := filepath.Join(pkgDir, "skills", name)
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			t.Fatalf("mkdir skill: %v", err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+name+"\n"), 0644); err != nil {
			t.Fatalf("write skill: %v", err)
		}
	}
}

func readSkillLink(t *testing.T, home, name string) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(home, ".copilot", "skills", name))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	return target
}

func TestLinkSkillsCreatesSymlinks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pkgDir := t.TempDir()
	writeSkillPackage(t, pkgDir, "plan")

	if err := LinkSkills(copilot{}, SkillSyncOptions{
		PackageDir:   pkgDir,
		ManagedRoots: []string{pkgDir},
		Stdout:       &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("LinkSkills: %v", err)
	}

	want := filepath.Join(pkgDir, "skills", "plan")
	if got := readSkillLink(t, home, "plan"); got != want {
		t.Errorf("skill link = %q, want %q", got, want)
	}
}

func TestLinkSkillsSkipsNonSymlinkTargets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pkgDir := t.TempDir()
	writeSkillPackage(t, pkgDir, "plan")
	installed := filepath.Join(home, ".copilot", "skills", "plan")
	if err := os.MkdirAll(installed, 0755); err != nil {
		t.Fatalf("mkdir installed: %v", err)
	}

	if err := LinkSkills(copilot{}, SkillSyncOptions{
		PackageDir:   pkgDir,
		ManagedRoots: []string{pkgDir},
		Stdout:       &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("LinkSkills: %v", err)
	}

	if info, err := os.Lstat(installed); err != nil || !info.IsDir() {
		t.Fatalf("installed target = %v, %v; want untouched directory", info, err)
	}
}

func TestLinkSkillsReplacesManagedSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pkgDir := t.TempDir()
	writeSkillPackage(t, pkgDir, "plan")
	managedRoot := t.TempDir()
	oldTarget := filepath.Join(managedRoot, "old", "plan")
	if err := os.MkdirAll(oldTarget, 0755); err != nil {
		t.Fatalf("mkdir old target: %v", err)
	}
	installed := filepath.Join(home, ".copilot", "skills", "plan")
	if err := os.MkdirAll(filepath.Dir(installed), 0755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.Symlink(oldTarget, installed); err != nil {
		t.Fatalf("symlink old target: %v", err)
	}

	if err := LinkSkills(copilot{}, SkillSyncOptions{
		PackageDir:   pkgDir,
		ManagedRoots: []string{managedRoot},
		Stdout:       &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("LinkSkills: %v", err)
	}

	want := filepath.Join(pkgDir, "skills", "plan")
	if got := readSkillLink(t, home, "plan"); got != want {
		t.Errorf("skill link = %q, want %q", got, want)
	}
}

func TestLinkSkillsKeepsForeignSymlinkNonInteractive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pkgDir := t.TempDir()
	writeSkillPackage(t, pkgDir, "plan")
	foreign := filepath.Join(t.TempDir(), "plan")
	if err := os.MkdirAll(foreign, 0755); err != nil {
		t.Fatalf("mkdir foreign: %v", err)
	}
	installed := filepath.Join(home, ".copilot", "skills", "plan")
	if err := os.MkdirAll(filepath.Dir(installed), 0755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.Symlink(foreign, installed); err != nil {
		t.Fatalf("symlink foreign: %v", err)
	}

	if err := LinkSkills(copilot{}, SkillSyncOptions{
		PackageDir:      pkgDir,
		ManagedRoots:    []string{pkgDir},
		Stdout:          &bytes.Buffer{},
		StdinIsTerminal: false,
	}); err != nil {
		t.Fatalf("LinkSkills: %v", err)
	}

	if got := readSkillLink(t, home, "plan"); got != foreign {
		t.Errorf("skill link = %q, want foreign %q", got, foreign)
	}
}

func TestLinkSkillsTTYDefaultNoKeepsForeignSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pkgDir := t.TempDir()
	writeSkillPackage(t, pkgDir, "plan")
	foreign := filepath.Join(t.TempDir(), "plan")
	if err := os.MkdirAll(foreign, 0755); err != nil {
		t.Fatalf("mkdir foreign: %v", err)
	}
	installed := filepath.Join(home, ".copilot", "skills", "plan")
	if err := os.MkdirAll(filepath.Dir(installed), 0755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.Symlink(foreign, installed); err != nil {
		t.Fatalf("symlink foreign: %v", err)
	}

	if err := LinkSkills(copilot{}, SkillSyncOptions{
		PackageDir:      pkgDir,
		ManagedRoots:    []string{pkgDir},
		Stdin:           bytes.NewBufferString("\n"),
		Stdout:          &bytes.Buffer{},
		StdinIsTerminal: true,
	}); err != nil {
		t.Fatalf("LinkSkills: %v", err)
	}

	if got := readSkillLink(t, home, "plan"); got != foreign {
		t.Errorf("skill link = %q, want foreign %q", got, foreign)
	}
}

func TestLinkSkillsTTYYesReplacesForeignSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pkgDir := t.TempDir()
	writeSkillPackage(t, pkgDir, "plan")
	foreign := filepath.Join(t.TempDir(), "plan")
	if err := os.MkdirAll(foreign, 0755); err != nil {
		t.Fatalf("mkdir foreign: %v", err)
	}
	installed := filepath.Join(home, ".copilot", "skills", "plan")
	if err := os.MkdirAll(filepath.Dir(installed), 0755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.Symlink(foreign, installed); err != nil {
		t.Fatalf("symlink foreign: %v", err)
	}

	if err := LinkSkills(copilot{}, SkillSyncOptions{
		PackageDir:      pkgDir,
		ManagedRoots:    []string{pkgDir},
		Stdin:           bytes.NewBufferString("yes\n"),
		Stdout:          &bytes.Buffer{},
		StdinIsTerminal: true,
	}); err != nil {
		t.Fatalf("LinkSkills: %v", err)
	}

	want := filepath.Join(pkgDir, "skills", "plan")
	if got := readSkillLink(t, home, "plan"); got != want {
		t.Errorf("skill link = %q, want %q", got, want)
	}
}

func TestUnlinkSkillsRemovesOnlyManagedSymlinks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pkgDir := t.TempDir()
	writeSkillPackage(t, pkgDir, "managed", "foreign", "real")
	managedRoot := t.TempDir()
	managedTarget := filepath.Join(managedRoot, "skills", "managed")
	foreignTarget := filepath.Join(t.TempDir(), "skills", "foreign")
	for _, path := range []string{managedTarget, foreignTarget} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
	}
	skillsDir := filepath.Join(home, ".copilot", "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.Symlink(managedTarget, filepath.Join(skillsDir, "managed")); err != nil {
		t.Fatalf("symlink managed: %v", err)
	}
	if err := os.Symlink(foreignTarget, filepath.Join(skillsDir, "foreign")); err != nil {
		t.Fatalf("symlink foreign: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(skillsDir, "real"), 0755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}

	if err := UnlinkSkills(copilot{}, SkillSyncOptions{
		PackageDir:   pkgDir,
		ManagedRoots: []string{managedRoot},
		Stdout:       &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("UnlinkSkills: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(skillsDir, "managed")); !os.IsNotExist(err) {
		t.Errorf("managed link still exists: %v", err)
	}
	if got := readSkillLink(t, home, "foreign"); got != foreignTarget {
		t.Errorf("foreign link = %q, want %q", got, foreignTarget)
	}
	if info, err := os.Lstat(filepath.Join(skillsDir, "real")); err != nil || !info.IsDir() {
		t.Fatalf("real target = %v, %v; want untouched directory", info, err)
	}
}

func TestSymlinkTargetsWithDotDotAreNeverManaged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pkgDir := t.TempDir()
	writeSkillPackage(t, pkgDir, "plan")
	managedRoot := t.TempDir()
	rawDotDotTarget := managedRoot + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "outside" + string(os.PathSeparator) + "plan"
	installed := filepath.Join(home, ".copilot", "skills", "plan")
	if err := os.MkdirAll(filepath.Dir(installed), 0755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.Symlink(rawDotDotTarget, installed); err != nil {
		t.Fatalf("symlink dotdot target: %v", err)
	}

	if err := LinkSkills(copilot{}, SkillSyncOptions{
		PackageDir:   pkgDir,
		ManagedRoots: []string{managedRoot},
		Stdout:       &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("LinkSkills: %v", err)
	}
	if got := readSkillLink(t, home, "plan"); got != rawDotDotTarget {
		t.Errorf("link with .. target = %q, want kept %q", got, rawDotDotTarget)
	}

	if err := UnlinkSkills(copilot{}, SkillSyncOptions{
		PackageDir:   pkgDir,
		ManagedRoots: []string{managedRoot},
		Stdout:       &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("UnlinkSkills: %v", err)
	}
	if got := readSkillLink(t, home, "plan"); got != rawDotDotTarget {
		t.Errorf("unlink with .. target = %q, want kept %q", got, rawDotDotTarget)
	}
}
