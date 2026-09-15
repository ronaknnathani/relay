package programui

import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/ronaknnathani/relay/internal/programview"
)

func TestBrowserArtifactLoadingAndOrdering(t *testing.T) {
	if testing.Short() || getenv("RELAY_BROWSER_TESTS") == "" {
		t.Skip("set RELAY_BROWSER_TESTS=1 to run browser tests")
	}
	snapshot := browserTestSnapshot()
	var detailItems []string
	var detailsMu sync.Mutex
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var firstOnce sync.Once
	loader := func(_ string, selector programview.ArtifactSelector) (programview.ArtifactResponse, error) {
		if selector.Item == "w1" {
			firstOnce.Do(func() { close(firstStarted) })
			<-releaseFirst
		}
		value := selector.Item + ":" + selector.Name
		return programview.ArtifactResponse{
			State: programview.ArtifactStateLoaded,
			Artifact: programview.ArtifactDTO{
				Name: selector.Name, Path: selector.Name, Present: true,
				Size: int64(len(value)), Text: &value,
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
	browser, cancelTimeout := context.WithTimeout(browser, 10*time.Second)
	defer cancelTimeout()
	url := "http://127.0.0.1:" + portText
	if err := chromedp.Run(browser,
		chromedp.Navigate(url),
		chromedp.Poll(`document.querySelectorAll(".card").length === 2`, nil),
		chromedp.Click(`.card[data-item="w1"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector("#drawer-title").textContent === "First task"`, nil),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first artifact request did not start")
	}
	if err := chromedp.Run(browser,
		chromedp.Evaluate(
			`document.dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true}))`, nil,
		),
		chromedp.Poll(`document.querySelector("#drawer").hidden === true`, nil),
		chromedp.Click(`.card[data-item="w2"]`, chromedp.ByQuery),
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
	detailsMu.Lock()
	defer detailsMu.Unlock()
	for _, detail := range detailItems {
		if detail != "" {
			t.Fatalf("browser requested selected-item snapshot %q", detail)
		}
	}
}

func browserTestSnapshot() programview.Snapshot {
	artifact := func() []programview.ArtifactDTO {
		return []programview.ArtifactDTO{{
			Name: "assignment.md", Path: "assignment.md", Present: true, Size: 12,
		}}
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
