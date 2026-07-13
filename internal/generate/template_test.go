package generate

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTemplateAndPortableDirectoryShape(t *testing.T) {
	root := repoRoot(t)

	var found []string
	for _, name := range []string{TemplateSkillsDir, "templated-skills", "template"} {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && info.IsDir() {
			found = append(found, name)
		} else if err != nil && !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", name, err)
		}
	}
	if !slices.Equal(found, []string{TemplateSkillsDir}) {
		t.Fatalf("template directories = %v, want only %s", found, TemplateSkillsDir)
	}
	for _, dir := range []string{PortableSkillsDir, TemplateSkillsDir} {
		if _, err := os.Stat(filepath.Join(root, dir, "skills")); !os.IsNotExist(err) {
			t.Fatalf("%s must not contain a nested skills/ dir", dir)
		}
		if _, err := os.Stat(filepath.Join(root, dir, ".claude-plugin")); !os.IsNotExist(err) {
			t.Fatalf("%s must not contain a Claude plugin manifest dir", dir)
		}
		if _, err := os.Stat(filepath.Join(root, dir, pluginManifestFile)); !os.IsNotExist(err) {
			t.Fatalf("%s must not contain %s", dir, pluginManifestFile)
		}
	}
}

func TestPortableSkillsMatchTemplateRender(t *testing.T) {
	root := repoRoot(t)
	out := t.TempDir()
	if err := GeneratePortableTemplate(root, out); err != nil {
		t.Fatalf("GeneratePortableTemplate: %v", err)
	}

	src := loadSourceForTest(t, root)
	assertPortableSkills(t, out, src)
	assertPortableBundledModes(t, out, src)
	assertNoUnexpectedFiles(t, out, expectedPortableSkillFiles(src))

	rel := executablePortableResources()[0]
	if err := os.Chmod(filepath.Join(out, rel), 0644); err != nil {
		t.Fatalf("chmod generated %s: %v", rel, err)
	}
	if err := GeneratePortableTemplate(root, out); err != nil {
		t.Fatalf("regenerate portable template: %v", err)
	}
	expectFileMode(t, out, rel, sourceBundledMode(t, src, rel))

	committed := filepath.Join(root, PortableSkillsDir)
	assertPortableSkills(t, committed, src)
	assertPortableBundledModes(t, committed, src)
	assertNoUnexpectedFiles(t, committed, expectedPortableSkillFiles(src))
	walkFiles(t, out, func(rel string, data []byte) {
		expectFile(t, committed, rel, data)
		expectFileMode(t, committed, rel, fileMode(t, filepath.Join(out, rel)))
	})
}

func TestRootSkillsPortability(t *testing.T) {
	root := repoRoot(t)
	templateRoot := filepath.Join(root, PortableSkillsDir)

	for _, rel := range []string{
		"review/agents/code-reviewer.md",
		"review/agents/comment-analyzer.md",
		"review/agents/git-history.md",
		"review/agents/pr-test-analyzer.md",
		"review/agents/prior-pr-history.md",
		"review/agents/security.md",
		"review/agents/silent-failure-hunter.md",
		"review/agents/type-design-analyzer.md",
		"stack-ship/references/decomposition.md",
		"stack-ship/references/guardrails.md",
		"stack-ship/references/monitor-loop.md",
		"stack-ship/references/pr-build-cycle.md",
		"stack-ship/references/stacked-mechanics.md",
		"stack-ship/references/state-files.md",
		"build-write-like-me/references/agent-content-filtering.md",
		"build-write-like-me/references/profile-template.md",
		"build-write-like-me/references/source-gathering.md",
		"build-write-like-me/scripts/analyze_style.py",
		"build-write-like-me/scripts/extract_transcripts.py",
		"build-write-like-me/scripts/fetch_github.py",
		"open-pr/scripts/open-pr.sh",
	} {
		if _, err := os.Stat(filepath.Join(templateRoot, rel)); err != nil {
			t.Fatalf("portable template missing bundled resource %s: %v", rel, err)
		}
	}
	for _, rel := range executablePortableResources() {
		if mode := fileMode(t, filepath.Join(templateRoot, rel)); mode&0111 == 0 {
			t.Errorf("portable template resource %s mode = %v, want executable bit", rel, mode)
		}
	}

	walkFiles(t, templateRoot, func(rel string, data []byte) {
		s := string(data)
		for _, forbidden := range []string{
			"{{subagent",
			"AskUserQuestion",
			"CLAUDE_PLUGIN_ROOT",
			"mcp__",
			"relay:",
		} {
			if strings.Contains(s, forbidden) {
				t.Errorf("%s contains forbidden portable token %q", rel, forbidden)
			}
		}
		if strings.HasSuffix(rel, "SKILL.md") {
			fm, ok := frontmatter(s)
			if !ok {
				t.Errorf("%s missing frontmatter", rel)
				return
			}
			for _, key := range []string{"name:", "description:"} {
				if !strings.Contains(fm, key) {
					t.Errorf("%s frontmatter missing %s", rel, key)
				}
			}
			for _, key := range claudeOnlyKeys {
				if strings.Contains(fm, key+":") {
					t.Errorf("%s frontmatter kept Claude-only key %s", rel, key)
				}
			}
		}
	})
}

func TestTemplateSkillsKeepRelayDirectives(t *testing.T) {
	root := repoRoot(t)
	templateRoot := filepath.Join(root, TemplateSkillsDir)
	if !treeContains(t, templateRoot, "{{subagent") {
		t.Fatalf("%s should retain Relay subagent templates", TemplateSkillsDir)
	}
}

func treeContains(t *testing.T, root, needle string) bool {
	t.Helper()
	found := false
	walkFiles(t, root, func(_ string, data []byte) {
		if strings.Contains(string(data), needle) {
			found = true
		}
	})
	return found
}

func assertPortableSkills(t *testing.T, out string, src *Source) {
	t.Helper()
	for _, e := range src.Entries {
		expectFile(t, out, filepath.Join(e.Name, "SKILL.md"), transformPortableTemplate(e.Body))
		for rel, data := range e.Bundled {
			expectFile(t, out, filepath.Join(e.Name, rel), transformPortableTemplate(data))
		}
	}
}

func assertPortableBundledModes(t *testing.T, out string, src *Source) {
	t.Helper()
	for _, e := range src.Entries {
		for rel, mode := range e.BundledModes {
			expectFileMode(t, out, filepath.Join(e.Name, rel), mode)
		}
	}
}

func expectFileMode(t *testing.T, root, rel string, want os.FileMode) {
	t.Helper()
	got := fileMode(t, filepath.Join(root, rel))
	if got != want {
		t.Errorf("generated %s mode = %v, want %v", rel, got, want)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func sourceBundledMode(t *testing.T, src *Source, rel string) os.FileMode {
	t.Helper()
	for _, e := range src.Entries {
		prefix := e.Name + string(filepath.Separator)
		if bundledRel, ok := strings.CutPrefix(rel, prefix); ok {
			return e.BundledModes[bundledRel]
		}
	}
	t.Fatalf("source bundled mode for %s not found", rel)
	return 0
}

func executablePortableResources() []string {
	return []string{
		"build-write-like-me/scripts/analyze_style.py",
		"build-write-like-me/scripts/extract_transcripts.py",
		"build-write-like-me/scripts/fetch_github.py",
		"open-pr/scripts/open-pr.sh",
	}
}

func expectedPortableSkillFiles(src *Source) map[string]bool {
	files := map[string]bool{}
	for _, e := range src.Entries {
		files[filepath.Join(e.Name, "SKILL.md")] = true
		for rel := range e.Bundled {
			files[filepath.Join(e.Name, rel)] = true
		}
	}
	return files
}
