package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/project"
	deliveryroute "github.com/ronaknnathani/relay/internal/route"
	"github.com/spf13/cobra"
)

type dispatchOutput struct {
	Phase           string `json:"phase"`
	DispatchID      string `json:"dispatch_id"`
	DispatchToken   string `json:"dispatch_token"`
	ReviewToken     string `json:"review_token,omitempty"`
	ValidationToken string `json:"validation_token,omitempty"`
	RouteToken      string `json:"route_token,omitempty"`
	ResultToken     string `json:"result_token,omitempty"`
	RouteRevision   int    `json:"route_revision,omitempty"`
	RouteDigest     string `json:"route_digest,omitempty"`
}

const (
	dispatchScopeFinish     = "finish"
	dispatchScopeReview     = "evidence-review"
	dispatchScopeValidation = "evidence-validation"
	dispatchScopeRoute      = "route-update"
	dispatchScopeResult     = "result"
)

var encodeDispatchOutput = func(output dispatchOutput) error {
	return json.NewEncoder(os.Stdout).Encode(output)
}

func newCmdStateDispatch() *cobra.Command {
	var inline bool
	var task, owner, coordinatorToken string
	command := &cobra.Command{
		Use:   "dispatch <slug> <phase>",
		Short: "Mark a phase in progress and count its worker dispatch",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return withLockedState(args[0], func(locked *project.WorkflowState, statePath string) error {
				state := *locked
				if err := state.ValidateAdaptiveDispatch(args[1]); err != nil {
					return err
				}
				if state.UsesAdaptiveDelivery() {
					if err := validateCoordinatorToken(state, coordinatorToken); err != nil {
						return err
					}
				}
				if state.UsesAdaptiveDelivery() && args[1] == "route" && !inline {
					return fmt.Errorf("adaptive route phase must be dispatched inline with --inline")
				}
				if state.UsesAdaptiveDelivery() && args[1] == "open-pr" && !inline {
					return fmt.Errorf("adaptive open-pr phase must be dispatched inline with --inline")
				}
				if state.UsesAdaptiveDelivery() && inline &&
					args[1] != "route" && args[1] != "open-pr" {
					return fmt.Errorf("adaptive phase %q cannot be dispatched inline", args[1])
				}
				if state.UsesAdaptiveDelivery() && !inline && strings.TrimSpace(owner) == "" {
					return fmt.Errorf("adaptive worker phase %q requires --owner", args[1])
				}
				if args[1] == "open-pr" {
					if err := requireCleanProjectWorktree(args[0]); err != nil {
						return err
					}
					snapshot, err := projectSnapshot(args[0])
					if err != nil {
						return err
					}
					if err := validateProjectRemoteBaseBinding(args[0], state, snapshot); err != nil {
						return err
					}
					if err := state.ValidateOpenPRReadiness(snapshot, false); err != nil {
						return err
					}
					state.FinalResult = nil
				}
				startedAt := time.Now().UTC().Format(time.RFC3339)
				if err := state.SetPhaseWithDelivery(
					args[1], project.PhaseInProgress, "", "", task, "", startedAt, "",
				); err != nil {
					return err
				}
				if !inline {
					incrementSubagent(&state)
				}
				dispatch, output, err := newPhaseDispatch(args[1], state.Route)
				if err != nil {
					return err
				}
				phase := state.Phases[args[1]]
				phase.Dispatch = dispatch
				state.Phases[args[1]] = phase
				if !inline {
					state.RecordDeliveryDispatch(args[1], dispatch.ID, owner)
				} else {
					state.RecordInlineDispatch(args[1], dispatch.ID)
				}
				state.InvalidateEvidenceForOwner(args[1])
				if err := encodeDispatchOutput(output); err != nil {
					return fmt.Errorf("publish dispatch capability: %w", err)
				}
				if err := project.SaveState(statePath, state); err != nil {
					return err
				}
				return nil
			})
		},
	}
	command.Flags().BoolVar(&inline, "inline", false, "record coordinator work without incrementing the worker count")
	command.Flags().StringVar(&task, "task", "", "free-form progress marker")
	command.Flags().StringVar(&owner, "owner", "", "stable worker identity for handoff telemetry")
	command.Flags().StringVar(
		&coordinatorToken, "coordinator-token", "",
		"trusted coordinator capability returned by adaptive state initialization",
	)
	return command
}

func requireCleanProjectWorktree(slug string) error {
	manifestPath, err := project.Find(slug)
	if err != nil {
		return err
	}
	manifest, err := project.Load(manifestPath)
	if err != nil {
		return err
	}
	if manifest.Worktree == nil || strings.TrimSpace(*manifest.Worktree) == "" {
		return fmt.Errorf("project %q has no worktree", slug)
	}
	clean, err := gitx.WorktreeClean(*manifest.Worktree)
	if err != nil {
		return err
	}
	if !clean {
		return fmt.Errorf("open-pr requires a clean worktree containing only reviewed committed changes")
	}
	return nil
}

func validateProjectRemoteBaseBinding(
	slug string,
	state project.WorkflowState,
	snapshot project.RepositorySnapshot,
) error {
	manifestPath, err := project.FindActive(slug)
	if err != nil {
		return err
	}
	manifest, err := project.Load(manifestPath)
	if err != nil {
		return err
	}
	if manifest.RemoteBaseSHA == "" || manifest.BaseBranch == "" ||
		manifest.RemoteBaseSHA != snapshot.BaseTipSHA ||
		manifest.BaseBranch != snapshot.BaseRef {
		return fmt.Errorf(
			"open-pr requires a coordinator-refreshed immutable remote base binding",
		)
	}
	if state.PendingPR.Number == 0 {
		remoteSHA := gitx.RevParse(
			*manifest.Worktree, "origin/"+manifest.BaseBranch,
		)
		if remoteSHA == "" || remoteSHA != manifest.RemoteBaseSHA {
			return fmt.Errorf(
				"open-pr remote base binding is stale; refresh the route before dispatch",
			)
		}
	}
	return nil
}

func newCmdStateWorker() *cobra.Command {
	var task, coordinatorToken string
	command := &cobra.Command{
		Use:   "worker <slug>",
		Short: "Count a delivery subagent that is not a phase dispatch",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return withLockedState(args[0], func(locked *project.WorkflowState, statePath string) error {
				state := *locked
				if strings.TrimSpace(task) == "" {
					return fmt.Errorf("worker count requires --task")
				}
				if state.UsesAdaptiveDelivery() && state.Route == nil {
					return fmt.Errorf("adaptive delivery must classify a route before dispatching helpers")
				}
				if state.UsesAdaptiveDelivery() {
					if err := validateCoordinatorToken(state, coordinatorToken); err != nil {
						return err
					}
				}
				if state.Route != nil && state.Route.Class == project.RouteEasy &&
					!state.Route.ForcedFull {
					return fmt.Errorf("easy route cannot dispatch off-route helpers")
				}
				incrementSubagent(&state)
				return project.SaveState(statePath, state)
			})
		},
	}
	command.Flags().StringVar(&task, "task", "", "subagent purpose")
	command.Flags().StringVar(
		&coordinatorToken, "coordinator-token", "",
		"trusted coordinator capability returned by adaptive state initialization",
	)
	return command
}

func newCmdStateGrant() *cobra.Command {
	var coordinatorToken string
	command := &cobra.Command{
		Use:   "grant <slug> stack-advance",
		Short: "Issue a one-time scoped workflow capability",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			if args[1] != "stack-advance" {
				return fmt.Errorf("unknown capability %q (want stack-advance)", args[1])
			}
			return withLockedState(args[0], func(locked *project.WorkflowState, statePath string) error {
				state := *locked
				if err := validateCoordinatorToken(state, coordinatorToken); err != nil {
					return err
				}
				if !state.UsesAdaptiveDelivery() || state.Route == nil {
					return fmt.Errorf("stack advance capability requires an adaptive routed project")
				}
				if state.PR.Number <= 0 || state.Phases["open-pr"].Status != project.PhaseDone {
					return fmt.Errorf("stack advance capability requires a completed open-pr phase")
				}
				token, err := randomToken(32)
				if err != nil {
					return fmt.Errorf("generate stack advance capability: %w", err)
				}
				sum := sha256.Sum256([]byte(token))
				state.StackAdvanceHash = fmt.Sprintf("%x", sum)
				if err := project.SaveState(statePath, state); err != nil {
					return err
				}
				return json.NewEncoder(os.Stdout).Encode(struct {
					Capability string `json:"capability"`
				}{Capability: token})
			})
		},
	}
	command.Flags().StringVar(
		&coordinatorToken, "coordinator-token", "",
		"trusted coordinator capability returned by adaptive state initialization",
	)
	return command
}

func incrementSubagent(state *project.WorkflowState) {
	state.SubagentCount++
}

func newPhaseDispatch(phase string, route *project.RouteDecision) (*project.PhaseDispatch, dispatchOutput, error) {
	id, err := randomToken(16)
	if err != nil {
		return nil, dispatchOutput{}, fmt.Errorf("generate dispatch id: %w", err)
	}
	dispatch := &project.PhaseDispatch{
		ID: id, TokenHashes: make(map[string]string),
	}
	output := dispatchOutput{Phase: phase, DispatchID: id}
	addCapability := func(scope string, target *string) error {
		token, err := randomToken(32)
		if err != nil {
			return fmt.Errorf("generate %s dispatch token: %w", scope, err)
		}
		sum := sha256.Sum256([]byte(token))
		dispatch.TokenHashes[scope] = fmt.Sprintf("%x", sum)
		*target = token
		return nil
	}
	if phase == "open-pr" {
		if err := addCapability(dispatchScopeResult, &output.ResultToken); err != nil {
			return nil, dispatchOutput{}, err
		}
		output.DispatchToken = output.ResultToken
	} else if err := addCapability(dispatchScopeFinish, &output.DispatchToken); err != nil {
		return nil, dispatchOutput{}, err
	}
	if route != nil {
		dispatch.RouteRevision = route.Revision
		dispatch.RouteDigest = route.Digest
		if phase == route.EffectiveReviewOwner() {
			if err := addCapability(dispatchScopeReview, &output.ReviewToken); err != nil {
				return nil, dispatchOutput{}, err
			}
		}
		if phase == route.ValidationOwner {
			if err := addCapability(dispatchScopeValidation, &output.ValidationToken); err != nil {
				return nil, dispatchOutput{}, err
			}
		}
		if phase != "open-pr" {
			if err := addCapability(dispatchScopeRoute, &output.RouteToken); err != nil {
				return nil, dispatchOutput{}, err
			}
		}
	}
	output.RouteRevision = dispatch.RouteRevision
	output.RouteDigest = dispatch.RouteDigest
	return dispatch, output, nil
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func newCmdStateFinish() *cobra.Command {
	var reason, artifact, outcome, task, dispatchToken string
	command := &cobra.Command{
		Use:   "finish <slug> <phase> <status>",
		Short: "Record a phase result and completion metadata",
		Args:  cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, args []string) error {
			status := args[2]
			switch status {
			case project.PhaseDone, project.PhaseSkipped:
				if outcome == "" {
					return fmt.Errorf("phase %q status %q requires --outcome material|no-op", args[1], status)
				}
			case project.PhaseBlocked, project.PhaseEscalated:
			default:
				return fmt.Errorf("finish status %q must be done, skipped, blocked, or escalated", status)
			}
			return withLockedState(args[0], func(state *project.WorkflowState, statePath string) error {
				if err := state.ValidateAdaptiveFinish(args[1], status); err != nil {
					return err
				}
				if state.UsesAdaptiveDelivery() && args[1] == "open-pr" {
					return fmt.Errorf("adaptive open-pr must complete with state pr or state final")
				}
				if state.UsesAdaptiveDelivery() {
					if err := consumePhaseDispatchToken(
						state, args[1], dispatchScopeFinish, dispatchToken,
					); err != nil {
						return err
					}
				}
				if err := state.SetPhaseWithDelivery(
					args[1], status, reason, artifact, task, outcome, "",
					time.Now().UTC().Format(time.RFC3339),
				); err != nil {
					return err
				}
				return project.SaveState(statePath, *state)
			})
		},
	}
	command.Flags().StringVar(&reason, "reason", "", "required reason for skipped, blocked, or escalated status")
	command.Flags().StringVar(&artifact, "artifact", "", "artifact the phase produced")
	command.Flags().StringVar(&outcome, "outcome", "", "material or no-op")
	command.Flags().StringVar(&task, "task", "", "free-form progress marker")
	command.Flags().StringVar(&dispatchToken, "dispatch-token", "", "token returned by state dispatch")
	return command
}

func validateAdaptiveOpenPRSuccess(slug string, state project.WorkflowState) error {
	if !state.UsesAdaptiveDelivery() {
		return nil
	}
	snapshot, err := projectSnapshot(slug)
	if err != nil {
		return err
	}
	return state.ValidateOpenPRReadiness(snapshot, true)
}

func newCmdStateEvidence() *cobra.Command {
	command := &cobra.Command{
		Use:   "evidence",
		Short: "Record and check snapshot-bound delivery evidence",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(newCmdStateEvidenceRecord(), newCmdStateEvidenceFresh())
	return command
}

func newCmdStateEvidenceRecord() *cobra.Command {
	var result, artifact, blockerCategory, blockerReason, gateFile string
	var roles []string
	var exitStatuses []int
	var critical, important, suggestion int
	var noGates bool
	var dispatchToken string
	command := &cobra.Command{
		Use:   "record <slug> <review|validation>",
		Short: "Record normalized evidence for the current repository snapshot",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			kind := args[1]
			if kind != "review" && kind != "validation" {
				return fmt.Errorf("unknown evidence kind %q (want review|validation)", kind)
			}
			if result != project.EvidencePassed && result != project.EvidenceFailed && result != project.EvidenceBlocked {
				return fmt.Errorf("invalid evidence result %q (want passed|failed|blocked)", result)
			}
			if result == project.EvidenceBlocked {
				if strings.TrimSpace(blockerCategory) == "" || strings.TrimSpace(blockerReason) == "" {
					return fmt.Errorf("blocked evidence requires --blocker-category and --blocker-reason")
				}
			} else if blockerCategory != "" || blockerReason != "" {
				return fmt.Errorf("blocker metadata is only valid for blocked evidence")
			}
			var definitions []gateDefinition
			if strings.TrimSpace(gateFile) != "" {
				var err error
				definitions, err = loadGateDefinitions(gateFile)
				if err != nil {
					return err
				}
			}
			if len(definitions) != len(exitStatuses) {
				return fmt.Errorf("--gate-file entries and --exit-status counts must match")
			}
			if kind == "validation" && result != project.EvidenceBlocked &&
				len(definitions) == 0 && !noGates {
				return fmt.Errorf("validation evidence requires --gate-file with matching --exit-status values or --no-gates")
			}
			if kind == "review" && result != project.EvidenceBlocked && len(roles) == 0 {
				return fmt.Errorf("review evidence requires at least one --role")
			}
			return withLockedState(args[0], func(state *project.WorkflowState, statePath string) error {
				snapshot, err := projectSnapshot(args[0])
				if err != nil {
					return err
				}
				owner, dispatch, err := validateEvidenceDispatch(
					state, kind, snapshot, dispatchToken,
				)
				if err != nil {
					return err
				}
				commandEvidence := make([]project.CommandEvidence, len(definitions))
				for i := range definitions {
					evidence, err := commandEvidenceForGate(definitions[i], exitStatuses[i])
					if err != nil {
						return err
					}
					commandEvidence[i] = evidence
				}
				result = evidenceResultWithFailurePrecedence(
					kind,
					result,
					commandEvidence,
					project.FindingCounts{
						Critical: critical, Important: important, Suggestion: suggestion,
					},
				)
				record := &project.EvidenceRecord{
					Snapshot: snapshot, RouteRevision: state.Route.Revision,
					RouteDigest: state.Route.Digest, DispatchID: dispatch.ID,
					Result: result, Owner: owner, Artifact: artifact,
					BlockerCategory: blockerCategory, BlockerReason: blockerReason,
					CompletedAt: time.Now().UTC().Format(time.RFC3339),
					Commands:    commandEvidence, NoGates: noGates, Roles: append([]string(nil), roles...),
					Findings: project.FindingCounts{
						Critical: critical, Important: important, Suggestion: suggestion,
					},
				}
				requiredRoles := []string(nil)
				if kind == project.EvidenceKindReview {
					requiredRoles = state.Route.ReviewRoles
				}
				if kind == project.EvidenceKindValidation {
					if err := project.ValidateValidationEvidence(*record, state.Route.Facts.GatePolicy); err != nil {
						return err
					}
				} else if err := project.ValidateEvidence(kind, *record, requiredRoles); err != nil {
					return err
				}
				if kind == "review" {
					state.Evidence.Review = record
				} else {
					state.Evidence.Validation = record
				}
				if record.Result == project.EvidenceBlocked {
					reason := fmt.Sprintf(
						"%s evidence blocked (%s): %s",
						kind, strings.TrimSpace(blockerCategory), strings.TrimSpace(blockerReason),
					)
					if err := state.SetPhaseWithDelivery(
						owner, project.PhaseBlocked, reason, artifact, "", "", "", record.CompletedAt,
					); err != nil {
						return fmt.Errorf("block %s evidence owner %q: %w", kind, owner, err)
					}
					if err := project.SaveState(statePath, *state); err != nil {
						return err
					}
					return json.NewEncoder(os.Stdout).Encode(record)
				}
				phase, reason, routeError, reopenError := "", "", "", ""
				if kind == "review" {
					if record.Result == project.EvidenceFailed || critical > 0 || important > 0 {
						phase = "review"
						reason = "review found Critical or Important issues"
						routeError = "route failed review"
						reopenError = "reopen implementation after failed review"
					}
				} else {
					if record.Result == project.EvidenceFailed {
						phase = "validate"
						reason = "validation evidence " + result
						routeError = "route failed validation"
						reopenError = "reopen implementation after failed validation"
					}
				}
				if reason != "" {
					trigger := deliveryroute.RiskUnresolvedReviewCI
					if kind == project.EvidenceKindValidation {
						trigger = deliveryroute.RiskFailedGate
					}
					if state.Route != nil {
						facts := state.Route.Facts
						if !slices.Contains(facts.RiskTriggers, trigger) {
							facts.RiskTriggers = append(facts.RiskTriggers, trigger)
						}
						decision, err := deliveryroute.Classify(facts)
						if err != nil {
							return fmt.Errorf("%s: %w", routeError, err)
						}
						decision.Snapshot = snapshot
						decision, err = deliveryroute.PreserveMonotonic(*state.Route, decision, reason)
						if err != nil {
							return fmt.Errorf("%s: %w", routeError, err)
						}
						if err := state.ApplyRoute(decision); err != nil {
							return fmt.Errorf("%s: %w", routeError, err)
						}
					}
					if err := state.SetPhaseWithDelivery(
						phase, project.PhaseEscalated, reason, artifact, "", "", "", record.CompletedAt,
					); err != nil {
						return fmt.Errorf("%s: %w", routeError, err)
					}
					if err := state.SetPhaseWithDelivery(
						"implement", project.PhaseEscalated, reason, "", artifact, "", "", "",
					); err != nil {
						return fmt.Errorf("%s: %w", reopenError, err)
					}
					staleReason := "must rerun after implementation changes"
					other := "review"
					if kind == project.EvidenceKindReview {
						other = "validate"
					}
					if otherPhase, ok := state.Phases[other]; ok && otherPhase.Status == project.PhaseDone {
						if err := state.SetPhaseWithDelivery(
							other, project.PhaseEscalated, staleReason, "", artifact, "", "", "",
						); err != nil {
							return fmt.Errorf("reopen %s after failed %s: %w", other, kind, err)
						}
					}
				}
				if err := project.SaveState(statePath, *state); err != nil {
					return err
				}
				return json.NewEncoder(os.Stdout).Encode(record)
			})
		},
	}
	command.Flags().StringVar(&result, "result", "", "passed, failed, or blocked")
	command.Flags().StringVar(&artifact, "artifact", "", "evidence artifact path")
	command.Flags().StringVar(&blockerCategory, "blocker-category", "", "blocked evidence category")
	command.Flags().StringVar(&blockerReason, "blocker-reason", "", "blocked evidence reason")
	command.Flags().StringVar(&gateFile, "gate-file", "", "JSON file containing validation gate argv")
	command.Flags().IntSliceVar(&exitStatuses, "exit-status", nil, "matching validation command exit status")
	command.Flags().StringArrayVar(&roles, "role", nil, "review role (repeatable)")
	command.Flags().IntVar(&critical, "critical", 0, "critical finding count")
	command.Flags().IntVar(&important, "important", 0, "important finding count")
	command.Flags().IntVar(&suggestion, "suggestion", 0, "suggestion finding count")
	command.Flags().BoolVar(&noGates, "no-gates", false, "the repository defines no validation commands")
	command.Flags().StringVar(&dispatchToken, "dispatch-token", "", "token returned by state dispatch for the owning phase")
	_ = command.MarkFlagRequired("result")
	_ = command.MarkFlagRequired("dispatch-token")
	return command
}

func evidenceResultWithFailurePrecedence(
	kind string,
	result string,
	commands []project.CommandEvidence,
	findings project.FindingCounts,
) string {
	if result != project.EvidenceBlocked {
		return result
	}
	if kind == project.EvidenceKindReview &&
		(findings.Critical > 0 || findings.Important > 0) {
		return project.EvidenceFailed
	}
	if kind == project.EvidenceKindValidation {
		for _, command := range commands {
			if command.ExitStatus != 0 {
				return project.EvidenceFailed
			}
		}
	}
	return result
}

func validateEvidenceDispatch(
	state *project.WorkflowState,
	kind string,
	snapshot project.RepositorySnapshot,
	token string,
) (string, *project.PhaseDispatch, error) {
	if state.Route == nil || state.Route.Revision <= 0 || state.Route.Digest == "" {
		return "", nil, fmt.Errorf("evidence requires a current routed workflow")
	}
	if state.Route.Snapshot != snapshot {
		return "", nil, fmt.Errorf("route snapshot is stale; refresh or reclassify before recording evidence")
	}
	owner := state.Route.ValidationOwner
	if kind == project.EvidenceKindReview {
		owner = state.Route.EffectiveReviewOwner()
	}
	phase, ok := state.Phases[owner]
	if !ok || phase.Status != project.PhaseInProgress || phase.Dispatch == nil {
		return "", nil, fmt.Errorf("canonical evidence owner %q is not actively dispatched", owner)
	}
	dispatch := phase.Dispatch
	if dispatch.RouteRevision != state.Route.Revision || dispatch.RouteDigest != state.Route.Digest {
		return "", nil, fmt.Errorf("dispatch for %q is stale for the current route revision", owner)
	}
	scope := dispatchScopeValidation
	if kind == project.EvidenceKindReview {
		scope = dispatchScopeReview
	}
	if err := consumeDispatchCapability(dispatch, scope, token); err != nil {
		return "", nil, fmt.Errorf("canonical evidence owner %q: %w", owner, err)
	}
	return owner, dispatch, nil
}

func consumePhaseDispatchToken(
	state *project.WorkflowState,
	phaseName string,
	scope string,
	token string,
) error {
	if err := validatePhaseDispatchToken(*state, phaseName, scope, token); err != nil {
		return err
	}
	phase := state.Phases[phaseName]
	dispatch := phase.Dispatch
	if err := consumeDispatchCapability(dispatch, scope, token); err != nil {
		return fmt.Errorf("canonical phase owner %q: %w", phaseName, err)
	}
	phase.Dispatch = dispatch
	state.Phases[phaseName] = phase
	return nil
}

func validatePhaseDispatchToken(
	state project.WorkflowState,
	phaseName string,
	scope string,
	token string,
) error {
	phase, ok := state.Phases[phaseName]
	if !ok || phase.Status != project.PhaseInProgress || phase.Dispatch == nil {
		return fmt.Errorf("canonical phase owner %q is not actively dispatched", phaseName)
	}
	if state.Route == nil ||
		phase.Dispatch.RouteRevision != state.Route.Revision ||
		phase.Dispatch.RouteDigest != state.Route.Digest {
		return fmt.Errorf("dispatch for canonical phase owner %q is stale", phaseName)
	}
	if err := validateDispatchCapability(phase.Dispatch, scope, token); err != nil {
		return fmt.Errorf("canonical phase owner %q: %w", phaseName, err)
	}
	return nil
}

func consumeDispatchCapability(dispatch *project.PhaseDispatch, scope, token string) error {
	if err := validateDispatchCapability(dispatch, scope, token); err != nil {
		return err
	}
	if dispatch.TokenHashes != nil {
		delete(dispatch.TokenHashes, scope)
	}
	if scope == dispatchScopeFinish {
		dispatch.TokenHash = ""
	}
	return nil
}

func validateDispatchCapability(dispatch *project.PhaseDispatch, scope, token string) error {
	hash := dispatch.TokenHashes[scope]
	if hash == "" && scope == dispatchScopeFinish {
		hash = dispatch.TokenHash
	}
	if hash == "" {
		return fmt.Errorf("dispatch capability %q is missing or already used", scope)
	}
	sum := sha256.Sum256([]byte(token))
	expected, err := decodeSHA256(hash)
	if err != nil {
		return fmt.Errorf("invalid stored dispatch token hash: %w", err)
	}
	if subtle.ConstantTimeCompare(sum[:], expected) != 1 {
		return fmt.Errorf("invalid dispatch token for scope %q", scope)
	}
	return nil
}

func validateCoordinatorToken(state project.WorkflowState, token string) error {
	if state.CoordinatorHash == "" {
		return fmt.Errorf(
			"adaptive state has no trusted coordinator capability; resume it through Relay before dispatch",
		)
	}
	sum := sha256.Sum256([]byte(token))
	expected, err := decodeSHA256(state.CoordinatorHash)
	if err != nil {
		return fmt.Errorf("invalid stored coordinator token hash: %w", err)
	}
	if subtle.ConstantTimeCompare(sum[:], expected) != 1 {
		return fmt.Errorf("invalid coordinator capability")
	}
	return removeConsumedCoordinatorHandoff(state.Slug, token)
}

func removeConsumedCoordinatorHandoff(slug, token string) error {
	path := filepath.Join(project.ActiveDir(), slug, ".coordinator-capability")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read coordinator capability handoff %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) != token {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove consumed coordinator capability handoff %s: %w", path, err)
	}
	return nil
}

func decodeSHA256(value string) ([]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode SHA-256: %w", err)
	}
	if len(decoded) != sha256.Size {
		return nil, fmt.Errorf("decode SHA-256: got %d bytes", len(decoded))
	}
	return decoded, nil
}

func newCmdStateEvidenceFresh() *cobra.Command {
	return &cobra.Command{
		Use:   "fresh <slug> <review|validation>",
		Short: "Require passing evidence for the current repository snapshot",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			state, err := loadState(args[0])
			if err != nil {
				return err
			}
			var record *project.EvidenceRecord
			switch args[1] {
			case "review":
				record = state.Evidence.Review
			case "validation":
				record = state.Evidence.Validation
			default:
				return fmt.Errorf("unknown evidence kind %q (want review|validation)", args[1])
			}
			if record == nil {
				return fmt.Errorf("%s evidence is missing", args[1])
			}
			if record.Result != project.EvidencePassed {
				return fmt.Errorf("%s evidence result is %s", args[1], record.Result)
			}
			snapshot, err := projectSnapshot(args[0])
			if err != nil {
				return err
			}
			if !record.Fresh(snapshot) {
				return fmt.Errorf("%s evidence is stale for the current repository snapshot", args[1])
			}
			if state.Route != nil {
				owner := state.Route.ValidationOwner
				if args[1] == project.EvidenceKindReview {
					owner = state.Route.EffectiveReviewOwner()
					if !record.FreshForReviewRoute(snapshot, *state.Route, state.Route.ReviewRoles, owner) {
						return fmt.Errorf("review evidence does not cover the current route's owner and selected roles")
					}
				} else if !record.FreshForValidationRoute(snapshot, *state.Route, owner) {
					return fmt.Errorf("validation evidence does not cover the current route's owner and exact gate policy")
				}
			}
			fmt.Println("fresh")
			return nil
		},
	}
}

func projectSnapshot(slug string) (project.RepositorySnapshot, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return project.RepositorySnapshot{}, err
	}
	path, err := project.Find(slug)
	if err != nil {
		return project.RepositorySnapshot{}, err
	}
	manifest, err := project.Load(path)
	if err != nil {
		return project.RepositorySnapshot{}, err
	}
	return project.RepositorySnapshotForManifest(manifest, filepath.Dir(path))
}
