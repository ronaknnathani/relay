package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
)

func writeSetupHerdr(t *testing.T, body string) string {
	t.Helper()
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "herdr")
	script := "#!/bin/sh\n[ \"$#\" -eq 1 ] && [ \"$1\" = --skill ] || exit 2\ncat \"$0.skill\"\n"
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	skillFile := binary + ".skill"
	if err := os.WriteFile(skillFile, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return skillFile
}

func TestSetupRefreshesHerdrSkillForEveryAgent(t *testing.T) {
	for _, name := range agent.Names() {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USER", "tester")
			source := writeSetupSource(t, "plan")
			skillFile := writeSetupHerdr(t, "# Herdr version one\n")
			for _, body := range []string{"# Herdr version one\n", "# Herdr version two\n"} {
				if err := os.WriteFile(skillFile, []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
				if _, err := runSetup(t, name, "--src", source); err != nil {
					t.Fatalf("setup %s: %v", name, err)
				}
				packageSkill := filepath.Join(agent.PackageDir(name), "skills", "herdr")
				installed := filepath.Join(home, "."+name, "skills", "herdr")
				if got, err := os.Readlink(installed); err != nil || got != packageSkill {
					t.Fatalf("installed Herdr link = %q, %v; want %q", got, err, packageSkill)
				}
				files := []string{filepath.Join(installed, "SKILL.md")}
				if name == "claude" {
					files = append(files, filepath.Join(source, "dist", "claude", "skills", "herdr", "SKILL.md"))
				}
				for _, file := range files {
					if got, err := os.ReadFile(file); err != nil || string(got) != body {
						t.Fatalf("Herdr skill %s = %q, %v; want %q", file, got, err, body)
					}
				}
			}
			t.Setenv("PATH", t.TempDir())
			if _, err := runSetup(t, name, "--uninstall", "--src", source); err != nil {
				t.Fatalf("uninstall %s without Herdr: %v", name, err)
			}
			if _, err := os.Lstat(filepath.Join(home, "."+name, "skills", "herdr")); !os.IsNotExist(err) {
				t.Fatalf("Herdr link still installed: %v", err)
			}
		})
	}
}

func TestSetupHerdrFailurePreservesInstalledPackage(t *testing.T) {
	for _, failure := range []string{"missing", "failed", "empty"} {
		t.Run(failure, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USER", "tester")
			source := writeSetupSource(t, "plan")
			const body = "# Installed Herdr\n"
			skillFile := writeSetupHerdr(t, body)
			if _, err := runSetup(t, "copilot", "--src", source); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "missing":
				t.Setenv("PATH", t.TempDir())
			case "failed":
				if err := os.WriteFile(strings.TrimSuffix(skillFile, ".skill"), []byte("#!/bin/sh\nprintf 'unsupported flag' >&2\nexit 2\n"), 0755); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.WriteFile(skillFile, []byte(" \n\t"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			out, err := runSetup(t, "copilot", "--src", source)
			if err == nil || !strings.Contains(err.Error(), "herdr --skill") || !strings.Contains(err.Error(), "retry setup") {
				t.Fatalf("setup error = %v, want actionable Herdr skill error", err)
			}
			if failure == "failed" && !strings.Contains(out, "unsupported flag") {
				t.Fatalf("setup output = %q, want Herdr stderr", out)
			}
			installed := filepath.Join(home, ".copilot", "skills", "herdr", "SKILL.md")
			if got, err := os.ReadFile(installed); err != nil || string(got) != body {
				t.Fatalf("installed Herdr skill = %q, %v; want %q", got, err, body)
			}
		})
	}
}
