package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ronaknnathani/relay/internal/project"
	"github.com/spf13/cobra"
)

type gateDefinition struct {
	ID   string            `json:"id"`
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env,omitempty"`
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func newCmdGate() *cobra.Command {
	command := &cobra.Command{
		Use:   "gate",
		Short: "Validate and run repository gates without shell interpolation",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(newCmdGateRun())
	return command
}

func newCmdGateRun() *cobra.Command {
	var file string
	command := &cobra.Command{
		Use:   "run <slug> <gate-id>",
		Short: "Run one route-approved gate as structured argv",
		Args:  cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			definitions, err := loadGateDefinitions(file)
			if err != nil {
				return err
			}
			var selected *gateDefinition
			for index := range definitions {
				if definitions[index].ID == args[1] {
					selected = &definitions[index]
					break
				}
			}
			if selected == nil {
				return fmt.Errorf("gate file %s does not define gate %q", file, args[1])
			}
			state, err := loadState(args[0])
			if err != nil {
				return err
			}
			if state.Route == nil {
				return fmt.Errorf("gate %q requires a current route decision", args[1])
			}
			required, err := requiredGateForDefinition(*selected)
			if err != nil {
				return err
			}
			if !slices.Contains(state.Route.Facts.GatePolicy.Gates, required) {
				return fmt.Errorf("gate %q does not match the current route policy", args[1])
			}
			manifestPath, err := project.Find(args[0])
			if err != nil {
				return err
			}
			manifest, err := project.Load(manifestPath)
			if err != nil {
				return err
			}
			if manifest.Worktree == nil || strings.TrimSpace(*manifest.Worktree) == "" {
				return fmt.Errorf("project %q has no worktree", args[0])
			}
			return runGateDefinition(
				command.Context(),
				command.InOrStdin(),
				command.OutOrStdout(),
				command.ErrOrStderr(),
				*manifest.Worktree,
				*selected,
			)
		},
	}
	command.Flags().StringVar(&file, "file", "", "JSON file containing structured gate argv")
	_ = command.MarkFlagRequired("file")
	return command
}

func loadGateDefinitions(path string) ([]gateDefinition, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("gate file path cannot be empty")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect gate file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("gate file %s must be a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read gate file %s: %w", path, err)
	}
	var definitions []gateDefinition
	if err := json.Unmarshal(data, &definitions); err != nil {
		return nil, fmt.Errorf("parse gate file %s: %w", path, err)
	}
	if len(definitions) == 0 {
		return nil, fmt.Errorf("gate file %s defines no gates", path)
	}
	seen := make(map[string]bool, len(definitions))
	for index := range definitions {
		definition := &definitions[index]
		definition.ID = strings.TrimSpace(definition.ID)
		if definition.ID == "" {
			return nil, fmt.Errorf("gate file %s contains an empty gate id", path)
		}
		if seen[definition.ID] {
			return nil, fmt.Errorf("gate file %s contains duplicate gate id %q", path, definition.ID)
		}
		seen[definition.ID] = true
		if len(definition.Argv) == 0 || strings.TrimSpace(definition.Argv[0]) == "" {
			return nil, fmt.Errorf("gate %q requires a non-empty argv", definition.ID)
		}
		for argumentIndex, argument := range definition.Argv {
			if strings.ContainsRune(argument, '\x00') {
				return nil, fmt.Errorf(
					"gate %q argv[%d] contains NUL", definition.ID, argumentIndex,
				)
			}
		}
		for name, value := range definition.Env {
			if !environmentName.MatchString(name) {
				return nil, fmt.Errorf("gate %q has invalid environment name %q", definition.ID, name)
			}
			if strings.ContainsRune(value, '\x00') {
				return nil, fmt.Errorf("gate %q environment %q contains NUL", definition.ID, name)
			}
		}
		if reparsesShellSource(*definition) {
			return nil, fmt.Errorf(
				"gate %q uses shell command-string execution; provide executable argv instead",
				definition.ID,
			)
		}
	}
	return definitions, nil
}

func reparsesShellSource(definition gateDefinition) bool {
	executable := filepath.Base(definition.Argv[0])
	if executable == "env" {
		for _, argument := range definition.Argv[1:] {
			if argument == "-S" || strings.HasPrefix(argument, "-S") ||
				argument == "--split-string" ||
				strings.HasPrefix(argument, "--split-string=") {
				return true
			}
		}
	}
	for index, argument := range definition.Argv {
		if !slices.Contains(
			[]string{
				"sh", "bash", "zsh", "dash", "ksh", "ash", "csh", "tcsh",
				"fish", "rbash", "powershell", "pwsh",
			},
			filepath.Base(argument),
		) {
			continue
		}
		for _, option := range definition.Argv[index+1:] {
			lowerOption := strings.ToLower(option)
			if lowerOption == "--command" || strings.HasPrefix(lowerOption, "--command=") ||
				lowerOption == "-command" || lowerOption == "-encodedcommand" ||
				(strings.HasPrefix(option, "-") &&
					!strings.HasPrefix(option, "--") &&
					strings.Contains(strings.TrimPrefix(option, "-"), "c")) {
				return true
			}
		}
	}
	return false
}

func requiredGateForDefinition(definition gateDefinition) (project.RequiredGate, error) {
	digest, err := gateDefinitionDigest(definition)
	if err != nil {
		return project.RequiredGate{}, err
	}
	display := filepath.Base(definition.Argv[0])
	if len(definition.Argv) > 1 {
		display += " <redacted-args>"
	}
	return project.RequiredGate{
		ID: definition.ID, CommandDigest: digest, RedactedDisplay: display,
	}, nil
}

func commandEvidenceForGate(
	definition gateDefinition,
	exitStatus int,
) (project.CommandEvidence, error) {
	required, err := requiredGateForDefinition(definition)
	if err != nil {
		return project.CommandEvidence{}, err
	}
	return project.CommandEvidence{
		GateID: required.ID, Display: required.RedactedDisplay,
		Digest: required.CommandDigest, ExitStatus: exitStatus,
	}, nil
}

func gateDefinitionDigest(definition gateDefinition) (string, error) {
	normalized := struct {
		Argv []string          `json:"argv"`
		Env  map[string]string `json:"env,omitempty"`
	}{
		Argv: append([]string(nil), definition.Argv...),
		Env:  definition.Env,
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode gate %q: %w", definition.ID, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func runGateDefinition(
	ctx context.Context,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	worktree string,
	definition gateDefinition,
) error {
	command := exec.CommandContext(ctx, definition.Argv[0], definition.Argv[1:]...)
	command.Dir = worktree
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = os.Environ()
	if len(definition.Env) > 0 {
		names := make([]string, 0, len(definition.Env))
		for name := range definition.Env {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			prefix := name + "="
			command.Env = slices.DeleteFunc(command.Env, func(value string) bool {
				return strings.HasPrefix(value, prefix)
			})
			command.Env = append(command.Env, name+"="+definition.Env[name])
		}
	}
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return fmt.Errorf("gate %q exited with status %d: %w", definition.ID, exitError.ExitCode(), err)
		}
		return fmt.Errorf("run gate %q: %w", definition.ID, err)
	}
	return nil
}
