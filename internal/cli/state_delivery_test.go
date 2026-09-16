package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/prwatch"
)

func dispatchPhase(t *testing.T, slug, phase string) string {
	t.Helper()
	args := []string{"dispatch", slug, phase}
	if phase == "route" || phase == "open-pr" {
		args = append(args, "--inline")
	} else {
		args = append(args, "--owner", phase+"-worker")
	}
	out, err := runState(t, args...)
	if err != nil {
		t.Fatal(err)
	}

	var dispatch dispatchOutput
	if err := json.Unmarshal([]byte(out), &dispatch); err != nil {
		t.Fatal(err)
	}
	if dispatch.DispatchToken == "" {
		t.Fatal("dispatch returned an empty token")
	}
	testDispatchTokens[dispatch.DispatchToken] = dispatch
	return dispatch.DispatchToken
}

func TestAdaptiveDispatchRequiresCoordinatorCapabilityAndRestrictsInline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	coordinatorToken := initAdaptiveState(t, "demo")
	if _, err := runStateRaw(t, "dispatch", "demo", "route", "--inline"); err == nil ||
		!strings.Contains(err.Error(), "coordinator") {
		t.Fatalf("unauthenticated dispatch error = %v", err)
	}
	routeOut, err := runStateRaw(
		t, "dispatch", "demo", "route", "--inline",
		"--coordinator-token", coordinatorToken,
	)
	if err != nil {
		t.Fatal(err)
	}
	var routeDispatch dispatchOutput
	if err := json.Unmarshal([]byte(routeOut), &routeDispatch); err != nil {
		t.Fatal(err)
	}
	state, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteStandard)
	prepareAdaptivePhase(&state, "implement")
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runStateRaw(
		t, "dispatch", "demo", "implement", "--inline",
		"--coordinator-token", coordinatorToken,
	); err == nil || !strings.Contains(err.Error(), "cannot be dispatched inline") {
		t.Fatalf("inline worker dispatch error = %v", err)
	}
}

func TestDispatchOutputFailureLeavesPhaseRecoverable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	coordinatorToken := initAdaptiveState(t, "demo")
	original := encodeDispatchOutput
	encodeDispatchOutput = func(dispatchOutput) error { return fmt.Errorf("closed pipe") }
	t.Cleanup(func() { encodeDispatchOutput = original })
	if _, err := runStateRaw(
		t, "dispatch", "demo", "route", "--inline",
		"--coordinator-token", coordinatorToken,
	); err == nil || !strings.Contains(err.Error(), "publish dispatch capability") {
		t.Fatalf("dispatch output error = %v", err)
	}
	state, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if state.Phases["route"].Status != project.PhasePending ||
		state.Phases["route"].Dispatch != nil {
		t.Fatalf("failed dispatch publication persisted unusable state: %+v", state.Phases["route"])
	}
}

func TestWorkerDispatchCapabilitiesAreScopedAndOneTime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	state.Route.Facts.GatePolicy = project.GatePolicy{Mode: project.GatePolicyNone}
	state.Route.Snapshot, err = projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	prepareAdaptivePhase(&state, "implement")
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	ensureTestCoordinator(t, "demo")
	out, err := runStateRaw(
		t, "dispatch", "demo", "implement", "--owner", "worker",
		"--coordinator-token", testCoordinatorToken,
	)
	if err != nil {
		t.Fatal(err)
	}
	var dispatch dispatchOutput
	if err := json.Unmarshal([]byte(out), &dispatch); err != nil {
		t.Fatal(err)
	}
	if _, err := runStateRaw(
		t, "evidence", "record", "demo", "review", "--result", "passed",
		"--role", "code-reviewer", "--dispatch-token", dispatch.DispatchToken,
	); err == nil {
		t.Fatal("finish capability was accepted for review evidence")
	}
	if _, err := runStateRaw(
		t, "evidence", "record", "demo", "review", "--result", "passed",
		"--role", "code-reviewer", "--dispatch-token", dispatch.ReviewToken,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runStateRaw(
		t, "evidence", "record", "demo", "review", "--result", "passed",
		"--role", "code-reviewer", "--dispatch-token", dispatch.ReviewToken,
	); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("reused review capability error = %v", err)
	}
	if _, err := runStateRaw(
		t, "evidence", "record", "demo", "validation", "--result", "passed",
		"--no-gates", "--dispatch-token", dispatch.ValidationToken,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runStateRaw(
		t, "finish", "demo", "implement", "done", "--outcome", "material",
		"--dispatch-token", dispatch.DispatchToken,
	); err != nil {
		t.Fatal(err)
	}
}

func TestSupersededDispatchTokensFailEveryMutationSurface(t *testing.T) {
	t.Run("evidence and finish", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		repo := initCLIGitRepo(t)
		saveDeliveryProject(t, "demo", repo)
		state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
		if err != nil {
			t.Fatal(err)
		}
		state.Route = testRouteDecision(project.RouteStandard)
		state.Route.Snapshot, err = projectSnapshot("demo")
		if err != nil {
			t.Fatal(err)
		}
		prepareAdaptivePhase(&state, "review")
		refreshRouteDigest(t, state.Route)
		if err := project.SaveState(project.StatePath("demo"), state); err != nil {
			t.Fatal(err)
		}
		ensureTestCoordinator(t, "demo")
		oldOut, err := runStateRaw(
			t, "dispatch", "demo", "review", "--owner", "old",
			"--coordinator-token", testCoordinatorToken,
		)
		if err != nil {
			t.Fatal(err)
		}
		var old dispatchOutput
		if err := json.Unmarshal([]byte(oldOut), &old); err != nil {
			t.Fatal(err)
		}
		if _, err := runStateRaw(
			t, "finish", "demo", "review", "blocked", "--reason", "replace worker",
			"--dispatch-token", old.DispatchToken,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := runStateRaw(
			t, "dispatch", "demo", "review", "--owner", "replacement",
			"--coordinator-token", testCoordinatorToken,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := runStateRaw(
			t, "evidence", "record", "demo", "review", "--result", "passed",
			"--role", "code-reviewer", "--dispatch-token", old.ReviewToken,
		); err == nil {
			t.Fatal("superseded token recorded evidence")
		}
		if _, err := runStateRaw(
			t, "finish", "demo", "review", "done", "--outcome", "material",
			"--dispatch-token", old.DispatchToken,
		); err == nil {
			t.Fatal("superseded token finished replacement dispatch")
		}
	})

	t.Run("pr and final", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		repo := initCLIGitRepo(t)
		saveDeliveryProject(t, "demo", repo)
		state := openPRReadyState(t, "demo")
		if err := project.SaveState(project.StatePath("demo"), state); err != nil {
			t.Fatal(err)
		}
		ensureTestCoordinator(t, "demo")
		oldOut, err := runStateRaw(
			t, "dispatch", "demo", "open-pr", "--inline",
			"--coordinator-token", testCoordinatorToken,
		)
		if err != nil {
			t.Fatal(err)
		}
		var old dispatchOutput
		if err := json.Unmarshal([]byte(oldOut), &old); err != nil {
			t.Fatal(err)
		}
		if _, err := runStateRaw(
			t, "final", "demo", "failed", "--reason", "replace",
			"--dispatch-token", old.ResultToken,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := runStateRaw(
			t, "dispatch", "demo", "open-pr", "--inline",
			"--coordinator-token", testCoordinatorToken,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := runStateRaw(
			t, "pr", "demo", "--number", "42", "--url", "https://github.com/example/test/pull/42",
			"--dispatch-token", old.ResultToken,
		); err == nil {
			t.Fatal("superseded result token recorded a PR")
		}
		if _, err := runStateRaw(
			t, "final", "demo", "failed", "--reason", "old token",
			"--dispatch-token", old.ResultToken,
		); err == nil {
			t.Fatal("superseded result token recorded a final result")
		}
	})
}

func TestAdaptiveOpenPRDispatchMustBeInline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state := openPRReadyState(t, "demo")
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "dispatch", "demo", "open-pr"); err == nil ||
		!strings.Contains(err.Error(), "must be dispatched inline") {
		t.Fatalf("non-inline open-pr dispatch error = %v", err)
	}
}

func TestAdaptiveOpenPRDispatchRejectsStaleLiveRemoteBase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state := openPRReadyState(t, "demo")
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	ensureTestCoordinator(t, "demo")
	tree := gitRevParse(t, repo, "origin/main^{tree}")
	moved := strings.TrimSpace(gitOutput(
		t, repo, "commit-tree", tree, "-p", "origin/main", "-m", "remote moved",
	))
	gitOutput(t, repo, "push", "-q", "origin", moved+":refs/heads/main")
	if _, err := runStateRaw(
		t, "dispatch", "demo", "open-pr", "--inline",
		"--coordinator-token", testCoordinatorToken,
	); err == nil || !strings.Contains(err.Error(), "remote base binding is stale") {
		t.Fatalf("stale remote base dispatch error = %v", err)
	}
}

func TestAdaptiveWorkerDispatchRequiresOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	prepareAdaptivePhase(&state, "implement")
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "dispatch", "demo", "implement"); err == nil ||
		!strings.Contains(err.Error(), "--owner") {
		t.Fatalf("ownerless worker dispatch error = %v", err)
	}
}

func finishPhase(t *testing.T, slug, phase, token string, args ...string) {
	t.Helper()
	command := []string{"finish", slug, phase}
	command = append(command, args...)
	command = append(command, "--dispatch-token", token)
	if _, err := runState(t, command...); err != nil {
		t.Fatal(err)
	}
}

func TestStateDispatchAndFinish(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := runState(t, "init", "demo", "--workflow", "deliver-pr", "--phases", "route,implement"); err != nil {
		t.Fatal(err)
	}
	out, err := runState(t, "dispatch", "demo", "route", "--inline")
	if err != nil {
		t.Fatal(err)
	}
	var routeDispatch dispatchOutput
	if err := json.Unmarshal([]byte(out), &routeDispatch); err != nil {
		t.Fatal(err)
	}
	state, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if state.Phases["route"].StartedAt == "" || state.SubagentCount != 0 {
		t.Fatalf("inline dispatch state = %+v", state)
	}
	if state.Phases["route"].Dispatch == nil ||
		state.Phases["route"].Dispatch.TokenHash == routeDispatch.DispatchToken {
		t.Fatalf("dispatch token was not stored as a one-way hash: %+v", state.Phases["route"].Dispatch)
	}
	finishPhase(t, "demo", "route", routeDispatch.DispatchToken,
		"done", "--outcome", "material", "--artifact", "route.md")
	dispatchPhase(t, "demo", "implement")
	state, err = project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if state.SubagentCount != 1 || state.Phases["route"].EndedAt == "" ||
		state.Phases["route"].Artifact != "route.md" ||
		state.Phases["route"].Outcome != project.PhaseOutcomeMaterial {
		t.Fatalf("finished/dispatch state = %+v", state)
	}
}

func TestStateFinishRequiresReasonAndOutcome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := runState(t, "init", "demo", "--workflow", "deliver-pr", "--phases", "route"); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "finish", "demo", "route", "skipped", "--outcome", "no-op"); err == nil {
		t.Fatal("skipped finish accepted without reason")
	}
	if _, err := runState(t, "finish", "demo", "route", "done"); err == nil {
		t.Fatal("done finish accepted without outcome")
	}
}

func TestAdaptiveFinishRequiresActiveCurrentSelectedPhase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteStandard)
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "finish", "demo", "implement", "done", "--outcome", "material"); err == nil {
		t.Fatal("out-of-order adaptive finish was accepted")
	}
	routeToken := dispatchPhase(t, "demo", "route")
	finishPhase(t, "demo", "route", routeToken, "done", "--outcome", "material")
	if _, err := runState(t, "finish", "demo", "clarify", "done", "--outcome", "material"); err == nil {
		t.Fatal("pending adaptive phase was completed without a dispatch")
	}
}

func TestAdaptiveAdvanceRequiresGuardedFinish(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteStandard)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "dispatch", "demo", "route", "--inline"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "advance", "demo"); err == nil {
		t.Fatal("adaptive advance completed a dispatched phase without outcome metadata")
	}
	after, err := os.ReadFile(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, after) {
		t.Fatal("rejected adaptive advance mutated state")
	}
}

func TestDeliveryEvidenceFreshnessTracksWorktree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	if _, err := runState(t, "init", "demo", "--workflow", "deliver-pr", "--phases", "route,implement,review,open-pr"); err != nil {
		t.Fatal(err)
	}
	state, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	prepareAdaptivePhase(&state, "implement")
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}

	token := dispatchPhase(t, "demo", "implement")
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "passed", "--artifact", "validation.md",
		"--gate", "test=go test ./...", "--exit-status", "0",
		"--dispatch-token", token); err != nil {
		t.Fatal(err)
	}
	out, err := runState(t, "evidence", "fresh", "demo", "validation")
	if err != nil || strings.TrimSpace(out) != "fresh" {
		t.Fatalf("fresh evidence = %q, %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "evidence", "fresh", "demo", "validation"); err == nil ||
		!strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale evidence error = %v", err)
	}
}

func TestPassingEvidenceRejectsFailuresAndMissingReviewRoles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}

	state.Route = testRouteDecision(project.RouteHighRisk)
	state.Route.ReviewRoles = []string{project.ReviewRoleCodeReviewer, project.ReviewRoleSecurity}
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	prepareAdaptivePhase(&state, "review")
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	reviewToken := dispatchPhase(t, "demo", "review")
	reviewTests := []struct {
		name string
		args []string
	}{
		{
			name: "blocking review finding",
			args: []string{"evidence", "record", "demo", "review", "--result", "passed",
				"--role", "code-reviewer", "--role", "security", "--important", "1",
				"--dispatch-token", reviewToken},
		},
		{
			name: "missing selected review role",
			args: []string{"evidence", "record", "demo", "review", "--result", "passed",
				"--role", "code-reviewer", "--dispatch-token", reviewToken},
		},
		{
			name: "negative finding count",
			args: []string{"evidence", "record", "demo", "review", "--result", "failed",
				"--role", "code-reviewer", "--role", "security", "--critical", "-1",
				"--dispatch-token", reviewToken},
		},
	}
	for _, test := range reviewTests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := runState(t, test.args...); err == nil {
				t.Fatalf("contradictory evidence was accepted: %v", test.args)
			}
		})
	}
	if _, err := runState(t, "evidence", "record", "demo", "review",
		"--result", "passed", "--role", "code-reviewer", "--role", "security",
		"--dispatch-token", reviewToken); err != nil {
		t.Fatal(err)
	}
	finishPhase(t, "demo", "review", reviewToken, "done", "--outcome", "material")
	validationToken := dispatchPhase(t, "demo", "validate")
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "passed", "--gate", "test=go test ./...", "--exit-status", "1",
		"--dispatch-token", validationToken); err == nil {
		t.Fatal("passing validation accepted a nonzero command")
	}
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "passed", "--no-gates", "--dispatch-token", validationToken); err == nil {
		t.Fatal("required gate policy accepted no-gates evidence")
	}
}

func TestValidationEvidenceAcceptsOnlyVerifiedNoGatesPolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	state.Route.Facts.GatePolicy = project.GatePolicy{Mode: project.GatePolicyNone}
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	prepareAdaptivePhase(&state, "implement")
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	token := dispatchPhase(t, "demo", "implement")
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "passed", "--no-gates", "--dispatch-token", token); err != nil {
		t.Fatalf("verified no-gates evidence rejected: %v", err)
	}
	if _, err := runState(t, "evidence", "fresh", "demo", "validation"); err != nil {
		t.Fatalf("verified no-gates evidence was not fresh: %v", err)
	}
}

func TestEvidenceRequiresActiveCanonicalDispatchToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteStandard)
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	prepareAdaptivePhase(&state, "review")
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}

	if _, err := runState(t, "evidence", "record", "demo", "review",
		"--result", "passed", "--role", "code-reviewer",
		"--dispatch-token", "forged"); err == nil ||
		!strings.Contains(err.Error(), "not actively dispatched") {
		t.Fatalf("evidence without active owner dispatch error = %v", err)
	}
	reviewToken := dispatchPhase(t, "demo", "review")
	if _, err := runState(t, "evidence", "record", "demo", "review",
		"--result", "passed", "--role", "code-reviewer",
		"--dispatch-token", "forged"); err == nil ||
		!strings.Contains(err.Error(), "invalid dispatch token") {
		t.Fatalf("forged dispatch token error = %v", err)
	}
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "passed", "--no-gates",
		"--dispatch-token", reviewToken); err == nil ||
		!strings.Contains(err.Error(), "validate") {
		t.Fatalf("non-owner dispatch recorded validation: %v", err)
	}
}

func TestEvidenceRecordRedactsAndHashesCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	command := "TOKEN=super-secret go test ./pkg/a,./pkg/b"
	state.Route.Facts.GatePolicy.Gates[0].CommandDigest = commandDigestForTest(command)
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	prepareAdaptivePhase(&state, "implement")
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}

	token := dispatchPhase(t, "demo", "implement")
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "passed",
		"--gate", "test="+command,
		"--exit-status", "0",
		"--dispatch-token", token); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(command))
	want := []project.CommandEvidence{{
		GateID: "test", Display: "go <redacted-args>",
		Digest: fmt.Sprintf("%x", sum), ExitStatus: 0,
	}}
	if got.Evidence.Validation == nil || got.Evidence.Validation.Owner != "implement" ||
		!slices.Equal(got.Evidence.Validation.Commands, want) {
		t.Fatalf("validation commands = %+v, want %+v", got.Evidence.Validation, want)
	}
	data, err := os.ReadFile(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "super-secret") {
		t.Fatal("validation command secret leaked into state")
	}
}

func TestRedispatchInvalidatesCanonicalOwnerEvidence(t *testing.T) {
	tests := []struct {
		name  string
		class string
		owner string
	}{
		{name: "easy implement", class: project.RouteEasy, owner: "implement"},
		{name: "standard review", class: project.RouteStandard, owner: "review"},
		{name: "standard validation", class: project.RouteStandard, owner: "validate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repo := initCLIGitRepo(t)
			saveDeliveryProject(t, "demo", repo)
			state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
			if err != nil {
				t.Fatal(err)
			}
			state.Route = testRouteDecision(test.class)
			snapshot, err := projectSnapshot("demo")
			if err != nil {
				t.Fatal(err)
			}
			state.Route.Snapshot = snapshot
			prepareAdaptivePhase(&state, test.owner)
			refreshRouteDigest(t, state.Route)
			if err := project.SaveState(project.StatePath("demo"), state); err != nil {
				t.Fatal(err)
			}
			token := dispatchPhase(t, "demo", test.owner)
			args := []string{
				"evidence", "record", "demo", "review", "--result", "passed",
				"--role", "code-reviewer", "--dispatch-token", token,
			}
			if test.owner == "validate" {
				args = []string{
					"evidence", "record", "demo", "validation", "--result", "passed",
					"--gate", "test=go test ./...", "--exit-status", "0",
					"--dispatch-token", token,
				}
			}
			if test.owner == "implement" {
				if _, err := runState(t, args...); err != nil {
					t.Fatal(err)
				}
				if _, err := runState(t, "evidence", "record", "demo", "validation",
					"--result", "passed", "--gate", "test=go test ./...", "--exit-status", "0",
					"--dispatch-token", token); err != nil {
					t.Fatal(err)
				}
			} else if _, err := runState(t, args...); err != nil {
				t.Fatal(err)
			}
			finishPhase(t, "demo", test.owner, token, "escalated", "--reason", "redo required")
			dispatchPhase(t, "demo", test.owner)
			got, err := project.LoadState(project.StatePath("demo"))
			if err != nil {
				t.Fatal(err)
			}
			if test.owner == "implement" &&
				(got.Evidence.Review != nil || got.Evidence.Validation != nil) {
				t.Fatalf("implement redispatch retained evidence: %+v", got.Evidence)
			}
			if test.owner == "review" && got.Evidence.Review != nil {
				t.Fatal("review redispatch retained review evidence")
			}
			if test.owner == "validate" && got.Evidence.Validation != nil {
				t.Fatal("validation redispatch retained validation evidence")
			}
		})
	}
}

func TestFailedValidationEvidenceReopensImplementation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}

	state.Phases["route"] = project.PhaseState{Status: project.PhaseDone}
	state.Phases["clarify"] = project.PhaseState{Status: project.PhaseSkipped, Reason: "explicit"}
	state.Phases["plan"] = project.PhaseState{Status: project.PhaseSkipped, Reason: "small"}
	state.Phases["implement"] = project.PhaseState{Status: project.PhaseDone, Artifact: "implementation.md"}
	state.Phases["simplify"] = project.PhaseState{Status: project.PhaseSkipped, Reason: "simple"}
	state.Phases["review"] = project.PhaseState{Status: project.PhaseDone}
	state.Route = testRouteDecision(project.RouteStandard)
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}

	token := dispatchPhase(t, "demo", "validate")
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "failed", "--artifact", "validation.md",
		"--gate", "test=go test ./...", "--exit-status", "1",
		"--dispatch-token", token); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Phases["validate"].Status != project.PhaseEscalated ||
		got.Phases["implement"].Status != project.PhaseEscalated ||
		got.Phases["review"].Status != project.PhaseEscalated ||
		got.Phases["implement"].Task != "validation.md" {
		t.Fatalf("failed validation routing = %+v", got.Phases)
	}
	if got.Route.Class != project.RouteHighRisk ||
		!slices.Contains(got.Route.Facts.RiskTriggers, project.RiskTrigger("failed-gate")) ||
		!slices.Contains(got.Route.EscalationReasons, "validation evidence failed") {
		t.Fatalf("failed validation route = %+v", got.Route)
	}
	if got.Next() != "implement" {
		t.Fatalf("next phase = %q, want implement; validation=%+v", got.Next(), *got.Evidence.Validation)
	}
}

func TestImportantReviewEvidenceReopensImplementation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Phases["route"] = project.PhaseState{Status: project.PhaseDone}
	state.Phases["clarify"] = project.PhaseState{Status: project.PhaseSkipped, Reason: "explicit"}
	state.Phases["plan"] = project.PhaseState{Status: project.PhaseSkipped, Reason: "small"}
	state.Phases["implement"] = project.PhaseState{Status: project.PhaseDone}
	state.Phases["simplify"] = project.PhaseState{Status: project.PhaseSkipped, Reason: "simple"}
	state.Route = testRouteDecision(project.RouteStandard)
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}

	token := dispatchPhase(t, "demo", "review")
	if _, err := runState(t, "evidence", "record", "demo", "review",
		"--result", "failed", "--artifact", "review.md",
		"--role", "code-reviewer", "--important", "1",
		"--dispatch-token", token); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Phases["review"].Status != project.PhaseEscalated ||
		got.Phases["implement"].Status != project.PhaseEscalated ||
		got.Next() != "implement" {
		t.Fatalf("failed review routing = %+v; review=%+v", got.Phases, *got.Evidence.Review)
	}
}

func TestBlockedEvidenceFailuresTakePrecedence(t *testing.T) {
	tests := map[string]struct {
		phase string
		kind  string
		args  []string
	}{
		"review": {
			phase: "review",
			kind:  "review",
			args: []string{
				"--result", "blocked", "--role", "code-reviewer", "--important", "1",
				"--blocker-category", "authentication",
				"--blocker-reason", "review service login expired",
			},
		},
		"validation": {
			phase: "validate",
			kind:  "validation",
			args: []string{
				"--result", "blocked", "--gate", "test=go test ./...", "--exit-status", "1",
				"--blocker-category", "authentication",
				"--blocker-reason", "artifact registry login expired",
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repo := initCLIGitRepo(t)
			saveDeliveryProject(t, "demo", repo)
			state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
			if err != nil {
				t.Fatal(err)
			}
			state.Route = testRouteDecision(project.RouteStandard)
			snapshot, err := projectSnapshot("demo")
			if err != nil {
				t.Fatal(err)
			}
			state.Route.Snapshot = snapshot
			prepareAdaptivePhase(&state, test.phase)
			refreshRouteDigest(t, state.Route)
			if err := project.SaveState(project.StatePath("demo"), state); err != nil {
				t.Fatal(err)
			}
			token := dispatchPhase(t, "demo", test.phase)
			args := append([]string{"evidence", "record", "demo", test.kind}, test.args...)
			args = append(args, "--dispatch-token", token)
			if _, err := runState(t, args...); err != nil {
				t.Fatal(err)
			}
			got, err := project.LoadState(project.StatePath("demo"))
			if err != nil {
				t.Fatal(err)
			}
			var evidence *project.EvidenceRecord
			if test.kind == project.EvidenceKindReview {
				evidence = got.Evidence.Review
			} else {
				evidence = got.Evidence.Validation
			}
			if evidence == nil || evidence.Result != project.EvidenceFailed ||
				evidence.BlockerCategory == "" || evidence.BlockerReason == "" ||
				got.Phases["implement"].Status != project.PhaseEscalated {
				t.Fatalf("failure precedence state = evidence %+v phases %+v", evidence, got.Phases)
			}
		})
	}
}

func TestBlockedReviewEvidenceStaysWithEvidenceOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteStandard)
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	prepareAdaptivePhase(&state, "review")
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	token := dispatchPhase(t, "demo", "review")
	if _, err := runState(t, "evidence", "record", "demo", "review",
		"--result", "blocked",
		"--blocker-category", "authentication",
		"--blocker-reason", "review service login expired",
		"--dispatch-token", token); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Evidence.Review == nil ||
		got.Evidence.Review.BlockerCategory != "authentication" ||
		got.Evidence.Review.BlockerReason != "review service login expired" {
		t.Fatalf("blocked review evidence = %+v", got.Evidence.Review)
	}
	if got.Phases["review"].Status != project.PhaseBlocked ||
		got.Phases["implement"].Status != project.PhaseDone ||
		got.Route.Class != project.RouteStandard ||
		slices.Contains(got.Route.Facts.RiskTriggers, project.RiskUnresolvedReviewCI) {
		t.Fatalf("blocked review recovery = route %+v phases %+v", got.Route, got.Phases)
	}
}

func TestBlockedValidationEvidenceRecordsWithoutFabricatedGatesAndRecovers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteStandard)
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	prepareAdaptivePhase(&state, "validate")
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}

	token := dispatchPhase(t, "demo", "validate")
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "blocked",
		"--blocker-category", "authentication",
		"--blocker-reason", "artifact registry login expired",
		"--dispatch-token", token); err != nil {
		t.Fatal(err)
	}
	blocked, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Evidence.Validation == nil ||
		len(blocked.Evidence.Validation.Commands) != 0 ||
		blocked.Evidence.Validation.NoGates ||
		blocked.Evidence.Validation.BlockerCategory != "authentication" ||
		blocked.Evidence.Validation.BlockerReason != "artifact registry login expired" ||
		blocked.Phases["validate"].Status != project.PhaseBlocked {
		t.Fatalf("blocked validation state = %+v", blocked)
	}
	if _, err := runState(t, "dispatch", "demo", "open-pr", "--inline"); err == nil {
		t.Fatal("open-pr dispatched with blocked validation evidence")
	}

	recoveryToken := dispatchPhase(t, "demo", "validate")
	recovering, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if recovering.Evidence.Validation != nil {
		t.Fatal("validation redispatch retained blocked evidence")
	}
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "passed",
		"--gate", "test=go test ./...",
		"--exit-status", "0",
		"--dispatch-token", recoveryToken); err != nil {
		t.Fatal(err)
	}
	recovered, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Evidence.Validation == nil ||
		recovered.Evidence.Validation.Result != project.EvidencePassed ||
		recovered.Evidence.Validation.BlockerCategory != "" ||
		recovered.Evidence.Validation.BlockerReason != "" {
		t.Fatalf("recovered validation evidence = %+v", recovered.Evidence.Validation)
	}
}

func TestFailedEasyReviewRestoresNewlySelectedPhases(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	state.Phases["route"] = project.PhaseState{
		Status: project.PhaseDone, Outcome: project.PhaseOutcomeMaterial,
	}
	for _, phase := range []string{"clarify", "plan", "simplify", "validate"} {
		state.Phases[phase] = project.PhaseState{
			Status: project.PhaseSkipped, Reason: "easy route", Outcome: project.PhaseOutcomeNoOp,
		}
	}
	state.Phases["implement"] = project.PhaseState{
		Status: project.PhaseEscalated, Reason: "review must be rerun",
	}
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}

	token := dispatchPhase(t, "demo", "implement")
	if _, err := runState(t, "evidence", "record", "demo", "review",
		"--result", "failed", "--artifact", "review.md",
		"--role", "code-reviewer", "--important", "1",
		"--dispatch-token", token); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"clarify", "plan"} {
		if got.Phases[phase].Status != project.PhaseSkipped {
			t.Errorf("high-risk escalation left selected %s phase as %q", phase, got.Phases[phase].Status)
		}
	}
	if got.Next() != "implement" {
		t.Errorf("failed review next phase = %q, want implement; review=%+v", got.Next(), *got.Evidence.Review)
	}
	if got.Phases["review"].Status != project.PhaseEscalated {
		t.Errorf("review status = %q, want escalated", got.Phases["review"].Status)
	}
	if got.Phases["validate"].Status != project.PhasePending {
		t.Errorf("newly selected validate phase remained %q", got.Phases["validate"].Status)
	}
	if got.Evidence.Review == nil || got.Evidence.Review.Owner != "implement" {
		t.Fatalf("easy review evidence owner = %+v", got.Evidence.Review)
	}
}

func TestReviewFreshRequiresCurrentSelectedRoles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}

	state.Route = testRouteDecision(project.RouteHighRisk)
	state.Route.ReviewRoles = []string{project.ReviewRoleCodeReviewer, project.ReviewRoleSecurity}
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	refreshRouteDigest(t, state.Route)
	state.Evidence.Review = &project.EvidenceRecord{
		Snapshot: snapshot, Result: project.EvidencePassed,
		RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
		DispatchID: "dispatch-1", Owner: project.EvidenceOwnerReview,
		CompletedAt: "2026-09-15T00:05:00Z",
		Roles:       []string{project.ReviewRoleCodeReviewer},
	}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "evidence", "fresh", "demo", "review"); err == nil ||
		!strings.Contains(err.Error(), "selected roles") {
		t.Fatalf("review freshness error = %v", err)
	}
}

func TestAdaptiveOpenPRTransitionsRequireFreshEvidenceAndActiveDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	state.Route.Facts.GatePolicy = project.GatePolicy{Mode: project.GatePolicyNone}
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	prepareAdaptivePhase(&state, "open-pr")
	refreshRouteDigest(t, state.Route)
	implement := state.Phases[project.EvidenceOwnerImplement]
	implement.CompletedDispatchID = "implement-dispatch"
	state.Phases[project.EvidenceOwnerImplement] = implement
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	ensureTestCoordinator(t, "demo")
	state, err = project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "dispatch", "demo", "open-pr", "--inline"); err == nil {
		t.Fatal("open-pr dispatch accepted missing evidence")
	}
	after, err := os.ReadFile(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, after) {
		t.Fatal("rejected open-pr dispatch mutated state")
	}

	state.Evidence.Review = &project.EvidenceRecord{
		Snapshot: snapshot, RouteRevision: state.Route.Revision,
		RouteDigest: state.Route.Digest, DispatchID: "implement-dispatch",
		Result: project.EvidencePassed, Owner: project.EvidenceOwnerImplement,
		CompletedAt: "2026-09-15T00:05:00Z",
		Roles:       append([]string(nil), state.Route.ReviewRoles...),
	}
	state.Evidence.Validation = &project.EvidenceRecord{
		Snapshot: snapshot, RouteRevision: state.Route.Revision,
		RouteDigest: state.Route.Digest, DispatchID: "implement-dispatch",
		Result: project.EvidencePassed, Owner: project.EvidenceOwnerImplement,
		CompletedAt: "2026-09-15T00:06:00Z", NoGates: true,
	}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "pr", "demo", "--number", "42", "--url", "https://github.com/example/test/pull/42"); err == nil {
		t.Fatal("adaptive PR recording succeeded without an active open-pr dispatch")
	}
	openPRToken := dispatchPhase(t, "demo", "open-pr")
	if _, err := runState(t, "finish", "demo", "open-pr", "skipped",
		"--reason", "not needed", "--outcome", "no-op",
		"--dispatch-token", openPRToken); err == nil {
		t.Fatal("selected open-pr phase was skipped")
	}
	if _, err := runState(t, "finish", "demo", "open-pr", "done", "--outcome", "material",
		"--dispatch-token", openPRToken); err == nil {
		t.Fatal("open-pr completed before the PR result was recorded")
	}
	if _, err := runState(t, "pr", "demo", "--number", "42"); err == nil {
		t.Fatal("adaptive PR recording accepted a partial replacement")
	}
	if _, err := runState(t, "pr", "demo", "--number", "42", "--url", "https://github.com/example/test/pull/42",
		"--dispatch-token", openPRToken); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.PR.Number != 42 || got.FinalResult == nil ||
		got.FinalResult.Status != "opened" ||
		got.Phases["open-pr"].Status != project.PhaseDone {
		t.Fatalf("guarded open-pr result = %+v, phase = %+v", got.FinalResult, got.Phases["open-pr"])
	}
}

func TestAdaptiveOpenPRSuccessCommandsRejectPostDispatchMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state := openPRReadyState(t, "demo")
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	openPRToken := dispatchPhase(t, "demo", "open-pr")
	if err := os.WriteFile(filepath.Join(repo, "changed-after-dispatch.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runState(
		t, "pr", "demo", "--number", "42", "--url", "https://github.com/example/test/pull/42",
		"--dispatch-token", openPRToken,
	); err == nil {
		t.Fatal("successful open-pr transition accepted a post-dispatch repository mutation")
	}
	after, err := os.ReadFile(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(before, after) {
		t.Fatal("ambiguous PR creation did not retain a reconciliation candidate")
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.PendingPR.Number != 42 || got.PendingPR.URL != "https://github.com/example/test/pull/42" ||
		got.PR.Number != 0 || got.FinalResult != nil {
		t.Fatalf("post-dispatch mutation state = %+v", got)
	}
}

func TestAdaptivePRRecordingRejectsMismatchedGitHubMetadata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*prwatch.PullRequest)
		want   string
	}{
		{name: "head branch", mutate: func(pr *prwatch.PullRequest) {
			pr.HeadRef = "other"
		}, want: "head branch"},
		{name: "head sha", mutate: func(pr *prwatch.PullRequest) {
			pr.HeadSHA = strings.Repeat("a", 40)
		}, want: "head SHA"},
		{name: "base branch", mutate: func(pr *prwatch.PullRequest) {
			pr.BaseRef = "release"
		}, want: "base"},
		{name: "base drift", mutate: func(pr *prwatch.PullRequest) {
			pr.BaseSHA = strings.Repeat("b", 40)
		}, want: "base SHA"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repo := initCLIGitRepo(t)
			saveDeliveryProject(t, "demo", repo)
			state := openPRReadyState(t, "demo")
			if err := project.SaveState(project.StatePath("demo"), state); err != nil {
				t.Fatal(err)
			}
			token := dispatchPhase(t, "demo", "open-pr")
			original := readPRForRecording
			readPRForRecording = func(
				_ context.Context, _ string, number int,
			) (prwatch.PullRequest, error) {
				manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), "demo"))
				if err != nil {
					return prwatch.PullRequest{}, err
				}
				pr := prwatch.PullRequest{
					Number: number, URL: fmt.Sprintf("https://github.com/example/test/pull/%d", number),
					State: prwatch.StateOpen, Repo: "example/test",
					HeadRef: manifest.Branch, HeadSHA: state.Route.Snapshot.HeadSHA,
					BaseRef: state.Route.Snapshot.BaseRef, BaseSHA: state.Route.Snapshot.BaseTipSHA,
				}
				test.mutate(&pr)
				return pr, nil
			}
			t.Cleanup(func() { readPRForRecording = original })
			if _, err := runState(
				t, "pr", "demo", "--number", "42",
				"--url", "https://github.com/example/test/pull/42", "--dispatch-token", token,
			); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("metadata mismatch error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAdaptivePRReconcileFindsAndRecordsExistingRemotePR(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state := openPRReadyState(t, "demo")
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	token := dispatchPhase(t, "demo", "open-pr")
	if _, err := runState(
		t, "pr", "demo", "--reconcile", "--dispatch-token", token,
	); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.PR.Number != 42 || got.FinalResult == nil ||
		got.FinalResult.Status != "opened" || got.PendingPR.Number != 0 {
		t.Fatalf("reconciled PR state = %+v", got)
	}
}

func TestAdaptivePRReconcilePrefersPersistedPendingPR(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state := openPRReadyState(t, "demo")
	state.PendingPR = project.PRRef{
		Number: 77, URL: "https://github.com/example/test/pull/77",
	}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	token := dispatchPhase(t, "demo", "open-pr")
	if _, err := runState(
		t, "pr", "demo", "--reconcile", "--dispatch-token", token,
	); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.PR.Number != 77 || got.PendingPR.Number != 0 {
		t.Fatalf("pending PR reconciliation = %+v", got)
	}
}

func TestAdaptivePRReconcileAcceptsMergedPendingPR(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state := openPRReadyState(t, "demo")
	state.PendingPR = project.PRRef{
		Number: 77, URL: "https://github.com/example/test/pull/77",
	}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	token := dispatchPhase(t, "demo", "open-pr")
	original := readPRForRecording
	readPRForRecording = func(
		ctx context.Context, worktree string, number int,
	) (prwatch.PullRequest, error) {
		pr, err := original(ctx, worktree, number)
		if err != nil {
			return prwatch.PullRequest{}, err
		}
		pr.State = prwatch.StateMerged
		pr.BaseSHA = strings.Repeat("f", 40)
		return pr, nil
	}
	t.Cleanup(func() { readPRForRecording = original })
	if _, err := runState(
		t, "pr", "demo", "--reconcile", "--dispatch-token", token,
	); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.PR.Number != 77 || got.FinalResult == nil ||
		got.FinalResult.Status != "opened" || got.PendingPR.Number != 0 {
		t.Fatalf("merged pending PR reconciliation = %+v", got)
	}
}

func TestAdaptiveFinalRefusesUnreconciledPendingPR(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state := openPRReadyState(t, "demo")
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	token := dispatchPhase(t, "demo", "open-pr")
	state, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	state.PendingPR = project.PRRef{
		Number: 42, URL: "https://github.com/example/test/pull/42",
	}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runStateRaw(
		t, "final", "demo", "failed", "--reason", "ambiguous recording",
		"--dispatch-token", token,
	); err == nil || !strings.Contains(err.Error(), "still OPEN") {
		t.Fatalf("pending PR final error = %v", err)
	}
	original := readPRForRecording
	readPRForRecording = func(
		ctx context.Context, worktree string, number int,
	) (prwatch.PullRequest, error) {
		pr, err := original(ctx, worktree, number)
		if err != nil {
			return prwatch.PullRequest{}, err
		}
		pr.State = prwatch.StateClosed
		return pr, nil
	}
	t.Cleanup(func() { readPRForRecording = original })
	if _, err := runStateRaw(
		t, "final", "demo", "failed", "--reason", "closed without merge",
		"--dispatch-token", token,
	); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.PendingPR.Number != 0 || got.FinalResult == nil ||
		got.FinalResult.Status != "failed" {
		t.Fatalf("closed pending PR final state = %+v", got)
	}
}

func TestAdaptiveFinalResultTransitionsOpenPRAtomically(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state := openPRReadyState(t, "demo")
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	openPRToken := dispatchPhase(t, "demo", "open-pr")
	if _, err := runState(t, "final", "demo", "opened"); err == nil {
		t.Fatal("adaptive final opened bypassed state pr")
	}
	if _, err := runState(t, "final", "demo", "failed", "--reason", "push rejected",
		"--dispatch-token", openPRToken); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.FinalResult == nil || got.FinalResult.Status != "failed" ||
		got.Phases["open-pr"].Status != project.PhaseBlocked ||
		got.Phases["open-pr"].Dispatch != nil {
		t.Fatalf("atomic failed result = %+v, phase = %+v", got.FinalResult, got.Phases["open-pr"])
	}
}

func TestAdaptiveTerminalMutationsRequireCanonicalDispatchToken(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, token string) error
	}{
		{
			name: "pr",
			run: func(t *testing.T, token string) error {
				_, err := runState(t, "pr", "demo", "--number", "42",
					"--url", "https://github.com/example/test/pull/42", "--dispatch-token", token)
				return err
			},
		},
		{
			name: "final",
			run: func(t *testing.T, token string) error {
				_, err := runState(t, "final", "demo", "failed",
					"--reason", "push rejected", "--dispatch-token", token)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repo := initCLIGitRepo(t)
			saveDeliveryProject(t, "demo", repo)
			state := openPRReadyState(t, "demo")
			if err := project.SaveState(project.StatePath("demo"), state); err != nil {
				t.Fatal(err)
			}
			dispatchPhase(t, "demo", "open-pr")
			if err := test.run(t, "forged"); err == nil ||
				(!strings.Contains(err.Error(), "invalid dispatch token") &&
					!strings.Contains(err.Error(), "dispatch capability")) {
				t.Fatalf("forged token error = %v", err)
			}
		})
	}
}

func openPRReadyState(t *testing.T, slug string) project.WorkflowState {
	t.Helper()
	state, err := project.NewState(slug, "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	state.Route.Facts.GatePolicy = project.GatePolicy{Mode: project.GatePolicyNone}
	snapshot, err := projectSnapshot(slug)
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Snapshot = snapshot
	prepareAdaptivePhase(&state, "open-pr")
	refreshRouteDigest(t, state.Route)
	state.Evidence.Review = &project.EvidenceRecord{
		Snapshot: snapshot, RouteRevision: state.Route.Revision,
		RouteDigest: state.Route.Digest, DispatchID: "implement-dispatch",
		Result: project.EvidencePassed, Owner: project.EvidenceOwnerImplement,
		CompletedAt: "2026-09-15T00:05:00Z",
		Roles:       append([]string(nil), state.Route.ReviewRoles...),
	}
	state.Evidence.Validation = &project.EvidenceRecord{
		Snapshot: snapshot, RouteRevision: state.Route.Revision,
		RouteDigest: state.Route.Digest, DispatchID: "implement-dispatch",
		Result: project.EvidencePassed, Owner: project.EvidenceOwnerImplement,
		CompletedAt: "2026-09-15T00:06:00Z", NoGates: true,
	}
	return state
}

func TestStatePRAndFailureCommandsWriteFinalResult(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := runState(t, "init", "opened", "--workflow", "deliver-pr", "--phases", "open-pr"); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "pr", "opened", "--number", "42"); err != nil {
		t.Fatalf("legacy number-only update: %v", err)
	}
	partial, err := project.LoadState(project.StatePath("opened"))
	if err != nil {
		t.Fatal(err)
	}
	if partial.PR.Number != 42 || partial.PR.URL != "" || partial.FinalResult != nil {
		t.Fatalf("legacy partial PR state = %+v, result = %+v", partial.PR, partial.FinalResult)
	}
	if _, err := runStateRaw(t, "pr", "opened", "--reconcile"); err == nil ||
		!strings.Contains(err.Error(), "requires an adaptive") {
		t.Fatalf("legacy reconcile error = %v", err)
	}
	if _, err := runState(t, "pr", "opened", "--url", "https://example.test/pull/42"); err != nil {
		t.Fatal(err)
	}
	opened, err := project.LoadState(project.StatePath("opened"))
	if err != nil {
		t.Fatal(err)
	}
	if opened.FinalResult == nil || opened.FinalResult.Status != "opened" ||
		opened.FinalResult.PRNumber != 42 {
		t.Fatalf("opened final result = %+v", opened.FinalResult)
	}
	if _, err := runState(t, "pr", "opened", "--number", "43"); err != nil {
		t.Fatalf("legacy number replacement: %v", err)
	}
	replaced, err := project.LoadState(project.StatePath("opened"))
	if err != nil {
		t.Fatal(err)
	}
	if replaced.PR.Number != 43 || replaced.PR.URL != "" || replaced.FinalResult != nil {
		t.Fatalf("legacy number was paired with stale URL: %+v, result = %+v", replaced.PR, replaced.FinalResult)
	}

	if _, err := runState(t, "init", "failed", "--workflow", "deliver-pr", "--phases", "open-pr"); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "final", "failed", "failed"); err == nil {
		t.Fatal("failed result without reason was accepted")
	}
	if _, err := runState(t, "final", "failed", "failed", "--reason", "push rejected"); err != nil {
		t.Fatal(err)
	}
	failed, err := project.LoadState(project.StatePath("failed"))
	if err != nil {
		t.Fatal(err)
	}
	if failed.FinalResult == nil || failed.FinalResult.Status != "failed" ||
		failed.FinalResult.Reason != "push rejected" {
		t.Fatalf("failed final result = %+v", failed.FinalResult)
	}
}

func TestStateWorkerRejectsOffRouteHelpersForEasyDelivery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "worker", "demo", "--task", "premature exploration"); err == nil {
		t.Fatal("adaptive delivery accepted a helper before classification")
	}
	state, err = project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "worker", "demo", "--task", "extra-review"); err == nil {
		t.Fatal("easy route accepted an off-route helper")
	}
	after, err := os.ReadFile(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, after) {
		t.Fatal("rejected easy-route helper mutated state")
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.SubagentCount != 0 {
		t.Fatalf("subagent count = %d, want 0", got.SubagentCount)
	}

	got.Route = testRouteDecision(project.RouteStandard)
	if err := project.SaveState(project.StatePath("demo"), got); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "worker", "demo", "--task", "extra-review"); err != nil {
		t.Fatalf("standard route rejected helper: %v", err)
	}
	got, err = project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.SubagentCount != 1 {
		t.Fatalf("standard subagent count = %d, want 1", got.SubagentCount)
	}
}

func TestAdaptiveRouteDispatchMustBeInline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "dispatch", "demo", "route"); err == nil {
		t.Fatal("adaptive route accepted a non-inline dispatch")
	}
	if _, err := runState(t, "dispatch", "demo", "route", "--inline"); err != nil {
		t.Fatal(err)
	}
}

func TestStateWorkerAllowsHelpersForForcedFullEasyDelivery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	state.Route.ForcedFull = true
	state.Route.Facts.AuthorRequestedFullWorkflow = true
	state.Route.SelectedPhases = append([]string(nil), project.AdaptiveDeliveryPhases...)
	state.Route.ReviewOwner = project.EvidenceOwnerReview
	state.Route.ValidationOwner = project.EvidenceOwnerValidate
	state.Route.Digest, err = project.RouteDigest(*state.Route)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}

	if _, err := runState(t, "worker", "demo", "--task", "extra-review"); err != nil {
		t.Fatalf("forced-full easy route rejected helper: %v", err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.SubagentCount != 1 {
		t.Fatalf("subagent count = %d, want 1", got.SubagentCount)
	}
}

func testRouteDecision(class string) *project.RouteDecision {
	selected := []string{"route", "implement", "review", "validate", "open-pr"}
	reviewOwner, validationOwner := "review", "validate"
	switch class {
	case project.RouteEasy:
		selected = []string{"route", "implement", "open-pr"}
		reviewOwner, validationOwner = "implement", "implement"
	case project.RouteHighRisk, project.RouteStackCandidate:
		selected = []string{"route", "clarify", "plan", "implement", "review", "validate", "open-pr"}
	}
	reasons := make(map[string]string, len(project.AdaptiveDeliveryPhases))
	for _, phase := range project.AdaptiveDeliveryPhases {
		reasons[phase] = "test route decision"
	}
	decision := &project.RouteDecision{
		Class:           class,
		SelectedPhases:  selected,
		PhaseReasons:    reasons,
		ReviewRoles:     []string{project.ReviewRoleCodeReviewer},
		ReviewOwner:     reviewOwner,
		ValidationOwner: validationOwner,
		Snapshot: project.RepositorySnapshot{
			BaseSHA: "base", HeadSHA: "head", Fingerprint: "fingerprint",
		},
		EvaluatedAt: "2026-09-15T00:00:00Z",
		Facts: project.RouteFacts{
			RequestedBehaviorExplicit: true,
			GatePolicy: project.GatePolicy{
				Mode: project.GatePolicyRequired,
				Gates: []project.RequiredGate{{
					ID: "test", CommandDigest: commandDigestForTest("go test ./..."),
					RedactedDisplay: "go <redacted-args>",
				}},
			},
			RiskAssessmentComplete: true,
			AssessmentFingerprint:  "fingerprint",
			PredictedSizeKnown:     true,
			PredictedFileCount:     1,
			PredictedChangedLines:  10,
		},
	}
	decision.Revision = 1
	digest, err := project.RouteDigest(*decision)
	if err != nil {
		panic(err)
	}
	decision.Digest = digest
	return decision
}

func prepareAdaptivePhase(state *project.WorkflowState, target string) {
	selected := make(map[string]bool, len(state.Route.SelectedPhases))
	for _, phase := range state.Route.SelectedPhases {
		selected[phase] = true
	}
	for _, phase := range state.Order {
		if phase == target {
			return
		}
		if selected[phase] {
			state.Phases[phase] = project.PhaseState{Status: project.PhaseDone}
		} else {
			state.Phases[phase] = project.PhaseState{
				Status:  project.PhaseSkipped,
				Reason:  "not selected",
				Outcome: project.PhaseOutcomeNoOp,
			}
		}
	}
}

func commandDigestForTest(command string) string {
	sum := sha256.Sum256([]byte(command))
	return fmt.Sprintf("%x", sum)
}

func refreshRouteDigest(t *testing.T, route *project.RouteDecision) {
	t.Helper()
	if route.Facts.RiskAssessmentComplete {
		route.Facts.AssessmentFingerprint = route.Snapshot.Revision()
	}
	route.Revision = 1
	digest, err := project.RouteDigest(*route)
	if err != nil {
		t.Fatal(err)
	}
	route.Digest = digest
}

func initCLIGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repo}, args...)...)
		command.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=relay", "GIT_AUTHOR_EMAIL=relay@example.com",
			"GIT_COMMITTER_NAME=relay", "GIT_COMMITTER_EMAIL=relay@example.com",
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("relay\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-q", "-m", "init")
	remote := filepath.Join(t.TempDir(), "origin.git")
	if output, err := exec.Command("git", "init", "--bare", "-q", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, output)
	}
	run("remote", "add", "origin", remote)
	run("push", "-q", "-u", "origin", "main")
	return repo
}

func saveDeliveryProject(t *testing.T, slug, repo string) {
	t.Helper()
	dir := filepath.Join(project.ActiveDir(), slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := repo
	baseSHA := gitRevParse(t, repo, "origin/main")
	branch := "feature"
	if err := project.Save(project.ManifestPath(project.ActiveDir(), slug), project.Manifest{
		Slug: slug, Repo: repo, Branch: branch, Worktree: &worktree,
		BaseBranch: "main", StartSHA: baseSHA, RemoteBaseSHA: baseSHA,
	}); err != nil {
		t.Fatal(err)
	}
	previousRead, previousFind := readPRForRecording, findPRsForRecording
	readPRForRecording = func(
		_ context.Context, _ string, number int,
	) (prwatch.PullRequest, error) {
		state, err := project.LoadState(project.StatePath(slug))
		if err != nil {
			return prwatch.PullRequest{}, err
		}
		return prwatch.PullRequest{
			Number: number, URL: fmt.Sprintf("https://github.com/example/test/pull/%d", number),
			State: prwatch.StateOpen, Repo: "example/test",
			HeadRef: branch, HeadSHA: state.Route.Snapshot.HeadSHA,
			BaseRef: "main", BaseSHA: baseSHA,
		}, nil
	}
	findPRsForRecording = func(
		ctx context.Context, worktree, head, base string,
	) ([]prwatch.PullRequest, error) {
		pr, err := readPRForRecording(ctx, worktree, 42)
		if err != nil {
			return nil, err
		}
		return []prwatch.PullRequest{pr}, nil
	}
	t.Cleanup(func() {
		readPRForRecording, findPRsForRecording = previousRead, previousFind
	})
}

func gitRevParse(t *testing.T, repo, ref string) string {
	t.Helper()
	output, err := exec.Command("git", "-C", repo, "rev-parse", ref).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}
