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
	"testing"
	"time"

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
	eventually(t, time.Second, func() bool {
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := program.New("local-first", "Local First", repo, "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}

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
