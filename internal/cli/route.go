package cli

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ronaknnathani/relay/internal/gitx"
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

type routeBaseOutput struct {
	BaseBranch    string `json:"base_branch"`
	StartSHA      string `json:"start_sha"`
	RemoteBaseSHA string `json:"remote_base_sha"`
}

func newCmdRoute() *cobra.Command {
	command := &cobra.Command{
		Use:   "route",
		Short: "Classify and refresh adaptive delivery routes",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(
		newCmdRouteSnapshot(), newCmdRouteClassify(), newCmdRouteRefresh(), newCmdRouteEscalate(),
		newCmdRouteBase(), newCmdRouteAdvance(),
	)
	return command
}

func newCmdRouteAdvance() *cobra.Command {
	var base, advanceToken string
	command := &cobra.Command{
		Use:   "advance <slug>",
		Short: "Rebind and refresh a stacked project with a one-time capability",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			base = strings.TrimSpace(base)
			if base == "" {
				return fmt.Errorf("route advance requires --base <remote-branch>")
			}
			state, statePath, err := loadStateAt(args[0])
			if err != nil {
				return err
			}
			if err := consumeStackAdvanceCapability(&state, advanceToken); err != nil {
				return err
			}
			if _, err := bindRemoteBase(args[0], base, "", true); err != nil {
				return err
			}
			snapshot, err := projectSnapshot(args[0])
			if err != nil {
				return err
			}
			decision, err := refreshRouteState(&state, snapshot)
			if err != nil {
				return err
			}
			if err := project.SaveState(statePath, state); err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(decision)
		},
	}
	command.Flags().StringVar(&base, "base", "", "new remote base branch")
	command.Flags().StringVar(
		&advanceToken, "advance-token", "",
		"one-time stack advance capability issued by the child coordinator",
	)
	_ = command.MarkFlagRequired("advance-token")
	return command
}

func newCmdRouteBase() *cobra.Command {
	var base, baseSHA, coordinatorToken string
	command := &cobra.Command{
		Use:   "base <slug>",
		Short: "Update the project base before refreshing its route",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			base = strings.TrimSpace(base)
			if base == "" {
				return fmt.Errorf("route base requires --base <remote-branch>")
			}
			if err := project.ValidateSlug(args[0]); err != nil {
				return err
			}
			state, _, err := loadStateAt(args[0])
			if err != nil {
				return err
			}
			if err := validateCoordinatorToken(state, coordinatorToken); err != nil {
				return err
			}
			result, err := bindRemoteBase(args[0], base, baseSHA, baseSHA == "")
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		},
	}
	command.Flags().StringVar(&base, "base", "", "new remote base branch")
	command.Flags().StringVar(
		&baseSHA, "sha", "",
		"immutable PR base SHA to pin after verifying it belongs to the remote base branch",
	)
	command.Flags().StringVar(
		&coordinatorToken, "coordinator-token", "",
		"trusted coordinator capability returned by adaptive state initialization",
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
	var coordinatorToken, dispatchToken string
	command := &cobra.Command{
		Use:   "classify <slug>",
		Short: "Classify normalized task facts and persist the route",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			state, statePath, err := loadStateAt(args[0])
			if err != nil {
				return err
			}
			if err := authorizeRouteUpdate(&state, coordinatorToken, dispatchToken); err != nil {
				return err
			}
			if coordinatorToken != "" {
				if err := refreshRemoteBaseBinding(args[0], state); err != nil {
					return err
				}
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
			facts, err := flags.input(snapshot)
			if err != nil {
				return err
			}
			if state.Route != nil && strings.TrimSpace(coordinatorToken) == "" {
				facts, err = preserveWorkerGatePolicy(state.Route.Facts.GatePolicy, facts)
				if err != nil {
					return err
				}
			}
			decision, err := deliveryroute.Classify(facts)
			if err != nil {
				return err
			}
			decision.Snapshot = snapshot
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
	command.Flags().StringVar(
		&coordinatorToken, "coordinator-token", "",
		"trusted coordinator capability returned by adaptive state initialization",
	)
	command.Flags().StringVar(
		&dispatchToken, "dispatch-token", "",
		"one-time route-update capability returned by state dispatch",
	)
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
	var coordinatorToken, dispatchToken string
	command := &cobra.Command{
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
			if err := authorizeRouteUpdate(&state, coordinatorToken, dispatchToken); err != nil {
				return err
			}
			if coordinatorToken != "" {
				if err := refreshRemoteBaseBinding(args[0], state); err != nil {
					return err
				}
			}
			snapshot, err := projectSnapshot(args[0])
			if err != nil {
				return err
			}
			decision, err := refreshRouteState(&state, snapshot)
			if err != nil {
				return err
			}
			return saveRouteDecision(statePath, state, decision)
		},
	}
	command.Flags().StringVar(
		&coordinatorToken, "coordinator-token", "",
		"trusted coordinator capability returned by adaptive state initialization",
	)
	command.Flags().StringVar(
		&dispatchToken, "dispatch-token", "",
		"one-time route-update capability returned by state dispatch",
	)
	return command
}

func refreshRouteState(
	state *project.WorkflowState,
	snapshot project.RepositorySnapshot,
) (project.RouteDecision, error) {
	facts := project.NormalizeRiskAssessment(state.Route.Facts, snapshot.Revision())
	facts.ActualFileCount = snapshot.FileCount
	facts.ActualChangedLines = snapshot.ChangedLines
	staleEasyEvidence := easyEvidenceIsStale(*state, snapshot)
	decision, err := deliveryroute.Classify(facts)
	if err != nil {
		return project.RouteDecision{}, err
	}
	reason := ""
	if project.RouteRank(decision.Class) != project.RouteRank(state.Route.Class) {
		reason = "current repository facts require a more conservative route"
	}
	decision, err = deliveryroute.PreserveMonotonic(*state.Route, decision, reason)
	if err != nil {
		return project.RouteDecision{}, err
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
			return project.RouteDecision{}, err
		}
	}
	decision.Snapshot = snapshot
	if err := state.ApplyRoute(decision); err != nil {
		return project.RouteDecision{}, err
	}
	if staleEasyEvidence {
		for _, phase := range []string{"review", "validate"} {
			if err := state.SetPhaseWithDelivery(
				phase, project.PhaseEscalated,
				"snapshot-bound easy-route evidence became stale",
				"", "", "", "", "",
			); err != nil {
				return project.RouteDecision{}, fmt.Errorf("reopen independent %s: %w", phase, err)
			}
		}
	}
	return decision, nil
}

func consumeStackAdvanceCapability(state *project.WorkflowState, token string) error {
	if state.StackAdvanceHash == "" {
		return fmt.Errorf("stack advance capability is missing or already used")
	}
	sum := sha256.Sum256([]byte(token))
	expected, err := decodeSHA256(state.StackAdvanceHash)
	if err != nil {
		return fmt.Errorf("invalid stored stack advance capability: %w", err)
	}
	if subtle.ConstantTimeCompare(sum[:], expected) != 1 {
		return fmt.Errorf("invalid stack advance capability")
	}
	state.StackAdvanceHash = ""
	return nil
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
	var reason, risk, stackRationale, coordinatorToken, dispatchToken string
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
			if err := authorizeRouteUpdate(&state, coordinatorToken, dispatchToken); err != nil {
				return err
			}
			current := *state.Route
			snapshot, err := projectSnapshot(args[0])
			if err != nil {
				return err
			}
			current.Facts = project.NormalizeRiskAssessment(
				current.Facts,
				snapshot.Revision(),
			)
			current.Facts.ActualFileCount = snapshot.FileCount
			current.Facts.ActualChangedLines = snapshot.ChangedLines
			if stackRationale != "" {
				current.Facts.StackRationale = stackRationale
				current.StackRationale = stackRationale
			}
			if args[1] == deliveryroute.ClassStackCandidate {
				current.Facts.StackDecomposition = true
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
	command.Flags().StringVar(
		&coordinatorToken, "coordinator-token", "",
		"trusted coordinator capability returned by adaptive state initialization",
	)
	command.Flags().StringVar(
		&dispatchToken, "dispatch-token", "",
		"one-time route-update capability returned by state dispatch",
	)
	_ = command.MarkFlagRequired("reason")
	return command
}

func authorizeRouteUpdate(
	state *project.WorkflowState,
	coordinatorToken string,
	dispatchToken string,
) error {
	if strings.TrimSpace(coordinatorToken) != "" {
		return validateCoordinatorToken(*state, coordinatorToken)
	}
	current := state.Current()
	if current == "" {
		return fmt.Errorf("route update requires a coordinator capability")
	}
	if strings.TrimSpace(dispatchToken) == "" {
		return fmt.Errorf(
			"route update requires --coordinator-token or the active phase --dispatch-token",
		)
	}
	return consumePhaseDispatchToken(state, current, dispatchScopeRoute, dispatchToken)
}

func refreshRemoteBaseBinding(slug string, state project.WorkflowState) error {
	manifestPath, err := project.FindActive(slug)
	if err != nil {
		return err
	}
	manifest, err := project.Load(manifestPath)
	if err != nil {
		return err
	}
	if manifest.Worktree == nil || strings.TrimSpace(*manifest.Worktree) == "" ||
		manifest.BaseBranch == "" || manifest.BaseBranch == "HEAD" ||
		!gitx.HasOrigin(*manifest.Worktree) ||
		state.PendingPR.Number > 0 {
		return nil
	}
	if manifest.RemoteBaseSHA == "" &&
		gitx.RevParse(*manifest.Worktree, "origin/"+manifest.BaseBranch) == "" {
		return nil
	}
	_, err = bindRemoteBase(slug, manifest.BaseBranch, "", false)
	return err
}

func bindRemoteBase(
	slug string,
	base string,
	requestedSHA string,
	updateStartSHA bool,
) (routeBaseOutput, error) {
	manifestPath, err := project.FindActive(slug)
	if err != nil {
		return routeBaseOutput{}, err
	}
	manifest, err := project.Load(manifestPath)
	if err != nil {
		return routeBaseOutput{}, err
	}
	if manifest.Worktree == nil || strings.TrimSpace(*manifest.Worktree) == "" {
		return routeBaseOutput{}, fmt.Errorf("project %q has no worktree", slug)
	}
	if base == "HEAD" || strings.HasPrefix(base, "refs/") ||
		strings.HasPrefix(base, "origin/") ||
		!gitx.ValidBranchName(*manifest.Worktree, base) ||
		base == manifest.Branch {
		return routeBaseOutput{}, fmt.Errorf(
			"route base %q must name a valid remote branch, not HEAD, a qualified ref, or the feature branch",
			base,
		)
	}
	if !gitx.HasOrigin(*manifest.Worktree) {
		return routeBaseOutput{}, fmt.Errorf("project %q has no origin remote for base %q", slug, base)
	}
	if output, err := gitx.Fetch(*manifest.Worktree, base); err != nil {
		return routeBaseOutput{}, fmt.Errorf(
			"fetch remote base %q for project %q: %w: %s", base, slug, err, output,
		)
	}
	remoteRef := "origin/" + base
	startSHA := gitx.RevParse(*manifest.Worktree, remoteRef)
	if startSHA == "" {
		return routeBaseOutput{}, fmt.Errorf("resolve remote base %q for project %q", base, slug)
	}
	requestedSHA = strings.TrimSpace(requestedSHA)
	if requestedSHA != "" {
		if !gitx.CommitExists(*manifest.Worktree, requestedSHA) {
			return routeBaseOutput{}, fmt.Errorf(
				"route base SHA %q is not an existing commit", requestedSHA,
			)
		}
		if !gitx.IsBranchReachable(*manifest.Worktree, requestedSHA, remoteRef) {
			return routeBaseOutput{}, fmt.Errorf(
				"route base SHA %q is not contained by remote branch %q", requestedSHA, base,
			)
		}
		startSHA = gitx.RevParse(*manifest.Worktree, requestedSHA)
	}
	previousBase := manifest.RemoteBaseSHA
	if previousBase == "" {
		previousBase = manifest.StartSHA
	}
	headSHA := gitx.RevParse(*manifest.Worktree, "HEAD")
	if headSHA != previousBase &&
		gitx.IsBranchReachable(*manifest.Worktree, "HEAD", remoteRef) {
		return routeBaseOutput{}, fmt.Errorf("remote base %q already contains the feature HEAD", base)
	}
	if manifest.BaseBranch == base && manifest.RemoteBaseSHA == startSHA &&
		(!updateStartSHA || manifest.StartSHA == startSHA) {
		return routeBaseOutput{
			BaseBranch: base, StartSHA: manifest.StartSHA, RemoteBaseSHA: startSHA,
		}, nil
	}
	manifest.BaseBranch = base
	if updateStartSHA {
		manifest.StartSHA = startSHA
	}
	manifest.RemoteBaseSHA = startSHA
	if err := project.Save(manifestPath, manifest); err != nil {
		return routeBaseOutput{}, err
	}
	return routeBaseOutput{
		BaseBranch: base, StartSHA: manifest.StartSHA, RemoteBaseSHA: startSHA,
	}, nil
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

func (flags routeFlags) input(snapshot project.RepositorySnapshot) (project.RouteFacts, error) {
	risks := make([]project.RiskTrigger, len(flags.risks))
	for index, risk := range flags.risks {
		risks[index] = project.RiskTrigger(risk)
	}
	if flags.noRepositoryGates && len(flags.gates) > 0 {
		return project.RouteFacts{}, fmt.Errorf(
			"--gate and --no-repository-gates cannot be used together",
		)
	}
	gatePolicy := project.GatePolicy{Mode: project.GatePolicyUnknown}
	if flags.noRepositoryGates {
		gatePolicy.Mode = project.GatePolicyNone
	}
	if len(flags.gates) > 0 {
		gatePolicy.Mode = project.GatePolicyRequired
		for index, value := range flags.gates {
			id, command, err := parseGateFlag(value, index)
			if err != nil {
				return project.RouteFacts{}, err
			}
			evidence := redactCommand(id, command, 0)
			gatePolicy.Gates = append(gatePolicy.Gates, project.RequiredGate{
				ID: evidence.GateID, CommandDigest: evidence.Digest,
				RedactedDisplay: evidence.Display,
			})
		}
	}
	return project.RouteFacts{
		RequestedBehaviorExplicit: flags.requestedExplicit,
		UnresolvedDecision:        flags.unresolved,
		GatePolicy:                gatePolicy,
		RiskAssessmentComplete:    flags.risksEvaluated, PredictedSizeKnown: flags.predictedSizeKnown,
		AssessmentFingerprint: snapshot.Revision(),
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
	}, nil
}

func preserveWorkerGatePolicy(
	approved project.GatePolicy,
	facts project.RouteFacts,
) (project.RouteFacts, error) {
	approved, err := project.NormalizeGatePolicy(approved)
	if err != nil {
		return project.RouteFacts{}, fmt.Errorf("normalize coordinator-approved gate policy: %w", err)
	}
	requested, err := project.NormalizeGatePolicy(facts.GatePolicy)
	if err != nil {
		return project.RouteFacts{}, err
	}
	if requested.Mode == project.GatePolicyUnknown {
		facts.GatePolicy = approved
		return facts, nil
	}
	if requested.Mode != approved.Mode || !slices.Equal(requested.Gates, approved.Gates) {
		return project.RouteFacts{}, fmt.Errorf(
			"dispatch-token route reassessment cannot change the coordinator-approved gate policy",
		)
	}
	facts.GatePolicy = approved
	return facts, nil
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
