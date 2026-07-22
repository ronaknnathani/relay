package agentsmd

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/project"
)

func TestApplyPreservesExistingAgentsMDAndPersistsState(t *testing.T) {
	worktree, projectDir := newAgentsMDProject(t)
	original := []byte("# Project\r\nKeep these instructions.\n")
	writeAgentsMDFile(t, worktree, original)

	prompt := "Active relay project: demo. Phase: plan."
	if err := Apply(worktree, projectDir, prompt); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	data := readAgentsMDFile(t, worktree)
	if !strings.HasPrefix(string(data), string(original)) {
		t.Fatalf("AGENTS.md prefix = %q, want original %q", data, original)
	}
	if countMarker(data, startMarker) != 1 || countMarker(data, endMarker) != 1 {
		t.Fatalf("AGENTS.md markers not written exactly once: %q", data)
	}
	if !strings.Contains(string(data), "# relay\n\n"+prompt+"\n") {
		t.Fatalf("AGENTS.md missing relay prompt: %q", data)
	}

	state := loadAgentsMDState(t, projectDir)
	if state.Version != 1 || !state.Existed {
		t.Fatalf("state = %+v, want version 1 with existed=true", state)
	}
	if got := decodeAgentsMDState(t, state.OriginalContentB64); string(got) != string(original) {
		t.Fatalf("original state = %q, want %q", got, original)
	}
	if got := decodeAgentsMDState(t, state.RelayFragmentB64); string(got) != string(data[len(original):]) {
		t.Fatalf("fragment state = %q, want suffix %q", got, data[len(original):])
	}
}

func TestApplyRepeatedReplacesManagedBlock(t *testing.T) {
	worktree, projectDir := newAgentsMDProject(t)
	original := []byte("Existing guidance\n")
	writeAgentsMDFile(t, worktree, original)

	if err := Apply(worktree, projectDir, "first prompt"); err != nil {
		t.Fatalf("Apply first: %v", err)
	}
	if err := Apply(worktree, projectDir, "second prompt"); err != nil {
		t.Fatalf("Apply second: %v", err)
	}

	data := readAgentsMDFile(t, worktree)
	if countMarker(data, startMarker) != 1 || countMarker(data, endMarker) != 1 {
		t.Fatalf("AGENTS.md markers not idempotent: %q", data)
	}
	if !strings.HasPrefix(string(data), string(original)) {
		t.Fatalf("AGENTS.md prefix = %q, want original %q", data, original)
	}
	if strings.Contains(string(data), "first prompt") || !strings.Contains(string(data), "second prompt") {
		t.Fatalf("AGENTS.md did not replace prompt: %q", data)
	}
}

func TestCleanupRestoresOriginalAgentsMD(t *testing.T) {
	worktree, projectDir := newAgentsMDProject(t)
	original := []byte("Existing guidance without newline")
	writeAgentsMDFile(t, worktree, original)

	if err := Apply(worktree, projectDir, "relay prompt"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := Cleanup(worktree, projectDir); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if got := readAgentsMDFile(t, worktree); string(got) != string(original) {
		t.Fatalf("AGENTS.md after cleanup = %q, want %q", got, original)
	}
	if state := loadManifestForAgentsMD(t, projectDir).AgentsMD; state != nil {
		t.Fatalf("AgentsMD state after cleanup = %+v, want nil", state)
	}
}

func TestCleanupRemovesRelayCreatedAgentsMD(t *testing.T) {
	worktree, projectDir := newAgentsMDProject(t)

	if err := Apply(worktree, projectDir, "relay prompt"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := Cleanup(worktree, projectDir); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	path := filepath.Join(worktree, "AGENTS.md")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("AGENTS.md exists after cleanup: %v", err)
	}
}

func TestCleanupPreservesSeparableEdits(t *testing.T) {
	worktree, projectDir := newAgentsMDProject(t)
	original := []byte("Existing guidance\n")
	writeAgentsMDFile(t, worktree, original)

	if err := Apply(worktree, projectDir, "relay prompt"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(worktree, "AGENTS.md"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open AGENTS.md: %v", err)
	}
	if _, err := f.WriteString("Session note\n"); err != nil {
		t.Fatalf("append session note: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close AGENTS.md: %v", err)
	}

	if err := Cleanup(worktree, projectDir); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	want := string(original) + "Session note\n"
	if got := string(readAgentsMDFile(t, worktree)); got != want {
		t.Fatalf("AGENTS.md after cleanup = %q, want %q", got, want)
	}
}

func TestCleanupConflictsOnEditedRelayBlock(t *testing.T) {
	worktree, projectDir := newAgentsMDProject(t)
	writeAgentsMDFile(t, worktree, []byte("Existing guidance\n"))

	if err := Apply(worktree, projectDir, "relay prompt"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	path := filepath.Join(worktree, "AGENTS.md")
	before := readAgentsMDFile(t, worktree)
	edited := strings.Replace(string(before), "relay prompt", "edited prompt", 1)
	if err := os.WriteFile(path, []byte(edited), 0644); err != nil {
		t.Fatalf("edit AGENTS.md: %v", err)
	}

	err := Cleanup(worktree, projectDir)
	if err == nil || !strings.Contains(err.Error(), "AGENTS.md") {
		t.Fatalf("Cleanup error = %v, want AGENTS.md conflict", err)
	}
	if got := string(readAgentsMDFile(t, worktree)); got != edited {
		t.Fatalf("AGENTS.md changed on conflict = %q, want %q", got, edited)
	}
}

func TestApplyConflictsOnUntrackedRelayMarkers(t *testing.T) {
	worktree, projectDir := newAgentsMDProject(t)
	writeAgentsMDFile(t, worktree, []byte(startMarker+"\nmanual\n"+endMarker+"\n"))

	err := Apply(worktree, projectDir, "relay prompt")
	if err == nil || !strings.Contains(err.Error(), "AGENTS.md") {
		t.Fatalf("Apply error = %v, want AGENTS.md conflict", err)
	}
}

func newAgentsMDProject(t *testing.T) (string, string) {
	t.Helper()
	worktree := t.TempDir()
	projectDir := t.TempDir()
	m := project.Manifest{Slug: "demo", Title: "demo", Worktree: &worktree, Status: "active"}
	if err := project.Save(filepath.Join(projectDir, "manifest.json"), m); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	return worktree, projectDir
}

func writeAgentsMDFile(t *testing.T, worktree string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktree, "AGENTS.md"), data, 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
}

func readAgentsMDFile(t *testing.T, worktree string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(worktree, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	return data
}

func loadAgentsMDState(t *testing.T, projectDir string) *project.AgentsMDState {
	t.Helper()
	state := loadManifestForAgentsMD(t, projectDir).AgentsMD
	if state == nil {
		t.Fatalf("AgentsMD state is nil")
	}
	return state
}

func loadManifestForAgentsMD(t *testing.T, projectDir string) project.Manifest {
	t.Helper()
	m, err := project.Load(filepath.Join(projectDir, "manifest.json"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	return m
}

func decodeAgentsMDState(t *testing.T, encoded string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	return data
}

func countMarker(data []byte, marker string) int {
	return strings.Count(string(data), marker)
}
