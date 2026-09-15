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
	valid := performanceReport{
		Mode: "verify", SourceCommit: "after", BinarySHA256: "after-binary",
		FixtureVersion: performanceFixture, HarnessVersion: performanceHarness,
		ChromiumVersion: "Chromium 140",
	}
	baseline := performanceReport{
		Mode: "baseline", SourceCommit: "main", BinarySHA256: "main-binary",
		FixtureVersion: performanceFixture, HarnessVersion: performanceHarness,
		ChromiumVersion: "Chromium 140",
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
