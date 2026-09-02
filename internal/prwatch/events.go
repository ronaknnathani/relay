package prwatch

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/ui"
)

// shortFingerprintLength bounds the fingerprint prefix printed in the watcher
// pane. The full fingerprint identifies a digest file, so only enough of it to
// correlate two events is ever printed.
const shortFingerprintLength = 8

// eventLog writes high-level watcher events to the watcher pane: routine
// operational events to out, and outcomes that leave attention undelivered to
// err. Nothing is written to disk—the pane is the log—and nothing that could
// carry pull request content is printed: no bodies, no titles, no authors, no
// session ids, and never a whole fingerprint.
//
// Every write is reported. A watcher whose events cannot reach the pane is not
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

func (l eventLog) started(at time.Time, slug string, mode Mode, owner string, prNumber int) error {
	return l.write(l.out, at, ui.EventStart, fmt.Sprintf(
		"project=%s mode=%s owner=%s pr=#%d", slug, mode, owner, prNumber,
	))
}

// check prints one completed observation and everything it decided: what the
// pull request looks like now, and the cadence and wall clock the watcher will
// actually keep — after any wake outcome has rolled that schedule back. label is
// "start" for the immediate observation a watcher runs at start and "n=<count>"
// for a scheduled one. A watch that has finished has no next check and prints
// no `next=` rather than a blank one.
func (l eventLog) check(
	at time.Time, label string, digest Digest, delaySeconds int64, nextCheckAt string,
) error {
	fields := []string{
		label,
		fmt.Sprintf("pr=#%d", digest.PR.Number),
		"state=" + digest.PR.State,
		fmt.Sprintf("actionable=%d", len(digest.Items)),
		"cadence=" + cadenceLabel(time.Duration(delaySeconds)*time.Second),
	}
	if next := l.nextField(at, nextCheckAt); next != "" {
		fields = append(fields, next)
	}
	fields = append(fields,
		"reasons="+loggedReasons(digest.Items),
		"fp="+shortFingerprint(digest.Fingerprint),
	)
	return l.write(l.out, at, ui.EventCheck, strings.Join(fields, " "))
}

// wakeSkipped prints that a wake was abandoned because the observation it was
// built from stopped being the watcher's current attention.
func (l eventLog) wakeSkipped(at time.Time, owner, fingerprint string) error {
	return l.write(l.out, at, ui.EventWake, fmt.Sprintf(
		"skipped owner=%s fp=%s reason=observation-superseded",
		owner, shortFingerprint(fingerprint),
	))
}

// wake prints one owner wake decision. Only a delivered wake handed the
// attention over; every other outcome leaves it pending and belongs on stderr
// with the command that shows the recorded detail.
func (l eventLog) wake(at time.Time, slug string, outcome WakeOutcome, fingerprint string) error {
	event := fmt.Sprintf("%s owner=%s", outcome.Kind, outcome.Owner)
	switch {
	case outcome.PaneID != "":
		event += " pane=" + outcome.PaneID
	case len(outcome.Panes) > 0:
		event += " panes=" + strings.Join(outcome.Panes, ",")
	}
	if outcome.Status != "" {
		event += " status=" + string(outcome.Status)
	}
	event += " fp=" + shortFingerprint(fingerprint)
	if outcome.Delivered() {
		return l.write(l.out, at, ui.EventWake, event)
	}
	return l.write(l.err, at, ui.EventWarn, fmt.Sprintf(
		"%s; attention remains pending, see `relay pr watch status %s`", event, slug,
	))
}

func (l eventLog) complete(at time.Time, slug string, prNumber int) error {
	return l.write(l.out, at, ui.EventDone, fmt.Sprintf(
		"project=%s pr=#%d reason=merged", slug, prNumber,
	))
}

// closed prints that a watch ended because the pull request was closed without
// merging and its owner was handed the escalation.
func (l eventLog) closed(at time.Time, slug string, prNumber int, owner string) error {
	return l.write(l.out, at, ui.EventDone, fmt.Sprintf(
		"project=%s pr=#%d reason=closed-unmerged escalated-to=%s", slug, prNumber, owner,
	))
}

func (l eventLog) stopped(at time.Time, slug, reason string) error {
	event := "project=" + slug
	if reason != "" {
		event += " reason=" + reason
	}
	return l.write(l.out, at, ui.EventStop, event)
}

// retry prints an observation failure with the cadence and the wall clock the
// watcher will try again at. A failed observation prints no check line, so this
// is the only place a reader learns when the watcher comes back.
func (l eventLog) retry(at time.Time, message string, delaySeconds int64, nextCheckAt string) error {
	event := fmt.Sprintf(
		"%s; cadence=%s", message, cadenceLabel(time.Duration(delaySeconds)*time.Second),
	)
	if next := l.nextField(at, nextCheckAt); next != "" {
		event += " " + next
	}
	return l.write(l.err, at, ui.EventError, event)
}

func (l eventLog) failure(at time.Time, message string) error {
	return l.write(l.err, at, ui.EventError, message)
}

// nextField renders the scheduled check a line carries, and nothing at all when
// there is none: a finished watch has no next check, and a blank `next=` would
// claim a schedule it does not have.
func (l eventLog) nextField(at time.Time, nextCheckAt string) string {
	next := ui.CompactScheduledText(nextCheckAt, at, l.loc)
	if next == "" {
		return ""
	}
	return "next=" + next
}

func (l eventLog) write(writer io.Writer, at time.Time, label, event string) error {
	if writer == nil {
		return nil
	}
	line := ui.EventLine(at, l.loc, label, event)
	if _, err := fmt.Fprintln(writer, line); err != nil {
		return fmt.Errorf("write pr watch event %q: %w", line, err)
	}
	return nil
}

// loggedReasons renders the sorted unique reason codes of a digest. Reason
// codes are a closed enum, so they are safe to print in full.
func loggedReasons(items []Item) string {
	codes := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if seen[item.Reason] {
			continue
		}
		seen[item.Reason] = true
		codes = append(codes, item.Reason)
	}
	if len(codes) == 0 {
		return "none"
	}
	sort.Strings(codes)
	return strings.Join(codes, ",")
}

func shortFingerprint(fingerprint string) string {
	if fingerprint == "" {
		return "none"
	}
	return fingerprint[:shortFingerprintLength]
}

func cadenceLabel(delay time.Duration) string {
	seconds := int64(delay / time.Second)
	if seconds > 0 && seconds%60 == 0 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%ds", seconds)
}
