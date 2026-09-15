package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ronaknnathani/relay/internal/project"
	deliveryroute "github.com/ronaknnathani/relay/internal/route"
	"github.com/spf13/cobra"
)

type routeFlags struct {
	requestedExplicit  bool
	unresolved         bool
	gates              []string
	noRepositoryGates  bool
	risksEvaluated     bool
	predictedSizeKnown bool
	predictedFiles     int
	predictedLines     int
	stack              bool
	stackRationale     string
	full               bool
	simplify           bool
	duplication        bool
	generatedChurn     bool
	reviewCleanup      bool
	changesTests       bool
	changesDocs        bool
	changesTypeDesign  bool
	historySensitive   bool
	changesGuidelines  bool
	risks              []string
}

func newCmdRoute() *cobra.Command {
	command := &cobra.Command{
		Use:   "route",
		Short: "Classify and refresh adaptive delivery routes",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(
		newCmdRouteSnapshot(), newCmdRouteClassify(), newCmdRouteRefresh(), newCmdRouteEscalate(),
	)
	return command
}

func newCmdRouteSnapshot() *cobra.Command {
	return &cobra.Command{
		Use:   "snapshot <slug>",
		Short: "Print the current repository snapshot without requiring a route",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			snapshot, err := projectSnapshot(args[0])
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(snapshot)
		},
	}
}

func newCmdRouteClassify() *cobra.Command {
	var flags routeFlags
	command := &cobra.Command{
		Use:   "classify <slug>",
		Short: "Classify normalized task facts and persist the route",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			state, statePath, err := loadStateAt(args[0])
			if err != nil {
				return err
			}
			snapshot, err := projectSnapshot(args[0])
			if err != nil {
				return err
			}
			manifestPath, err := project.Find(args[0])
			if err != nil {
				return err
			}
			manifest, err := project.Load(manifestPath)
			if err != nil {
				return err
			}
			flags.full = flags.full || manifest.DeliveryMode == project.DeliveryModeFull
			facts := flags.input(snapshot)
			decision, err := deliveryroute.Classify(facts)
			if err != nil {
				return err
			}
			decision.Snapshot = snapshot
			decision.Facts = facts
			if state.Route != nil {
				reason := routeTransitionReason(*state.Route, decision)
				decision, err = deliveryroute.PreserveMonotonic(
					*state.Route, decision, reason,
				)
				if err != nil {
					return err
				}
				decision.Snapshot = snapshot
			}
			if err := state.ApplyRoute(decision); err != nil {
				return err
			}
			return saveRouteDecision(statePath, state, decision)
		},
	}
	bindRouteFlags(command, &flags)
	return command
}

func routeTransitionReason(previous, next project.RouteDecision) string {
	if project.RouteRank(next.Class) > project.RouteRank(previous.Class) {
		return "current repository facts require a more conservative route"
	}
	if project.RouteRank(previous.Class) > project.RouteRank(next.Class) {
		return "existing route is more conservative"
	}
	for _, phase := range previous.SelectedPhases {
		if !slices.Contains(next.SelectedPhases, phase) {
			return "existing route is more conservative"
		}
	}
	for _, role := range previous.ReviewRoles {
		if !slices.Contains(next.ReviewRoles, role) {
			return "existing route is more conservative"
		}
	}
	return ""
}

func newCmdRouteRefresh() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh <slug>",
		Short: "Refresh actual diff facts and conservatively reclassify",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			state, statePath, err := loadStateAt(args[0])
			if err != nil {
				return err
			}
			if state.Route == nil {
				return fmt.Errorf("project %q has no route decision", args[0])
			}
			snapshot, err := projectSnapshot(args[0])
			if err != nil {
				return err
			}
			facts := project.NormalizeRiskAssessment(state.Route.Facts, snapshot.Fingerprint)
			facts.ActualFileCount = snapshot.FileCount
			facts.ActualChangedLines = snapshot.ChangedLines
			staleEasyEvidence := easyEvidenceIsStale(state, snapshot)
			decision, err := deliveryroute.Classify(facts)
			if err != nil {
				return err
			}
			reason := ""
			if project.RouteRank(decision.Class) != project.RouteRank(state.Route.Class) {
				reason = "current repository facts require a more conservative route"
			}
			decision, err = deliveryroute.PreserveMonotonic(
				*state.Route, decision, reason,
			)
			if err != nil {
				return err
			}
			if staleEasyEvidence {
				target := decision.Class
				if project.RouteRank(target) < project.RouteRank(project.RouteStandard) {
					target = project.RouteStandard
				}
				decision, err = deliveryroute.Escalate(
					decision, target, "snapshot-bound easy-route evidence became stale", "",
				)
				if err != nil {
					return err
				}
			}
			decision.Snapshot = snapshot
			if err := state.ApplyRoute(decision); err != nil {
				return err
			}
			if staleEasyEvidence {
				for _, phase := range []string{"review", "validate"} {
					if err := state.SetPhaseWithDelivery(
						phase, project.PhaseEscalated,
						"snapshot-bound easy-route evidence became stale",
						"", "", "", "", "",
					); err != nil {
						return fmt.Errorf("reopen independent %s: %w", phase, err)
					}
				}
			}
			return saveRouteDecision(statePath, state, decision)
		},
	}
}

func easyEvidenceIsStale(state project.WorkflowState, snapshot project.RepositorySnapshot) bool {
	if state.Route == nil || state.Route.Class != project.RouteEasy || state.Route.ForcedFull {
		return false
	}
	if record := state.Evidence.Review; record != nil &&
		!record.FreshForReviewRoute(
			snapshot, *state.Route, state.Route.ReviewRoles, state.Route.EffectiveReviewOwner(),
		) {
		return true
	}
	if record := state.Evidence.Validation; record != nil &&
		!record.FreshForRoute(snapshot, *state.Route, state.Route.ValidationOwner) {
		return true
	}
	return false
}

func newCmdRouteEscalate() *cobra.Command {
	var reason, risk, stackRationale string
	command := &cobra.Command{
		Use:   "escalate <slug> <standard|high-risk|stack-candidate>",
		Short: "Explicitly escalate a persisted route",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			state, statePath, err := loadStateAt(args[0])
			if err != nil {
				return err
			}
			if state.Route == nil {
				return fmt.Errorf("project %q has no route decision", args[0])
			}
			current := *state.Route
			snapshot, err := projectSnapshot(args[0])
			if err != nil {
				return err
			}
			current.Facts = project.NormalizeRiskAssessment(
				current.Facts,
				snapshot.Fingerprint,
			)
			if stackRationale != "" {
				current.Facts.StackRationale = stackRationale
				current.StackRationale = stackRationale
			}
			if args[1] == deliveryroute.ClassStackCandidate &&
				current.Facts.StackRationale == "" {
				return fmt.Errorf("stack-candidate escalation requires --stack-rationale")
			}
			decision, err := deliveryroute.Escalate(
				current, args[1], reason, deliveryroute.RiskTrigger(risk),
			)
			if err != nil {
				return err
			}
			decision.Snapshot = snapshot
			riskTrigger := project.RiskTrigger(risk)
			if risk != "" && !slices.Contains(decision.Facts.RiskTriggers, riskTrigger) {
				decision.Facts.RiskTriggers = append(decision.Facts.RiskTriggers, riskTrigger)
			}
			if err := state.ApplyRoute(decision); err != nil {
				return err
			}
			return saveRouteDecision(statePath, state, decision)
		},
	}
	command.Flags().StringVar(&reason, "reason", "", "why a more conservative route is required")
	command.Flags().StringVar(&risk, "risk", "", "optional closed risk trigger")
	command.Flags().StringVar(&stackRationale, "stack-rationale", "", "why the work should be decomposed into a stack")
	_ = command.MarkFlagRequired("reason")
	return command
}

func bindRouteFlags(command *cobra.Command, flags *routeFlags) {
	command.Flags().BoolVar(&flags.requestedExplicit, "requested-behavior-explicit", false, "the requested behavior is explicit")
	command.Flags().BoolVar(&flags.unresolved, "unresolved-decision", false, "a product or design decision remains unresolved")
	command.Flags().StringArrayVar(&flags.gates, "gate", nil, "required validation gate as id=command (repeatable)")
	command.Flags().BoolVar(&flags.noRepositoryGates, "no-repository-gates", false, "the repository was verified to define no relevant gates")
	command.Flags().BoolVar(&flags.risksEvaluated, "risk-assessment-complete", false, "all closed safety risks were explicitly evaluated")
	command.Flags().BoolVar(&flags.predictedSizeKnown, "predicted-size-known", false, "the predicted file and line counts were explicitly estimated")
	command.Flags().IntVar(&flags.predictedFiles, "predicted-files", 0, "predicted changed file count")
	command.Flags().IntVar(&flags.predictedLines, "predicted-lines", 0, "predicted changed-line count")
	command.Flags().BoolVar(&flags.stack, "stack-decomposition", false, "the work has an independent stack decomposition")
	command.Flags().StringVar(&flags.stackRationale, "stack-rationale", "", "why the work is a stack candidate")
	command.Flags().BoolVar(&flags.full, "full", false, "force every delivery phase")
	command.Flags().BoolVar(&flags.simplify, "simplify", false, "the author requested simplification")
	command.Flags().BoolVar(&flags.duplication, "duplication", false, "the change contains observable duplication")
	command.Flags().BoolVar(&flags.generatedChurn, "generated-churn", false, "the change contains generated churn")
	command.Flags().BoolVar(&flags.reviewCleanup, "review-cleanup", false, "review requested cleanup")
	command.Flags().BoolVar(&flags.changesTests, "changes-tests", false, "the change modifies tests or test gates")
	command.Flags().BoolVar(&flags.changesDocs, "changes-documentation-comments", false, "the change modifies documentation, comments, or explanatory text")
	command.Flags().BoolVar(&flags.changesTypeDesign, "changes-type-design", false, "the change modifies type design")
	command.Flags().BoolVar(&flags.historySensitive, "history-sensitive", false, "the change requires repository-history compatibility evidence")
	command.Flags().BoolVar(&flags.changesGuidelines, "changes-repository-guidelines", false, "the change modifies or conflicts with repository guidelines")
	command.Flags().StringArrayVar(&flags.risks, "risk", nil, "closed risk trigger (repeatable)")
}

func (flags routeFlags) input(snapshot project.RepositorySnapshot) project.RouteFacts {
	risks := make([]project.RiskTrigger, len(flags.risks))
	for index, risk := range flags.risks {
		risks[index] = project.RiskTrigger(risk)
	}
	gatePolicy := project.GatePolicy{Mode: project.GatePolicyUnknown}
	if flags.noRepositoryGates {
		gatePolicy.Mode = project.GatePolicyNone
	}
	if len(flags.gates) > 0 {
		gatePolicy.Mode = project.GatePolicyRequired
		for _, value := range flags.gates {
			id, command, ok := strings.Cut(value, "=")
			if !ok || strings.TrimSpace(id) == "" || strings.TrimSpace(command) == "" {
				gatePolicy = project.GatePolicy{Mode: "invalid"}
				break
			}
			evidence := redactCommand(strings.TrimSpace(id), command, 0)
			gatePolicy.Gates = append(gatePolicy.Gates, project.RequiredGate{
				ID: evidence.GateID, CommandDigest: evidence.Digest,
				RedactedDisplay: evidence.Display,
			})
		}
	}
	if flags.noRepositoryGates && len(flags.gates) > 0 {
		gatePolicy = project.GatePolicy{Mode: "invalid"}
	}
	return project.RouteFacts{
		RequestedBehaviorExplicit: flags.requestedExplicit,
		UnresolvedDecision:        flags.unresolved,
		GatePolicy:                gatePolicy,
		RiskAssessmentComplete:    flags.risksEvaluated, PredictedSizeKnown: flags.predictedSizeKnown,
		AssessmentFingerprint: snapshot.Fingerprint,
		PredictedFileCount:    flags.predictedFiles, PredictedChangedLines: flags.predictedLines,
		ActualFileCount: snapshot.FileCount, ActualChangedLines: snapshot.ChangedLines,
		StackDecomposition: flags.stack, StackRationale: flags.stackRationale,
		AuthorRequestedFullWorkflow:   flags.full,
		AuthorRequestedSimplification: flags.simplify,
		Duplication:                   flags.duplication, GeneratedChurn: flags.generatedChurn,
		ReviewRequestedCleanup:       flags.reviewCleanup,
		ChangesTests:                 flags.changesTests,
		ChangesDocumentationComments: flags.changesDocs,
		ChangesTypeDesign:            flags.changesTypeDesign,
		HistorySensitive:             flags.historySensitive,
		ChangesRepositoryGuidelines:  flags.changesGuidelines,
		RiskTriggers:                 risks,
	}
}

func saveRouteDecision(statePath string, state project.WorkflowState, decision project.RouteDecision) error {
	if err := project.SaveState(statePath, state); err != nil {
		return err
	}
	if state.Route != nil {
		decision = *state.Route
	}
	return json.NewEncoder(os.Stdout).Encode(decision)
}
