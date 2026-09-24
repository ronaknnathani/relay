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

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/ui"
)

const (
	snapshotTTL        = 5 * time.Second
	githubTTL          = 12 * time.Second
	initialRefreshWait = 500 * time.Millisecond
	overviewRefreshTTL = 3 * time.Second
)

var programUIHerdrCommandTimeout = 5 * time.Second

// Options configures the foreground local Program UI server.
type Options struct {
	Slug            string
	Port            int
	Open            bool
	Out             io.Writer
	Builder         Builder
	LocalBuilder    Builder
	OverviewBuilder func() (programview.OverviewSnapshot, error)
	GitHub          programview.Fetcher
	Agents          programview.AgentLister
	Now             func() time.Time
	OpenBrowser     func(string) error
}

// Serve starts the loopback Program UI and blocks until cancellation. A non-empty
// slug serves one verified program; an empty slug serves the unified overview.
func Serve(ctx context.Context, options Options) error {
	if options.Port < 0 || options.Port > 65535 {
		return fmt.Errorf("program UI port %d is outside 0-65535", options.Port)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	builder, localBuilder := detailBuilders(ctx, options, now)
	overviewBuilder := options.OverviewBuilder

	var detailSeed programview.Snapshot
	var overviewSeed programview.OverviewSnapshot
	var err error
	if options.Slug == "" {
		if overviewBuilder == nil {
			overviewBuilder = func() (programview.OverviewSnapshot, error) {
				programs, diagnostics, discoverErr := program.Discover(program.ActiveDir())
				if discoverErr != nil {
					return programview.OverviewSnapshot{}, discoverErr
				}
				return programview.BuildOverview(programs, diagnostics, now), nil
			}
		}
		overviewSeed, err = overviewBuilder()
		if err != nil {
			return fmt.Errorf("build program overview: %w", err)
		}
	} else {
		detailSeed, err = localBuilder(options.Slug, "")
		if err != nil {
			return fmt.Errorf("verify program %q: %w", options.Slug, err)
		}
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
	var handler http.Handler
	if options.Slug == "" {
		overview := newOverviewFeed(
			overviewSeed, overviewRefreshTTL, now, overviewBuilder,
		)
		handler = newUnifiedRouter(actualPort, overview, func(slug string) (http.Handler, error) {
			seed, buildErr := localBuilder(slug, "")
			if buildErr != nil {
				return nil, buildErr
			}
			detail, feed := newDetailRuntime(slug, actualPort, seed, builder, now)
			scheduleDetailRefresh(ctx, feed)
			return detail, nil
		})
	} else {
		detail, feed := newDetailRuntime(options.Slug, actualPort, detailSeed, builder, now)
		handler = detail
		scheduleDetailRefresh(ctx, feed)
	}
	server := &http.Server{
		Handler:           handler,
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
			closeErr := server.Close()
			serveErr := <-serveError
			if errors.Is(serveErr, http.ErrServerClosed) {
				serveErr = nil
			}
			if closeErr != nil || serveErr != nil {
				return errors.Join(
					fmt.Errorf("force close program UI after graceful shutdown: %w", err),
					closeErr,
					serveErr,
				)
			}
			return nil
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

func detailBuilders(
	ctx context.Context,
	options Options,
	now func() time.Time,
) (Builder, Builder) {
	builder := options.Builder
	localBuilder := options.LocalBuilder
	if builder != nil {
		if localBuilder == nil {
			localBuilder = builder
		}
		return builder, localBuilder
	}
	github := options.GitHub
	if github == nil {
		github = programview.NewGHFetcher()
	}
	cachedGitHub := newGitHubCache(github, githubTTL, now)
	agents := options.Agents
	if agents == nil {
		agents = programview.NewHerdrAgentListerWithCommandTimeout(
			ctx, programUIHerdrCommandTimeout,
		)
	}
	if agents != nil {
		agents = newAgentCache(agents, githubTTL, now)
	}
	builder = func(slug, detailItem string) (programview.Snapshot, error) {
		return programview.Build(slug, programview.Options{
			Now: now, GitHub: cachedGitHub, Agents: agents, DetailItem: detailItem,
		})
	}
	if localBuilder == nil {
		localBuilder = func(slug, detailItem string) (programview.Snapshot, error) {
			return programview.Build(slug, programview.Options{
				Now: now, GitHub: cachedGitHub, Agents: agents,
				DetailItem: detailItem, LocalOnly: true,
			})
		}
	}
	return builder, localBuilder
}

func newDetailRuntime(
	slug, port string,
	seed programview.Snapshot,
	builder Builder,
	now func() time.Time,
) (http.Handler, *snapshotFeed) {
	cache := newSnapshotCache(snapshotTTL, now, builder)
	feed := newSnapshotFeed(seed, snapshotTTL, now, func() (programview.Snapshot, error) {
		return builder(slug, "")
	})
	return newHandler(slug, port, cache, feed, defaultArtifactLoader), feed
}

func scheduleDetailRefresh(ctx context.Context, feed *snapshotFeed) {
	go func() {
		timer := time.NewTimer(initialRefreshWait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
			feed.Refresh()
		}
	}()
}

func stopServer(server *http.Server, serveError <-chan error) error {
	closeErr := server.Close()
	serveErr := <-serveError
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(closeErr, serveErr)
}
