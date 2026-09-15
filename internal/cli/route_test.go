package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ronaknnathani/relay/internal/project"
)

func runRoute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := newCmdRoute()
	command.SetArgs(args)
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	return captureStdout(t, command.Execute)
}

func TestRouteClassifyPersistsEasyDecision(t *testing.T) {
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

	out, err := runRoute(t, "classify", "demo",
		"--requested-behavior-explicit",
		"--gate", "test=TOKEN=super-secret go test ./...",
		"--risk-assessment-complete",
		"--predicted-size-known",
		"--predicted-files", "1",
		"--predicted-lines", "20",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"class":"easy"`) {
		t.Fatalf("classify output = %q", out)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Route == nil || got.Route.Class != project.RouteEasy ||
		got.Route.Snapshot.Fingerprint == "" ||
		got.Phases["clarify"].Status != project.PhaseSkipped ||
		got.Phases["review"].Status != project.PhaseSkipped ||
		got.Route.ReviewOwner != "implement" ||
		got.Route.ValidationOwner != "implement" ||
		got.Phases["validate"].Reason == "" {
		t.Fatalf("persisted route = %+v, phases = %+v", got.Route, got.Phases)
	}
	data, err := os.ReadFile(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "super-secret") ||
		strings.Contains(string(data), "go test ./...") {
		t.Fatal("raw gate command was persisted")
	}
}

func TestRouteReclassifyReopensImplementationAndCompletesEasyDelivery(t *testing.T) {
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
	classify := []string{
		"classify", "demo", "--requested-behavior-explicit",
		"--no-repository-gates", "--risk-assessment-complete",
		"--predicted-size-known", "--predicted-files", "1", "--predicted-lines", "20",
	}
	if _, err := runState(t, "dispatch", "demo", "route", "--inline"); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, classify...); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "finish", "demo", "route", "done", "--outcome", "material"); err != nil {
		t.Fatal(err)
	}
	dispatchPhase(t, "demo", "implement")
	if err := os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, classify...); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Phases["implement"].Status != project.PhaseEscalated ||
		got.Phases["implement"].Dispatch != nil {
		t.Fatalf("reclassified implementation = %+v", got.Phases["implement"])
	}
	token := dispatchPhase(t, "demo", "implement")
	if _, err := runState(t, "evidence", "record", "demo", "review",
		"--result", "passed", "--role", "code-reviewer", "--dispatch-token", token); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "passed", "--no-gates", "--dispatch-token", token); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "finish", "demo", "implement", "done", "--outcome", "material"); err != nil {
		t.Fatal(err)
	}
	dispatchPhase(t, "demo", "open-pr")
	if _, err := runState(t, "pr", "demo", "--number", "42", "--url", "https://example.test/pull/42"); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "finish", "demo", "open-pr", "done", "--outcome", "material"); err != nil {
		t.Fatal(err)
	}
}

func TestRouteClassifyIsIdempotentForUnchangedFacts(t *testing.T) {
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
	args := []string{
		"classify", "demo", "--requested-behavior-explicit",
		"--no-repository-gates", "--risk-assessment-complete",
		"--predicted-size-known", "--predicted-files", "1", "--predicted-lines", "20",
	}
	if _, err := runRoute(t, args...); err != nil {
		t.Fatal(err)
	}
	before, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, args...); err != nil {
		t.Fatal(err)
	}
	after, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Route.Revision != before.Route.Revision ||
		after.Route.Digest != before.Route.Digest ||
		!slices.Equal(after.Route.EscalationReasons, before.Route.EscalationReasons) {
		t.Fatalf("unchanged classify churned route: before=%+v after=%+v", before.Route, after.Route)
	}
}

func TestRouteClassifyPersistsChangeSurfaceRoles(t *testing.T) {
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
	if _, err := runRoute(t, "classify", "demo",
		"--requested-behavior-explicit",
		"--no-repository-gates",
		"--risk-assessment-complete",
		"--predicted-size-known",
		"--predicted-files", "1",
		"--predicted-lines", "20",
		"--changes-tests",
		"--changes-documentation-comments",
		"--changes-type-design",
		"--history-sensitive",
		"--changes-repository-guidelines",
	); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{
		project.ReviewRolePRTestAnalyzer,
		project.ReviewRoleCommentAnalyzer,
		project.ReviewRoleTypeDesignAnalyzer,
		project.ReviewRoleGitHistory,
		project.ReviewRolePriorPRHistory,
	} {
		if !slices.Contains(got.Route.ReviewRoles, role) {
			t.Errorf("persisted roles %v missing %q", got.Route.ReviewRoles, role)
		}
	}
	if got.Route.ReviewOwner != project.EvidenceOwnerImplement ||
		slices.Contains(got.Route.SelectedPhases, "review") {
		t.Fatalf("easy specialist route dispatched extra review: %+v", got.Route)
	}
}

func TestRouteClassifyRejectsConflictingAndDuplicateGatePolicies(t *testing.T) {
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
	base := []string{
		"classify", "demo", "--requested-behavior-explicit",
		"--risk-assessment-complete", "--predicted-size-known",
		"--predicted-files", "1", "--predicted-lines", "20",
	}
	tests := map[string][]string{
		"conflicting": append(append([]string(nil), base...),
			"--gate", "test=go test ./...", "--no-repository-gates"),
		"duplicate id": append(append([]string(nil), base...),
			"--gate", "test=go test ./...", "--gate", "test=go test ./internal/..."),
		"duplicate command": append(append([]string(nil), base...),
			"--gate", "unit=go test ./...", "--gate", "all=go test ./..."),
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := runRoute(t, args...); err == nil {
				t.Fatal("invalid gate policy was accepted")
			}
		})
	}
}

func TestRouteRefreshEscalatesStaleEasyEvidenceToIndependentPhases(t *testing.T) {
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
	if _, err := runRoute(t, "classify", "demo",
		"--requested-behavior-explicit", "--gate", "test=go test ./...",
		"--risk-assessment-complete", "--predicted-size-known",
		"--predicted-files", "1", "--predicted-lines", "20",
	); err != nil {
		t.Fatal(err)
	}
	dispatchPhase(t, "demo", "route")
	if _, err := runState(t, "finish", "demo", "route", "done", "--outcome", "material"); err != nil {
		t.Fatal(err)
	}
	token := dispatchPhase(t, "demo", "implement")
	if _, err := runState(t, "evidence", "record", "demo", "review",
		"--result", "passed", "--artifact", "implementation.md",
		"--role", "code-reviewer", "--dispatch-token", token); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "passed", "--artifact", "implementation.md",
		"--gate", "test=go test ./...", "--exit-status", "0",
		"--dispatch-token", token); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, "refresh", "demo"); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Route.Class != project.RouteStandard ||
		got.Route.ReviewOwner != "review" ||
		got.Route.ValidationOwner != "validate" ||
		got.Route.Facts.RiskAssessmentComplete {
		t.Fatalf("stale evidence route = %+v", got.Route)
	}
	for _, phase := range []string{"review", "validate"} {
		if got.Phases[phase].Status != project.PhaseEscalated {
			t.Errorf("%s status = %q, want escalated", phase, got.Phases[phase].Status)
		}
	}
}

func TestRouteRefreshEscalatesOnActualDiffAndNeverDowngrades(t *testing.T) {
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
	if _, err := runRoute(t, "classify", "demo",
		"--requested-behavior-explicit", "--gate", "test=go test ./...",
		"--risk-assessment-complete", "--predicted-size-known",
		"--predicted-files", "1", "--predicted-lines", "20"); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("line\n", 151)
	if err := os.WriteFile(filepath.Join(repo, "large.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, "refresh", "demo"); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Route.Class != project.RouteStandard || got.Route.PreviousClass != project.RouteEasy ||
		got.Phases["validate"].Status != project.PhasePending {
		t.Fatalf("refreshed route = %+v, validate = %+v", got.Route, got.Phases["validate"])
	}
	if err := os.Remove(filepath.Join(repo, "large.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, "refresh", "demo"); err != nil {
		t.Fatal(err)
	}
	got, err = project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Route.Class != project.RouteStandard {
		t.Fatalf("refresh downgraded route to %q", got.Route.Class)
	}
	if got.Route.PreviousClass != project.RouteEasy ||
		!slices.Contains(got.Route.EscalationReasons, "current repository facts require a more conservative route") {
		t.Fatalf("equal-class refresh erased escalation history: %+v", got.Route)
	}
	if got.Phases["plan"].Status != project.PhasePending {
		t.Fatalf("refresh removed a conservatively selected plan phase: %+v", got.Phases["plan"])
	}
}

func TestRouteForcedFullAndExplicitEscalation(t *testing.T) {
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
	if _, err := runRoute(t, "classify", "demo",
		"--requested-behavior-explicit", "--gate", "test=go test ./...",
		"--risk-assessment-complete", "--predicted-size-known",
		"--predicted-files", "1", "--predicted-lines", "20",
		"--full"); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range got.Order {
		if got.Phases[phase].Status == project.PhaseSkipped {
			t.Fatalf("forced-full phase %q was skipped", phase)
		}
	}

	if _, err := runRoute(t, "escalate", "demo", "high-risk",
		"--reason", "security boundary discovered", "--risk", "auth-security"); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, "escalate", "demo", "high-risk",
		"--reason", "concurrency risk discovered", "--risk", "concurrency-distributed"); err != nil {
		t.Fatal(err)
	}
	got, err = project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Route.ForcedFull || got.Route.ValidationOwner != "validate" ||
		!slices.Contains(got.Route.Facts.RiskTriggers, project.RiskConcurrencyDistributed) {
		t.Fatalf("same-rank forced-full escalation = %+v", got.Route)
	}
	if _, err := runRoute(t, "escalate", "demo", "easy", "--reason", "downgrade"); err == nil {
		t.Fatal("route downgrade was accepted")
	}
}

func TestRouteEscalationInvalidatesAssessmentAfterSnapshotChange(t *testing.T) {
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
	if _, err := runRoute(t, "classify", "demo",
		"--requested-behavior-explicit",
		"--no-repository-gates",
		"--risk-assessment-complete",
		"--predicted-size-known",
		"--predicted-files", "1",
		"--predicted-lines", "20",
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, "escalate", "demo", "high-risk",
		"--reason", "new dependency risk",
		"--risk", "dependency-build-release",
	); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Route.Facts.RiskAssessmentComplete ||
		got.Route.Facts.AssessmentFingerprint != "" {
		t.Fatalf("stale assessment survived escalation: %+v", got.Route.Facts)
	}
}

func TestRouteRiskRevisionInvalidatesExistingEvidence(t *testing.T) {
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
	if _, err := runRoute(t, "classify", "demo",
		"--requested-behavior-explicit", "--gate", "test=go test ./...",
		"--risk-assessment-complete", "--predicted-size-known",
		"--predicted-files", "1", "--predicted-lines", "20",
	); err != nil {
		t.Fatal(err)
	}
	dispatchPhase(t, "demo", "route")
	if _, err := runState(t, "finish", "demo", "route", "done", "--outcome", "material"); err != nil {
		t.Fatal(err)
	}
	token := dispatchPhase(t, "demo", "implement")
	if _, err := runState(t, "evidence", "record", "demo", "review",
		"--result", "passed", "--role", "code-reviewer",
		"--dispatch-token", token); err != nil {
		t.Fatal(err)
	}
	before, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, "escalate", "demo", "high-risk",
		"--reason", "dependency provenance changed",
		"--risk", "dependency-build-release"); err != nil {
		t.Fatal(err)
	}
	after, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Route.Revision <= before.Route.Revision ||
		after.Route.Digest == before.Route.Digest {
		t.Fatalf("route revision did not change: before=%+v after=%+v", before.Route, after.Route)
	}
	if _, err := runState(t, "evidence", "fresh", "demo", "review"); err == nil {
		t.Fatal("pre-escalation review evidence remained fresh")
	}
	if after.Phases["review"].Status != project.PhasePending {
		t.Fatalf("new independent review was not reopened: %+v", after.Phases["review"])
	}
}

func TestRouteSnapshotRejectsTraversalSlug(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := runRoute(t, "snapshot", "../outside"); err == nil {
		t.Fatal("route snapshot accepted a traversal slug")
	}
}

func TestRouteSnapshotDoesNotRequireClassification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)

	out, err := runRoute(t, "snapshot", "demo")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot project.RepositorySnapshot
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Fingerprint == "" || snapshot.HeadSHA == "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestRouteClassifyPersistsConservativelyMergedFacts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteHighRisk)
	state.Route.Facts.RiskTriggers = []project.RiskTrigger{project.RiskAuthSecurity}
	state.Route.Facts.UnresolvedDecision = true
	refreshRouteDigest(t, state.Route)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}

	if _, err := runRoute(t, "classify", "demo",
		"--requested-behavior-explicit", "--gate", "test=go test ./...",
		"--risk-assessment-complete", "--predicted-size-known",
		"--predicted-files", "1", "--predicted-lines", "20",
	); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Route.Facts.UnresolvedDecision ||
		!slices.Contains(got.Route.Facts.RiskTriggers, project.RiskAuthSecurity) {
		t.Fatalf("classify discarded conservative facts: %+v", got.Route.Facts)
	}
}

func TestRouteClassifyReclassifiesConservativelyMergedFacts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}

	if _, err := runRoute(t, "classify", "demo",
		"--gate", "test=go test ./...", "--risk-assessment-complete",
		"--predicted-size-known", "--predicted-files", "1", "--predicted-lines", "20",
	); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Route.Class != project.RouteStandard || got.Route.Facts.RequestedBehaviorExplicit {
		t.Fatalf("merged uncertain facts did not escalate classification: %+v", got.Route)
	}
}

func TestRouteStackEscalationRequiresAndPersistsRationale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Route = testRouteDecision(project.RouteEasy)
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}

	if _, err := runRoute(t, "escalate", "demo", "stack-candidate",
		"--reason", "split is safer"); err == nil {
		t.Fatal("stack escalation without rationale succeeded")
	}
	if _, err := runRoute(t, "escalate", "demo", "stack-candidate",
		"--reason", "split is safer",
		"--stack-rationale", "API and implementation can ship independently"); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Route.StackRationale != "API and implementation can ship independently" ||
		got.Route.Facts.StackRationale != got.Route.StackRationale {
		t.Fatalf("stack rationale was not persisted: %+v", got.Route)
	}
}
