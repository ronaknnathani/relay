package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
)

// Setup installs the canonical tech-lead skill and clears the retired link it
// replaces, so the agent is never offered two entry points for one role.
func TestSetupInstallsTLAndRemovesRelayManagedCTOLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "tester")
	source := writeSetupSource(t, "tl")
	if _, err := runSetup(t, "copilot", "--src", source); err != nil {
		t.Fatalf("setup copilot: %v", err)
	}

	installedDir := filepath.Join(home, ".copilot", "skills")
	staleTarget := filepath.Join(agent.PackageDir("copilot"), "skills", "cto")
	if err := os.MkdirAll(staleTarget, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(staleTarget, filepath.Join(installedDir, "cto")); err != nil {
		t.Fatal(err)
	}

	out, err := runSetup(t, "copilot", "--src", source)
	if err != nil {
		t.Fatalf("setup copilot: %v\n%s", err, out)
	}
	wantTL := filepath.Join(agent.PackageDir("copilot"), "skills", "tl")
	if got, err := os.Readlink(filepath.Join(installedDir, "tl")); err != nil || got != wantTL {
		t.Fatalf("installed tl link = %q, %v; want %q", got, err, wantTL)
	}
	if _, err := os.Lstat(filepath.Join(installedDir, "cto")); !os.IsNotExist(err) {
		t.Fatalf("retired cto link still installed: %v", err)
	}
	if !strings.Contains(out, "removed retired cto") {
		t.Errorf("setup output = %q, want the retired removal reported", out)
	}
}

// Foreign directories, files, and foreign symlinks that happen to share the
// retired name are the user's own and survive setup untouched.
func TestSetupPreservesForeignCTOEntries(t *testing.T) {
	tests := []struct {
		name   string
		create func(t *testing.T, path, foreign string)
		verify func(t *testing.T, path, foreign string)
	}{
		{
			name: "foreign symlink",
			create: func(t *testing.T, path, foreign string) {
				t.Helper()
				if err := os.MkdirAll(foreign, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreign, path); err != nil {
					t.Fatal(err)
				}
			},
			verify: func(t *testing.T, path, foreign string) {
				t.Helper()
				got, err := os.Readlink(path)
				if err != nil || got != foreign {
					t.Fatalf("foreign cto link = %q, %v; want %q", got, err, foreign)
				}
			},
		},
		{
			name: "foreign directory",
			create: func(t *testing.T, path, _ string) {
				t.Helper()
				if err := os.MkdirAll(path, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("# mine\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			verify: func(t *testing.T, path, _ string) {
				t.Helper()
				if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
					t.Fatalf("foreign cto directory was disturbed: %v", err)
				}
			},
		},
		{
			name: "foreign file",
			create: func(t *testing.T, path, _ string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("mine\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			verify: func(t *testing.T, path, _ string) {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "mine\n" {
					t.Fatalf("foreign cto file = %q, %v; want it untouched", data, err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USER", "tester")
			source := writeSetupSource(t, "tl")
			installedDir := filepath.Join(home, ".copilot", "skills")
			if err := os.MkdirAll(installedDir, 0755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(installedDir, "cto")
			foreign := filepath.Join(t.TempDir(), "cto")
			test.create(t, path, foreign)

			if _, err := runSetup(t, "copilot", "--src", source); err != nil {
				t.Fatalf("setup copilot: %v", err)
			}
			test.verify(t, path, foreign)
		})
	}
}

// Uninstall also clears the retired managed link so no dangling relay-owned
// symlink survives a removal.
func TestSetupUninstallRemovesRetiredCTOLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "tester")
	source := writeSetupSource(t, "tl")
	if _, err := runSetup(t, "copilot", "--src", source); err != nil {
		t.Fatalf("setup copilot: %v", err)
	}
	installedDir := filepath.Join(home, ".copilot", "skills")
	staleTarget := filepath.Join(agent.PackageDir("copilot"), "skills", "cto")
	if err := os.MkdirAll(staleTarget, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(staleTarget, filepath.Join(installedDir, "cto")); err != nil {
		t.Fatal(err)
	}

	if _, err := runSetup(t, "copilot", "--uninstall", "--src", source); err != nil {
		t.Fatalf("setup copilot --uninstall: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(installedDir, "cto")); !os.IsNotExist(err) {
		t.Fatalf("retired cto link survived uninstall: %v", err)
	}
}
