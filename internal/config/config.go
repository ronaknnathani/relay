// Package config persists the user-level settings for the relay CLI at
// $HOME/.relay/config.json. Settings are loaded on demand by the
// commands that need them (new, resume) — non-launching commands such as
// status never block on first-time setup.
package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/project"
	"golang.org/x/term"
)

// Config is the on-disk schema. Field names match the existing JSON; do
// not rename without migrating ~/.relay/config.json.
type Config struct {
	BranchPrefix string `json:"branch_prefix"`
	DefaultAgent string `json:"default_agent,omitempty"`
	// PermissionModes stores agent-specific permission modes chosen at setup or
	// through `relay config`. Missing entries are prompted once when that agent
	// is first launched.
	PermissionModes map[string]string `json:"permission_modes,omitempty"`
	// PermissionMode is the retired global field. It is still read for
	// backwards-compatible migration but omitted on the next save.
	PermissionMode string `json:"permission_mode,omitempty"`
}

// WorktreePrefix derives the on-disk worktree directory prefix from the
// branch prefix by replacing `/` with `_`. Example: "ronaknnathani/" -> "ronaknnathani_".
func (c Config) WorktreePrefix() string {
	return strings.ReplaceAll(c.BranchPrefix, "/", "_")
}

func (c Config) PermissionModeFor(agentName string) string {
	if c.PermissionModes != nil {
		return c.PermissionModes[agentName]
	}
	return ""
}

func (c *Config) SetPermissionMode(agentName, mode string) {
	if c.PermissionModes == nil {
		c.PermissionModes = map[string]string{}
	}
	c.PermissionModes[agentName] = mode
	c.PermissionMode = ""
}

// Path returns the absolute path to the config file.
func Path() string {
	return filepath.Join(project.RelayDir(), "config.json")
}

// Load reads the config file. Returns (cfg, true, nil) on success,
// (zero, false, nil) if the file does not exist, and (zero, false, err)
// on any other failure.
func Load() (Config, bool, error) {
	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	// Pre-seed defaults so a hand-written minimal config (e.g. only
	// branch_prefix set) matches what the interactive prompt would have saved.
	c := Config{DefaultAgent: agent.DefaultName}
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.PermissionModes == nil {
		c.PermissionModes = map[string]string{}
	}
	if c.PermissionMode != "" && c.PermissionModes[c.DefaultAgent] == "" {
		c.PermissionModes[c.DefaultAgent] = c.PermissionMode
	}
	// Migrate the retired dangerously_skip_permissions flag: a config that had
	// it enabled was bypassing all prompts, so preserve that as "bypass" rather
	// than silently dropping to the new agent default.
	if c.PermissionModes[c.DefaultAgent] == "" {
		var legacy struct {
			Dangerous *bool `json:"dangerously_skip_permissions"`
		}
		if json.Unmarshal(data, &legacy) == nil && legacy.Dangerous != nil && *legacy.Dangerous {
			c.PermissionModes[c.DefaultAgent] = "bypass"
		}
	}
	return c, true, nil
}

// Save writes c to disk atomically (tmp file + rename) so an interrupted
// write cannot leave a truncated config on disk.
func Save(c Config) error {
	dir := project.RelayDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	c.PermissionMode = ""
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	final := Path()
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, final, err)
	}
	return nil
}

var branchPrefixRE = regexp.MustCompile(`^[a-zA-Z0-9-]+/$`)

// validateBranchPrefix normalizes and validates a branch prefix. Trims
// whitespace, appends `/` if missing, then enforces ^[a-zA-Z0-9-]+/$.
func validateBranchPrefix(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("prefix cannot be empty")
	}
	if !strings.HasSuffix(s, "/") {
		s += "/"
	}
	if !branchPrefixRE.MatchString(s) {
		return "", fmt.Errorf("invalid prefix %q: must be alphanumerics or hyphens followed by /", s)
	}
	return s, nil
}

// validateAgent normalizes and checks an agent name against the registry.
// Empty input resolves to the default agent.
func validateAgent(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return agent.DefaultName, nil
	}
	if _, err := agent.Get(s); err != nil {
		return "", err
	}
	return s, nil
}

// promptPermissionMode asks the user to choose one of the agent's permission
// modes, defaulting to the first. Returns "" when the agent declares no modes.
func promptPermissionMode(reader *bufio.Reader, agentName string, modes []string) (string, error) {
	if len(modes) == 0 {
		return "", nil
	}
	for {
		fmt.Printf("Permission mode for %s (%s) [%s]: ", agentName, strings.Join(modes, ", "), modes[0])
		raw, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("reading permission mode: %w", err)
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return modes[0], nil
		}
		if slices.Contains(modes, raw) {
			return raw, nil
		}
		fmt.Printf("  invalid mode; choose one of: %s\n", strings.Join(modes, ", "))
	}
}

func defaultPermissionMode(agentName string) (string, error) {
	a, err := agent.Get(agentName)
	if err != nil {
		return "", err
	}
	modes := a.PermissionModes()
	if len(modes) == 0 {
		return "", nil
	}
	return modes[0], nil
}

func supportsPermissionMode(agentName, mode string) (bool, error) {
	a, err := agent.Get(agentName)
	if err != nil {
		return false, err
	}
	return slices.Contains(a.PermissionModes(), mode), nil
}

func SetDefaultAgent(c Config, agentName string) (Config, error) {
	agentName, err := validateAgent(agentName)
	if err != nil {
		return Config{}, err
	}
	c.DefaultAgent = agentName
	return c, nil
}

func SetBranchPrefix(c Config, prefix string) (Config, error) {
	normalized, err := validateBranchPrefix(prefix)
	if err != nil {
		return Config{}, err
	}
	c.BranchPrefix = normalized
	return c, nil
}

func SetAgentPermissionMode(c Config, agentName, mode string) (Config, error) {
	agentName, err := validateAgent(agentName)
	if err != nil {
		return Config{}, err
	}
	mode = strings.TrimSpace(mode)
	ok, err := supportsPermissionMode(agentName, mode)
	if err != nil {
		return Config{}, err
	}
	if !ok {
		a, err := agent.Get(agentName)
		if err != nil {
			return Config{}, err
		}
		return Config{}, fmt.Errorf("invalid permission mode %q for %s; choose one of: %s", mode, agentName, strings.Join(a.PermissionModes(), ", "))
	}
	c.SetPermissionMode(agentName, mode)
	return c, nil
}

func defaultBranchPrefix() string {
	defaultUser := os.Getenv("USER")
	if defaultUser == "" {
		defaultUser = "user"
	}
	return defaultUser + "/"
}

// prompt runs the interactive first-time setup. If stdin is not a TTY, it
// writes defaults silently (so scripted use does not hang) and prints the
// path to stderr.
func prompt(defaultAgent string) (Config, error) {
	defaultPrefix := defaultBranchPrefix()
	defaultAgent, err := validateAgent(defaultAgent)
	if err != nil {
		return Config{}, err
	}
	defaultPermMode, err := defaultPermissionMode(defaultAgent)
	if err != nil {
		return Config{}, err
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		cfg := Config{BranchPrefix: defaultPrefix, DefaultAgent: defaultAgent}
		cfg.SetPermissionMode(defaultAgent, defaultPermMode)
		if err := Save(cfg); err != nil {
			return Config{}, err
		}
		fmt.Fprintf(os.Stderr, "relay: wrote default config to %s (non-interactive; defaults used)\n", Path())
		return cfg, nil
	}

	fmt.Println()
	fmt.Println("First-time setup for `relay`. These answers are saved to disk and reused for every project.")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	var prefix string
	for {
		fmt.Printf("Branch prefix for new projects [%s]: ", defaultPrefix)
		raw, err := reader.ReadString('\n')
		if err != nil {
			return Config{}, fmt.Errorf("reading prefix: %w", err)
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			raw = defaultPrefix
		}
		p, err := validateBranchPrefix(raw)
		if err != nil {
			fmt.Printf("  %s\n", err)
			continue
		}
		prefix = p
		break
	}

	var agentName string
	for {
		fmt.Printf("Coding agent (%s) [%s]: ", strings.Join(agent.Names(), ", "), defaultAgent)
		raw, err := reader.ReadString('\n')
		if err != nil {
			return Config{}, fmt.Errorf("reading agent: %w", err)
		}
		if strings.TrimSpace(raw) == "" {
			raw = defaultAgent
		}
		a, err := validateAgent(raw)
		if err != nil {
			fmt.Printf("  %s\n", err)
			continue
		}
		agentName = a
		break
	}

	a, err := agent.Get(agentName)
	if err != nil {
		return Config{}, err
	}
	permMode, err := promptPermissionMode(reader, agentName, a.PermissionModes())
	if err != nil {
		return Config{}, err
	}

	cfg := Config{BranchPrefix: prefix, DefaultAgent: agentName}
	cfg.SetPermissionMode(agentName, permMode)
	if err := Save(cfg); err != nil {
		return Config{}, fmt.Errorf("saving config: %w", err)
	}
	fmt.Println()
	fmt.Printf("Saved config to %s. Edit this file to change settings later.\n", Path())
	fmt.Println()
	return cfg, nil
}

// SetupForAgent runs setup-time config initialization for one selected agent.
func SetupForAgent(agentName string) (Config, error) {
	a, err := agent.Get(strings.TrimSpace(agentName))
	if err != nil {
		return Config{}, err
	}
	cfg, ok, err := Load()
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if ok {
		cfg, err = validateBase(cfg)
		if err != nil {
			return Config{}, err
		}
	} else {
		cfg = Config{BranchPrefix: defaultBranchPrefix(), DefaultAgent: agent.DefaultName}
	}

	if term.IsTerminal(int(os.Stdin.Fd())) {
		cfg, err = promptSetupForAgent(cfg, a)
	} else {
		cfg, err = ensureSetupPermissionMode(cfg, a)
	}
	if err != nil {
		return Config{}, err
	}
	if err := Save(cfg); err != nil {
		return Config{}, fmt.Errorf("saving config: %w", err)
	}
	return cfg, nil
}

func ensureSetupPermissionMode(cfg Config, a agent.Agent) (Config, error) {
	modes := a.PermissionModes()
	if slices.Contains(modes, cfg.PermissionModeFor(a.Name())) {
		return cfg, nil
	}
	mode := ""
	if len(modes) > 0 {
		mode = modes[0]
	}
	return SetAgentPermissionMode(cfg, a.Name(), mode)
}

func promptSetupForAgent(cfg Config, a agent.Agent) (Config, error) {
	reader := bufio.NewReader(os.Stdin)
	prefix, err := promptBranchPrefix(reader, cfg.BranchPrefix)
	if err != nil {
		return Config{}, err
	}
	cfg, err = SetBranchPrefix(cfg, prefix)
	if err != nil {
		return Config{}, err
	}
	mode, err := promptPermissionMode(reader, a.Name(), a.PermissionModes())
	if err != nil {
		return Config{}, err
	}
	cfg, err = SetAgentPermissionMode(cfg, a.Name(), mode)
	if err != nil {
		return Config{}, err
	}
	setDefault, err := promptSetDefaultAgent(reader, a.Name())
	if err != nil {
		return Config{}, err
	}
	if setDefault {
		cfg, err = SetDefaultAgent(cfg, a.Name())
		if err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func promptBranchPrefix(reader *bufio.Reader, current string) (string, error) {
	if current == "" {
		current = defaultBranchPrefix()
	}
	for {
		fmt.Printf("Branch prefix for new projects [%s]: ", current)
		raw, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("reading prefix: %w", err)
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			raw = current
		}
		prefix, err := validateBranchPrefix(raw)
		if err != nil {
			fmt.Printf("  %s\n", err)
			continue
		}
		return prefix, nil
	}
}

func promptSetDefaultAgent(reader *bufio.Reader, agentName string) (bool, error) {
	fmt.Printf("Set %s as default agent? [y/N] ", agentName)
	raw, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("reading default-agent answer: %w", err)
	}
	switch strings.TrimSpace(raw) {
	case "y", "Y", "yes", "Yes":
		return true, nil
	default:
		return false, nil
	}
}

// Ensure returns the loaded config, running first-time setup if the file
// does not exist. Re-validates the branch prefix in case the user
// hand-edited the file.
func Ensure() (Config, error) {
	return EnsureForAgent("")
}

// EnsureBase returns the loaded config, running first-time setup if the file
// does not exist, but does not prompt for any missing per-agent permission mode.
func EnsureBase() (Config, error) {
	cfg, ok, err := Load()
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if !ok {
		cfg, err = prompt(agent.DefaultName)
		if err != nil {
			return Config{}, fmt.Errorf("config setup: %w", err)
		}
		return cfg, nil
	}
	return validateBase(cfg)
}

func validateBase(cfg Config) (Config, error) {
	normalized, err := validateBranchPrefix(cfg.BranchPrefix)
	if err != nil {
		return Config{}, fmt.Errorf("config: branch_prefix in %s is invalid: %w", Path(), err)
	}
	cfg.BranchPrefix = normalized
	if cfg.DefaultAgent == "" {
		cfg.DefaultAgent = agent.DefaultName
	}
	if _, err := agent.Get(cfg.DefaultAgent); err != nil {
		return Config{}, fmt.Errorf("config: default_agent in %s is invalid: %w", Path(), err)
	}
	return cfg, nil
}

// EnsureForAgent returns the loaded config, using preferredAgent as the
// first-time setup default and ensuring permission_modes contains a valid entry
// for the agent about to launch. Existing default_agent is preserved unless this
// is the first config write.
func EnsureForAgent(preferredAgent string) (Config, error) {
	if preferredAgent != "" {
		if _, err := validateAgent(preferredAgent); err != nil {
			return Config{}, err
		}
	}
	cfg, ok, err := Load()
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if ok {
		cfg, err = validateBase(cfg)
		if err != nil {
			return Config{}, err
		}
		agentName := cfg.DefaultAgent
		if preferredAgent != "" {
			agentName = preferredAgent
		}
		ok, err := supportsPermissionMode(agentName, cfg.PermissionModeFor(agentName))
		if err != nil {
			return Config{}, err
		}
		if ok {
			return cfg, nil
		}
		a, err := agent.Get(agentName)
		if err != nil {
			return Config{}, err
		}
		var mode string
		if term.IsTerminal(int(os.Stdin.Fd())) {
			reader := bufio.NewReader(os.Stdin)
			mode, err = promptPermissionMode(reader, agentName, a.PermissionModes())
			if err != nil {
				return Config{}, err
			}
		} else {
			mode, err = defaultPermissionMode(agentName)
			if err != nil {
				return Config{}, err
			}
			fmt.Fprintf(os.Stderr, "relay: set permission_modes.%s=%q in %s (non-interactive; default used)\n", agentName, mode, Path())
		}
		cfg.SetPermissionMode(agentName, mode)
		if err := Save(cfg); err != nil {
			return Config{}, fmt.Errorf("saving config: %w", err)
		}
		return cfg, nil
	}
	defaultAgent := agent.DefaultName
	if preferredAgent != "" {
		defaultAgent = preferredAgent
	}
	cfg, err = prompt(defaultAgent)
	if err != nil {
		return Config{}, fmt.Errorf("config setup: %w", err)
	}
	return cfg, nil
}
