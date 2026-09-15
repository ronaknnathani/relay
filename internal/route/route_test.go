package route

import (
	"slices"
	"testing"

	"github.com/ronaknnathani/relay/internal/project"
)

func easyInput() Input {
	return Input{
		RequestedBehaviorExplicit: true,
		GatePolicy:                project.GatePolicy{Mode: project.GatePolicyNone},
		RiskAssessmentComplete:    true,
		AssessmentFingerprint:     "fingerprint",
		PredictedSizeKnown:        true,
		PredictedFileCount:        2,
		PredictedChangedLines:     40,
		ActualFileCount:           2,
		ActualChangedLines:        40,
	}
}

func TestRiskAssessmentRequiresFingerprint(t *testing.T) {
	input := easyInput()
	input.AssessmentFingerprint = ""
	decision, err := Classify(input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Class != ClassStandard || decision.Facts.RiskAssessmentComplete {
		t.Fatalf("unbound assessment remained eligible: %+v", decision)
	}
}

func TestChangeSurfacesSelectReachableSpecialists(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Input)
		roles  []string
	}{
		{
			name: "tests",
			mutate: func(input *Input) {
				input.ChangesTests = true
			},
			roles: []string{project.ReviewRolePRTestAnalyzer},
		},
		{
			name: "documentation and comments",
			mutate: func(input *Input) {
				input.ChangesDocumentationComments = true
			},
			roles: []string{project.ReviewRoleCommentAnalyzer},
		},
		{
			name: "type design",
			mutate: func(input *Input) {
				input.ChangesTypeDesign = true
			},
			roles: []string{project.ReviewRoleTypeDesignAnalyzer},
		},
		{
			name: "history",
			mutate: func(input *Input) {
				input.HistorySensitive = true
			},
			roles: []string{project.ReviewRoleGitHistory},
		},
		{
			name: "repository guidelines",
			mutate: func(input *Input) {
				input.ChangesRepositoryGuidelines = true
			},
			roles: []string{project.ReviewRolePriorPRHistory},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := easyInput()
			test.mutate(&input)
			decision, err := Classify(input)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Class != ClassEasy ||
				decision.ReviewOwner != project.EvidenceOwnerImplement ||
				slices.Contains(decision.SelectedPhases, "review") {
				t.Fatalf("easy specialist ownership changed: %+v", decision)
			}
			for _, role := range test.roles {
				if !slices.Contains(decision.ReviewRoles, role) {
					t.Errorf("roles %v missing %q", decision.ReviewRoles, role)
				}
			}
		})
	}
}

var requiredRiskTriggers = []RiskTrigger{
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

func TestEasyEligibilityRequiresEveryInclusionRule(t *testing.T) {
	tests := map[string]func(*Input){
		"explicit behavior":  func(input *Input) { input.RequestedBehaviorExplicit = false },
		"resolved decisions": func(input *Input) { input.UnresolvedDecision = true },
		"known checks":       func(input *Input) { input.GatePolicy = project.GatePolicy{} },
		"evaluated risks":    func(input *Input) { input.RiskAssessmentComplete = false },
		"predicted size":     func(input *Input) { input.PredictedSizeKnown = false },
		"predicted files":    func(input *Input) { input.PredictedFileCount = 4 },
		"predicted lines":    func(input *Input) { input.PredictedChangedLines = 151 },
		"actual files":       func(input *Input) { input.ActualFileCount = 4 },
		"actual lines":       func(input *Input) { input.ActualChangedLines = 151 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := easyInput()
			mutate(&input)
			decision, err := Classify(input)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Class == ClassEasy {
				t.Fatalf("%s violation classified easy: %+v", name, decision)
			}
		})
	}
}

func TestEveryRiskTriggerExcludesEasy(t *testing.T) {
	if !slices.Equal(AllRiskTriggers(), requiredRiskTriggers) {
		t.Fatalf("risk trigger contract = %v, want %v", AllRiskTriggers(), requiredRiskTriggers)
	}
	for _, trigger := range requiredRiskTriggers {
		t.Run(string(trigger), func(t *testing.T) {
			input := easyInput()
			input.RiskTriggers = []RiskTrigger{trigger}
			decision, err := Classify(input)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Class != ClassHighRisk {
				t.Fatalf("risk %q class = %q, want high-risk", trigger, decision.Class)
			}
		})
	}
}

func TestDuplicateRiskTriggersAreNormalizedBeforePersistence(t *testing.T) {
	input := easyInput()
	input.RiskTriggers = []RiskTrigger{RiskAuthSecurity, RiskAuthSecurity}
	decision, err := Classify(input)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(decision.Facts.RiskTriggers, []RiskTrigger{RiskAuthSecurity}) {
		t.Fatalf("normalized risks = %v", decision.Facts.RiskTriggers)
	}
}

func TestForcedFullSelectsAllPhasesWithoutChangingClass(t *testing.T) {
	input := easyInput()
	input.AuthorRequestedFullWorkflow = true
	decision, err := Classify(input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Class != ClassEasy || !decision.ForcedFull ||
		!slices.Equal(decision.SelectedPhases, AllPhases) {
		t.Fatalf("forced decision = %+v", decision)
	}
}

func TestEasyRouteDeterministicallyReducesSequentialWork(t *testing.T) {
	decision, err := Classify(easyInput())
	if err != nil {
		t.Fatal(err)
	}
	const fixedPhases, fixedDispatches, fixedHandoffs = 7, 7, 6
	selectedPhases := len(decision.SelectedPhases) - 1
	dispatches := selectedPhases
	handoffs := dispatches - 1
	if selectedPhases != 2 || dispatches != 2 || handoffs != 1 {
		t.Fatalf(
			"easy metrics = phases:%d dispatches:%d handoffs:%d",
			selectedPhases,
			dispatches,
			handoffs,
		)
	}
	if selectedPhases >= fixedPhases || dispatches >= fixedDispatches || handoffs >= fixedHandoffs {
		t.Fatal("easy route did not reduce every deterministic latency driver")
	}
}

func TestSelectionAndOwnershipByClass(t *testing.T) {
	easy, err := Classify(easyInput())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(easy.SelectedPhases, []string{"route", "implement", "open-pr"}) ||
		easy.ReviewOwner != project.EvidenceOwnerImplement ||
		easy.ValidationOwner != project.EvidenceOwnerImplement ||
		!slices.Equal(easy.ReviewRoles, []string{project.ReviewRoleCodeReviewer}) {
		t.Fatalf("easy decision = %+v", easy)
	}

	standardInput := easyInput()
	standardInput.PredictedChangedLines = 200
	standardInput.ActualChangedLines = 200
	standardInput.RequestedBehaviorExplicit = false
	standard, err := Classify(standardInput)
	if err != nil {
		t.Fatal(err)
	}
	if standard.Class != ClassStandard ||
		!slices.Contains(standard.SelectedPhases, "clarify") ||
		!slices.Contains(standard.SelectedPhases, "plan") ||
		slices.Contains(standard.SelectedPhases, "simplify") ||
		standard.ReviewOwner != project.EvidenceOwnerReview ||
		standard.ValidationOwner != project.EvidenceOwnerValidate {
		t.Fatalf("standard decision = %+v", standard)
	}

	highInput := easyInput()
	highInput.RiskTriggers = []RiskTrigger{RiskAuthSecurity, RiskConcurrencyDistributed}
	high, err := Classify(highInput)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{
		project.ReviewRoleCodeReviewer,
		project.ReviewRoleSecurity,
		project.ReviewRoleSilentFailureHunter,
	} {
		if !slices.Contains(high.ReviewRoles, role) {
			t.Errorf("high-risk roles %v missing %q", high.ReviewRoles, role)
		}
	}
}

func TestStandardReviewRolesAreTriggerSelected(t *testing.T) {
	input := easyInput()
	input.GeneratedChurn = true
	decision, err := Classify(input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Class != ClassStandard ||
		!slices.Equal(decision.ReviewRoles, []string{
			project.ReviewRoleCodeReviewer,
			project.ReviewRolePRTestAnalyzer,
		}) {
		t.Fatalf("standard review roles = %+v", decision)
	}
}

func TestDependencyBuildReleaseSelectsSupplyChainSecurityCoverage(t *testing.T) {
	input := easyInput()
	input.RiskTriggers = []RiskTrigger{RiskDependencyBuildRelease}
	decision, err := Classify(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{
		project.ReviewRoleSecurity,
		project.ReviewRolePRTestAnalyzer,
	} {
		if !slices.Contains(decision.ReviewRoles, role) {
			t.Errorf("dependency/build/release roles %v missing %q", decision.ReviewRoles, role)
		}
	}
}

func TestSimplifyAndStackSelection(t *testing.T) {
	input := easyInput()
	input.AuthorRequestedSimplification = true
	decision, err := Classify(input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Class != ClassStandard || !slices.Contains(decision.SelectedPhases, "simplify") {
		t.Fatalf("simplification decision = %+v", decision)
	}

	input = easyInput()
	input.StackDecomposition = true
	input.StackRationale = "independent API and implementation changes"
	decision, err = Classify(input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Class != ClassStackCandidate || decision.StackRationale == "" ||
		slices.Contains(decision.SelectedPhases, "simplify") {
		t.Fatalf("stack decision = %+v", decision)
	}
}

func TestActualSizeAndEscalationAreMonotonic(t *testing.T) {
	input := easyInput()
	input.ActualChangedLines = 151
	decision, err := Classify(input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Class != ClassStandard {
		t.Fatalf("actual-size decision = %+v", decision)
	}
	previous, err := Classify(easyInput())
	if err != nil {
		t.Fatal(err)
	}
	previous.Class = ClassHighRisk
	previous.SelectedPhases = append(
		previous.SelectedPhases,
		"clarify",
		"plan",
		"simplify",
		"validate",
	)
	previous.ReviewRoles = append(previous.ReviewRoles, project.ReviewRoleSecurity)
	previous.EscalationReasons = []string{"prior security classification"}
	decision, err = PreserveMonotonic(previous, decision, "automatic route downgrades are disabled")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Class != ClassHighRisk ||
		!slices.Contains(decision.SelectedPhases, "simplify") ||
		!slices.Contains(decision.ReviewRoles, project.ReviewRoleSecurity) ||
		!slices.Equal(decision.EscalationReasons, []string{
			"prior security classification",
			"automatic route downgrades are disabled",
		}) {
		t.Fatalf("monotonic decision = %+v", decision)
	}
	if _, err := Escalate(decision, ClassEasy, "try faster path", ""); err == nil {
		t.Fatal("explicit downgrade was accepted")
	}
}

func TestExplicitEscalationRetainsFactsPhasesRolesAndHistory(t *testing.T) {
	current, err := Classify(easyInput())
	if err != nil {
		t.Fatal(err)
	}
	current.Facts.AuthorRequestedSimplification = true
	current.SelectedPhases = append(current.SelectedPhases, "simplify")
	current.ReviewRoles = append(current.ReviewRoles, project.ReviewRoleTypeDesignAnalyzer)
	current.EscalationReasons = []string{"earlier escalation"}

	escalated, err := Escalate(current, ClassHighRisk, "validation failed", RiskFailedGate)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"simplify", "clarify", "plan", "validate"} {
		if !slices.Contains(escalated.SelectedPhases, phase) {
			t.Errorf("escalation dropped phase %q: %+v", phase, escalated)
		}
	}
	for _, role := range []string{
		project.ReviewRoleCodeReviewer,
		project.ReviewRoleTypeDesignAnalyzer,
		project.ReviewRolePRTestAnalyzer,
	} {
		if !slices.Contains(escalated.ReviewRoles, role) {
			t.Errorf("escalation dropped role %q: %+v", role, escalated)
		}
	}
	if !slices.Contains(escalated.Facts.RiskTriggers, RiskFailedGate) ||
		!slices.Equal(escalated.EscalationReasons, []string{
			"earlier escalation",
			"validation failed",
		}) {
		t.Fatalf("escalation history/facts = %+v", escalated)
	}
}

func TestSameRankEscalationAcceptsNewRiskOrReason(t *testing.T) {
	input := easyInput()
	input.RiskTriggers = []RiskTrigger{RiskAuthSecurity}
	current, err := Classify(input)
	if err != nil {
		t.Fatal(err)
	}
	escalated, err := Escalate(
		current,
		ClassHighRisk,
		"concurrency risk discovered",
		RiskConcurrencyDistributed,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(escalated.Facts.RiskTriggers, RiskConcurrencyDistributed) ||
		!slices.Contains(escalated.EscalationReasons, "concurrency risk discovered") {
		t.Fatalf("same-rank escalation = %+v", escalated)
	}
	if _, err := Escalate(
		escalated,
		ClassHighRisk,
		"concurrency risk discovered",
		RiskConcurrencyDistributed,
	); err == nil {
		t.Fatal("duplicate same-rank escalation was accepted")
	}
}
