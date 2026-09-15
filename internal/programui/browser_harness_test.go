package programui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/heapprofiler"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const (
	performanceRuns    = 40
	performanceFixture = "reference-program-v1"
	performanceHarness = "complete-roadmap-v16"
)

type performanceReport struct {
	Mode            string                 `json:"mode"`
	GeneratedAt     string                 `json:"generated_at"`
	SourceCommit    string                 `json:"source_commit"`
	BinaryRevision  string                 `json:"binary_revision"`
	BinaryModified  bool                   `json:"binary_modified"`
	BinarySHA256    string                 `json:"binary_sha256"`
	FixtureVersion  string                 `json:"fixture_version"`
	HarnessVersion  string                 `json:"harness_version"`
	ChromiumVersion string                 `json:"chromium_version"`
	Environment     performanceEnvironment `json:"environment"`
	Samples         map[string][]float64   `json:"samples"`
	P50             map[string]float64     `json:"p50"`
	P95             map[string]float64     `json:"p95"`
}

type performanceEnvironment struct {
	MachineID  string `json:"machine_id"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	CPU        string `json:"cpu"`
	LogicalCPU int    `json:"logical_cpu"`
	GoVersion  string `json:"go_version"`
}

type browserSample struct {
	ProcessStartToURL    float64
	NavigationToUsable   float64
	NavigationToGraph    float64
	NavigationToHydrated float64
	DocumentTTFB         float64
	DocumentTotal        float64
	DocumentBytes        float64
	ProgramBytes         float64
	ClickToDrawer        float64
	ClickToFileContent   float64
	ArtifactBytes        float64
	ArtifactRequests     float64
	MaxLongTask          float64
	TotalBlockingTime    float64
	UnchangedReopen      float64
}

func measureProgramUI(t *testing.T, mode string) performanceReport {
	t.Helper()
	if mode != "baseline" && mode != "verify" {
		t.Fatalf("RELAY_BROWSER_PERF = %q, want baseline or verify", mode)
	}
	fixture := newReferenceProgramFixture(t)
	binary := os.Getenv("RELAY_PERF_BINARY")
	if binary == "" {
		binary = buildRelayForPerformance(t)
	}
	commandDir := performanceCommandDir(t)
	chrome := chromeExecutable(t)
	allocator, cancelAllocator := chromedp.NewExecAllocator(
		context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chrome),
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
		BinarySHA256:   fileSHA256(t, binary),
		FixtureVersion: performanceFixture, HarnessVersion: performanceHarness,
		ChromiumVersion: chromeVersion(t, chrome),
		Environment:     currentPerformanceEnvironment(t),
		Samples:         map[string][]float64{},
		P50:             map[string]float64{},
		P95:             map[string]float64{},
	}
	report.BinaryRevision, report.BinaryModified = binaryVCS(t, binary)
	report.SourceCommit = report.BinaryRevision
	for run := 0; run <= performanceRuns; run++ {
		tab, cancelTab := chromedp.NewContext(browser)
		installPerformanceObserver(t, tab)
		sample := measureBrowserRun(t, tab, binary, commandDir, fixture.program.Slug, mode)
		cancelTab()
		if run == 0 {
			continue
		}
		appendPerformanceSample(report.Samples, sample)
	}
	for name, samples := range report.Samples {
		report.P50[name] = percentile(samples, 0.50)
		report.P95[name] = percentile(samples, 0.95)
	}
	return report
}

func installPerformanceObserver(t *testing.T, tab context.Context) {
	t.Helper()
	if err := chromedp.Run(tab, chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(`
			window.__relayLongTasks = [];
			window.__relayCompleteUsableAt = 0;
			new PerformanceObserver((list) => {
				for (const entry of list.getEntries()) {
					window.__relayLongTasks.push({
						startTime: entry.startTime,
						duration: entry.duration
					});
				}
			}).observe({type: "longtask", buffered: true});
			const relayHydrated = () => {
				if (typeof state === "undefined" ||
					state.snapshot?.schema !== "relay.program.v1" ||
					state.snapshot?.items?.length !== 100) {
					return;
				}
				window.__relayHydratedAt = performance.now();
				relayHydrationObserver.disconnect();
			};
			const relayHydrationObserver = new MutationObserver(relayHydrated);
			relayHydrationObserver.observe(document, {
				attributes: true,
				characterData: true,
				childList: true,
				subtree: true
			});
			const relayGraphComplete = () => {
				const edgeCount = Array.from(document.querySelectorAll("#graph-edges .edge"))
					.reduce((total, path) => total + Number(path.dataset.edgeCount || 1), 0);
				if (window.__relayCompleteGraphAt || window.__relayGraphPaintPending ||
					document.querySelectorAll(".card").length !== 100 ||
					edgeCount !== 200) {
					return;
				}
				window.__relayGraphPaintPending = true;
				requestAnimationFrame(() => {
					window.__relayCompleteGraphAt = performance.now();
					window.__relayGraphPaintPending = false;
					relayGraphObserver.disconnect();
				});
			};
			const relayGraphObserver = new MutationObserver(relayGraphComplete);
			relayGraphObserver.observe(document, {
				attributes: true,
				characterData: true,
				childList: true,
				subtree: true
			});
			const relayUsable = () => {
				const cards = Array.from(document.querySelectorAll(".card"));
				const refresh = document.querySelector("#refresh");
				const roadmapTab = document.querySelector("#tab-roadmap");
				if (window.__relayCompleteUsableAt > 0 ||
					(document.querySelector("#app-script") && !window.__relayCoreReady) ||
					document.querySelector("#program-title")?.textContent !== "Reference Program" ||
					!document.querySelector("#program-summary")?.textContent ||
					document.querySelector("#task-total")?.textContent !== "100" ||
					!document.querySelector("#progress-counts")?.textContent.includes("100") ||
					!document.querySelector("#roadmap-note")?.textContent ||
					cards.length !== 100 ||
					!cards.every((card) => card.dataset.focusKey &&
						card.textContent.includes("Reference task")) ||
					document.querySelectorAll("#graph-nodes .stage").length === 0 ||
					!document.querySelector("#graph")?.getAttribute("aria-label")?.includes(
						"100 tasks, 200 dependency links") ||
					!refresh || refresh.disabled ||
					!roadmapTab) {
					return;
				}
				if (!window.__relayUsabilityProbe) {
					const first = cards[0];
					const second = cards[1];
					first.focus({preventScroll: true});
					window.__relayUsabilityProbe = {
						target: second.dataset.item,
						prevented: !first.dispatchEvent(new KeyboardEvent("keydown", {
							key: "ArrowRight", bubbles: true, cancelable: true
						}))
					};
					return;
				}
				const probe = window.__relayUsabilityProbe;
				const selected = document.querySelector(
					'.card[data-item="' + probe.target + '"]');
				if (probe.prevented &&
					selected?.dataset.selected === "true" &&
					document.activeElement === selected &&
					roadmapTab.getAttribute("aria-selected") === "true" &&
					document.querySelector("#panel-roadmap")?.hidden === false) {
					requestAnimationFrame(() => {
						if (roadmapTab.getAttribute("aria-selected") === "true" &&
							document.querySelector("#panel-roadmap")?.hidden === false &&
							selected.dataset.selected === "true") {
							window.__relayCompleteUsableAt = performance.now();
							relayObserver.disconnect();
						}
					});
				}
			};
			const relayObserver = new MutationObserver(relayUsable);
			relayObserver.observe(document, {
				attributes: true,
				characterData: true,
				childList: true,
				subtree: true
			});
			queueMicrotask(relayHydrated);
			queueMicrotask(relayGraphComplete);
			queueMicrotask(relayUsable);
		`).Do(ctx)
		return err
	})); err != nil {
		t.Fatalf("install performance observer: %v", err)
	}
}

func measureBrowserRun(
	t *testing.T,
	browser context.Context,
	binary string,
	commandDir string,
	slug string,
	mode string,
) browserSample {
	t.Helper()
	output := newLineWriter()
	var stderr bytes.Buffer
	command := exec.Command(binary, "program", "ui", slug, "--port", "0", "--no-open")
	command.Env = environmentWithPath(commandDir)
	command.Stdout = output
	command.Stderr = &stderr
	serverDone := make(chan error, 1)
	started := time.Now()
	if err := command.Start(); err != nil {
		t.Fatalf("start release binary: %v", err)
	}
	go func() {
		serverDone <- command.Wait()
	}()
	url := waitForProgramURL(t, output, serverDone)
	processStartToURL := durationMilliseconds(time.Since(started))

	tab, cancelTimeout := context.WithTimeout(browser, 20*time.Second)
	defer cancelTimeout()
	if err := chromedp.Run(tab, chromedp.ActionFunc(func(ctx context.Context) error {
		return heapprofiler.CollectGarbage().Do(ctx)
	})); err != nil {
		t.Fatalf("collect browser garbage before navigation: %v", err)
	}
	navigationStarted := time.Now()
	if err := chromedp.Run(tab,
		chromedp.Navigate(url),
		waitForBrowserCondition(`window.__relayCompleteUsableAt > 0`),
	); err != nil {
		var diagnostic string
		_ = chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify({
			title: document.querySelector("#program-title")?.textContent,
			summary: document.querySelector("#program-summary")?.textContent,
			total: document.querySelector("#task-total")?.textContent,
			progress: document.querySelector("#progress-counts")?.textContent,
			note: document.querySelector("#roadmap-note")?.textContent,
			cards: document.querySelectorAll(".card").length,
			stages: document.querySelectorAll("#graph-nodes .stage").length,
			edges: Array.from(document.querySelectorAll("#graph-edges .edge"))
				.reduce((total, path) => total + Number(path.dataset.edgeCount || 1), 0),
			graphLabel: document.querySelector("#graph")?.getAttribute("aria-label"),
			schema: typeof state === "undefined" ? "" : state.snapshot?.schema,
			items: typeof state === "undefined" ? 0 : state.snapshot?.items?.length,
			selected: document.querySelector(".card[data-selected='true']")?.dataset.item,
			focused: document.activeElement?.dataset?.item,
			roadmapSelected: document.querySelector("#tab-roadmap")?.getAttribute("aria-selected"),
			roadmapHidden: document.querySelector("#panel-roadmap")?.hidden,
			probe: window.__relayUsabilityProbe,
			usable: window.__relayCompleteUsableAt
		})`, &diagnostic))
		t.Fatalf("navigate to usable program UI: %v: %s", err, diagnostic)
	}
	var navigationToUsable float64
	if err := chromedp.Run(tab,
		chromedp.Evaluate(`window.__relayCompleteUsableAt`, &navigationToUsable),
	); err != nil {
		t.Fatalf("read navigation-to-usable timing: %v", err)
	}
	if navigationToUsable <= 0 {
		navigationToUsable = durationMilliseconds(time.Since(navigationStarted))
	}
	if err := chromedp.Run(tab, waitForBrowserCondition(`
		typeof state !== "undefined" &&
		state.snapshot?.schema === "relay.program.v1" &&
		state.snapshot?.items?.length === 100 &&
		window.__relayCompleteGraphAt > 0 &&
		performance.getEntriesByType("resource").some((candidate) => {
			const url = new URL(candidate.name);
			return url.pathname === "/api/program" && url.search === "";
		})
	`)); err != nil {
		t.Fatalf("wait for full program hydration: %v", err)
	}
	var navigationToGraph float64
	var navigationToHydrated float64
	if err := chromedp.Run(tab,
		chromedp.Evaluate(`window.__relayCompleteGraphAt`, &navigationToGraph),
		chromedp.Evaluate(`window.__relayHydratedAt`, &navigationToHydrated),
	); err != nil {
		t.Fatalf("read navigation-to-hydrated timing: %v", err)
	}

	var documentTiming resourceTiming
	var programTiming resourceTiming
	if err := chromedp.Run(tab, chromedp.Evaluate(`
		(() => {
			const entry = performance.getEntriesByType("navigation")[0];
			return entry ? {
				ttfb: entry.responseStart - entry.startTime,
				total: entry.responseEnd - entry.startTime,
				bytes: entry.encodedBodySize
			} : {ttfb: 0, total: 0, bytes: 0};
		})()
	`, &documentTiming), chromedp.Evaluate(`
		(() => {
			const entry = performance.getEntriesByType("resource").find((candidate) => {
				const url = new URL(candidate.name);
				return url.pathname === "/api/program" && url.search === "";
			});
			return entry ? {
				ttfb: entry.responseStart - entry.startTime,
				total: entry.responseEnd - entry.startTime,
				bytes: entry.encodedBodySize
			} : {ttfb: 0, total: 0, bytes: 0};
		})()
	`, &programTiming)); err != nil {
		t.Fatalf("read initial resource timings: %v", err)
	}
	var taskArtifactTextPresent bool
	if err := chromedp.Run(tab, chromedp.Evaluate(`
		state.snapshot.items.some((item) =>
			(item.artifacts || []).some((artifact) => artifact.text !== undefined))
	`, &taskArtifactTextPresent)); err != nil {
		t.Fatalf("inspect initial program payload: %v", err)
	}
	if mode == "verify" && taskArtifactTextPresent {
		t.Fatal("initial /api/program response included task artifact text")
	}

	if err := chromedp.Run(tab, chromedp.Evaluate(`performance.clearResourceTimings()`, nil)); err != nil {
		t.Fatalf("clear resource timings before artifact request: %v", err)
	}
	drawerStarted := time.Now()
	if err := chromedp.Run(tab,
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		waitForBrowserCondition(`document.querySelector("#drawer").dataset.state === "open" &&
			document.querySelector("#drawer-title").textContent === "Reference task 001"`),
	); err != nil {
		t.Fatalf("open task drawer: %v", err)
	}
	clickToDrawer := durationMilliseconds(time.Since(drawerStarted))
	var artifactText string
	if err := chromedp.Run(tab,
		waitForBrowserCondition(`(() => {
			return Array.from(document.querySelectorAll(".artifact-text"))
				.some((artifact) => artifact.textContent.length === 131072);
		})()`),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".artifact-text"))
			.find((artifact) => artifact.textContent.length === 131072).textContent`, &artifactText),
	); err != nil {
		t.Fatalf("wait for selected artifact: %v", err)
	}
	clickToFileContent := durationMilliseconds(time.Since(drawerStarted))
	if want := strings.Repeat(`"`, 128*1024); artifactText != want {
		t.Fatalf("selected artifact content differs from deterministic fixture")
	}
	var artifactResources struct {
		Count int     `json:"count"`
		Bytes float64 `json:"bytes"`
	}
	if err := chromedp.Run(tab, chromedp.Evaluate(`
		(() => {
			const entries = performance.getEntriesByType("resource")
				.filter((entry) => new URL(entry.name).pathname === "/api/artifact");
			return {
				count: entries.length,
				bytes: entries.reduce((total, entry) => total + entry.encodedBodySize, 0)
			};
		})()
	`, &artifactResources)); err != nil {
		t.Fatalf("read artifact response timings: %v", err)
	}

	if err := chromedp.Run(tab,
		chromedp.Evaluate(`document.dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true}))`, nil),
		waitForBrowserCondition(`document.querySelector("#drawer").hidden === true`),
	); err != nil {
		t.Fatalf("close task drawer: %v", err)
	}
	reopenStarted := time.Now()
	if err := chromedp.Run(tab,
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		waitForBrowserCondition(`document.querySelector("#drawer").dataset.state === "open" &&
			Array.from(document.querySelectorAll(".artifact-text"))
				.some((artifact) => artifact.textContent.length === 131072)`),
	); err != nil {
		t.Fatalf("reopen task drawer: %v", err)
	}
	unchangedReopen := durationMilliseconds(time.Since(reopenStarted))

	var longTasks []struct {
		StartTime float64 `json:"startTime"`
		Duration  float64 `json:"duration"`
	}
	if err := chromedp.Run(tab, chromedp.Evaluate(`window.__relayLongTasks || []`, &longTasks)); err != nil {
		t.Fatalf("read long tasks: %v", err)
	}
	maxLongTask := 0.0
	totalBlockingTime := 0.0
	for _, task := range longTasks {
		if task.StartTime > navigationToUsable {
			continue
		}
		maxLongTask = math.Max(maxLongTask, task.Duration)
		if task.Duration > 50 {
			totalBlockingTime += task.Duration - 50
		}
	}

	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("stop program UI process: %v", err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("stop program UI: %v: %s", err, strings.TrimSpace(stderr.String()))
		}
	case <-time.After(8 * time.Second):
		if err := command.Process.Kill(); err != nil {
			t.Fatalf("kill unresponsive program UI: %v", err)
		}
		<-serverDone
		t.Fatal("program UI did not stop")
	}
	return browserSample{
		ProcessStartToURL: processStartToURL, NavigationToUsable: navigationToUsable,
		NavigationToGraph:    navigationToGraph,
		NavigationToHydrated: navigationToHydrated,
		DocumentTTFB:         documentTiming.TTFB, DocumentTotal: documentTiming.Total,
		DocumentBytes: documentTiming.Bytes, ProgramBytes: programTiming.Bytes,
		ClickToDrawer: clickToDrawer, ClickToFileContent: clickToFileContent,
		ArtifactBytes:    artifactResources.Bytes,
		ArtifactRequests: float64(artifactResources.Count),
		MaxLongTask:      maxLongTask, TotalBlockingTime: totalBlockingTime,
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
	case <-time.After(10 * time.Second):
		t.Fatal("program UI did not publish URL")
	}
	return ""
}

func waitForBrowserCondition(expression string) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(`
		new Promise((resolve) => {
			const ready = () => Boolean(%s);
			if (ready()) {
				resolve(true);
				return;
			}
			const observer = new MutationObserver(() => {
				if (ready()) {
					observer.disconnect();
					resolve(true);
				}
			});
			observer.observe(document.documentElement, {
				attributes: true,
				characterData: true,
				childList: true,
				subtree: true
			});
		})
	`, expression), nil, func(params *runtime.EvaluateParams) *runtime.EvaluateParams {
		return params.WithAwaitPromise(true)
	})
}

func buildRelayForPerformance(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "relay")
	command := exec.Command(
		"go", "build", "-buildvcs=true", "-trimpath", "-ldflags=-s -w",
		"-o", binary, "./cmd/relay",
	)
	command.Dir = filepath.Join("..", "..")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build relay for performance test: %v\n%s", err, output)
	}
	return binary
}

func performanceCommandDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	script := `#!/bin/sh
printf '%s\n' '{"number":42,"url":"https://github.example/pr/42","state":"OPEN","isDraft":false,"mergeable":"MERGEABLE","reviewDecision":"APPROVED","statusCheckRollup":[],"title":"Fixture PR","updatedAt":"2026-09-14T20:00:00Z"}'
`
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return dir
}

func environmentWithPath(path string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PATH=") {
			environment = append(environment, entry)
		}
	}
	return append(environment, "PATH="+path)
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
		"process_start_to_url_ms":   sample.ProcessStartToURL,
		"navigation_to_usable_ms":   sample.NavigationToUsable,
		"navigation_to_graph_ms":    sample.NavigationToGraph,
		"navigation_to_hydrated_ms": sample.NavigationToHydrated,
		"document_ttfb_ms":          sample.DocumentTTFB,
		"document_total_ms":         sample.DocumentTotal,
		"document_bytes":            sample.DocumentBytes,
		"program_response_bytes":    sample.ProgramBytes,
		"click_to_drawer_ms":        sample.ClickToDrawer,
		"click_to_file_content_ms":  sample.ClickToFileContent,
		"artifact_response_bytes":   sample.ArtifactBytes,
		"artifact_request_count":    sample.ArtifactRequests,
		"max_long_task_ms":          sample.MaxLongTask,
		"total_blocking_time_ms":    sample.TotalBlockingTime,
		"unchanged_reopen_ms":       sample.UnchangedReopen,
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
	if err := validatePerformanceMetadata(
		report,
		baseline,
		repositoryCommit(t, "HEAD"),
		repositoryMergeBase(t, "HEAD", "origin/main"),
	); err != nil {
		t.Fatal(err)
	}
	if err := validatePerformancePercentiles(report, performanceRuns); err != nil {
		t.Fatal(err)
	}
	if err := validatePerformancePercentiles(baseline, performanceRuns); err != nil {
		t.Fatalf("baseline report: %v", err)
	}
	budgets := map[string]float64{
		"navigation_to_usable_ms":  1000,
		"navigation_to_graph_ms":   1000,
		"click_to_drawer_ms":       100,
		"click_to_file_content_ms": 500,
		"unchanged_reopen_ms":      100,
		"program_response_bytes":   1024 * 1024,
		"artifact_response_bytes":  192 * 1024,
		"artifact_request_count":   1,
		"max_long_task_ms":         100,
		"total_blocking_time_ms":   200,
	}
	for name, budget := range budgets {
		if got := report.P95[name]; got > budget {
			t.Errorf("%s p95 = %.2f, want <= %.2f", name, got, budget)
		}
	}
	baselineNavigation := baseline.P95["navigation_to_usable_ms"]
	if baselineNavigation <= 0 {
		t.Error("baseline navigation_to_usable_ms p95 is missing")
	} else if got := report.P95["navigation_to_usable_ms"]; got > baselineNavigation*0.5 {
		t.Errorf(
			"navigation_to_usable_ms p95 = %.2f, want <= 50%% of baseline %.2f",
			got, baselineNavigation,
		)
	}
	for _, required := range []string{
		"document_bytes",
		"program_response_bytes",
		"artifact_response_bytes",
		"artifact_request_count",
	} {
		if report.P50[required] <= 0 || report.P95[required] <= 0 {
			t.Errorf("%s exact resource measurements are missing", required)
		}
	}
	for name, samples := range report.Samples {
		if len(samples) != performanceRuns {
			t.Errorf("%s samples = %d, want %d", name, len(samples), performanceRuns)
		}
	}
}

func validatePerformancePercentiles(report performanceReport, sampleCount int) error {
	for name, samples := range report.Samples {
		if len(samples) != sampleCount {
			return fmt.Errorf("%s samples = %d, want %d", name, len(samples), sampleCount)
		}
		for _, quantile := range []struct {
			name  string
			value float64
			want  float64
		}{
			{name: "p50", value: report.P50[name], want: percentile(samples, 0.50)},
			{name: "p95", value: report.P95[name], want: percentile(samples, 0.95)},
		} {
			if math.Abs(quantile.value-quantile.want) > 0.001 {
				return fmt.Errorf(
					"%s %s = %.3f, recomputed %.3f",
					name, quantile.name, quantile.value, quantile.want,
				)
			}
		}
	}
	return nil
}

func validatePerformanceMetadata(
	report performanceReport,
	baseline performanceReport,
	currentCommit string,
	baselineCommit string,
) error {
	switch {
	case report.Mode != "verify":
		return fmt.Errorf("performance report mode = %q, want verify", report.Mode)
	case baseline.Mode != "baseline":
		return fmt.Errorf("baseline report mode = %q, want baseline", baseline.Mode)
	case report.SourceCommit != currentCommit:
		return fmt.Errorf("performance source commit = %q, want current HEAD %q", report.SourceCommit, currentCommit)
	case baseline.SourceCommit != baselineCommit:
		return fmt.Errorf("baseline source commit = %q, want origin/main %q", baseline.SourceCommit, baselineCommit)
	case report.BinaryRevision != report.SourceCommit:
		return fmt.Errorf(
			"performance binary revision = %q, want source commit %q",
			report.BinaryRevision, report.SourceCommit,
		)
	case baseline.BinaryRevision != baseline.SourceCommit:
		return fmt.Errorf(
			"baseline binary revision = %q, want source commit %q",
			baseline.BinaryRevision, baseline.SourceCommit,
		)
	case report.BinaryModified:
		return errors.New("performance binary was built from a dirty worktree")
	case baseline.BinaryModified:
		return errors.New("baseline binary was built from a dirty worktree")
	case report.BinarySHA256 == "":
		return errors.New("performance binary SHA-256 is missing")
	case baseline.BinarySHA256 == "":
		return errors.New("baseline binary SHA-256 is missing")
	case report.BinarySHA256 == baseline.BinarySHA256:
		return errors.New("performance and baseline binary SHA-256 values are identical")
	case report.FixtureVersion != performanceFixture ||
		baseline.FixtureVersion != performanceFixture:
		return fmt.Errorf(
			"fixture versions = current %q baseline %q, want %q",
			report.FixtureVersion, baseline.FixtureVersion, performanceFixture,
		)
	case report.HarnessVersion != performanceHarness ||
		baseline.HarnessVersion != performanceHarness:
		return fmt.Errorf(
			"harness versions = current %q baseline %q, want %q",
			report.HarnessVersion, baseline.HarnessVersion, performanceHarness,
		)
	case report.ChromiumVersion == "":
		return errors.New("performance Chromium version is missing")
	case baseline.ChromiumVersion == "":
		return errors.New("baseline Chromium version is missing")
	case report.ChromiumVersion != baseline.ChromiumVersion:
		return fmt.Errorf(
			"Chromium versions differ: current %q baseline %q",
			report.ChromiumVersion, baseline.ChromiumVersion,
		)
	case report.Environment != baseline.Environment:
		return fmt.Errorf(
			"performance environments differ: current %+v baseline %+v",
			report.Environment, baseline.Environment,
		)
	default:
		return nil
	}
}

func binaryVCS(t *testing.T, path string) (string, bool) {
	t.Helper()
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		t.Fatalf("read build info from performance binary %s: %v", path, err)
	}
	revision := ""
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		t.Fatalf("performance binary %s has no embedded VCS revision", path)
	}
	return revision, modified
}

func currentPerformanceEnvironment(t *testing.T) performanceEnvironment {
	t.Helper()
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("read performance host name: %v", err)
	}
	cpu := goruntime.GOARCH
	if goruntime.GOOS == "darwin" {
		if output, commandErr := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").CombinedOutput(); commandErr == nil && strings.TrimSpace(string(output)) != "" {
			cpu = strings.TrimSpace(string(output))
		}
	}
	machineDigest := sha256.Sum256([]byte(hostname))
	return performanceEnvironment{
		MachineID: fmt.Sprintf("%x", machineDigest[:8]),
		OS:        goruntime.GOOS, Arch: goruntime.GOARCH, CPU: cpu,
		LogicalCPU: goruntime.NumCPU(), GoVersion: goruntime.Version(),
	}
}

func repositoryCommit(t *testing.T, ref string) string {
	t.Helper()
	command := exec.Command("git", "rev-parse", ref)
	command.Dir = filepath.Join("..", "..")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve git commit %s: %v\n%s", ref, err, output)
	}
	return strings.TrimSpace(string(output))
}

func repositoryMergeBase(t *testing.T, left, right string) string {
	t.Helper()
	command := exec.Command("git", "merge-base", left, right)
	command.Dir = filepath.Join("..", "..")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve git merge-base %s %s: %v\n%s", left, right, err, output)
	}
	return strings.TrimSpace(string(output))
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read performance binary %s: %v", path, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func chromeVersion(t *testing.T, executable string) string {
	t.Helper()
	output, err := exec.Command(executable, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("read Chromium version from %s: %v\n%s", executable, err, output)
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		t.Fatalf("Chromium version from %s is empty", executable)
	}
	return version
}

func durationMilliseconds(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / 1000
}

func (report performanceReport) String() string {
	return fmt.Sprintf("%s performance report with %d metrics", report.Mode, len(report.Samples))
}
