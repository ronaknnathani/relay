package programui

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/programview"
)

func TestHandlerCachesRenderedIndex(t *testing.T) {
	handler := NewHandler(HandlerOptions{Slug: "relay-v1", Port: 4321})
	request := httptest.NewRequest(http.MethodGet, "http://localhost:4321/", nil)
	request.Host = "localhost:4321"
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET index status = %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Length"); got != fmt.Sprint(response.Body.Len()) {
		t.Fatalf("Content-Length = %q, want %d", got, response.Body.Len())
	}
	if bytes.Contains(response.Body.Bytes(), []byte(roadmapCoreToken)) ||
		bytes.Contains(response.Body.Bytes(), []byte(cssTemplateToken)) ||
		bytes.Contains(response.Body.Bytes(), []byte("<style></style>")) ||
		!bytes.Contains(response.Body.Bytes(), []byte("--canvas:")) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`href="app.js"`)) {
		t.Fatal("rendered index must contain the complete first-paint CSS and bootstrap with no template token")
	}
}

func TestPrepareIndexTemplateRejectsMissingRequiredTokens(t *testing.T) {
	valid := []byte(readAsset(t, "assets/index.html"))
	for _, test := range []struct {
		name  string
		index []byte
		want  string
	}{
		{name: "css", index: bytes.ReplaceAll(valid, []byte(cssTemplateToken), nil), want: cssTemplateToken},
		{name: "roadmap", index: bytes.ReplaceAll(valid, []byte(roadmapCoreToken), nil), want: roadmapCoreToken},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := prepareIndexTemplateData(test.index, []byte("roadmap"), []byte("styles"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("prepareIndexTemplateData() error = %v, want missing %s", err, test.want)
			}
		})
	}
}

func TestHandlerRendersMergedProgressBeforeHydration(t *testing.T) {
	seed := programview.Snapshot{
		Schema:   programview.SchemaVersion,
		Program:  programview.ProgramDTO{Title: "Progress"},
		Progress: programview.ProgressDTO{Total: 7, Merged: 3, Canceled: 2},
		Graph: programview.GraphDTO{
			Nodes:  []programview.GraphNodeDTO{{ID: "w1", Title: "First", Lane: "pending"}},
			Layers: [][]string{{"w1"}},
		},
		Items: []programview.ItemDTO{{
			ID: "w1", Title: "First", Status: "pending", Lane: "pending",
			Priority: "P1", Dependencies: []string{},
		}},
	}
	feed := newSnapshotFeed(seed, time.Minute, time.Now, func() (programview.Snapshot, error) {
		return seed, nil
	})
	handler := newHandler("relay-v1", "4321", newSnapshotCache(time.Minute, nil, nil), feed, nil)
	request := httptest.NewRequest(http.MethodGet, "http://localhost:4321/", nil)
	request.Host = "localhost:4321"
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET index status = %d: %s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(
		`<p id="progress-counts" class="signal__note">3 of 7 merged`,
	)) {
		t.Fatal("rendered index does not preserve merged progress")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(`"schema":"relay.program.roadmap.bootstrap.v1"`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"stages":[[{"i":"w1","t":"First","l":"pending","p":"P1"`)) ||
		bytes.Contains(response.Body.Bytes(), []byte(`"items"`)) {
		t.Fatal("rendered index must embed the compact roadmap graph without task detail records")
	}
}

func TestNewRoadmapBootstrapPreservesCompleteGraphMetadata(t *testing.T) {
	bootstrap := newRoadmapBootstrap(roadmapGraph{
		Nodes: []roadmapNode{{
			ID: "w1", Title: "First", Lane: "dispatched", Layer: 0,
			Priority: "P1", Dependencies: []string{"w0"}, PRNumber: 42,
			Ready: true, Orphaned: true,
		}},
		Edges:  []programview.GraphEdgeDTO{{From: "w0", To: "w1"}},
		Layers: [][]string{{"w1"}},
		Cyclic: true,
	})
	if bootstrap.Schema != "relay.program.roadmap.bootstrap.v1" ||
		len(bootstrap.Stages) != 1 || len(bootstrap.Stages[0]) != 1 {
		t.Fatalf("roadmap bootstrap = %+v", bootstrap)
	}
	node := bootstrap.Stages[0][0]
	if node.ID != "w1" || node.Title != "First" || node.Lane != "dispatched" ||
		node.Priority != "P1" || len(node.Dependencies) != 1 ||
		node.Dependencies[0] != "w0" || node.PRNumber != 42 ||
		!node.Ready || !node.Orphaned ||
		len(bootstrap.Edges) != 1 || bootstrap.Edges[0] != [2]string{"w0", "w1"} ||
		!bootstrap.Cyclic {
		t.Fatalf("roadmap bootstrap = %+v", bootstrap)
	}
}

func TestInitialRoadmapConnectorsSerializesSingleTaskStages(t *testing.T) {
	graph := roadmapGraph{
		Nodes:  []roadmapNode{{ID: "w1"}, {ID: "w2"}},
		Edges:  []programview.GraphEdgeDTO{{From: "w1", To: "w2"}},
		Layers: [][]string{{"w1"}, {"w2"}},
	}
	connectors := initialRoadmapConnectors(graph)
	if connectors == nil {
		t.Fatal("single-task stages must have initial connectors")
	}
	if connectors.Width != 1000 || connectors.Height != 290 ||
		connectors.NormalPath != "M 500 133 V 185" || connectors.NormalCount != 1 ||
		connectors.BackPath != "" || connectors.BackCount != 0 {
		t.Fatalf("initial connectors = %+v", connectors)
	}
	graph.Layers = [][]string{{"w1", "w2"}}
	if connectors := initialRoadmapConnectors(graph); connectors != nil {
		t.Fatalf("parallel-stage connectors = %+v, want browser layout", connectors)
	}
}

func TestHandlerLoadsCurrentRoadmapOutsideDocument(t *testing.T) {
	now := time.Date(2026, 9, 15, 16, 0, 0, 0, time.UTC)
	seed := programview.Snapshot{
		Schema:  programview.SchemaVersion,
		Program: programview.ProgramDTO{Title: "Seed roadmap"},
		Items:   []programview.ItemDTO{},
	}
	updated := seed
	updated.Program.Title = "Updated roadmap"
	feed := newSnapshotFeed(seed, time.Minute, func() time.Time { return now }, func() (programview.Snapshot, error) {
		return updated, nil
	})
	handler := newHandler("relay-v1", "4321", newSnapshotCache(time.Minute, nil, nil), feed, nil)

	feed.Refresh()
	eventually(t, time.Second, func() bool {
		return bytes.Contains(feed.roadmapResponse(), []byte("Updated roadmap"))
	})

	request := httptest.NewRequest(http.MethodGet, "http://localhost:4321/", nil)
	request.Host = "localhost:4321"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET index status = %d: %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("Seed roadmap")) ||
		!bytes.Contains(response.Body.Bytes(), []byte("Updated roadmap")) {
		t.Fatal("rendered index did not use the feed's current roadmap projection")
	}

	request = httptest.NewRequest(http.MethodGet, "http://localhost:4321/api/program?view=roadmap", nil)
	request.Host = "localhost:4321"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!bytes.Contains(response.Body.Bytes(), []byte("Updated roadmap")) {
		t.Fatal("roadmap endpoint did not return the feed's current projection")
	}
}

func TestHandlerRoadmapViewKeepsOnlyInitialUIData(t *testing.T) {
	handler := NewHandler(HandlerOptions{
		Slug: "relay-v1",
		Port: 4321,
		Builder: func(_, _ string) (programview.Snapshot, error) {
			return programview.Snapshot{
				Schema:  "relay.program.v1",
				Program: programview.ProgramDTO{Title: "Program"},
				Graph: programview.GraphDTO{Nodes: []programview.GraphNodeDTO{{
					ID: "w1", Title: "Task", Lane: "pending",
				}}},
				Items: []programview.ItemDTO{{
					ID: "w1", Title: "Task", Priority: "P0", Status: "pending",
					Dependencies: []string{"w0"}, Notes: []string{"deferred"},
					Worker:  &programview.WorkerDTO{Status: "working"},
					Mailbox: programview.MailboxDTO{Available: true, Inbox: 2, Outbox: 1},
				}},
				OpenDecisions:    []programview.DecisionDTO{{ID: "d1"}},
				Contracts:        []programview.ContractDTO{{Ref: "contract"}},
				ProgramArtifacts: []programview.ArtifactDTO{{Name: "goal.md"}},
			}, nil
		},
	})
	request := httptest.NewRequest(http.MethodGet, "http://localhost:4321/api/program?view=roadmap", nil)
	request.Host = "localhost:4321"
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET roadmap status = %d: %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode roadmap response: %v", err)
	}
	if payload["schema"] != "relay.program.roadmap.v1" {
		t.Fatalf("schema = %v", payload["schema"])
	}
	if _, ok := payload["contracts"]; ok {
		t.Fatal("roadmap response includes deferred contracts")
	}
	overview := payload["overview"].(map[string]any)
	if overview["open_decisions"] != float64(1) ||
		overview["workers"] != float64(1) ||
		overview["active_workers"] != float64(1) ||
		overview["unread_messages"] != float64(3) {
		t.Fatalf("overview = %#v", overview)
	}
	if _, ok := payload["items"]; ok {
		t.Fatal("roadmap response duplicates graph nodes as task details")
	}
	node := payload["graph"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
	if node["priority"] != "P0" || node["dependency_count"] != float64(1) {
		t.Fatalf("roadmap node = %#v", node)
	}
	dependencies := node["dependencies"].([]any)
	if len(dependencies) != 1 || dependencies[0] != "w0" {
		t.Fatalf("roadmap node dependencies = %#v", dependencies)
	}
}

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
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("artifact status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Security-Policy") == "" ||
		response.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		response.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("artifact headers = %v", response.Header())
	}
	etag := response.Header().Get("ETag")
	if etag == "" {
		t.Fatal("artifact ETag is empty")
	}
	if response.Body.Len() > 192*1024 {
		t.Fatalf("artifact response bytes = %d, want <= %d", response.Body.Len(), 192*1024)
	}
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	var envelope programview.ArtifactResponse
	if err := json.Unmarshal(decoded, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.State != programview.ArtifactStateLoaded ||
		envelope.Artifact.Text == nil || len(*envelope.Artifact.Text) != 128*1024 {
		t.Fatalf("artifact response = %+v", envelope)
	}
	if strings.Contains(string(decoded), strings.Repeat("p", 1024)) {
		t.Fatal("artifact response included an unrequested file body")
	}

	request = httptest.NewRequest(http.MethodGet,
		"http://localhost:4321/api/artifact?kind=task&item=w1&name=assignment.md", nil)
	request.Host = "localhost:4321"
	request.Header.Set("Accept-Encoding", "gzip")
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
		if calls == 2 || calls == 4 {
			return programview.PullRequestDTO{}, errors.New("refresh failed")
		}
		return programview.PullRequestDTO{Number: 41 + calls, State: "open"}, nil
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
	recovered, err := cache.Fetch(context.Background(), "/repo", "#42")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || recovered.Stale || recovered.Number != 44 ||
		recovered.FetchedAt == first.FetchedAt {
		t.Fatalf("recovered refresh = calls %d, first %+v, recovered %+v", calls, first, recovered)
	}

	now = now.Add(5*time.Minute + time.Second)
	expired, err := cache.Fetch(context.Background(), "/repo", "#42")
	if err == nil || !strings.Contains(err.Error(), "refresh failed") {
		t.Fatalf("expired stale refresh error = %v", err)
	}
	if calls != 4 || expired != (programview.PullRequestDTO{}) {
		t.Fatalf("expired stale refresh = calls %d, PR %+v", calls, expired)
	}
}

func TestGitHubCacheKeepsPullRequestRepositoryIdentity(t *testing.T) {
	var calls int
	cache := newGitHubCache(
		fetcherFunc(func(context.Context, string, string) (programview.PullRequestDTO, error) {
			calls++
			return programview.PullRequestDTO{Number: calls, State: "open"}, nil
		}),
		time.Minute,
		time.Now,
	)
	local, err := cache.Fetch(context.Background(), "/repo", "#42")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := cache.Fetch(
		context.Background(),
		"/repo",
		"https://github.example/other/repo/pull/42",
	)
	if err != nil {
		t.Fatal(err)
	}
	foreignFiles, err := cache.Fetch(
		context.Background(),
		"/repo",
		"https://github.example/other/repo/pull/42/files",
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || local.Number != 1 || foreign.Number != 2 || foreignFiles.Number != 2 {
		t.Fatalf(
			"fetch calls = %d, local = %d, foreign = %d, foreign files = %d",
			calls, local.Number, foreign.Number, foreignFiles.Number,
		)
	}
}

func TestGitHubCacheIsolatesPullRequestURLsByLocalRepository(t *testing.T) {
	var calls int
	cache := newGitHubCache(
		fetcherFunc(func(_ context.Context, repo, _ string) (programview.PullRequestDTO, error) {
			calls++
			if repo == "/checkout/two" {
				return programview.PullRequestDTO{}, errors.New("pull request repository does not match checkout")
			}
			return programview.PullRequestDTO{Number: 42, State: "merged"}, nil
		}),
		time.Minute,
		time.Now,
	)
	ref := "https://github.example/acme/repo/pull/42"
	if _, err := cache.Fetch(context.Background(), "/checkout/one", ref); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Fetch(context.Background(), "/checkout/two", ref); err == nil ||
		!strings.Contains(err.Error(), "does not match checkout") {
		t.Fatalf("second checkout error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("fetch calls = %d, want 2", calls)
	}
}

func TestAgentCacheTTLStaleFallbackAndRecovery(t *testing.T) {
	now := time.Date(2026, 8, 25, 16, 0, 0, 0, time.UTC)
	var calls int
	lister := agentListerFunc(func() ([]herdr.Agent, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("refresh failed")
		}
		return []herdr.Agent{{PaneID: fmt.Sprintf("pane-%d", calls)}}, nil
	})
	cache := newAgentCache(lister, 12*time.Second, func() time.Time { return now })

	firstResult, err := cache.AgentsWithProvenance()
	if err != nil {
		t.Fatal(err)
	}
	first := firstResult.Agents
	if firstResult.Stale || firstResult.FetchedAt == "" {
		t.Fatalf("fresh agent provenance = %+v", firstResult)
	}
	now = now.Add(10 * time.Second)
	if _, err := cache.Agents(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("fresh cache calls = %d", calls)
	}
	now = now.Add(3 * time.Second)
	staleResult, err := cache.AgentsWithProvenance()
	if err == nil || !strings.Contains(err.Error(), "refresh failed") {
		t.Fatalf("stale refresh error = %v", err)
	}
	stale := staleResult.Agents
	if calls != 2 || len(stale) != 1 || stale[0].PaneID != first[0].PaneID {
		t.Fatalf("stale refresh = calls %d, first %+v, stale %+v", calls, first, stale)
	}
	if !staleResult.Stale || staleResult.FetchedAt != firstResult.FetchedAt ||
		staleResult.StaleReason != "refresh failed" {
		t.Fatalf("stale agent provenance = %+v, want fetched_at %q", staleResult, firstResult.FetchedAt)
	}
	recovered, err := cache.Agents()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(recovered) != 1 || recovered[0].PaneID != "pane-3" {
		t.Fatalf("recovered refresh = calls %d, agents %+v", calls, recovered)
	}
}

type fetcherFunc func(context.Context, string, string) (programview.PullRequestDTO, error)

func (f fetcherFunc) Fetch(ctx context.Context, repo, ref string) (programview.PullRequestDTO, error) {
	return f(ctx, repo, ref)
}

type agentListerFunc func() ([]herdr.Agent, error)

func (f agentListerFunc) Agents() ([]herdr.Agent, error) {
	return f()
}

func snapshotCacheEntryCount(cache *snapshotCache) int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return len(cache.entries)
}
