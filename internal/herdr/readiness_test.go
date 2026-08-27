package herdr

import (
	"errors"
	"strings"
	"testing"
)

func TestRequireRejectsMissingBinaryBeforeAnythingElse(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	listed := false

	_, err := Require(Requirement{
		Command:   "relay program new",
		Topology:  true,
		Available: func() bool { return false },
		Agents: func() ([]Agent, error) {
			listed = true
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("Require succeeded without the herdr binary")
	}
	for _, want := range []string{"relay program new", "herdr", "install", "integration"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if listed {
		t.Fatal("Require queried the Herdr server without a binary")
	}
}

func TestRequireRejectsMissingTopologyWithoutFallback(t *testing.T) {
	tests := []struct {
		name        string
		env         string
		workspaceID string
		want        string
	}{
		{name: "outside Herdr", want: "HERDR_ENV=1"},
		{name: "no workspace", env: "1", want: "HERDR_WORKSPACE_ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HERDR_ENV", test.env)
			t.Setenv("HERDR_WORKSPACE_ID", test.workspaceID)

			_, err := Require(Requirement{
				Command:   "relay program worker start",
				Topology:  true,
				Available: func() bool { return true },
				Agents:    func() ([]Agent, error) { return nil, nil },
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Require error = %v", err)
			}
			if strings.Contains(err.Error(), "manually") || strings.Contains(err.Error(), "without Herdr") {
				t.Fatalf("Require error offers a non-Herdr fallback: %v", err)
			}
		})
	}
}

func TestRequireFailsClosedWhenServerIsUnreachable(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")

	_, err := Require(Requirement{
		Command:   "relay program patrol run",
		Topology:  false,
		Available: func() bool { return true },
		Agents:    func() ([]Agent, error) { return nil, errors.New("connection refused") },
	})
	if err == nil {
		t.Fatal("Require succeeded with an unreachable Herdr server")
	}
	for _, want := range []string{"relay program patrol run", "connection refused", "herdr agent list"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestRequireReturnsWorkspaceAndReusesTheAgentList(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w7")
	calls := 0
	agents := []Agent{{Status: StatusIdle, PaneID: "w7:p1"}}

	readiness, err := Require(Requirement{
		Command:   "relay program worker start",
		Topology:  true,
		Available: func() bool { return true },
		Agents: func() ([]Agent, error) {
			calls++
			return agents, nil
		},
	})
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	if readiness.WorkspaceID != "w7" || len(readiness.Agents) != 1 || readiness.Agents[0].PaneID != "w7:p1" {
		t.Fatalf("readiness = %+v", readiness)
	}
	if calls != 1 {
		t.Fatalf("agent list calls = %d, want 1", calls)
	}
}

func TestRequireWithoutTopologyIgnoresWorkspaceEnvironment(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("HERDR_WORKSPACE_ID", "")

	readiness, err := Require(Requirement{
		Command:   "relay resume",
		Available: func() bool { return true },
		Agents:    func() ([]Agent, error) { return []Agent{}, nil },
	})
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	if readiness.WorkspaceID != "" {
		t.Fatalf("workspace ID = %q, want empty", readiness.WorkspaceID)
	}
}

func TestRequireDefaultsToTheInstalledClient(t *testing.T) {
	previous := lookPath
	t.Cleanup(func() { lookPath = previous })
	lookPath = func(string) (string, error) { return "", errors.New("not found") }

	if _, err := Require(Requirement{Command: "relay resume"}); err == nil {
		t.Fatal("Require succeeded with default seams and no binary")
	}
}
