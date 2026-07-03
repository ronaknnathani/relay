package ui

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// LabelWidth is the column width used by PrintField for label alignment.
const LabelWidth = 18

// PadRight returns s padded with spaces on the right to the given width.
func PadRight(s string, width int) string {
	pad := width - len(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

// TerminalWidth returns the current terminal width in columns, or 120
// when stdout is not a TTY or the width cannot be determined.
func TerminalWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 120
	}
	return w
}

// PrintField prints a "Label: value" line with the label dim-colored and
// padded to LabelWidth.
func PrintField(label, value string) {
	fmt.Printf("  %s %s\n", Color(Dim, PadRight(label+":", LabelWidth)), value)
}

// Warn prints a "warning: ..." message to stderr. Use for non-fatal errors
// where execution continues.
func Warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
}
