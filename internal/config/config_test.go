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
	cfg, err := prompt() // stdin is not a TTY under `go test`
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
