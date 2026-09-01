package prwatch

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/herdr"
)

// fakeOwnerClient answers agent lookups from a fixed list and records prompts.
type fakeOwnerClient struct {
	agents    []herdr.Agent
	agentsErr error
	promptErr error
	prompts   []string
	targets   []string
}

func (f *fakeOwnerClient) Agents() ([]herdr.Agent, error) {
	if f.agentsErr != nil {
		return nil, f.agentsErr
	}
	return f.agents, nil
}

func (f *fakeOwnerClient) PromptAgent(target, text string) error {
	f.targets = append(f.targets, target)
	f.prompts = append(f.prompts, text)
	return f.promptErr
}

func liveAgent(title, pane string, status herdr.Status) herdr.Agent {
	return herdr.Agent{TerminalTitle: title, PaneID: pane, Status: status, StateChangeSeq: 1}
}

func TestOwnerSlugRouting(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    Mode
		project string
		owner   string
		want    string
		wantErr bool
	}{
		{name: "standalone defaults to the project", mode: ModeStandalone, project: "demo", want: "demo"},
		{name: "managed owns the project worker", mode: ModeManaged, project: "demo", want: "demo"},
		{name: "managed rejects a tech lead owner", mode: ModeManaged, project: "demo", owner: "program-tl", wantErr: true},
		{name: "stack requires an owner", mode: ModeStack, project: "demo", wantErr: true},
		{name: "stack names the orchestrator", mode: ModeStack, project: "demo", owner: "stack-run", want: "stack-run"},
		{name: "invalid owner", mode: ModeStack, project: "demo", owner: "../escape", wantErr: true},
		{name: "invalid project", mode: ModeStandalone, project: "../escape", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := OwnerSlug(test.mode, test.project, test.owner)
			if test.wantErr {
				if err == nil {
					t.Fatalf("OwnerSlug = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("OwnerSlug: %v", err)
			}
			if got != test.want {
				t.Errorf("OwnerSlug = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFindLiveProjectOwnerMatchesExactIdentities(t *testing.T) {
	agents := []herdr.Agent{
		liveAgent("relay:foo-bar", "pane-other", herdr.StatusIdle),
		liveAgent("relay:foo - deliver-pr", "pane-owner", herdr.StatusIdle),
	}
	owner, err := herdr.FindLiveProjectOwner(agents, "foo")
	if err != nil {
		t.Fatalf("FindLiveProjectOwner: %v", err)
	}
	if owner.PaneID != "pane-owner" {
		t.Errorf("owner pane = %q, want pane-owner", owner.PaneID)
	}

	if _, err := herdr.FindLiveProjectOwner(agents, "missing"); !errors.Is(err, herdr.ErrNoLiveProjectOwner) {
		t.Errorf("missing owner = %v, want ErrNoLiveProjectOwner", err)
	}

	duplicated := append(agents, liveAgent("relay:foo", "pane-duplicate", herdr.StatusIdle))
	_, err = herdr.FindLiveProjectOwner(duplicated, "foo")
	var duplicate *herdr.DuplicateProjectOwnerError
	if !errors.As(err, &duplicate) {
		t.Fatalf("duplicate owner = %v, want *DuplicateProjectOwnerError", err)
	}
	if len(duplicate.PaneIDs) != 2 {
		t.Errorf("duplicate panes = %v, want both", duplicate.PaneIDs)
	}
}

func TestWakePromptsTheExactOwnerPane(t *testing.T) {
	client := &fakeOwnerClient{agents: []herdr.Agent{
		liveAgent("relay:other", "pane-other", herdr.StatusIdle),
		liveAgent("relay:demo", "pane-demo", herdr.StatusIdle),
	}}
	fingerprint := Fingerprint([]Item{{Key: "comment:1:t0"}})

	outcome := Wake(client, "demo", "demo", fingerprint)
	if outcome.Kind != WakeDelivered || outcome.PaneID != "pane-demo" {
		t.Fatalf("outcome = %+v, want a delivered wake on pane-demo", outcome)
	}
	if len(client.targets) != 1 || client.targets[0] != "pane-demo" {
		t.Fatalf("prompt targets = %v, want only pane-demo", client.targets)
	}
	want := fmt.Sprintf("Run pr-monitor once for project demo using watcher fingerprint %s.", fingerprint)
	if client.prompts[0] != want {
		t.Errorf("prompt = %q, want %q", client.prompts[0], want)
	}
}

func TestWakeWakesTheStackOrchestratorNotTheProject(t *testing.T) {
	client := &fakeOwnerClient{agents: []herdr.Agent{
		liveAgent("relay:demo", "pane-demo", herdr.StatusIdle),
		liveAgent("relay:stack-run", "pane-stack", herdr.StatusIdle),
	}}
	outcome := Wake(client, "stack-run", "demo", Fingerprint([]Item{{Key: "stack-front-merged:1:head"}}))
	if outcome.Kind != WakeDelivered || outcome.PaneID != "pane-stack" {
		t.Fatalf("outcome = %+v, want the orchestrator pane", outcome)
	}
	if !strings.Contains(client.prompts[0], "project demo") {
		t.Errorf("prompt = %q, want the front project named", client.prompts[0])
	}
}

func TestWakeReportsMissingDuplicateAndBusyOwners(t *testing.T) {
	fingerprint := Fingerprint([]Item{{Key: "comment:1:t0"}})

	missing := Wake(&fakeOwnerClient{}, "demo", "demo", fingerprint)
	if missing.Kind != WakeOwnerMissing || missing.Delivered() {
		t.Errorf("missing outcome = %+v, want owner-missing", missing)
	}

	duplicated := Wake(&fakeOwnerClient{agents: []herdr.Agent{
		liveAgent("relay:demo", "pane-a", herdr.StatusIdle),
		liveAgent("relay:demo", "pane-b", herdr.StatusIdle),
	}}, "demo", "demo", fingerprint)
	if duplicated.Kind != WakeOwnerDuplicated || len(duplicated.Panes) != 2 {
		t.Errorf("duplicate outcome = %+v, want owner-duplicated with both panes", duplicated)
	}

	busyClient := &fakeOwnerClient{agents: []herdr.Agent{
		liveAgent("relay:demo", "pane-demo", herdr.StatusWorking),
	}}
	busy := Wake(busyClient, "demo", "demo", fingerprint)
	if busy.Kind != WakeOwnerBusy || busy.Status != herdr.StatusWorking {
		t.Errorf("busy outcome = %+v, want owner-busy", busy)
	}
	if len(busyClient.prompts) != 0 {
		t.Error("a busy owner was prompted anyway")
	}
}

func TestWakeReportsFailedAndUncertainDelivery(t *testing.T) {
	fingerprint := Fingerprint([]Item{{Key: "comment:1:t0"}})
	agents := []herdr.Agent{liveAgent("relay:demo", "pane-demo", herdr.StatusIdle)}

	failed := Wake(&fakeOwnerClient{agents: agents, promptErr: errors.New("pane vanished")},
		"demo", "demo", fingerprint)
	if failed.Kind != WakeFailed || !strings.Contains(failed.Error, "pane vanished") {
		t.Errorf("failed outcome = %+v, want the prompt failure", failed)
	}

	uncertain := Wake(&fakeOwnerClient{
		agents:    agents,
		promptErr: fmt.Errorf("%w: staged", herdr.ErrPromptDeliveryUncertain),
	}, "demo", "demo", fingerprint)
	if uncertain.Kind != WakeUncertain {
		t.Errorf("uncertain outcome = %+v, want uncertain", uncertain)
	}

	listFailed := Wake(&fakeOwnerClient{agentsErr: errors.New("herdr is down")}, "demo", "demo", fingerprint)
	if listFailed.Kind != WakeFailed || !strings.Contains(listFailed.Error, "herdr is down") {
		t.Errorf("agent list failure = %+v, want a failed wake", listFailed)
	}
}
