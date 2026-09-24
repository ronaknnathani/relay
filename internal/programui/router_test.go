package programui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/programview"
)

func TestUnifiedRouterServesOverviewAndPrefixedDetail(t *testing.T) {
	feed := newOverviewFeed(
		programview.OverviewSnapshot{
			Schema:      programview.OverviewSchemaVersion,
			Programs:    []programview.ProgramOverviewDTO{{Slug: "alpha"}},
			Work:        []programview.OverviewWorkItemDTO{},
			Diagnostics: []programview.OverviewDiagnosticDTO{},
		},
		time.Hour,
		time.Now,
		nil,
	)
	var created atomic.Int32
	router := newUnifiedRouter("4321", feed, func(slug string) (http.Handler, error) {
		created.Add(1)
		if slug != "alpha" {
			return nil, errors.New("unexpected slug")
		}
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			response.Header().Set("Content-Type", "text/plain")
			_, _ = response.Write([]byte(request.URL.Path))
		}), nil
	})

	for _, path := range []string{"/", "/overview.css", "/overview.js", "/api/overview"} {
		response := serveUnifiedRequest(router, http.MethodGet, path, "localhost:4321")
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d: %s", path, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" ||
			response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("GET %s security headers = %v", path, response.Header())
		}
	}

	redirect := serveUnifiedRequest(router, http.MethodGet, "/programs/alpha", "localhost:4321")
	if redirect.Code != http.StatusPermanentRedirect ||
		redirect.Header().Get("Location") != "/programs/alpha/" {
		t.Fatalf("redirect = %d %q", redirect.Code, redirect.Header().Get("Location"))
	}
	detail := serveUnifiedRequest(router, http.MethodGet, "/programs/alpha/api/program", "localhost:4321")
	if detail.Code != http.StatusOK || detail.Body.String() != "/api/program" {
		t.Fatalf("detail = %d %q", detail.Code, detail.Body.String())
	}
	second := serveUnifiedRequest(router, http.MethodGet, "/programs/alpha/app.js", "localhost:4321")
	if second.Code != http.StatusOK || second.Body.String() != "/app.js" || created.Load() != 1 {
		t.Fatalf("second detail = %d %q, created %d", second.Code, second.Body.String(), created.Load())
	}
	if response := serveUnifiedRequest(router, http.MethodGet, "/programs/missing/", "localhost:4321"); response.Code != http.StatusNotFound {
		t.Fatalf("unknown detail status = %d", response.Code)
	}
}

func TestUnifiedRouterRejectsInvalidHostMethodAndPaths(t *testing.T) {
	feed := newOverviewFeed(
		programview.OverviewSnapshot{
			Schema:      programview.OverviewSchemaVersion,
			Programs:    []programview.ProgramOverviewDTO{},
			Work:        []programview.OverviewWorkItemDTO{},
			Diagnostics: []programview.OverviewDiagnosticDTO{},
		},
		time.Hour,
		time.Now,
		nil,
	)
	router := newUnifiedRouter("4321", feed, func(string) (http.Handler, error) {
		t.Fatal("detail factory called")
		return nil, nil
	})
	if response := serveUnifiedRequest(router, http.MethodGet, "/", "example.com:4321"); response.Code != http.StatusForbidden {
		t.Fatalf("invalid host status = %d", response.Code)
	}
	response := serveUnifiedRequest(router, http.MethodPost, "/", "localhost:4321")
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST status = %d, Allow = %q", response.Code, response.Header().Get("Allow"))
	}
	if response := serveUnifiedRequest(router, http.MethodGet, "/missing", "localhost:4321"); response.Code != http.StatusNotFound {
		t.Fatalf("missing path status = %d", response.Code)
	}
	head := serveUnifiedRequest(router, http.MethodHead, "/api/overview", "localhost:4321")
	if head.Code != http.StatusOK || strings.TrimSpace(head.Body.String()) != "" {
		t.Fatalf("HEAD response = %d %q", head.Code, head.Body.String())
	}
}

func serveUnifiedRequest(handler http.Handler, method, path, host string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://"+host+path, nil)
	request.Host = host
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
