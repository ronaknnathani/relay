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
	"github.com/ronaknnathani/relay/internal/prwatch"
)

func runRoute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	if len(args) >= 2 {
		switch args[0] {
		case "base", "classify", "refresh", "escalate":
			ensureTestCoordinator(t, args[1])
			if !slices.Contains(args, "--coordinator-token") &&
				!slices.Contains(args, "--dispatch-token") {
				args = append(args, "--coordinator-token", testCoordinatorToken)
			}
		}
	}
	return runRouteRaw(t, args...)
}

func runRouteRaw(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := newCmdRoute()
	command.SetArgs(args)
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	return captureStdout(t, command.Execute)
}

func TestRouteBaseRejectsTraversalArchivedAndUnsafeRefs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := runRouteRaw(t, "base", "../archived/demo", "--base", "main"); err == nil ||
		!strings.Contains(err.Error(), "invalid slug") {
		t.Fatalf("traversal route base error = %v", err)
	}

	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	gitOutput(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, repo, "add", "feature.txt")
	gitOutput(t, repo, "commit", "-q", "-m", "feature")
	manifestPath := project.ManifestPath(project.ActiveDir(), "demo")
	manifest, err := project.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Branch = "feature"
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runRouteRaw(t, "base", "demo", "--base", "main"); err == nil ||
		!strings.Contains(err.Error(), "coordinator") {
		t.Fatalf("unauthenticated route base error = %v", err)
	}
	for _, base := range []string{"HEAD", "feature", "refs/heads/main"} {
		if _, err := runRoute(t, "base", "demo", "--base", base); err == nil {
			t.Fatalf("unsafe base %q was accepted", base)
		}
	}
}

func TestRouteBasePinsRemoteTipAndDetectsDrift(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	gitOutput(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, repo, "add", "feature.txt")
	gitOutput(t, repo, "commit", "-q", "-m", "feature")
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, "base", "demo", "--base", "main"); err != nil {
		t.Fatal(err)
	}
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), "demo"))
	if err != nil {
		t.Fatal(err)
	}
	remoteMain := gitRevParse(t, repo, "origin/main")
	if manifest.RemoteBaseSHA != remoteMain {
		t.Fatalf("remote base SHA = %q, want %q", manifest.RemoteBaseSHA, remoteMain)
	}
	tree := gitRevParse(t, repo, "origin/main^{tree}")
	moved := strings.TrimSpace(gitOutput(
		t, repo, "commit-tree", tree, "-p", "origin/main", "-m", "remote moved",
	))
	gitOutput(t, repo, "push", "-q", "origin", moved+":refs/heads/main")
	snapshot, err := projectSnapshot("demo")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.BaseTipSHA != manifest.RemoteBaseSHA || snapshot.BaseTipSHA == moved {
		t.Fatalf("snapshot followed moving remote base: %+v", snapshot)
	}
	branchPoint := manifest.StartSHA
	if _, err := runRoute(
		t, "classify", "demo",
		"--requested-behavior-explicit", "--no-repository-gates",
		"--risk-assessment-complete", "--predicted-size-known",
		"--predicted-files", "1", "--predicted-lines", "20",
	); err != nil {
		t.Fatalf("classify after remote base moved: %v", err)
	}
	rebound, err := project.Load(project.ManifestPath(project.ActiveDir(), "demo"))
	if err != nil {
		t.Fatal(err)
	}
	if rebound.RemoteBaseSHA != moved {
		t.Fatalf("rebound remote base = %q, want %q", rebound.RemoteBaseSHA, moved)
	}
	if rebound.StartSHA != branchPoint {
		t.Fatalf("automatic remote rebind moved branch point from %q to %q", branchPoint, rebound.StartSHA)
	}
	if _, err := runRoute(
		t, "base", "demo", "--base", "main", "--sha", manifest.RemoteBaseSHA,
	); err != nil {
		t.Fatalf("pin reviewed PR base SHA: %v", err)
	}
	pinned, err := project.Load(project.ManifestPath(project.ActiveDir(), "demo"))
	if err != nil {
		t.Fatal(err)
	}
	if pinned.RemoteBaseSHA != manifest.RemoteBaseSHA || pinned.StartSHA != branchPoint {
		t.Fatalf("explicit PR base pin changed branch point: %+v", pinned)
	}
	if _, err := runRoute(
		t, "base", "demo", "--base", "main", "--sha", gitRevParse(t, repo, "HEAD"),
	); err == nil || !strings.Contains(err.Error(), "not contained") {
		t.Fatalf("uncontained PR base SHA error = %v", err)
	}
}

func TestRouteClassifyDoesNotRebindRemoteBaseAcrossUnrelatedHistory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	gitOutput(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, repo, "add", "feature.txt")
	gitOutput(t, repo, "commit", "-q", "-m", "feature")
	saveDeliveryProject(t, "demo", repo)
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, "base", "demo", "--base", "main"); err != nil {
		t.Fatal(err)
	}
	manifestPath := project.ManifestPath(project.ActiveDir(), "demo")
	before, err := project.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	tree := gitRevParse(t, repo, "main^{tree}")
	unrelated := strings.TrimSpace(gitOutput(
		t, repo, "commit-tree", tree, "-m", "unrelated remote history",
	))
	gitOutput(t, repo, "push", "-q", "--force", "origin", unrelated+":refs/heads/main")

	if _, err := runRoute(t, "classify", "demo",
		"--requested-behavior-explicit", "--no-repository-gates",
		"--risk-assessment-complete", "--predicted-size-known",
		"--predicted-files", "1", "--predicted-lines", "20",
	); err != nil {
		t.Fatal(err)
	}
	after, err := project.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.RemoteBaseSHA != before.RemoteBaseSHA || after.RemoteBaseSHA == unrelated {
		t.Fatalf("automatic rebind replaced intended base identity: before=%+v after=%+v", before, after)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Route.Snapshot.BaseTipSHA != before.RemoteBaseSHA {
		t.Fatalf("route followed unrelated remote history: %+v", got.Route.Snapshot)
	}
}

func TestRouteClassifySupportsLocalBranchAndCommitBases(t *testing.T) {
	for _, test := range []struct {
		name string
		base func(t *testing.T, repo string) string
	}{
		{
			name: "local branch",
			base: func(t *testing.T, repo string) string {
				t.Helper()
				return "stack/parent"
			},
		},
		{
			name: "commit SHA",
			base: func(t *testing.T, repo string) string {
				t.Helper()
				return gitRevParse(t, repo, "stack/parent")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repo := initCLIGitRepo(t)
			if test.name == "local branch" {
				gitOutput(t, repo, "push", "-q", "origin", "main:refs/heads/stack/parent")
			}
			gitOutput(t, repo, "checkout", "-q", "-b", "stack/parent")
			if err := os.WriteFile(filepath.Join(repo, "parent.txt"), []byte("parent\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitOutput(t, repo, "add", "parent.txt")
			gitOutput(t, repo, "commit", "-q", "-m", "parent")
			base := test.base(t, repo)
			gitOutput(t, repo, "checkout", "-q", "-b", "feature")
			saveDeliveryProject(t, "demo", repo)
			manifestPath := project.ManifestPath(project.ActiveDir(), "demo")
			manifest, err := project.Load(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			manifest.Branch = "feature"
			manifest.BaseBranch = base
			manifest.StartSHA = gitRevParse(t, repo, "stack/parent")
			manifest.RemoteBaseSHA = ""
			if err := project.Save(manifestPath, manifest); err != nil {
				t.Fatal(err)
			}
			state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
			if err != nil {
				t.Fatal(err)
			}
			if err := project.SaveState(project.StatePath("demo"), state); err != nil {
				t.Fatal(err)
			}
			if _, err := runRoute(t,
				"classify", "demo", "--requested-behavior-explicit",
				"--no-repository-gates", "--risk-assessment-complete",
				"--predicted-size-known", "--predicted-files", "1", "--predicted-lines", "20",
			); err != nil {
				t.Fatalf("classify with %s base %q: %v", test.name, base, err)
			}
			manifest, err = project.Load(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.RemoteBaseSHA != "" {
				t.Fatalf("local base %q was incorrectly remote-bound to %q", base, manifest.RemoteBaseSHA)
			}
		})
	}
}

func TestRouteAdvanceUsesOneTimeScopedCapabilityAfterChildExit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	baseSHA := gitRevParse(t, repo, "origin/main")
	gitOutput(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, repo, "add", "feature.txt")
	gitOutput(t, repo, "commit", "-q", "-m", "feature")
	saveDeliveryProject(t, "demo", repo)

	state := openPRReadyState(t, "demo")
	state.Phases["open-pr"] = project.PhaseState{
		Status: project.PhaseDone, CompletedDispatchID: "open-pr-dispatch",
	}
	state.PR = project.PRRef{Number: 42, URL: "https://github.com/example/test/pull/42"}
	state.FinalResult = &project.FinalResult{
		Status: "opened", PRNumber: 42, PRURL: state.PR.URL,
		RouteRevision: state.Route.Revision, RouteDigest: state.Route.Digest,
		Snapshot: state.Route.Snapshot, DispatchID: "open-pr-dispatch",
	}
	state.DispatchCount = 1
	state.LastDispatch = "open-pr"
	state.LastDispatchID = "open-pr-dispatch"
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	ensureTestCoordinator(t, "demo")
	out, err := runState(t, "grant", "demo", "stack-advance")
	if err != nil {
		t.Fatalf("grant stack advance capability: %v", err)
	}
	var grant struct {
		Capability string `json:"capability"`
	}
	if err := json.Unmarshal([]byte(out), &grant); err != nil {
		t.Fatal(err)
	}
	if grant.Capability == "" {
		t.Fatal("stack advance grant returned an empty capability")
	}

	tree := gitRevParse(t, repo, baseSHA+"^{tree}")
	moved := strings.TrimSpace(gitOutput(
		t, repo, "commit-tree", tree, "-p", baseSHA, "-m", "remote moved",
	))
	gitOutput(t, repo, "push", "-q", "origin", moved+":refs/heads/main")
	if _, err := runRouteRaw(
		t, "advance", "demo", "--base", "main", "--advance-token", grant.Capability,
	); err != nil {
		t.Fatalf("advance stack front: %v", err)
	}
	manifest, err := project.Load(project.ManifestPath(project.ActiveDir(), "demo"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RemoteBaseSHA != moved || manifest.StartSHA != moved ||
		got.Route.Snapshot.BaseTipSHA != moved {
		t.Fatalf("advanced base manifest=%+v route=%+v", manifest, got.Route.Snapshot)
	}
	if got.StackAdvanceHash != "" {
		t.Fatal("stack advance capability was not consumed")
	}
	if _, err := runRouteRaw(
		t, "advance", "demo", "--base", "main", "--advance-token", grant.Capability,
	); err == nil || !strings.Contains(err.Error(), "missing or already used") {
		t.Fatalf("reused stack advance capability error = %v", err)
	}
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

func TestRouteClassifyPersistsNormalizedIncompleteRiskAssessment(t *testing.T) {
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
		"--predicted-size-known",
		"--predicted-files", "1",
		"--predicted-lines", "20",
	); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Route == nil || got.Route.Class != project.RouteStandard ||
		got.Route.Facts.RiskAssessmentComplete ||
		got.Route.Facts.AssessmentFingerprint != "" {
		t.Fatalf("normalized conservative route = %+v", got.Route)
	}
}

func TestRouteReclassifyRebindsImplementationAndKeepsEasyDeliveryAtTwoDispatches(t *testing.T) {
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
	routeToken := dispatchPhase(t, "demo", "route")
	if _, err := runRoute(t, classify...); err != nil {
		t.Fatal(err)
	}
	finishPhase(t, "demo", "route", routeToken, "done", "--outcome", "material")
	implementToken := dispatchPhase(t, "demo", "implement")
	beforeChange, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
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
	if got.Phases["implement"].Status != project.PhaseInProgress ||
		got.Phases["implement"].Dispatch == nil ||
		got.Phases["implement"].Dispatch.RouteRevision != got.Route.Revision ||
		got.Phases["implement"].Dispatch.RouteDigest != got.Route.Digest {
		t.Fatalf("reclassified implementation = %+v\nprevious=%+v\nnext=%+v",
			got.Phases["implement"], beforeChange.Route, got.Route)
	}
	if _, err := runState(t, "evidence", "record", "demo", "review",
		"--result", "passed", "--role", "code-reviewer", "--dispatch-token", implementToken); err != nil {
		t.Fatal(err)
	}
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "passed", "--no-gates", "--dispatch-token", implementToken); err != nil {
		t.Fatal(err)
	}
	finishPhase(t, "demo", "implement", implementToken, "done", "--outcome", "material")
	openPRToken := dispatchPhase(t, "demo", "open-pr")
	if _, err := runState(t, "pr", "demo", "--number", "42", "--url", "https://github.com/example/test/pull/42",
		"--dispatch-token", openPRToken); err != nil {
		t.Fatal(err)
	}
	got, err = project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	dispatches, handoffs := got.DeliveryMetrics()
	if dispatches != 1 || handoffs != 0 {
		t.Fatalf("easy delivery metrics = dispatches:%d handoffs:%d", dispatches, handoffs)
	}
}

func TestRouteClassifyLetsImplementWorkerReassessAndFinishEasyRoute(t *testing.T) {
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
	if _, err := runRoute(t, classify...); err != nil {
		t.Fatal(err)
	}
	routeToken := dispatchPhase(t, "demo", "route")
	finishPhase(t, "demo", "route", routeToken, "done", "--outcome", "material")
	implementToken := dispatchPhase(t, "demo", "implement")
	dispatch := testDispatchTokens[implementToken]
	if err := os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workerClassify := append(append([]string(nil), classify...),
		"--dispatch-token", dispatch.RouteToken)
	if _, err := runRouteRaw(t, workerClassify...); err != nil {
		t.Fatalf("worker reassessment: %v", err)
	}
	if _, err := runState(t, "evidence", "record", "demo", "review",
		"--result", "passed", "--role", "code-reviewer",
		"--dispatch-token", dispatch.ReviewToken); err != nil {
		t.Fatalf("record worker review evidence: %v", err)
	}
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "passed", "--no-gates",
		"--dispatch-token", dispatch.ValidationToken); err != nil {
		t.Fatalf("record worker validation evidence: %v", err)
	}
	finishPhase(t, "demo", "implement", implementToken, "done", "--outcome", "material")
}

func TestRouteClassifyWorkerCannotChangeCoordinatorGatePolicy(t *testing.T) {
	tests := []struct {
		name  string
		gates []string
	}{
		{name: "remove gates", gates: []string{"--no-repository-gates"}},
		{name: "replace gate digest", gates: []string{"--gate", "test=go test ./internal/..."}},
		{name: "add gate", gates: []string{
			"--gate", "test=go test ./...",
			"--gate", "lint=make lint",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
				"--gate", "test=go test ./...", "--risk-assessment-complete",
				"--predicted-size-known", "--predicted-files", "1", "--predicted-lines", "20",
			}
			if _, err := runRoute(t, classify...); err != nil {
				t.Fatal(err)
			}
			routeToken := dispatchPhase(t, "demo", "route")
			finishPhase(t, "demo", "route", routeToken, "done", "--outcome", "material")
			implementToken := dispatchPhase(t, "demo", "implement")
			dispatch := testDispatchTokens[implementToken]

			workerClassify := []string{
				"classify", "demo", "--requested-behavior-explicit",
				"--risk-assessment-complete", "--predicted-size-known",
				"--predicted-files", "1", "--predicted-lines", "20",
				"--dispatch-token", dispatch.RouteToken,
			}
			workerClassify = append(workerClassify, test.gates...)
			if _, err := runRouteRaw(t, workerClassify...); err == nil ||
				!strings.Contains(err.Error(), "coordinator-approved gate policy") {
				t.Fatalf("worker gate-policy mutation error = %v", err)
			}
			got, err := project.LoadState(project.StatePath("demo"))
			if err != nil {
				t.Fatal(err)
			}
			if got.Route.Facts.GatePolicy.Mode != project.GatePolicyRequired ||
				len(got.Route.Facts.GatePolicy.Gates) != 1 ||
				got.Route.Facts.GatePolicy.Gates[0].CommandDigest != commandDigestForTest("go test ./...") {
				t.Fatalf("worker changed gate policy: %+v", got.Route.Facts.GatePolicy)
			}

			workerClassify = []string{
				"classify", "demo", "--requested-behavior-explicit",
				"--gate", "test=go test ./...", "--risk-assessment-complete",
				"--predicted-size-known", "--predicted-files", "1", "--predicted-lines", "20",
				"--dispatch-token", dispatch.RouteToken,
			}
			if _, err := runRouteRaw(t, workerClassify...); err != nil {
				t.Fatalf("worker could not preserve exact gate policy after rejection: %v", err)
			}
		})
	}
}

func TestRouteRefreshPreservesCompatibleStandardWorkerDispatch(t *testing.T) {
	for _, test := range []struct {
		name          string
		phase         string
		classifyFlags []string
	}{
		{name: "implement", phase: "implement"},
		{name: "simplify", phase: "simplify", classifyFlags: []string{"--simplify"}},
	} {
		t.Run(test.name, func(t *testing.T) {
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
				"classify", "demo", "--requested-behavior-explicit", "--unresolved-decision",
				"--no-repository-gates", "--risk-assessment-complete",
				"--predicted-size-known", "--predicted-files", "1", "--predicted-lines", "20",
			}
			classify = append(classify, test.classifyFlags...)
			if _, err := runRoute(t, classify...); err != nil {
				t.Fatal(err)
			}
			state, err = project.LoadState(project.StatePath("demo"))
			if err != nil {
				t.Fatal(err)
			}
			prepareAdaptivePhase(&state, test.phase)
			if err := project.SaveState(project.StatePath("demo"), state); err != nil {
				t.Fatal(err)
			}
			token := dispatchPhase(t, "demo", test.phase)
			dispatch := testDispatchTokens[token]
			if err := os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := runRouteRaw(
				t, "refresh", "demo", "--dispatch-token", dispatch.RouteToken,
			); err != nil {
				t.Fatalf("worker refresh: %v", err)
			}
			got, err := project.LoadState(project.StatePath("demo"))
			if err != nil {
				t.Fatal(err)
			}
			if got.Phases[test.phase].Status != project.PhaseInProgress ||
				got.Phases[test.phase].Dispatch == nil ||
				got.Phases[test.phase].Dispatch.RouteRevision != got.Route.Revision {
				t.Fatalf("compatible worker dispatch was not rebound: %+v", got.Phases[test.phase])
			}
			finishPhase(t, "demo", test.phase, token, "done", "--outcome", "material")
		})
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

func TestRouteRefreshInvalidatesChangedTaskInputs(t *testing.T) {
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
	if _, err := runRoute(t,
		"classify", "demo",
		"--requested-behavior-explicit",
		"--no-repository-gates",
		"--risk-assessment-complete",
		"--predicted-size-known",
		"--predicted-files", "1",
		"--predicted-lines", "20",
	); err != nil {
		t.Fatal(err)
	}
	before, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(project.ActiveDir(), "demo", "task.md")
	if err := os.WriteFile(taskPath, []byte("# Task\n\nChanged acceptance criteria.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, "refresh", "demo"); err != nil {
		t.Fatal(err)
	}
	after, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Route.Snapshot.InputRevision == before.Route.Snapshot.InputRevision ||
		after.Route.Revision <= before.Route.Revision ||
		after.Route.Facts.RiskAssessmentComplete ||
		after.Route.Class != project.RouteStandard {
		t.Fatalf("task input refresh = before %+v after %+v", before.Route, after.Route)
	}
}

func TestRouteClassifyPersistsChangedGatePolicyAndStalesValidation(t *testing.T) {
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
	classify := func(gate string) error {
		_, err := runRoute(t, "classify", "demo",
			"--requested-behavior-explicit",
			"--gate", gate,
			"--risk-assessment-complete",
			"--predicted-size-known",
			"--predicted-files", "1",
			"--predicted-lines", "20",
		)
		return err
	}
	if err := classify("test=go test ./..."); err != nil {
		t.Fatal(err)
	}
	routeToken := dispatchPhase(t, "demo", "route")
	finishPhase(t, "demo", "route", routeToken, "done", "--outcome", "material")
	token := dispatchPhase(t, "demo", "implement")
	if _, err := runState(t, "evidence", "record", "demo", "validation",
		"--result", "passed", "--gate", "test=go test ./...", "--exit-status", "0",
		"--dispatch-token", token); err != nil {
		t.Fatal(err)
	}

	if err := classify("lint=make lint"); err != nil {
		t.Fatalf("gate-policy reclassification failed: %v", err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatalf("load reclassified state: %v", err)
	}
	if got.Evidence.Validation == nil {
		t.Fatal("gate-policy change discarded durable validation history")
	}
	if got.Evidence.Validation.FreshForValidationRoute(
		got.Route.Snapshot,
		*got.Route,
		got.Route.ValidationOwner,
	) {
		t.Fatal("prior validation remained fresh after the gate policy changed")
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
		"unknown": append([]string(nil), base...),
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

func TestRouteEscalateRefreshesActualSizeFacts(t *testing.T) {
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
		"--requested-behavior-explicit", "--no-repository-gates",
		"--risk-assessment-complete", "--predicted-size-known",
		"--predicted-files", "1", "--predicted-lines", "20",
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, "escalate", "demo", "standard", "--reason", "explicit escalation"); err != nil {
		t.Fatal(err)
	}
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Route.Facts.ActualFileCount != got.Route.Snapshot.FileCount ||
		got.Route.Facts.ActualChangedLines != got.Route.Snapshot.ChangedLines ||
		got.Route.Facts.ActualFileCount != 1 ||
		got.Route.Facts.ActualChangedLines != 2 {
		t.Fatalf("escalated actual size facts = %+v, snapshot = %+v", got.Route.Facts, got.Route.Snapshot)
	}
}

func TestRouteBaseUpdatesManifestBeforeRefresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	gitOutput(t, repo, "branch", "-M", "main")
	gitOutput(t, repo, "push", "-q", "-u", "origin", "main")
	gitOutput(t, repo, "checkout", "-q", "-b", "parent")
	if err := os.WriteFile(filepath.Join(repo, "parent.txt"), []byte("parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gitOutput(t, repo, "add", "parent.txt")
	gitOutput(t, repo, "commit", "-q", "-m", "parent")
	mainSHA := gitRevParse(t, repo, "main")
	manifestPath := project.ManifestPath(project.ActiveDir(), "demo")
	manifest, err := project.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Branch = "parent"
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	archivedDir := filepath.Join(project.ArchivedDir(), "demo")
	if err := os.MkdirAll(archivedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	archivedWorktree := repo
	if err := project.Save(filepath.Join(archivedDir, "manifest.json"), project.Manifest{
		Slug: "demo", Worktree: &archivedWorktree, BaseBranch: "archived-base",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := runRoute(t, "base", "demo", "--base", "main"); err != nil {
		t.Fatal(err)
	}
	manifestPath, err = project.Find("demo")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = project.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.BaseBranch != "main" || manifest.StartSHA != mainSHA ||
		manifest.RemoteBaseSHA != mainSHA {
		t.Fatalf("updated manifest base = %q at %q", manifest.BaseBranch, manifest.StartSHA)
	}
	archived, err := project.Load(filepath.Join(archivedDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if archived.BaseBranch != "archived-base" {
		t.Fatalf("route base mutated archived project: %+v", archived)
	}
}

func TestRouteRefreshPreservesRecordedPRForStackWatcher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	gitOutput(t, repo, "branch", "-M", "main")
	gitOutput(t, repo, "push", "-q", "-u", "origin", "main")
	gitOutput(t, repo, "checkout", "-q", "-b", "feature")
	saveDeliveryProject(t, "demo", repo)
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, repo, "add", "feature.txt")
	gitOutput(t, repo, "commit", "-q", "-m", "feature")
	state, err := project.NewState("demo", "deliver-pr", project.AdaptiveDeliveryPhases)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoute(t, "base", "demo", "--base", "main"); err != nil {
		t.Fatal(err)
	}
	state = openPRReadyState(t, "demo")
	if err := project.SaveState(project.StatePath("demo"), state); err != nil {
		t.Fatal(err)
	}
	token := dispatchPhase(t, "demo", "open-pr")
	if _, err := runState(t, "pr", "demo", "--number", "42",
		"--url", "https://github.com/example/test/pull/42", "--dispatch-token", token); err != nil {
		t.Fatal(err)
	}

	tree := gitRevParse(t, repo, "main^{tree}")
	newBase := strings.TrimSpace(gitOutput(t, repo, "commit-tree", tree, "-p", "main", "-m", "advance base"))
	gitOutput(t, repo, "update-ref", "refs/heads/main", newBase)
	if _, err := runRoute(t, "refresh", "demo"); err != nil {
		t.Fatal(err)
	}

	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.PR.Number != 42 || got.PR.URL != "https://github.com/example/test/pull/42" ||
		got.FinalResult != nil || got.Phases["open-pr"].Status != project.PhaseEscalated {
		t.Fatalf("refreshed open PR state = pr:%+v final:%+v phase:%+v",
			got.PR, got.FinalResult, got.Phases["open-pr"])
	}
	target, err := prwatch.LoadTarget("demo")
	if err != nil {
		t.Fatal(err)
	}
	if target.PRNumber != 42 {
		t.Fatalf("watch target PR = %d, want 42", target.PRNumber)
	}
}

func TestRouteClassifyReportsActionableGateFlagErrors(t *testing.T) {
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
	tests := map[string]struct {
		flags []string
		want  string
	}{
		"missing separator": {
			flags: []string{"--gate", "test"},
			want:  "--gate #1 must use id=command",
		},
		"empty id": {
			flags: []string{"--gate", "=TOKEN=super-secret go test ./..."},
			want:  "--gate #1 has an empty id",
		},
		"empty command": {
			flags: []string{"--gate", "test= "},
			want:  `--gate "test" has an empty command`,
		},
		"conflicting declarations": {
			flags: []string{"--gate", "test=go test ./...", "--no-repository-gates"},
			want:  "--gate and --no-repository-gates cannot be used together",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := runRoute(t, append(append([]string(nil), base...), test.flags...)...)
			if err == nil {
				t.Fatal("invalid gate flags were accepted")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want actionable detail %q", err, test.want)
			}
			if strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("error exposed raw gate command: %q", err)
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
	routeToken := dispatchPhase(t, "demo", "route")
	finishPhase(t, "demo", "route", routeToken, "done", "--outcome", "material")
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

func TestRouteClassifyUsesForcedFullManifestWithoutFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initCLIGitRepo(t)
	saveDeliveryProject(t, "demo", repo)
	manifestPath := project.ManifestPath(project.ActiveDir(), "demo")
	manifest, err := project.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.DeliveryMode = project.DeliveryModeFull
	if err := project.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
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
	got, err := project.LoadState(project.StatePath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Route == nil || !got.Route.ForcedFull ||
		!slices.Equal(got.Route.SelectedPhases, project.AdaptiveDeliveryPhases) {
		t.Fatalf("manifest-forced route = %+v", got.Route)
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
	routeToken := dispatchPhase(t, "demo", "route")
	finishPhase(t, "demo", "route", routeToken, "done", "--outcome", "material")
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
	state.Route.ReviewRoles = []string{
		project.ReviewRoleCodeReviewer,
		project.ReviewRoleSecurity,
	}
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
