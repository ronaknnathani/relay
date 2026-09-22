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

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/programview"
)

func TestBrowserHydratesAndPollsWhenDeferredBundleFails(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	seed := browserTestSnapshot()
	seed.Items[0].Worker = nil
	full := browserTestSnapshot()
	full.Items[0].Worker = &programview.WorkerDTO{Status: "working", PaneID: "pane-live"}
	full.SourceHealth.GitHub.Status = "ok"
	full.SourceHealth.Herdr.Status = "ok"
	updated := full
	updated.Graph.Nodes = append([]programview.GraphNodeDTO(nil), full.Graph.Nodes...)
	updated.Graph.Nodes[0].Title = "Updated roadmap task"
	updated.Items = append([]programview.ItemDTO(nil), full.Items...)
	updated.Items[0].Title = "Updated roadmap task"
	updated.Progress.Total = 7

	initialNow := time.Date(2026, 9, 14, 20, 0, 0, 0, time.UTC)
	var nowNanos atomic.Int64
	nowNanos.Store(initialNow.UnixNano())
	now := func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() }
	var phase atomic.Int32
	var builds atomic.Int32
	output := newLineWriter()
	serverContext, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- Serve(serverContext, Options{
			Slug: "browser-test", Port: 0, Open: false, Out: output, Now: now,
			LocalBuilder: func(string, string) (programview.Snapshot, error) {
				return seed, nil
			},
			Builder: func(string, string) (programview.Snapshot, error) {
				builds.Add(1)
				if phase.Load() == 0 {
					return full, nil
				}
				return updated, nil
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
	browser, cancelTimeout := context.WithTimeout(browser, 25*time.Second)
	defer cancelTimeout()
	if err := chromedp.Run(browser,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(`
				window.__relayErrors = [];
				window.addEventListener("error", (event) => window.__relayErrors.push(event.message));
				window.addEventListener("unhandledrejection", (event) =>
					window.__relayErrors.push(String(event.reason)));
			`).Do(ctx)
			return err
		}),
		network.Enable(),
		network.SetBlockedURLs([]string{"*app-deferred.css", "*app-deferred.js"}),
		chromedp.Navigate(url),
		chromedp.Poll(`document.querySelector("#reconnect").textContent.includes(
			"Click Refresh to retry")`, nil),
	); err != nil {
		t.Fatalf("deferred bundle error state: %v", err)
	}
	if err := chromedp.Run(browser,
		chromedp.Poll(
			`document.querySelector("#worker-count").textContent === "1"`,
			nil,
			chromedp.WithPollingTimeout(8*time.Second),
		),
	); err != nil {
		var diagnostic string
		_ = chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify({
			worker: document.querySelector("#worker-count")?.textContent,
			feed: document.querySelector("#feed-state")?.textContent,
			reconnect: document.querySelector("#reconnect")?.textContent,
			schema: state.snapshot?.schema,
			items: state.itemsByID.size,
			errors: window.__relayErrors,
			resources: performance.getEntriesByType("resource").map((entry) => new URL(entry.name).pathname)
		})`, &diagnostic))
		t.Fatalf("full snapshot hydration after deferred failure: %v: %s", err, diagnostic)
	}
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`
			window.__relayKeyboardPrevented = [
				!document.querySelector('[role="tablist"]').dispatchEvent(
					new KeyboardEvent("keydown", {key: "ArrowRight", bubbles: true, cancelable: true})),
				!document.querySelector(".card").dispatchEvent(
					new KeyboardEvent("keydown", {key: " ", bubbles: true, cancelable: true})),
				!document.body.dispatchEvent(
					new KeyboardEvent("keydown", {key: "ArrowDown", bubbles: true, cancelable: true}))
			].every(Boolean);
			document.querySelector("#theme-toggle").click();
		`, nil),
		chromedp.Poll(`window.__relayKeyboardPrevented === true &&
			document.documentElement.dataset.theme === "dark"`, nil),
	); err != nil {
		t.Fatalf("bootstrap controls after deferred failure: %v", err)
	}

	phase.Store(1)
	nowNanos.Store(initialNow.Add(6 * time.Second).UnixNano())
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`document.querySelector("#task-total").textContent === "7" &&
			document.querySelector('.card[data-item="w1"]').textContent.includes(
				"Updated roadmap task") &&
			window.__relayErrors.length === 0 &&
			document.querySelector("#reconnect").textContent.includes(
				"Click Refresh to retry")`, nil, chromedp.WithPollingTimeout(8*time.Second)),
	); err != nil {
		var diagnostic string
		_ = chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify({
			total: document.querySelector("#task-total")?.textContent,
			card: document.querySelector('.card[data-item="w1"]')?.textContent,
			reconnect: document.querySelector("#reconnect")?.textContent,
			reconnectHidden: document.querySelector("#reconnect")?.hidden,
			signature: state.signature,
			errors: window.__relayErrors
		})`, &diagnostic))
		t.Fatalf("polling after deferred bundle failure: %v: %s", err, diagnostic)
	}
	if builds.Load() < 2 {
		t.Fatalf("snapshot builds = %d, want at least 2", builds.Load())
	}
}

func TestBrowserDeferredActionExceptionsSurfaceAndRecover(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	snapshot := browserTestSnapshot()
	snapshot.Warnings = []string{"fixture warning"}
	url := startBrowserTestFeedHandler(t, snapshot)

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
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(`
				window.__relayActionRejections = [];
				window.addEventListener("unhandledrejection", (event) => {
					window.__relayActionRejections.push(String(event.reason));
				});
			`).Do(ctx)
			return err
		}),
		chromedp.Navigate(url),
		chromedp.Poll(`!document.querySelector("#warning-count").hidden`, nil),
		chromedp.Evaluate(`document.querySelector('[data-tab="goal"]').click()`, nil),
		chromedp.Poll(`Boolean(document.querySelector("#diagnostics"))`, nil),
		chromedp.Evaluate(`
			dom.diagnostics.scrollIntoView = () => {
				throw new TypeError("deferred warning action failed");
			};
			document.querySelector("#warning-count").click();
		`, nil),
		chromedp.Poll(`window.__relayActionRejections.some((message) =>
			message.includes("deferred warning action failed"))`, nil),
		chromedp.Evaluate(`
			delete dom.diagnostics.scrollIntoView;
			dom.diagnostics.open = false;
			document.querySelector("#warning-count").click();
		`, nil),
		chromedp.Poll(`dom.diagnostics.open === true`, nil),
	); err != nil {
		var diagnostic string
		_ = chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify({
			rejections: window.__relayActionRejections,
			diagnosticsOpen: dom.diagnostics?.open,
			tab: state.tab
		})`, &diagnostic))
		t.Fatalf("deferred action exception telemetry and recovery: %v: %s", err, diagnostic)
	}
}

func TestBrowserDeepLinkedDeferredTabsWaitForAssetsAndRecover(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	tabs := []struct {
		name     string
		rendered string
	}{
		{name: "kanban", rendered: `document.querySelectorAll("#kanban-board .kanban__lane").length === 6`},
		{name: "tasks", rendered: `document.querySelector("#ledger-count").textContent === "2 of 2 tasks"`},
		{name: "decisions", rendered: `document.querySelector("#decisions-note").textContent === "Nothing is waiting on you."`},
		{name: "goal", rendered: `document.querySelector("#goal-body").textContent.includes("No program files were found on disk.")`},
	}
	for _, mode := range []string{"delayed", "blocked"} {
		for _, tab := range tabs {
			t.Run(mode+"/"+tab.name, func(t *testing.T) {
				snapshot := browserTestSnapshot()
				snapshot.SourceHealth.GitHub.Status = "ok"
				snapshot.SourceHealth.Herdr.Status = "ok"
				var release chan struct{}
				var releaseOnce sync.Once
				url := ""
				if mode == "delayed" {
					release = make(chan struct{})
					t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
					feed := newSnapshotFeed(
						snapshot,
						time.Minute,
						time.Now,
						func() (programview.Snapshot, error) { return snapshot, nil },
					)
					url = startBrowserHTTPHandler(t, func(port int) http.Handler {
						base := newHandler(
							snapshot.Program.Slug,
							strconv.Itoa(port),
							newSnapshotCache(time.Minute, time.Now, nil),
							feed,
							nil,
						)
						return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
							if request.URL.Path == "/app-deferred.css" ||
								request.URL.Path == "/app-deferred.js" {
								select {
								case <-release:
								case <-request.Context().Done():
									return
								}
							}
							base.ServeHTTP(response, request)
						})
					})
				} else {
					url = startBrowserTestFeedHandler(t, snapshot)
				}

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
				browser, cancelTimeout := context.WithTimeout(browser, 30*time.Second)
				defer cancelTimeout()

				setup := []chromedp.Action{
					chromedp.ActionFunc(func(ctx context.Context) error {
						_, err := page.AddScriptToEvaluateOnNewDocument(`
							window.__relayErrors = [];
							window.addEventListener("error", (event) => window.__relayErrors.push(event.message));
							window.addEventListener("unhandledrejection", (event) =>
								window.__relayErrors.push(String(event.reason)));
						`).Do(ctx)
						return err
					}),
				}
				if mode == "blocked" {
					setup = append(setup,
						network.Enable(),
						network.SetBlockedURLs([]string{"*app-deferred.css", "*app-deferred.js"}),
					)
				}
				deepLink := url + "/#tab=" + tab.name
				if mode == "delayed" {
					setup = append(setup, chromedp.ActionFunc(func(ctx context.Context) error {
						_, _, _, _, err := page.Navigate(deepLink).Do(ctx)
						return err
					}))
				} else {
					setup = append(setup, chromedp.Navigate(deepLink))
				}
				if err := chromedp.Run(browser, setup...); err != nil {
					t.Fatalf("navigate to %s deep link with %s assets: %v", tab.name, mode, err)
				}
				if err := chromedp.Run(browser,
					chromedp.Poll(`typeof state !== "undefined" &&
						state.snapshot?.schema === "relay.program.v1" &&
						state.failures === 0 &&
						!document.querySelector("#feed-state").textContent.includes("Reconnecting") &&
						window.__relayErrors.length === 0`, nil, chromedp.WithPollingTimeout(8*time.Second)),
				); err != nil {
					var diagnostic string
					_ = chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify({
						ready: document.readyState,
						tab: typeof state === "undefined" ? null : state.tab,
						schema: typeof state === "undefined" ? null : state.snapshot?.schema,
						feed: document.querySelector("#feed-state")?.textContent,
						reconnect: document.querySelector("#reconnect")?.textContent,
						errors: window.__relayErrors
					})`, &diagnostic))
					t.Fatalf("hydrate %s deep link with %s assets: %v: %s", tab.name, mode, err, diagnostic)
				}
				if mode == "delayed" {
					if err := chromedp.Run(browser,
						chromedp.Poll(
							`document.querySelector("#reconnect").textContent === `+
								strconv.Quote("Loading "+strings.ToUpper(tab.name[:1])+tab.name[1:]+"…")+` &&
								!document.querySelector("#reconnect").textContent.includes("Reconnecting")`,
							nil,
						),
						chromedp.ActionFunc(func(context.Context) error {
							releaseOnce.Do(func() { close(release) })
							return nil
						}),
					); err != nil {
						t.Fatalf("delayed deferred asset state for %s deep link: %v", tab.name, err)
					}
				} else {
					if err := chromedp.Run(browser,
						chromedp.Poll(`document.querySelector("#reconnect").textContent.includes(
							"Deferred") &&
							document.querySelector("#reconnect").textContent.includes(
								"failed to load. Click Refresh to retry.") &&
							!document.querySelector("#reconnect").textContent.includes("Reconnecting")`,
							nil,
						),
						network.SetBlockedURLs([]string{}),
						chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
					); err != nil {
						t.Fatalf("blocked deferred asset state for %s deep link: %v", tab.name, err)
					}
				}
				if err := chromedp.Run(browser,
					chromedp.Poll(`typeof deferredUIReady !== "undefined" &&
						deferredUIReady &&
						document.querySelector('[data-tab="`+tab.name+`"]').getAttribute("aria-selected") === "true" &&
						document.querySelector("#panel-`+tab.name+`").hidden === false &&
						`+tab.rendered+` &&
						document.querySelector("#reconnect").hidden &&
						window.__relayErrors.length === 0`, nil, chromedp.WithPollingTimeout(8*time.Second)),
				); err != nil {
					var diagnostic string
					_ = chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify({
						tab: state.tab,
						schema: state.snapshot?.schema,
						deferred: deferredUIReady,
						feed: document.querySelector("#feed-state")?.textContent,
						reconnect: document.querySelector("#reconnect")?.textContent,
						selected: document.querySelector('[role="tab"][aria-selected="true"]')?.dataset.tab,
						errors: window.__relayErrors
					})`, &diagnostic))
					t.Fatalf("%s deferred assets for %s deep link: %v: %s", mode, tab.name, err, diagnostic)
				}
			})
		}
	}
}

func TestBrowserDeepLinkedDeferredTabsWaitForFullSnapshot(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	goal := "# Authoritative goal\n"
	tabs := []struct {
		name       string
		falseEmpty string
		rendered   string
	}{
		{
			name:       "kanban",
			falseEmpty: `document.querySelectorAll("#kanban-board .kanban__lane").length === 6`,
			rendered:   `document.querySelectorAll("#kanban-board .kanban__lane").length === 6`,
		},
		{
			name:       "tasks",
			falseEmpty: `document.querySelector("#ledger-count").textContent === "No tasks"`,
			rendered:   `document.querySelector("#ledger-count").textContent === "2 of 2 tasks"`,
		},
		{
			name:       "decisions",
			falseEmpty: `document.querySelector("#decisions-note").textContent === "Nothing is waiting on you."`,
			rendered:   `document.querySelector("#decisions-note").textContent === "1 decision waiting on you."`,
		},
		{
			name:       "goal",
			falseEmpty: `document.querySelector("#goal-body").textContent.includes("No program files were found on disk.")`,
			rendered:   `document.querySelector("#goal-body").textContent.includes("Authoritative goal")`,
		},
	}
	for _, tab := range tabs {
		t.Run(tab.name, func(t *testing.T) {
			snapshot := browserTestSnapshot()
			snapshot.OpenDecisions = []programview.DecisionDTO{{
				ID: "d1", Question: "Choose the authoritative path.",
			}}
			snapshot.ProgramArtifacts = []programview.ArtifactDTO{{
				Name: "goal.md", Path: "goal.md", Present: true,
				Size: int64(len(goal)), Text: &goal,
			}}
			assetsRelease := make(chan struct{})
			snapshotRelease := make(chan struct{})
			var assetsOnce sync.Once
			var snapshotOnce sync.Once
			t.Cleanup(func() {
				assetsOnce.Do(func() { close(assetsRelease) })
				snapshotOnce.Do(func() { close(snapshotRelease) })
			})
			feed := newSnapshotFeed(
				snapshot,
				time.Minute,
				time.Now,
				func() (programview.Snapshot, error) { return snapshot, nil },
			)
			url := startBrowserHTTPHandler(t, func(port int) http.Handler {
				base := newHandler(
					snapshot.Program.Slug,
					strconv.Itoa(port),
					newSnapshotCache(time.Minute, time.Now, nil),
					feed,
					nil,
				)
				return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
					switch {
					case request.URL.Path == "/app-deferred.css" ||
						request.URL.Path == "/app-deferred.js":
						select {
						case <-assetsRelease:
						case <-request.Context().Done():
							return
						}
					case request.URL.Path == "/api/program" &&
						request.URL.Query().Get("view") == "":
						select {
						case <-snapshotRelease:
						case <-request.Context().Done():
							return
						}
					}
					base.ServeHTTP(response, request)
				})
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
			if err := chromedp.Run(browser,
				chromedp.ActionFunc(func(ctx context.Context) error {
					_, err := page.AddScriptToEvaluateOnNewDocument(`
						window.__relayErrors = [];
						window.addEventListener("error", (event) => window.__relayErrors.push(event.message));
						window.addEventListener("unhandledrejection", (event) =>
							window.__relayErrors.push(String(event.reason)));
					`).Do(ctx)
					return err
				}),
				chromedp.ActionFunc(func(ctx context.Context) error {
					_, _, _, _, err := page.Navigate(url + "/#tab=" + tab.name).Do(ctx)
					return err
				}),
				chromedp.Poll(`typeof deferredScriptPromise !== "undefined" &&
					deferredScriptPromise !== null`, nil),
				chromedp.ActionFunc(func(context.Context) error {
					assetsOnce.Do(func() { close(assetsRelease) })
					return nil
				}),
				chromedp.Poll(`deferredUIReady === true`, nil),
			); err != nil {
				t.Fatalf("load deferred assets before full snapshot: %v", err)
			}
			var renderedFalseEmpty bool
			if err := chromedp.Run(browser, chromedp.Evaluate(tab.falseEmpty, &renderedFalseEmpty)); err != nil {
				t.Fatal(err)
			}
			if renderedFalseEmpty {
				t.Fatalf("%s rendered an empty-program state before the full snapshot", tab.name)
			}
			if err := chromedp.Run(browser,
				chromedp.Evaluate(`window.__relayBeforeFull = {
					schema: state.snapshot?.schema,
					tab: state.tab,
					pending: state.pendingTab?.name,
					panelHidden: document.querySelector("#panel-`+tab.name+`").hidden,
					errors: window.__relayErrors.slice()
				}`, nil),
				chromedp.Poll(`window.__relayBeforeFull.schema === "relay.program.roadmap.bootstrap.v1" &&
					window.__relayBeforeFull.tab === "roadmap" &&
					window.__relayBeforeFull.pending === "`+tab.name+`" &&
					window.__relayBeforeFull.panelHidden === true &&
					window.__relayBeforeFull.errors.length === 0`, nil),
				chromedp.ActionFunc(func(context.Context) error {
					snapshotOnce.Do(func() { close(snapshotRelease) })
					return nil
				}),
				chromedp.Poll(`state.snapshot?.schema === "relay.program.v1" &&
					state.tab === "`+tab.name+`" &&
					`+tab.rendered+` &&
					window.__relayErrors.length === 0`, nil, chromedp.WithPollingTimeout(8*time.Second)),
			); err != nil {
				t.Fatalf("render authoritative %s tab: %v", tab.name, err)
			}
		})
	}
}

func TestBrowserKanbanBoard(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	item := func(id, title, status, priority string) programview.ItemDTO {
		return programview.ItemDTO{
			ID: id, Title: title, Status: status, Lane: status, Priority: priority,
			Dependencies: []string{}, Dependents: []string{}, Contracts: []string{},
			Notes: []string{}, Decisions: []programview.DecisionDTO{},
			Artifacts: []programview.ArtifactDTO{}, Warnings: []string{},
			Mailbox: programview.MailboxDTO{InboxIDs: []string{}, OutboxIDs: []string{}},
		}
	}

	tests := []struct {
		name     string
		items    []programview.ItemDTO
		expected string
	}{
		{
			name: "populated",
			items: []programview.ItemDTO{
				item("w1", "First pending", "pending", "P2"),
				item("w2", "Dispatched task", "dispatched", "P1"),
				item("w3", "Second pending", "pending", "P3"),
				item("w4", "Blocked task", "blocked", "P0"),
				item("w5", "Merged task", "merged", "P1"),
			},
			expected: `[{"lane":"pending","heading":"Pending","count":"2","ids":["w1","w3"]},` +
				`{"lane":"dispatched","heading":"Dispatched","count":"1","ids":["w2"]},` +
				`{"lane":"in-review","heading":"In review","count":"0","ids":[]},` +
				`{"lane":"blocked","heading":"Blocked","count":"1","ids":["w4"]},` +
				`{"lane":"merged","heading":"Merged","count":"1","ids":["w5"]},` +
				`{"lane":"cancelled","heading":"Cancelled","count":"0","ids":[]}]`,
		},
		{
			name:     "empty",
			items:    []programview.ItemDTO{},
			expected: `[{"lane":"pending","heading":"Pending","count":"0","ids":[]},` +
				`{"lane":"dispatched","heading":"Dispatched","count":"0","ids":[]},` +
				`{"lane":"in-review","heading":"In review","count":"0","ids":[]},` +
				`{"lane":"blocked","heading":"Blocked","count":"0","ids":[]},` +
				`{"lane":"merged","heading":"Merged","count":"0","ids":[]},` +
				`{"lane":"cancelled","heading":"Cancelled","count":"0","ids":[]}]`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := browserTestSnapshot()
			snapshot.Items = test.items
			snapshot.Progress.Total = len(test.items)
			url := startBrowserTestFeedHandler(t, snapshot)

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
				chromedp.Navigate(url+"/#tab=kanban"),
				chromedp.Poll(`typeof renderKanban === "function" &&
					state.tab === "kanban" &&
					document.querySelectorAll("#kanban-board .kanban__lane").length === 6`, nil),
			); err != nil {
				t.Fatalf("render Kanban board: %v", err)
			}

			var actual string
			if err := chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify(
				Array.from(document.querySelectorAll("#kanban-board .kanban__lane"), (lane) => ({
					lane: lane.dataset.lane,
					heading: lane.querySelector(".kanban__lane-title").textContent,
					count: lane.querySelector(".kanban__lane-count").textContent,
					ids: Array.from(lane.querySelectorAll(".kanban__card"), (card) => card.dataset.taskId)
				}))
			)`, &actual)); err != nil {
				t.Fatal(err)
			}
			if actual != test.expected {
				t.Fatalf("Kanban lanes = %s, want %s", actual, test.expected)
			}

			if test.name == "empty" {
				return
			}
			var cardContract string
			if err := chromedp.Run(browser,
				chromedp.Evaluate(`JSON.stringify({
					count: document.querySelectorAll("#kanban-board .kanban__card").length,
					tag: document.querySelector('.kanban__card[data-task-id="w1"]').tagName,
					type: document.querySelector('.kanban__card[data-task-id="w1"]').type,
					text: document.querySelector('.kanban__card[data-task-id="w1"]').textContent,
					label: document.querySelector('.kanban__card[data-task-id="w1"]').getAttribute("aria-label")
				})`, &cardContract),
			); err != nil {
				t.Fatal(err)
			}
			wantCard := `{"count":5,"tag":"BUTTON","type":"button","text":"w1○PendingFirst pendingP2",` +
				`"label":"Task w1: First pending. Status Pending. Priority P2. No dependencies."}`
			if cardContract != wantCard {
				t.Fatalf("Kanban card contract = %s, want %s", cardContract, wantCard)
			}

			var afterFilters string
			if err := chromedp.Run(browser,
				chromedp.Evaluate(`(() => {
					selectTab("tasks");
					state.filter = "no matching task";
					state.statuses = new Set(["cancelled"]);
					renderLedger();
					selectTab("kanban");
					return JSON.stringify(Array.from(
						document.querySelectorAll("#kanban-board .kanban__card"),
						(card) => card.dataset.taskId
					));
				})()`, &afterFilters),
			); err != nil {
				t.Fatal(err)
			}
			if afterFilters != `["w1","w3","w2","w4","w5"]` {
				t.Fatalf("Kanban tasks after Tasks filters = %s", afterFilters)
			}
		})
	}
}

func TestBrowserKanbanNavigation(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	snapshot := browserTestSnapshot()
	snapshot.Items[0].Status = "pending"
	snapshot.Items[0].Lane = "pending"
	snapshot.Items[1].Status = "in-review"
	snapshot.Items[1].Lane = "in-review"

	var mutation atomic.Bool
	feed := newSnapshotFeed(snapshot, time.Minute, time.Now, func() (programview.Snapshot, error) {
		return snapshot, nil
	})
	url := startBrowserHTTPHandler(t, func(port int) http.Handler {
		base := newHandler(
			snapshot.Program.Slug,
			strconv.Itoa(port),
			newSnapshotCache(time.Minute, time.Now, nil),
			feed,
			nil,
		)
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet && request.Method != http.MethodHead {
				mutation.Store(true)
			}
			base.ServeHTTP(response, request)
		})
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

	if err := chromedp.Run(browser,
		chromedp.Navigate(url),
		chromedp.Poll(`typeof renderKanban === "function" &&
			state.snapshot?.schema === "relay.program.v1"`, nil),
		chromedp.Focus("#tab-roadmap", chromedp.ByQuery),
		chromedp.Evaluate(`document.activeElement.dispatchEvent(new KeyboardEvent(
			"keydown", {key: "ArrowRight", bubbles: true, cancelable: true}))`, nil),
		chromedp.Poll(`state.tab === "kanban" &&
			location.hash === "#tab=kanban" &&
			document.activeElement?.id === "tab-kanban"`, nil),
		chromedp.Focus(`.kanban__card[data-task-id="w1"]`, chromedp.ByQuery),
		chromedp.KeyEvent("\r"),
		chromedp.Poll(`state.selected === "w1" &&
			document.querySelector("#drawer").dataset.state === "open" &&
			document.querySelector("#drawer-title").textContent === "First task" &&
			location.hash === "#tab=kanban&task=w1"`, nil),
		chromedp.Click("#drawer-close", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector("#drawer").hidden === true &&
			document.activeElement?.dataset.taskId === "w1"`, nil),
		chromedp.Focus(`.kanban__card[data-task-id="w2"]`, chromedp.ByQuery),
		chromedp.KeyEvent(" "),
		chromedp.Poll(`state.selected === "w2" &&
			document.querySelector("#drawer-title").textContent === "Second task" &&
			location.hash === "#tab=kanban&task=w2"`, nil),
	); err != nil {
		t.Fatalf("Kanban keyboard navigation: %v", err)
	}
	if mutation.Load() {
		t.Fatal("Kanban navigation issued a mutation request")
	}

	if err := chromedp.Run(browser,
		chromedp.Navigate(url+"/#tab=kanban&task=w1"),
		chromedp.Poll(`state.tab === "kanban" &&
			state.selected === "w1" &&
			document.querySelector("#drawer").dataset.state === "open" &&
			document.querySelector("#drawer-title").textContent === "First task"`, nil),
		chromedp.Reload(),
		chromedp.Poll(`state.tab === "kanban" &&
			state.selected === "w1" &&
			document.querySelector("#drawer").dataset.state === "open" &&
			document.querySelector("#drawer-title").textContent === "First task"`, nil),
	); err != nil {
		t.Fatalf("Kanban deep-link restoration: %v", err)
	}
	if mutation.Load() {
		t.Fatal("Kanban deep-link restoration issued a mutation request")
	}
}

func TestBrowserKanbanRefresh(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	initial := browserTestSnapshot()
	initial.Items[0].Status = "pending"
	initial.Items[0].Lane = "pending"
	initial.Items[1].Status = "dispatched"
	initial.Items[1].Lane = "dispatched"

	updated := initial
	updated.Items = []programview.ItemDTO{
		{
			ID: "w3", Title: "New pending task", Status: "pending", Lane: "pending", Priority: "P3",
			Dependencies: []string{}, Dependents: []string{}, Contracts: []string{}, Notes: []string{},
			Decisions: []programview.DecisionDTO{}, Artifacts: []programview.ArtifactDTO{},
			Warnings: []string{}, Mailbox: programview.MailboxDTO{InboxIDs: []string{}, OutboxIDs: []string{}},
		},
		initial.Items[1],
	}
	updated.Items[1].Status = "in-review"
	updated.Items[1].Lane = "in-review"
	updated.Items[1].Title = "Updated review task"
	updated.Items[1].Priority = "P0"
	updated.Progress.Total = 2

	var refreshes atomic.Int32
	feed := newSnapshotFeed(initial, time.Minute, time.Now, func() (programview.Snapshot, error) {
		if refreshes.Add(1) == 1 {
			return updated, nil
		}
		return programview.Snapshot{}, errors.New("test refresh failed")
	})
	url := startBrowserHTTPHandler(t, func(port int) http.Handler {
		return newHandler(
			initial.Program.Slug,
			strconv.Itoa(port),
			newSnapshotCache(time.Minute, time.Now, nil),
			feed,
			nil,
		)
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

	if err := chromedp.Run(browser,
		chromedp.Navigate(url+"/#tab=kanban"),
		chromedp.Poll(`state.tab === "kanban" &&
			document.querySelectorAll("#kanban-board .kanban__card").length === 2`, nil),
		chromedp.Evaluate(`(() => {
			dom.kanbanScroll.scrollLeft = 180;
			document.querySelector('.kanban__card[data-task-id="w2"]').focus();
		})()`, nil),
	); err != nil {
		t.Fatalf("prepare Kanban refresh: %v", err)
	}

	feed.Refresh()
	eventually(t, time.Second, func() bool {
		return !feed.Get().Refresh.Refreshing && feed.Get().Items[0].ID == "w3"
	})
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`JSON.stringify(
			Array.from(document.querySelectorAll("#kanban-board .kanban__lane"), (lane) => ({
				lane: lane.dataset.lane,
				count: lane.querySelector(".kanban__lane-count").textContent,
				ids: Array.from(lane.querySelectorAll(".kanban__card"), (card) => card.dataset.taskId)
			}))
		) === JSON.stringify([
			{lane: "pending", count: "1", ids: ["w3"]},
			{lane: "dispatched", count: "0", ids: []},
			{lane: "in-review", count: "1", ids: ["w2"]},
			{lane: "blocked", count: "0", ids: []},
			{lane: "merged", count: "0", ids: []},
			{lane: "cancelled", count: "0", ids: []}
		]) &&
			document.querySelector('.kanban__card[data-task-id="w1"]') === null &&
			document.querySelector('.kanban__card[data-task-id="w2"] .card__title').textContent ===
				"Updated review task" &&
			document.querySelector('.kanban__card[data-task-id="w2"] .card__foot').textContent === "P0" &&
			document.activeElement?.dataset.taskId === "w2" &&
			dom.kanbanScroll.scrollLeft >= 150`, nil),
	); err != nil {
		t.Fatalf("apply successful Kanban refresh: %v", err)
	}

	feed.Refresh()
	eventually(t, time.Second, func() bool {
		return !feed.Get().Refresh.Refreshing && feed.Get().Refresh.Status == "failed"
	})
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`document.querySelectorAll("#kanban-board .kanban__card").length === 2 &&
			document.querySelector('.kanban__card[data-task-id="w3"]') !== null &&
			document.querySelector('.kanban__card[data-task-id="w2"] .card__title').textContent ===
				"Updated review task" &&
			document.querySelector("#feed-state").textContent.includes("Stale") &&
			document.querySelector("#feed-state").textContent.includes("test refresh failed")`, nil),
	); err != nil {
		t.Fatalf("retain Kanban board after failed refresh: %v", err)
	}
}

func TestBrowserHeaderRefreshWaitsForFullSnapshot(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	snapshot := browserTestSnapshot()
	snapshot.Program.Summary = "Authoritative program summary"
	snapshot.Program.State = "active"
	snapshot.Progress.Merged = 1
	snapshot.Progress.Percent = 50
	snapshot.Plan.NextAction = "Continue with the authoritative plan."
	snapshotRelease := make(chan struct{})
	var snapshotOnce sync.Once
	t.Cleanup(func() {
		snapshotOnce.Do(func() { close(snapshotRelease) })
	})
	feed := newSnapshotFeed(
		snapshot,
		time.Minute,
		time.Now,
		func() (programview.Snapshot, error) { return snapshot, nil },
	)
	url := startBrowserHTTPHandler(t, func(port int) http.Handler {
		base := newHandler(
			snapshot.Program.Slug,
			strconv.Itoa(port),
			newSnapshotCache(time.Minute, time.Now, nil),
			feed,
			nil,
		)
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/api/program" &&
				request.URL.Query().Get("view") == "" {
				select {
				case <-snapshotRelease:
				case <-request.Context().Done():
					return
				}
			}
			base.ServeHTTP(response, request)
		})
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
	browser, cancelTimeout := context.WithTimeout(browser, 30*time.Second)
	defer cancelTimeout()

	const headerSnapshot = `JSON.stringify({
		title: document.title,
		programTitle: document.querySelector("#program-title").textContent,
		summary: document.querySelector("#program-summary").textContent,
		slug: document.querySelector("#program-slug").textContent,
		state: document.querySelector("#program-state").textContent,
		updated: document.querySelector("#program-updated").textContent,
		repo: document.querySelector("#program-repo").textContent,
		agent: document.querySelector("#program-agent").textContent,
		percent: document.querySelector("#progress-percent").textContent,
		progress: document.querySelector("#progress-counts").textContent,
		tasks: document.querySelector("#task-total").textContent,
		breakdown: document.querySelector("#task-breakdown").textContent,
		capacity: document.querySelector("#capacity-readout").textContent,
		capacityNote: document.querySelector("#capacity-note").textContent,
		decisions: document.querySelector("#decision-count").textContent,
		decisionNote: document.querySelector("#decision-note").textContent,
		workers: document.querySelector("#worker-count").textContent,
		workerNote: document.querySelector("#worker-note").textContent,
		patrol: document.querySelector("#patrol-status").textContent,
		patrolNote: document.querySelector("#patrol-note").textContent,
		patrolTurn: document.querySelector("#patrol-turn").textContent,
		nextAction: document.querySelector("#next-action").textContent,
		nextCommand: document.querySelector("#next-command").textContent,
		warningHidden: document.querySelector("#warning-count").hidden,
		warningText: document.querySelector("#warning-count").textContent,
		schema: document.querySelector("#schema-version").textContent
	})`
	if err := chromedp.Run(browser,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(`
				window.__relayErrors = [];
				window.__relayHeaderIntervalTicks = 0;
				window.addEventListener("error", (event) => window.__relayErrors.push(event.message));
				window.addEventListener("unhandledrejection", (event) =>
					window.__relayErrors.push(String(event.reason)));
				const relaySetInterval = window.setInterval.bind(window);
				window.setInterval = (callback, delay, ...args) => relaySetInterval(() => {
					if (delay === 15000) {
						window.__relayHeaderIntervalTicks += 1;
					}
					callback(...args);
				}, delay);
			`).Do(ctx)
			return err
		}),
		chromedp.Navigate(url),
		chromedp.Poll(`typeof state !== "undefined" &&
			state.snapshot?.schema === "relay.program.roadmap.bootstrap.v1" &&
			window.__relayErrors.length === 0`, nil),
		chromedp.Evaluate(`window.__relayServerHeader = `+headerSnapshot, nil),
		chromedp.Poll(`window.__relayHeaderIntervalTicks >= 1`, nil,
			chromedp.WithPollingTimeout(18*time.Second)),
	); err != nil {
		t.Fatalf("wait for header refresh interval: %v", err)
	}

	var headerPreserved bool
	if err := chromedp.Run(browser, chromedp.Evaluate(
		`window.__relayServerHeader === `+headerSnapshot, &headerPreserved,
	)); err != nil {
		t.Fatal(err)
	}
	if !headerPreserved {
		var diagnostic string
		_ = chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify({
			before: JSON.parse(window.__relayServerHeader),
			after: JSON.parse(`+headerSnapshot+`),
			schema: state.snapshot?.schema,
			ticks: window.__relayHeaderIntervalTicks,
			errors: window.__relayErrors
		})`, &diagnostic))
		t.Fatalf("bootstrap header changed after periodic refresh: %s", diagnostic)
	}

	if err := chromedp.Run(browser,
		chromedp.ActionFunc(func(context.Context) error {
			snapshotOnce.Do(func() { close(snapshotRelease) })
			return nil
		}),
		chromedp.Poll(`state.snapshot?.schema === "relay.program.v1" &&
			document.querySelector("#program-title").textContent === "Browser Test" &&
			document.querySelector("#program-summary").textContent === "Authoritative program summary" &&
			document.querySelector("#program-state").textContent.includes("Active") &&
			document.querySelector("#progress-percent").textContent === "50" &&
			document.querySelector("#progress-counts").textContent === "1 of 2 merged" &&
			document.querySelector("#task-total").textContent === "2" &&
			document.querySelector("#next-action").textContent ===
				"Continue with the authoritative plan." &&
			document.querySelector("#schema-version").textContent === "relay.program.v1" &&
			window.__relayErrors.length === 0`, nil, chromedp.WithPollingTimeout(8*time.Second)),
	); err != nil {
		t.Fatalf("recover authoritative header after full snapshot: %v", err)
	}
}

func TestBrowserQueuesHashChangesBeforeDeferredBundle(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	snapshot := browserTestSnapshot()
	snapshot.OpenDecisions = []programview.DecisionDTO{{
		ID: "d1", Question: "Choose the requested tab.",
	}}
	snapshotRelease := make(chan struct{})
	var snapshotOnce sync.Once
	t.Cleanup(func() {
		snapshotOnce.Do(func() { close(snapshotRelease) })
	})
	feed := newSnapshotFeed(
		snapshot,
		time.Minute,
		time.Now,
		func() (programview.Snapshot, error) { return snapshot, nil },
	)
	url := startBrowserHTTPHandler(t, func(port int) http.Handler {
		base := newHandler(
			snapshot.Program.Slug,
			strconv.Itoa(port),
			newSnapshotCache(time.Minute, time.Now, nil),
			feed,
			nil,
		)
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/api/program" &&
				request.URL.Query().Get("view") == "" {
				select {
				case <-snapshotRelease:
				case <-request.Context().Done():
					return
				}
			}
			base.ServeHTTP(response, request)
		})
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
	if err := chromedp.Run(browser,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(`
				window.__relayErrors = [];
				window.addEventListener("error", (event) => window.__relayErrors.push(event.message));
				window.addEventListener("unhandledrejection", (event) =>
					window.__relayErrors.push(String(event.reason)));
			`).Do(ctx)
			return err
		}),
		network.Enable(),
		network.SetBlockedURLs([]string{"*app-deferred.css", "*app-deferred.js"}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, _, _, _, err := page.Navigate(url).Do(ctx)
			return err
		}),
		chromedp.Poll(`document.querySelector("#reconnect").textContent.includes(
			"failed to load. Click Refresh to retry.")`, nil),
		chromedp.Evaluate(`
			window.__relayTabSelections = {tasks: 0, decisions: 0};
			const tabs = document.querySelectorAll('[role="tab"]');
			const observer = new MutationObserver((records) => {
				records.forEach((record) => {
					const tab = record.target.dataset.tab;
					if (record.attributeName === "aria-selected" &&
						record.target.getAttribute("aria-selected") === "true" &&
						Object.prototype.hasOwnProperty.call(window.__relayTabSelections, tab)) {
						window.__relayTabSelections[tab] += 1;
					}
				});
			});
			tabs.forEach((tab) => observer.observe(tab, {attributes: true}));
			window.location.hash = "tab=tasks";
			window.location.hash = "tab=decisions";
		`, nil),
		chromedp.Poll(`window.location.hash === "#tab=decisions" &&
			window.__relayErrors.length === 0`, nil),
		network.SetBlockedURLs([]string{}),
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
	); err != nil {
		t.Fatalf("queue pre-bundle hash changes: %v", err)
	}
	if err := chromedp.Run(browser,
		chromedp.Poll(`deferredUIReady === true`, nil),
		chromedp.Poll(`state.snapshot?.schema === "relay.program.roadmap.bootstrap.v1" &&
			state.tab === "roadmap" &&
			window.__relayErrors.length === 0`, nil),
		chromedp.ActionFunc(func(context.Context) error {
			snapshotOnce.Do(func() { close(snapshotRelease) })
			return nil
		}),
		chromedp.Poll(`state.snapshot?.schema === "relay.program.v1" &&
			state.tab === "decisions" &&
			document.querySelector("#decisions-note").textContent === "1 decision waiting on you." &&
			window.__relayTabSelections.tasks === 0 &&
			window.__relayTabSelections.decisions === 1 &&
			window.__relayErrors.length === 0`, nil, chromedp.WithPollingTimeout(8*time.Second)),
	); err != nil {
		var diagnostic string
		_ = chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify({
			hash: window.location.hash,
			tab: state.tab,
			schema: state.snapshot?.schema,
			pending: state.pendingTab?.name,
			selections: window.__relayTabSelections,
			errors: window.__relayErrors
		})`, &diagnostic))
		t.Fatalf("apply queued hash once after dependencies: %v: %s", err, diagnostic)
	}
}

func TestBrowserKeepsPendingExternalDrawerStateNonAuthoritative(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	snapshot := browserTestSnapshot()
	snapshot.Items[0].RecordedPR = &programview.PullRequestDTO{
		Number: 42, Ref: "#42", State: "open", Title: "Recorded PR",
	}
	url := startBrowserTestHandler(t, HandlerOptions{
		Slug: "browser-test",
		Builder: func(_ string, _ string) (programview.Snapshot, error) {
			return snapshot, nil
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
		chromedp.Poll(`typeof deferredUIReady !== "undefined" && deferredUIReady`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		chromedp.Poll(`document.querySelector("#detail-body").textContent.includes(
			"GitHub refresh pending") &&
			document.querySelector("#detail-body").textContent.includes(
				"Herdr refresh pending")`, nil),
	); err != nil {
		t.Fatalf("pending external drawer state: %v", err)
	}
	var detail string
	if err := chromedp.Run(browser, chromedp.Text("#detail-body", &detail)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(detail, "GitHub did not answer") ||
		strings.Contains(detail, "worker may have exited") {
		t.Fatalf("pending drawer made an authoritative negative claim: %s", detail)
	}
}

func TestBrowserPreservesPreHydrationRoadmapSelection(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	snapshot := browserTestSnapshot()
	snapshot.Graph.Layers = [][]string{{"w1"}, {"w2"}}
	url := startBrowserTestFeedHandler(t, snapshot)

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
		network.Enable(),
		network.SetBlockedURLs([]string{"*/app.js"}),
		chromedp.Navigate(url),
		chromedp.Poll(`window.__relayCoreReady === true &&
			document.querySelectorAll(".card").length === 2 &&
			Array.from(document.querySelectorAll(".card")).every((card) =>
				card.children.length === 3 &&
				card.children[0].classList.contains("card__top") &&
				card.children[1].classList.contains("card__title") &&
				card.children[2].classList.contains("card__foot")) &&
			document.querySelector('.card[data-item="w1"]').getBoundingClientRect().width <
				document.querySelector("#graph-nodes").getBoundingClientRect().width * 0.75`, nil),
		chromedp.Evaluate(`
			document.querySelector('.card[data-item="w1"]').dispatchEvent(
				new KeyboardEvent("keydown", {key: "ArrowRight", bubbles: true, cancelable: true}))
		`, nil),
		chromedp.Poll(`document.querySelector('.card[data-item="w2"]').dataset.selected === "true"`, nil),
		network.SetBlockedURLs([]string{}),
		chromedp.Evaluate(`
			(() => {
				const script = document.createElement("script");
				script.src = "/app.js";
				document.body.append(script);
			})()
		`, nil),
		chromedp.Poll(`typeof state !== "undefined" &&
			state.snapshot?.schema === "relay.program.v1" &&
			Array.from(document.querySelectorAll(".card")).every((card) =>
				card.children.length === 3 &&
				card.children[0].classList.contains("card__top") &&
				card.children[1].classList.contains("card__title") &&
				card.children[2].classList.contains("card__foot")) &&
			document.querySelector('.card[data-item="w1"]').getBoundingClientRect().width <
				document.querySelector("#graph-nodes").getBoundingClientRect().width * 0.75`, nil),
	); err != nil {
		t.Fatalf("pre-hydration keyboard selection: %v", err)
	}
	var selection struct {
		State    string   `json:"state"`
		Selected []string `json:"selected"`
	}
	if err := chromedp.Run(browser, chromedp.Evaluate(`({
		state: state.selected,
		selected: Array.from(document.querySelectorAll('.card[data-selected="true"]'),
			(card) => card.dataset.item)
	})`, &selection)); err != nil {
		t.Fatal(err)
	}
	if selection.State != "w2" || !slices.Equal(selection.Selected, []string{"w2"}) {
		t.Fatalf("hydrated selection = %+v, want state and DOM on w2", selection)
	}
}

func TestBrowserRoadmapRebuildsWhenDependenciesChange(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	initial := browserTestSnapshot()
	initial.Progress.Total = 3
	initial.Graph.Nodes = append(initial.Graph.Nodes,
		programview.GraphNodeDTO{ID: "w3", Title: "Third task", Lane: "dispatched", Layer: 1},
	)
	initial.Graph.Edges = []programview.GraphEdgeDTO{{From: "w1", To: "w3"}}
	initial.Graph.Layers = [][]string{{"w1", "w2"}, {"w3"}}
	initial.Items = append(initial.Items, programview.ItemDTO{
		ID: "w3", Title: "Third task", Status: "dispatched", Lane: "dispatched", Priority: "P1",
		Dependencies: []string{"w1"}, Dependents: []string{}, Contracts: []string{},
		Notes: []string{}, Decisions: []programview.DecisionDTO{}, Warnings: []string{},
		Mailbox: programview.MailboxDTO{InboxIDs: []string{}, OutboxIDs: []string{}},
	})
	initial.Items[0].Dependents = []string{"w3"}

	updated := initial
	updated.Graph.Nodes = append([]programview.GraphNodeDTO(nil), initial.Graph.Nodes...)
	updated.Graph.Nodes[1].Layer = 1
	updated.Graph.Edges = []programview.GraphEdgeDTO{{From: "w1", To: "w2"}}
	updated.Graph.Layers = [][]string{{"w1"}, {"w2", "w3"}}
	updated.Items = append([]programview.ItemDTO(nil), initial.Items...)
	updated.Items[0].Dependents = []string{"w2"}
	updated.Items[1].Dependencies = []string{"w1"}
	updated.Items[2].Dependencies = []string{}

	feed := newSnapshotFeed(initial, time.Minute, time.Now, func() (programview.Snapshot, error) {
		return updated, nil
	})
	url := startBrowserHTTPHandler(t, func(port int) http.Handler {
		return newHandler(
			initial.Program.Slug,
			strconv.Itoa(port),
			newSnapshotCache(time.Minute, time.Now, nil),
			feed,
			nil,
		)
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
		chromedp.Poll(`state.snapshot?.schema === "relay.program.v1" &&
			document.querySelectorAll(".card").length === 3`, nil),
		chromedp.Evaluate(`
			(() => {
				const first = document.querySelector('.card[data-item="w1"]');
				first.focus();
				first.dispatchEvent(new KeyboardEvent(
					"keydown", {key: "ArrowRight", bubbles: true, cancelable: true}));
			})()
		`, nil),
		chromedp.Poll(`state.selected === "w2" &&
			document.activeElement?.dataset.item === "w2"`, nil),
	); err != nil {
		t.Fatalf("initial roadmap selection: %v", err)
	}

	feed.Refresh()
	eventually(t, time.Second, func() bool {
		return slices.Equal(feed.Get().Graph.Layers[0], []string{"w1"})
	})
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`JSON.stringify(
			Array.from(document.querySelectorAll("#graph-nodes > .stage"),
				(stage) => Array.from(stage.querySelectorAll(":scope > .card"),
					(card) => card.dataset.item))) === JSON.stringify([["w1"], ["w2", "w3"]]) &&
			JSON.stringify(Array.from(document.querySelectorAll("#graph-nodes > .stage"),
				(stage) => stage.dataset.label)) ===
				JSON.stringify(["Stage 1 · 1 task", "Stage 2 · 2 tasks"]) &&
			document.querySelector('.card[data-item="w2"]').dataset.stage === "1" &&
			state.selected === "w2" &&
			document.activeElement?.dataset.item === "w2" &&
			JSON.stringify(state.connectorPaths.map(({from, to, downward}) =>
				({from, to, downward}))) ===
				JSON.stringify([{from: "w1", to: "w2", downward: true}])`, nil),
		chromedp.Evaluate(`
			document.activeElement.dispatchEvent(new KeyboardEvent(
				"keydown", {key: "ArrowUp", bubbles: true, cancelable: true}))
		`, nil),
		chromedp.Poll(`state.selected === "w1" &&
			document.activeElement?.dataset.item === "w1"`, nil),
		chromedp.Evaluate(`
			window.__relayStableCards = Array.from(document.querySelectorAll(".card"));
			window.__relayNextGeneration = state.programGeneration + 1;
		`, nil),
	); err != nil {
		t.Fatalf("dependency-only roadmap refresh: %v", err)
	}

	feed.Refresh()
	eventually(t, time.Second, func() bool { return !feed.Get().Refresh.Refreshing })
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`state.programGeneration >= window.__relayNextGeneration &&
			programController === null`, nil),
		chromedp.Poll(`window.__relayStableCards.every(
			(card, index) => card === document.querySelectorAll(".card")[index]) &&
			state.selected === "w1" &&
			document.activeElement?.dataset.item === "w1"`, nil),
	); err != nil {
		t.Fatalf("unchanged roadmap refresh: %v", err)
	}
}

func TestBrowserRoadmapRedrawsConnectorsWhenCardMetadataChanges(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	initial := browserTestSnapshot()
	initial.Graph.Edges = []programview.GraphEdgeDTO{{From: "w1", To: "w2"}}
	initial.Graph.Layers = [][]string{{"w1"}, {"w2"}}
	initial.Graph.Nodes[0].Layer = 0
	initial.Graph.Nodes[1].Layer = 1
	initial.Items[0].Dependents = []string{"w2"}
	initial.Items[1].Dependencies = []string{"w1"}

	updated := initial
	updated.Graph.Nodes = append([]programview.GraphNodeDTO(nil), initial.Graph.Nodes...)
	updated.Items = append([]programview.ItemDTO(nil), initial.Items...)
	updated.Graph.Nodes[0].Title = strings.Repeat("Expanded metadata changes card geometry ", 8)
	updated.Items[0].Title = updated.Graph.Nodes[0].Title

	feed := newSnapshotFeed(initial, time.Minute, time.Now, func() (programview.Snapshot, error) {
		return updated, nil
	})
	url := startBrowserHTTPHandler(t, func(port int) http.Handler {
		return newHandler(
			initial.Program.Slug,
			strconv.Itoa(port),
			newSnapshotCache(time.Minute, time.Now, nil),
			feed,
			nil,
		)
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
		chromedp.EmulateViewport(700, 900),
		chromedp.Navigate(url),
		chromedp.Poll(`state.snapshot?.schema === "relay.program.v1" &&
			document.querySelectorAll(".card").length === 2 &&
			document.querySelector("#graph-edges .edge")?.getAttribute("d")`, nil),
		chromedp.Evaluate(`
			window.__relayMetadataCard = document.querySelector('.card[data-item="w1"]');
			window.__relayMetadataHeight = window.__relayMetadataCard.getBoundingClientRect().height;
			window.__relayMetadataPath =
				document.querySelector("#graph-edges .edge").getAttribute("d");
		`, nil),
	); err != nil {
		t.Fatalf("capture initial connector geometry: %v", err)
	}

	feed.Refresh()
	eventually(t, time.Second, func() bool {
		return feed.Get().Graph.Nodes[0].Title == updated.Graph.Nodes[0].Title
	})
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`
			(() => {
				const card = document.querySelector('.card[data-item="w1"]');
				const path = document.querySelector("#graph-edges .edge")?.getAttribute("d");
				return card === window.__relayMetadataCard &&
					card.getBoundingClientRect().height > window.__relayMetadataHeight &&
					path && path !== window.__relayMetadataPath;
			})()
		`, nil),
	); err != nil {
		var diagnostic string
		_ = chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify({
			sameCard: document.querySelector('.card[data-item="w1"]') === window.__relayMetadataCard,
			beforeHeight: window.__relayMetadataHeight,
			afterHeight: document.querySelector('.card[data-item="w1"]')?.getBoundingClientRect().height,
			beforePath: window.__relayMetadataPath,
			afterPath: document.querySelector("#graph-edges .edge")?.getAttribute("d")
		})`, &diagnostic))
		t.Fatalf("metadata-only connector redraw: %v: %s", err, diagnostic)
	}
}

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

func TestBrowserRoadmapResumesAfterTabSwitch(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	fixture := newReferenceProgramFixture(t)
	output := newLineWriter()
	serverContext, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- Serve(serverContext, Options{
			Slug: fixture.program.Slug, Port: 0, Open: false, Out: output,
			GitHub: &controlledFetcher{},
			Agents: &controlledAgentLister{},
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

	if err := chromedp.Run(browser,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(`
				const relayAnimationFrame = window.requestAnimationFrame.bind(window);
				window.requestAnimationFrame = (callback) =>
					relayAnimationFrame((timestamp) => setTimeout(() => callback(timestamp), 100));
			`).Do(ctx)
			return err
		}),
		chromedp.Navigate(url),
		chromedp.Poll(`typeof selectTab === "function" &&
			typeof deferredUIReady !== "undefined" &&
			deferredUIReady &&
			document.querySelectorAll(".card").length === 100`, nil),
		chromedp.Evaluate(`
			(() => {
				const nodes = [];
				const edges = [];
				const layers = [];
				const items = [];
				for (let index = 1; index <= 160; index += 1) {
					const id = "w" + index;
					const dependencies = index === 1 ? [] : ["w" + (index - 1)];
					nodes.push({id, title: "Synthetic task " + index, lane: "dispatched", layer: index - 1});
					layers.push([id]);
					items.push({
						id,
						title: "Synthetic task " + index,
						status: "dispatched",
						priority: "P1",
						dependencies,
						dependents: index === 160 ? [] : ["w" + (index + 1)]
					});
					if (index > 1) {
						edges.push({from: "w" + (index - 1), to: id});
					}
				}
				state.snapshot = {
					...state.snapshot,
					items,
					graph: {nodes, edges, layers, cyclic: false},
					progress: {...state.snapshot.progress, total: 160, dispatched: 160},
					plan: {...state.snapshot.plan, in_flight: nodes.map((node) => node.id)}
				};
				state.itemsByID = new Map(items.map((item) => [item.id, item]));
				state.dirtyTabs.add("roadmap");
				renderActiveTab();
			})()
		`, nil),
		chromedp.Poll(`document.querySelectorAll(".card").length === 2`, nil),
		chromedp.Evaluate(`selectTab("tasks")`, nil),
		chromedp.Poll(`document.querySelector("#panel-tasks").hidden === false`, nil),
		chromedp.Evaluate(`selectTab("roadmap")`, nil),
		chromedp.Poll(`document.querySelectorAll(".card").length === 160 &&
			Array.from(document.querySelectorAll("#graph-edges .edge"))
				.reduce((total, path) => total + Number(path.dataset.edgeCount || 1), 0) === 159`, nil),
	); err != nil {
		t.Fatalf("resume roadmap render: %v", err)
	}
	var label string
	if err := chromedp.Run(browser, chromedp.AttributeValue(
		`.card[data-item="w3"]`, "aria-label", &label, nil,
	)); err != nil {
		t.Fatal(err)
	}
	want := "Task w3: Synthetic task 3. Status Dispatched. Priority P1. Dependencies: w2."
	if label != want {
		t.Fatalf("task card accessible name = %q, want %q", label, want)
	}
}

func TestBrowserBatchedRoadmapCompletionKeepsDrawerFocus(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	url := startBrowserTestFeedHandler(t, browserTestSnapshot())

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
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(`
				const relayAnimationFrame = window.requestAnimationFrame.bind(window);
				window.requestAnimationFrame = (callback) =>
					relayAnimationFrame((timestamp) => setTimeout(() => callback(timestamp), 100));
			`).Do(ctx)
			return err
		}),
		chromedp.Navigate(url),
		chromedp.Poll(`typeof openDrawer === "function" &&
			deferredUIReady &&
			document.querySelectorAll(".card").length === 2`, nil),
		chromedp.Evaluate(`
			(() => {
				document.querySelector('.card[data-item="w1"]').focus();
				const nodes = [];
				const edges = [];
				const layers = [];
				const items = [];
				for (let index = 1; index <= 260; index += 1) {
					const id = "w" + index;
					const dependencies = index === 1 ? [] : ["w" + (index - 1)];
					nodes.push({
						id,
						title: "Synthetic task " + index,
						lane: "dispatched",
						layer: index - 1
					});
					layers.push([id]);
					items.push({
						id,
						title: "Synthetic task " + index,
						status: "dispatched",
						lane: "dispatched",
						priority: "P1",
						dependencies,
						dependents: index === 260 ? [] : ["w" + (index + 1)],
						contracts: [],
						notes: [],
						decisions: [],
						artifacts: [],
						warnings: [],
						mailbox: {inbox_ids: [], outbox_ids: []}
					});
					if (index > 1) {
						edges.push({from: "w" + (index - 1), to: id});
					}
				}
				state.snapshot = {
					...state.snapshot,
					items,
					graph: {nodes, edges, layers, cyclic: false},
					progress: {...state.snapshot.progress, total: 260, dispatched: 260},
					plan: {...state.snapshot.plan, in_flight: nodes.map((node) => node.id)}
				};
				state.itemsByID = new Map(items.map((item) => [item.id, item]));
				state.dirtyTabs.add("roadmap");
				renderActiveTab();
				selectItem("w1");
				openDrawer(false);
			})()
		`, nil),
		chromedp.Poll(`document.querySelectorAll(".card").length === 260 &&
			document.querySelector("#drawer").dataset.state === "open"`, nil),
	); err != nil {
		t.Fatalf("complete roadmap while drawer is open: %v", err)
	}
	var focusID string
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.activeElement?.id || ""`, &focusID),
	); err != nil {
		t.Fatal(err)
	}
	if focusID != "detail-panel" {
		t.Fatalf("focus after batched roadmap completion = %q, want detail-panel", focusID)
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
				chromedp.ActionFunc(func(ctx context.Context) error {
					_, err := page.AddScriptToEvaluateOnNewDocument(`
						window.__relayErrors = [];
						window.addEventListener("error", (event) => window.__relayErrors.push(event.message));
						window.addEventListener("unhandledrejection", (event) =>
							window.__relayErrors.push(String(event.reason)));
					`).Do(ctx)
					return err
				}),
				chromedp.Navigate(url),
				chromedp.Poll(`document.querySelector("#feed-state").textContent === "Live · every 3s" &&
					document.querySelector(".card[data-item='w1']").dataset.meta.includes("PR #42") &&
					document.querySelector("#worker-count").textContent === "1"`, nil),
			); err != nil {
				t.Fatalf("initial %s summary: %v", source, err)
			}
			if err := chromedp.Run(browser,
				chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
				chromedp.Poll(
					`document.querySelector("#detail-body").textContent.includes("Initial PR") &&
						document.querySelector("#detail-body").textContent.includes("pane-initial")`,
					nil,
					chromedp.WithPollingTimeout(8*time.Second),
				),
			); err != nil {
				var diagnostic string
				_ = chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify({
					drawer: document.querySelector("#drawer")?.dataset.state,
					detail: document.querySelector("#detail-body")?.textContent,
					feed: document.querySelector("#feed-state")?.textContent,
					schema: state.snapshot?.schema,
					items: state.itemsByID.size,
					deferred: deferredUIReady,
					selected: state.selected,
					drawerOpen: state.drawerOpen,
					errors: window.__relayErrors
				})`, &diagnostic))
				t.Fatalf("initial %s detail: %v: %s", source, err, diagnostic)
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
			nowNanos.Store(initialNow.Add(19 * time.Second).UnixNano())
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
			if call > 1 {
				return programview.ArtifactResponse{
					State: programview.ArtifactStateError, Error: "notes refresh failed",
				}, errors.New("notes refresh failed")
			}
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
		chromedp.Poll(`state.snapshot?.schema === "relay.program.v1" &&
			programController === null &&
			document.querySelectorAll(".card").length === 2`, nil),
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
		chromedp.Click(`button[data-focus-key="art:w2:notes.md"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector("#section-files").textContent.includes(
			"notes refresh failed") &&
			document.querySelector("#section-files").textContent.includes(
				"Last successful check found notes.md missing; current state is unknown.") &&
			!document.querySelector("#section-files").textContent.includes(
				"The worker appends notes")`, nil),
	); err != nil {
		t.Fatalf("failed missing-artifact revalidation: %v", err)
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

func TestBrowserPreservesTaskArtifactScrollAcrossRefresh(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	overflowing := func(marker string, lines int) string {
		return marker + "\n" + strings.Repeat("A long artifact line that keeps the nested viewer overflowing.\n", lines)
	}
	snapshot := browserTestSnapshot()
	snapshot.Items = append([]programview.ItemDTO(nil), snapshot.Items...)
	snapshot.Items[0].Artifacts = []programview.ArtifactDTO{
		{Name: "assignment.md", Path: "assignment.md", Present: true},
		{Name: "task.md", Path: "task.md", Present: true},
	}
	snapshot.Items[1].Artifacts = []programview.ArtifactDTO{
		{Name: "assignment.md", Path: "assignment.md", Present: true},
	}
	snapshot.Items[0].Dependents = []string{"w2"}
	snapshot.Items[0].Contracts = []string{"shared@v1"}
	snapshot.Items[1].Contracts = []string{"shared@v1"}
	snapshot.Contracts = []programview.ContractDTO{{
		Ref: "shared@v1", Name: "shared", Version: 1,
		Artifact: programview.ArtifactDTO{
			Name: "shared.md", Path: "shared.md", Present: true,
		},
	}}
	var artifactMu sync.RWMutex
	artifactContents := map[string]string{
		"task:w1:assignment.md": overflowing("assignment-initial", 120),
		"task:w1:task.md":       "task-second-file",
		"task:w2:assignment.md": overflowing("assignment-second-task", 90),
		"contract:shared@v1":    overflowing("shared-contract", 90),
	}
	setArtifact := func(key, value string) {
		artifactMu.Lock()
		artifactContents[key] = value
		artifactMu.Unlock()
	}
	url := startBrowserTestHandler(t, HandlerOptions{
		Slug: "browser-test",
		Builder: func(_ string, _ string) (programview.Snapshot, error) {
			return snapshot, nil
		},
		ArtifactLoader: func(_ string, selector programview.ArtifactSelector) (programview.ArtifactResponse, error) {
			key := artifactCacheKeyForTest(selector)
			artifactMu.RLock()
			value := artifactContents[key]
			artifactMu.RUnlock()
			name := selector.Name
			if name == "" {
				name = "shared.md"
			}
			return programview.ArtifactResponse{
				State: programview.ArtifactStateLoaded,
				Artifact: programview.ArtifactDTO{
					Name: name, Path: name, Present: true,
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
	browser, cancelTimeout := context.WithTimeout(browser, 30*time.Second)
	defer cancelTimeout()
	if err := chromedp.Run(browser,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(`
				const relayFetch = window.fetch.bind(window);
				window.__relayArtifactCalls = 0;
				window.__relayArtifactCompletions = 0;
				window.fetch = (input, options) => {
					const url = new URL(typeof input === "string" ? input : input.url, window.location.href);
					const artifact = url.pathname === "/api/artifact";
					if (artifact) {
						window.__relayArtifactCalls += 1;
					}
					return relayFetch(input, options).then((response) => {
						if (artifact) {
							window.__relayArtifactCompletions += 1;
						}
						return response;
					});
				};
			`).Do(ctx)
			return err
		}),
		chromedp.Navigate(url),
		chromedp.Poll(`state.snapshot?.schema === "relay.program.v1" &&
			programController === null &&
			document.querySelectorAll(".card").length === 2`, nil),
		chromedp.Evaluate(`clearTimeout(pollTimer); pollTimer = null`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent.startsWith(
			"assignment-initial") && window.__relayArtifactCompletions === 1`, nil),
	); err != nil {
		t.Fatal(err)
	}

	type scrollState struct {
		ScrollTop float64 `json:"scrollTop"`
		Maximum   float64 `json:"maximum"`
		Drawer    float64 `json:"drawer"`
		Selected  string  `json:"selected"`
		Focus     string  `json:"focus"`
	}
	var initial scrollState
	if err := chromedp.Run(browser, chromedp.Evaluate(`(() => {
		const viewer = document.querySelector(".artifact-text");
		const drawer = document.querySelector("#drawer-scroll");
		viewer.scrollTop = viewer.scrollHeight;
		drawer.scrollTop = Math.min(80, drawer.scrollHeight - drawer.clientHeight);
		document.querySelector('button[data-focus-key="art:w1:assignment.md"]').focus();
		return {
			scrollTop: viewer.scrollTop,
			maximum: viewer.scrollHeight - viewer.clientHeight,
			drawer: drawer.scrollTop,
			selected: document.querySelector(
				'button[data-focus-key="art:w1:assignment.md"]').getAttribute("aria-current"),
			focus: document.activeElement?.dataset.focusKey || ""
		};
	})()`, &initial)); err != nil {
		t.Fatal(err)
	}
	if initial.Maximum <= 0 || initial.ScrollTop != initial.Maximum || initial.Drawer <= 0 {
		t.Fatalf("initial scroll state = %+v, want overflowing nested and outer viewers", initial)
	}
	if initial.Selected != "true" || initial.Focus != "art:w1:assignment.md" {
		t.Fatalf("initial selection state = %+v", initial)
	}

	if err := chromedp.Run(browser,
		chromedp.Evaluate(`schedule(0)`, nil),
		chromedp.Poll(`window.__relayArtifactCalls === 2 &&
			window.__relayArtifactCompletions === 2`, nil),
		chromedp.Evaluate(`clearTimeout(pollTimer); pollTimer = null`, nil),
	); err != nil {
		t.Fatalf("automatic artifact refresh: %v", err)
	}
	var automatic scrollState
	if err := chromedp.Run(browser, chromedp.Evaluate(`(() => {
		const viewer = document.querySelector(".artifact-text");
		return {
			scrollTop: viewer.scrollTop,
			maximum: viewer.scrollHeight - viewer.clientHeight,
			drawer: document.querySelector("#drawer-scroll").scrollTop,
			selected: document.querySelector(
				'button[data-focus-key="art:w1:assignment.md"]').getAttribute("aria-current"),
			focus: document.activeElement?.dataset.focusKey || ""
		};
	})()`, &automatic)); err != nil {
		t.Fatal(err)
	}
	if automatic.Maximum-automatic.ScrollTop > 1 {
		t.Fatalf("automatic refresh scroll state = %+v, want bottom distance <= 1", automatic)
	}
	if automatic.Selected != "true" || automatic.Focus != "art:w1:assignment.md" {
		t.Fatalf("automatic refresh selection state = %+v", automatic)
	}
	if automatic.Drawer != initial.Drawer {
		t.Fatalf("automatic refresh outer scroll = %v, want %v", automatic.Drawer, initial.Drawer)
	}

	setArtifact("task:w1:assignment.md", overflowing("assignment-longer", 180))
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`window.__relayArtifactCalls === 3 &&
			window.__relayArtifactCompletions === 3 &&
			document.querySelector(".artifact-text")?.textContent.startsWith("assignment-longer")`, nil),
		chromedp.Evaluate(`clearTimeout(pollTimer); pollTimer = null`, nil),
	); err != nil {
		t.Fatalf("changed artifact refresh: %v", err)
	}
	var changed scrollState
	if err := chromedp.Run(browser, chromedp.Evaluate(`(() => {
		const viewer = document.querySelector(".artifact-text");
		return {
			scrollTop: viewer.scrollTop,
			maximum: viewer.scrollHeight - viewer.clientHeight,
			drawer: document.querySelector("#drawer-scroll").scrollTop,
			selected: document.querySelector(
				'button[data-focus-key="art:w1:assignment.md"]').getAttribute("aria-current"),
			focus: document.activeElement?.dataset.focusKey || ""
		};
	})()`, &changed)); err != nil {
		t.Fatal(err)
	}
	if changed.Maximum-changed.ScrollTop > 1 {
		t.Fatalf("changed refresh scroll state = %+v, want bottom distance <= 1", changed)
	}
	if changed.Selected != "true" || changed.Focus != "art:w1:assignment.md" {
		t.Fatalf("changed refresh selection state = %+v", changed)
	}

	var previousOffset float64
	if err := chromedp.Run(browser, chromedp.Evaluate(`(() => {
		const viewer = document.querySelector(".artifact-text");
		viewer.scrollTop = Math.min(137, viewer.scrollHeight - viewer.clientHeight - 20);
		return viewer.scrollTop;
	})()`, &previousOffset)); err != nil {
		t.Fatal(err)
	}
	if previousOffset <= 0 {
		t.Fatalf("non-bottom offset = %v, want > 0", previousOffset)
	}
	setArtifact("task:w1:assignment.md", overflowing("assignment-shorter", 80))
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`window.__relayArtifactCalls === 4 &&
			window.__relayArtifactCompletions === 4 &&
			document.querySelector(".artifact-text")?.textContent.startsWith("assignment-shorter")`, nil),
		chromedp.Evaluate(`clearTimeout(pollTimer); pollTimer = null`, nil),
	); err != nil {
		t.Fatalf("non-bottom artifact refresh: %v", err)
	}
	var offset scrollState
	if err := chromedp.Run(browser, chromedp.Evaluate(`(() => {
		const viewer = document.querySelector(".artifact-text");
		return {
			scrollTop: viewer.scrollTop,
			maximum: viewer.scrollHeight - viewer.clientHeight,
			drawer: document.querySelector("#drawer-scroll").scrollTop,
			selected: document.querySelector(
				'button[data-focus-key="art:w1:assignment.md"]').getAttribute("aria-current"),
			focus: document.activeElement?.dataset.focusKey || ""
		};
	})()`, &offset)); err != nil {
		t.Fatal(err)
	}
	wantOffset := min(previousOffset, offset.Maximum)
	if offset.ScrollTop == 0 || offset.ScrollTop < wantOffset-1 || offset.ScrollTop > wantOffset+1 {
		t.Fatalf("non-bottom refresh scroll state = %+v, want offset %v ± 1", offset, wantOffset)
	}

	var otherFile scrollState
	if err := chromedp.Run(browser,
		chromedp.Click(`button[data-focus-key="art:w1:task.md"]`, chromedp.ByQuery),
		chromedp.Poll(`window.__relayArtifactCalls === 5 &&
			window.__relayArtifactCompletions === 5 &&
			document.querySelector(".artifact-text")?.textContent.startsWith("task-second-file")`, nil),
		chromedp.Evaluate(`(() => {
			const viewer = document.querySelector(".artifact-text");
			return {
				scrollTop: viewer.scrollTop,
				maximum: viewer.scrollHeight - viewer.clientHeight,
				selected: document.querySelector(
					'button[data-focus-key="art:w1:task.md"]').getAttribute("aria-current"),
				focus: document.activeElement?.dataset.focusKey || ""
			};
		})()`, &otherFile),
	); err != nil {
		t.Fatalf("select other file: %v", err)
	}
	if otherFile.Maximum != 0 || otherFile.ScrollTop != 0 ||
		otherFile.Selected != "true" || otherFile.Focus != "art:w1:task.md" {
		t.Fatalf("other file state = %+v, want initial top with selected focused control", otherFile)
	}

	setArtifact("task:w1:task.md", overflowing("task-second-file-grown", 90))
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`window.__relayArtifactCalls === 6 &&
			window.__relayArtifactCompletions === 6 &&
			document.querySelector(".artifact-text")?.textContent.startsWith(
				"task-second-file-grown")`, nil),
		chromedp.Evaluate(`clearTimeout(pollTimer); pollTimer = null`, nil),
	); err != nil {
		t.Fatalf("grow non-overflowing artifact: %v", err)
	}
	var grownFile scrollState
	if err := chromedp.Run(browser, chromedp.Evaluate(`(() => {
		const viewer = document.querySelector(".artifact-text");
		return {
			scrollTop: viewer.scrollTop,
			maximum: viewer.scrollHeight - viewer.clientHeight,
			selected: document.querySelector(
				'button[data-focus-key="art:w1:task.md"]').getAttribute("aria-current"),
			focus: document.activeElement?.dataset.focusKey || ""
		};
	})()`, &grownFile)); err != nil {
		t.Fatal(err)
	}
	if grownFile.Maximum <= 0 || grownFile.ScrollTop != 0 ||
		grownFile.Selected != "true" || grownFile.Focus != "art:w1:task.md" {
		t.Fatalf("grown file state = %+v, want overflowing viewer kept at initial top", grownFile)
	}

	type navigationState struct {
		ScrollTop float64 `json:"scrollTop"`
		Selected  string  `json:"selected"`
		Focus     string  `json:"focus"`
	}
	var otherTask navigationState
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`(() => {
			const viewer = document.querySelector(".artifact-text");
			viewer.scrollTop = Math.min(137, viewer.scrollHeight - viewer.clientHeight - 20);
			if (viewer.scrollTop <= 0) {
				throw new Error("cross-task source viewer did not scroll");
			}
		})()`, nil),
		chromedp.Evaluate(
			`document.dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true}))`, nil,
		),
		chromedp.Poll(`document.querySelector("#drawer").hidden === true`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w2"]').click()`, nil),
		chromedp.Poll(`window.__relayArtifactCalls === 7 &&
			window.__relayArtifactCompletions === 7 &&
			document.querySelector(".artifact-text")?.textContent.startsWith(
				"assignment-second-task")`, nil),
		chromedp.Evaluate(`({
			scrollTop: document.querySelector(".artifact-text").scrollTop,
			selected: document.querySelector(
				'button[data-focus-key="art:w2:assignment.md"]').getAttribute("aria-current"),
			focus: document.activeElement?.id || ""
		})`, &otherTask),
	); err != nil {
		t.Fatalf("select other task: %v", err)
	}
	if otherTask.ScrollTop != 0 || otherTask.Selected != "true" || otherTask.Focus != "detail-panel" {
		t.Fatalf("other task state = %+v, want initial top with existing selection and focus", otherTask)
	}

	if err := chromedp.Run(browser,
		chromedp.Evaluate(`window.__relayArtifactCalls = 0;
			window.__relayArtifactCompletions = 0`, nil),
		chromedp.Click(`button[data-focus-key="contract:w2:shared@v1"]`, chromedp.ByQuery),
		chromedp.Poll(`window.__relayArtifactCalls === 1 &&
			window.__relayArtifactCompletions === 1 &&
			document.querySelector(".artifact-text")?.textContent.startsWith("shared-contract")`, nil),
		chromedp.Evaluate(
			`document.dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true}))`, nil,
		),
		chromedp.Poll(`document.querySelector("#drawer").hidden === true`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		chromedp.Poll(`window.__relayArtifactCalls === 2 &&
			window.__relayArtifactCompletions === 2`, nil),
		chromedp.Click(`button[data-focus-key="contract:w1:shared@v1"]`, chromedp.ByQuery),
		chromedp.Poll(`window.__relayArtifactCalls === 3 &&
			window.__relayArtifactCompletions === 3 &&
			document.querySelector(".artifact-text")?.textContent.startsWith("shared-contract")`, nil),
	); err != nil {
		t.Fatalf("select shared contract in both tasks: %v", err)
	}
	var contractOffset float64
	if err := chromedp.Run(browser, chromedp.Evaluate(`(() => {
		const viewer = document.querySelector(".artifact-text");
		viewer.scrollTop = Math.min(137, viewer.scrollHeight - viewer.clientHeight - 20);
		return viewer.scrollTop;
	})()`, &contractOffset)); err != nil {
		t.Fatal(err)
	}
	if contractOffset <= 0 {
		t.Fatalf("shared contract source offset = %v, want > 0", contractOffset)
	}

	var otherTaskContract navigationState
	if err := chromedp.Run(browser,
		chromedp.Click(`button[data-focus-key="dependent:w2"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector("#drawer-id").textContent === "w2" &&
			document.querySelector(".artifact-text")?.textContent.startsWith("shared-contract")`, nil),
		chromedp.Evaluate(`({
			scrollTop: document.querySelector(".artifact-text").scrollTop,
			selected: document.querySelector(
				'button[data-focus-key="contract:w2:shared@v1"]').getAttribute("aria-current"),
			focus: document.activeElement?.dataset.focusKey || ""
		})`, &otherTaskContract),
	); err != nil {
		t.Fatalf("open shared contract in dependent task: %v", err)
	}
	if otherTaskContract.ScrollTop != 0 || otherTaskContract.Selected != "true" {
		t.Fatalf(
			"other task shared contract state = %+v, want initial top with existing selection",
			otherTaskContract,
		)
	}
}

func TestBrowserRefreshQueuesOneArtifactRevalidationDuringInflightLoad(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	initial := browserTestSnapshot()
	updated := initial
	updated.Graph.Nodes = append([]programview.GraphNodeDTO(nil), initial.Graph.Nodes...)
	updated.Items = append([]programview.ItemDTO(nil), initial.Items...)
	updated.Graph.Nodes[0].Title = "First task after snapshot refresh"
	updated.Items[0].Title = updated.Graph.Nodes[0].Title
	now := time.Date(2026, 9, 15, 20, 0, 0, 0, time.UTC)
	var nowNanos atomic.Int64
	nowNanos.Store(now.UnixNano())
	var builds atomic.Int32
	url := startBrowserTestHandler(t, HandlerOptions{
		Slug: "browser-test",
		Now:  func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() },
		Builder: func(_ string, _ string) (programview.Snapshot, error) {
			if builds.Add(1) == 1 {
				return initial, nil
			}
			return updated, nil
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
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(`
				const relayFetch = window.fetch.bind(window);
				window.__relayArtifactCalls = 0;
				window.__relayReleaseFirstArtifact = null;
				window.fetch = (input, options) => {
					const url = new URL(typeof input === "string" ? input : input.url, window.location.href);
					if (url.pathname !== "/api/artifact") {
						return relayFetch(input, options);
					}
					window.__relayArtifactCalls += 1;
					const call = window.__relayArtifactCalls;
					const text = call === 1 ? "old-on-disk-content" : "new-on-disk-content";
					const response = () => new Response(JSON.stringify({
						state: "loaded",
						artifact: {
							name: "assignment.md",
							path: "assignment.md",
							present: true,
							size: text.length,
							updated_at: "",
							truncated: false,
							text
						}
					}), {
						status: 200,
						headers: {"Content-Type": "application/json", "ETag": '"' + call + '"'}
					});
					if (call !== 1) {
						return Promise.resolve(response());
					}
					return new Promise((resolve) => {
						window.__relayReleaseFirstArtifact = () => resolve(response());
					});
				};
			`).Do(ctx)
			return err
		}),
		chromedp.Navigate(url),
		chromedp.Poll(`state.snapshot?.schema === "relay.program.v1" &&
			programController === null &&
			document.querySelectorAll(".card").length === 2`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		chromedp.Poll(`window.__relayArtifactCalls === 1 &&
			typeof window.__relayReleaseFirstArtifact === "function"`, nil),
	); err != nil {
		t.Fatalf("start delayed artifact load: %v", err)
	}

	nowNanos.Store(now.Add(3 * time.Second).UnixNano())
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`document.querySelector('.card[data-item="w1"]').textContent.includes(
			"First task after snapshot refresh")`, nil),
		chromedp.Evaluate(`window.__relayReleaseFirstArtifact()`, nil),
	); err != nil {
		t.Fatalf("complete overlapping snapshot refresh: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	var result struct {
		Calls int    `json:"calls"`
		Text  string `json:"text"`
	}
	if err := chromedp.Run(browser, chromedp.Evaluate(`({
		calls: window.__relayArtifactCalls,
		text: document.querySelector(".artifact-text")?.textContent || ""
	})`, &result)); err != nil {
		t.Fatal(err)
	}
	if result.Calls != 2 || result.Text != "new-on-disk-content" {
		var diagnostic string
		_ = chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify({
			calls: window.__relayArtifactCalls,
			text: document.querySelector(".artifact-text")?.textContent,
			card: document.querySelector('.card[data-item="w1"]')?.textContent
		})`, &diagnostic))
		t.Fatalf("follow-up artifact revalidation = %+v: %s", result, diagnostic)
	}
}

func TestBrowserReopenQueuesOneArtifactRevalidationDuringInflightLoad(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	initial := browserTestSnapshot()
	updated := initial
	updated.Graph.Nodes = append([]programview.GraphNodeDTO(nil), initial.Graph.Nodes...)
	updated.Items = append([]programview.ItemDTO(nil), initial.Items...)
	updated.Graph.Nodes[0].Title = "First task after closed-drawer refresh"
	updated.Items[0].Title = updated.Graph.Nodes[0].Title
	now := time.Date(2026, 9, 15, 20, 0, 0, 0, time.UTC)
	var nowNanos atomic.Int64
	nowNanos.Store(now.UnixNano())
	var builds atomic.Int32
	url := startBrowserTestHandler(t, HandlerOptions{
		Slug: "browser-test",
		Now:  func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() },
		Builder: func(_ string, _ string) (programview.Snapshot, error) {
			if builds.Add(1) == 1 {
				return initial, nil
			}
			return updated, nil
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
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(`
				const relayFetch = window.fetch.bind(window);
				window.__relayArtifactCalls = 0;
				window.__relayReleaseArtifacts = {};
				window.fetch = (input, options) => {
					const url = new URL(typeof input === "string" ? input : input.url, window.location.href);
					if (url.pathname !== "/api/artifact") {
						return relayFetch(input, options);
					}
					window.__relayArtifactCalls += 1;
					const call = window.__relayArtifactCalls;
					const text = call === 1 ? "old-on-disk-content" : "new-on-disk-content";
					const response = () => new Response(JSON.stringify({
						state: "loaded",
						artifact: {
							name: "assignment.md",
							path: "assignment.md",
							present: true,
							size: text.length,
							updated_at: "",
							truncated: false,
							text
						}
					}), {
						status: 200,
						headers: {"Content-Type": "application/json", "ETag": '"' + call + '"'}
					});
					return new Promise((resolve) => {
						window.__relayReleaseArtifacts[call] = () => resolve(response());
					});
				};
			`).Do(ctx)
			return err
		}),
		chromedp.Navigate(url),
		chromedp.Poll(`state.snapshot?.schema === "relay.program.v1" &&
			programController === null &&
			document.querySelectorAll(".card").length === 2`, nil),
		chromedp.Evaluate(`clearTimeout(pollTimer); pollTimer = null`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		chromedp.Poll(`window.__relayArtifactCalls === 1 &&
			typeof window.__relayReleaseArtifacts[1] === "function"`, nil),
		chromedp.Evaluate(
			`document.dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true}))`, nil,
		),
		chromedp.Poll(`document.querySelector("#drawer").hidden === true`, nil),
	); err != nil {
		t.Fatalf("start delayed artifact load and close drawer: %v", err)
	}

	nowNanos.Store(now.Add(3 * time.Second).UnixNano())
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`document.querySelector('.card[data-item="w1"]').textContent.includes(
			"First task after closed-drawer refresh")`, nil),
		chromedp.Evaluate(`clearTimeout(pollTimer); pollTimer = null`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		chromedp.Poll(`document.querySelector("#drawer").dataset.state === "open" &&
			window.__relayArtifactCalls === 1`, nil),
		chromedp.Evaluate(`window.__relayReleaseArtifacts[1]()`, nil),
		chromedp.Poll(`window.__relayArtifactCalls === 2 &&
			typeof window.__relayReleaseArtifacts[2] === "function"`, nil),
		chromedp.Evaluate(`window.__relayReleaseArtifacts[2]()`, nil),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent ===
			"new-on-disk-content"`, nil),
	); err != nil {
		t.Fatalf("revalidate artifact after reopening drawer: %v", err)
	}
	var calls int
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`window.__relayArtifactCalls`, &calls),
	); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("artifact requests = %d, want exactly 2", calls)
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
			window.__relayArtifactCalls = {};
			window.fetch = (input, options) => {
				const url = new URL(typeof input === "string" ? input : input.url, window.location.href);
				if (url.pathname !== "/api/artifact") {
					return relayFetch(input, options);
				}
				const item = url.searchParams.get("item");
				const name = url.searchParams.get("name");
				const key = item + ":" + name;
				const call = (window.__relayArtifactCalls[key] || 0) + 1;
				window.__relayArtifactCalls[key] = call;
				const body = JSON.stringify({
					state: "loaded",
					artifact: {
						name,
						path: name,
						present: true,
						size: key.length,
						updated_at: "",
						truncated: false,
						text: key + ":" + call
					}
				});
				const response = () => new Response(body, {
					status: 200,
					headers: {"Content-Type": "application/json", "ETag": '"' + key + '"'}
				});
				if (key !== "w1:assignment.md" && key !== "w2:task.md") {
					return Promise.resolve(response());
				}
				window.__relayPendingArtifacts[key] = call;
				window.__relayReleaseArtifacts[key] = window.__relayReleaseArtifacts[key] || {};
				return new Promise((resolve) => {
					window.__relayReleaseArtifacts[key][call] = () => resolve(response());
				}).then((value) => {
					window.__relayCompletedArtifacts[key] = call;
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
		chromedp.Poll(`state.snapshot?.schema === "relay.program.v1" &&
			programController === null &&
			document.querySelectorAll(".card").length === 2`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		chromedp.Poll(`window.__relayPendingArtifacts["w1:assignment.md"] === 1`, nil),
		chromedp.Evaluate(
			`document.dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true}))`, nil,
		),
		chromedp.Poll(`document.querySelector("#drawer").hidden === true`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w2"]').click()`, nil),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent === "w2:assignment.md:1"`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w1"]').click()`, nil),
		chromedp.Poll(`window.__relayPendingArtifacts["w1:assignment.md"] === 2`, nil),
		chromedp.Evaluate(`window.__relayReleaseArtifacts["w1:assignment.md"][2]()`, nil),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent === "w1:assignment.md:2"`, nil),
		chromedp.Evaluate(`window.__relayReleaseArtifacts["w1:assignment.md"][1]()`, nil),
		chromedp.Poll(`window.__relayCompletedArtifacts["w1:assignment.md"] === 1`, nil),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent === "w1:assignment.md:2"`, nil),
		chromedp.Evaluate(
			`document.dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true}))`, nil,
		),
		chromedp.Poll(`document.querySelector("#drawer").hidden === true`, nil),
		chromedp.Evaluate(`document.querySelector('.card[data-item="w2"]').click()`, nil),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent === "w2:assignment.md:2"`, nil),
		chromedp.Click(`button[data-focus-key="art:w2:task.md"]`, chromedp.ByQuery),
		chromedp.Poll(`window.__relayPendingArtifacts["w2:task.md"] === 1`, nil),
		chromedp.Click(`button[data-focus-key="art:w2:plan.md"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent === "w2:plan.md:1"`, nil),
		chromedp.Evaluate(`window.__relayReleaseArtifacts["w2:task.md"][1]()`, nil),
		chromedp.Poll(`window.__relayCompletedArtifacts["w2:task.md"] === 1`, nil),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent === "w2:plan.md:1"`, nil),
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
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(`
				window.__relayErrors = [];
				window.addEventListener("error", (event) => window.__relayErrors.push(event.message));
				window.addEventListener("unhandledrejection", (event) =>
					window.__relayErrors.push(String(event.reason)));
			`).Do(ctx)
			return err
		}),
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

func TestBrowserReconcilesRemovedContractSelection(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	initial := browserTestSnapshot()
	initial.Items[0].Contracts = []string{"a@v1", "b@v1"}
	initial.Contracts = []programview.ContractDTO{
		{Ref: "a@v1", Name: "a", Version: 1, Artifact: programview.ArtifactDTO{Name: "a.md", Path: "a.md"}},
		{Ref: "b@v1", Name: "b", Version: 1, Artifact: programview.ArtifactDTO{Name: "b.md", Path: "b.md"}},
	}
	updated := initial
	updated.Items = append([]programview.ItemDTO(nil), initial.Items...)
	updated.Items[0].Contracts = []string{"a@v1"}
	updated.Contracts = initial.Contracts[:1]

	now := time.Date(2026, 9, 14, 20, 0, 0, 0, time.UTC)
	var builds atomic.Int32
	url := startBrowserTestHandler(t, HandlerOptions{
		Slug: "browser-test",
		Now:  func() time.Time { return now },
		Builder: func(_ string, _ string) (programview.Snapshot, error) {
			if builds.Add(1) == 1 {
				return initial, nil
			}
			return updated, nil
		},
		ArtifactLoader: func(_ string, selector programview.ArtifactSelector) (programview.ArtifactResponse, error) {
			value := selector.Ref
			return programview.ArtifactResponse{
				State: programview.ArtifactStateLoaded,
				Artifact: programview.ArtifactDTO{
					Name: selector.Ref + ".md", Path: selector.Ref + ".md",
					Present: true, Size: int64(len(value)), Text: &value,
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
	); err != nil {
		t.Fatalf("open contract task: %v", err)
	}
	if err := chromedp.Run(browser,
		chromedp.Click(`button[data-focus-key="contract:w1:b@v1"]`, chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector(".artifact-text")?.textContent === "b@v1"`,
			nil,
			chromedp.WithPollingTimeout(8*time.Second),
		),
	); err != nil {
		var diagnostic string
		_ = chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify({
			buttons: Array.from(document.querySelectorAll("[data-focus-key]"), (node) => node.dataset.focusKey),
			detail: document.querySelector("#detail-body")?.textContent,
			schema: state.snapshot?.schema,
			items: state.itemsByID.size,
			selected: state.selected,
			drawer: state.drawerOpen,
			deferred: deferredUIReady,
			errors: window.__relayErrors
		})`, &diagnostic))
		t.Fatalf("select contract before refresh: %v: %s", err, diagnostic)
	}

	now = now.Add(3 * time.Second)
	if err := chromedp.Run(browser,
		chromedp.Evaluate(`document.querySelector("#refresh").click()`, nil),
		chromedp.Poll(`document.querySelector(
			'button[data-focus-key="contract:w1:a@v1"]').getAttribute("aria-current") === "true"`, nil),
		chromedp.Poll(`document.querySelector(".artifact-text")?.textContent === "a@v1"`, nil),
		chromedp.Poll(`document.querySelector(
			'button[data-focus-key="contract:w1:b@v1"]') === null`, nil),
	); err != nil {
		t.Fatalf("removed contract reconciliation: %v", err)
	}
}

func startBrowserTestHandler(t *testing.T, options HandlerOptions) string {
	t.Helper()
	return startBrowserHTTPHandler(t, func(port int) http.Handler {
		options.Port = port
		return NewHandler(options)
	})
}

func startBrowserTestFeedHandler(t *testing.T, snapshot programview.Snapshot) string {
	t.Helper()
	feed := newSnapshotFeed(snapshot, time.Minute, time.Now, func() (programview.Snapshot, error) {
		return snapshot, nil
	})
	return startBrowserHTTPHandler(t, func(port int) http.Handler {
		return newHandler(
			snapshot.Program.Slug,
			strconv.Itoa(port),
			newSnapshotCache(time.Minute, time.Now, nil),
			feed,
			nil,
		)
	})
}

func startBrowserHTTPHandler(t *testing.T, newHandler func(int) http.Handler) string {
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
	server := &http.Server{Handler: newHandler(port), ReadHeaderTimeout: time.Second}
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
