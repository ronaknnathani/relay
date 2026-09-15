package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Phase status values. A workflow phase moves pending -> in-progress -> done.
const (
	PhasePending    = "pending"
	PhaseInProgress = "in-progress"
	PhaseDone       = "done"
	PhaseSkipped    = "skipped"
	PhaseBlocked    = "blocked"
	PhaseEscalated  = "escalated"
)

// PhaseState is the status of a single workflow phase, plus optional progress
// detail: the artifact it produced and a free-form task marker (e.g. "3/7").
type PhaseState struct {
	Status    string         `json:"status"`
	Artifact  string         `json:"artifact,omitempty"`
	Task      string         `json:"task,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Outcome   string         `json:"outcome,omitempty"`
	StartedAt string         `json:"started_at,omitempty"`
	EndedAt   string         `json:"ended_at,omitempty"`
	Dispatch  *PhaseDispatch `json:"dispatch,omitempty"`
}

// PhaseDispatch binds phase-owned evidence to one unguessable dispatch token.
// Only its hash is persisted; the token is returned once to the dispatched
// worker.
type PhaseDispatch struct {
	ID            string `json:"id"`
	TokenHash     string `json:"token_hash"`
	RouteRevision int    `json:"route_revision,omitempty"`
	RouteDigest   string `json:"route_digest,omitempty"`
}

// PRRef records the pull request a project produced.
type PRRef struct {
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
}

// WorkflowState is the resumable, machine-owned state for one project's run
// through a workflow. The relay binary is its sole writer; skills mutate it
// only through `relay state`, never by editing JSON by hand, so the schema
// stays valid across agents (Claude/Copilot/Codex). Order is the canonical
// phase sequence the workflow skill declares at init; Phases tracks each
// phase's status. The current/next phase is derived from Order + Phases rather
// than stored, so there is a single source of truth.
//
// Concurrency: SaveState is atomic (unique temp + rename), so a reader never
// sees a half-written file. The load-modify-save sequence in each `relay state`
// command is not locked, so concurrent writers to the same slug are
// last-write-wins. This is acceptable because callers serialize writes per
// project (one writer per branch/run), which the orchestrator guarantees.
type WorkflowState struct {
	Version       int                   `json:"version,omitempty"`
	Slug          string                `json:"slug"`
	Workflow      string                `json:"workflow"`
	Order         []string              `json:"order"`
	Phases        map[string]PhaseState `json:"phases"`
	Route         *RouteDecision        `json:"route,omitempty"`
	Evidence      DeliveryEvidence      `json:"evidence,omitempty"`
	SubagentCount int                   `json:"subagent_count,omitempty"`
	FinalResult   *FinalResult          `json:"final_result,omitempty"`
	PR            PRRef                 `json:"pr"`
	Updated       string                `json:"updated"`
}

// validStatus reports whether s is a supported phase status.
func validStatus(s string) bool {
	switch s {
	case PhasePending, PhaseInProgress, PhaseDone, PhaseSkipped, PhaseBlocked, PhaseEscalated:
		return true
	default:
		return false
	}
}

// ValidateSlug rejects slugs that could escape the project tree or are unsafe
// as a path segment. The state commands resolve files from a slug, so an
// unsanitized `..`-bearing slug would otherwise write outside the project dir.
func ValidateSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("slug cannot be empty")
	}
	if strings.ContainsAny(slug, "/\\") || strings.Contains(slug, "..") || strings.HasPrefix(slug, ".") {
		return fmt.Errorf("invalid slug %q: must be a single path segment without '/', '..', or a leading '.'", slug)
	}
	return nil
}

// StatePath returns the state.json path for an active project slug.
func StatePath(slug string) string {
	return filepath.Join(ActiveDir(), slug, "state.json")
}

// NewState builds a fresh state for a workflow with every phase pending.
// It errors on an empty order or duplicate phase names so a malformed
// declaration fails at init rather than producing an unusable state machine.
func NewState(slug, workflow string, order []string) (WorkflowState, error) {
	if len(order) == 0 {
		return WorkflowState{}, fmt.Errorf("workflow %q: phase order is empty", workflow)
	}
	// Copy the order so a later mutation of the caller's slice cannot diverge
	// the canonical order from the phases map keys.
	ordered := append([]string(nil), order...)
	phases := make(map[string]PhaseState, len(ordered))
	for _, p := range ordered {
		if _, dup := phases[p]; dup {
			return WorkflowState{}, fmt.Errorf("workflow %q: duplicate phase %q", workflow, p)
		}
		phases[p] = PhaseState{Status: PhasePending}
	}
	return WorkflowState{
		Version: WorkflowStateVersion, Slug: slug, Workflow: workflow,
		Order: ordered, Phases: phases,
	}, nil
}

// validate asserts the structural invariants the state machine relies on:
// non-empty order, no duplicate phases, every order entry present in Phases,
// and every status valid. LoadState calls it so a partially-written, drifted,
// or hand-edited file (the cross-agent source of truth any agent may have
// touched) fails loudly at load instead of misbehaving downstream.
func (ws WorkflowState) validate() error {
	if ws.Version < 0 || ws.Version > WorkflowStateVersion {
		return fmt.Errorf("state %q: unsupported version %d", ws.Slug, ws.Version)
	}
	if len(ws.Order) == 0 {
		return fmt.Errorf("state %q: empty phase order", ws.Slug)
	}
	seen := make(map[string]bool, len(ws.Order))
	for _, p := range ws.Order {
		if seen[p] {
			return fmt.Errorf("state %q: duplicate phase %q in order", ws.Slug, p)
		}
		seen[p] = true
		ph, ok := ws.Phases[p]
		if !ok {
			return fmt.Errorf("state %q: phase %q in order but missing from phases", ws.Slug, p)
		}
		if !validStatus(ph.Status) {
			return fmt.Errorf("state %q: phase %q has invalid status %q", ws.Slug, p, ph.Status)
		}
		if requiresReason(ph.Status) && strings.TrimSpace(ph.Reason) == "" {
			return fmt.Errorf("state %q: phase %q status %q requires a reason", ws.Slug, p, ph.Status)
		}
		if ph.Outcome != "" && ph.Outcome != PhaseOutcomeMaterial && ph.Outcome != PhaseOutcomeNoOp {
			return fmt.Errorf("state %q: phase %q has invalid outcome %q", ws.Slug, p, ph.Outcome)
		}
		if (ph.Status == PhasePending || ph.Status == PhaseInProgress ||
			ph.Status == PhaseBlocked || ph.Status == PhaseEscalated) && ph.Outcome != "" {
			return fmt.Errorf(
				"state %q: phase %q status %q cannot have outcome %q",
				ws.Slug, p, ph.Status, ph.Outcome,
			)
		}
		if ph.Status == PhaseSkipped && ph.Outcome != "" && ph.Outcome != PhaseOutcomeNoOp {
			return fmt.Errorf(
				"state %q: phase %q status %q requires outcome %q",
				ws.Slug, p, ph.Status, PhaseOutcomeNoOp,
			)
		}
		if ph.Dispatch != nil {
			if ph.Status != PhaseInProgress {
				return fmt.Errorf("state %q: phase %q has a dispatch while status is %q", ws.Slug, p, ph.Status)
			}
			if strings.TrimSpace(ph.Dispatch.ID) == "" || !validSHA256(ph.Dispatch.TokenHash) {
				return fmt.Errorf("state %q: phase %q has an invalid dispatch", ws.Slug, p)
			}
		}
	}
	if ws.Route != nil && !validRouteClass(ws.Route.Class) {
		return fmt.Errorf("state %q: invalid route class %q", ws.Slug, ws.Route.Class)
	}
	if ws.FinalResult != nil {
		if err := ValidateFinalResult(*ws.FinalResult); err != nil {
			return fmt.Errorf("state %q: %w", ws.Slug, err)
		}
	}
	if !ws.hasAdaptiveState() {
		return nil
	}
	if ws.Route == nil {
		if ws.Evidence.Review != nil || ws.Evidence.Validation != nil {
			return fmt.Errorf("state %q: delivery evidence requires a route decision", ws.Slug)
		}
		return nil
	}
	if err := ws.validateRoute(); err != nil {
		return fmt.Errorf("state %q: %w", ws.Slug, err)
	}
	return nil
}

func (ws WorkflowState) hasAdaptiveState() bool {
	if ws.Version > 0 || ws.Route != nil || ws.Evidence.Review != nil ||
		ws.Evidence.Validation != nil || ws.SubagentCount != 0 || ws.FinalResult != nil {
		return true
	}
	for _, phase := range ws.Phases {
		if phase.Status == PhaseSkipped || phase.Status == PhaseBlocked || phase.Status == PhaseEscalated ||
			phase.Reason != "" || phase.Outcome != "" || phase.StartedAt != "" || phase.EndedAt != "" {
			return true
		}
	}
	return false
}

func (ws WorkflowState) validateRoute() error {
	route := *ws.Route
	if err := validateSnapshot(route.Snapshot); err != nil {
		return fmt.Errorf("invalid route snapshot: %w", err)
	}
	if _, err := time.Parse(time.RFC3339, route.EvaluatedAt); err != nil {
		return fmt.Errorf("invalid route evaluation time %q: %w", route.EvaluatedAt, err)
	}
	if route.Revision <= 0 || !validSHA256(route.Digest) {
		return fmt.Errorf("route has invalid revision metadata")
	}
	selected := make(map[string]bool, len(route.SelectedPhases))
	for _, phase := range route.SelectedPhases {
		if _, ok := ws.Phases[phase]; !ok {
			return fmt.Errorf("route selected unknown phase %q", phase)
		}
		if selected[phase] {
			return fmt.Errorf("route selected duplicate phase %q", phase)
		}
		selected[phase] = true
	}
	for _, required := range []string{"route", "implement", "open-pr"} {
		if !selected[required] {
			return fmt.Errorf("route is missing required phase %q", required)
		}
	}
	for _, phase := range ws.Order {
		if strings.TrimSpace(route.PhaseReasons[phase]) == "" {
			return fmt.Errorf("route phase %q requires a reason", phase)
		}
	}
	if route.ForcedFull && len(selected) != len(ws.Order) {
		return fmt.Errorf("forced-full route must select every phase")
	}
	if len(route.ReviewRoles) == 0 {
		return fmt.Errorf("route requires at least one review role")
	}
	reviewOwner := route.EffectiveReviewOwner()
	if reviewOwner != EvidenceOwnerImplement && reviewOwner != EvidenceOwnerReview {
		return fmt.Errorf("route has invalid review owner %q", route.ReviewOwner)
	}
	if reviewOwner == EvidenceOwnerImplement &&
		(route.Class != RouteEasy || route.ForcedFull || selected["review"]) {
		return fmt.Errorf("only an unforced easy route without a review phase may assign review to implement")
	}
	if reviewOwner == EvidenceOwnerReview && !selected["review"] {
		return fmt.Errorf("review-owned route must select review")
	}
	roles := make(map[string]bool, len(route.ReviewRoles))
	for _, role := range route.ReviewRoles {
		if !validReviewRole(role) {
			return fmt.Errorf("route contains unknown review role %q", role)
		}
		if roles[role] {
			return fmt.Errorf("route contains duplicate review role %q", role)
		}
		roles[role] = true
	}
	if route.ValidationOwner != EvidenceOwnerImplement && route.ValidationOwner != EvidenceOwnerValidate {
		return fmt.Errorf("route has invalid validation owner %q", route.ValidationOwner)
	}
	if route.ValidationOwner == EvidenceOwnerImplement && (route.Class != RouteEasy || route.ForcedFull) {
		return fmt.Errorf("only an unforced easy route may assign validation to implement")
	}
	if route.ValidationOwner == EvidenceOwnerImplement && selected["validate"] {
		return fmt.Errorf("implement cannot own validation while validate is selected")
	}
	if route.ValidationOwner == EvidenceOwnerValidate && !selected["validate"] {
		return fmt.Errorf("validation-owned route must select validate")
	}
	if route.PreviousClass != "" {
		if !validRouteClass(route.PreviousClass) ||
			RouteRank(route.PreviousClass) > RouteRank(route.Class) {
			return fmt.Errorf("route has invalid previous class %q", route.PreviousClass)
		}
		if len(route.EscalationReasons) == 0 {
			return fmt.Errorf("route with previous class requires an escalation reason")
		}
	}
	for _, reason := range route.EscalationReasons {
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("route contains an empty escalation reason")
		}
	}
	if route.Class == RouteStackCandidate && strings.TrimSpace(route.StackRationale) == "" {
		return fmt.Errorf("stack-candidate route requires a rationale")
	}
	if err := validateRouteFacts(route.Facts); err != nil {
		return err
	}
	if normalized := NormalizeRiskAssessment(route.Facts, route.Snapshot.Fingerprint); normalized.RiskAssessmentComplete != route.Facts.RiskAssessmentComplete ||
		normalized.AssessmentFingerprint != route.Facts.AssessmentFingerprint {
		return fmt.Errorf("route risk assessment does not match the current snapshot")
	}
	if route.Class == RouteEasy && !EasyRouteEligible(route.Facts) {
		return fmt.Errorf("easy route facts do not prove easy eligibility")
	}
	digest, err := RouteDigest(route)
	if err != nil {
		return fmt.Errorf("compute route digest: %w", err)
	}
	if route.Digest != digest {
		return fmt.Errorf("route digest does not match persisted semantics")
	}
	if ws.Evidence.Review != nil {
		if err := ValidateEvidence(EvidenceKindReview, *ws.Evidence.Review, nil); err != nil {
			return fmt.Errorf("invalid review evidence: %w", err)
		}
	}
	if ws.Evidence.Validation != nil {
		var err error
		if route.Facts.GatePolicy.Known() {
			err = ValidateValidationEvidence(*ws.Evidence.Validation, route.Facts.GatePolicy)
		} else {
			err = ValidateEvidence(EvidenceKindValidation, *ws.Evidence.Validation, nil)
		}
		if err != nil {
			return fmt.Errorf("invalid validation evidence: %w", err)
		}
	}
	return nil
}

func validateRouteFacts(facts RouteFacts) error {
	for _, value := range []struct {
		name  string
		count int
	}{
		{"predicted file count", facts.PredictedFileCount},
		{"predicted changed-line count", facts.PredictedChangedLines},
		{"actual file count", facts.ActualFileCount},
		{"actual changed-line count", facts.ActualChangedLines},
	} {
		if value.count < 0 {
			return fmt.Errorf("route %s cannot be negative", value.name)
		}
	}
	if _, err := NormalizeGatePolicy(facts.GatePolicy); err != nil {
		return fmt.Errorf("route gate policy: %w", err)
	}
	seen := make(map[RiskTrigger]bool, len(facts.RiskTriggers))
	for _, trigger := range facts.RiskTriggers {
		if !validRiskTrigger(trigger) {
			return fmt.Errorf("route contains unknown risk trigger %q", trigger)
		}
		if seen[trigger] {
			return fmt.Errorf("route contains duplicate risk trigger %q", trigger)
		}
		seen[trigger] = true
	}
	if facts.StackDecomposition && strings.TrimSpace(facts.StackRationale) == "" {
		return fmt.Errorf("route stack decomposition requires a rationale")
	}
	return nil
}

// LoadState reads, decodes, and validates a state file from path.
func LoadState(path string) (WorkflowState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return WorkflowState{}, fmt.Errorf("read state %s: %w", path, err)
	}
	var ws WorkflowState
	if err := json.Unmarshal(data, &ws); err != nil {
		return WorkflowState{}, fmt.Errorf("parse state %s: %w", path, err)
	}
	if err := ws.validate(); err != nil {
		return WorkflowState{}, fmt.Errorf("invalid state %s: %w", path, err)
	}
	return ws, nil
}

// marshalState stamps Updated and renders ws as indented JSON with a trailing
// newline.
func marshalState(ws WorkflowState) ([]byte, error) {
	ws.Updated = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode state: %w", err)
	}
	return append(data, '\n'), nil
}

// SaveState writes ws to path atomically (unique temp file + rename), stamping
// Updated, so a reader never sees a partial write and concurrent writers never
// share a temp file.
func SaveState(path string, ws WorkflowState) error {
	if err := ws.validate(); err != nil {
		return fmt.Errorf("invalid state %s: %w", path, err)
	}
	data, err := marshalState(ws)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// CreateState writes a new state file, failing if one already exists, so init
// cannot clobber an in-progress run (no check-then-write race). The returned
// error satisfies errors.Is(err, fs.ErrExist) when the file is already present.
func CreateState(path string, ws WorkflowState) error {
	data, err := marshalState(ws)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Next returns the first phase in Order that is not yet done, or "" when every
// phase is done. A resume-first skill calls this to learn what to do next: an
// interrupted in-progress phase is returned so the run continues it.
func (ws WorkflowState) Next() string {
	for _, p := range ws.Order {
		if !terminalPhaseStatus(ws.Phases[p].Status) {
			return p
		}
	}
	return ""
}

// Current returns the phase the run is on: the in-progress phase if one exists,
// otherwise the first not-done phase, otherwise "" when all phases are done.
func (ws WorkflowState) Current() string {
	for _, p := range ws.Order {
		if ws.Phases[p].Status == PhaseInProgress {
			return p
		}
	}
	for _, p := range ws.Order {
		if ws.Phases[p].Status == PhaseBlocked || ws.Phases[p].Status == PhaseEscalated {
			return p
		}
	}
	return ws.Next()
}

// After returns the phase that follows the given phase in Order, or "" when
// the phase is last or unknown.
func (ws WorkflowState) After(phase string) string {
	for i, p := range ws.Order {
		if p == phase && i+1 < len(ws.Order) {
			return ws.Order[i+1]
		}
	}
	return ""
}

// SetPhase updates one phase's status and, when non-empty, its artifact/task.
// It errors on an unknown phase or invalid status. Any status-to-status move is
// allowed, including a backward one (e.g. done -> in-progress), so a skill can
// deliberately re-open a phase to redo it during a fix or resume.
func (ws *WorkflowState) SetPhase(name, status, artifact, task string) error {
	ph, ok := ws.Phases[name]
	if !ok {
		return fmt.Errorf("unknown phase %q", name)
	}
	if !validStatus(status) {
		return fmt.Errorf("invalid status %q (want pending|in-progress|done|skipped|blocked|escalated)", status)
	}
	ph.Status = status
	if artifact != "" {
		ph.Artifact = artifact
	}
	if task != "" {
		ph.Task = task
	}
	if status != PhaseInProgress {
		ph.Dispatch = nil
	}
	ws.Phases[name] = ph
	return nil
}

// SetPhaseWithDelivery updates one phase including its delivery metadata.
func (ws *WorkflowState) SetPhaseWithDelivery(
	name, status, reason, artifact, task, outcome, startedAt, endedAt string,
) error {
	ph, ok := ws.Phases[name]
	if !ok {
		return fmt.Errorf("unknown phase %q", name)
	}
	if !validStatus(status) {
		return fmt.Errorf("invalid status %q (want pending|in-progress|done|skipped|blocked|escalated)", status)
	}
	if requiresReason(status) && strings.TrimSpace(reason) == "" {
		return fmt.Errorf("phase %q status %q requires a reason", name, status)
	}
	if outcome != "" && outcome != PhaseOutcomeMaterial && outcome != PhaseOutcomeNoOp {
		return fmt.Errorf("invalid outcome %q (want material|no-op)", outcome)
	}
	if (status == PhasePending || status == PhaseInProgress ||
		status == PhaseBlocked || status == PhaseEscalated) && outcome != "" {
		return fmt.Errorf("phase %q status %q cannot have outcome %q", name, status, outcome)
	}
	if status == PhaseSkipped && outcome != "" && outcome != PhaseOutcomeNoOp {
		return fmt.Errorf("phase %q status %q requires outcome %q", name, status, PhaseOutcomeNoOp)
	}
	if (ph.Status == PhaseBlocked || ph.Status == PhaseEscalated) && status == PhaseDone {
		return fmt.Errorf(
			"phase %q status %q must be reopened before it can be completed",
			name, ph.Status,
		)
	}
	if status == PhasePending || status == PhaseInProgress {
		ph.Reason = ""
		ph.Artifact = ""
		ph.Task = ""
		ph.Outcome = ""
		ph.StartedAt = ""
		ph.EndedAt = ""
	}
	if status == PhaseBlocked || status == PhaseEscalated {
		ph.Outcome = ""
	}
	ph.Status = status
	if reason != "" {
		ph.Reason = reason
	} else if !requiresReason(status) {
		ph.Reason = ""
	}
	if artifact != "" {
		ph.Artifact = artifact
	}
	if task != "" {
		ph.Task = task
	}
	if outcome != "" {
		ph.Outcome = outcome
	}
	if startedAt != "" {
		ph.StartedAt = startedAt
	}
	if endedAt != "" {
		ph.EndedAt = endedAt
	}
	if status != PhaseInProgress {
		ph.Dispatch = nil
	}
	ws.Phases[name] = ph
	return nil
}

func requiresReason(status string) bool {
	return status == PhaseSkipped || status == PhaseBlocked || status == PhaseEscalated
}

func terminalPhaseStatus(status string) bool {
	return status == PhaseDone || status == PhaseSkipped
}

// Advance marks the current phase (the one Current reports) done and returns
// the next not-done phase ("" when the run is now complete). It errors when all
// phases are already done, or when the state is corrupt enough that the current
// phase is unknown, so neither case is silently ignored.
func (ws *WorkflowState) Advance() (string, error) {
	cur := ws.Current()
	if cur == "" {
		return "", fmt.Errorf("all phases already done")
	}
	status := ws.Phases[cur].Status
	if status == PhaseBlocked || status == PhaseEscalated {
		return "", fmt.Errorf(
			"advance %q: unresolved status %q must be reopened before completion",
			cur, status,
		)
	}
	if err := ws.SetPhase(cur, PhaseDone, "", ""); err != nil {
		return "", fmt.Errorf("advance %q: %w", cur, err)
	}
	return ws.Next(), nil
}

// SetPR records the pull request the project produced. A zero number or empty
// URL is ignored so a partial update (number now, URL later) does not clobber
// an already-set field; clearing a recorded PR is intentionally not supported.
func (ws *WorkflowState) SetPR(number int, url string) {
	changed := false
	if number != 0 {
		if ws.PR.Number != number {
			ws.PR.URL = ""
			changed = true
		}
		ws.PR.Number = number
	}
	if url != "" {
		changed = changed || ws.PR.URL != url
		ws.PR.URL = url
	}
	if changed {
		ws.FinalResult = nil
	}
}

// ProgressPath returns the progress.md path for an active project slug.
func ProgressPath(slug string) string {
	return filepath.Join(ActiveDir(), slug, "progress.md")
}

// AppendProgress appends a timestamped line to path, creating it if needed.
// progress.md is the human-readable, append-only audit trail that a restarted
// agent reads to see what already happened.
func AppendProgress(path, msg string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	stamp := time.Now().UTC().Format(time.RFC3339)
	if _, err := fmt.Fprintf(f, "- %s  %s\n", stamp, msg); err != nil {
		return fmt.Errorf("append %s: %w", path, err)
	}
	return nil
}
