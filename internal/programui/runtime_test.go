package programui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
)

func TestServeOrReuseOpensRequestedPathOnRunningServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstOutput := newLineWriter()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- ServeOrReuse(firstContext, reusableProgramUIOptions(firstOutput))
	}()

	baseURL := waitForProgramURL(t, firstOutput, firstDone)
	var secondOutput bytes.Buffer
	var opened string
	err := ServeOrReuse(context.Background(), Options{
		InitialPath:  "/programs/alpha/",
		Port:         0,
		PortExplicit: true,
		Open:         true,
		Out:          &secondOutput,
		OpenBrowser: func(target string) error {
			opened = target
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := baseURL + "/programs/alpha/"
	if strings.TrimSpace(secondOutput.String()) != want || opened != want {
		t.Fatalf("reused target = (%q, %q), want %q", secondOutput.String(), opened, want)
	}

	cancelFirst()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(program.RelayDir(), programUIStateName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime state remained after shutdown: %v", err)
	}
}

func TestServeOrReuseRejectsDifferentExplicitPort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstOutput := newLineWriter()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- ServeOrReuse(firstContext, reusableProgramUIOptions(firstOutput))
	}()
	baseURL := waitForProgramURL(t, firstOutput, firstDone)
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}

	err = ServeOrReuse(context.Background(), Options{
		Port:         1,
		PortExplicit: true,
		Open:         false,
		Out:          io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "already running on port "+parsed.Port()) {
		t.Fatalf("port mismatch error = %v", err)
	}

	cancelFirst()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestServeOrReuseRestartsAfterGracefulShutdown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	startAndStop := func() string {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		output := newLineWriter()
		done := make(chan error, 1)
		go func() {
			done <- ServeOrReuse(ctx, reusableProgramUIOptions(output))
		}()
		target := waitForProgramURL(t, output, done)
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		return target
	}

	_ = startAndStop()
	_ = startAndStop()
}

func TestServeOrReuseConcurrentColdStartsUseOnePort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outputs := []*lineWriter{newLineWriter(), newLineWriter()}
	done := []chan error{make(chan error, 1), make(chan error, 1)}
	start := make(chan struct{})
	for index := range outputs {
		go func(index int) {
			<-start
			done[index] <- ServeOrReuse(ctx, reusableProgramUIOptions(outputs[index]))
		}(index)
	}
	close(start)

	targets := make([]string, len(outputs))
	for index, output := range outputs {
		select {
		case line := <-output.lines:
			targets[index] = strings.TrimSpace(line)
		case <-time.After(10 * time.Second):
			t.Fatalf("invocation %d did not publish a URL", index)
		}
	}
	if targets[0] != targets[1] {
		t.Fatalf("concurrent targets = %q and %q", targets[0], targets[1])
	}

	returned := -1
	select {
	case err := <-done[0]:
		if err != nil {
			t.Fatal(err)
		}
		returned = 0
	case err := <-done[1]:
		if err != nil {
			t.Fatal(err)
		}
		returned = 1
	case <-time.After(3 * time.Second):
		t.Fatal("neither concurrent invocation reused the running server")
	}

	cancel()
	if err := <-done[1-returned]; err != nil {
		t.Fatal(err)
	}
}

func TestServeOrReuseRecoversAfterForeignProbeAndLockRelease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	originalTimeout := programUIReuseTimeout
	originalDelay := programUIRetryDelay
	programUIReuseTimeout = 2 * time.Second
	programUIRetryDelay = 20 * time.Millisecond
	t.Cleanup(func() {
		programUIReuseTimeout = originalTimeout
		programUIRetryDelay = originalDelay
	})

	if err := os.MkdirAll(program.RelayDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := patrollock.Acquire(filepath.Join(program.RelayDir(), programUILockName))
	if err != nil {
		t.Fatal(err)
	}
	foreign := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"schema":"foreign"}`))
	}))
	defer foreign.Close()
	if err := writeProgramUIRuntimeState(
		filepath.Join(program.RelayDir(), programUIStateName),
		programUIRuntimeState{URL: foreign.URL},
	); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := newLineWriter()
	done := make(chan error, 1)
	go func() {
		done <- ServeOrReuse(ctx, reusableProgramUIOptions(output))
	}()
	time.Sleep(100 * time.Millisecond)
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}

	target := waitForProgramURL(t, output, done)
	if target == foreign.URL {
		t.Fatal("foreign listener was reused")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func reusableProgramUIOptions(out io.Writer) Options {
	return Options{
		Port: 0, Open: false, Out: out,
		OverviewBuilder: func() (programview.OverviewSnapshot, error) {
			return programview.OverviewSnapshot{
				Schema:      programview.OverviewSchemaVersion,
				Programs:    []programview.ProgramOverviewDTO{{Slug: "alpha"}},
				Work:        []programview.OverviewWorkItemDTO{},
				Diagnostics: []programview.OverviewDiagnosticDTO{},
			}, nil
		},
		LocalBuilder: func(slug, detail string) (programview.Snapshot, error) {
			return programview.Snapshot{
				Schema:     programview.SchemaVersion,
				Program:    programview.ProgramDTO{Slug: slug},
				DetailItem: detail,
				Items:      []programview.ItemDTO{},
				Contracts:  []programview.ContractDTO{},
				Warnings:   []string{},
			}, nil
		},
		Builder: func(slug, detail string) (programview.Snapshot, error) {
			return programview.Snapshot{
				Schema:     programview.SchemaVersion,
				Program:    programview.ProgramDTO{Slug: slug},
				DetailItem: detail,
				Items:      []programview.ItemDTO{},
				Contracts:  []programview.ContractDTO{},
				Warnings:   []string{},
			}, nil
		},
	}
}
