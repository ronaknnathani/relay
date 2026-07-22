package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VerifySkillsInstalled checks that the skill package used by the selected
// agent exists before relay hands off to the agent process.
func VerifySkillsInstalled(a Agent, command string) error {
	switch a.Name() {
	case "codex":
		return verifyInstalledSkills(a.Name(), codexSkillsDir(), command)
	case "copilot":
		return verifyInstalledSkills(a.Name(), copilotSkillsDir(), command)
	case "claude":
		return verifyInstalledSkills(a.Name(), claudeSkillsDir(), command)
	default:
		return nil
	}
}

func verifyInstalledSkills(agentName, installed, command string) error {
	generated := filepath.Join(PackageDir(agentName), "skills")
	entries, err := os.ReadDir(generated)
	if err == nil {
		var missing []string
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if err := requireFile(filepath.Join(installed, e.Name(), "SKILL.md")); err != nil {
				missing = append(missing, e.Name())
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("relay: %s skills are not fully installed; missing %s in %s. %s",
				agentName, strings.Join(missing, ", "), installed, setupHint(agentName))
		}
	} else if !os.IsNotExist(err) {
		return installError(agentName, err)
	}

	if command != "" {
		if err := requireRelaySkill(installed, command); err != nil {
			return fmt.Errorf("relay: %s skill %q is not installed as a relay-managed skill in %s (%v). %s Remove a conflicting skill of the same name before running setup",
				agentName, command, installed, err, setupHint(agentName))
		}
	}
	return nil
}

func claudeSkillsDir() string {
	return filepath.Join(os.Getenv("HOME"), ".claude", "skills")
}

func copilotSkillsDir() string {
	return filepath.Join(os.Getenv("HOME"), ".copilot", "skills")
}

func codexSkillsDir() string {
	return filepath.Join(os.Getenv("HOME"), ".codex", "skills")
}

func requireFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	return nil
}

// requireRelaySkill verifies that <installed>/<name> is a relay-managed skill:
// relay installs skills as symlinks, so a real directory/file of the same name
// is a non-relay skill shadowing relay's and must not be treated as installed.
func requireRelaySkill(installed, name string) error {
	entry := filepath.Join(installed, name)
	info, err := os.Lstat(entry)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s exists but is not a relay-managed symlink", entry)
	}
	return requireFile(filepath.Join(entry, "SKILL.md"))
}

func installError(agentName string, err error) error {
	return fmt.Errorf("relay: generated %s skills are not installed at %s: %w. %s",
		agentName, PackageDir(agentName), err, setupHint(agentName))
}

func setupHint(agentName string) string {
	return fmt.Sprintf("Relay-managed workflows require `relay setup %s` from the relay repository. `npx skills add <repo>` installs standalone skills only.", agentName)
}
