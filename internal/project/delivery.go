package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

const (
	// WorkflowStateVersion is the current additive delivery-state schema.
	WorkflowStateVersion = 1

	RouteEasy           = "easy"
	RouteStandard       = "standard"
	RouteHighRisk       = "high-risk"
	RouteStackCandidate = "stack-candidate"

	PhaseOutcomeMaterial = "material"
	PhaseOutcomeNoOp     = "no-op"

	EvidencePassed  = "passed"
	EvidenceFailed  = "failed"
	EvidenceBlocked = "blocked"

	EvidenceKindReview     = "review"
	EvidenceKindValidation = "validation"

	EvidenceOwnerImplement = "implement"
	EvidenceOwnerReview    = "review"
	EvidenceOwnerValidate  = "validate"

	GatePolicyUnknown  = "unknown"
	GatePolicyRequired = "required"
	GatePolicyNone     = "none"

	ReviewRoleCodeReviewer        = "code-reviewer"
	ReviewRoleSilentFailureHunter = "silent-failure-hunter"
	ReviewRoleTypeDesignAnalyzer  = "type-design-analyzer"
	ReviewRolePRTestAnalyzer      = "pr-test-analyzer"
	ReviewRoleCommentAnalyzer     = "comment-analyzer"
	ReviewRoleSecurity            = "security"
	ReviewRoleGitHistory          = "git-history"
	ReviewRolePriorPRHistory      = "prior-pr-history"

	RiskPublicContract         RiskTrigger = "public-contract"
	RiskPersistenceMigration   RiskTrigger = "persistence-migration"
	RiskAuthSecurity           RiskTrigger = "auth-security"
	RiskConcurrencyDistributed RiskTrigger = "concurrency-distributed"
	RiskDependencyBuildRelease RiskTrigger = "dependency-build-release"
	RiskGeneratedArtifact      RiskTrigger = "generated-artifact"
	RiskDestructiveOperation   RiskTrigger = "destructive-operation"
	RiskUnresolvedReviewCI     RiskTrigger = "unresolved-review-ci"
	RiskFailedGate             RiskTrigger = "failed-gate"

	easyFileLimit = 3
	easyLineLimit = 150
)

// AdaptiveDeliveryPhases is the canonical phase order for new deliver-pr runs.
var AdaptiveDeliveryPhases = []string{
	"route", "clarify", "plan", "implement", "simplify", "review", "validate", "open-pr",
}

// RepositorySnapshot identifies the exact repository state associated with a
// route or evidence record.
type RepositorySnapshot struct {
	BaseRef       string `json:"base_ref,omitempty"`
	BaseSHA       string `json:"base_sha"`
	BaseTipSHA    string `json:"base_tip_sha,omitempty"`
	HeadSHA       string `json:"head_sha"`
	Fingerprint   string `json:"fingerprint"`
	InputRevision string `json:"input_revision,omitempty"`
	FileCount     int    `json:"file_count"`
	ChangedLines  int    `json:"changed_lines"`
}

// Revision binds freshness-sensitive assessment to both repository and
// project-side delivery inputs. Legacy snapshots without an input revision
// retain their historical repository-only identity.
func (snapshot RepositorySnapshot) Revision() string {
	if snapshot.InputRevision == "" {
		return snapshot.Fingerprint
	}
	sum := sha256.Sum256([]byte(snapshot.Fingerprint + "\x00" + snapshot.InputRevision))
	return hex.EncodeToString(sum[:])
}

// RouteDecision is the durable result of deterministic delivery routing.
type RouteDecision struct {
	Revision          int                `json:"revision,omitempty"`
	Digest            string             `json:"digest,omitempty"`
	Class             string             `json:"class"`
	ForcedFull        bool               `json:"forced_full,omitempty"`
	SelectedPhases    []string           `json:"selected_phases"`
	PhaseReasons      map[string]string  `json:"phase_reasons"`
	ReviewRoles       []string           `json:"review_roles"`
	ReviewOwner       string             `json:"review_owner,omitempty"`
	ValidationOwner   string             `json:"validation_owner"`
	Snapshot          RepositorySnapshot `json:"snapshot"`
	PreviousClass     string             `json:"previous_class,omitempty"`
	EscalationReasons []string           `json:"escalation_reasons,omitempty"`
	StackRationale    string             `json:"stack_rationale,omitempty"`
	EvaluatedAt       string             `json:"evaluated_at"`
	Facts             RouteFacts         `json:"facts"`
}

// RiskTrigger is one normalized safety trigger retained with a route decision.
type RiskTrigger string

// RouteFacts are the normalized inputs retained so refresh can recompute a
// decision without repeated agent discovery.
type RouteFacts struct {
	RequestedBehaviorExplicit     bool          `json:"requested_behavior_explicit"`
	UnresolvedDecision            bool          `json:"unresolved_decision,omitempty"`
	GatePolicy                    GatePolicy    `json:"gate_policy,omitempty"`
	RiskAssessmentComplete        bool          `json:"risk_assessment_complete"`
	AssessmentFingerprint         string        `json:"assessment_fingerprint,omitempty"`
	PredictedSizeKnown            bool          `json:"predicted_size_known"`
	PredictedFileCount            int           `json:"predicted_file_count"`
	PredictedChangedLines         int           `json:"predicted_changed_lines"`
	ActualFileCount               int           `json:"actual_file_count"`
	ActualChangedLines            int           `json:"actual_changed_lines"`
	StackDecomposition            bool          `json:"stack_decomposition,omitempty"`
	StackRationale                string        `json:"stack_rationale,omitempty"`
	AuthorRequestedFullWorkflow   bool          `json:"author_requested_full_workflow,omitempty"`
	AuthorRequestedSimplification bool          `json:"author_requested_simplification,omitempty"`
	Duplication                   bool          `json:"duplication,omitempty"`
	GeneratedChurn                bool          `json:"generated_churn,omitempty"`
	ReviewRequestedCleanup        bool          `json:"review_requested_cleanup,omitempty"`
	ChangesTests                  bool          `json:"changes_tests,omitempty"`
	ChangesDocumentationComments  bool          `json:"changes_documentation_comments,omitempty"`
	ChangesTypeDesign             bool          `json:"changes_type_design,omitempty"`
	HistorySensitive              bool          `json:"history_sensitive,omitempty"`
	ChangesRepositoryGuidelines   bool          `json:"changes_repository_guidelines,omitempty"`
	RiskTriggers                  []RiskTrigger `json:"risk_triggers,omitempty"`
}

// NormalizeRiskAssessment clears assessment completeness unless it is bound
// to the supplied repository fingerprint. An empty fingerprint only enforces
// that a completed assessment names the snapshot it assessed.
func NormalizeRiskAssessment(facts RouteFacts, fingerprint string) RouteFacts {
	if !facts.RiskAssessmentComplete ||
		strings.TrimSpace(facts.AssessmentFingerprint) == "" ||
		(fingerprint != "" && facts.AssessmentFingerprint != fingerprint) {
		facts.RiskAssessmentComplete = false
		facts.AssessmentFingerprint = ""
	}
	return facts
}

// RouteSizeExceedsEasy reports whether either predicted or actual change size
// exceeds the conservative easy-route limit.
func RouteSizeExceedsEasy(facts RouteFacts) bool {
	return facts.PredictedFileCount > easyFileLimit ||
		facts.PredictedChangedLines > easyLineLimit ||
		facts.ActualFileCount > easyFileLimit ||
		facts.ActualChangedLines > easyLineLimit
}

// RouteNeedsSimplification reports whether current facts require an explicit
// simplification phase.
func RouteNeedsSimplification(facts RouteFacts) bool {
	return facts.AuthorRequestedSimplification || facts.Duplication ||
		facts.GeneratedChurn || facts.ReviewRequestedCleanup
}

// EasyRouteEligible reports whether facts satisfy every conservative easy
// route inclusion rule.
func EasyRouteEligible(facts RouteFacts) bool {
	return facts.RequestedBehaviorExplicit &&
		!facts.UnresolvedDecision &&
		facts.GatePolicy.Known() &&
		facts.RiskAssessmentComplete &&
		facts.PredictedSizeKnown &&
		!RouteSizeExceedsEasy(facts) &&
		!RouteNeedsSimplification(facts) &&
		!facts.StackDecomposition &&
		len(facts.RiskTriggers) == 0
}

// MinimumRouteClass returns the least conservative class permitted by the
// persisted current facts.
func MinimumRouteClass(facts RouteFacts) string {
	if facts.StackDecomposition {
		return RouteStackCandidate
	}
	if len(facts.RiskTriggers) > 0 {
		return RouteHighRisk
	}
	if EasyRouteEligible(facts) {
		return RouteEasy
	}
	return RouteStandard
}

// RequiredRoutePhases returns the minimum selected phase set for a route.
func RequiredRoutePhases(class string, facts RouteFacts, forcedFull bool) []string {
	if forcedFull {
		return append([]string(nil), AdaptiveDeliveryPhases...)
	}
	switch class {
	case RouteEasy:
		return []string{"route", "implement", "open-pr"}
	case RouteStackCandidate:
		selected := []string{"route", "clarify", "plan", "implement"}
		if RouteNeedsSimplification(facts) {
			selected = append(selected, "simplify")
		}
		return append(selected, "review", "validate", "open-pr")
	case RouteHighRisk:
		selected := []string{"route"}
		if !facts.RequestedBehaviorExplicit || facts.UnresolvedDecision {
			selected = append(selected, "clarify")
		}
		if facts.UnresolvedDecision || RouteSizeExceedsEasy(facts) ||
			hasPlanningRisk(facts.RiskTriggers) {
			selected = append(selected, "plan")
		}
		selected = append(selected, "implement")
		if RouteNeedsSimplification(facts) {
			selected = append(selected, "simplify")
		}
		return append(selected, "review", "validate", "open-pr")
	default:
		selected := []string{"route"}
		if !facts.RequestedBehaviorExplicit || facts.UnresolvedDecision {
			selected = append(selected, "clarify")
		}
		if facts.UnresolvedDecision || RouteSizeExceedsEasy(facts) {
			selected = append(selected, "plan")
		}
		selected = append(selected, "implement")
		if RouteNeedsSimplification(facts) {
			selected = append(selected, "simplify")
		}
		return append(selected, "review", "validate", "open-pr")
	}
}

func hasPlanningRisk(triggers []RiskTrigger) bool {
	for _, trigger := range triggers {
		if trigger != RiskUnresolvedReviewCI && trigger != RiskFailedGate {
			return true
		}
	}
	return false
}

// RequiredReviewRoles returns every review lens required by current facts.
func RequiredReviewRoles(facts RouteFacts) []string {
	roles := []string{ReviewRoleCodeReviewer}
	add := func(role string) {
		if !slices.Contains(roles, role) {
			roles = append(roles, role)
		}
	}
	if facts.ChangesTests || facts.GeneratedChurn {
		add(ReviewRolePRTestAnalyzer)
	}
	if facts.ChangesDocumentationComments {
		add(ReviewRoleCommentAnalyzer)
	}
	if facts.ChangesTypeDesign {
		add(ReviewRoleTypeDesignAnalyzer)
	}
	if facts.HistorySensitive {
		add(ReviewRoleGitHistory)
	}
	if facts.ChangesRepositoryGuidelines {
		add(ReviewRolePriorPRHistory)
	}
	for _, trigger := range facts.RiskTriggers {
		switch trigger {
		case RiskPublicContract:
			add(ReviewRoleTypeDesignAnalyzer)
			add(ReviewRoleGitHistory)
			add(ReviewRolePriorPRHistory)
		case RiskPersistenceMigration:
			add(ReviewRoleTypeDesignAnalyzer)
			add(ReviewRoleGitHistory)
		case RiskAuthSecurity:
			add(ReviewRoleSecurity)
		case RiskConcurrencyDistributed:
			add(ReviewRoleSilentFailureHunter)
			add(ReviewRoleGitHistory)
		case RiskDependencyBuildRelease:
			add(ReviewRoleSecurity)
			add(ReviewRolePRTestAnalyzer)
		case RiskGeneratedArtifact:
			add(ReviewRolePRTestAnalyzer)
		case RiskDestructiveOperation:
			add(ReviewRoleSecurity)
			add(ReviewRoleSilentFailureHunter)
		case RiskUnresolvedReviewCI:
			add(ReviewRolePriorPRHistory)
			add(ReviewRoleCommentAnalyzer)
			add(ReviewRolePRTestAnalyzer)
		case RiskFailedGate:
			add(ReviewRolePriorPRHistory)
			add(ReviewRolePRTestAnalyzer)
		}
	}
	order := []string{
		ReviewRoleCodeReviewer,
		ReviewRoleSilentFailureHunter,
		ReviewRoleTypeDesignAnalyzer,
		ReviewRolePRTestAnalyzer,
		ReviewRoleCommentAnalyzer,
		ReviewRoleSecurity,
		ReviewRoleGitHistory,
		ReviewRolePriorPRHistory,
	}
	slices.SortFunc(roles, func(left, right string) int {
		return slices.Index(order, left) - slices.Index(order, right)
	})
	return roles
}

// GatePolicy is the exact repository validation contract bound to a route.
type GatePolicy struct {
	Mode  string         `json:"mode,omitempty"`
	Gates []RequiredGate `json:"gates,omitempty"`
}

// RequiredGate identifies one required repository check without persisting
// its raw command.
type RequiredGate struct {
	ID              string `json:"id"`
	CommandDigest   string `json:"command_digest"`
	RedactedDisplay string `json:"redacted_display"`
}

// NormalizeGatePolicy validates and deterministically orders a gate policy.
func NormalizeGatePolicy(policy GatePolicy) (GatePolicy, error) {
	if policy.Mode == "" {
		policy.Mode = GatePolicyUnknown
	}
	switch policy.Mode {
	case GatePolicyUnknown, GatePolicyNone:
		if len(policy.Gates) != 0 {
			return GatePolicy{}, fmt.Errorf("gate policy mode %q cannot contain gates", policy.Mode)
		}
	case GatePolicyRequired:
		if len(policy.Gates) == 0 {
			return GatePolicy{}, fmt.Errorf("required gate policy must contain at least one gate")
		}
	default:
		return GatePolicy{}, fmt.Errorf("invalid gate policy mode %q", policy.Mode)
	}
	normalized := GatePolicy{Mode: policy.Mode, Gates: append([]RequiredGate(nil), policy.Gates...)}
	slices.SortFunc(normalized.Gates, func(left, right RequiredGate) int {
		return strings.Compare(left.ID, right.ID)
	})
	ids := make(map[string]bool, len(normalized.Gates))
	digests := make(map[string]bool, len(normalized.Gates))
	for _, gate := range normalized.Gates {
		if strings.TrimSpace(gate.ID) == "" {
			return GatePolicy{}, fmt.Errorf("required gate id cannot be empty")
		}
		if !validSHA256(gate.CommandDigest) {
			return GatePolicy{}, fmt.Errorf("required gate %q has invalid command digest", gate.ID)
		}
		if strings.TrimSpace(gate.RedactedDisplay) == "" {
			return GatePolicy{}, fmt.Errorf("required gate %q has empty redacted display", gate.ID)
		}
		if ids[gate.ID] {
			return GatePolicy{}, fmt.Errorf("duplicate required gate id %q", gate.ID)
		}
		if digests[gate.CommandDigest] {
			return GatePolicy{}, fmt.Errorf("duplicate required gate command digest for %q", gate.ID)
		}
		ids[gate.ID] = true
		digests[gate.CommandDigest] = true
	}
	return normalized, nil
}

// Known reports whether the repository gate set was explicitly established.
func (policy GatePolicy) Known() bool {
	return policy.Mode == GatePolicyRequired || policy.Mode == GatePolicyNone
}

// CommandEvidence records a normalized validation command result without its
// output.
type CommandEvidence struct {
	GateID     string `json:"gate_id"`
	Display    string `json:"display"`
	Digest     string `json:"digest"`
	ExitStatus int    `json:"exit_status"`
}

// FindingCounts records review findings by severity.
type FindingCounts struct {
	Critical   int `json:"critical,omitempty"`
	Important  int `json:"important,omitempty"`
	Suggestion int `json:"suggestion,omitempty"`
}

// EvidenceRecord binds a normalized review or validation result to one exact
// repository snapshot.
type EvidenceRecord struct {
	Snapshot        RepositorySnapshot `json:"snapshot"`
	RouteRevision   int                `json:"route_revision"`
	RouteDigest     string             `json:"route_digest"`
	DispatchID      string             `json:"dispatch_id"`
	Result          string             `json:"result"`
	Owner           string             `json:"owner"`
	Artifact        string             `json:"artifact,omitempty"`
	BlockerCategory string             `json:"blocker_category,omitempty"`
	BlockerReason   string             `json:"blocker_reason,omitempty"`
	CompletedAt     string             `json:"completed_at"`
	Commands        []CommandEvidence  `json:"commands,omitempty"`
	NoGates         bool               `json:"no_gates,omitempty"`
	Roles           []string           `json:"roles,omitempty"`
	Findings        FindingCounts      `json:"findings,omitempty"`
}

// Fresh reports whether passing evidence applies to snapshot exactly.
func (record EvidenceRecord) Fresh(snapshot RepositorySnapshot) bool {
	if record.Result != EvidencePassed ||
		record.Findings.Critical != 0 ||
		record.Findings.Important != 0 ||
		record.Findings.Suggestion < 0 {
		return false
	}
	for _, command := range record.Commands {
		if command.ExitStatus != 0 {
			return false
		}
	}
	return record.Snapshot.BaseSHA == snapshot.BaseSHA &&
		record.Snapshot.HeadSHA == snapshot.HeadSHA &&
		record.Snapshot.Fingerprint == snapshot.Fingerprint &&
		record.Snapshot.InputRevision == snapshot.InputRevision
}

// FreshForOwner reports whether passing evidence applies to the exact snapshot
// and was produced by the route's canonical owner.
func (record EvidenceRecord) FreshForOwner(snapshot RepositorySnapshot, owner string) bool {
	return record.Fresh(snapshot) && owner != "" && record.Owner == owner
}

// FreshForRoute reports whether evidence was produced for the current route
// revision by its active canonical owner.
func (record EvidenceRecord) FreshForRoute(
	snapshot RepositorySnapshot,
	route RouteDecision,
	owner string,
) bool {
	return record.FreshForOwner(snapshot, owner) &&
		route.Revision > 0 &&
		record.RouteRevision == route.Revision &&
		record.RouteDigest == route.Digest &&
		record.DispatchID != ""
}

// FreshForReview reports whether passing review evidence covers the exact
// snapshot and every role selected by the current route.
func (record EvidenceRecord) FreshForReview(snapshot RepositorySnapshot, requiredRoles []string) bool {
	if !record.Fresh(snapshot) {
		return false
	}
	for _, required := range requiredRoles {
		if !slices.Contains(record.Roles, required) {
			return false
		}
	}
	return true
}

// FreshForReviewRoute also binds review evidence to the current route
// revision and dispatch.
func (record EvidenceRecord) FreshForReviewRoute(
	snapshot RepositorySnapshot,
	route RouteDecision,
	requiredRoles []string,
	owner string,
) bool {
	return record.FreshForRoute(snapshot, route, owner) &&
		record.FreshForReview(snapshot, requiredRoles)
}

// ValidateEvidence rejects contradictory or incomplete normalized evidence.
func ValidateEvidence(kind string, record EvidenceRecord, requiredRoles []string) error {
	if !slices.Contains([]string{EvidencePassed, EvidenceFailed, EvidenceBlocked}, record.Result) {
		return fmt.Errorf("invalid evidence result %q", record.Result)
	}
	if err := validateSnapshot(record.Snapshot); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, record.CompletedAt); err != nil {
		return fmt.Errorf("invalid evidence completion time %q: %w", record.CompletedAt, err)
	}
	if record.RouteRevision <= 0 || !validSHA256(record.RouteDigest) {
		return fmt.Errorf("evidence requires a valid route revision and digest")
	}
	if strings.TrimSpace(record.DispatchID) == "" {
		return fmt.Errorf("evidence requires a dispatch id")
	}
	if strings.TrimSpace(record.Owner) == "" {
		return fmt.Errorf("evidence requires a canonical owner")
	}
	hasBlockerCategory := strings.TrimSpace(record.BlockerCategory) != ""
	hasBlockerReason := strings.TrimSpace(record.BlockerReason) != ""
	if hasBlockerCategory != hasBlockerReason {
		return fmt.Errorf("evidence blocker metadata requires both category and reason")
	}
	if record.Result == EvidenceBlocked {
		if !hasBlockerCategory {
			return fmt.Errorf("blocked evidence requires a blocker category and reason")
		}
	} else if record.Result == EvidencePassed && hasBlockerCategory {
		return fmt.Errorf("passing evidence cannot contain blocker metadata")
	}
	if record.Findings.Critical < 0 || record.Findings.Important < 0 || record.Findings.Suggestion < 0 {
		return fmt.Errorf("review finding counts cannot be negative")
	}
	switch kind {
	case EvidenceKindReview:
		return validateReviewEvidence(record, requiredRoles)
	case EvidenceKindValidation:
		return validateValidationEvidence(record)
	default:
		return fmt.Errorf("unknown evidence kind %q", kind)
	}
}

func validateReviewEvidence(record EvidenceRecord, requiredRoles []string) error {
	if record.Owner != "" &&
		record.Owner != EvidenceOwnerImplement &&
		record.Owner != EvidenceOwnerReview {
		return fmt.Errorf("review evidence has invalid owner %q", record.Owner)
	}
	if len(record.Commands) > 0 || record.NoGates {
		return fmt.Errorf("review evidence cannot contain validation commands")
	}
	if record.Result != EvidenceBlocked && len(record.Roles) == 0 {
		return fmt.Errorf("review evidence requires at least one role")
	}
	seen := make(map[string]bool, len(record.Roles))
	for _, role := range record.Roles {
		if !validReviewRole(role) {
			return fmt.Errorf("review evidence contains unknown role %q", role)
		}
		if seen[role] {
			return fmt.Errorf("review evidence contains duplicate role %q", role)
		}
		seen[role] = true
	}
	if record.Result == EvidencePassed && (record.Findings.Critical > 0 || record.Findings.Important > 0) {
		return fmt.Errorf("passing review evidence cannot contain Critical or Important findings")
	}
	if record.Result == EvidenceBlocked && (record.Findings.Critical > 0 || record.Findings.Important > 0) {
		return fmt.Errorf("blocked review evidence cannot contain Critical or Important findings")
	}
	if record.Result == EvidenceFailed && record.Findings.Critical == 0 && record.Findings.Important == 0 {
		return fmt.Errorf("failed review evidence requires a Critical or Important finding")
	}
	if record.Result == EvidencePassed {
		for _, role := range requiredRoles {
			if !seen[role] {
				return fmt.Errorf("passing review evidence is missing selected role %q", role)
			}
		}
	}
	return nil
}

// ValidateValidationEvidence rejects validation that does not exactly cover
// the route's normalized required gate set.
func ValidateValidationEvidence(record EvidenceRecord, policy GatePolicy) error {
	if err := ValidateEvidence(EvidenceKindValidation, record, nil); err != nil {
		return err
	}
	normalized, err := NormalizeGatePolicy(policy)
	if err != nil {
		return err
	}
	if record.Result == EvidenceBlocked {
		if record.NoGates {
			return fmt.Errorf("blocked validation evidence cannot use no-gates")
		}
		if normalized.Mode == GatePolicyRequired {
			return validateRecordedGates(record.Commands, normalized.Gates, false)
		}
		if len(record.Commands) != 0 {
			return fmt.Errorf("blocked validation evidence cannot record gates without a required gate policy")
		}
		return nil
	}
	switch normalized.Mode {
	case GatePolicyUnknown:
		return fmt.Errorf("validation evidence requires a known gate policy")
	case GatePolicyNone:
		if !record.NoGates {
			return fmt.Errorf("gate policy none requires no-gates validation evidence")
		}
		return nil
	}
	if record.NoGates {
		return fmt.Errorf("required gate policy cannot use no-gates validation evidence")
	}
	requireAll := record.Result != EvidenceFailed ||
		(strings.TrimSpace(record.BlockerCategory) == "" && strings.TrimSpace(record.BlockerReason) == "")
	return validateRecordedGates(record.Commands, normalized.Gates, requireAll)
}

func validateRecordedGates(commands []CommandEvidence, required []RequiredGate, requireAll bool) error {
	expected := make(map[string]string, len(required))
	for _, gate := range required {
		expected[gate.ID] = gate.CommandDigest
	}
	seen := make(map[string]bool, len(commands))
	for _, command := range commands {
		if seen[command.GateID] {
			return fmt.Errorf("validation evidence contains duplicate gate %q", command.GateID)
		}
		digest, ok := expected[command.GateID]
		if !ok {
			return fmt.Errorf("validation evidence contains unrelated gate %q", command.GateID)
		}
		if command.Digest != digest {
			return fmt.Errorf("validation evidence gate %q has the wrong command digest", command.GateID)
		}
		seen[command.GateID] = true
	}
	if requireAll {
		missing := make([]string, 0, len(expected)-len(seen))
		for _, gate := range required {
			if !seen[gate.ID] {
				missing = append(missing, gate.ID)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("validation evidence is missing required gates: %s", strings.Join(missing, ", "))
		}
	}
	return nil
}

func validateValidationEvidence(record EvidenceRecord) error {
	if record.Owner != "" &&
		record.Owner != EvidenceOwnerImplement &&
		record.Owner != EvidenceOwnerValidate {
		return fmt.Errorf("validation evidence has invalid owner %q", record.Owner)
	}
	if len(record.Roles) > 0 ||
		record.Findings.Critical != 0 || record.Findings.Important != 0 || record.Findings.Suggestion != 0 {
		return fmt.Errorf("validation evidence cannot contain review roles or findings")
	}
	if record.NoGates {
		if len(record.Commands) > 0 {
			return fmt.Errorf("no-gates validation evidence cannot contain commands")
		}
		if record.Result != EvidencePassed {
			return fmt.Errorf("no-gates validation evidence must pass")
		}
		return nil
	}
	if len(record.Commands) == 0 {
		if record.Result == EvidenceBlocked {
			return nil
		}
		return fmt.Errorf("validation evidence requires at least one command or no-gates attestation")
	}
	failed := false
	seen := make(map[string]bool, len(record.Commands))
	for _, command := range record.Commands {
		if strings.TrimSpace(command.Display) == "" || !validSHA256(command.Digest) {
			return fmt.Errorf("validation evidence contains an invalid redacted command")
		}
		if command.GateID != "" && seen[command.GateID] {
			return fmt.Errorf("validation evidence contains duplicate gate %q", command.GateID)
		}
		if command.GateID != "" {
			seen[command.GateID] = true
		}
		if command.ExitStatus < 0 {
			return fmt.Errorf("validation command %q has negative exit status", command.Display)
		}
		failed = failed || command.ExitStatus != 0
	}

	if record.Result == EvidencePassed && failed {
		return fmt.Errorf("passing validation evidence cannot contain failed commands")
	}
	if record.Result == EvidenceBlocked && failed {
		return fmt.Errorf("blocked validation evidence cannot contain failed commands")
	}
	if record.Result == EvidenceFailed && !failed {
		return fmt.Errorf("failed validation evidence requires a failed command")
	}
	return nil
}

// FreshForValidationRoute reports whether passing validation evidence covers
// the exact gate policy bound to the current route.
func (record EvidenceRecord) FreshForValidationRoute(
	snapshot RepositorySnapshot,
	route RouteDecision,
	owner string,
) bool {
	return record.FreshForRoute(snapshot, route, owner) &&
		ValidateValidationEvidence(record, route.Facts.GatePolicy) == nil
}

// RouteDigest hashes the semantic route decision. Evaluation time and revision
// are excluded so an unchanged re-evaluation retains the same revision.
func RouteDigest(decision RouteDecision) (string, error) {
	facts := decision.Facts
	normalizedPolicy, err := NormalizeGatePolicy(facts.GatePolicy)
	if err != nil {
		return "", err
	}
	facts.GatePolicy = normalizedPolicy
	value := struct {
		Class             string
		ForcedFull        bool
		SelectedPhases    []string
		PhaseReasons      map[string]string
		ReviewRoles       []string
		ReviewOwner       string
		ValidationOwner   string
		Snapshot          RepositorySnapshot
		PreviousClass     string
		EscalationReasons []string
		StackRationale    string
		Facts             RouteFacts
	}{
		Class: decision.Class, ForcedFull: decision.ForcedFull,
		SelectedPhases: decision.SelectedPhases, PhaseReasons: decision.PhaseReasons,
		ReviewRoles: decision.ReviewRoles, ReviewOwner: decision.EffectiveReviewOwner(),
		ValidationOwner: decision.ValidationOwner, Snapshot: decision.Snapshot,
		PreviousClass: decision.PreviousClass, EscalationReasons: decision.EscalationReasons,
		StackRationale: decision.StackRationale, Facts: facts,
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode route digest: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func validateSnapshot(snapshot RepositorySnapshot) error {
	if strings.TrimSpace(snapshot.BaseSHA) == "" ||
		strings.TrimSpace(snapshot.HeadSHA) == "" ||
		strings.TrimSpace(snapshot.Fingerprint) == "" {
		return fmt.Errorf("evidence snapshot requires base SHA, HEAD SHA, and fingerprint")
	}
	if snapshot.FileCount < 0 || snapshot.ChangedLines < 0 {
		return fmt.Errorf("evidence snapshot counts cannot be negative")
	}
	return nil
}

func validReviewRole(role string) bool {
	return slices.Contains([]string{
		ReviewRoleCodeReviewer,
		ReviewRoleSilentFailureHunter,
		ReviewRoleTypeDesignAnalyzer,
		ReviewRolePRTestAnalyzer,
		ReviewRoleCommentAnalyzer,
		ReviewRoleSecurity,
		ReviewRoleGitHistory,
		ReviewRolePriorPRHistory,
	}, role)
}

func validRiskTrigger(trigger RiskTrigger) bool {
	return slices.Contains([]RiskTrigger{
		RiskPublicContract,
		RiskPersistenceMigration,
		RiskAuthSecurity,
		RiskConcurrencyDistributed,
		RiskDependencyBuildRelease,
		RiskGeneratedArtifact,
		RiskDestructiveOperation,
		RiskUnresolvedReviewCI,
		RiskFailedGate,
	}, trigger)
}

// DeliveryEvidence stores the latest normalized review and validation result.
type DeliveryEvidence struct {
	Review     *EvidenceRecord `json:"review,omitempty"`
	Validation *EvidenceRecord `json:"validation,omitempty"`
}

// EffectiveReviewOwner returns the persisted owner, or the legacy independent
// owner when an older route selected the review phase before ownership was
// recorded explicitly.
func (decision RouteDecision) EffectiveReviewOwner() string {
	if decision.ReviewOwner != "" {
		return decision.ReviewOwner
	}
	if slices.Contains(decision.SelectedPhases, EvidenceOwnerReview) {
		return EvidenceOwnerReview
	}
	return ""
}

// FinalResult records the durable delivery outcome.
type FinalResult struct {
	Status        string             `json:"status"`
	PRNumber      int                `json:"pr_number,omitempty"`
	PRURL         string             `json:"pr_url,omitempty"`
	Reason        string             `json:"reason,omitempty"`
	RouteRevision int                `json:"route_revision,omitempty"`
	RouteDigest   string             `json:"route_digest,omitempty"`
	Snapshot      RepositorySnapshot `json:"snapshot,omitempty"`
	DispatchID    string             `json:"dispatch_id,omitempty"`
}

// ValidateFinalResult rejects incomplete or success-shaped delivery outcomes.
func ValidateFinalResult(result FinalResult) error {
	switch result.Status {
	case "opened":
		if result.PRNumber <= 0 || strings.TrimSpace(result.PRURL) == "" {
			return fmt.Errorf("opened final result requires a PR number and URL")
		}
		if strings.TrimSpace(result.Reason) != "" {
			return fmt.Errorf("opened final result cannot contain a failure reason")
		}
	case "blocked", "failed":
		if strings.TrimSpace(result.Reason) == "" {
			return fmt.Errorf("%s final result requires a reason", result.Status)
		}
	default:
		return fmt.Errorf("invalid final result status %q", result.Status)
	}
	return nil
}

func (ws WorkflowState) validateAdaptiveFinalState() error {
	openPR, ok := ws.Phases["open-pr"]
	if !ok {
		return fmt.Errorf("adaptive delivery requires an open-pr phase")
	}
	if ws.FinalResult == nil {
		if ws.PR.Number != 0 || ws.PR.URL != "" {
			if ws.PR.Number <= 0 || strings.TrimSpace(ws.PR.URL) == "" {
				return fmt.Errorf("adaptive PR identity requires both number and URL")
			}
			if openPR.Status == PhaseDone {
				return fmt.Errorf("completed open-pr phase requires an opened final result")
			}
			return nil
		}
		if openPR.Status == PhaseDone {
			return fmt.Errorf("completed open-pr phase requires an opened final result")
		}
		return nil
	}
	result := *ws.FinalResult
	if ws.Route == nil {
		return fmt.Errorf("adaptive final result requires a route")
	}
	switch result.Status {
	case "opened":
		if openPR.Status != PhaseDone || openPR.Dispatch != nil {
			return fmt.Errorf("opened final result requires a completed open-pr phase")
		}
		if ws.PR.Number != result.PRNumber || ws.PR.URL != result.PRURL {
			return fmt.Errorf("opened final result does not match the recorded PR identity")
		}
		if result.RouteRevision != ws.Route.Revision ||
			result.RouteDigest != ws.Route.Digest ||
			result.Snapshot != ws.Route.Snapshot {
			return fmt.Errorf("opened final result is stale for the current route")
		}
		if result.DispatchID == "" ||
			ws.LastDispatch != "open-pr" ||
			ws.LastDispatchID != result.DispatchID {
			return fmt.Errorf("opened final result is not bound to the latest open-pr dispatch")
		}
		if ws.Evidence.Review == nil ||
			!ws.Evidence.Review.FreshForReviewRoute(
				ws.Route.Snapshot,
				*ws.Route,
				ws.Route.ReviewRoles,
				ws.Route.EffectiveReviewOwner(),
			) ||
			!ws.evidenceMatchesOwnerDispatch(
				ws.Route.EffectiveReviewOwner(), ws.Evidence.Review.DispatchID,
			) {
			return fmt.Errorf("opened final result requires fresh review evidence")
		}
		if ws.Evidence.Validation == nil ||
			!ws.Evidence.Validation.FreshForValidationRoute(
				ws.Route.Snapshot,
				*ws.Route,
				ws.Route.ValidationOwner,
			) ||
			!ws.evidenceMatchesOwnerDispatch(
				ws.Route.ValidationOwner, ws.Evidence.Validation.DispatchID,
			) {
			return fmt.Errorf("opened final result requires fresh validation evidence")
		}
	case "blocked", "failed":
		if ws.PR.Number != 0 || ws.PR.URL != "" {
			return fmt.Errorf("%s final result cannot retain a PR identity", result.Status)
		}
		if openPR.Status != PhaseBlocked || openPR.Dispatch != nil {
			return fmt.Errorf("%s final result requires a blocked open-pr phase", result.Status)
		}
	default:
		return fmt.Errorf("invalid final result status %q", result.Status)
	}
	return nil
}

// ValidateAdaptiveDispatch rejects dispatches that do not match the current
// selected phase of a versioned adaptive delivery.
func (ws WorkflowState) ValidateAdaptiveDispatch(name string) error {
	if !ws.UsesAdaptiveDelivery() {
		return nil
	}
	phase, ok := ws.Phases[name]
	if !ok {
		return fmt.Errorf("unknown phase %q", name)
	}
	if ws.Route == nil {
		if name != "route" {
			return fmt.Errorf("adaptive delivery must dispatch route before %q", name)
		}
		return validateDispatchableStatus(name, phase.Status)
	}
	if !slices.Contains(ws.Route.SelectedPhases, name) {
		return fmt.Errorf("phase %q is not selected by the current route", name)
	}
	current := ws.currentSelectedPhase()
	if current == "" {
		return fmt.Errorf("adaptive delivery is already complete")
	}
	if current != name {
		return fmt.Errorf("phase %q cannot dispatch before current phase %q", name, current)
	}
	return validateDispatchableStatus(name, phase.Status)
}

// ValidateAdaptiveFinish requires CLI-driven adaptive phase results to apply
// to the active dispatch for the current selected phase.
func (ws WorkflowState) ValidateAdaptiveFinish(name, status string) error {
	if !ws.UsesAdaptiveDelivery() {
		return nil
	}
	if ws.Route == nil {
		return fmt.Errorf("adaptive delivery requires a current route decision")
	}
	phase, ok := ws.Phases[name]
	if !ok {
		return fmt.Errorf("unknown phase %q", name)
	}
	if !slices.Contains(ws.Route.SelectedPhases, name) {
		return fmt.Errorf("phase %q is not selected by the current route", name)
	}
	current := ws.currentSelectedPhase()
	if current != name {
		return fmt.Errorf("phase %q cannot finish before current phase %q", name, current)
	}
	if phase.Status != PhaseInProgress || phase.Dispatch == nil {
		return fmt.Errorf("phase %q requires an active dispatch before finishing", name)
	}
	if status == PhaseSkipped {
		return fmt.Errorf("selected phase %q cannot be skipped", name)
	}
	if status == PhaseDone {
		if err := ws.validatePhaseEvidence(name); err != nil {
			return err
		}
	}
	return nil
}

func (ws WorkflowState) validatePhaseEvidence(name string) error {
	if ws.Route == nil {
		return nil
	}
	if ws.Route.EffectiveReviewOwner() == name {
		if ws.Evidence.Review == nil ||
			!ws.Evidence.Review.FreshForReviewRoute(
				ws.Route.Snapshot,
				*ws.Route,
				ws.Route.ReviewRoles,
				name,
			) ||
			!ws.evidenceMatchesOwnerDispatch(name, ws.Evidence.Review.DispatchID) {
			return fmt.Errorf("phase %q requires fresh passing review evidence before finishing", name)
		}
	}
	if ws.Route.ValidationOwner == name {
		if ws.Evidence.Validation == nil ||
			!ws.Evidence.Validation.FreshForValidationRoute(
				ws.Route.Snapshot,
				*ws.Route,
				name,
			) ||
			!ws.evidenceMatchesOwnerDispatch(name, ws.Evidence.Validation.DispatchID) {
			return fmt.Errorf("phase %q requires fresh passing validation evidence before finishing", name)
		}
	}
	return nil
}

func (ws WorkflowState) evidenceMatchesOwnerDispatch(name, dispatchID string) bool {
	phase, ok := ws.Phases[name]
	if !ok {
		return false
	}
	if phase.Status == PhaseInProgress && phase.Dispatch != nil {
		return phase.Dispatch.ID == dispatchID
	}
	if phase.CompletedDispatchID == "" {
		return true
	}
	return phase.CompletedDispatchID == dispatchID
}

// UsesAdaptiveDelivery reports whether transition guards apply to this state.
// Route-less custom phase orders remain compatible with the legacy state CLI.
func (ws WorkflowState) UsesAdaptiveDelivery() bool {
	return ws.Workflow == "deliver-pr" &&
		(ws.Route != nil || (ws.Version > 0 && slices.Equal(ws.Order, AdaptiveDeliveryPhases)))
}

func validateDispatchableStatus(name, status string) error {
	switch status {
	case PhasePending, PhaseBlocked, PhaseEscalated:
		return nil
	case PhaseInProgress:
		return fmt.Errorf("phase %q already has an active dispatch", name)
	default:
		return fmt.Errorf("phase %q is already terminal with status %q", name, status)
	}
}

func (ws WorkflowState) currentSelectedPhase() string {
	if ws.Route == nil {
		return ""
	}
	for _, name := range ws.Order {
		if slices.Contains(ws.Route.SelectedPhases, name) &&
			!terminalPhaseStatus(ws.Phases[name].Status) {
			return name
		}
	}
	return ""
}

// ValidateOpenPRReadiness requires fresh route-bound review and validation
// evidence. Successful completion additionally requires the active open-pr
// dispatch that is bound to the current route.
func (ws WorkflowState) ValidateOpenPRReadiness(
	snapshot RepositorySnapshot,
	requireDispatch bool,
) error {
	if !ws.UsesAdaptiveDelivery() {
		return nil
	}
	if ws.Route == nil {
		return fmt.Errorf("open-pr requires a current route decision")
	}
	if ws.Route.Snapshot != snapshot {
		return fmt.Errorf("open-pr route snapshot is stale")
	}
	if ws.currentSelectedPhase() != "open-pr" {
		return fmt.Errorf("open-pr is not the current selected phase")
	}
	if ws.Evidence.Review == nil ||
		!ws.Evidence.Review.FreshForReviewRoute(
			snapshot,
			*ws.Route,
			ws.Route.ReviewRoles,
			ws.Route.EffectiveReviewOwner(),
		) ||
		!ws.evidenceMatchesOwnerDispatch(
			ws.Route.EffectiveReviewOwner(), ws.Evidence.Review.DispatchID,
		) {
		return fmt.Errorf("open-pr requires fresh passing review evidence for the current route")
	}
	if ws.Evidence.Validation == nil ||
		!ws.Evidence.Validation.FreshForValidationRoute(
			snapshot,
			*ws.Route,
			ws.Route.ValidationOwner,
		) ||
		!ws.evidenceMatchesOwnerDispatch(
			ws.Route.ValidationOwner, ws.Evidence.Validation.DispatchID,
		) {
		return fmt.Errorf("open-pr requires fresh passing validation evidence for the exact gate policy")
	}
	if !requireDispatch {
		return nil
	}
	phase := ws.Phases["open-pr"]
	if phase.Status != PhaseInProgress || phase.Dispatch == nil {
		return fmt.Errorf("open-pr requires an active dispatch")
	}
	if phase.Dispatch.RouteRevision != ws.Route.Revision ||
		phase.Dispatch.RouteDigest != ws.Route.Digest {
		return fmt.Errorf("open-pr dispatch is stale for the current route")
	}
	return nil
}

// ApplyRoute updates selected/skipped phases while preserving completed
// non-evidence work. Route changes may reopen stale evidence owners and
// open-pr, and automatic changes may only become more conservative.
func (ws *WorkflowState) ApplyRoute(decision RouteDecision) error {
	if !validRouteClass(decision.Class) {
		return fmt.Errorf("invalid route class %q", decision.Class)
	}
	normalizedPolicy, err := NormalizeGatePolicy(decision.Facts.GatePolicy)
	if err != nil {
		return err
	}
	decision.Facts.GatePolicy = normalizedPolicy
	if ws.Route != nil && RouteRank(decision.Class) < RouteRank(ws.Route.Class) {
		return fmt.Errorf("route downgrade %q -> %q is not allowed", ws.Route.Class, decision.Class)
	}
	digest, err := RouteDigest(decision)
	if err != nil {
		return err
	}
	previousRevision := 0
	routeChanged := true
	inputsChanged := false
	if ws.Route != nil {
		previousRevision = ws.Route.Revision
		routeChanged = ws.Route.Digest != digest
		inputsChanged = ws.Route.Snapshot.InputRevision != decision.Snapshot.InputRevision
	}
	decision.Revision = max(1, previousRevision)
	if ws.Route != nil && routeChanged {
		decision.Revision = max(1, previousRevision+1)
	}
	decision.Digest = digest
	reboundPhase := ""
	if route := ws.Phases["route"]; route.Status == PhaseInProgress && route.Dispatch != nil {
		reboundPhase = "route"
	} else if ws.Route != nil && routeChanged && canRebindActiveDispatch(*ws.Route, decision) {
		current := ws.currentSelectedPhase()
		phase := ws.Phases[current]
		if current != "route" && current != "open-pr" &&
			phase.Status == PhaseInProgress && phase.Dispatch != nil {
			reboundPhase = current
		}
	}
	selected := make(map[string]bool, len(decision.SelectedPhases))
	for _, phase := range decision.SelectedPhases {
		if _, ok := ws.Phases[phase]; !ok {
			return fmt.Errorf("route selected unknown phase %q", phase)
		}
		selected[phase] = true
	}
	if inputsChanged {
		ws.reopenImplementationDependents(
			selected,
			"delivery inputs changed; fresh implementation required",
		)
	}
	for _, name := range ws.Order {
		phase := ws.Phases[name]
		if phase.Status == PhaseDone {
			continue
		}
		if phase.Status == PhaseBlocked || phase.Status == PhaseEscalated {
			ws.Phases[name] = phase
			continue
		}
		if selected[name] {
			if phase.Status == PhaseSkipped {
				phase.Status = PhasePending
				phase.Reason = ""
				phase.Outcome = ""
				phase.EndedAt = ""
			}
			if routeChanged && phase.Status == PhaseInProgress && phase.Dispatch != nil {
				if name == reboundPhase {
					phase.Dispatch.RouteRevision = decision.Revision
					phase.Dispatch.RouteDigest = decision.Digest
				} else if previousRevision > 0 {
					phase.Status = PhaseEscalated
					phase.Reason = "route revision invalidated active dispatch"
					phase.Outcome = ""
					phase.EndedAt = ""
					phase.Dispatch = nil
				}
			}
			ws.Phases[name] = phase
			continue
		}
		reason := decision.PhaseReasons[name]
		if reason == "" {
			return fmt.Errorf("route phase %q: skipped phase requires a reason", name)
		}
		phase.Status = PhaseSkipped
		phase.Reason = reason
		phase.Outcome = PhaseOutcomeNoOp
		ws.Phases[name] = phase
	}
	copyDecision := decision
	copyDecision.SelectedPhases = append([]string(nil), decision.SelectedPhases...)
	copyDecision.ReviewRoles = append([]string(nil), decision.ReviewRoles...)
	copyDecision.EscalationReasons = append([]string(nil), decision.EscalationReasons...)
	copyDecision.PhaseReasons = cloneStringMap(decision.PhaseReasons)
	ws.Version = WorkflowStateVersion
	ws.Route = &copyDecision
	if routeChanged && previousRevision > 0 {
		ws.FinalResult = nil
		if phase := ws.Phases["open-pr"]; phase.Status == PhaseDone {
			phase.Status = PhaseEscalated
			phase.Reason = "route revision invalidated opened PR result"
			phase.Outcome = ""
			phase.EndedAt = ""
			phase.Dispatch = nil
			ws.Phases["open-pr"] = phase
		}
		ws.invalidateEvidenceForRouteChange(reboundPhase)
		if inputsChanged {
			ws.Evidence = DeliveryEvidence{}
		}
	}
	return nil
}

func canRebindActiveDispatch(previous, next RouteDecision) bool {
	return previous.Class == next.Class &&
		previous.ForcedFull == next.ForcedFull &&
		previous.Snapshot.InputRevision == next.Snapshot.InputRevision &&
		rebindRelevantFactsEqual(previous.Facts, next.Facts) &&
		slices.Equal(previous.SelectedPhases, next.SelectedPhases) &&
		slices.Equal(previous.ReviewRoles, next.ReviewRoles) &&
		previous.EffectiveReviewOwner() == next.EffectiveReviewOwner() &&
		previous.ValidationOwner == next.ValidationOwner
}

func rebindRelevantFactsEqual(previous, next RouteFacts) bool {
	previousPolicy, previousErr := NormalizeGatePolicy(previous.GatePolicy)
	nextPolicy, nextErr := NormalizeGatePolicy(next.GatePolicy)
	if previousErr != nil || nextErr != nil || !reflect.DeepEqual(previousPolicy, nextPolicy) {
		return false
	}
	if !slices.Equal(previous.RiskTriggers, next.RiskTriggers) {
		return false
	}
	previous.GatePolicy = GatePolicy{}
	previous.RiskTriggers = nil
	previous.ActualFileCount = 0
	previous.ActualChangedLines = 0
	previous.RiskAssessmentComplete = false
	previous.AssessmentFingerprint = ""
	next.GatePolicy = GatePolicy{}
	next.RiskTriggers = nil
	next.ActualFileCount = 0
	next.ActualChangedLines = 0
	next.RiskAssessmentComplete = false
	next.AssessmentFingerprint = ""
	return reflect.DeepEqual(previous, next)
}

func (ws *WorkflowState) reopenImplementationDependents(selected map[string]bool, reason string) {
	reopen := false
	for _, name := range ws.Order {
		if name == EvidenceOwnerImplement {
			reopen = true
		}
		if !reopen || !selected[name] {
			continue
		}
		phase := ws.Phases[name]
		if phase.Status != PhaseDone {
			continue
		}
		phase.Status = PhaseEscalated
		phase.Reason = reason
		phase.Outcome = ""
		phase.EndedAt = ""
		phase.Dispatch = nil
		ws.Phases[name] = phase
	}
}

func (ws *WorkflowState) invalidateEvidenceForRouteChange(reboundPhase string) {
	for name, phase := range ws.Phases {
		if phase.Dispatch != nil && name != reboundPhase {
			phase.Dispatch = nil
			ws.Phases[name] = phase
		}
	}
	for kind, record := range map[string]*EvidenceRecord{
		EvidenceKindReview:     ws.Evidence.Review,
		EvidenceKindValidation: ws.Evidence.Validation,
	} {
		if record == nil {
			continue
		}
		owner := ws.Route.ValidationOwner
		if kind == EvidenceKindReview {
			owner = ws.Route.EffectiveReviewOwner()
		}
		if owner == EvidenceOwnerImplement {
			ws.reopenEvidencePhase(EvidenceOwnerImplement, "route revision invalidated implementation evidence")
			continue
		}
		ws.reopenEvidencePhase(owner, "route revision invalidated "+kind+" evidence")
	}
}

// InvalidateEvidenceForOwner removes evidence owned by a newly dispatched
// phase before replacement work begins.
func (ws *WorkflowState) InvalidateEvidenceForOwner(owner string) {
	if ws.Route == nil {
		return
	}
	if ws.Evidence.Review != nil && ws.Route.EffectiveReviewOwner() == owner {
		ws.Evidence.Review = nil
	}
	if ws.Evidence.Validation != nil && ws.Route.ValidationOwner == owner {
		ws.Evidence.Validation = nil
	}
}

func (ws *WorkflowState) reopenEvidencePhase(name, reason string) {
	phase, ok := ws.Phases[name]
	if !ok || !slices.Contains(ws.Route.SelectedPhases, name) {
		return
	}
	if phase.Status != PhaseDone && phase.Status != PhaseSkipped {
		return
	}
	phase.Status = PhaseEscalated
	phase.Reason = reason
	phase.Outcome = ""
	phase.EndedAt = ""
	phase.Dispatch = nil
	ws.Phases[name] = phase
}

func validRouteClass(class string) bool {
	return slices.Contains([]string{RouteEasy, RouteStandard, RouteHighRisk, RouteStackCandidate}, class)
}

// RouteRank orders route classes from least to most conservative.
func RouteRank(class string) int {
	switch class {
	case RouteEasy:
		return 1
	case RouteStandard:
		return 2
	case RouteHighRisk:
		return 3
	case RouteStackCandidate:
		return 4
	default:
		return 0
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
