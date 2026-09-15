package programui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/programview"
)

func TestHandlerSecurityRoutesAndSelectedItem(t *testing.T) {
	expectedProgress := programview.ProgressDTO{
		Total: 39, Pending: 14, Dispatched: 1, Merged: 21,
		Canceled: 3, Completed: 24, Percent: 53,
	}
	handler := NewHandler(HandlerOptions{
		Slug: "relay-v1",
		Port: 4321,
		Builder: func(slug, detail string) (programview.Snapshot, error) {
			return programview.Snapshot{
				Schema:     programview.SchemaVersion,
				DetailItem: detail,
				Progress:   expectedProgress,
				Items:      []programview.ItemDTO{},
				Contracts:  []programview.ContractDTO{},
				Warnings:   []string{},
			}, nil
		},
	})

	request := httptest.NewRequest(http.MethodGet, "http://localhost:4321/api/program?item=w2", nil)
	request.Host = "localhost:4321"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET API status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("API headers = %v", response.Header())
	}
	var snapshot programview.Snapshot
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.DetailItem != "w2" {
		t.Fatalf("detail item = %q", snapshot.DetailItem)
	}
	if snapshot.Progress != expectedProgress {
		t.Fatalf("progress = %+v, want %+v", snapshot.Progress, expectedProgress)
	}

	for _, test := range []struct {
		method string
		path   string
		host   string
		status int
	}{
		{method: http.MethodHead, path: "/", host: "127.0.0.1:4321", status: http.StatusOK},
		{method: http.MethodPost, path: "/api/program", host: "localhost:4321", status: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/missing", host: "localhost:4321", status: http.StatusNotFound},
		{method: http.MethodGet, path: "/", host: "evil.example:4321", status: http.StatusForbidden},
	} {
		request := httptest.NewRequest(test.method, "http://"+test.host+test.path, nil)
		request.Host = test.host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Errorf("%s %s host %s status = %d, want %d", test.method, test.path, test.host, response.Code, test.status)
		}
		if test.method == http.MethodHead && response.Body.Len() != 0 {
			t.Errorf("HEAD body = %q", response.Body.String())
		}
	}
}

func TestHandlerRejectsMalformedDetailItemWithoutBuildingSnapshot(t *testing.T) {
	var builds atomic.Int32
	handler := NewHandler(HandlerOptions{
		Slug: "relay-v1",
		Port: 4321,
		Builder: func(_, _ string) (programview.Snapshot, error) {
			builds.Add(1)
			return programview.Snapshot{}, nil
		},
	})

	for _, item := range []string{"w0", "w01", "W1", "../w1", "w" + strings.Repeat("9", 64)} {
		request := httptest.NewRequest(http.MethodGet, "http://localhost:4321/api/program?item="+item, nil)
		request.Host = "localhost:4321"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("item %q status = %d, want %d", item, response.Code, http.StatusBadRequest)
		}
	}
	if got := builds.Load(); got != 0 {
		t.Fatalf("snapshot builds = %d, want 0", got)
	}
}

func TestHandlerServesOneArtifactWithETagAndStatusMapping(t *testing.T) {
	fixture := newReferenceProgramFixture(t)
	errorPath := filepath.Join(fixture.projectsDir, fixture.program.Items[0].ProjectSlug, "context.md")
	if err := os.Remove(errorPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(errorPath, 0o755); err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(fixture.projectsDir, fixture.program.Items[0].ProjectSlug, "follow-ups.md")
	if err := os.Remove(missingPath); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(HandlerOptions{
		Slug: fixture.program.Slug,
		Port: 4321,
		Builder: func(slug, detail string) (programview.Snapshot, error) {
			return programview.Build(slug, programview.Options{DetailItem: detail})
		},
	})

	request := httptest.NewRequest(http.MethodGet,
		"http://localhost:4321/api/artifact?kind=task&item=w1&name=assignment.md", nil)
	request.Host = "localhost:4321"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("artifact status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Security-Policy") == "" ||
		response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("artifact headers = %v", response.Header())
	}
	etag := response.Header().Get("ETag")
	if etag == "" {
		t.Fatal("artifact ETag is empty")
	}
	var envelope programview.ArtifactResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.State != programview.ArtifactStateLoaded ||
		envelope.Artifact.Text == nil || len(*envelope.Artifact.Text) != 128*1024 {
		t.Fatalf("artifact response = %+v", envelope)
	}
	if response.Body.Len() > 192*1024 {
		t.Fatalf("artifact response bytes = %d, want <= %d", response.Body.Len(), 192*1024)
	}
	if strings.Contains(response.Body.String(), strings.Repeat("p", 1024)) {
		t.Fatal("artifact response included an unrequested file body")
	}

	request = httptest.NewRequest(http.MethodGet,
		"http://localhost:4321/api/artifact?kind=task&item=w1&name=assignment.md", nil)
	request.Host = "localhost:4321"
	request.Header.Set("If-None-Match", etag)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotModified || response.Body.Len() != 0 {
		t.Fatalf("conditional response = %d %q", response.Code, response.Body.String())
	}

	for _, test := range []struct {
		name   string
		path   string
		status int
		state  programview.ArtifactState
	}{
		{name: "invalid kind", path: "/api/artifact?kind=other", status: http.StatusBadRequest},
		{name: "invalid task name", path: "/api/artifact?kind=task&item=w1&name=../goal.md", status: http.StatusBadRequest},
		{name: "unknown task", path: "/api/artifact?kind=task&item=w999&name=task.md", status: http.StatusNotFound},
		{name: "unknown contract", path: "/api/artifact?kind=contract&ref=missing@v1", status: http.StatusNotFound},
		{name: "missing", path: "/api/artifact?kind=task&item=w1&name=follow-ups.md", status: http.StatusOK, state: programview.ArtifactStateMissing},
		{name: "read error", path: "/api/artifact?kind=task&item=w1&name=context.md", status: http.StatusInternalServerError, state: programview.ArtifactStateError},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://localhost:4321"+test.path, nil)
			request.Host = "localhost:4321"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			if test.state == "" {
				return
			}
			var got programview.ArtifactResponse
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.State != test.state {
				t.Fatalf("state = %q, want %q", got.State, test.state)
			}
		})
	}
}

func TestSnapshotCacheSingleFlightsConcurrentBuilds(t *testing.T) {
	var builds atomic.Int32
	release := make(chan struct{})
	cache := newSnapshotCache(time.Minute, time.Now, func(_, detail string) (programview.Snapshot, error) {
		builds.Add(1)
		<-release
		return programview.Snapshot{DetailItem: detail}, nil
	})

	const callers = 12
	var wait sync.WaitGroup
	wait.Add(callers)
	start := make(chan struct{})
	for range callers {
		go func() {
			defer wait.Done()
			<-start
			if _, err := cache.Get("relay-v1", "w1"); err != nil {
				t.Errorf("Get: %v", err)
			}
		}()
	}
	close(start)
	for builds.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wait.Wait()
	if got := builds.Load(); got != 1 {
		t.Fatalf("build count = %d, want 1", got)
	}
}

func TestSnapshotCacheEvictsOldestEntriesAtBound(t *testing.T) {
	builds := make(map[string]int)
	cache := newSnapshotCache(time.Minute, time.Now, func(_, detail string) (programview.Snapshot, error) {
		builds[detail]++
		return programview.Snapshot{DetailItem: detail}, nil
	})

	for id := 1; id <= 100; id++ {
		detail := fmt.Sprintf("w%d", id)
		if _, err := cache.Get("relay-v1", detail); err != nil {
			t.Fatal(err)
		}
	}
	if got := snapshotCacheEntryCount(cache); got != 64 {
		t.Fatalf("cache entries = %d, want 64", got)
	}
	if _, err := cache.Get("relay-v1", "w1"); err != nil {
		t.Fatal(err)
	}
	if builds["w1"] != 2 {
		t.Fatalf("oldest entry builds = %d, want 2", builds["w1"])
	}
}

func TestGitHubCacheTTLAndStaleFallback(t *testing.T) {
	now := time.Date(2026, 8, 25, 16, 0, 0, 0, time.UTC)
	var calls int
	fetcher := fetcherFunc(func(context.Context, string, string) (programview.PullRequestDTO, error) {
		calls++
		if calls > 1 {
			return programview.PullRequestDTO{}, errors.New("refresh failed")
		}
		return programview.PullRequestDTO{Number: 42, State: "open"}, nil
	})
	cache := newGitHubCache(fetcher, 12*time.Second, func() time.Time { return now })

	first, err := cache.Fetch(context.Background(), "/repo", "#42")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	if _, err := cache.Fetch(context.Background(), "/repo", "#42"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("fresh cache calls = %d", calls)
	}
	now = now.Add(3 * time.Second)
	stale, err := cache.Fetch(context.Background(), "/repo", "#42")
	if err == nil || !strings.Contains(err.Error(), "refresh failed") {
		t.Fatalf("stale refresh error = %v", err)
	}
	if calls != 2 || !stale.Stale || stale.StaleReason != "refresh failed" ||
		stale.FetchedAt != first.FetchedAt || stale.Number != first.Number {
		t.Fatalf("stale refresh = calls %d, first %+v, stale %+v", calls, first, stale)
	}

	now = time.Date(2026, 8, 25, 16, 0, 0, 0, time.UTC).Add(5*time.Minute + time.Second)
	expired, err := cache.Fetch(context.Background(), "/repo", "#42")
	if err == nil || !strings.Contains(err.Error(), "refresh failed") {
		t.Fatalf("expired stale refresh error = %v", err)
	}
	if calls != 3 || expired != (programview.PullRequestDTO{}) {
		t.Fatalf("expired stale refresh = calls %d, PR %+v", calls, expired)
	}
}

type fetcherFunc func(context.Context, string, string) (programview.PullRequestDTO, error)

func (f fetcherFunc) Fetch(ctx context.Context, repo, ref string) (programview.PullRequestDTO, error) {
	return f(ctx, repo, ref)
}

func snapshotCacheEntryCount(cache *snapshotCache) int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return len(cache.entries)
}
