package generate

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "image/png"
)

func TestAdaptiveWorkflowAndRetiredWrapperDocs(t *testing.T) {
	root := repoRoot(t)
	readme := readFile(t, filepath.Join(root, "README.md"))
	for _, want := range []string{
		"relay --full", "easy, standard, high-risk, or stack-candidate",
		"`/relay-status <args>` -> `relay status <args>`",
		"`/relay-archive <args>` -> `relay archive <args>`",
		"`/todo <args>` -> `relay todo <args>`",
		"active phase dispatch",
		"`implement -> open-pr`",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README missing adaptive delivery documentation %q", want)
		}
	}
	if strings.Contains(readme, "clarify -> plan -> implement -> simplify -> review -> validate -> open-pr") {
		t.Error("README still promises the fixed seven-phase sequence")
	}
	contributing := readFile(t, filepath.Join(root, "CONTRIBUTING.md"))
	for _, want := range []string{
		"`skipped`, `blocked`, and `escalated`", "`relay route`",
		"`dispatch`, `finish`, and `evidence`", "15,000 words",
	} {
		if !strings.Contains(contributing, want) {
			t.Errorf("CONTRIBUTING missing workflow governance %q", want)
		}
	}
	explore := readFile(t, filepath.Join(root, "skills", "explore", "SKILL.md"))
	for _, want := range []string{
		"source identity",
		"caller-supplied freshness context",
	} {
		if !strings.Contains(explore, want) {
			t.Errorf("explore freshness contract missing %q", want)
		}
	}
	if strings.Contains(explore, `relay route snapshot "$SLUG"`) {
		t.Error("explore still requires the Relay CLI for freshness")
	}
	route := readFile(t, filepath.Join(root, "skills", "route", "SKILL.md"))
	for _, want := range []string{
		`relay route snapshot "$SLUG"`,
		"pass that freshness context to `explore` and `clarify`",
	} {
		if !strings.Contains(route, want) {
			t.Errorf("route freshness integration missing %q", want)
		}
	}

	diagram := readFile(t, filepath.Join(root, "docs", "skill-layers.html"))
	for _, want := range []string{
		"route", "Easy path", "Standard / high-risk", "snapshot evidence",
		"current facts + implement evidence", "Handles one PR attention event",
	} {
		if !strings.Contains(diagram, want) {
			t.Errorf("skill diagram missing adaptive branch %q", want)
		}
	}
	tl := readFile(t, filepath.Join(root, "skills", "tl", "SKILL.md"))
	if !strings.Contains(tl, "Process every usable entry even when another entry has a warning") {
		t.Error("tech-lead contract does not preserve partial successful results")
	}
	stackState := readFile(t, filepath.Join(root, "skills", "stack-ship", "references", "state-files.md"))
	if strings.Contains(stackState, "monitorMode") || strings.Contains(stackState, "next tick set") {
		t.Error("stack state docs retain obsolete watcher scheduling state")
	}
	imageFile, err := os.Open(filepath.Join(root, "docs", "skill-layers.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer imageFile.Close()
	config, _, err := image.DecodeConfig(imageFile)
	if err != nil {
		t.Fatal(err)
	}
	if config.Width < 1400 || config.Width <= config.Height {
		t.Fatalf("skill diagram is not a desktop capture: %dx%d", config.Width, config.Height)
	}
}
