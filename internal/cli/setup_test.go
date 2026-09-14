package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
)

func writeSetupSource(t *testing.T, skillNames ...string) string {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"name":"relay"}`+"\n"), 0644); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	for _, name := range skillNames {
		skillDir := filepath.Join(source, "skills", name)
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			t.Fatalf("mkdir skill: %v", err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+name+"\n"), 0644); err != nil {
			t.Fatalf("write skill: %v", err)
		}
	}
	return source
}

func runSetup(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newCmdSetup()
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(""))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestSetupRequiresExactlyOneSupportedAgent(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{""},
		{" \t "},
		{"copilot", "extra"},
	} {
		_, err := runSetup(t, args...)
		if err == nil || !strings.Contains(err.Error(), "setup requires exactly one agent (supported: claude, codex, copilot)") {
			t.Fatalf("runSetup(%v) error = %v, want supported-agent arity error", args, err)
		}
	}

	_, err := runSetup(t, "nope")
	if err == nil || !strings.Contains(err.Error(), `unknown agent "nope" (supported: claude, codex, copilot)`) {
		t.Fatalf("runSetup(nope) error = %v, want unknown-agent supported list", err)
	}
}

func TestSetupRejectsSourceMissingSkillsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	source := writeSetupSource(t)

	_, err := runSetup(t, "copilot", "--src", source)
	want := "--src " + source + " is not a relay source directory (missing plugin.json or skills/)"
	if err == nil || err.Error() != want {
		t.Fatalf("setup copilot --src without skills error = %v, want %q", err, want)
	}
	if _, err := os.Lstat(filepath.Join(home, ".copilot", "skills")); !os.IsNotExist(err) {
		t.Fatalf("copilot skills dir exists after rejected --src: %v", err)
	}
	if _, err := os.Lstat(agent.PackageDir("copilot")); !os.IsNotExist(err) {
		t.Fatalf("copilot package dir exists after rejected --src: %v", err)
	}
}

func TestSetupCopilotGeneratesLinksAndWritesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "tester")
	source := writeSetupSource(t, "plan")

	if _, err := runSetup(t, "copilot", "--src", source); err != nil {
		t.Fatalf("setup copilot: %v", err)
	}

	packageSkill := filepath.Join(agent.PackageDir("copilot"), "skills", "plan")
	if _, err := os.Stat(filepath.Join(packageSkill, "SKILL.md")); err != nil {
		t.Fatalf("generated copilot skill: %v", err)
	}
	installed := filepath.Join(home, ".copilot", "skills", "plan")
	if got, err := os.Readlink(installed); err != nil || got != packageSkill {
		t.Fatalf("installed copilot link = %q, %v; want %q", got, err, packageSkill)
	}
	cfg, ok, err := config.Load()
	if err != nil || !ok {
		t.Fatalf("Load config: ok=%v err=%v", ok, err)
	}
	if cfg.BranchPrefix != "tester/" {
		t.Errorf("BranchPrefix = %q, want tester/", cfg.BranchPrefix)
	}
	if cfg.DefaultAgent != agent.DefaultName {
		t.Errorf("DefaultAgent = %q, want %q", cfg.DefaultAgent, agent.DefaultName)
	}
	if got := cfg.PermissionModeFor("copilot"); got != "allow-all" {
		t.Errorf("PermissionModeFor(copilot) = %q, want allow-all", got)
	}
}

func TestSetupUsesCurrentRelayRootWithoutSrc(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "tester")
	source := writeSetupSource(t, "cwd-only")
	t.Chdir(source)

	if _, err := runSetup(t, "copilot"); err != nil {
		t.Fatalf("setup copilot from relay root: %v", err)
	}

	packageSkill := filepath.Join(agent.PackageDir("copilot"), "skills", "cwd-only")
	if _, err := os.Stat(filepath.Join(packageSkill, "SKILL.md")); err != nil {
		t.Fatalf("generated copilot skill: %v", err)
	}
}

func TestSetupPromptsForSourceWhenDiscoveryFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "tester")
	source := writeSetupSource(t, "plan")
	invalidSource := filepath.Join(t.TempDir(), "missing")
	t.Chdir(t.TempDir())
	copilot, err := agent.Get("copilot")
	if err != nil {
		t.Fatalf("get copilot agent: %v", err)
	}
	var out bytes.Buffer

	err = runSetupCommand(setupOptions{
		agent:           copilot,
		stdin:           strings.NewReader(invalidSource + "\n" + source + "\n"),
		stdout:          &out,
		stdinIsTerminal: true,
	})
	if err != nil {
		t.Fatalf("interactive setup copilot: %v", err)
	}
	for _, want := range []string{
		"could not discover relay source directory; pass --src <path>",
		"Relay source directory: ",
		"source directory " + invalidSource + " is not a relay source directory",
		"Using relay source " + source,
	} {
		if got := out.String(); !strings.Contains(got, want) {
			t.Errorf("setup output = %q, want it to contain %q", got, want)
		}
	}

	packageSkill := filepath.Join(agent.PackageDir("copilot"), "skills", "plan")
	if _, err := os.Stat(filepath.Join(packageSkill, "SKILL.md")); err != nil {
		t.Fatalf("generated copilot skill: %v", err)
	}
}

func TestSetupPromptAcceptsHomeRelativeSourceAfterBlankInput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "tester")
	source := filepath.Join(home, "relay-source")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"name":"relay"}`+"\n"), 0644); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	skillDir := filepath.Join(source, "skills", "home-relative")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# home-relative\n"), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	t.Chdir(t.TempDir())
	copilot, err := agent.Get("copilot")
	if err != nil {
		t.Fatalf("get copilot agent: %v", err)
	}
	var out bytes.Buffer

	err = runSetupCommand(setupOptions{
		agent:           copilot,
		stdin:           strings.NewReader("\n~/relay-source\n"),
		stdout:          &out,
		stdinIsTerminal: true,
	})
	if err != nil {
		t.Fatalf("interactive setup copilot: %v", err)
	}
	if got := strings.Count(out.String(), "Relay source directory: "); got != 2 {
		t.Errorf("source prompts = %d, want 2; output = %q", got, out.String())
	}
	if !strings.Contains(out.String(), "relay source directory is required") {
		t.Errorf("setup output = %q, want blank-input guidance", out.String())
	}

	packageSkill := filepath.Join(agent.PackageDir("copilot"), "skills", "home-relative")
	if _, err := os.Stat(filepath.Join(packageSkill, "SKILL.md")); err != nil {
		t.Fatalf("generated copilot skill: %v", err)
	}
}

func TestSetupWithoutSrcStaysNonInteractiveWhenInputIsNotTerminal(t *testing.T) {
	t.Chdir(t.TempDir())
	copilot, err := agent.Get("copilot")
	if err != nil {
		t.Fatalf("get copilot agent: %v", err)
	}
	var out bytes.Buffer

	err = runSetupCommand(setupOptions{
		agent:  copilot,
		stdin:  strings.NewReader(""),
		stdout: &out,
	})
	if err == nil || err.Error() != "could not discover relay source directory; pass --src <path>" {
		t.Fatalf("non-interactive setup error = %v, want source discovery error", err)
	}
	if out.Len() != 0 {
		t.Errorf("non-interactive setup output = %q, want no prompt", out.String())
	}
}

func TestSetupSrcTakesPrecedenceOverCurrentRelayRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "tester")
	currentSource := writeSetupSource(t, "cwd-only")
	overrideSource := writeSetupSource(t, "override-only")
	t.Chdir(currentSource)

	if _, err := runSetup(t, "copilot", "--src", overrideSource); err != nil {
		t.Fatalf("setup copilot with --src: %v", err)
	}

	packageSkills := filepath.Join(agent.PackageDir("copilot"), "skills")
	if _, err := os.Stat(filepath.Join(packageSkills, "override-only", "SKILL.md")); err != nil {
		t.Fatalf("generated override skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(packageSkills, "cwd-only")); !os.IsNotExist(err) {
		t.Fatalf("generated current-directory skill despite --src override: %v", err)
	}
}

func TestSetupCodexGeneratesLinksAndWritesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "tester")
	source := writeSetupSource(t, "plan")

	if _, err := runSetup(t, "codex", "--src", source); err != nil {
		t.Fatalf("setup codex: %v", err)
	}

	packageSkill := filepath.Join(agent.PackageDir("codex"), "skills", "plan")
	if _, err := os.Stat(filepath.Join(packageSkill, "SKILL.md")); err != nil {
		t.Fatalf("generated codex skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(agent.PackageDir("codex"), ".claude-plugin", "plugin.json")); !os.IsNotExist(err) {
		t.Fatalf("codex generated Claude plugin manifest: %v", err)
	}
	installed := filepath.Join(home, ".codex", "skills", "plan")
	if got, err := os.Readlink(installed); err != nil || got != packageSkill {
		t.Fatalf("installed codex link = %q, %v; want %q", got, err, packageSkill)
	}
	cfg, ok, err := config.Load()
	if err != nil || !ok {
		t.Fatalf("Load config: ok=%v err=%v", ok, err)
	}
	if cfg.DefaultAgent != agent.DefaultName {
		t.Errorf("DefaultAgent = %q, want %q", cfg.DefaultAgent, agent.DefaultName)
	}
	if got := cfg.PermissionModeFor("codex"); got != "auto" {
		t.Errorf("PermissionModeFor(codex) = %q, want auto", got)
	}
}

func TestSetupUninstallRemovesManagedLinksOnlyAndKeepsConfigAndPackage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "tester")
	source := writeSetupSource(t, "managed", "foreign")
	if _, err := runSetup(t, "copilot", "--src", source); err != nil {
		t.Fatalf("setup copilot: %v", err)
	}

	packageDir := agent.PackageDir("copilot")
	sentinel := filepath.Join(packageDir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	configBefore, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatalf("read config before uninstall: %v", err)
	}
	foreignTarget := filepath.Join(t.TempDir(), "foreign")
	if err := os.MkdirAll(foreignTarget, 0755); err != nil {
		t.Fatalf("mkdir foreign target: %v", err)
	}
	foreignLink := filepath.Join(home, ".copilot", "skills", "foreign")
	if err := os.Remove(foreignLink); err != nil {
		t.Fatalf("remove generated foreign link: %v", err)
	}
	if err := os.Symlink(foreignTarget, foreignLink); err != nil {
		t.Fatalf("symlink foreign target: %v", err)
	}

	if _, err := runSetup(t, "copilot", "--uninstall", "--src", source); err != nil {
		t.Fatalf("setup copilot --uninstall: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(home, ".copilot", "skills", "managed")); !os.IsNotExist(err) {
		t.Errorf("managed link still exists: %v", err)
	}
	if got, err := os.Readlink(foreignLink); err != nil || got != foreignTarget {
		t.Errorf("foreign link = %q, %v; want %q", got, err, foreignTarget)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("uninstall touched generated package sentinel: %v", err)
	}
	configAfter, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatalf("read config after uninstall: %v", err)
	}
	if !bytes.Equal(configAfter, configBefore) {
		t.Errorf("uninstall modified config\nbefore: %s\nafter: %s", configBefore, configAfter)
	}
}

func TestDiscoverSourceDir(t *testing.T) {
	source := writeSetupSource(t, "plan")
	withDots := filepath.Join(source, ".")
	got, err := discoverSourceDir(withDots)
	if err != nil {
		t.Fatalf("discoverSourceDir(--src): %v", err)
	}
	if got != filepath.Clean(source) {
		t.Errorf("discoverSourceDir(--src) = %q, want %q", got, filepath.Clean(source))
	}

	binary := filepath.Join(source, "bin", "darwin", "relay")
	if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		t.Fatalf("mkdir binary dir: %v", err)
	}
	if err := os.WriteFile(binary, []byte("relay"), 0755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	link := filepath.Join(t.TempDir(), "relay")
	if err := os.Symlink(binary, link); err != nil {
		t.Fatalf("symlink binary: %v", err)
	}
	got, err = sourceDirFromExecutable(link)
	if err != nil {
		t.Fatalf("sourceDirFromExecutable: %v", err)
	}
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatalf("resolve source symlinks: %v", err)
	}
	if got != resolvedSource {
		t.Errorf("sourceDirFromExecutable = %q, want %q", got, resolvedSource)
	}

	_, err = discoverSourceDirFromCandidates([]string{filepath.Join(t.TempDir(), "relay")})
	if err == nil || err.Error() != "could not discover relay source directory; pass --src <path>" {
		t.Fatalf("discoverSourceDirFromCandidates error = %v, want --src hint", err)
	}
}

func TestDiscoverSourceDirIgnoresNonRelayWorkingDirectories(t *testing.T) {
	t.Run("invalid source layout", func(t *testing.T) {
		source := writeSetupSource(t, "broken")
		if err := os.Remove(filepath.Join(source, "skills", "broken", "SKILL.md")); err != nil {
			t.Fatalf("remove skill file: %v", err)
		}
		t.Chdir(source)

		_, err := discoverSourceDir("")
		for _, want := range []string{
			"relay source directory in current working directory " + source + " is invalid",
			"read skill broken",
			"pass --src <path>",
		} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("discoverSourceDir error = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("different plugin", func(t *testing.T) {
		source := writeSetupSource(t, "plan")
		if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"name":"other"}`+"\n"), 0644); err != nil {
			t.Fatalf("write plugin manifest: %v", err)
		}
		t.Chdir(source)

		_, err := discoverSourceDir("")
		if err == nil || err.Error() != "could not discover relay source directory; pass --src <path>" {
			t.Fatalf("discoverSourceDir error = %v, want source discovery error", err)
		}
	})
}

func TestSetupClaudeGeneratesDistAndLinksStablePackage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "tester")
	source := writeSetupSource(t, "plan")

	if _, err := runSetup(t, "claude", "--src", source); err != nil {
		t.Fatalf("setup claude: %v", err)
	}

	packageSkill := filepath.Join(agent.PackageDir("claude"), "skills", "plan")
	distSkill := filepath.Join(source, "dist", "claude", "skills", "plan", "SKILL.md")
	if _, err := os.Stat(filepath.Join(packageSkill, "SKILL.md")); err != nil {
		t.Fatalf("generated stable claude skill: %v", err)
	}
	if _, err := os.Stat(distSkill); err != nil {
		t.Fatalf("generated dist claude skill: %v", err)
	}
	installed := filepath.Join(home, ".claude", "skills", "plan")
	if got, err := os.Readlink(installed); err != nil || got != packageSkill {
		t.Fatalf("installed claude link = %q, %v; want stable package %q", got, err, packageSkill)
	}
}
