package programui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
)

func TestServeUsesLoopbackDynamicPortOpensAndStopsOnContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := newLineWriter()
	opened := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Options{
			Slug: "relay-v1", Port: 0, Open: true, Out: output,
			Builder: func(_, detail string) (programview.Snapshot, error) {
				return programview.Snapshot{
					Schema: programview.SchemaVersion, DetailItem: detail,
					Items: []programview.ItemDTO{}, Contracts: []programview.ContractDTO{},
					Warnings: []string{},
				}, nil
			},
			OpenBrowser: func(target string) error {
				opened <- target
				return nil
			},
		})
	}()

	var url string
	select {
	case url = <-output.lines:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not print URL")
	}
	url = strings.TrimSpace(url)
	openedURL := <-opened
	if !strings.HasPrefix(url, "http://127.0.0.1:") || openedURL != url {
		t.Fatalf("URL = %q, opened = %q", url, openedURL)
	}
	response, err := http.Get(url + "/api/program")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("API status = %d", response.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop after cancellation")
	}
}

func TestServeWarnsAndContinuesWhenBrowserOpenFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := newLineWriter()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Options{
			Slug: "relay-v1", Port: 0, Open: true, Out: output,
			Builder: func(_, detail string) (programview.Snapshot, error) {
				return programview.Snapshot{
					Schema: programview.SchemaVersion, DetailItem: detail,
					Items: []programview.ItemDTO{}, Contracts: []programview.ContractDTO{},
					Warnings: []string{},
				}, nil
			},
			OpenBrowser: func(string) error {
				return errors.New("no browser")
			},
		})
	}()

	url := strings.TrimSpace(<-output.lines)
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("URL = %q", url)
	}
	select {
	case warning := <-output.lines:
		if !strings.Contains(warning, "warning") || !strings.Contains(warning, "no browser") {
			t.Fatalf("browser warning = %q", warning)
		}
	case err := <-done:
		t.Fatalf("server stopped after browser failure: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("server did not report browser warning")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop after cancellation")
	}
}

func TestServeDefaultHerdrListerTimesOutInsteadOfHanging(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := program.New("bounded-herdr", "Bounded Herdr", repo, "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	herdrPath := filepath.Join(binDir, "herdr")
	if err := os.WriteFile(herdrPath, []byte("#!/bin/sh\nexec /bin/sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	previousTimeout := programUIHerdrCommandTimeout
	programUIHerdrCommandTimeout = 20 * time.Millisecond
	t.Cleanup(func() { programUIHerdrCommandTimeout = previousTimeout })

	ctx, cancel := context.WithCancel(context.Background())
	output := newLineWriter()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Options{
			Slug: p.Slug, Port: 0, Open: false, Out: output,
		})
	}()

	var url string
	select {
	case line := <-output.lines:
		url = strings.TrimSpace(line)
	case err := <-done:
		t.Fatalf("server stopped before listening: %v", err)
	case <-time.After(time.Second):
		t.Fatal("server hung while listing Herdr agents")
	}
	response, err := http.Get(url + "/api/program")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("API status = %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"status":"loading"`) {
		t.Fatalf("initial API response did not report pending enrichment: %s", body)
	}
	eventually(t, 2*time.Second, func() bool {
		response, requestErr := http.Get(url + "/api/program")
		if requestErr != nil {
			return false
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		return readErr == nil && strings.Contains(string(body), "context deadline exceeded")
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop after cancellation")
	}
}

func TestServePublishesLocalSnapshotBeforeDelayedSources(t *testing.T) {
	fixture := newReferenceProgramFixture(t)
	p := fixture.program

	release := make(chan struct{})
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := newLineWriter()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Options{
			Slug: p.Slug, Port: 0, Open: false, Out: output,
			Agents: &controlledAgentLister{release: release, started: started},
		})
	}()

	var url string
	select {
	case line := <-output.lines:
		url = strings.TrimSpace(line)
	case err := <-done:
		t.Fatalf("server stopped before listening: %v", err)
	case <-time.After(time.Second):
		t.Fatal("delayed Herdr source blocked URL publication")
	}
	response, err := http.Get(url + "/api/program")
	if err != nil {
		t.Fatal(err)
	}
	var initial programview.Snapshot
	if err := json.NewDecoder(response.Body).Decode(&initial); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if initial.SourceHealth.Herdr.Status != "loading" {
		t.Fatalf("initial Herdr status = %q, want loading", initial.SourceHealth.Herdr.Status)
	}
	if initial.Refresh.Status != "partial" {
		t.Fatalf("initial refresh status = %q, want partial", initial.Refresh.Status)
	}
	if len(initial.ProgramArtifacts) == 0 || len(initial.Contracts) == 0 ||
		len(initial.OpenDecisions) == 0 || len(initial.Items) == 0 ||
		initial.Items[0].Repo == "" || initial.Items[0].ProjectSlug == "" ||
		len(initial.Items[0].Artifacts) == 0 {
		t.Fatalf("initial local metadata is incomplete: %+v", initial)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background refresh did not start")
	}
	close(release)
	eventually(t, time.Second, func() bool {
		response, requestErr := http.Get(url + "/api/program")
		if requestErr != nil {
			return false
		}
		defer response.Body.Close()
		var snapshot programview.Snapshot
		return json.NewDecoder(response.Body).Decode(&snapshot) == nil &&
			snapshot.SourceHealth.Herdr.Status == "ok"
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop after cancellation")
	}
}

func TestServeAttributesExternalFailuresToTheirSources(t *testing.T) {
	fixture := newReferenceProgramFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := newLineWriter()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Options{
			Slug: fixture.program.Slug, Port: 0, Open: false, Out: output,
			GitHub: &controlledFetcher{err: errors.New("GitHub unavailable")},
			Agents: &controlledAgentLister{err: errors.New("Herdr unavailable")},
		})
	}()

	url := waitForProgramURL(t, output, done)
	eventually(t, 2*time.Second, func() bool {
		response, err := http.Get(url + "/api/program")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		var snapshot programview.Snapshot
		if json.NewDecoder(response.Body).Decode(&snapshot) != nil {
			return false
		}
		githubWarnings := strings.Join(snapshot.SourceHealth.GitHub.Warnings, "\n")
		herdrWarnings := strings.Join(snapshot.SourceHealth.Herdr.Warnings, "\n")
		return snapshot.Refresh.Status == "fresh" &&
			snapshot.SourceHealth.GitHub.Status == "degraded" &&
			strings.Contains(githubWarnings, "GitHub unavailable") &&
			!strings.Contains(githubWarnings, "Herdr unavailable") &&
			snapshot.SourceHealth.Herdr.Status == "degraded" &&
			strings.Contains(herdrWarnings, "Herdr unavailable") &&
			!strings.Contains(herdrWarnings, "GitHub unavailable")
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop after cancellation")
	}
}

func TestServeRetainsAndRecoversEachExternalSource(t *testing.T) {
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

			ctx, cancel := context.WithCancel(context.Background())
			output := newLineWriter()
			done := make(chan error, 1)
			go func() {
				done <- Serve(ctx, Options{
					Slug: fixture.program.Slug, Port: 0, Open: false, Out: output,
					Now: now, GitHub: github, Agents: agents,
				})
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("Serve: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Error("program UI did not stop")
				}
			})
			url := waitForProgramURL(t, output, done)

			var initial programview.Snapshot
			eventually(t, 2*time.Second, func() bool {
				initial = getProgramSnapshot(t, url)
				item := initial.Items[0]
				return initial.Refresh.Status == "fresh" && item.LivePR != nil &&
					item.LivePR.Title == "Initial PR" && item.Worker != nil &&
					item.Worker.PaneID == "pane-initial"
			})

			phase.Store(1)
			nowNanos.Store(initialNow.Add(13 * time.Second).UnixNano())
			_ = getProgramSnapshot(t, url)
			var stale programview.Snapshot
			eventually(t, 2*time.Second, func() bool {
				stale = getProgramSnapshot(t, url)
				item := stale.Items[0]
				if source == "GitHub" {
					return stale.SourceHealth.GitHub.Status == "degraded" &&
						item.LivePR != nil && item.LivePR.Stale &&
						item.LivePR.Title == "Initial PR"
				}
				return stale.SourceHealth.Herdr.Status == "degraded" &&
					item.Worker != nil && item.Worker.PaneID == "pane-initial"
			})

			phase.Store(2)
			nowNanos.Store(initialNow.Add(16 * time.Second).UnixNano())
			_ = getProgramSnapshot(t, url)
			eventually(t, 2*time.Second, func() bool {
				recovered := getProgramSnapshot(t, url)
				item := recovered.Items[0]
				if source == "GitHub" {
					return recovered.SourceHealth.GitHub.Status == "ok" &&
						item.LivePR != nil && !item.LivePR.Stale &&
						item.LivePR.Title == "Recovered PR"
				}
				return recovered.SourceHealth.Herdr.Status == "ok" &&
					item.Worker != nil && item.Worker.PaneID == "pane-recovered"
			})
		})
	}
}

func getProgramSnapshot(t *testing.T, url string) programview.Snapshot {
	t.Helper()
	response, err := http.Get(url + "/api/program")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var snapshot programview.Snapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

type lineWriter struct {
	lines chan string
}

func newLineWriter() *lineWriter {
	return &lineWriter{lines: make(chan string, 4)}
}

func (w *lineWriter) Write(data []byte) (int, error) {
	w.lines <- string(data)
	return len(data), nil
}
