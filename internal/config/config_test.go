package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ronaknnathani/relay/internal/agent"
)

// writeConfig points HOME at a temp dir and writes the given JSON to the
// config path, returning that temp HOME.
func writeConfig(t *testing.T, json string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Dir(Path())
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(Path(), []byte(json), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestLoadDefaultsAgent(t *testing.T) {
	writeConfig(t, `{"branch_prefix":"x/"}`)
	cfg, ok, err := Load()
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if cfg.DefaultAgent != agent.DefaultName {
		t.Errorf("DefaultAgent = %q, want %q", cfg.DefaultAgent, agent.DefaultName)
	}
}

func TestLoadMigratesLegacyDangerouslySkip(t *testing.T) {
	writeConfig(t, `{"branch_prefix":"x/","dangerously_skip_permissions":true}`)
	cfg, ok, err := Load()
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if got := cfg.PermissionModeFor(agent.DefaultName); got != "bypass" {
		t.Errorf("PermissionModeFor(default) = %q, want bypass (migrated from dangerously_skip_permissions)", got)
	}
}

func TestLoadKeepsExplicitPermissionMode(t *testing.T) {
	// An explicit permission_mode is not overridden by the legacy flag.
	writeConfig(t, `{"branch_prefix":"x/","permission_mode":"default","dangerously_skip_permissions":true}`)
	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.PermissionModeFor(agent.DefaultName); got != "default" {
		t.Errorf("PermissionModeFor(default) = %q, want default (explicit value wins over legacy)", got)
	}
}

func TestEnsureRejectsInvalidAgent(t *testing.T) {
	writeConfig(t, `{"branch_prefix":"x/","default_agent":"nope"}`)
	if _, err := Ensure(); err == nil {
		t.Fatal("Ensure: expected error for invalid default_agent, got nil")
	}
}

func TestEnsureDefaultsAgentWhenEmpty(t *testing.T) {
	writeConfig(t, `{"branch_prefix":"x/"}`)
	cfg, err := Ensure()
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if cfg.DefaultAgent != agent.DefaultName {
		t.Errorf("DefaultAgent = %q, want %q", cfg.DefaultAgent, agent.DefaultName)
	}
}

func TestValidateAgent(t *testing.T) {
	if got, err := validateAgent(""); err != nil || got != agent.DefaultName {
		t.Errorf("validateAgent(\"\") = %q, %v; want default, nil", got, err)
	}
	if got, err := validateAgent(" copilot "); err != nil || got != "copilot" {
		t.Errorf("validateAgent(copilot) = %q, %v; want copilot, nil", got, err)
	}
	if _, err := validateAgent("nope"); err == nil {
		t.Error("validateAgent(nope): expected error, got nil")
	}
}

// TestNonInteractivePromptDefaultsAgent asserts the scripted (non-TTY) setup
// persists the default agent rather than leaving it empty.
func TestNonInteractivePromptDefaultsAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := prompt(agent.DefaultName) // stdin is not a TTY under `go test`
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if cfg.DefaultAgent != agent.DefaultName {
		t.Errorf("DefaultAgent = %q, want %q", cfg.DefaultAgent, agent.DefaultName)
	}
	reloaded, ok, err := Load()
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if reloaded.DefaultAgent != agent.DefaultName {
		t.Errorf("persisted DefaultAgent = %q, want %q", reloaded.DefaultAgent, agent.DefaultName)
	}
}

func TestNonInteractivePromptUsesPreferredAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := prompt("copilot") // stdin is not a TTY under `go test`
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if cfg.DefaultAgent != "copilot" {
		t.Errorf("DefaultAgent = %q, want copilot", cfg.DefaultAgent)
	}
	if got := cfg.PermissionModeFor("copilot"); got != "allow-all" {
		t.Errorf("PermissionModeFor(copilot) = %q, want allow-all", got)
	}
}

func TestEnsureForAgentAddsMissingAgentPermissionMode(t *testing.T) {
	writeConfig(t, `{"branch_prefix":"x/","default_agent":"claude","permission_mode":"auto"}`)
	cfg, err := EnsureForAgent("copilot") // stdin is not a TTY under `go test`
	if err != nil {
		t.Fatalf("EnsureForAgent: %v", err)
	}
	if cfg.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want existing default claude", cfg.DefaultAgent)
	}
	if got := cfg.PermissionModeFor("claude"); got != "auto" {
		t.Errorf("PermissionModeFor(claude) = %q, want existing auto", got)
	}
	if got := cfg.PermissionModeFor("copilot"); got != "allow-all" {
		t.Errorf("PermissionModeFor(copilot) = %q, want allow-all", got)
	}
}

func TestEnsureForAgentKeepsExistingAgentPermissionMode(t *testing.T) {
	writeConfig(t, `{"branch_prefix":"x/","default_agent":"claude","permission_modes":{"claude":"auto","copilot":"prompt"}}`)
	cfg, err := EnsureForAgent("copilot")
	if err != nil {
		t.Fatalf("EnsureForAgent: %v", err)
	}
	if got := cfg.PermissionModeFor("copilot"); got != "prompt" {
		t.Errorf("PermissionModeFor(copilot) = %q, want prompt", got)
	}
}

func TestSetupForAgentNonInteractiveCreatesDefaultConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "tester")

	cfg, err := SetupForAgent("copilot")
	if err != nil {
		t.Fatalf("SetupForAgent: %v", err)
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

	reloaded, ok, err := Load()
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if reloaded.DefaultAgent != agent.DefaultName {
		t.Errorf("persisted DefaultAgent = %q, want %q", reloaded.DefaultAgent, agent.DefaultName)
	}
	if got := reloaded.PermissionModeFor("copilot"); got != "allow-all" {
		t.Errorf("persisted PermissionModeFor(copilot) = %q, want allow-all", got)
	}
}

func TestSetupForAgentPreservesExistingConfigAndRepairsSelectedPermissionMode(t *testing.T) {
	writeConfig(t, `{"branch_prefix":"rnathani/","default_agent":"claude","permission_modes":{"claude":"bypass","copilot":"auto"}}`)

	cfg, err := SetupForAgent("copilot")
	if err != nil {
		t.Fatalf("SetupForAgent: %v", err)
	}
	if cfg.BranchPrefix != "rnathani/" {
		t.Errorf("BranchPrefix = %q, want rnathani/", cfg.BranchPrefix)
	}
	if cfg.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want claude", cfg.DefaultAgent)
	}
	if got := cfg.PermissionModeFor("claude"); got != "bypass" {
		t.Errorf("PermissionModeFor(claude) = %q, want existing bypass", got)
	}
	if got := cfg.PermissionModeFor("copilot"); got != "allow-all" {
		t.Errorf("PermissionModeFor(copilot) = %q, want repaired allow-all", got)
	}
}

func TestSetupForAgentRejectsUnknownAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := SetupForAgent("nope"); err == nil {
		t.Fatal("SetupForAgent(nope): expected error, got nil")
	}
}

func TestSetAgentPermissionMode(t *testing.T) {
	cfg := Config{}
	cfg, err := SetAgentPermissionMode(cfg, "copilot", "prompt")
	if err != nil {
		t.Fatalf("SetAgentPermissionMode: %v", err)
	}
	if got := cfg.PermissionModeFor("copilot"); got != "prompt" {
		t.Errorf("PermissionModeFor(copilot) = %q, want prompt", got)
	}
	if _, err := SetAgentPermissionMode(cfg, "copilot", "auto"); err == nil {
		t.Fatal("SetAgentPermissionMode invalid mode: expected error, got nil")
	}
}
