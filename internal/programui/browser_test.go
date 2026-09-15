package programui

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/programview"
)

func TestBrowserShowsSixSecondExternalRefreshProvenance(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	for _, delayedSource := range []string{"GitHub", "Herdr"} {
		t.Run(delayedSource, func(t *testing.T) {
			fixture := newReferenceProgramFixture(t)
			worktree := filepath.Join(
				fixture.program.Repo, ".worktrees", fixture.program.Items[0].ProjectSlug,
			)
			githubDelay := time.Duration(0)
			herdrDelay := time.Duration(0)
			if delayedSource == "GitHub" {
				githubDelay = 6 * time.Second
			} else {
				herdrDelay = 6 * time.Second
			}
			output := newLineWriter()
			serverContext, cancelServer := context.WithCancel(context.Background())
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- Serve(serverContext, Options{
					Slug: fixture.program.Slug, Port: 0, Open: false, Out: output,
					GitHub: &controlledFetcher{
						delay: githubDelay,
						result: programview.PullRequestDTO{
							Number: 42, Ref: "#42", State: "open", Title: "Delayed PR",
						},
					},
					Agents: &controlledAgentLister{
						delay: herdrDelay,
						agents: []herdr.Agent{{
							Status: herdr.StatusWorking, PaneID: "pane-42", CWD: worktree,
						}},
					},
				})
			}()
			t.Cleanup(func() {
				cancelServer()
				select {
				case err := <-serverDone:
					if err != nil {
						t.Errorf("Serve: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Error("program UI did not stop")
				}
			})
			url := waitForProgramURL(t, output, serverDone)

			allocator, cancelAllocator := chromedp.NewExecAllocator(
				context.Background(),
				append(chromedp.DefaultExecAllocatorOptions[:],
					chromedp.ExecPath(chromeExecutable(t)),
					chromedp.Flag("headless", true),
					chromedp.Flag("disable-gpu", true),
				)...,
			)
			defer cancelAllocator()
			browser, cancelBrowser := chromedp.NewContext(allocator)
			defer cancelBrowser()
			browser, cancelTimeout := context.WithTimeout(browser, 15*time.Second)
			defer cancelTimeout()
			if err := chromedp.Run(browser); err != nil {
				t.Fatal(err)
			}

			started := time.Now()
			if err := chromedp.Run(browser,
				chromedp.Navigate(url),
				chromedp.Poll(`document.querySelectorAll(".card").length === 100`, nil),
			); err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(started); elapsed >= time.Second {
				t.Fatalf("local UI usability = %s, want < 1s", elapsed)
			}
			if err := chromedp.Run(browser,
				chromedp.Poll(`document.querySelector("#feed-state").dataset.live === "false"`, nil),
				chromedp.Poll(`document.querySelector("#feed-state").textContent === "Live · every 3s" &&
					document.querySelector(".card[data-item='w1']").dataset.meta.includes("PR #42") &&
					document.querySelector("#worker-count").textContent === "1"`, nil),
			); err != nil {
				t.Fatalf("%s refresh: %v", delayedSource, err)
			}
		})
	}
}

func TestBrowserRetainsAndRecoversEachExternalSource(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	for _, source := range []string{"GitHub", "Herdr"} {
		t.Run(source, func(t *testing.T) {
			fixture := newReferenceProgramFixture(t)
			worktree := filepath.Join(
				fixture.program.Repo, ".worktrees", fixture.program.Items[0].ProjectSlug,
			)
			initialNow := time.Date(2026, 9, 14, 20, 0, 0, 0, time.UTC)
			var nowNanos atomic.Int64
			nowNanos.Store(initialNow.UnixNano())
			now := func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() }
			var phase atomic.Int32
			github := fetcherFunc(func(
				context.Context,
				string,
				string,
			) (programview.PullRequestDTO, error) {
				if source == "GitHub" && phase.Load() == 1 {
					return programview.PullRequestDTO{}, errors.New("GitHub unavailable")
				}
				title := "Initial PR"
				if phase.Load() == 2 {
					title = "Recovered PR"
				}
				return programview.PullRequestDTO{
					Number: 42, Ref: "#42", State: "open", Title: title,
				}, nil
			})
			agents := agentListerFunc(func() ([]herdr.Agent, error) {
				if source == "Herdr" && phase.Load() == 1 {
					return nil, errors.New("Herdr unavailable")
				}
				paneID := "pane-initial"
				if phase.Load() == 2 {
					paneID = "pane-recovered"
				}
				return []herdr.Agent{{
					Status: herdr.StatusWorking, PaneID: paneID, CWD: worktree,
				}}, nil
			})

			output := newLineWriter()
			serverContext, cancelServer := context.WithCancel(context.Background())
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- Serve(serverContext, Options{
					Slug: fixture.program.Slug, Port: 0, Open: false, Out: output,
					Now: now, GitHub: github, Agents: agents,
				})
			}()
			t.Cleanup(func() {
				cancelServer()
				select {
				case err := <-serverDone:
					if err != nil {
						t.Errorf("Serve: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Error("program UI did not stop")
				}
			})
			url := waitForProgramURL(t, output, serverDone)

			allocator, cancelAllocator := chromedp.NewExecAllocator(
				context.Background(),
				append(chromedp.DefaultExecAllocatorOptions[:],
					chromedp.ExecPath(chromeExecutable(t)),
					chromedp.Flag("headless", true),
					chromedp.Flag("disable-gpu", true),
				)...,
			)
			defer cancelAllocator()
			browser, cancelBrowser := chromedp.NewContext(allocator)
			defer cancelBrowser()
			browser, cancelTimeout := context.WithTimeout(browser, 20*time.Second)
			defer cancelTimeout()
			if err := chromedp.Run(browser,
				chromedp.Navigate(url),
				chromedp.Poll(`document.querySelector("#feed-state").textContent === "Live · every 3s" &&
					document.querySelector(".card[data-item='w1']").dataset.meta.includes("PR #42") &&
					document.querySelector("#worker-count").textContent === "1"`, nil),
				chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
				chromedp.Poll(`document.querySelector("#detail-body").textContent.includes("Initial PR") &&
					document.querySelector("#detail-body").textContent.includes("pane-initial")`, nil),
			); err != nil {
				t.Fatalf("initial %s snapshot: %v", source, err)
			}

			phase.Store(1)
			nowNanos.Store(initialNow.Add(13 * time.Second).UnixNano())
			if err := chromedp.Run(browser,
				chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
			); err != nil {
				t.Fatal(err)
			}
			if source == "GitHub" {
				if err := chromedp.Run(browser,
					chromedp.Poll(`document.querySelector("#detail-body").textContent.includes(
						"Pull request · stale GitHub cache") &&
						document.querySelector("#detail-body").textContent.includes("Initial PR") &&
						document.querySelector("#detail-body").textContent.includes("GitHub unavailable")`, nil),
				); err != nil {
					t.Fatalf("retained GitHub snapshot: %v", err)
				}
			} else {
				if err := chromedp.Run(browser,
					chromedp.Poll(`document.querySelector("#detail-body").textContent.includes("pane-initial") &&
						!document.querySelector("#warning-count").hidden`, nil),
					chromedp.Evaluate(`document.querySelector('[data-tab="goal"]').click()`, nil),
					chromedp.Evaluate(`document.querySelector("#diagnostics").open = true`, nil),
					chromedp.Poll(`document.querySelector("#warnings").textContent.includes("Herdr unavailable")`, nil),
				); err != nil {
					t.Fatalf("retained Herdr snapshot: %v", err)
				}
			}

			phase.Store(2)
			nowNanos.Store(initialNow.Add(16 * time.Second).UnixNano())
			if err := chromedp.Run(browser,
				chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
				chromedp.Poll(`document.querySelector("#feed-state").textContent === "Live · every 3s" &&
					document.querySelector("#warning-count").hidden`, nil),
				chromedp.Evaluate(`document.querySelector('[data-tab="roadmap"]').click()`, nil),
				chromedp.Poll(`document.querySelectorAll(".card").length === 100`, nil),
				chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
			); err != nil {
				t.Fatal(err)
			}
			recoveryText := "Recovered PR"
			if source == "Herdr" {
				recoveryText = "pane-recovered"
			}
			if err := chromedp.Run(browser,
				chromedp.Poll(`document.querySelector("#detail-body").textContent.includes(`+
					strconv.Quote(recoveryText)+`)`, nil),
			); err != nil {
				t.Fatalf("%s recovery: %v", source, err)
			}
		})
	}
}

func TestBrowserArtifactLoadingAndOrdering(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	snapshot := browserTestSnapshot()
	var detailItems []string
	var detailsMu sync.Mutex
	artifactCalls := make(map[string]int)
	var artifactMu sync.Mutex
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var firstOnce sync.Once
	loader := func(_ string, selector programview.ArtifactSelector) (programview.ArtifactResponse, error) {
		key := artifactCacheKeyForTest(selector)
		artifactMu.Lock()
		artifactCalls[key]++
		call := artifactCalls[key]
		artifactMu.Unlock()
		if selector.Item == "w1" {
			firstOnce.Do(func() { close(firstStarted) })
			<-releaseFirst
		}
		value := selector.Item + ":" + selector.Name
		state := programview.ArtifactStateLoaded
		switch selector.Name {
		case "assignment.md":
			switch call {
			case 2:
				value += ":updated"
			case 3:
				value += ":before-refresh"
			default:
				if call > 3 {
					value += ":refreshed"
				}
			}
		case "task.md":
			value = ""
			state = programview.ArtifactStateEmpty
		case "notes.md":
			value = ""
			state = programview.ArtifactStateMissing
		case "plan.md":
			value = "truncated-body"
			state = programview.ArtifactStateTruncated
		case "context.md":
			return programview.ArtifactResponse{
				State: programview.ArtifactStateError, Error: "cannot read context",
			}, errors.New("cannot read context")
		}
		present := state != programview.ArtifactStateMissing
		return programview.ArtifactResponse{
			State: state,
			Artifact: programview.ArtifactDTO{
				Name: selector.Name, Path: selector.Name, Present: present,
				Size: func() int64 {
					if state == programview.ArtifactStateTruncated {
						return 4096
					}
					return int64(len(value))
				}(),
				Text: &value, Truncated: state == programview.ArtifactStateTruncated,
			},
		}, nil
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(HandlerOptions{
		Slug: "browser-test", Port: port,
		Builder: func(_ string, detail string) (programview.Snapshot, error) {
			detailsMu.Lock()
			detailItems = append(detailItems, detail)
			detailsMu.Unlock()
			return snapshot, nil
		},
		ArtifactLoader: loader,
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("close browser test server: %v", err)
		}
		if err := <-done; err != nil && err != http.ErrServerClosed {
			t.Errorf("serve browser test: %v", err)
		}
	})

	allocator, cancelAllocator := chromedp.NewExecAllocator(
		context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromeExecutable(t)),
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
		)...,
	)
	defer cancelAllocator()
	browser, cancelBrowser := chromedp.NewContext(allocator)
	defer cancelBrowser()
	browser, cancelTimeout := context.WithTimeout(browser, 20*time.Second)
	defer cancelTimeout()
	url := "http://127.0.0.1:" + portText
	if err := chromedp.Run(browser,
		chromedp.Navigate(url),
		chromedp.Poll(`document.querySelectorAll(".card").length === 2`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		chromedp.Poll(`document.querySelector("#drawer-title").textContent === "First task"`, nil),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first artifact request did not start")
	}
	var loadingText string
	var artifactTextCount int
	if err := chromedp.Run(browser,
		chromedp.Text("#section-files", &loadingText, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll("#section-files .artifact-text").length`, &artifactTextCount),
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loadingText, "Loading assignment.md") || artifactTextCount != 0 {
		t.Fatalf("pre-load state = %q with %d artifact bodies", loadingText, artifactTextCount)
	}
	if err := chromedp.Run(browser,
		chromedp.Evaluate(
			`document.dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true}))`, nil,
		),
		chromedp.Poll(`document.querySelector("#drawer").hidden === true`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w2"]').click()`, nil),
		chromedp.Poll(`Array.from(document.querySelectorAll(".artifact-text"))
			.some((node) => node.textContent === "w2:assignment.md")`, nil),
	); err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)
	time.Sleep(50 * time.Millisecond)
	var body string
	if err := chromedp.Run(browser, chromedp.Text(".artifact-text", &body, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if body != "w2:assignment.md" {
		t.Fatalf("visible artifact = %q, want second selection", body)
	}
	var assignmentSection string
	if err := chromedp.Run(browser,
		chromedp.Text("#section-files", &assignmentSection, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(assignmentSection, "assignment.md · 16 B") {
		t.Fatalf("loaded artifact metadata = %q", assignmentSection)
	}
	artifactMu.Lock()
	firstAssignmentCalls := artifactCalls["task:w2:assignment.md"]
	artifactMu.Unlock()
	if firstAssignmentCalls != 1 {
		t.Fatalf("initial assignment requests = %d, want 1", firstAssignmentCalls)
	}
	if err := chromedp.Run(browser,
		chromedp.Evaluate(
			`document.dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true}))`, nil,
		),
		chromedp.Poll(`document.querySelector("#drawer").hidden === true`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		chromedp.Poll(`Array.from(document.querySelectorAll(".artifact-text"))
			.some((node) => node.textContent === "w1:assignment.md:updated")`, nil),
	); err != nil {
		t.Fatalf("reopen aborted artifact: %v", err)
	}
	artifactMu.Lock()
	reopenCalls := artifactCalls["task:w1:assignment.md"]
	artifactMu.Unlock()
	if reopenCalls != 2 {
		t.Fatalf("aborted artifact requests = %d, want 2", reopenCalls)
	}
	if err := chromedp.Run(browser,
		chromedp.Evaluate(
			`document.dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true}))`, nil,
		),
		chromedp.Poll(`document.querySelector("#drawer").hidden === true`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w2"]').click()`, nil),
		chromedp.Poll(`Array.from(document.querySelectorAll(".artifact-text"))
			.some((node) => node.textContent === "w2:assignment.md:updated")`, nil),
	); err != nil {
		t.Fatalf("revalidate cached artifact on reopen: %v", err)
	}
	artifactMu.Lock()
	reopenCalls = artifactCalls["task:w2:assignment.md"]
	artifactMu.Unlock()
	if reopenCalls != 2 {
		t.Fatalf("cached reopen requests = %d, want 2", reopenCalls)
	}
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "task.md", want: "exists but is empty"},
		{name: "notes.md", want: "The worker appends notes"},
		{name: "plan.md", want: "plan.md · 4.0 KB"},
		{name: "context.md", want: "cannot read context"},
	} {
		selector := `button[data-focus-key="art:w2:` + test.name + `"]`
		expression := `document.querySelector("#section-files").textContent.includes(` +
			strconv.Quote(test.want) + `)`
		if err := chromedp.Run(browser,
			chromedp.Click(selector, chromedp.ByQuery),
			chromedp.Poll(expression, nil),
		); err != nil {
			t.Fatalf("%s state: %v", test.name, err)
		}
		var focusKey string
		if err := chromedp.Run(browser,
			chromedp.Evaluate(`document.activeElement && document.activeElement.dataset.focusKey`, &focusKey),
		); err != nil {
			t.Fatalf("%s focus: %v", test.name, err)
		}
		if want := "art:w2:" + test.name; focusKey != want {
			t.Fatalf("%s focus = %q, want %q", test.name, focusKey, want)
		}
		if test.name == "plan.md" {
			var sectionText string
			if err := chromedp.Run(browser,
				chromedp.Text("#section-files", &sectionText, chromedp.ByQuery),
			); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(sectionText, "truncated-body") ||
				!strings.Contains(sectionText, "This file was truncated for display.") {
				t.Fatalf("truncated artifact state = %q", sectionText)
			}
		}
	}
	if err := chromedp.Run(browser,
		chromedp.Click(`button[data-focus-key="art:w2:assignment.md"]`, chromedp.ByQuery),
		chromedp.Poll(`Array.from(document.querySelectorAll(".artifact-text"))
			.some((node) => node.textContent === "w2:assignment.md:before-refresh")`, nil),
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`Array.from(document.querySelectorAll(".artifact-text"))
			.some((node) => node.textContent === "w2:assignment.md:refreshed")`, nil),
		chromedp.Text(".artifact-text", &body, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	if body != "w2:assignment.md:refreshed" {
		artifactMu.Lock()
		calls := artifactCalls["task:w2:assignment.md"]
		artifactMu.Unlock()
		t.Fatalf("revalidated artifact = %q after %d requests", body, calls)
	}
	artifactMu.Lock()
	refreshCalls := artifactCalls["task:w2:assignment.md"]
	artifactMu.Unlock()
	if refreshCalls != 4 {
		t.Fatalf("changed-on-disk refresh requests = %d, want 4", refreshCalls)
	}
	detailsMu.Lock()
	defer detailsMu.Unlock()
	for _, detail := range detailItems {
		if detail != "" {
			t.Fatalf("browser requested selected-item snapshot %q", detail)
		}
	}
}

func TestBrowserRejectsUnabortableStaleArtifactResponses(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	snapshot := browserTestSnapshot()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(HandlerOptions{
		Slug: "browser-test", Port: port,
		Builder: func(_ string, _ string) (programview.Snapshot, error) {
			return snapshot, nil
		},
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("close browser test server: %v", err)
		}
		if err := <-done; err != nil && err != http.ErrServerClosed {
			t.Errorf("serve browser test: %v", err)
		}
	})

	allocator, cancelAllocator := chromedp.NewExecAllocator(
		context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromeExecutable(t)),
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
		)...,
	)
	defer cancelAllocator()
	browser, cancelBrowser := chromedp.NewContext(allocator)
	defer cancelBrowser()
	browser, cancelTimeout := context.WithTimeout(browser, 15*time.Second)
	defer cancelTimeout()
	if err := chromedp.Run(browser, chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(`
			const relayFetch = window.fetch.bind(window);
			window.__relayPendingArtifacts = {};
			window.__relayCompletedArtifacts = {};
			window.__relayReleaseArtifacts = {};
			window.fetch = (input, options) => {
				const url = new URL(typeof input === "string" ? input : input.url, window.location.href);
				if (url.pathname !== "/api/artifact") {
					return relayFetch(input, options);
				}
				const item = url.searchParams.get("item");
				const name = url.searchParams.get("name");
				const key = item + ":" + name;
				const body = JSON.stringify({
					state: "loaded",
					artifact: {
						name,
						path: name,
						present: true,
						size: key.length,
						updated_at: "",
						truncated: false,
						text: key
					}
				});
				const response = () => new Response(body, {
					status: 200,
					headers: {"Content-Type": "application/json", "ETag": '"' + key + '"'}
				});
				if (key !== "w1:assignment.md" && key !== "w2:task.md") {
					return Promise.resolve(response());
				}
				window.__relayPendingArtifacts[key] = true;
				return new Promise((resolve) => {
					window.__relayReleaseArtifacts[key] = () => resolve(response());
				}).then((value) => {
					window.__relayCompletedArtifacts[key] = true;
					return value;
				});
			};
		`).Do(ctx)
		return err
	})); err != nil {
		t.Fatal(err)
	}

	url := "http://127.0.0.1:" + portText
	if err := chromedp.Run(browser,
		chromedp.Navigate(url),
		chromedp.Poll(`document.querySelectorAll(".card").length === 2`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		chromedp.Poll(`window.__relayPendingArtifacts["w1:assignment.md"] === true`, nil),
		chromedp.Evaluate(
			`document.dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true}))`, nil,
		),
		chromedp.Poll(`document.querySelector("#drawer").hidden === true`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w2"]').click()`, nil),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent === "w2:assignment.md"`, nil),
		chromedp.Evaluate(`window.__relayReleaseArtifacts["w1:assignment.md"]()`, nil),
		chromedp.Poll(`window.__relayCompletedArtifacts["w1:assignment.md"] === true`, nil),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent === "w2:assignment.md"`, nil),
		chromedp.Click(`button[data-focus-key="art:w2:task.md"]`, chromedp.ByQuery),
		chromedp.Poll(`window.__relayPendingArtifacts["w2:task.md"] === true`, nil),
		chromedp.Click(`button[data-focus-key="art:w2:plan.md"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent === "w2:plan.md"`, nil),
		chromedp.Evaluate(`window.__relayReleaseArtifacts["w2:task.md"]()`, nil),
		chromedp.Poll(`window.__relayCompletedArtifacts["w2:task.md"] === true`, nil),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent === "w2:plan.md"`, nil),
	); err != nil {
		t.Fatalf("stale artifact ordering: %v", err)
	}
}

func TestBrowserKeepsPendingArtifactSelectionValidAfterMetadataRefresh(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	complete := browserTestSnapshot()
	initial := complete
	initial.Items = append([]programview.ItemDTO(nil), complete.Items...)
	initial.Items[0].ChildAvailable = true
	initial.Items[0].Artifacts = nil
	now := time.Date(2026, 9, 14, 20, 0, 0, 0, time.UTC)
	var builds atomic.Int32
	url := startBrowserTestHandler(t, HandlerOptions{
		Slug: "browser-test",
		Now:  func() time.Time { return now },
		Builder: func(_ string, _ string) (programview.Snapshot, error) {
			if builds.Add(1) == 1 {
				return initial, nil
			}
			return complete, nil
		},
		ArtifactLoader: func(_ string, selector programview.ArtifactSelector) (programview.ArtifactResponse, error) {
			value := selector.Item + ":" + selector.Name
			return programview.ArtifactResponse{
				State: programview.ArtifactStateLoaded,
				Artifact: programview.ArtifactDTO{
					Name: selector.Name, Path: selector.Name, Present: true,
					Size: int64(len(value)), Text: &value,
				},
			}, nil
		},
	})

	allocator, cancelAllocator := chromedp.NewExecAllocator(
		context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromeExecutable(t)),
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
		)...,
	)
	defer cancelAllocator()
	browser, cancelBrowser := chromedp.NewContext(allocator)
	defer cancelBrowser()
	browser, cancelTimeout := context.WithTimeout(browser, 15*time.Second)
	defer cancelTimeout()
	if err := chromedp.Run(browser,
		chromedp.Navigate(url),
		chromedp.Poll(`document.querySelectorAll(".card").length === 2`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent === "w1:assignment.md"`, nil),
	); err != nil {
		t.Fatal(err)
	}
	var pendingNames []string
	if err := chromedp.Run(browser, chromedp.Evaluate(
		`Array.from(document.querySelectorAll("#section-files .link-button"), (node) => node.textContent)`,
		&pendingNames,
	)); err != nil {
		t.Fatal(err)
	}
	if len(pendingNames) != 15 || slices.Contains(pendingNames, "goal.md") ||
		slices.Contains(pendingNames, "decisions.md") {
		t.Fatalf("pending task artifact names = %v", pendingNames)
	}

	now = now.Add(3 * time.Second)
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`document.querySelector("#section-files").textContent.includes("4 of 5 files written")`, nil),
		chromedp.Poll(`document.querySelector(
			'button[data-focus-key="art:w1:assignment.md"]').getAttribute("aria-current") === "true"`, nil),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent === "w1:assignment.md"`, nil),
	); err != nil {
		t.Fatalf("metadata refresh selection: %v", err)
	}
}

func startBrowserTestHandler(t *testing.T, options HandlerOptions) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	options.Port = port
	server := &http.Server{Handler: NewHandler(options), ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("close browser test server: %v", err)
		}
		if err := <-done; err != nil && err != http.ErrServerClosed {
			t.Errorf("serve browser test: %v", err)
		}
	})
	return "http://127.0.0.1:" + portText
}

func artifactCacheKeyForTest(selector programview.ArtifactSelector) string {
	if selector.Kind == programview.ArtifactKindTask {
		return "task:" + selector.Item + ":" + selector.Name
	}
	return "contract:" + selector.Ref
}

func browserTestSnapshot() programview.Snapshot {
	artifact := func() []programview.ArtifactDTO {
		return []programview.ArtifactDTO{
			{Name: "assignment.md", Path: "assignment.md", Present: true, Size: 12},
			{Name: "task.md", Path: "task.md", Present: true},
			{Name: "notes.md", Path: "notes.md"},
			{Name: "plan.md", Path: "plan.md", Present: true, Size: 20},
			{Name: "context.md", Path: "context.md", Present: true, Size: 10},
		}
	}
	return programview.Snapshot{
		Schema: programview.SchemaVersion,
		Program: programview.ProgramDTO{
			Slug: "browser-test", Title: "Browser Test", DisplayTitle: "Browser Test",
		},
		Progress: programview.ProgressDTO{Total: 2},
		Plan: programview.PlanDTO{
			Ready: []string{}, InFlight: []string{}, Blocked: []programview.BlockedPlanDTO{},
			Orphaned: []string{}, OpenDecisions: []string{},
		},
		Graph: programview.GraphDTO{
			Nodes: []programview.GraphNodeDTO{
				{ID: "w1", Title: "First task", Lane: "dispatched"},
				{ID: "w2", Title: "Second task", Lane: "dispatched"},
			},
			Edges: []programview.GraphEdgeDTO{}, Layers: [][]string{{"w1", "w2"}},
		},
		Items: []programview.ItemDTO{
			{ID: "w1", Title: "First task", Status: "dispatched", Lane: "dispatched", Priority: "P1", Dependencies: []string{}, Dependents: []string{}, Contracts: []string{}, Notes: []string{}, Decisions: []programview.DecisionDTO{}, Artifacts: artifact(), Warnings: []string{}, Mailbox: programview.MailboxDTO{InboxIDs: []string{}, OutboxIDs: []string{}}},
			{ID: "w2", Title: "Second task", Status: "dispatched", Lane: "dispatched", Priority: "P1", Dependencies: []string{}, Dependents: []string{}, Contracts: []string{}, Notes: []string{}, Decisions: []programview.DecisionDTO{}, Artifacts: artifact(), Warnings: []string{}, Mailbox: programview.MailboxDTO{InboxIDs: []string{}, OutboxIDs: []string{}}},
		},
		Contracts: []programview.ContractDTO{}, OpenDecisions: []programview.DecisionDTO{},
		ResolvedDecisions: []programview.DecisionDTO{}, ProgramArtifacts: []programview.ArtifactDTO{},
		Warnings: []string{},
		SourceHealth: programview.SourceHealthDTO{
			Projects: programview.SourceDTO{Status: "ok", Warnings: []string{}},
			GitHub:   programview.SourceDTO{Status: "loading", Warnings: []string{}},
			Herdr:    programview.SourceDTO{Status: "loading", Warnings: []string{}},
			Mailbox:  programview.SourceDTO{Status: "ok", Warnings: []string{}},
			Patrol:   programview.SourceDTO{Status: "ok", Warnings: []string{}},
		},
	}
}

func getenv(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}
