package ui

import "time"

// Pane event labels. A long-running Relay process — a program patrol, a pull
// request watcher — prints one line per high-level event into the pane a person
// is watching, and each line opens with one of these. They are uppercase and
// padded to the same five columns so the fields after them line up when a
// reader scans a pane, and so the shape of a line is legible before any of its
// words are read.
const (
	EventStart = "START"
	EventTick  = "TICK "
	EventCheck = "CHECK"
	EventWake  = "WAKE "
	EventDone  = "DONE "
	EventStop  = "STOP "
	EventWarn  = "WARN "
	EventError = "ERROR"
)

// EventLine renders one pane event: the reader's own wall clock in brackets, an
// aligned label, and the fields that event carries. An event with no fields
// prints as its label alone rather than with a dangling separator.
func EventLine(at time.Time, loc *time.Location, label, fields string) string {
	line := "[" + CompactTime(at, loc) + "] " + label
	if fields == "" {
		return line
	}
	return line + " " + fields
}
