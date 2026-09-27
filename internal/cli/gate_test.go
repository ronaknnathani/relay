package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGateFileTreatsShellMetacharactersAsLiteralArgv(t *testing.T) {
	dir := t.TempDir()
	injected := filepath.Join(dir, "injected")
	path := filepath.Join(dir, "gates.json")
	definitions := []gateDefinition{{
		ID: "test",
		Argv: []string{
			"/usr/bin/printf",
			"$(touch " + injected + ")",
		},
	}}
	data, err := json.Marshal(definitions)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadGateDefinitions(path)
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := runGateDefinition(
		context.Background(), strings.NewReader(""), &stdout, &stdout, dir, loaded[0],
	); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "$(touch "+injected+")" {
		t.Fatalf("gate output = %q", stdout.String())
	}
	if _, err := os.Stat(injected); !os.IsNotExist(err) {
		t.Fatalf("gate argv was reparsed by a shell: %v", err)
	}
}

func TestGateFileRejectsShellCommandStrings(t *testing.T) {
	for _, argv := range [][]string{
		{"sh", "-c", "touch injected"},
		{"bash", "-lc", "touch injected"},
		{"sh", "-ec", "touch injected"},
		{"bash", "--login", "-c", "touch injected"},
		{"/usr/bin/env", "bash", "-c", "touch injected"},
		{"busybox", "sh", "-c", "touch injected"},
		{"xargs", "-I{}", "sh", "-c", "touch injected"},
		{"/usr/bin/env", "-S", "sh -c 'touch injected'"},
		{"/usr/bin/env", "--split-string=sh -c 'touch injected'"},
		{"csh", "-c", "touch injected"},
		{"tcsh", "-c", "touch injected"},
		{"fish", "-c", "touch injected"},
		{"ash", "-c", "touch injected"},
		{"pwsh", "-Command", "touch injected"},
	} {
		t.Run(strings.Join(argv[:min(2, len(argv))], "_"), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gates.json")
			data, err := json.Marshal([]gateDefinition{{ID: "test", Argv: argv}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadGateDefinitions(path); err == nil ||
				!strings.Contains(err.Error(), "shell command-string execution") {
				t.Fatalf("shell command-string gate error = %v", err)
			}
		})
	}
}
