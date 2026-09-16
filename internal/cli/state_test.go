package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/project"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// wrote. The state commands print their machine-readable output to os.Stdout,
// which is the contract skills consume.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

// runState executes `relay state <args...>` against an isolated HOME and
// returns its stdout and error.
func runState(t *testing.T, args ...string) (string, error) {
	t.Helper()
	args = prepareStateTestArgs(t, args)
	return runStateRaw(t, args...)
}

func runStateRaw(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newCmdState()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return captureStdout(t, cmd.Execute)
}

func initAdaptiveState(t *testing.T, slug string) string {
	t.Helper()
	out, err := runStateRaw(
		t, "init", slug, "--workflow", "deliver-pr",
		"--phases", strings.Join(project.AdaptiveDeliveryPhases, ","),
	)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		CoordinatorToken string `json:"coordinator_token"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.CoordinatorToken == "" {
		t.Fatal("adaptive state init returned no coordinator token")
	}
	return result.CoordinatorToken
}

const testCoordinatorToken = "test-coordinator-capability"

var testDispatchTokens = map[string]dispatchOutput{}

func prepareStateTestArgs(t *testing.T, args []string) []string {
	t.Helper()
	if len(args) < 2 {
		return args
	}
	switch args[0] {
	case "dispatch", "worker", "grant":
		ensureTestCoordinator(t, args[1])
		if !slices.Contains(args, "--coordinator-token") {
			args = append(args, "--coordinator-token", testCoordinatorToken)
		}
	case "evidence":
		if len(args) >= 4 && args[1] == "record" {
			scope := dispatchScopeReview
			if args[3] == "validation" {
				scope = dispatchScopeValidation
			}
			replaceTestDispatchToken(args, scope)
		}
	}
	return args
}

func ensureTestCoordinator(t *testing.T, slug string) {
	t.Helper()
	path := project.StatePath(slug)
	state, err := project.LoadState(path)
	if err != nil || state.CoordinatorHash != "" {
		return
	}
	sum := sha256.Sum256([]byte(testCoordinatorToken))
	state.CoordinatorHash = fmt.Sprintf("%x", sum)
	if err := project.SaveState(path, state); err != nil {
		t.Fatal(err)
	}
}

func replaceTestDispatchToken(args []string, scope string) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "--dispatch-token" {
			continue
		}
		output, ok := testDispatchTokens[args[i+1]]
		if !ok {
			return
		}
		switch scope {
		case dispatchScopeReview:
			args[i+1] = output.ReviewToken
		case dispatchScopeValidation:
			args[i+1] = output.ValidationToken
		}
		return
	}
}

func TestStateInitNextAdvance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := runState(t, "init", "demo", "--workflow", "deliver-pr", "--phases", "clarify,plan,implement")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if strings.TrimSpace(out) != "clarify" {
		t.Errorf("init printed %q, want clarify", out)
	}
	out, _ = runState(t, "next", "demo")
	if strings.TrimSpace(out) != "clarify" {
		t.Errorf("next printed %q, want clarify", out)
	}
	if _, err := runState(t, "set", "demo", "clarify", "done"); err != nil {
		t.Fatalf("set: %v", err)
	}
	out, _ = runState(t, "advance", "demo")
	if strings.TrimSpace(out) != "implement" {
		t.Errorf("advance printed %q, want implement (clarify done + plan advanced)", out)
	}
}

func TestStateMissingSlugIsActionable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := runState(t, "next", "ghost")
	if err == nil || !strings.Contains(err.Error(), "relay state init ghost") {
		t.Errorf("missing-state error = %v, want it to point at `relay state init ghost`", err)
	}
}

func TestStateRejectsSlugTraversal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := runState(t, "init", "../escape", "--workflow", "wf", "--phases", "a")
	if err == nil || !strings.Contains(err.Error(), "invalid slug") {
		t.Errorf("traversal slug error = %v, want an invalid-slug rejection", err)
	}
}

func TestStateDoubleInitRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := runState(t, "init", "demo", "--workflow", "wf", "--phases", "a,b"); err != nil {
		t.Fatalf("first init: %v", err)
	}
	_, err := runState(t, "init", "demo", "--workflow", "wf", "--phases", "a,b")
	if err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Errorf("double init error = %v, want an already-initialized rejection", err)
	}
}

func TestStateCurrentQuotesTask(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := runState(t, "init", "demo", "--workflow", "wf", "--phases", "a,b,c"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runState(t, "set", "demo", "b", "in-progress", "--task", "step 3 of 7"); err != nil {
		t.Fatalf("set: %v", err)
	}
	out, _ := runState(t, "current", "demo")
	if !strings.Contains(out, `task="step 3 of 7"`) {
		t.Errorf("current digest = %q, want a quoted task field", out)
	}
	if !strings.Contains(out, "phase=b status=in-progress next=c") {
		t.Errorf("current digest = %q, want phase=b status=in-progress next=c", out)
	}
}

func TestStateSetRejectsAdaptiveDeliveryTransitions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	before, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"set", "demo", "implement", "done"},
		{"set", "demo", "open-pr", "skipped"},
		{"set", "demo", "route", "in-progress"},
	} {
		if _, err := runState(t, args...); err == nil ||
			!strings.Contains(err.Error(), "state dispatch") {
			t.Fatalf("adaptive state set %v error = %v", args, err)
		}
	}
	after, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	before.Updated = ""
	after.Updated = ""
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected adaptive set mutated state:\nbefore=%+v\nafter=%+v", before, after)
	}
}
