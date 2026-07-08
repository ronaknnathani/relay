package agent

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SkillSyncOptions configures generated skill link/unlink operations.
type SkillSyncOptions struct {
	PackageDir      string
	ManagedRoots    []string
	Stdin           io.Reader
	Stdout          io.Writer
	StdinIsTerminal bool
}

// LinkSkills links generated skills for a into the agent's personal skills dir.
func LinkSkills(a Agent, opts SkillSyncOptions) error {
	return syncSkills(a, opts, true)
}

// UnlinkSkills removes relay-managed skill links for a from the agent's skills dir.
func UnlinkSkills(a Agent, opts SkillSyncOptions) error {
	return syncSkills(a, opts, false)
}

func syncSkills(a Agent, opts SkillSyncOptions, link bool) error {
	installedDir, err := skillsDirForAgent(a)
	if err != nil {
		return err
	}
	packageDir, err := filepath.Abs(opts.PackageDir)
	if err != nil {
		return fmt.Errorf("resolve package dir %s: %w", opts.PackageDir, err)
	}
	sourceDir := filepath.Join(packageDir, "skills")
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if link {
		if err := os.MkdirAll(installedDir, 0755); err != nil {
			return fmt.Errorf("mkdir skills dir %s: %w", installedDir, err)
		}
	}
	entries, err := os.ReadDir(sourceDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read skills dir %s: %w", sourceDir, err)
	}
	for _, entry := range entries {
		skillDir := filepath.Join(sourceDir, entry.Name())
		info, err := os.Stat(skillDir)
		if err != nil {
			return fmt.Errorf("stat skill %s: %w", skillDir, err)
		}
		if !info.IsDir() {
			continue
		}
		if err := syncSkill(entry.Name(), skillDir, installedDir, opts, link); err != nil {
			return err
		}
	}
	return nil
}

func syncSkill(name, skillDir, installedDir string, opts SkillSyncOptions, link bool) error {
	out := opts.Stdout
	target := filepath.Join(installedDir, name)
	info, err := os.Lstat(target)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat installed skill %s: %w", target, err)
	}
	if err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			fmt.Fprintf(out, "  skipping %s: %s exists and is not a symlink\n", name, target)
			return nil
		}
		current, err := os.Readlink(target)
		if err != nil {
			return fmt.Errorf("readlink %s: %w", target, err)
		}
		if !isManagedTarget(current, opts.ManagedRoots) {
			if !link {
				fmt.Fprintf(out, "  keeping %s: %s -> %s is not managed by relay\n", name, target, current)
				return nil
			}
			replace, err := promptReplaceForeign(name, target, current, opts, out)
			if err != nil {
				return err
			}
			if !replace {
				fmt.Fprintf(out, "  skipping %s\n", name)
				return nil
			}
		}
	}

	if !link {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove skill link %s: %w", target, err)
		}
		fmt.Fprintf(out, "  removed %s\n", name)
		return nil
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing skill link %s: %w", target, err)
	}
	if err := os.Symlink(skillDir, target); err != nil {
		return fmt.Errorf("link skill %s -> %s: %w", target, skillDir, err)
	}
	fmt.Fprintf(out, "  linked %s\n", name)
	return nil
}

func promptReplaceForeign(name, target, current string, opts SkillSyncOptions, out io.Writer) (bool, error) {
	fmt.Fprintf(out, "  %s -> %s is not managed by relay\n", target, current)
	if !opts.StdinIsTerminal {
		fmt.Fprintln(out, "  (non-interactive: keeping existing symlink)")
		return false, nil
	}
	fmt.Fprintf(out, "  Replace it with the relay skill %q? [y/N] ", name)
	in := opts.Stdin
	if in == nil {
		in = strings.NewReader("")
	}
	raw, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("reading replace answer: %w", err)
	}
	switch strings.TrimSpace(raw) {
	case "y", "Y", "yes", "Yes":
		return true, nil
	default:
		return false, nil
	}
}

func isManagedTarget(target string, managedRoots []string) bool {
	if hasParentSegment(target) {
		return false
	}
	for _, root := range managedRoots {
		if target == root || strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func hasParentSegment(path string) bool {
	for _, segment := range strings.Split(path, string(os.PathSeparator)) {
		if segment == ".." {
			return true
		}
	}
	return false
}

func skillsDirForAgent(a Agent) (string, error) {
	switch a.Name() {
	case "claude":
		return claudeSkillsDir(), nil
	case "copilot":
		return copilotSkillsDir(), nil
	default:
		return "", fmt.Errorf("skills dir for agent %q is not supported", a.Name())
	}
}
