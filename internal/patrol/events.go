package patrol

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/ui"
)

// maxLoggedReasons bounds one tick line. Attention codes are stable and safe to
// print, but a large program can carry dozens of them and an operational event
// has to stay one readable line in the patrol pane.
const maxLoggedReasons = 8

// maxLoggedCodeLength bounds one printed reason code so an unexpectedly long
// identifier cannot dominate a line or smuggle a payload into it.
const maxLoggedCodeLength = 64

// identifierReasonFamilies lists the reason families whose detail is a durable
// Relay identifier—an item ID, a decision ID, a child phase, or a contract
// ref—and is therefore safe to print in full. Every other family prints as the
// family alone, because its detail is derived from free warning text that can
// quote paths or error output. Unknown families redact by default.
var identifierReasonFamilies = map[string]bool{
	"open-decision":        true,
	"unread-worker-outbox": true,
	"ready-item":           true,
	"orphan-item":          true,
	"blocked-item":         true,
	"missing-worker":       true,
	"early-child-phase":    true,
	"contract-warning":     true,
	"contract-drift":       true,
}

// eventLog writes high-level patrol events to the patrol pane: routine
// operational events to out, and outcomes that leave attention undelivered to
// err. Nothing is written to disk—the pane is the log—and nothing that could
// carry program content is printed, only timestamps, slugs, safe enums, pane
// IDs, and reason codes.
//
// Every write is reported. A patrol whose events cannot reach the pane is not
// observable, so its caller stops instead of running blind.
type eventLog struct {
	out io.Writer
	err io.Writer
	// loc is the zone events are stamped in: the pane is read by a person, so
	// it carries their wall clock rather than the UTC the record carries. A nil
	// loc means the host's own zone.
	loc *time.Location
}

func newEventLog(out, err io.Writer, loc *time.Location) eventLog {
	return eventLog{out: out, err: err, loc: loc}
}

func (l eventLog) started(at time.Time, slug string) error {
	return l.write(l.out, at, ui.EventStart, "program="+slug)
}

// tick prints one due observation. It carries the cadence it chose and the wall
// clock the next observation is actually due at, so a reader learns when the
// patrol wakes next from the line that made the decision rather than from a
// second line that repeats it.
func (l eventLog) tick(at time.Time, reasons []Reason, delaySeconds int64, nextTickAt string) error {
	return l.write(l.out, at, ui.EventTick, fmt.Sprintf(
		"cadence=%s%s reasons=%s",
		cadenceLabel(delaySeconds), l.nextField(at, nextTickAt), loggedReasons(reasons),
	))
}

func (l eventLog) stopped(at time.Time, slug, reason string) error {
	event := "program=" + slug
	if reason != "" {
		event += " reason=" + reason
	}
	return l.write(l.out, at, ui.EventStop, event)
}

// wake prints one tech lead wake decision. Delivered and not-needed are routine;
// every other outcome left attention pending and belongs on stderr with the
// command that shows the full recorded detail.
func (l eventLog) wake(at time.Time, slug string, outcome turnOutcome) error {
	event := "TL " + string(outcome.Kind)
	switch {
	case len(outcome.Panes) == 1:
		event += " pane=" + outcome.Panes[0]
	case len(outcome.Panes) > 1:
		event += " panes=" + strings.Join(outcome.Panes, ",")
	}
	if outcome.Status != "" {
		event += " status=" + outcome.Status
	}
	if !outcome.degraded() {
		return l.write(l.out, at, ui.EventWake, event)
	}
	return l.write(l.err, at, ui.EventWarn, fmt.Sprintf(
		"%s; attention remains pending, see `relay program patrol status %s`", event, slug,
	))
}

// retry prints an observation failure with the cadence and the wall clock the
// patrol will try again at. A failed tick prints no summary of its own, so this
// is the only line that says when the patrol comes back.
func (l eventLog) retry(at time.Time, message string, delaySeconds int64, nextTickAt string) error {
	return l.write(l.err, at, ui.EventError, fmt.Sprintf(
		"%s; cadence=%s%s", message, cadenceLabel(delaySeconds), l.nextField(at, nextTickAt),
	))
}

func (l eventLog) failure(at time.Time, message string) error {
	return l.write(l.err, at, ui.EventError, message)
}

// nextField renders the scheduled time a line carries, and nothing at all when
// there is none: a terminal event has no next tick, and a blank `next=` would
// claim a schedule the patrol does not have.
func (l eventLog) nextField(at time.Time, nextTickAt string) string {
	next := ui.CompactScheduledText(nextTickAt, at, l.loc)
	if next == "" {
		return ""
	}
	return " next=" + next
}

func (l eventLog) write(writer io.Writer, at time.Time, label, event string) error {
	if writer == nil {
		return nil
	}
	line := ui.EventLine(at, l.loc, label, event)
	if _, err := fmt.Fprintln(writer, line); err != nil {
		return fmt.Errorf("write patrol event %q: %w", line, err)
	}
	return nil
}

func loggedReasons(reasons []Reason) string {
	if len(reasons) == 0 {
		return "none"
	}
	codes := make([]string, 0, len(reasons))
	seen := make(map[string]bool, len(reasons))
	for _, reason := range reasons {
		code := loggedCode(reason.Code)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		codes = append(codes, code)
	}
	sort.Strings(codes)
	if len(codes) == 0 {
		return "none"
	}
	if len(codes) <= maxLoggedReasons {
		return strings.Join(codes, ",")
	}
	return fmt.Sprintf(
		"%s,+%d more", strings.Join(codes[:maxLoggedReasons], ","), len(codes)-maxLoggedReasons,
	)
}

// loggedCode returns the printable form of one reason code: the whole code when
// its family names an identifier detail, and the bare family otherwise.
func loggedCode(code string) string {
	family, _, hasDetail := strings.Cut(code, ":")
	if !hasDetail {
		return family
	}
	if !identifierReasonFamilies[family] || len(code) > maxLoggedCodeLength {
		return family
	}
	return code
}

func cadenceLabel(delaySeconds int64) string {
	if delaySeconds > 0 && delaySeconds%60 == 0 {
		return fmt.Sprintf("%dm", delaySeconds/60)
	}
	return fmt.Sprintf("%ds", delaySeconds)
}
