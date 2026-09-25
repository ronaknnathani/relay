package programui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/ui"
)

const (
	programUIStateName = "program-ui.json"
	programUILockName  = "program-ui.lock"
)

var (
	programUIReuseTimeout = 10 * time.Second
	programUIRetryDelay   = 50 * time.Millisecond
	programUIProbeTimeout = 500 * time.Millisecond
)

type programUIRuntimeState struct {
	PID       int    `json:"pid"`
	URL       string `json:"url"`
	StartedAt string `json:"started_at"`
}

// ServeOrReuse starts the unified Program UI or reuses the process already
// serving it. InitialPath controls which page is printed and opened.
func ServeOrReuse(ctx context.Context, options Options) error {
	if err := os.MkdirAll(program.RelayDir(), 0o700); err != nil {
		return fmt.Errorf("create Relay directory %s: %w", program.RelayDir(), err)
	}
	lockPath := filepath.Join(program.RelayDir(), programUILockName)
	statePath := filepath.Join(program.RelayDir(), programUIStateName)
	deadline := time.Now().Add(programUIReuseTimeout)
	var lastReuseErr error
	for {
		lock, err := patrollock.Acquire(lockPath)
		if err == nil {
			if removeErr := os.Remove(statePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				_ = lock.Release()
				return fmt.Errorf("remove stale program UI state %s: %w", statePath, removeErr)
			}
			options.Slug = ""
			options.onReady = func(baseURL string) error {
				return writeProgramUIRuntimeState(statePath, programUIRuntimeState{
					PID:       os.Getpid(),
					URL:       baseURL,
					StartedAt: time.Now().UTC().Format(time.RFC3339),
				})
			}
			serveErr := Serve(ctx, options)
			removeErr := os.Remove(statePath)
			if errors.Is(removeErr, os.ErrNotExist) {
				removeErr = nil
			}
			return errors.Join(serveErr, removeErr, lock.Release())
		}
		if !errors.Is(err, patrollock.ErrLocked) {
			return err
		}

		state, reuseErr := readReusableProgramUI(statePath)
		if reuseErr == nil {
			if options.PortExplicit && options.Port != 0 && options.Port != statePort(state.URL) {
				return fmt.Errorf(
					"program UI is already running on port %d, not requested port %d",
					statePort(state.URL),
					options.Port,
				)
			}
			return printAndOpenProgramUI(options, state.URL)
		}
		lastReuseErr = reuseErr
		if !time.Now().Before(deadline) {
			return fmt.Errorf("reuse running program UI: %w", lastReuseErr)
		}
		timer := time.NewTimer(programUIRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func readReusableProgramUI(path string) (programUIRuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return programUIRuntimeState{}, fmt.Errorf("read runtime state %s: %w", path, err)
	}
	var state programUIRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return programUIRuntimeState{}, fmt.Errorf("decode runtime state %s: %w", path, err)
	}
	if err := validateProgramUIBaseURL(state.URL); err != nil {
		return programUIRuntimeState{}, err
	}
	if err := probeProgramUI(state.URL); err != nil {
		return programUIRuntimeState{}, err
	}
	return state, nil
}

func validateProgramUIBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse program UI URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("program UI URL %q is not a loopback HTTP origin", raw)
	}
	host := parsed.Hostname()
	if host != "127.0.0.1" && !strings.EqualFold(host, "localhost") {
		return fmt.Errorf("program UI URL %q is not loopback", raw)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("program UI URL %q has an invalid port", raw)
	}
	return nil
}

func probeProgramUI(baseURL string) error {
	client := &http.Client{
		Timeout: programUIProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Get(strings.TrimRight(baseURL, "/") + "/api/overview")
	if err != nil {
		return fmt.Errorf("probe program UI: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("probe program UI: status %d", response.StatusCode)
	}
	var snapshot struct {
		Schema string `json:"schema"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&snapshot); err != nil {
		return fmt.Errorf("decode program UI probe: %w", err)
	}
	if snapshot.Schema != programview.OverviewSchemaVersion {
		return fmt.Errorf("probe program UI: schema %q", snapshot.Schema)
	}
	return nil
}

func writeProgramUIRuntimeState(path string, state programUIRuntimeState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode runtime state: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".program-ui-*.json")
	if err != nil {
		return fmt.Errorf("create runtime state: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure runtime state %s: %w", tempPath, err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write runtime state %s: %w", tempPath, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close runtime state %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace runtime state %s: %w", path, err)
	}
	return nil
}

func printAndOpenProgramUI(options Options, baseURL string) error {
	targetURL := programUITargetURL(baseURL, options.InitialPath)
	out := options.Out
	if out == nil {
		out = os.Stdout
	}
	if _, err := fmt.Fprintln(out, targetURL); err != nil {
		return fmt.Errorf("print program UI URL: %w", err)
	}
	if !options.Open {
		return nil
	}
	openBrowser := options.OpenBrowser
	if openBrowser == nil {
		openBrowser = ui.OpenBrowser
	}
	if err := openBrowser(targetURL); err != nil {
		if _, printErr := fmt.Fprintf(out, "warning: open program UI: %v\n", err); printErr != nil {
			return fmt.Errorf("print browser warning: %w", printErr)
		}
	}
	return nil
}

func programUITargetURL(baseURL, path string) string {
	if path == "" || path == "/" {
		return strings.TrimRight(baseURL, "/")
	}
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func statePort(raw string) int {
	parsed, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	port, _ := strconv.Atoi(parsed.Port())
	return port
}
