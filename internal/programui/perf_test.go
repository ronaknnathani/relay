package programui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProgramUIPerformance(t *testing.T) {
	mode := os.Getenv("RELAY_BROWSER_PERF")
	if mode == "" {
		t.Skip("set RELAY_BROWSER_PERF=baseline or verify")
	}
	output := os.Getenv("RELAY_PERF_OUTPUT")
	if output == "" {
		t.Fatal("RELAY_PERF_OUTPUT is required")
	}

	report := measureProgramUI(t, mode)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(output, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if report.SourceModified {
		t.Fatal("performance source worktree is modified; commit or remove all changes before running the official benchmark")
	}
	if report.LoadAdmission.Status != performanceLoadValid {
		t.Fatalf("performance environment is invalid: %s", report.LoadAdmission.Reason)
	}
	if mode == "verify" {
		verifyPerformanceReport(t, report, os.Getenv("RELAY_PERF_BASELINE"))
	}
}

func TestValidatePerformanceMetadata(t *testing.T) {
	environment := performanceEnvironment{
		MachineID: "machine", OS: "darwin", Arch: "arm64", CPU: "test",
		LogicalCPU: 8, GoVersion: "go1.25",
	}
	valid := performanceReport{
		Mode: "verify", SourceCommit: "after",
		SourceCommitTime: "2026-09-15T20:30:00Z", BinaryRevision: "after",
		BinaryVersion: "performance", BinaryBuildDate: "2026-09-15T20:30:00Z",
		BinaryBuildMethod: performanceBuildProvenance, BinarySHA256: "after-binary",
		FixtureVersion: performanceFixture, HarnessVersion: performanceHarness,
		ChromiumVersion: "Chromium 140", Environment: environment,
		LoadAdmission: validPerformanceLoadAdmission(environment.LogicalCPU),
	}
	baseline := performanceReport{
		Mode: "baseline", SourceCommit: "main", BinaryRevision: "main",
		BinarySHA256:   "main-binary",
		FixtureVersion: performanceFixture, HarnessVersion: performanceHarness,
		ChromiumVersion: "Chromium 140", Environment: environment,
	}
	if err := validatePerformanceMetadata(valid, baseline, "after", "main"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*performanceReport, *performanceReport)
		want   string
	}{
		{
			name: "comparison source",
			mutate: func(_ *performanceReport, baseline *performanceReport) {
				baseline.SourceCommit = "unknown"
			},
			want: "baseline source commit",
		},
		{
			name: "binary revision",
			mutate: func(report *performanceReport, _ *performanceReport) {
				report.BinaryRevision = "other"
			},
			want: "performance binary revision",
		},
		{
			name: "dirty source",
			mutate: func(report *performanceReport, _ *performanceReport) {
				report.SourceModified = true
			},
			want: "source worktree was modified",
		},
		{
			name: "dirty binary",
			mutate: func(report *performanceReport, _ *performanceReport) {
				report.BinaryModified = true
			},
			want: "dirty worktree",
		},
		{
			name: "build date",
			mutate: func(report *performanceReport, _ *performanceReport) {
				report.BinaryBuildDate = "2026-09-15T20:31:00Z"
			},
			want: "binary build date",
		},
		{
			name: "build method",
			mutate: func(report *performanceReport, _ *performanceReport) {
				report.BinaryBuildMethod = "implicit-vcs"
			},
			want: "binary build method",
		},
		{
			name: "binary",
			mutate: func(report *performanceReport, baseline *performanceReport) {
				report.BinarySHA256 = baseline.BinarySHA256
			},
			want: "binary SHA-256 values are identical",
		},
		{
			name: "fixture",
			mutate: func(_ *performanceReport, baseline *performanceReport) {
				baseline.FixtureVersion = "other"
			},
			want: "fixture versions",
		},
		{
			name: "harness",
			mutate: func(report *performanceReport, _ *performanceReport) {
				report.HarnessVersion = "other"
			},
			want: "harness versions",
		},
		{
			name: "Chromium",
			mutate: func(_ *performanceReport, baseline *performanceReport) {
				baseline.ChromiumVersion = "Chromium 139"
			},
			want: "Chromium versions differ",
		},
		{
			name: "environment",
			mutate: func(report *performanceReport, _ *performanceReport) {
				report.Environment.LogicalCPU++
				for index := range report.LoadAdmission.Observations {
					report.LoadAdmission.Observations[index].LogicalCPU++
					report.LoadAdmission.Observations[index].OneMinute =
						float64(report.LoadAdmission.Observations[index].LogicalCPU) * 0.25
				}
			},
			want: "performance environments differ",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := valid
			comparison := baseline
			test.mutate(&report, &comparison)
			err := validatePerformanceMetadata(report, comparison, "after", "main")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func validPerformanceLoadAdmission(logicalCPU int) performanceLoadAdmission {
	observations := make([]performanceLoadObservation, performanceLoadObservationCount)
	for sequence := range observations {
		observations[sequence] = performanceLoadObservation{
			Sequence:            sequence,
			OneMinute:           float64(logicalCPU) * 0.25,
			LogicalCPU:          logicalCPU,
			NormalizedOneMinute: 0.25,
		}
	}
	return performanceLoadAdmission{
		Status: performanceLoadValid,
		Policy: performanceLoadPolicy{
			Metric:                 performanceLoadMetric,
			MaxNormalizedOneMinute: performanceMaxNormalizedOneMinute,
			ObservationCount:       performanceLoadObservationCount,
			ObservationIntervalMS:  int(performanceLoadObservationDelay / time.Millisecond),
		},
		InvalidSequence: -1,
		Observations:    observations,
	}
}

func TestValidatePerformancePercentiles(t *testing.T) {
	report := performanceReport{
		Samples: map[string][]float64{"navigation_to_usable_ms": {1, 2, 3, 4}},
		P50:     map[string]float64{"navigation_to_usable_ms": 2},
		P95:     map[string]float64{"navigation_to_usable_ms": 4},
	}
	if err := validatePerformancePercentiles(report, 4); err != nil {
		t.Fatal(err)
	}
	report.P50["navigation_to_usable_ms"] = 1
	if err := validatePerformancePercentiles(report, 4); err == nil ||
		!strings.Contains(err.Error(), "p50") {
		t.Fatalf("percentile validation error = %v, want p50 mismatch", err)
	}
}

func TestAppendPerformanceSampleTracksExactProgramAndAllArtifactResponses(t *testing.T) {
	samples := map[string][]float64{}
	appendPerformanceSample(samples, browserSample{
		ProgramBytes:     512,
		ArtifactBytes:    1024,
		ArtifactRequests: 1,
	})
	for name, want := range map[string]float64{
		"program_response_bytes":  512,
		"artifact_response_bytes": 1024,
		"artifact_request_count":  1,
	} {
		if got := samples[name]; len(got) != 1 || got[0] != want {
			t.Fatalf("%s samples = %v, want [%v]", name, got, want)
		}
	}
}

func TestValidatePerformanceBudgetsEnforcesAbsoluteLimitsPerRun(t *testing.T) {
	report := performanceReport{
		Samples: map[string][]float64{
			"navigation_to_usable_ms":  {40, 45, 50},
			"program_response_bytes":   {512, 1024*1024 + 1, 512},
			"artifact_response_bytes":  {1024, 1024, 192*1024 + 1},
			"artifact_request_count":   {1, 2, 1},
			"max_long_task_ms":         {10, 101, 10},
			"total_blocking_time_ms":   {0, 0, 201},
			"click_to_drawer_ms":       {10, 10, 10},
			"click_to_file_content_ms": {10, 10, 10},
			"unchanged_reopen_ms":      {10, 10, 10},
		},
		P95: map[string]float64{
			"navigation_to_usable_ms":  50,
			"program_response_bytes":   512,
			"artifact_response_bytes":  1024,
			"artifact_request_count":   1,
			"max_long_task_ms":         10,
			"total_blocking_time_ms":   0,
			"click_to_drawer_ms":       10,
			"click_to_file_content_ms": 10,
			"unchanged_reopen_ms":      10,
		},
	}
	errs := validatePerformanceBudgets(report)
	for _, want := range []string{
		"program_response_bytes maximum",
		"artifact_response_bytes maximum",
		"artifact_request_count maximum",
		"max_long_task_ms maximum",
		"total_blocking_time_ms maximum",
	} {
		if !strings.Contains(strings.Join(errs, "\n"), want) {
			t.Errorf("budget errors = %v, want %q", errs, want)
		}
	}
}

func TestRepositoryProvenanceUsesLinkedWorktreeHEAD(t *testing.T) {
	repository := t.TempDir()
	runPerformanceTestGit(t, repository, "init", "-q")
	runPerformanceTestGit(t, repository, "config", "user.name", "Relay Test")
	runPerformanceTestGit(t, repository, "config", "user.email", "relay@example.com")
	if err := os.WriteFile(filepath.Join(repository, "fixture.txt"), []byte("primary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runPerformanceTestGit(t, repository, "add", "fixture.txt")
	runPerformanceTestGit(t, repository, "commit", "-q", "-m", "primary")
	primaryCommit := strings.TrimSpace(runPerformanceTestGit(t, repository, "rev-parse", "HEAD"))

	worktree := filepath.Join(t.TempDir(), "linked")
	runPerformanceTestGit(t, repository, "worktree", "add", "-q", "-b", "linked", worktree)
	if err := os.WriteFile(filepath.Join(worktree, "fixture.txt"), []byte("linked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runPerformanceTestGit(t, worktree, "add", "fixture.txt")
	runPerformanceTestGit(t, worktree, "commit", "-q", "-m", "linked")
	linkedCommit := strings.TrimSpace(runPerformanceTestGit(t, worktree, "rev-parse", "HEAD"))
	if linkedCommit == primaryCommit {
		t.Fatal("linked worktree did not advance beyond the primary worktree")
	}

	provenance, err := readRepositoryProvenance(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if provenance.Commit != linkedCommit {
		t.Fatalf("source commit = %q, want linked worktree HEAD %q", provenance.Commit, linkedCommit)
	}
	if provenance.Modified {
		t.Fatal("clean linked worktree reported as modified")
	}
	if provenance.CommitTime.IsZero() {
		t.Fatal("source commit time is missing")
	}
	if err := os.WriteFile(filepath.Join(worktree, "fixture.txt"), []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provenance, err = readRepositoryProvenance(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if !provenance.Modified {
		t.Fatal("modified linked worktree reported as clean")
	}
}

func TestPerformanceBuildArgumentsUseExplicitReproducibleProvenance(t *testing.T) {
	provenance := repositoryProvenance{
		Commit:     "0123456789abcdef",
		CommitTime: time.Date(2026, time.September, 15, 20, 30, 0, 0, time.UTC),
	}
	args := performanceBuildArguments("/tmp/relay", provenance)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-buildvcs=false",
		"-trimpath",
		"github.com/ronaknnathani/relay/internal/cli.version=performance",
		"github.com/ronaknnathani/relay/internal/cli.commit=" + provenance.Commit,
		"github.com/ronaknnathani/relay/internal/cli.date=2026-09-15T20:30:00Z",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("build arguments %q do not contain %q", joined, want)
		}
	}
}

func TestEvaluatePerformanceLoadRejectsWholeRunWithoutDroppingObservations(t *testing.T) {
	policy := performanceLoadPolicy{
		Metric:                 "one_minute_load_average_per_logical_cpu",
		MaxNormalizedOneMinute: 0.75,
		ObservationCount:       3,
		ObservationIntervalMS:  1000,
	}
	observations := []performanceLoadObservation{
		{Sequence: 0, OneMinute: 4.5, LogicalCPU: 10, NormalizedOneMinute: 0.45},
		{Sequence: 1, OneMinute: 8, LogicalCPU: 10, NormalizedOneMinute: 0.8},
		{Sequence: 2, OneMinute: 4, LogicalCPU: 10, NormalizedOneMinute: 0.4},
	}
	admission := evaluatePerformanceLoad(policy, observations)
	if admission.Status != performanceLoadInvalid {
		t.Fatalf("load status = %q, want %q", admission.Status, performanceLoadInvalid)
	}
	if len(admission.Observations) != len(observations) {
		t.Fatalf("recorded observations = %d, want %d", len(admission.Observations), len(observations))
	}
	if admission.InvalidSequence != 1 {
		t.Fatalf("invalid sequence = %d, want 1", admission.InvalidSequence)
	}
	if !strings.Contains(admission.Reason, "0.800") ||
		!strings.Contains(admission.Reason, "0.750") {
		t.Fatalf("load rejection reason = %q, want observed and allowed normalized load", admission.Reason)
	}
}

func TestEvaluatePerformanceLoadAcceptsEveryObservationAtOrBelowPolicy(t *testing.T) {
	policy := performanceLoadPolicy{
		Metric:                 "one_minute_load_average_per_logical_cpu",
		MaxNormalizedOneMinute: 0.75,
		ObservationCount:       2,
		ObservationIntervalMS:  1000,
	}
	observations := []performanceLoadObservation{
		{Sequence: 0, OneMinute: 7.5, LogicalCPU: 10, NormalizedOneMinute: 0.75},
		{Sequence: 1, OneMinute: 4, LogicalCPU: 10, NormalizedOneMinute: 0.4},
	}
	admission := evaluatePerformanceLoad(policy, observations)
	if admission.Status != performanceLoadValid {
		t.Fatalf("load status = %q, want %q: %s", admission.Status, performanceLoadValid, admission.Reason)
	}
}

func runPerformanceTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
