// Package programui serves the embedded local Relay Program UI.
package programui

import (
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

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

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
	return &handler{slug: slug, port: port, cache: cache, feed: feed, artifactLoader: artifactLoader}
}

type handler struct {
	slug           string
	port           string
	cache          *snapshotCache
	feed           *snapshotFeed
	artifactLoader ArtifactLoader
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
		h.serveAsset(response, request, "assets/index.html", "text/html; charset=utf-8")
	case "/app.css":
		h.serveAsset(response, request, "assets/app.css", "text/css; charset=utf-8")
	case "/app.js":
		h.serveAsset(response, request, "assets/app.min.js", "text/javascript; charset=utf-8")
	case "/api/program":
		h.serveProgram(response, request)
	case "/api/artifact":
		h.serveArtifact(response, request)
	default:
		http.NotFound(response, request)
	}
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
	detailItem, ok := normalizeDetailItem(request.URL.Query().Get("item"))
	if !ok {
		http.Error(response, "invalid item: expected w followed by a positive integer", http.StatusBadRequest)
		return
	}
	var snapshot programview.Snapshot
	if detailItem == "" && h.feed != nil {
		snapshot = h.feed.Get()
	} else {
		var err error
		snapshot, err = h.cache.Get(h.slug, detailItem)
		if err != nil {
			http.Error(response, fmt.Sprintf("build program snapshot: %v", err), http.StatusInternalServerError)
			return
		}
	}
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	if err := json.NewEncoder(response).Encode(snapshot); err != nil {
		return
	}
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
	if _, err := response.Write(data); err != nil {
		return
	}
}

func setSecurityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
}
