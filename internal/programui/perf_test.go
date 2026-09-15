package programui

import (
	"encoding/json"
	"os"
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
