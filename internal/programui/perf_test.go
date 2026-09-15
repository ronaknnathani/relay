package programui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
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
		Mode: "verify", SourceCommit: "after", BinaryRevision: "after",
		BinarySHA256:   "after-binary",
		FixtureVersion: performanceFixture, HarnessVersion: performanceHarness,
		ChromiumVersion: "Chromium 140", Environment: environment,
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
			name: "dirty binary",
			mutate: func(report *performanceReport, _ *performanceReport) {
				report.BinaryModified = true
			},
			want: "dirty worktree",
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
