package programui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/ui"
)

const (
	snapshotTTL = 2 * time.Second
	githubTTL   = 12 * time.Second
)

// Options configures the foreground local Program UI server.
type Options struct {
	Slug        string
	Port        int
	Open        bool
	Out         io.Writer
	Builder     Builder
	GitHub      programview.Fetcher
	Agents      programview.AgentLister
	Now         func() time.Time
	OpenBrowser func(string) error
}

// Serve verifies the program, listens on loopback, and blocks until cancellation.
func Serve(ctx context.Context, options Options) error {
	if options.Port < 0 || options.Port > 65535 {
		return fmt.Errorf("program UI port %d is outside 0-65535", options.Port)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	builder := options.Builder
	if builder == nil {
		github := options.GitHub
		if github == nil {
			github = programview.NewGHFetcher()
		}
		cachedGitHub := newGitHubCache(github, githubTTL, now)
		agents := options.Agents
		if agents == nil {
			agents = programview.NewHerdrAgentLister()
		}
		builder = func(slug, detailItem string) (programview.Snapshot, error) {
			return programview.Build(slug, programview.Options{
				Now: now, GitHub: cachedGitHub, Agents: agents, DetailItem: detailItem,
			})
		}
	}
	cache := newSnapshotCache(snapshotTTL, now, builder)
	if _, err := cache.Get(options.Slug, ""); err != nil {
		return fmt.Errorf("verify program %q: %w", options.Slug, err)
	}
	if err := ctx.Err(); err != nil {
		return nil
	}

	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", options.Port))
	if err != nil {
		return fmt.Errorf("listen for program UI on 127.0.0.1:%d: %w", options.Port, err)
	}
	_, actualPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		closeErr := listener.Close()
		return errors.Join(fmt.Errorf("resolve program UI listener address %s: %w", listener.Addr(), err), closeErr)
	}
	url := "http://127.0.0.1:" + actualPort
	server := &http.Server{
		Handler:           newHandler(options.Slug, actualPort, cache),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveError := make(chan error, 1)
	go func() {
		serveError <- server.Serve(listener)
	}()

	out := options.Out
	if out == nil {
		out = os.Stdout
	}
	if _, err := fmt.Fprintln(out, url); err != nil {
		stopErr := stopServer(server, serveError)
		return errors.Join(fmt.Errorf("print program UI URL: %w", err), stopErr)
	}
	if options.Open {
		openBrowser := options.OpenBrowser
		if openBrowser == nil {
			openBrowser = ui.OpenBrowser
		}
		if err := openBrowser(url); err != nil {
			if _, printErr := fmt.Fprintf(out, "warning: open program UI: %v\n", err); printErr != nil {
				stopErr := stopServer(server, serveError)
				return errors.Join(fmt.Errorf("print browser warning: %w", printErr), stopErr)
			}
		}
	}

	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shut down program UI: %w", err)
		}
		err := <-serveError
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve program UI: %w", err)
		}
		return nil
	case err := <-serveError:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve program UI: %w", err)
	}
}

func stopServer(server *http.Server, serveError <-chan error) error {
	closeErr := server.Close()
	serveErr := <-serveError
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(closeErr, serveErr)
}
