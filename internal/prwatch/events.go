package prwatch

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
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
}

func newEventLog(out, err io.Writer) eventLog {
	return eventLog{out: out, err: err}
}

func (l eventLog) started(at time.Time, slug string, mode Mode, owner string, prNumber int) error {
	return l.write(l.out, at, fmt.Sprintf(
		"pr watch started project=%s mode=%s owner=%s pr=#%d", slug, mode, owner, prNumber,
	))
}

// observation prints one completed observation. label is "start" for the
// immediate observation a watcher runs at start and "check n=<count>" for a
// scheduled one.
func (l eventLog) observation(at time.Time, label string, digest Digest, delay time.Duration) error {
	return l.write(l.out, at, fmt.Sprintf(
		"%s pr=#%d state=%s actionable=%d reasons=%s fp=%s cadence=%s",
		label, digest.PR.Number, digest.PR.State, len(digest.Items),
		loggedReasons(digest.Items), shortFingerprint(digest.Fingerprint), cadenceLabel(delay),
	))
}

func (l eventLog) nextCheck(at time.Time, nextCheckAt string, delay time.Duration) error {
	return l.write(l.out, at, fmt.Sprintf(
		"next check at=%s cadence=%s", nextCheckAt, cadenceLabel(delay),
	))
}

// wakeSkipped prints that a wake was abandoned because the observation it was
// built from stopped being the watcher's current attention.
func (l eventLog) wakeSkipped(at time.Time, owner, fingerprint string) error {
	return l.write(l.out, at, fmt.Sprintf(
		"owner wake skipped owner=%s fp=%s reason=observation-superseded",
		owner, shortFingerprint(fingerprint),
	))
}

// wake prints one owner wake decision. Only a delivered wake handed the
// attention over; every other outcome leaves it pending and belongs on stderr
// with the command that shows the recorded detail.
func (l eventLog) wake(at time.Time, slug string, outcome WakeOutcome, fingerprint string) error {
	event := fmt.Sprintf("owner wake %s owner=%s", outcome.Kind, outcome.Owner)
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
		return l.write(l.out, at, event)
	}
	return l.write(l.err, at, fmt.Sprintf(
		"warning: %s; attention remains pending, see `relay pr watch status %s`", event, slug,
	))
}

func (l eventLog) complete(at time.Time, slug string, prNumber int) error {
	return l.write(l.out, at, fmt.Sprintf(
		"pr watch complete project=%s pr=#%d reason=merged", slug, prNumber,
	))
}

// closed prints that a watch ended because the pull request was closed without
// merging and its owner was handed the escalation.
func (l eventLog) closed(at time.Time, slug string, prNumber int, owner string) error {
	return l.write(l.out, at, fmt.Sprintf(
		"pr watch complete project=%s pr=#%d reason=closed-unmerged escalated-to=%s",
		slug, prNumber, owner,
	))
}

func (l eventLog) stopped(at time.Time, slug, reason string) error {
	if reason == "" {
		return l.write(l.out, at, "pr watch stopped project="+slug)
	}
	return l.write(l.out, at, "pr watch stopped project="+slug+" reason="+reason)
}

func (l eventLog) failure(at time.Time, message string) error {
	return l.write(l.err, at, "error: "+message)
}

func (l eventLog) write(writer io.Writer, at time.Time, event string) error {
	if writer == nil {
		return nil
	}
	if _, err := fmt.Fprintf(writer, "%s %s\n", at.UTC().Format(time.RFC3339), event); err != nil {
		return fmt.Errorf("write pr watch event %q: %w", event, err)
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
