package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/generate"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type setupOptions struct {
	agent           agent.Agent
	sourceOverride  string
	uninstall       bool
	stdin           io.Reader
	stdout          io.Writer
	stdinIsTerminal bool
}

func newCmdSetup() *cobra.Command {
	var (
		src       string
		uninstall bool
	)
	cmd := &cobra.Command{
		Use:   "setup <agent>",
		Short: "Generate and install relay skills for an agent",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("setup requires exactly one agent (supported: %s)", strings.Join(agent.Names(), ", "))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			agentName := strings.TrimSpace(args[0])
			if agentName == "" {
				return fmt.Errorf("setup requires exactly one agent (supported: %s)", strings.Join(agent.Names(), ", "))
			}
			a, err := agent.Get(agentName)
			if err != nil {
				return err
			}
			stdin := cmd.InOrStdin()
			return runSetupCommand(setupOptions{
				agent:           a,
				sourceOverride:  src,
				uninstall:       uninstall,
				stdin:           stdin,
				stdout:          cmd.OutOrStdout(),
				stdinIsTerminal: isTerminalReader(stdin),
			})
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "relay source directory")
	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "remove relay-managed skill links")
	return cmd
}

func runSetupCommand(opts setupOptions) error {
	sourceRoot, err := discoverSourceDir(opts.sourceOverride)
	if err != nil && opts.sourceOverride == "" && opts.stdinIsTerminal {
		if _, writeErr := fmt.Fprintln(opts.stdout, err); writeErr != nil {
			return fmt.Errorf("write source discovery error: %w", writeErr)
		}
		sourceRoot, err = promptForValidSourceDir(opts.stdin, opts.stdout)
	}
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(opts.stdout, "Using relay source %s\n", sourceRoot); err != nil {
		return fmt.Errorf("write selected source directory: %w", err)
	}
	roots, err := setupManagedRoots(sourceRoot)
	if err != nil {
		return err
	}
	packageDir := agent.PackageDir(opts.agent.Name())
	syncOpts := agent.SkillSyncOptions{
		PackageDir:      packageDir,
		ManagedRoots:    roots,
		Stdin:           opts.stdin,
		Stdout:          opts.stdout,
		StdinIsTerminal: opts.stdinIsTerminal,
	}
	if opts.uninstall {
		if err := agent.UnlinkSkills(opts.agent, syncOpts); err != nil {
			return err
		}
		return removeRetiredSkills(opts.agent, syncOpts)
	}

	if err := regeneratePackage(opts.agent, sourceRoot, packageDir); err != nil {
		return err
	}
	if opts.agent.Name() == "claude" {
		if err := regeneratePackage(opts.agent, sourceRoot, filepath.Join(sourceRoot, "dist", "claude")); err != nil {
			return err
		}
	}
	if err := agent.LinkSkills(opts.agent, syncOpts); err != nil {
		return err
	}
	if err := removeRetiredSkills(opts.agent, syncOpts); err != nil {
		return err
	}
	_, err = config.SetupForAgent(opts.agent.Name())
	return err
}

func promptForSourceDir(stdin *bufio.Reader, stdout io.Writer) (string, error) {
	if _, err := fmt.Fprint(stdout, "Relay source directory: "); err != nil {
		return "", fmt.Errorf("write source directory prompt: %w", err)
	}
	sourceDir, err := stdin.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read source directory: %w", err)
	}
	sourceDir = strings.TrimSpace(sourceDir)
	if sourceDir == "" {
		if err == io.EOF {
			return "", fmt.Errorf("read source directory: %w", err)
		}
		return "", nil
	}
	return sourceDir, nil
}

func promptForValidSourceDir(stdin io.Reader, stdout io.Writer) (string, error) {
	reader := bufio.NewReader(stdin)
	for {
		sourceDir, err := promptForSourceDir(reader, stdout)
		if err != nil {
			return "", err
		}
		if sourceDir == "" {
			if _, err := fmt.Fprintln(stdout, "relay source directory is required"); err != nil {
				return "", fmt.Errorf("write source directory error: %w", err)
			}
			continue
		}
		sourceRoot, err := sourceDirFromPath(sourceDir, "source directory")
		if err == nil {
			return sourceRoot, nil
		}
		if _, writeErr := fmt.Fprintln(stdout, err); writeErr != nil {
			return "", fmt.Errorf("write source directory error: %w", writeErr)
		}
	}
}

// retiredSkills names skills Relay used to install and no longer ships.
var retiredSkills = []string{"cto", "relay-status", "relay-archive", "todo"}

func removeRetiredSkills(a agent.Agent, syncOpts agent.SkillSyncOptions) error {
	for _, name := range retiredSkills {
		if err := agent.RemoveRetiredSkill(a, name, syncOpts); err != nil {
			return err
		}
	}
	return nil
}

func regeneratePackage(a agent.Agent, sourceRoot, packageDir string) error {
	if err := os.RemoveAll(packageDir); err != nil {
		return fmt.Errorf("remove generated package %s: %w", packageDir, err)
	}
	if err := generate.Generate(a, sourceRoot, packageDir); err != nil {
		return fmt.Errorf("generate %s package in %s: %w", a.Name(), packageDir, err)
	}
	return nil
}

func discoverSourceDir(srcOverride string) (string, error) {
	if srcOverride != "" {
		return sourceDirFromPath(srcOverride, "--src")
	}
	var workingDirErr error
	if workingDir, err := os.Getwd(); err == nil {
		sourceRoot, isRelaySource, err := sourceDirFromWorkingDirectory(workingDir)
		if err == nil && isRelaySource {
			return sourceRoot, nil
		}
		if isRelaySource {
			workingDirErr = fmt.Errorf(
				"relay source directory in current working directory %s is invalid; pass --src <path>: %w",
				workingDir,
				err,
			)
		}
	}
	var candidates []string
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, executable)
	}
	if os.Args[0] != "" {
		candidates = append(candidates, os.Args[0])
	}
	sourceRoot, err := discoverSourceDirFromCandidates(candidates)
	if err != nil && workingDirErr != nil {
		return "", workingDirErr
	}
	return sourceRoot, err
}

func sourceDirFromPath(sourceDir, label string) (string, error) {
	if sourceDir == "~" || strings.HasPrefix(sourceDir, "~"+string(os.PathSeparator)) {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory in source path %s: %w", sourceDir, err)
		}
		sourceDir = filepath.Join(homeDir, strings.TrimPrefix(sourceDir, "~"+string(os.PathSeparator)))
	}
	sourceRoot, err := filepath.Abs(sourceDir)
	if err != nil {
		return "", fmt.Errorf("resolve source dir %s: %w", sourceDir, err)
	}
	if !hasSourceLayout(sourceRoot) {
		return "", fmt.Errorf("%s %s is not a relay source directory (missing plugin.json or skills/)", label, sourceRoot)
	}
	if _, err := generate.LoadSource(sourceRoot); err != nil {
		return "", err
	}
	return sourceRoot, nil
}

func discoverSourceDirFromCandidates(candidates []string) (string, error) {
	for _, candidate := range candidates {
		sourceRoot, err := sourceDirFromExecutable(candidate)
		if err == nil {
			return sourceRoot, nil
		}
	}
	return "", fmt.Errorf("could not discover relay source directory; pass --src <path>")
}

func sourceDirFromWorkingDirectory(workingDir string) (string, bool, error) {
	if !hasSourceLayout(workingDir) {
		return "", false, nil
	}
	manifestData, err := os.ReadFile(filepath.Join(workingDir, "plugin.json"))
	if err != nil {
		return "", true, fmt.Errorf("read plugin manifest: %w", err)
	}
	var manifest struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return "", true, fmt.Errorf("parse plugin manifest: %w", err)
	}
	if manifest.Name != "relay" {
		return "", false, nil
	}
	if _, err := generate.LoadSource(workingDir); err != nil {
		return "", true, err
	}
	return workingDir, true, nil
}

func sourceDirFromExecutable(executable string) (string, error) {
	resolved, err := resolveExecutablePath(executable)
	if err != nil {
		return "", err
	}
	exeDir := filepath.Dir(resolved)
	binDir := filepath.Dir(exeDir)
	if filepath.Base(resolved) != "relay" || filepath.Base(binDir) != "bin" {
		return "", fmt.Errorf("executable %s is not in <repo>/bin/<os>/relay", resolved)
	}
	sourceRoot := filepath.Dir(binDir)
	if !hasSourceLayout(sourceRoot) {
		return "", fmt.Errorf("source root %s is missing plugin.json or skills/", sourceRoot)
	}
	return sourceRoot, nil
}

func resolveExecutablePath(executable string) (string, error) {
	candidate := executable
	if !filepath.IsAbs(candidate) && !strings.ContainsRune(candidate, os.PathSeparator) {
		found, err := exec.LookPath(candidate)
		if err != nil {
			return "", fmt.Errorf("look up executable %s: %w", executable, err)
		}
		candidate = found
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve executable %s: %w", candidate, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve executable symlinks %s: %w", abs, err)
	}
	return resolved, nil
}

func hasSourceLayout(sourceRoot string) bool {
	manifest, err := os.Stat(filepath.Join(sourceRoot, "plugin.json"))
	if err != nil || manifest.IsDir() {
		return false
	}
	skills, err := os.Stat(filepath.Join(sourceRoot, "skills"))
	return err == nil && skills.IsDir()
}

func setupManagedRoots(sourceRoot string) ([]string, error) {
	sourceRoot, err := filepath.Abs(sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve managed source root %s: %w", sourceRoot, err)
	}
	relayDir := project.RelayDir()
	relayRoot, err := filepath.Abs(relayDir)
	if err != nil {
		return nil, fmt.Errorf("resolve relay dir %s: %w", relayDir, err)
	}
	return []string{sourceRoot, relayRoot}, nil
}

func isTerminalReader(r io.Reader) bool {
	file, ok := r.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}
