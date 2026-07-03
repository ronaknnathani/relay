// Package ui contains terminal presentation helpers used by the CLI.
package ui

import "os"

// ANSI color escape codes.
const (
	Reset   = "\033[0m"
	Bold    = "\033[1m"
	Dim     = "\033[2m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"
)

var useColor = true

// InitColor decides whether color is enabled based on whether stdout is a TTY.
// If stdout is not a character device (pipe, redirect), color is disabled.
func InitColor() {
	fi, err := os.Stdout.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		useColor = false
	}
}

// DisableColor forces color off. Used by the --no-color flag.
func DisableColor() { useColor = false }

// Color wraps s with the given ANSI code and a reset, unless color is disabled.
func Color(code, s string) string {
	if !useColor {
		return s
	}
	return code + s + Reset
}

// StatusColor wraps a status string in its conventional color.
func StatusColor(status string) string {
	switch status {
	case "initialized":
		return Color(Cyan, status)
	case "implementing":
		return Color(Yellow, status)
	case "complete":
		return Color(Green, status)
	default:
		return status
	}
}

// PhaseColor wraps a phase string in its conventional color.
func PhaseColor(phase string) string {
	switch phase {
	case "done":
		return Color(Green, phase)
	case "init", "plan", "discuss":
		return Color(Cyan, phase)
	case "implement", "simplify":
		return Color(Yellow, phase)
	case "review", "fix", "validate":
		return Color(Magenta, phase)
	case "rebase", "pr", "ci", "code-review":
		return Color(Blue, phase)
	default:
		return phase
	}
}
