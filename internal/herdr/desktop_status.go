package herdr

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Desktop app agents report their status with OSC 7501 written to their own
// terminal. Copilot and Claude don't emit it, so Relay installs agent hooks
// that call ReportDesktopStatus on each lifecycle event.

var desktopStates = map[string]bool{
	"idle": true, "working": true, "done": true, "blocked": true, "error": true, "clear": true,
}

var desktopKinds = map[string]bool{"": true, "permission": true, "question": true, "auth": true}

// DesktopStatusRecord renders one OSC 7501 status record.
func DesktopStatusRecord(state, kind, app string) (string, error) {
	if !desktopStates[state] {
		return "", fmt.Errorf("unknown Herdr desktop status %q; want idle, working, done, blocked, error or clear", state)
	}
	if !desktopKinds[kind] {
		return "", fmt.Errorf("unknown Herdr desktop status kind %q; want permission, question or auth", kind)
	}
	if kind != "" && state != "blocked" {
		return "", fmt.Errorf("status kind %q applies only to blocked", kind)
	}
	fields := []string{"state=" + state}
	if kind != "" {
		fields = append(fields, "kind="+kind)
	}
	if app = strings.TrimSpace(app); app != "" {
		if strings.ContainsAny(app, ":;=\x1b\x07") {
			return "", fmt.Errorf("app name %q contains a reserved character", app)
		}
		fields = append(fields, "app="+app)
	}
	return "\x1b]7501;" + strings.Join(fields, ":") + "\x1b\\", nil
}

// ReportDesktopStatus writes state to the terminal of the agent that ran this
// hook. It does nothing outside a Herdr desktop app terminal, and nothing for
// hook events fired inside a subagent, whose turns don't change the session's
// own status.
func ReportDesktopStatus(state, kind, app string, hookInput []byte) error {
	record, err := DesktopStatusRecord(state, kind, app)
	if err != nil {
		return err
	}
	if !DesktopEnvironment() || firedInSubagent(hookInput) {
		return nil
	}
	tty, err := agentTTY()
	if err != nil {
		return err
	}
	defer tty.Close()
	// One small write, so it never interleaves with the agent's own output.
	_, err = tty.WriteString(record)
	return err
}

// RecordDesktopSession stores the agent's session id from its hook input as a
// label on the agent's terminal, where the desktop backend reads it back as
// NativeSessionID. Input without a session id, or from a subagent, is ignored.
func RecordDesktopSession(hookInput []byte) error {
	if !DesktopEnvironment() || firedInSubagent(hookInput) {
		return nil
	}
	sessionID := hookSessionID(hookInput)
	if sessionID == "" {
		return nil
	}
	terminal := os.Getenv("HC_TERMINAL_ID")
	output, err := exec.Command("hc", "label", "--json", terminal, desktopSessionLabel+"="+sessionID).CombinedOutput()
	if err != nil {
		return fmt.Errorf("label terminal %s with session %s: %w: %s", terminal, sessionID, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func hookSessionID(hookInput []byte) string {
	var input map[string]any
	if json.Unmarshal(hookInput, &input) != nil {
		return ""
	}
	for _, key := range []string{"session_id", "sessionId"} {
		if value, ok := input[key].(string); ok && value != "" && !strings.ContainsAny(value, "= \t\n") {
			return value
		}
	}
	return ""
}

func firedInSubagent(hookInput []byte) bool {
	var input map[string]any
	if json.Unmarshal(hookInput, &input) != nil {
		return false
	}
	for _, key := range []string{"agentId", "agent_id"} {
		if value, ok := input[key].(string); ok && value != "" {
			return true
		}
	}
	return false
}

// agentTTY opens the terminal the agent runs in. Hooks usually run without a
// controlling terminal, so it walks up the process tree to the first
// ancestor that has one: the agent itself.
func agentTTY() (*os.File, error) {
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		return tty, nil
	}
	pid := os.Getppid()
	for depth := 0; depth < 16 && pid > 1; depth++ {
		output, err := exec.Command("ps", "-o", "ppid=,tty=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			break
		}
		fields := strings.Fields(string(output))
		if len(fields) != 2 {
			break
		}
		if tty := fields[1]; tty != "??" && tty != "?" {
			return os.OpenFile("/dev/"+tty, os.O_WRONLY, 0)
		}
		next, err := strconv.Atoi(fields[0])
		if err != nil {
			break
		}
		pid = next
	}
	return nil, fmt.Errorf("no ancestor of process %d has a terminal", os.Getpid())
}
