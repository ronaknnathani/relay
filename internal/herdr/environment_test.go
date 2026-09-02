package herdr

import (
	"errors"
	"strings"
	"testing"
)

func TestRequireEnvironment(t *testing.T) {
	t.Run("not inside Herdr", func(t *testing.T) {
		t.Setenv("HERDR_ENV", "")
		t.Setenv("HERDR_WORKSPACE_ID", "")
		_, err := RequireEnvironment()
		if err == nil || !strings.Contains(err.Error(), "HERDR_ENV=1") {
			t.Fatalf("RequireEnvironment error = %v", err)
		}
	})

	t.Run("missing workspace", func(t *testing.T) {
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_WORKSPACE_ID", "")
		_, err := RequireEnvironment()
		if err == nil || !strings.Contains(err.Error(), "HERDR_WORKSPACE_ID") {
			t.Fatalf("RequireEnvironment error = %v", err)
		}
	})

	t.Run("valid", func(t *testing.T) {
		t.Setenv("HERDR_ENV", "1")
		t.Setenv("HERDR_WORKSPACE_ID", "w7")
		workspaceID, err := RequireEnvironment()
		if err != nil {
			t.Fatalf("RequireEnvironment: %v", err)
		}
		if workspaceID != "w7" {
			t.Fatalf("workspace ID = %q, want w7", workspaceID)
		}
	})
}

func TestAvailableUsesExecutableLookup(t *testing.T) {
	previous := lookPath
	t.Cleanup(func() { lookPath = previous })

	var name string
	lookPath = func(file string) (string, error) {
		name = file
		return "/usr/local/bin/herdr", nil
	}
	if !Available() {
		t.Fatal("Available = false, want true")
	}
	if name != "herdr" {
		t.Fatalf("looked up %q, want herdr", name)
	}

	lookPath = func(string) (string, error) {
		return "", errors.New("not found")
	}
	if Available() {
		t.Fatal("Available = true, want false")
	}
}
