// Package programui serves the embedded local Relay Program UI.
package programui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

//go:embed assets/*
var embeddedAssets embed.FS

// HandlerOptions configures the read-only Program UI handler.
type HandlerOptions struct {
	Slug    string
	Port    int
	Builder Builder
	Now     func() time.Time
}

// NewHandler creates the Program UI HTTP handler.
func NewHandler(options HandlerOptions) http.Handler {
	cache := newSnapshotCache(2*time.Second, options.Now, options.Builder)
	return newHandler(options.Slug, strconv.Itoa(options.Port), cache)
}

func newHandler(slug, port string, cache *snapshotCache) *handler {
	return &handler{slug: slug, port: port, cache: cache}
}

type handler struct {
	slug  string
	port  string
	cache *snapshotCache
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
		h.serveAsset(response, request, "assets/app.js", "text/javascript; charset=utf-8")
	case "/api/program":
		h.serveProgram(response, request)
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
	snapshot, err := h.cache.Get(h.slug, detailItem)
	if err != nil {
		http.Error(response, fmt.Sprintf("build program snapshot: %v", err), http.StatusInternalServerError)
		return
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

func setSecurityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
}
