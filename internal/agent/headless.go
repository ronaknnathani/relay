package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// HeadlessTurnTimeout bounds one automated Relay turn. A turn that has not
// finished by then is killed with its whole process group.
const HeadlessTurnTimeout = 10 * time.Minute

// AutomatedTurnEnvVar marks a process as a bounded automated Relay turn. CEO-only
// commands refuse to run when it is set.
const AutomatedTurnEnvVar = "RELAY_AUTOMATED_TURN"

// AutomatedTurnEnvEntry is the exact environment entry a bounded turn receives.
const AutomatedTurnEnvEntry = AutomatedTurnEnvVar + "=1"

// AutomatedTurnSessionEnvVar carries the fresh agent session id of the bounded
// turn. Relay stamps durable governance the turn writes with a short prefix of
// it, so an automated action is never mistaken for a human CTO action.
const AutomatedTurnSessionEnvVar = "RELAY_AUTOMATED_TURN_SESSION_ID"

// HeadlessTurnOptions carries everything an adapter needs to build one bounded
// noninteractive turn. It is deliberately separate from LaunchOptions: a turn
// has no Herdr pane, no session name, and no interactive prompt.
type HeadlessTurnOptions struct {
	Repo       string // repository the turn runs in
	ProgramDir string // ~/.relay/programs/active/<slug>, outside the repository
	SessionID  string // fresh agent session identifier; never a resumed session
	Prompt     string // the complete noninteractive prompt
}

// HeadlessTurner is implemented by adapters whose CLI can run one bounded
// noninteractive turn and exit. Capabilities().HeadlessTurn reports the same
// fact for callers that only hold an Agent.
type HeadlessTurner interface {
	Agent
	HeadlessTurnArgs(o HeadlessTurnOptions) []string
}

// HeadlessTurnRequest describes one bounded automated turn.
type HeadlessTurnRequest struct {
	Agent      Agent
	Repo       string
	ProgramDir string
	Prompt     string
	// SessionID pins the fresh agent session. It is generated when empty; a
	// caller that needs the id before the turn starts (to name a log file, for
	// example) supplies it.
	SessionID string
	// LogPath receives the turn's combined stdout and stderr.
	LogPath string
	// Timeout overrides HeadlessTurnTimeout when positive.
	Timeout time.Duration
}

// HeadlessTurnResult records what one bounded turn did. A non-zero exit or a
// timeout is a recorded outcome, not a Go error: only Relay-side failures
// (unsupported agent, unusable log path, missing binary) return an error.
type HeadlessTurnResult struct {
	SessionID string
	LogPath   string
	StartedAt time.Time
	EndedAt   time.Time
	ExitCode  int
	TimedOut  bool
	Error     string
}

// ProcessRunner starts one resolved argv with env and copies its combined
// output into output. It returns the process exit code and, when the process
// did not exit cleanly, the reason.
type ProcessRunner func(ctx context.Context, path string, args, env []string, output io.Writer) (int, error)

// HeadlessTurnDeps injects the clock, the session-id source, binary lookup, and
// the process runner so bounded turns are testable without a real agent CLI.
type HeadlessTurnDeps struct {
	Now          func() time.Time
	NewSessionID func() (string, error)
	Lookup       func(Agent) (string, error)
	Run          ProcessRunner
}

// NewSessionID returns a fresh random RFC 4122 version 4 UUID.
func NewSessionID() (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", fmt.Errorf("generate agent session id: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

// RunHeadlessTurn runs exactly one bounded noninteractive turn in a fresh agent
// session and records its combined output at request.LogPath.
func RunHeadlessTurn(
	ctx context.Context,
	request HeadlessTurnRequest,
	deps HeadlessTurnDeps,
) (result HeadlessTurnResult, retErr error) {
	deps = normalizedTurnDeps(deps)
	turner, err := headlessTurner(request.Agent)
	if err != nil {
		return HeadlessTurnResult{}, err
	}
	sessionID := request.SessionID
	if sessionID == "" {
		generated, err := deps.NewSessionID()
		if err != nil {
			return HeadlessTurnResult{}, err
		}
		sessionID = generated
	}
	binary, err := deps.Lookup(turner)
	if err != nil {
		return HeadlessTurnResult{}, err
	}
	log, err := createTurnLog(request.LogPath)
	if err != nil {
		return HeadlessTurnResult{}, err
	}
	defer func() {
		if err := log.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close bounded turn log %s: %w", request.LogPath, err))
		}
	}()

	timeout := request.Timeout
	if timeout <= 0 {
		timeout = HeadlessTurnTimeout
	}
	turnCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := turner.HeadlessTurnArgs(HeadlessTurnOptions{
		Repo:       request.Repo,
		ProgramDir: request.ProgramDir,
		SessionID:  sessionID,
		Prompt:     request.Prompt,
	})
	result = HeadlessTurnResult{
		SessionID: sessionID,
		LogPath:   request.LogPath,
		StartedAt: deps.Now().UTC(),
	}
	code, runErr := deps.Run(turnCtx, binary, args, automatedTurnEnv(os.Environ(), sessionID), log)
	result.EndedAt = deps.Now().UTC()
	result.ExitCode = code
	if runErr != nil {
		result.Error = runErr.Error()
		if result.ExitCode == 0 {
			result.ExitCode = -1
		}
	}
	if errors.Is(turnCtx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		if result.ExitCode == 0 {
			result.ExitCode = -1
		}
		result.Error = fmt.Sprintf("bounded turn exceeded its %s limit", timeout)
	}
	return result, nil
}

func normalizedTurnDeps(deps HeadlessTurnDeps) HeadlessTurnDeps {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.NewSessionID == nil {
		deps.NewSessionID = NewSessionID
	}
	if deps.Lookup == nil {
		deps.Lookup = func(a Agent) (string, error) { return a.Lookup() }
	}
	if deps.Run == nil {
		deps.Run = ExecTurnRunner
	}
	return deps
}

func headlessTurner(a Agent) (HeadlessTurner, error) {
	if a == nil {
		return nil, errors.New("run bounded turn: no agent was configured")
	}
	turner, ok := a.(HeadlessTurner)
	if !ok || !a.Capabilities().HeadlessTurn {
		return nil, fmt.Errorf(
			"agent %q cannot run a bounded noninteractive turn; "+
				"only copilot supports headless turns today—recreate the program with copilot "+
				"or drive it from the interactive CTO session",
			a.Name(),
		)
	}
	return turner, nil
}

func createTurnLog(path string) (*os.File, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("run bounded turn: log path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create bounded turn log directory %s: %w", dir, err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create bounded turn log %s: %w", path, err)
	}
	return file, nil
}

// automatedTurnEnv marks the child process as one bounded automated turn and
// names the session it runs as. Inherited markers are dropped first so a stale
// or forged value from the parent environment can never survive.
func automatedTurnEnv(environ []string, sessionID string) []string {
	result := make([]string, 0, len(environ)+2)
	for _, entry := range environ {
		if strings.HasPrefix(entry, AutomatedTurnEnvVar+"=") ||
			strings.HasPrefix(entry, AutomatedTurnSessionEnvVar+"=") {
			continue
		}
		result = append(result, entry)
	}
	return append(result, AutomatedTurnEnvEntry, AutomatedTurnSessionEnvVar+"="+sessionID)
}

// ExecTurnRunner runs argv directly — never through a shell — in its own
// process group, so a timeout kills the agent and everything it spawned.
func ExecTurnRunner(
	ctx context.Context,
	path string,
	args, env []string,
	output io.Writer,
) (int, error) {
	command := exec.Command(path, args...)
	command.Env = env
	command.Stdin = nil
	command.Stdout = output
	command.Stderr = output
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return -1, fmt.Errorf("start bounded turn %s: %w", path, err)
	}
	group := command.Process.Pid
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return exitCode(command, err), err
	case <-ctx.Done():
		killErr := syscall.Kill(-group, syscall.SIGKILL)
		waitErr := <-done
		code := exitCode(command, waitErr)
		if code == 0 {
			code = -1
		}
		return code, fmt.Errorf("bounded turn %s stopped: %w", path, errors.Join(ctx.Err(), killErr))
	}
}

func exitCode(command *exec.Cmd, err error) int {
	if command.ProcessState != nil {
		return command.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}
