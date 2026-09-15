package project

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRouteFactsIgnoreRetiredHistoricalInputs(t *testing.T) {
	var facts RouteFacts
	if err := json.Unmarshal([]byte(`{
		"requested_behavior_explicit": true,
		"historical_review_no_op": true,
		"historical_validation_no_op": true,
		"historical_reduction_bps": 6000
	}`), &facts); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{
		"historical_review_no_op",
		"historical_validation_no_op",
		"historical_reduction_bps",
	} {
		if strings.Contains(string(data), retired) {
			t.Errorf("retired route input %q was persisted: %s", retired, data)
		}
	}
}

func validAdaptiveState(t *testing.T) WorkflowState {
	t.Helper()
	state, err := NewState("demo", "deliver-pr", AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := RepositorySnapshot{
		BaseSHA: "base", HeadSHA: "head", Fingerprint: "fingerprint",
		FileCount: 2, ChangedLines: 20,
	}
	reasons := make(map[string]string, len(AdaptiveDeliveryPhases))
	for _, phase := range AdaptiveDeliveryPhases {
		reasons[phase] = "selected by standard route"
	}
	state.Route = &RouteDecision{
		Class:             RouteStandard,
		SelectedPhases:    append([]string(nil), AdaptiveDeliveryPhases...),
		PhaseReasons:      reasons,
		ReviewRoles:       []string{ReviewRoleCodeReviewer},
		ReviewOwner:       "review",
		ValidationOwner:   "validate",
		Snapshot:          snapshot,
		PreviousClass:     RouteEasy,
		EscalationReasons: []string{"actual size exceeded easy threshold"},
		EvaluatedAt:       "2026-09-15T00:00:00Z",
		Facts: RouteFacts{
			RequestedBehaviorExplicit: true,
			GatePolicy: GatePolicy{
				Mode: GatePolicyRequired,
				Gates: []RequiredGate{{
					ID: "test", CommandDigest: commandDigest("go test ./..."),
					RedactedDisplay: "go <redacted-args>",
				}},
			},
			RiskAssessmentComplete: true,
			AssessmentFingerprint:  snapshot.Fingerprint,
			PredictedSizeKnown:     true,
			PredictedFileCount:     2,
			PredictedChangedLines:  20,
			ActualFileCount:        2,
			ActualChangedLines:     20,
		},
	}
	digest, err := RouteDigest(*state.Route)
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Revision = 1
	state.Route.Digest = digest
	return state
}

func TestAdaptiveStateRejectsStaleRiskAssessment(t *testing.T) {
	state := validAdaptiveState(t)
	state.Route.Facts.AssessmentFingerprint = "stale"
	if err := state.validate(); err == nil {
		t.Fatal("state accepted a risk assessment from another snapshot")
	}
}

func TestAdaptiveStateRejectsTamperedRouteDigest(t *testing.T) {
	state := validAdaptiveState(t)
	state.Route.Digest = strings.Repeat("b", 64)
	if err := state.validate(); err == nil {
		t.Fatal("state accepted a route whose digest did not match its semantics")
	}
}

func TestAdaptiveStateRejectsIncompleteEasyEligibility(t *testing.T) {
	tests := map[string]func(*RouteFacts){
		"size limit": func(facts *RouteFacts) {
			facts.ActualChangedLines = 151
		},
		"simplification": func(facts *RouteFacts) {
			facts.Duplication = true
		},
		"stack decomposition": func(facts *RouteFacts) {
			facts.StackDecomposition = true
			facts.StackRationale = "separate dependent changes"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			state := validAdaptiveState(t)
			state.Route.Class = RouteEasy
			state.Route.PreviousClass = ""
			state.Route.EscalationReasons = nil
			state.Route.SelectedPhases = []string{"route", "implement", "open-pr"}
			state.Route.ReviewOwner = EvidenceOwnerImplement
			state.Route.ValidationOwner = EvidenceOwnerImplement
			mutate(&state.Route.Facts)
			digest, err := RouteDigest(*state.Route)
			if err != nil {
				t.Fatal(err)
			}
			state.Route.Digest = digest
			if err := state.validate(); err == nil {
				t.Fatalf("easy route accepted %s violation", name)
			}
		})
	}
}

func TestDeliveryStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := validAdaptiveState(t)
	state.Evidence.Validation = &EvidenceRecord{
		Snapshot:      state.Route.Snapshot,
		RouteRevision: state.Route.Revision,
		RouteDigest:   state.Route.Digest,
		DispatchID:    "dispatch-1",
		Result:        EvidencePassed,
		Owner:         EvidenceOwnerValidate,
		Artifact:      "validation.md",
		CompletedAt:   "2026-09-15T00:05:00Z",
		Commands:      []CommandEvidence{testCommandEvidence("go test ./...", 0)},
	}
	state.SubagentCount = 2
	state.Phases["route"] = PhaseState{
		Status: PhaseDone, Outcome: PhaseOutcomeMaterial,
		StartedAt: "2026-09-15T00:00:00Z", EndedAt: "2026-09-15T00:01:00Z",
	}
	state.Phases["clarify"] = PhaseState{
		Status: PhaseDone, Outcome: PhaseOutcomeNoOp,
		EndedAt: "2026-09-15T00:01:00Z",
	}

	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	got.Updated = ""
	state.Updated = ""
	if !reflect.DeepEqual(got, state) {
		t.Fatalf("round-trip mismatch:\n got: %#v\nwant: %#v", got, state)
	}
}

func TestAdaptiveOpenedResultMustMatchRouteEvidenceDispatchAndPhase(t *testing.T) {
	state := validOpenedAdaptiveState(t)
	if err := state.validate(); err != nil {
		t.Fatalf("valid opened state rejected: %v", err)
	}
	tests := map[string]func(*WorkflowState){
		"route": func(candidate *WorkflowState) {
			candidate.FinalResult.RouteDigest = strings.Repeat("b", 64)
		},
		"snapshot": func(candidate *WorkflowState) {
			candidate.FinalResult.Snapshot.Fingerprint = "stale"
		},
		"dispatch": func(candidate *WorkflowState) {
			candidate.FinalResult.DispatchID = "older-dispatch"
		},
		"phase": func(candidate *WorkflowState) {
			candidate.Phases["open-pr"] = PhaseState{Status: PhaseInProgress}
		},
		"pr identity": func(candidate *WorkflowState) {
			candidate.PR.Number = 43
		},
		"review": func(candidate *WorkflowState) {
			candidate.Evidence.Review.Result = EvidenceBlocked
			candidate.Evidence.Review.BlockerCategory = "tooling"
			candidate.Evidence.Review.BlockerReason = "review unavailable"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := validOpenedAdaptiveState(t)
			mutate(&candidate)
			if err := candidate.validate(); err == nil {
				t.Fatal("inconsistent opened result was accepted")
			}
		})
	}
}

func TestRouteChangeClearsOpenedResult(t *testing.T) {
	state := validOpenedAdaptiveState(t)
	changed := *state.Route
	changed.Snapshot.Fingerprint = "changed"
	changed.Facts.AssessmentFingerprint = "changed"
	if err := state.ApplyRoute(changed); err != nil {
		t.Fatal(err)
	}
	if state.FinalResult != nil || state.PR != (PRRef{}) ||
		state.Phases["open-pr"].Status != PhaseEscalated {
		t.Fatalf("route change retained opened result: result=%+v pr=%+v phase=%+v",
			state.FinalResult, state.PR, state.Phases["open-pr"])
	}
}

func validOpenedAdaptiveState(t *testing.T) WorkflowState {
	t.Helper()
	state := validAdaptiveState(t)
	for _, name := range state.Route.SelectedPhases {
		state.Phases[name] = PhaseState{Status: PhaseDone}
	}
	snapshot := state.Route.Snapshot
	state.Evidence.Review = &EvidenceRecord{
		Snapshot: snapshot, RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
		DispatchID: "review-dispatch", Result: EvidencePassed, Owner: EvidenceOwnerReview,
		CompletedAt: "2026-09-15T00:05:00Z", Roles: append([]string(nil), state.Route.ReviewRoles...),
	}
	state.Evidence.Validation = &EvidenceRecord{
		Snapshot: snapshot, RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
		DispatchID: "validate-dispatch", Result: EvidencePassed, Owner: EvidenceOwnerValidate,
		CompletedAt: "2026-09-15T00:06:00Z",
		Commands:    []CommandEvidence{testCommandEvidence("go test ./...", 0)},
	}
	state.DispatchCount = 4
	state.HandoffCount = 3
	state.LastDispatch = "open-pr"
	state.LastDispatchID = "open-pr-dispatch"
	state.PR = PRRef{Number: 42, URL: "https://example.test/pull/42"}
	state.FinalResult = &FinalResult{
		Status: "opened", PRNumber: 42, PRURL: "https://example.test/pull/42",
		RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
		Snapshot: snapshot, DispatchID: state.LastDispatchID,
	}
	return state
}

func TestAdaptiveStateValidationRejectsMalformedRouteAndEvidence(t *testing.T) {
	tests := map[string]func(*WorkflowState){
		"unknown selected phase": func(state *WorkflowState) {
			state.Route.SelectedPhases = append(state.Route.SelectedPhases, "ship-it")
		},
		"unknown review role": func(state *WorkflowState) {
			state.Route.ReviewRoles = []string{"imaginary-reviewer"}
		},
		"invalid validation owner": func(state *WorkflowState) {
			state.Route.ValidationOwner = "review"
		},
		"invalid review owner": func(state *WorkflowState) {
			state.Route.ReviewOwner = "validate"
		},
		"unknown risk trigger": func(state *WorkflowState) {
			state.Route.Facts.RiskTriggers = []RiskTrigger{"unknown-risk"}
		},
		"negative route count": func(state *WorkflowState) {
			state.Route.Facts.ActualFileCount = -1
		},
		"passing failed command": func(state *WorkflowState) {
			state.Evidence.Validation = &EvidenceRecord{
				Snapshot: state.Route.Snapshot, Result: EvidencePassed,
				RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
				DispatchID: "dispatch-1", Owner: EvidenceOwnerValidate,
				CompletedAt: "2026-09-15T00:05:00Z",
				Commands:    []CommandEvidence{testCommandEvidence("go test ./...", 1)},
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			state := validAdaptiveState(t)
			mutate(&state)
			if err := state.validate(); err == nil {
				t.Fatalf("malformed adaptive state %q was accepted", name)
			}
		})
	}
}

func TestAdaptiveStateRejectsImplementOwnershipWhenValidateIsSelected(t *testing.T) {
	state := validAdaptiveState(t)
	state.Route.Class = RouteEasy
	state.Route.PreviousClass = ""
	state.Route.EscalationReasons = nil
	state.Route.ValidationOwner = "implement"
	if err := state.validate(); err == nil {
		t.Fatal("implement ownership with selected validate phase was accepted")
	}
}

func TestAdaptiveStateRejectsLessConservativePersistedRouteSemantics(t *testing.T) {
	tests := map[string]func(*WorkflowState){
		"class": func(state *WorkflowState) {
			state.Route.Class = RouteStandard
			state.Route.Facts.RiskTriggers = []RiskTrigger{RiskAuthSecurity}
		},
		"phase": func(state *WorkflowState) {
			state.Route.Class = RouteHighRisk
			state.Route.Facts.RiskTriggers = []RiskTrigger{RiskAuthSecurity}
			state.Route.SelectedPhases = []string{"route", "implement", "review", "validate", "open-pr"}
		},
		"role": func(state *WorkflowState) {
			state.Route.Class = RouteHighRisk
			state.Route.Facts.RiskTriggers = []RiskTrigger{RiskAuthSecurity}
			state.Route.ReviewRoles = []string{ReviewRoleCodeReviewer}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			state := validAdaptiveState(t)
			mutate(&state)
			digest, err := RouteDigest(*state.Route)
			if err != nil {
				t.Fatal(err)
			}
			state.Route.Digest = digest
			if err := state.validate(); err == nil {
				t.Fatal("less-conservative persisted route was accepted")
			}
		})
	}
}

func TestAdaptiveStateAcceptsCollapsedEasyOwnership(t *testing.T) {
	state := validAdaptiveState(t)
	state.Route.Class = RouteEasy
	state.Route.PreviousClass = ""
	state.Route.EscalationReasons = nil
	state.Route.SelectedPhases = []string{"route", "implement", "open-pr"}
	state.Route.ReviewOwner = "implement"
	state.Route.ValidationOwner = "implement"
	digest, err := RouteDigest(*state.Route)
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Digest = digest
	if err := state.validate(); err != nil {
		t.Fatalf("collapsed easy route was rejected: %v", err)
	}
}

func TestAdaptiveDispatchRequiresCurrentSelectedPhase(t *testing.T) {
	state, err := NewState("demo", "deliver-pr", AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.ValidateAdaptiveDispatch("implement"); err == nil {
		t.Fatal("adaptive dispatch before routing accepted implement")
	}
	if err := state.ValidateAdaptiveDispatch("route"); err != nil {
		t.Fatalf("route dispatch before classification: %v", err)
	}

	state = validAdaptiveState(t)
	state.Phases["route"] = PhaseState{Status: PhaseDone}
	if err := state.ValidateAdaptiveDispatch("plan"); err == nil {
		t.Fatal("later adaptive phase dispatched out of order")
	}
	if err := state.ValidateAdaptiveDispatch("clarify"); err != nil {
		t.Fatalf("current adaptive phase rejected: %v", err)
	}
	state.Phases["clarify"] = PhaseState{
		Status: PhaseBlocked,
		Reason: "author decision required",
	}
	if err := state.ValidateAdaptiveDispatch("clarify"); err != nil {
		t.Fatalf("blocked current phase could not be redispatched: %v", err)
	}

	state.Version = 0
	if err := state.ValidateAdaptiveDispatch("open-pr"); err == nil {
		t.Fatal("version-zero routed state bypassed adaptive dispatch ordering")
	}
}

func TestAdaptiveFinishRequiresActiveCurrentSelectedPhase(t *testing.T) {
	state := validAdaptiveState(t)
	state.Phases["route"] = PhaseState{Status: PhaseDone}

	for name, status := range map[string]string{
		"plan":    PhaseDone,
		"open-pr": PhaseSkipped,
	} {
		if err := state.ValidateAdaptiveFinish(name, status); err == nil {
			t.Fatalf("adaptive finish accepted %s as %s", name, status)
		}
	}
	if err := state.ValidateAdaptiveFinish("clarify", PhaseDone); err == nil {
		t.Fatal("adaptive finish accepted a pending phase")
	}
	state.Phases["clarify"] = PhaseState{
		Status: PhaseInProgress,
		Dispatch: &PhaseDispatch{
			ID: "clarify-dispatch", TokenHash: strings.Repeat("a", 64),
			RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
		},
	}
	if err := state.ValidateAdaptiveFinish("clarify", PhaseDone); err != nil {
		t.Fatalf("active current phase was rejected: %v", err)
	}
	if err := state.ValidateAdaptiveFinish("clarify", PhaseSkipped); err == nil {
		t.Fatal("selected adaptive phase was allowed to finish as skipped")
	}
}

func TestRouteChangeReopensActiveSelectedPhaseForRedispatch(t *testing.T) {
	state := validAdaptiveState(t)
	for _, name := range []string{"route", "clarify", "plan"} {
		state.Phases[name] = PhaseState{Status: PhaseDone}
	}
	state.Phases["implement"] = PhaseState{
		Status: PhaseInProgress,
		Dispatch: &PhaseDispatch{
			ID: "implement-dispatch", TokenHash: strings.Repeat("a", 64),
			RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
		},
	}
	changed := *state.Route
	changed.Snapshot.Fingerprint = "changed"
	changed.Facts.AssessmentFingerprint = "changed"
	if err := state.ApplyRoute(changed); err != nil {
		t.Fatal(err)
	}
	implement := state.Phases["implement"]
	if implement.Status != PhaseEscalated || implement.Dispatch != nil {
		t.Fatalf("implementation was not safely reopened: %+v", implement)
	}
	if err := state.ValidateAdaptiveDispatch("implement"); err != nil {
		t.Fatalf("reopened implementation is not redispatchable: %v", err)
	}
}

func TestOpenPRReadinessRequiresExactEvidenceAndActiveDispatch(t *testing.T) {
	state := validAdaptiveState(t)
	for _, phase := range state.Order {
		if phase == "open-pr" {
			break
		}
		state.Phases[phase] = PhaseState{Status: PhaseDone}
	}
	if err := state.ValidateOpenPRReadiness(state.Route.Snapshot, false); err == nil {
		t.Fatal("open-pr readiness accepted missing evidence")
	}
	state.Evidence.Review = &EvidenceRecord{
		Snapshot: state.Route.Snapshot, RouteRevision: state.Route.Revision,
		RouteDigest: state.Route.Digest, DispatchID: "review-dispatch",
		Result: EvidencePassed, Owner: state.Route.EffectiveReviewOwner(),
		CompletedAt: "2026-09-15T00:05:00Z",
		Roles:       append([]string(nil), state.Route.ReviewRoles...),
	}
	state.Evidence.Validation = &EvidenceRecord{
		Snapshot: state.Route.Snapshot, RouteRevision: state.Route.Revision,
		RouteDigest: state.Route.Digest, DispatchID: "validate-dispatch",
		Result: EvidencePassed, Owner: state.Route.ValidationOwner,
		CompletedAt: "2026-09-15T00:06:00Z",
		Commands:    []CommandEvidence{testCommandEvidence("go test ./...", 0)},
	}
	if err := state.ValidateOpenPRReadiness(state.Route.Snapshot, false); err != nil {
		t.Fatalf("fresh evidence did not allow open-pr dispatch: %v", err)
	}
	if err := state.ValidateOpenPRReadiness(state.Route.Snapshot, true); err == nil {
		t.Fatal("open-pr completion accepted without an active dispatch")
	}
	state.Phases["open-pr"] = PhaseState{
		Status: PhaseInProgress,
		Dispatch: &PhaseDispatch{
			ID: "open-pr-dispatch", TokenHash: strings.Repeat("a", 64),
			RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
		},
	}
	if err := state.ValidateOpenPRReadiness(state.Route.Snapshot, true); err != nil {
		t.Fatalf("fresh evidence and active dispatch rejected: %v", err)
	}
}

func TestAdaptiveStateRejectsReviewOwnerWithoutMatchingPhase(t *testing.T) {
	state := validAdaptiveState(t)
	state.Route.Class = RouteEasy
	state.Route.PreviousClass = ""
	state.Route.EscalationReasons = nil
	state.Route.SelectedPhases = []string{"route", "implement", "open-pr"}
	state.Route.ReviewOwner = "review"
	state.Route.ValidationOwner = "implement"
	if err := state.validate(); err == nil {
		t.Fatal("review-owned route without review phase was accepted")
	}
}

func TestVersionZeroStateWithAdaptiveFieldsUsesAdaptiveValidation(t *testing.T) {
	state := validAdaptiveState(t)
	state.Version = 0
	state.Route.ReviewRoles = []string{"imaginary-reviewer"}
	if err := state.validate(); err == nil {
		t.Fatal("version-zero state with adaptive fields bypassed adaptive validation")
	}
}

func TestExpandedRouteRolesDoNotInvalidateHistoricalReviewEvidence(t *testing.T) {
	state := validAdaptiveState(t)
	state.Route.ReviewRoles = []string{ReviewRoleCodeReviewer, ReviewRoleSecurity}
	digest, err := RouteDigest(*state.Route)
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Digest = digest
	state.Evidence.Review = &EvidenceRecord{
		Snapshot: state.Route.Snapshot, Result: EvidencePassed,
		RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
		DispatchID: "dispatch-1", Owner: EvidenceOwnerReview,
		CompletedAt: "2026-09-15T00:05:00Z",
		Roles:       []string{ReviewRoleCodeReviewer},
	}
	if err := state.validate(); err != nil {
		t.Fatalf("historical review evidence made state unloadable: %v", err)
	}
	if state.Evidence.Review.FreshForReview(state.Route.Snapshot, state.Route.ReviewRoles) {
		t.Fatal("historical review evidence incorrectly covers newly selected roles")
	}
}

func TestChangedGatePolicyDoesNotInvalidateHistoricalValidationState(t *testing.T) {
	state := validAdaptiveState(t)
	state.Evidence.Validation = &EvidenceRecord{
		Snapshot: state.Route.Snapshot, Result: EvidencePassed,
		RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
		DispatchID: "dispatch-1", Owner: EvidenceOwnerValidate,
		CompletedAt: "2026-09-15T00:05:00Z",
		Commands:    []CommandEvidence{testCommandEvidence("go test ./...", 0)},
	}
	state.Route.Facts.GatePolicy.Gates = append(
		state.Route.Facts.GatePolicy.Gates,
		RequiredGate{
			ID: "lint", CommandDigest: commandDigest("make lint"),
			RedactedDisplay: "make <redacted-args>",
		},
	)
	state.Route.Revision++
	digest, err := RouteDigest(*state.Route)
	if err != nil {
		t.Fatal(err)
	}
	state.Route.Digest = digest

	if err := state.validate(); err != nil {
		t.Fatalf("historical validation evidence made state unloadable: %v", err)
	}
	if state.Evidence.Validation.FreshForValidationRoute(
		state.Route.Snapshot,
		*state.Route,
		state.Route.ValidationOwner,
	) {
		t.Fatal("historical validation evidence incorrectly covers the changed gate policy")
	}
}

func TestSkippedIsTerminalButBlockedAndEscalatedAreCurrent(t *testing.T) {
	state, err := NewState("demo", "deliver-pr", []string{"route", "clarify", "implement"})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetPhase("route", PhaseDone, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := state.SetPhaseWithDelivery("clarify", PhaseSkipped, "explicit request", "", "", PhaseOutcomeNoOp, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := state.Next(); got != "implement" {
		t.Fatalf("Next() = %q, want implement", got)
	}
	if err := state.SetPhaseWithDelivery("implement", PhaseBlocked, "missing dependency", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := state.Next(); got != "implement" {
		t.Fatalf("Next() = %q, want blocked implement", got)
	}
	if err := state.SetPhaseWithDelivery("implement", PhaseEscalated, "failed gate", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := state.Current(); got != "implement" {
		t.Fatalf("Current() = %q, want escalated implement", got)
	}
}

func TestReasonRequiredForNonSuccessTerminalStates(t *testing.T) {
	state, err := NewState("demo", "deliver-pr", []string{"route"})
	if err != nil {
		t.Fatal(err)
	}

	for _, status := range []string{PhaseSkipped, PhaseBlocked, PhaseEscalated} {
		if err := state.SetPhaseWithDelivery("route", status, "", "", "", "", "", ""); err == nil {
			t.Errorf("status %q accepted without a reason", status)
		}
	}
}

func TestSetPhaseReopenClearsTerminalMetadata(t *testing.T) {
	state, err := NewState("demo", "deliver-pr", []string{"implement"})
	if err != nil {
		t.Fatal(err)
	}
	state.Phases["implement"] = PhaseState{
		Status: PhaseDone, Artifact: "implementation.md", Task: "4/7",
		Reason: "old result", Outcome: PhaseOutcomeMaterial,
		StartedAt: "2026-09-15T00:00:00Z", EndedAt: "2026-09-15T00:05:00Z",
	}
	if err := state.SetPhase("implement", PhaseInProgress, "", ""); err != nil {
		t.Fatal(err)
	}
	reopened := state.Phases["implement"]
	if reopened.Reason != "" || reopened.Outcome != "" ||
		reopened.StartedAt != "" || reopened.EndedAt != "" ||
		reopened.Artifact != "implementation.md" || reopened.Task != "4/7" {
		t.Fatalf("reopened phase retained terminal metadata: %+v", reopened)
	}
}

func TestSaveStateRejectsInvalidRouteFactsBeforeWriting(t *testing.T) {
	state := validAdaptiveState(t)
	state.Route.Facts.RiskTriggers = []RiskTrigger{RiskAuthSecurity, RiskAuthSecurity}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := SaveState(path, state); err == nil {
		t.Fatal("SaveState accepted duplicate route risks")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid state was written: %v", err)
	}
}

func TestPhaseReopenClearsTerminalMetadataAndEnforcesOutcomeInvariants(t *testing.T) {
	state, err := NewState("demo", "deliver-pr", []string{"implement"})
	if err != nil {
		t.Fatal(err)
	}
	state.Phases["implement"] = PhaseState{
		Status: PhaseDone, Artifact: "old.md", Task: "old task",
		Outcome: PhaseOutcomeMaterial, StartedAt: "start", EndedAt: "end",
	}
	if err := state.SetPhaseWithDelivery(
		"implement", PhaseInProgress, "", "", "new task", "", "new start", "",
	); err != nil {
		t.Fatal(err)
	}
	got := state.Phases["implement"]
	if got.Reason != "" || got.Outcome != "" || got.EndedAt != "" ||
		got.Artifact != "" || got.Task != "new task" || got.StartedAt != "new start" {
		t.Fatalf("reopened phase retained terminal metadata: %+v", got)
	}

	for name, test := range map[string]struct {
		status  string
		outcome string
	}{
		"pending material":  {PhasePending, PhaseOutcomeMaterial},
		"in progress no-op": {PhaseInProgress, PhaseOutcomeNoOp},
		"blocked material":  {PhaseBlocked, PhaseOutcomeMaterial},
		"escalated no-op":   {PhaseEscalated, PhaseOutcomeNoOp},
		"skipped material":  {PhaseSkipped, PhaseOutcomeMaterial},
	} {
		t.Run(name, func(t *testing.T) {
			reason := ""
			if requiresReason(test.status) {
				reason = "test reason"
			}
			if err := state.SetPhaseWithDelivery(
				"implement", test.status, reason, "", "", test.outcome, "", "",
			); err == nil {
				t.Fatalf("status %q accepted outcome %q", test.status, test.outcome)
			}
		})
	}
}

func TestAdvanceRefusesBlockedAndEscalatedPhases(t *testing.T) {
	for _, status := range []string{PhaseBlocked, PhaseEscalated} {
		t.Run(status, func(t *testing.T) {
			state, err := NewState("demo", "deliver-pr", []string{"implement"})
			if err != nil {
				t.Fatal(err)
			}
			state.Phases["implement"] = PhaseState{Status: status, Reason: "needs resolution"}
			if _, err := state.Advance(); err == nil {
				t.Fatalf("Advance accepted unresolved status %q", status)
			}
		})
	}
}

func TestEvidenceFreshRequiresPassingExactSnapshot(t *testing.T) {
	snapshot := RepositorySnapshot{BaseSHA: "base", HeadSHA: "head", Fingerprint: "one"}
	route := RouteDecision{Revision: 1, Digest: strings.Repeat("a", 64)}
	record := EvidenceRecord{
		Snapshot: snapshot, RouteRevision: 1, RouteDigest: route.Digest,
		DispatchID: "dispatch-1", Result: EvidencePassed, Owner: "implement",
	}
	if !record.Fresh(snapshot) {
		t.Fatal("passing evidence for the exact snapshot is stale")
	}
	if !record.FreshForOwner(snapshot, "implement") {
		t.Fatal("evidence from the canonical owner is stale")
	}
	if record.FreshForOwner(snapshot, "review") {
		t.Fatal("evidence from the wrong owner reported fresh")
	}
	if !record.FreshForRoute(snapshot, route, "implement") {
		t.Fatal("evidence for the current route revision is stale")
	}
	route.Revision++
	if record.FreshForRoute(snapshot, route, "implement") {
		t.Fatal("evidence remained fresh after a route revision")
	}
	route.Revision--
	changed := snapshot
	changed.Fingerprint = "two"
	if record.Fresh(changed) {
		t.Fatal("evidence remained fresh after a snapshot change")
	}
	record.Result = EvidenceFailed
	if record.Fresh(snapshot) {
		t.Fatal("failed evidence reported fresh")
	}
	record.Result = EvidencePassed
	record.Commands = []CommandEvidence{testCommandEvidence("go test ./...", 1)}
	if record.Fresh(snapshot) {
		t.Fatal("passing evidence with a failed command reported fresh")
	}

	record.Commands = nil
	record.Findings.Important = 1
	if record.Fresh(snapshot) {
		t.Fatal("passing evidence with a blocking finding reported fresh")
	}
	record.Findings.Important = 0
	changedInputs := snapshot
	changedInputs.InputRevision = "changed-input-revision"
	if record.Fresh(changedInputs) {
		t.Fatal("evidence remained fresh after project inputs changed")
	}
}

func TestGatePolicyNormalizationAndRouteDigest(t *testing.T) {
	testDigest := commandDigest("go test ./...")
	lintDigest := commandDigest("make lint")
	left := GatePolicy{
		Mode: GatePolicyRequired,
		Gates: []RequiredGate{
			{ID: "test", CommandDigest: testDigest, RedactedDisplay: "go <redacted-args>"},
			{ID: "lint", CommandDigest: lintDigest, RedactedDisplay: "make <redacted-args>"},
		},
	}
	right := GatePolicy{
		Mode: GatePolicyRequired,
		Gates: []RequiredGate{
			{ID: "lint", CommandDigest: lintDigest, RedactedDisplay: "make <redacted-args>"},
			{ID: "test", CommandDigest: testDigest, RedactedDisplay: "go <redacted-args>"},
		},
	}
	normalizedLeft, err := NormalizeGatePolicy(left)
	if err != nil {
		t.Fatal(err)
	}
	normalizedRight, err := NormalizeGatePolicy(right)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalizedLeft, normalizedRight) {
		t.Fatalf("equivalent policies normalized differently: %#v != %#v", normalizedLeft, normalizedRight)
	}

	decision := validAdaptiveState(t).Route
	decision.Facts.GatePolicy = left
	first, err := RouteDigest(*decision)
	if err != nil {
		t.Fatal(err)
	}
	decision.Facts.GatePolicy = right
	second, err := RouteDigest(*decision)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equivalent gate ordering changed digest: %s != %s", first, second)
	}
	decision.Facts.GatePolicy.Gates[0].ID = "staticcheck"
	changed, err := RouteDigest(*decision)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("changed gate id did not change route digest")
	}
}

func TestValidationEvidenceRequiresExactGatePolicy(t *testing.T) {
	policy := GatePolicy{
		Mode: GatePolicyRequired,
		Gates: []RequiredGate{
			{ID: "lint", CommandDigest: commandDigest("make lint"), RedactedDisplay: "make <redacted-args>"},
			{ID: "test", CommandDigest: commandDigest("go test ./..."), RedactedDisplay: "go <redacted-args>"},
		},
	}
	base := EvidenceRecord{
		Snapshot:      RepositorySnapshot{BaseSHA: "base", HeadSHA: "head", Fingerprint: "fingerprint"},
		RouteRevision: 1, RouteDigest: strings.Repeat("a", 64), DispatchID: "dispatch",
		Result: EvidencePassed, Owner: EvidenceOwnerValidate, CompletedAt: "2026-09-15T00:00:00Z",
		Commands: []CommandEvidence{
			{GateID: "lint", Display: "make <redacted-args>", Digest: commandDigest("make lint"), ExitStatus: 0},
			{GateID: "test", Display: "go <redacted-args>", Digest: commandDigest("go test ./..."), ExitStatus: 0},
		},
	}
	if err := ValidateValidationEvidence(base, policy); err != nil {
		t.Fatalf("exact gate evidence rejected: %v", err)
	}
	tests := map[string]func(*EvidenceRecord){
		"missing": func(record *EvidenceRecord) { record.Commands = record.Commands[:1] },
		"extra": func(record *EvidenceRecord) {
			record.Commands = append(record.Commands, CommandEvidence{
				GateID: "build", Display: "go <redacted-args>",
				Digest: commandDigest("go build ./..."), ExitStatus: 0,
			})
		},
		"duplicate": func(record *EvidenceRecord) { record.Commands[1].GateID = "lint" },
		"wrong id":  func(record *EvidenceRecord) { record.Commands[1].GateID = "unit" },
		"wrong digest": func(record *EvidenceRecord) {
			record.Commands[1].Digest = commandDigest("go test ./internal/...")
		},
		"failed": func(record *EvidenceRecord) { record.Commands[1].ExitStatus = 1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			record := base
			record.Commands = append([]CommandEvidence(nil), base.Commands...)
			mutate(&record)
			if err := ValidateValidationEvidence(record, policy); err == nil {
				t.Fatal("invalid gate evidence was accepted")
			}
		})
	}
	missing := base
	missing.Commands = missing.Commands[:1]
	if err := ValidateValidationEvidence(missing, policy); err == nil ||
		!strings.Contains(err.Error(), "missing required gates: test") {
		t.Fatalf("missing gate error = %v", err)
	}
	noGates := base
	noGates.Commands = nil
	noGates.NoGates = true
	if err := ValidateValidationEvidence(noGates, policy); err == nil {
		t.Fatal("required policy accepted no-gates evidence")
	}
	if err := ValidateValidationEvidence(noGates, GatePolicy{Mode: GatePolicyNone}); err != nil {
		t.Fatalf("verified no-gates policy rejected: %v", err)
	}
	if err := ValidateValidationEvidence(noGates, GatePolicy{}); err == nil {
		t.Fatal("unknown gate policy accepted no-gates evidence")
	}
}

func TestBlockedValidationEvidenceCanOmitGateExecution(t *testing.T) {
	record := EvidenceRecord{
		Snapshot: RepositorySnapshot{
			BaseSHA: "base", HeadSHA: "head", Fingerprint: "fingerprint",
		},
		RouteRevision: 1, RouteDigest: strings.Repeat("a", 64),
		DispatchID: "dispatch", Result: EvidenceBlocked, Owner: EvidenceOwnerValidate,
		BlockerCategory: "input", BlockerReason: "required credentials are unavailable",
		CompletedAt: "2026-09-15T00:00:00Z",
	}
	for _, policy := range []GatePolicy{
		{},
		{Mode: GatePolicyNone},
		{
			Mode: GatePolicyRequired,
			Gates: []RequiredGate{{
				ID: "test", CommandDigest: commandDigest("go test ./..."),
				RedactedDisplay: "go <redacted-args>",
			}},
		},
	} {
		if err := ValidateValidationEvidence(record, policy); err != nil {
			t.Fatalf("blocked validation with policy %+v was rejected: %v", policy, err)
		}
	}
}

func TestBlockedEvidenceCannotMaskFailures(t *testing.T) {
	review := EvidenceRecord{
		Snapshot: RepositorySnapshot{
			BaseSHA: "base", HeadSHA: "head", Fingerprint: "fingerprint",
		},
		RouteRevision: 1, RouteDigest: strings.Repeat("a", 64),
		DispatchID: "dispatch", Result: EvidenceBlocked, Owner: EvidenceOwnerReview,
		BlockerCategory: "authentication", BlockerReason: "review service unavailable",
		CompletedAt: "2026-09-15T00:00:00Z",
		Roles:       []string{ReviewRoleCodeReviewer},
		Findings:    FindingCounts{Important: 1},
	}
	if err := ValidateEvidence(EvidenceKindReview, review, nil); err == nil {
		t.Fatal("blocked review masked an Important finding")
	}

	validation := EvidenceRecord{
		Snapshot:      review.Snapshot,
		RouteRevision: 1, RouteDigest: strings.Repeat("a", 64),
		DispatchID: "dispatch", Result: EvidenceBlocked, Owner: EvidenceOwnerValidate,
		BlockerCategory: "authentication", BlockerReason: "artifact registry unavailable",
		CompletedAt: "2026-09-15T00:00:00Z",
		Commands:    []CommandEvidence{testCommandEvidence("go test ./...", 1)},
	}
	if err := ValidateEvidence(EvidenceKindValidation, validation, nil); err == nil {
		t.Fatal("blocked validation masked a failed gate")
	}
}

func testCommandEvidence(command string, exitStatus int) CommandEvidence {
	sum := sha256.Sum256([]byte(command))
	return CommandEvidence{
		GateID: "test", Display: "go <redacted-args>",
		Digest: fmt.Sprintf("%x", sum), ExitStatus: exitStatus,
	}
}

func commandDigest(command string) string {
	sum := sha256.Sum256([]byte(command))
	return fmt.Sprintf("%x", sum)
}

func TestRouteApplicationIsMonotonicAndPreservesCompletedPhases(t *testing.T) {
	state, err := NewState("demo", "deliver-pr", AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Version = 0
	if err := state.SetPhase("implement", PhaseDone, "implementation.md", ""); err != nil {
		t.Fatal(err)
	}
	easy := RouteDecision{
		Class: RouteEasy, SelectedPhases: []string{"route", "implement", "open-pr"},
		ReviewOwner: "implement", ValidationOwner: "implement",
		PhaseReasons: map[string]string{
			"clarify": "explicit", "plan": "small", "simplify": "simple",
			"review": "implement owns review", "validate": "implement owns validation",
		},
	}
	if err := state.ApplyRoute(easy); err != nil {
		t.Fatal(err)
	}
	if state.Version != WorkflowStateVersion {
		t.Fatalf("route application left state version %d", state.Version)
	}
	if state.Phases["implement"].Artifact != "implementation.md" || state.Phases["implement"].Status != PhaseDone {
		t.Fatalf("completed phase changed: %+v", state.Phases["implement"])
	}
	if state.Phases["clarify"].Status != PhaseSkipped {
		t.Fatalf("clarify status = %q, want skipped", state.Phases["clarify"].Status)
	}
	standard := RouteDecision{
		Class: RouteStandard, PreviousClass: RouteEasy,
		SelectedPhases:    []string{"route", "clarify", "implement", "review", "validate", "open-pr"},
		ReviewOwner:       "review",
		ValidationOwner:   "validate",
		PhaseReasons:      map[string]string{"plan": "not needed", "simplify": "simple"},
		EscalationReasons: []string{"actual size exceeded easy threshold"},
	}
	if err := state.ApplyRoute(standard); err != nil {
		t.Fatal(err)
	}
	if state.Phases["clarify"].Status != PhasePending {
		t.Fatalf("selected previously skipped phase = %q, want pending", state.Phases["clarify"].Status)
	}
	if err := state.ApplyRoute(easy); err == nil {
		t.Fatal("route downgrade was accepted")
	}
}

func TestRouteApplicationPreservesUnresolvedPhaseState(t *testing.T) {
	state, err := NewState("demo", "deliver-pr", AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	state.Phases["validate"] = PhaseState{
		Status: PhaseEscalated,
		Reason: "validation failed",
	}
	decision := RouteDecision{
		Class:          RouteEasy,
		SelectedPhases: []string{"route", "implement", "open-pr"},
		ReviewOwner:    "implement", ValidationOwner: "implement",
		PhaseReasons: map[string]string{
			"clarify": "explicit", "plan": "small", "simplify": "simple",
			"review": "implement owns review", "validate": "implement owns validation",
		},
	}
	if err := state.ApplyRoute(decision); err != nil {
		t.Fatal(err)
	}
	if got := state.Phases["validate"]; got.Status != PhaseEscalated || got.Reason != "validation failed" {
		t.Fatalf("unresolved validation was rewritten: %+v", got)
	}
}
