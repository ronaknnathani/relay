package generate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// retiredRole matches the retired managed-program role identity as a whole
// word, in any case. It deliberately does not match words that merely contain
// those letters, such as "connector" or "director".
var retiredRole = regexp.MustCompile(`(?i)\bctos?\b`)

// Nothing an agent or a reader ever sees may still name the retired role: the
// generated skill packages are the user-facing surface of every harness.
func TestGeneratedSkillsNeverNameTheRetiredRole(t *testing.T) {
	for _, agentName := range []string{"claude", "copilot", "codex"} {
		t.Run(agentName, func(t *testing.T) {
			_, out := generateAgent(t, agentName)
			walkTextFiles(t, out, func(path, body string) {
				if match := retiredRole.FindString(body); match != "" {
					t.Errorf("%s still names the retired role %q", path, match)
				}
			})
		})
	}
}

// The canonical tech-lead skill is what every harness installs.
func TestGeneratedPackagesInstallTheTLSkill(t *testing.T) {
	for _, agentName := range []string{"claude", "copilot", "codex"} {
		t.Run(agentName, func(t *testing.T) {
			_, out := generateAgent(t, agentName)
			body := readFile(t, filepath.Join(out, "skills", "tl", "SKILL.md"))
			if !strings.Contains(body, "name: tl") {
				t.Errorf("tl skill frontmatter = %.80q, want name: tl", body)
			}
			if _, err := os.Stat(filepath.Join(out, "skills", "cto")); !os.IsNotExist(err) {
				t.Errorf("the retired cto skill is still generated: %v", err)
			}
		})
	}
}

// The shipped source docs and skills carry the same guarantee, so a reader
// never meets the retired role either.
func TestSourceDocsAndSkillsNeverNameTheRetiredRole(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"skills", "docs"} {
		walkTextFiles(t, filepath.Join(root, dir), func(path, body string) {
			if match := retiredRole.FindString(body); match != "" {
				t.Errorf("%s still names the retired role %q", path, match)
			}
		})
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if match := retiredRole.FindString(string(readme)); match != "" {
		t.Errorf("README.md still names the retired role %q", match)
	}
}

// legacyIdentitySources are the only files allowed to spell the retired role in
// a Go string literal. They declare the compatibility identities every decode
// chokepoint normalizes through, and the retired skill link setup removes.
var legacyIdentitySources = map[string]bool{
	filepath.Join("internal", "role", "identity.go"): true,
	filepath.Join("internal", "cli", "setup.go"):     true,
}

// No shipped Go code may put the retired role into a string an operator can
// see — a command description, an error, or printed output. Compatibility is
// confined to the named legacy constants.
func TestGoStringLiteralsNeverNameTheRetiredRole(t *testing.T) {
	root := repoRoot(t)
	fileSet := token.NewFileSet()
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		name := info.Name()
		if info.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if legacyIdentitySources[relative] {
			return nil
		}
		file, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			if match := retiredRole.FindString(literal.Value); match != "" {
				t.Errorf("%s has a user-visible string naming the retired role %q: %s",
					relative, match, literal.Value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal: %v", err)
	}
}

func walkTextFiles(t *testing.T, root string, check func(path, body string)) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !isTextAsset(path) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		check(path, string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func isTextAsset(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".json", ".txt", ".html", ".css", ".js", ".yaml", ".yml":
		return true
	}
	return false
}
