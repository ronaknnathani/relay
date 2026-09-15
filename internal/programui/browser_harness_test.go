package programui

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const (
	performanceRuns       = 20
	performanceAgentDelay = 900 * time.Millisecond
)

type performanceReport struct {
	Mode        string               `json:"mode"`
	GeneratedAt string               `json:"generated_at"`
	Samples     map[string][]float64 `json:"samples"`
	Percentiles map[string]float64   `json:"p95"`
}

type browserSample struct {
	ProcessStartToURL  float64
	NavigationToUsable float64
	InitialAPITTFB     float64
	InitialAPITotal    float64
	InitialAPIBytes    float64
	ClickToDrawer      float64
	ClickToFileContent float64
	IncrementalBytes   float64
	MaxLongTask        float64
	TotalBlockingTime  float64
	UnchangedReopen    float64
}

func measureProgramUI(t *testing.T, mode string) performanceReport {
	t.Helper()
	if mode != "baseline" && mode != "verify" {
		t.Fatalf("RELAY_BROWSER_PERF = %q, want baseline or verify", mode)
	}
	fixture := newReferenceProgramFixture(t)
	buildRelayForPerformance(t)
	allocator, cancelAllocator := chromedp.NewExecAllocator(
		context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromeExecutable(t)),
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("no-first-run", true),
			chromedp.Flag("disable-background-networking", true),
		)...,
	)
	defer cancelAllocator()
	browser, cancelBrowser := chromedp.NewContext(allocator)
	defer cancelBrowser()
	if err := chromedp.Run(browser); err != nil {
		t.Fatalf("start Chromium: %v", err)
	}

	report := performanceReport{
		Mode: mode, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Samples: map[string][]float64{}, Percentiles: map[string]float64{},
	}
	for run := 0; run <= performanceRuns; run++ {
		sample := measureBrowserRun(t, browser, fixture.program.Slug)
		if run == 0 {
			continue
		}
		appendPerformanceSample(report.Samples, sample)
	}
	for name, samples := range report.Samples {
		report.Percentiles[name] = percentile(samples, 0.95)
	}
	return report
}

func measureBrowserRun(t *testing.T, browser context.Context, slug string) browserSample {
	t.Helper()
	release := make(chan struct{})
	time.AfterFunc(performanceAgentDelay, func() { close(release) })
	output := newLineWriter()
	serverContext, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	started := time.Now()
	go func() {
		serverDone <- Serve(serverContext, Options{
			Slug: slug, Port: 0, Open: false, Out: output,
			Agents: &controlledAgentLister{release: release},
		})
	}()
	url := waitForProgramURL(t, output, serverDone)
	processStartToURL := durationMilliseconds(time.Since(started))

	tab, cancelTab := chromedp.NewContext(browser)
	defer cancelTab()
	tab, cancelTimeout := context.WithTimeout(tab, 20*time.Second)
	defer cancelTimeout()
	if err := chromedp.Run(tab, chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(`
			window.__relayLongTasks = [];
			new PerformanceObserver((list) => {
				for (const entry of list.getEntries()) {
					window.__relayLongTasks.push(entry.duration);
				}
			}).observe({type: "longtask", buffered: true});
		`).Do(ctx)
		return err
	})); err != nil {
		t.Fatalf("install performance observer: %v", err)
	}
	if err := chromedp.Run(tab,
		chromedp.Navigate(url),
		chromedp.Poll(`document.querySelectorAll(".card").length === 100 &&
			document.querySelector("#program-title").textContent === "Reference Program"`, nil),
	); err != nil {
		t.Fatalf("navigate to usable program UI: %v", err)
	}
	navigationToUsable := durationMilliseconds(time.Since(started))

	var initial resourceTiming
	if err := chromedp.Run(tab, chromedp.Evaluate(`
		(() => {
			const entries = performance.getEntriesByType("resource")
				.filter((entry) => new URL(entry.name).pathname === "/api/program");
			const entry = entries[entries.length - 1];
			return entry ? {
				ttfb: entry.responseStart - entry.startTime,
				total: entry.responseEnd - entry.startTime,
				bytes: entry.encodedBodySize
			} : {ttfb: 0, total: 0, bytes: 0};
		})()
	`, &initial)); err != nil {
		t.Fatalf("read initial API timing: %v", err)
	}

	beforeBytes := resourceBytes(t, tab)
	drawerStarted := time.Now()
	if err := chromedp.Run(tab,
		chromedp.Click(`.card[data-item="w1"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector("#drawer").dataset.state === "open" &&
			document.querySelector("#drawer-title").textContent === "Reference task 001"`, nil),
	); err != nil {
		t.Fatalf("open task drawer: %v", err)
	}
	clickToDrawer := durationMilliseconds(time.Since(drawerStarted))
	fileStarted := time.Now()
	if err := chromedp.Run(tab,
		chromedp.Poll(`(() => {
			return Array.from(document.querySelectorAll(".artifact-text"))
				.some((artifact) => artifact.textContent.length === 131072);
		})()`, nil),
	); err != nil {
		t.Fatalf("wait for selected artifact: %v", err)
	}
	clickToFileContent := durationMilliseconds(time.Since(fileStarted))
	incrementalBytes := resourceBytes(t, tab) - beforeBytes

	if err := chromedp.Run(tab,
		chromedp.Evaluate(`document.dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true}))`, nil),
		chromedp.Poll(`document.querySelector("#drawer").hidden === true`, nil),
	); err != nil {
		t.Fatalf("close task drawer: %v", err)
	}
	reopenStarted := time.Now()
	if err := chromedp.Run(tab,
		chromedp.Click(`.card[data-item="w1"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector("#drawer").dataset.state === "open" &&
			Array.from(document.querySelectorAll(".artifact-text"))
				.some((artifact) => artifact.textContent.length === 131072)`, nil),
	); err != nil {
		t.Fatalf("reopen task drawer: %v", err)
	}
	unchangedReopen := durationMilliseconds(time.Since(reopenStarted))

	var longTasks []float64
	if err := chromedp.Run(tab, chromedp.Evaluate(`window.__relayLongTasks || []`, &longTasks)); err != nil {
		t.Fatalf("read long tasks: %v", err)
	}
	maxLongTask := 0.0
	totalBlockingTime := 0.0
	for _, duration := range longTasks {
		maxLongTask = math.Max(maxLongTask, duration)
		if duration > 50 {
			totalBlockingTime += duration - 50
		}
	}

	cancelServer()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("stop program UI: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("program UI did not stop")
	}
	return browserSample{
		ProcessStartToURL: processStartToURL, NavigationToUsable: navigationToUsable,
		InitialAPITTFB: initial.TTFB, InitialAPITotal: initial.Total,
		InitialAPIBytes: initial.Bytes, ClickToDrawer: clickToDrawer,
		ClickToFileContent: clickToFileContent, IncrementalBytes: incrementalBytes,
		MaxLongTask: maxLongTask, TotalBlockingTime: totalBlockingTime,
		UnchangedReopen: unchangedReopen,
	}
}

type resourceTiming struct {
	TTFB  float64 `json:"ttfb"`
	Total float64 `json:"total"`
	Bytes float64 `json:"bytes"`
}

func waitForProgramURL(t *testing.T, output *lineWriter, serverDone <-chan error) string {
	t.Helper()
	select {
	case line := <-output.lines:
		return strings.TrimSpace(line)
	case err := <-serverDone:
		t.Fatalf("program UI stopped before publishing URL: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("program UI did not publish URL")
	}
	return ""
}

func resourceBytes(t *testing.T, ctx context.Context) float64 {
	t.Helper()
	var bytes float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(`
		performance.getEntriesByType("resource")
			.reduce((sum, entry) => sum + entry.encodedBodySize, 0)
	`, &bytes)); err != nil {
		t.Fatalf("read resource bytes: %v", err)
	}
	return bytes
}

func buildRelayForPerformance(t *testing.T) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "relay")
	command := exec.Command("go", "build", "-o", binary, "./cmd/relay")
	command.Dir = filepath.Join("..", "..")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build relay for performance test: %v\n%s", err, output)
	}
}

func chromeExecutable(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Skip("Chrome or Chromium is required for browser performance tests")
	return ""
}

func appendPerformanceSample(samples map[string][]float64, sample browserSample) {
	values := map[string]float64{
		"process_start_to_url_ms":  sample.ProcessStartToURL,
		"navigation_to_usable_ms":  sample.NavigationToUsable,
		"initial_api_ttfb_ms":      sample.InitialAPITTFB,
		"initial_api_total_ms":     sample.InitialAPITotal,
		"initial_api_bytes":        sample.InitialAPIBytes,
		"click_to_drawer_ms":       sample.ClickToDrawer,
		"click_to_file_content_ms": sample.ClickToFileContent,
		"incremental_bytes":        sample.IncrementalBytes,
		"max_long_task_ms":         sample.MaxLongTask,
		"total_blocking_time_ms":   sample.TotalBlockingTime,
		"unchanged_reopen_ms":      sample.UnchangedReopen,
	}
	for name, value := range values {
		samples[name] = append(samples[name], value)
	}
}

func percentile(values []float64, quantile float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	if len(sorted) == 0 {
		return 0
	}
	index := int(math.Ceil(quantile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

func verifyPerformanceReport(t *testing.T, report performanceReport, baselinePath string) {
	t.Helper()
	if baselinePath == "" {
		t.Fatal("RELAY_PERF_BASELINE is required in verify mode")
	}
	data, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("read baseline %s: %v", baselinePath, err)
	}
	var baseline performanceReport
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatalf("decode baseline %s: %v", baselinePath, err)
	}
	budgets := map[string]float64{
		"navigation_to_usable_ms":  1000,
		"click_to_drawer_ms":       100,
		"click_to_file_content_ms": 500,
		"unchanged_reopen_ms":      100,
		"initial_api_bytes":        1024 * 1024,
		"incremental_bytes":        192 * 1024,
		"max_long_task_ms":         100,
		"total_blocking_time_ms":   200,
	}
	for name, budget := range budgets {
		if got := report.Percentiles[name]; got > budget {
			t.Errorf("%s p95 = %.2f, want <= %.2f", name, got, budget)
		}
	}
	baselineNavigation := baseline.Percentiles["navigation_to_usable_ms"]
	if baselineNavigation <= 0 {
		t.Fatal("baseline navigation_to_usable_ms p95 is missing")
	}
	if got := report.Percentiles["navigation_to_usable_ms"]; got > baselineNavigation*0.5 {
		t.Errorf("navigation_to_usable_ms p95 = %.2f, want <= 50%% of baseline %.2f", got, baselineNavigation)
	}
	for name, samples := range report.Samples {
		if len(samples) != performanceRuns {
			t.Errorf("%s samples = %d, want %d", name, len(samples), performanceRuns)
		}
	}
}

func durationMilliseconds(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / 1000
}

func (report performanceReport) String() string {
	return fmt.Sprintf("%s performance report with %d metrics", report.Mode, len(report.Samples))
}
