// Package route implements deterministic delivery classification and phase
// selection from normalized task and repository facts.
package route

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/project"
)

const (
	ClassEasy           = project.RouteEasy
	ClassStandard       = project.RouteStandard
	ClassHighRisk       = project.RouteHighRisk
	ClassStackCandidate = project.RouteStackCandidate

	RiskPublicContract         = project.RiskPublicContract
	RiskPersistenceMigration   = project.RiskPersistenceMigration
	RiskAuthSecurity           = project.RiskAuthSecurity
	RiskConcurrencyDistributed = project.RiskConcurrencyDistributed
	RiskDependencyBuildRelease = project.RiskDependencyBuildRelease
	RiskGeneratedArtifact      = project.RiskGeneratedArtifact
	RiskDestructiveOperation   = project.RiskDestructiveOperation
	RiskUnresolvedReviewCI     = project.RiskUnresolvedReviewCI
	RiskFailedGate             = project.RiskFailedGate
)

// AllPhases is the canonical phase order for adaptive delivery.
var AllPhases = append([]string(nil), project.AdaptiveDeliveryPhases...)

// RiskTrigger is one closed safety trigger that prevents easy routing.
type RiskTrigger = project.RiskTrigger

// Input contains normalized routing facts and no transcript content.
type Input = project.RouteFacts

// Decision is the durable routing decision.
type Decision = project.RouteDecision

// AllRiskTriggers returns the closed trigger set in stable order.
func AllRiskTriggers() []RiskTrigger {
	return []RiskTrigger{
		RiskPublicContract,
		RiskPersistenceMigration,
		RiskAuthSecurity,
		RiskConcurrencyDistributed,
		RiskDependencyBuildRelease,
		RiskGeneratedArtifact,
		RiskDestructiveOperation,
		RiskUnresolvedReviewCI,
		RiskFailedGate,
	}
}

// Classify returns the route selected by the normalized facts.
func Classify(input Input) (Decision, error) {
	input.RiskTriggers = orderedUnion(AllRiskTriggers(), nil, input.RiskTriggers)
	input = project.NormalizeRiskAssessment(input, "")
	normalizedPolicy, err := project.NormalizeGatePolicy(input.GatePolicy)
	if err != nil {
		return Decision{}, err
	}
	if normalizedPolicy.Mode == project.GatePolicyUnknown {
		return Decision{}, fmt.Errorf(
			"repository gate policy is unknown; pass --gate id=command or --no-repository-gates",
		)
	}
	input.GatePolicy = normalizedPolicy
	if err := validateInput(input); err != nil {
		return Decision{}, err
	}
	class := project.MinimumRouteClass(input)
	selected := project.RequiredRoutePhases(class, input, input.AuthorRequestedFullWorkflow)
	decision := Decision{
		Class:           class,
		ForcedFull:      input.AuthorRequestedFullWorkflow,
		SelectedPhases:  selected,
		PhaseReasons:    phaseReasons(class, selected, input),
		ReviewRoles:     project.RequiredReviewRoles(input),
		ReviewOwner:     project.EvidenceOwnerReview,
		ValidationOwner: project.EvidenceOwnerValidate,
		StackRationale:  strings.TrimSpace(input.StackRationale),
		EvaluatedAt:     time.Now().UTC().Format(time.RFC3339),
		Facts:           input,
	}
	if class == ClassEasy && !input.AuthorRequestedFullWorkflow {
		decision.ReviewOwner = project.EvidenceOwnerImplement
		decision.ValidationOwner = project.EvidenceOwnerImplement
	}
	return decision, nil
}

// PreserveMonotonic prevents a fresh classification from automatically
// downgrading a route after work has begun.
func PreserveMonotonic(previous, next Decision, reason string) (Decision, error) {
	if !validClass(previous.Class) {
		return Decision{}, fmt.Errorf("invalid previous route class %q", previous.Class)
	}
	next.Facts = mergeFacts(previous.Facts, next.Facts)
	next.ForcedFull = previous.ForcedFull || next.ForcedFull ||
		next.Facts.AuthorRequestedFullWorkflow
	if factsClass := project.MinimumRouteClass(next.Facts); project.RouteRank(factsClass) > project.RouteRank(next.Class) {
		next.Class = factsClass
	}
	next.SelectedPhases = project.RequiredRoutePhases(next.Class, next.Facts, next.ForcedFull)
	next.PhaseReasons = phaseReasons(next.Class, next.SelectedPhases, next.Facts)
	next.ReviewRoles = project.RequiredReviewRoles(next.Facts)
	next.ReviewOwner = project.EvidenceOwnerReview
	next.ValidationOwner = project.EvidenceOwnerValidate
	if next.Class == ClassEasy && !next.ForcedFull {
		next.ReviewOwner = project.EvidenceOwnerImplement
		next.ValidationOwner = project.EvidenceOwnerImplement
	}
	next.StackRationale = strings.TrimSpace(next.Facts.StackRationale)
	refreshedSelected := append([]string(nil), next.SelectedPhases...)
	next.SelectedPhases = orderedUnion(AllPhases, previous.SelectedPhases, next.SelectedPhases)
	next.ReviewRoles = orderedUnion(allReviewRoles(), previous.ReviewRoles, next.ReviewRoles)
	if slices.Contains(next.SelectedPhases, "review") {
		next.ReviewOwner = project.EvidenceOwnerReview
	}
	if slices.Contains(next.SelectedPhases, "validate") {
		next.ValidationOwner = project.EvidenceOwnerValidate
	}
	next.EscalationReasons = append([]string(nil), previous.EscalationReasons...)

	previousRank, nextRank := project.RouteRank(previous.Class), project.RouteRank(next.Class)
	switch {
	case nextRank < previousRank:
		next.PreviousClass = previous.PreviousClass
		next.Class = previous.Class
		next.PhaseReasons = mergePhaseReasons(
			previous.PhaseReasons,
			next.PhaseReasons,
			previous.SelectedPhases,
			refreshedSelected,
		)
		next.ReviewOwner = previous.EffectiveReviewOwner()
		next.ValidationOwner = previous.ValidationOwner
		next.EscalationReasons = appendReason(next.EscalationReasons, reason)
	case nextRank == previousRank:
		next.PreviousClass = previous.PreviousClass
		next.PhaseReasons = mergePhaseReasons(
			previous.PhaseReasons,
			next.PhaseReasons,
			previous.SelectedPhases,
			refreshedSelected,
		)
		next.EscalationReasons = appendReason(next.EscalationReasons, reason)
	default:
		next.PreviousClass = previous.Class
		next.EscalationReasons = appendReason(next.EscalationReasons, reason)
	}
	return next, nil
}

// Escalate explicitly moves a decision to a more conservative class.
func Escalate(current Decision, target, reason string, trigger RiskTrigger) (Decision, error) {
	if !validClass(target) {
		return Decision{}, fmt.Errorf("invalid target route class %q", target)
	}
	currentRank, targetRank := project.RouteRank(current.Class), project.RouteRank(target)
	if targetRank < currentRank {
		return Decision{}, fmt.Errorf("route escalation must become more conservative than %q", current.Class)
	}
	if strings.TrimSpace(reason) == "" {
		return Decision{}, fmt.Errorf("route escalation requires a reason")
	}
	if trigger != "" && !slices.Contains(AllRiskTriggers(), trigger) {
		return Decision{}, fmt.Errorf("invalid risk trigger %q", trigger)
	}
	newRisk := trigger != "" && !slices.Contains(current.Facts.RiskTriggers, trigger)
	newReason := !slices.Contains(current.EscalationReasons, strings.TrimSpace(reason))
	if targetRank == currentRank && !newRisk && !newReason {
		return Decision{}, fmt.Errorf("same-rank escalation requires a new risk or reason")
	}
	if newRisk {
		current.Facts.RiskTriggers = append(current.Facts.RiskTriggers, trigger)
	}
	next, err := Classify(current.Facts)
	if err != nil {
		return Decision{}, err
	}
	next.Class = target
	next.ForcedFull = current.ForcedFull
	next.SelectedPhases = project.RequiredRoutePhases(target, current.Facts, current.ForcedFull)
	next.PhaseReasons = phaseReasons(target, next.SelectedPhases, current.Facts)
	next.ReviewRoles = project.RequiredReviewRoles(current.Facts)
	next.ReviewOwner = project.EvidenceOwnerReview
	next.ValidationOwner = project.EvidenceOwnerValidate
	return PreserveMonotonic(current, next, reason)
}

func validateInput(input Input) error {
	for _, value := range []struct {
		name  string
		count int
	}{
		{"predicted file count", input.PredictedFileCount},
		{"predicted changed-line count", input.PredictedChangedLines},
		{"actual file count", input.ActualFileCount},
		{"actual changed-line count", input.ActualChangedLines},
	} {
		if value.count < 0 {
			return fmt.Errorf("%s cannot be negative", value.name)
		}
	}
	if _, err := project.NormalizeGatePolicy(input.GatePolicy); err != nil {
		return err
	}
	for _, trigger := range input.RiskTriggers {
		if !slices.Contains(AllRiskTriggers(), trigger) {
			return fmt.Errorf("invalid risk trigger %q", trigger)
		}
	}
	if input.StackDecomposition && strings.TrimSpace(input.StackRationale) == "" {
		return fmt.Errorf("stack decomposition requires a rationale")
	}
	return nil
}

func phaseReasons(class string, selected []string, input Input) map[string]string {
	reasons := genericPhaseReasons(class, selected)
	if input.AuthorRequestedFullWorkflow {
		for _, phase := range AllPhases {
			reasons[phase] = "author requested the full workflow"
		}
		return reasons
	}
	reasons["clarify"] = selectedReason(
		selected,
		"clarify",
		"requirements need clarification",
		"requirements are explicit",
	)
	reasons["plan"] = selectedReason(
		selected,
		"plan",
		"design or change breadth requires a plan",
		"no separate design decision is required",
	)
	reasons["simplify"] = selectedReason(
		selected,
		"simplify",
		"complexity evidence triggered simplification",
		"the planned diff has no simplification trigger",
	)
	if class == ClassEasy {
		reasons["review"] = "implement owns proportional review on the easy route"
		reasons["validate"] = "implement owns final gates on the easy route"
	}
	return reasons
}

func genericPhaseReasons(class string, selected []string) map[string]string {
	reasons := make(map[string]string, len(AllPhases))
	for _, phase := range AllPhases {
		if slices.Contains(selected, phase) {
			reasons[phase] = fmt.Sprintf("%s route selected %s", class, phase)
		} else {
			reasons[phase] = fmt.Sprintf("%s route does not require %s", class, phase)
		}
	}
	return reasons
}

func selectedReason(selected []string, phase, yes, no string) string {
	if slices.Contains(selected, phase) {
		return yes
	}
	return no
}

func validClass(class string) bool {
	return slices.Contains(
		[]string{ClassEasy, ClassStandard, ClassHighRisk, ClassStackCandidate},
		class,
	)
}

func allReviewRoles() []string {
	return []string{
		project.ReviewRoleCodeReviewer,
		project.ReviewRoleSilentFailureHunter,
		project.ReviewRoleTypeDesignAnalyzer,
		project.ReviewRolePRTestAnalyzer,
		project.ReviewRoleCommentAnalyzer,
		project.ReviewRoleSecurity,
		project.ReviewRoleGitHistory,
		project.ReviewRolePriorPRHistory,
	}
}

func orderedUnion[T comparable](order, first, second []T) []T {
	present := make(map[T]bool, len(first)+len(second))
	for _, value := range append(append([]T(nil), first...), second...) {
		present[value] = true
	}
	result := make([]T, 0, len(present))
	for _, value := range order {
		if present[value] {
			result = append(result, value)
			delete(present, value)
		}
	}
	for _, values := range [][]T{first, second} {
		for _, value := range values {
			if present[value] {
				result = append(result, value)
				delete(present, value)
			}
		}
	}
	return result
}

func mergeFacts(previous, next Input) Input {
	next.RequestedBehaviorExplicit = previous.RequestedBehaviorExplicit &&
		next.RequestedBehaviorExplicit
	next.UnresolvedDecision = previous.UnresolvedDecision || next.UnresolvedDecision
	if next.GatePolicy.Mode == "" {
		next.GatePolicy = previous.GatePolicy
	}
	if next.AssessmentFingerprint == previous.AssessmentFingerprint {
		next.RiskAssessmentComplete = previous.RiskAssessmentComplete &&
			next.RiskAssessmentComplete
	}
	next.PredictedSizeKnown = previous.PredictedSizeKnown && next.PredictedSizeKnown
	next.PredictedFileCount = max(previous.PredictedFileCount, next.PredictedFileCount)
	next.PredictedChangedLines = max(
		previous.PredictedChangedLines,
		next.PredictedChangedLines,
	)
	next.ActualFileCount = max(previous.ActualFileCount, next.ActualFileCount)
	next.ActualChangedLines = max(previous.ActualChangedLines, next.ActualChangedLines)
	next.StackDecomposition = previous.StackDecomposition || next.StackDecomposition
	if next.StackRationale == "" {
		next.StackRationale = previous.StackRationale
	}
	next.AuthorRequestedFullWorkflow = previous.AuthorRequestedFullWorkflow ||
		next.AuthorRequestedFullWorkflow
	next.AuthorRequestedSimplification = previous.AuthorRequestedSimplification ||
		next.AuthorRequestedSimplification
	next.Duplication = previous.Duplication || next.Duplication
	next.GeneratedChurn = previous.GeneratedChurn || next.GeneratedChurn
	next.ReviewRequestedCleanup = previous.ReviewRequestedCleanup ||
		next.ReviewRequestedCleanup
	next.ChangesTests = previous.ChangesTests || next.ChangesTests
	next.ChangesDocumentationComments = previous.ChangesDocumentationComments ||
		next.ChangesDocumentationComments
	next.ChangesTypeDesign = previous.ChangesTypeDesign || next.ChangesTypeDesign
	next.HistorySensitive = previous.HistorySensitive || next.HistorySensitive
	next.ChangesRepositoryGuidelines = previous.ChangesRepositoryGuidelines ||
		next.ChangesRepositoryGuidelines
	next.RiskTriggers = orderedUnion(
		AllRiskTriggers(),
		previous.RiskTriggers,
		next.RiskTriggers,
	)
	return next
}

func mergePhaseReasons(
	previous,
	next map[string]string,
	previousSelected,
	refreshedSelected []string,
) map[string]string {
	merged := make(map[string]string, len(AllPhases))
	for _, phase := range AllPhases {
		if reason := next[phase]; reason != "" {
			merged[phase] = reason
		} else {
			merged[phase] = previous[phase]
		}
		if slices.Contains(previousSelected, phase) &&
			!slices.Contains(refreshedSelected, phase) {
			merged[phase] = "retained from the prior conservative route"
		}
	}
	return merged
}

func appendReason(reasons []string, reason string) []string {
	reason = strings.TrimSpace(reason)
	if reason == "" || slices.Contains(reasons, reason) {
		return reasons
	}
	return append(reasons, reason)
}
