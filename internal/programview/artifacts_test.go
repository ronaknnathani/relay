package programview

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadArtifactRejectsEscapesAndSymlinksOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readArtifact(root, "../secret.md", 1024, true); err == nil {
		t.Fatal("relative path escape was accepted")
	}
	link := filepath.Join(root, "context.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readArtifact(root, "context.md", 1024, true); err == nil {
		t.Fatal("symlink escape was accepted")
	}
}
