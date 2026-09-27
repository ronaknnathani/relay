package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
)

func TestSetupAndUninstallRemoveEveryRelayManagedRetiredSkill(t *testing.T) {
	for _, operation := range []string{"setup", "repeated setup", "uninstall"} {
		t.Run(operation, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USER", "tester")
			source := writeSetupSource(t, "tl")
			if _, err := runSetup(t, "copilot", "--src", source); err != nil {
				t.Fatal(err)
			}
			installed := filepath.Join(home, ".copilot", "skills")
			for _, name := range retiredSkills {
				target := filepath.Join(agent.PackageDir("copilot"), "skills", name)
				if err := os.MkdirAll(target, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(installed, name)); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"copilot", "--src", source}
			if operation == "uninstall" {
				args = append(args, "--uninstall")
			}
			if _, err := runSetup(t, args...); err != nil {
				t.Fatal(err)
			}
			if operation == "repeated setup" {
				for _, name := range retiredSkills {
					target := filepath.Join(agent.PackageDir("copilot"), "skills", name)
					if err := os.Symlink(target, filepath.Join(installed, name)); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := runSetup(t, "copilot", "--src", source); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range retiredSkills {
				if _, err := os.Lstat(filepath.Join(installed, name)); !os.IsNotExist(err) {
					t.Errorf("retired %s survived %s: %v", name, operation, err)
				}
			}
		})
	}
}
