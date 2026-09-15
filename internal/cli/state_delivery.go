package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

const easySubagentLimit = 3

type dispatchOutput struct {
	Phase         string `json:"phase"`
	DispatchID    string `json:"dispatch_id"`
	DispatchToken string `json:"dispatch_token"`
	RouteRevision int    `json:"route_revision,omitempty"`
	RouteDigest   string `json:"route_digest,omitempty"`
}

func newCmdStateDispatch() *cobra.Command {
	var inline bool
	var task string
	command := &cobra.Command{
		Use:   "dispatch <slug> <phase>",
		Short: "Mark a phase in progress and count its worker dispatch",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			state, statePath, err := loadStateAt(args[0])
			if err != nil {
				return err
			}
			if err := state.ValidateAdaptiveDispatch(args[1]); err != nil {
				return err
			}
			if args[1] == "open-pr" {
				snapshot, err := projectSnapshot(args[0])
				if err != nil {
					return err
				}
				if err := state.ValidateOpenPRReadiness(snapshot, false); err != nil {
					return err
				}
			}
			startedAt := time.Now().UTC().Format(time.RFC3339)
			if err := state.SetPhaseWithDelivery(
				args[1], project.PhaseInProgress, "", "", task, "", startedAt, "",
			); err != nil {
				return err
			}
			if !inline {
				if err := incrementSubagent(&state); err != nil {
					return err
				}
			}
			dispatch, output, err := newPhaseDispatch(args[1], state.Route)
			if err != nil {
				return err
			}
			phase := state.Phases[args[1]]
			phase.Dispatch = dispatch
			state.Phases[args[1]] = phase
			state.RecordDeliveryDispatch(args[1])
			state.InvalidateEvidenceForOwner(args[1])
			if err := project.SaveState(statePath, state); err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(output)
		},
	}
	command.Flags().BoolVar(&inline, "inline", false, "record coordinator work without incrementing the worker count")
	command.Flags().StringVar(&task, "task", "", "free-form progress marker")
	return command
}

func newCmdStateWorker() *cobra.Command {
	var task string
	command := &cobra.Command{
		Use:   "worker <slug>",
		Short: "Count a delivery subagent that is not a phase dispatch",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			state, statePath, err := loadStateAt(args[0])
			if err != nil {
				return err
			}
			if strings.TrimSpace(task) == "" {
				return fmt.Errorf("worker count requires --task")
			}
			if err := incrementSubagent(&state); err != nil {
				return err
			}
			return project.SaveState(statePath, state)
		},
	}
	command.Flags().StringVar(&task, "task", "", "subagent purpose")
	return command
}

func incrementSubagent(state *project.WorkflowState) error {
	if state.Route != nil && state.Route.Class == project.RouteEasy &&
		!state.Route.ForcedFull && state.SubagentCount >= easySubagentLimit {
		return fmt.Errorf("easy route cannot exceed %d subagents", easySubagentLimit)
	}
	state.SubagentCount++
	return nil
}

func newPhaseDispatch(phase string, route *project.RouteDecision) (*project.PhaseDispatch, dispatchOutput, error) {
	id, err := randomToken(16)
	if err != nil {
		return nil, dispatchOutput{}, fmt.Errorf("generate dispatch id: %w", err)
	}
	token, err := randomToken(32)
	if err != nil {
		return nil, dispatchOutput{}, fmt.Errorf("generate dispatch token: %w", err)
	}
	sum := sha256.Sum256([]byte(token))
	dispatch := &project.PhaseDispatch{
		ID: id, TokenHash: fmt.Sprintf("%x", sum),
	}
	if route != nil {
		dispatch.RouteRevision = route.Revision
		dispatch.RouteDigest = route.Digest
	}
	return dispatch, dispatchOutput{
		Phase: phase, DispatchID: id, DispatchToken: token,
		RouteRevision: dispatch.RouteRevision, RouteDigest: dispatch.RouteDigest,
	}, nil
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func newCmdStateFinish() *cobra.Command {
	var reason, artifact, outcome, task string
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
			state, statePath, err := loadStateAt(args[0])
			if err != nil {
				return err
			}
			if err := state.ValidateAdaptiveFinish(args[1], status); err != nil {
				return err
			}
			if args[1] == "open-pr" && status == project.PhaseDone {
				if err := validateAdaptiveOpenPRCompletion(args[0], state); err != nil {
					return err
				}
			}
			if err := state.SetPhaseWithDelivery(
				args[1], status, reason, artifact, task, outcome, "",
				time.Now().UTC().Format(time.RFC3339),
			); err != nil {
				return err
			}
			return project.SaveState(statePath, state)
		},
	}
	command.Flags().StringVar(&reason, "reason", "", "required reason for skipped, blocked, or escalated status")
	command.Flags().StringVar(&artifact, "artifact", "", "artifact the phase produced")
	command.Flags().StringVar(&outcome, "outcome", "", "material or no-op")
	command.Flags().StringVar(&task, "task", "", "free-form progress marker")
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

func validateAdaptiveOpenPRCompletion(slug string, state project.WorkflowState) error {
	if err := validateAdaptiveOpenPRSuccess(slug, state); err != nil {
		return err
	}
	if !state.UsesAdaptiveDelivery() {
		return nil
	}
	if state.FinalResult == nil || state.FinalResult.Status != "opened" ||
		state.PR.Number <= 0 || state.PR.URL == "" ||
		state.FinalResult.PRNumber != state.PR.Number ||
		state.FinalResult.PRURL != state.PR.URL {
		return fmt.Errorf("open-pr completion requires one recorded opened PR result")
	}
	return nil
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
	var result, artifact, blockerCategory, blockerReason string
	var gates, roles []string
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
			if len(gates) != len(exitStatuses) {
				return fmt.Errorf("--gate and --exit-status counts must match")
			}
			if kind == "validation" && result != project.EvidenceBlocked &&
				len(gates) == 0 && !noGates {
				return fmt.Errorf("validation evidence requires at least one --gate and --exit-status or --no-gates")
			}
			if kind == "review" && len(roles) == 0 {
				return fmt.Errorf("review evidence requires at least one --role")
			}
			state, statePath, err := loadStateAt(args[0])
			if err != nil {
				return err
			}
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
			commandEvidence := make([]project.CommandEvidence, len(gates))
			for i := range gates {
				id, commandText, ok := strings.Cut(gates[i], "=")
				if !ok || strings.TrimSpace(id) == "" || strings.TrimSpace(commandText) == "" {
					return fmt.Errorf("--gate must use id=command")
				}
				commandEvidence[i] = redactCommand(strings.TrimSpace(id), commandText, exitStatuses[i])
			}
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
			if result == project.EvidenceBlocked {
				reason := fmt.Sprintf(
					"%s evidence blocked (%s): %s",
					kind, strings.TrimSpace(blockerCategory), strings.TrimSpace(blockerReason),
				)
				if err := state.SetPhaseWithDelivery(
					owner, project.PhaseBlocked, reason, artifact, "", "", "", record.CompletedAt,
				); err != nil {
					return fmt.Errorf("block %s evidence owner %q: %w", kind, owner, err)
				}
				if err := project.SaveState(statePath, state); err != nil {
					return err
				}
				return json.NewEncoder(os.Stdout).Encode(record)
			}
			phase, reason, routeError, reopenError := "", "", "", ""
			if kind == "review" {
				if result == project.EvidenceFailed || critical > 0 || important > 0 {
					phase = "review"
					reason = "review found Critical or Important issues"
					routeError = "route failed review"
					reopenError = "reopen implementation after failed review"
				}
			} else {
				if result == project.EvidenceFailed {
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
					priorPhases := make(map[string]project.PhaseState, len(state.Phases))
					for name, phaseState := range state.Phases {
						priorPhases[name] = phaseState
					}
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
					for _, name := range state.Order {
						if name == "implement" {
							break
						}
						if prior := priorPhases[name]; prior.Status == project.PhaseSkipped {
							state.Phases[name] = prior
						}
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
			if err := project.SaveState(statePath, state); err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(record)
		},
	}
	command.Flags().StringVar(&result, "result", "", "passed, failed, or blocked")
	command.Flags().StringVar(&artifact, "artifact", "", "evidence artifact path")
	command.Flags().StringVar(&blockerCategory, "blocker-category", "", "blocked evidence category")
	command.Flags().StringVar(&blockerReason, "blocker-reason", "", "blocked evidence reason")
	command.Flags().StringArrayVar(&gates, "gate", nil, "validation gate as id=command (repeatable)")
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

func validateEvidenceDispatch(
	state project.WorkflowState,
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
	sum := sha256.Sum256([]byte(token))
	expected, err := decodeSHA256(dispatch.TokenHash)
	if err != nil {
		return "", nil, fmt.Errorf("invalid stored dispatch token hash: %w", err)
	}
	if subtle.ConstantTimeCompare(sum[:], expected) != 1 {
		return "", nil, fmt.Errorf("invalid dispatch token for canonical evidence owner %q", owner)
	}
	return owner, dispatch, nil
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

func redactCommand(gateID, command string, exitStatus int) project.CommandEvidence {
	sum := sha256.Sum256([]byte(command))
	fields := strings.Fields(command)
	display := "<redacted-command>"
	for _, field := range fields {
		if strings.Contains(field, "=") {
			continue
		}
		display = filepath.Base(field)
		if len(fields) > 1 {
			display += " <redacted-args>"
		}
		break
	}
	return project.CommandEvidence{
		GateID: gateID, Display: display, Digest: fmt.Sprintf("%x", sum), ExitStatus: exitStatus,
	}
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
	if manifest.Worktree == nil || strings.TrimSpace(*manifest.Worktree) == "" {
		return project.RepositorySnapshot{}, fmt.Errorf("project %q has no worktree", slug)
	}
	base := manifest.StartSHA
	if base == "" {
		base = manifest.BaseBranch
	}
	snapshot, err := gitx.Snapshot(*manifest.Worktree, base)
	if err != nil {
		return project.RepositorySnapshot{}, fmt.Errorf("snapshot project %q: %w", slug, err)
	}
	return project.RepositorySnapshot{
		BaseSHA: snapshot.BaseSHA, HeadSHA: snapshot.HeadSHA, Fingerprint: snapshot.Fingerprint,
		FileCount: snapshot.FileCount, ChangedLines: snapshot.ChangedLines,
	}, nil
}
