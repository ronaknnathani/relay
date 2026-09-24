package programui

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

type detailHandlerFactory func(string) (http.Handler, error)

type unifiedRouter struct {
	port     string
	overview *overviewFeed
	factory  detailHandlerFactory

	mu       sync.Mutex
	handlers map[string]http.Handler
}

func newUnifiedRouter(
	port string,
	overview *overviewFeed,
	factory detailHandlerFactory,
) http.Handler {
	return &unifiedRouter{
		port: port, overview: overview, factory: factory,
		handlers: make(map[string]http.Handler),
	}
}

func (r *unifiedRouter) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(response.Header())
	if !allowedLoopbackHost(request.Host, r.port) {
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
		r.serveAsset(response, request, "assets/overview.min.html", "text/html; charset=utf-8")
	case "/overview.css":
		r.serveAsset(response, request, "assets/overview.min.css", "text/css; charset=utf-8")
	case "/overview.js":
		r.serveAsset(response, request, "assets/overview.min.js", "text/javascript; charset=utf-8")
	case "/api/overview":
		r.serveOverview(response, request)
	default:
		r.serveDetail(response, request)
	}
}

func allowedLoopbackHost(hostport, port string) bool {
	host, requestedPort, err := net.SplitHostPort(hostport)
	if err != nil || requestedPort != port {
		return false
	}
	return host == "127.0.0.1" || strings.EqualFold(host, "localhost")
}

func (r *unifiedRouter) serveAsset(
	response http.ResponseWriter,
	request *http.Request,
	name, contentType string,
) {
	data, err := fs.ReadFile(embeddedAssets, name)
	if err != nil {
		http.Error(response, fmt.Sprintf("read embedded asset %s", name), http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Length", strconv.Itoa(len(data)))
	response.Header().Set("Content-Type", contentType)
	response.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	if _, err := response.Write(data); err != nil {
		return
	}
}

func (r *unifiedRouter) serveOverview(response http.ResponseWriter, request *http.Request) {
	_, encoded := r.overview.response()
	if encoded == nil {
		http.Error(response, "encode program overview", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Length", strconv.Itoa(len(encoded)))
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	if _, err := response.Write(encoded); err != nil {
		return
	}
}

func (r *unifiedRouter) serveDetail(response http.ResponseWriter, request *http.Request) {
	const prefix = "/programs/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		http.NotFound(response, request)
		return
	}
	remainder := strings.TrimPrefix(request.URL.Path, prefix)
	slash := strings.IndexByte(remainder, '/')
	encodedSlug := remainder
	detailPath := ""
	if slash >= 0 {
		encodedSlug = remainder[:slash]
		detailPath = remainder[slash:]
	}
	slug, err := url.PathUnescape(encodedSlug)
	if err != nil || slug == "" || strings.Contains(slug, "/") || !r.active(slug) {
		http.NotFound(response, request)
		return
	}
	if slash < 0 {
		http.Redirect(response, request, prefix+url.PathEscape(slug)+"/", http.StatusPermanentRedirect)
		return
	}
	handler, err := r.detailHandler(slug)
	if err != nil {
		http.Error(response, fmt.Sprintf("start program detail %q: %v", slug, err), http.StatusInternalServerError)
		return
	}
	cloned := request.Clone(request.Context())
	cloned.URL = cloneURL(request.URL)
	cloned.URL.Path = detailPath
	cloned.URL.RawPath = ""
	handler.ServeHTTP(response, cloned)
}

func (r *unifiedRouter) active(slug string) bool {
	for _, current := range r.overview.Get().Programs {
		if current.Slug == slug {
			return true
		}
	}
	return false
}

func (r *unifiedRouter) detailHandler(slug string) (http.Handler, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if handler := r.handlers[slug]; handler != nil {
		return handler, nil
	}
	handler, err := r.factory(slug)
	if err != nil {
		return nil, err
	}
	r.handlers[slug] = handler
	return handler, nil
}

func cloneURL(source *url.URL) *url.URL {
	cloned := *source
	return &cloned
}
