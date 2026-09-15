// Package programui serves the embedded local Relay Program UI.
package programui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/programview"
)

const (
	contentSecurityPolicy  = "default-src 'self'; script-src 'self' 'sha256-UXIL+j6UmJdVusQ2iRt/3tKDJxuh42y6D1HM1W2MC54=' 'sha256-wzXipU/5dsm4694RKwiOug2N2fEgyyYmJNXosKK7HBk='; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
	bootstrapTemplateToken = "__RELAY_BOOTSTRAP__"
	roadmapTemplateToken   = "__RELAY_INITIAL_ROADMAP__"
)

//go:embed assets/*
var embeddedAssets embed.FS

// HandlerOptions configures the read-only Program UI handler.
type HandlerOptions struct {
	Slug           string
	Port           int
	Builder        Builder
	ArtifactLoader ArtifactLoader
	Now            func() time.Time
}

// ArtifactLoader reads one allowlisted program artifact.
type ArtifactLoader func(string, programview.ArtifactSelector) (programview.ArtifactResponse, error)

// NewHandler creates the Program UI HTTP handler.
func NewHandler(options HandlerOptions) http.Handler {
	cache := newSnapshotCache(2*time.Second, options.Now, options.Builder)
	loader := options.ArtifactLoader
	if loader == nil {
		loader = defaultArtifactLoader
	}
	return newHandler(options.Slug, strconv.Itoa(options.Port), cache, nil, loader)
}

func defaultArtifactLoader(slug string, selector programview.ArtifactSelector) (programview.ArtifactResponse, error) {
	return programview.LoadArtifact(slug, selector, 0)
}

func newHandler(
	slug, port string,
	cache *snapshotCache,
	feed *snapshotFeed,
	artifactLoader ArtifactLoader,
) *handler {
	roadmap := []byte("null")
	if feed != nil {
		roadmap = feed.roadmapResponse()
	}
	index, indexErr := prepareIndexTemplate()
	return &handler{
		slug: slug, port: port, cache: cache, feed: feed, artifactLoader: artifactLoader,
		indexTemplate: index, initialRoadmap: roadmap, indexErr: indexErr,
	}
}

type handler struct {
	slug           string
	port           string
	cache          *snapshotCache
	feed           *snapshotFeed
	artifactLoader ArtifactLoader
	indexTemplate  []byte
	initialRoadmap []byte
	indexErr       error
}

type roadmapSnapshot struct {
	Schema       string                      `json:"schema"`
	GeneratedAt  string                      `json:"generated_at"`
	Refresh      programview.RefreshDTO      `json:"refresh"`
	Program      programview.ProgramDTO      `json:"program"`
	Patrol       programview.PatrolDTO       `json:"patrol"`
	Progress     programview.ProgressDTO     `json:"progress"`
	Plan         programview.PlanDTO         `json:"plan"`
	Graph        roadmapGraph                `json:"graph"`
	Overview     roadmapOverview             `json:"overview"`
	Warnings     []string                    `json:"warnings"`
	SourceHealth programview.SourceHealthDTO `json:"source_health"`
}

type roadmapGraph struct {
	Nodes  []roadmapNode              `json:"nodes"`
	Edges  []programview.GraphEdgeDTO `json:"edges"`
	Layers [][]string                 `json:"layers,omitempty"`
	Cyclic bool                       `json:"cyclic"`
}

type roadmapNode struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Lane            string   `json:"lane"`
	Layer           int      `json:"layer"`
	Priority        string   `json:"priority"`
	DependencyCount int      `json:"dependency_count"`
	Dependencies    []string `json:"dependencies"`
	PRNumber        int      `json:"pr_number,omitempty"`
	Ready           bool     `json:"ready"`
	Orphaned        bool     `json:"orphaned"`
}

type roadmapOverview struct {
	OpenDecisions  int `json:"open_decisions"`
	Workers        int `json:"workers"`
	ActiveWorkers  int `json:"active_workers"`
	UnreadMessages int `json:"unread_messages"`
}

func (h *handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(response.Header())
	if !h.allowedHost(request.Host) {
		http.Error(response, "forbidden host", http.StatusForbidden)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch request.URL.Path {
	case "/":
		h.serveIndex(response, request)
	case "/app.css":
		h.serveAsset(response, request, "assets/app.min.css", "text/css; charset=utf-8")
	case "/app-deferred.css":
		h.serveAsset(response, request, "assets/app-deferred.min.css", "text/css; charset=utf-8")
	case "/app.js":
		h.serveAsset(response, request, "assets/app.min.js", "text/javascript; charset=utf-8")
	case "/app-deferred.js":
		h.serveAsset(response, request, "assets/app-deferred.min.js", "text/javascript; charset=utf-8")
	case "/api/program":
		h.serveProgram(response, request)
	case "/api/artifact":
		h.serveArtifact(response, request)
	default:
		http.NotFound(response, request)
	}
}

func (h *handler) serveIndex(response http.ResponseWriter, request *http.Request) {
	if h.indexErr != nil {
		http.Error(response, h.indexErr.Error(), http.StatusInternalServerError)
		return
	}
	roadmap := h.initialRoadmap
	if h.feed != nil {
		roadmap = h.feed.roadmapResponse()
	}
	index := bytes.Replace(h.indexTemplate, []byte(roadmapTemplateToken), roadmap, 1)
	response.Header().Set("Content-Length", strconv.Itoa(len(index)))
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	if _, err := response.Write(index); err != nil {
		return
	}
}

func prepareIndexTemplate() ([]byte, error) {
	index, err := fs.ReadFile(embeddedAssets, "assets/index.min.html")
	if err != nil {
		return nil, fmt.Errorf("read embedded asset assets/index.min.html: %w", err)
	}
	bootstrap, err := fs.ReadFile(embeddedAssets, "assets/bootstrap.min.js")
	if err != nil {
		return nil, fmt.Errorf("read embedded asset assets/bootstrap.min.js: %w", err)
	}
	index = bytes.Replace(index, []byte(bootstrapTemplateToken), bootstrap, 1)
	return index, nil
}

func (h *handler) allowedHost(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || port != h.port {
		return false
	}
	return host == "127.0.0.1" || strings.EqualFold(host, "localhost")
}

func (h *handler) serveAsset(response http.ResponseWriter, request *http.Request, name, contentType string) {
	data, err := fs.ReadFile(embeddedAssets, name)
	if err != nil {
		http.Error(response, fmt.Sprintf("read embedded asset %s", name), http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", contentType)
	response.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	if _, err := response.Write(data); err != nil {
		return
	}
}

func (h *handler) serveProgram(response http.ResponseWriter, request *http.Request) {
	view := request.URL.Query().Get("view")
	if view != "" && view != "roadmap" {
		http.Error(response, "invalid view: expected roadmap", http.StatusBadRequest)
		return
	}
	detailItem, ok := normalizeDetailItem(request.URL.Query().Get("item"))
	if !ok {
		http.Error(response, "invalid item: expected w followed by a positive integer", http.StatusBadRequest)
		return
	}
	if view != "" && detailItem != "" {
		http.Error(response, "item and view cannot be combined", http.StatusBadRequest)
		return
	}
	var snapshot programview.Snapshot
	var encoded []byte
	var compressed []byte
	switch {
	case view == "roadmap" && h.feed != nil:
		encoded = h.feed.roadmapResponse()
	case detailItem == "" && h.feed != nil:
		snapshot, encoded, compressed = h.feed.response()
	default:
		var err error
		snapshot, err = h.cache.Get(h.slug, detailItem)
		if err != nil {
			http.Error(response, fmt.Sprintf("build program snapshot: %v", err), http.StatusInternalServerError)
			return
		}
	}
	if view == "roadmap" && encoded == nil {
		var err error
		encoded, err = json.Marshal(newRoadmapSnapshot(snapshot))
		if err != nil {
			http.Error(response, fmt.Sprintf("encode roadmap snapshot: %v", err), http.StatusInternalServerError)
			return
		}
		compressed = nil
	}
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	useGzip := compressed != nil && strings.Contains(request.Header.Get("Accept-Encoding"), "gzip")
	if useGzip {
		response.Header().Set("Content-Encoding", "gzip")
		response.Header().Set("Vary", "Accept-Encoding")
	}

	response.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	if useGzip {
		if _, err := response.Write(compressed); err != nil {
			return
		}
		return
	}
	if encoded != nil {
		if _, err := response.Write(encoded); err != nil {
			return
		}
		return
	}
	if err := json.NewEncoder(response).Encode(snapshot); err != nil {
		return
	}
}

func newRoadmapSnapshot(snapshot programview.Snapshot) roadmapSnapshot {
	result := roadmapSnapshot{
		Schema: "relay.program.roadmap.v1", GeneratedAt: snapshot.GeneratedAt,
		Refresh: snapshot.Refresh, Program: snapshot.Program, Patrol: snapshot.Patrol,
		Progress: snapshot.Progress, Plan: snapshot.Plan,
		Graph: roadmapGraph{
			Nodes:  make([]roadmapNode, 0, len(snapshot.Graph.Nodes)),
			Edges:  snapshot.Graph.Edges,
			Cyclic: snapshot.Graph.Cyclic,
		},
		Overview: roadmapOverview{OpenDecisions: len(snapshot.OpenDecisions)},
		Warnings: snapshot.Warnings, SourceHealth: snapshot.SourceHealth,
	}
	items := make(map[string]programview.ItemDTO, len(snapshot.Items))
	for _, item := range snapshot.Items {
		items[item.ID] = item
		if item.Worker != nil {
			result.Overview.Workers++
			if item.Worker.Status == "working" {
				result.Overview.ActiveWorkers++
			}
		}
		if item.Mailbox.Available {
			result.Overview.UnreadMessages += item.Mailbox.Inbox + item.Mailbox.Outbox
		}
	}
	for _, node := range snapshot.Graph.Nodes {
		item := items[node.ID]
		prNumber := 0
		if item.LivePR != nil {
			prNumber = item.LivePR.Number
		} else if item.RecordedPR != nil {
			prNumber = item.RecordedPR.Number
		}
		result.Graph.Nodes = append(result.Graph.Nodes, roadmapNode{
			ID: node.ID, Title: node.Title, Lane: node.Lane, Layer: node.Layer,
			Priority: item.Priority, DependencyCount: len(item.Dependencies),
			Dependencies: append([]string(nil), item.Dependencies...),
			PRNumber:     prNumber, Ready: item.Ready, Orphaned: item.Orphaned,
		})
	}
	return result
}

func (h *handler) serveArtifact(response http.ResponseWriter, request *http.Request) {
	selector, err := artifactSelector(request)
	if err != nil {
		h.writeArtifactError(response, request, http.StatusBadRequest, err)
		return
	}
	if h.artifactLoader == nil {
		h.writeArtifactError(response, request, http.StatusInternalServerError,
			errors.New("artifact loader is not configured"))
		return
	}
	envelope, err := h.artifactLoader(h.slug, selector)
	if err != nil {
		switch {
		case errors.Is(err, programview.ErrInvalidArtifactSelector):
			h.writeArtifactError(response, request, http.StatusBadRequest, err)
		case errors.Is(err, programview.ErrArtifactSelectorNotFound):
			h.writeArtifactError(response, request, http.StatusNotFound, err)
		default:
			if envelope.State != programview.ArtifactStateError {
				envelope.State = programview.ArtifactStateError
				envelope.Error = err.Error()
			}
			h.writeArtifactJSON(response, request, http.StatusInternalServerError, envelope)
		}
		return
	}
	h.writeArtifactJSON(response, request, http.StatusOK, envelope)
}

func artifactSelector(request *http.Request) (programview.ArtifactSelector, error) {
	query := request.URL.Query()
	kind := programview.ArtifactKind(query.Get("kind"))
	item := query.Get("item")
	name := query.Get("name")
	ref := query.Get("ref")
	switch kind {
	case programview.ArtifactKindTask:
		item, ok := normalizeDetailItem(item)
		if !ok || item == "" || name == "" || ref != "" {
			return programview.ArtifactSelector{}, fmt.Errorf(
				"%w: task requests require item and name only", programview.ErrInvalidArtifactSelector,
			)
		}
		return programview.ArtifactSelector{Kind: kind, Item: item, Name: name}, nil
	case programview.ArtifactKindContract:
		if ref == "" || item != "" || name != "" {
			return programview.ArtifactSelector{}, fmt.Errorf(
				"%w: contract requests require ref only", programview.ErrInvalidArtifactSelector,
			)
		}
		return programview.ArtifactSelector{Kind: kind, Ref: ref}, nil
	default:
		return programview.ArtifactSelector{}, fmt.Errorf(
			"%w: kind must be task or contract", programview.ErrInvalidArtifactSelector,
		)
	}
}

func (h *handler) writeArtifactError(
	response http.ResponseWriter,
	request *http.Request,
	status int,
	err error,
) {
	h.writeArtifactJSON(response, request, status, programview.ArtifactResponse{
		State: programview.ArtifactStateError, Error: err.Error(),
	})
}

func (h *handler) writeArtifactJSON(
	response http.ResponseWriter,
	request *http.Request,
	status int,
	envelope programview.ArtifactResponse,
) {
	data, err := json.Marshal(envelope)
	if err != nil {
		http.Error(response, fmt.Sprintf("encode artifact response: %v", err), http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	payload := data
	if strings.Contains(request.Header.Get("Accept-Encoding"), "gzip") {
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(data); err != nil {
			http.Error(response, fmt.Sprintf("compress artifact response: %v", err), http.StatusInternalServerError)
			return
		}
		if err := writer.Close(); err != nil {
			http.Error(response, fmt.Sprintf("finish artifact response compression: %v", err), http.StatusInternalServerError)
			return
		}
		payload = compressed.Bytes()
		response.Header().Set("Content-Encoding", "gzip")
		response.Header().Set("Vary", "Accept-Encoding")
	}
	if status == http.StatusOK {
		etag := fmt.Sprintf(`"%x"`, sha256.Sum256(data))
		response.Header().Set("ETag", etag)
		if request.Header.Get("If-None-Match") == etag {
			response.WriteHeader(http.StatusNotModified)
			return
		}
	}
	response.WriteHeader(status)
	if request.Method == http.MethodHead {
		return
	}
	if _, err := response.Write(payload); err != nil {
		return
	}
}

func setSecurityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
}
