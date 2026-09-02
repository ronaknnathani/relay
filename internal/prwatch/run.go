package prwatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// maxConsecutiveErrors stops a watcher that cannot observe GitHub at all, so a
// broken watcher is visibly stopped instead of silently blind.
const maxConsecutiveErrors = 3

// defaultInterval is how often the watcher wakes internally to compare the
// clock with the next scheduled check. Waking is silent; only a scheduled check
// prints an event.
const defaultInterval = 30 * time.Second

// Ticker provides watcher wakeups.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// TickerFactory creates the internal watcher wakeup ticker.
type TickerFactory func(time.Duration) Ticker

// Observer performs one fresh read-only GitHub observation.
type Observer func(ctx context.Context, target Target) (Observation, error)

// Options supplies watcher runtime seams.
type Options struct {
	Mode  Mode
	Owner string
	Now   func() time.Time
	// Ticker creates the internal wakeup ticker. Interval defaults to 30s.
	Ticker   TickerFactory
	Interval time.Duration
	// Locate resolves the project's pull request and the directory gh runs in.
	Locate func(slug string) (Target, error)
	// Observe reads GitHub. It defaults to the gh CLI client.
	Observe Observer
	// Client wakes the owner. A nil client reports every wake as failed.
	Client OwnerClient
	// Out receives routine watcher events and Err receives outcomes that leave
	// attention undelivered. The foreground `relay pr watch run` process
	// supplies its own stdout and stderr, which makes the Herdr watcher pane
	// the watcher's log; nothing is ever written to a file. A nil writer is
	// silent.
	Out io.Writer
	Err io.Writer
	// Location is the zone pane events are stamped in. It defaults to the
	// host's own zone, because the pane is read by whoever is sitting in front
	// of it; only a test pins it, and nothing persisted is affected.
	Location *time.Location
	PID      int
	// TabID and PaneID are the Herdr tab and pane hosting this watcher. They
	// are recorded in runtime state so `relay pr watch stop` closes the exact
	// pane it started and never guesses at one.
	TabID        string
	PaneID       string
	RelayVersion string
}

type wallTicker struct {
	ticker *time.Ticker
}

func (t wallTicker) C() <-chan time.Time { return t.ticker.C }
func (t wallTicker) Stop()               { t.ticker.Stop() }

func normalizedOptions(options Options) Options {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Ticker == nil {
		options.Ticker = func(interval time.Duration) Ticker {
			return wallTicker{ticker: time.NewTicker(interval)}
		}
	}
	if options.Interval <= 0 {
		options.Interval = defaultInterval
	}
	if options.Locate == nil {
		options.Locate = LoadTarget
	}
	if options.Observe == nil {
		options.Observe = func(ctx context.Context, target Target) (Observation, error) {
			return NewClient(NewCLIRunner(0), target.Dir).Observe(ctx, target.PRNumber)
		}
	}
	if options.Mode == "" {
		options.Mode = ModeStandalone
	}
	if options.Location == nil {
		options.Location = time.Local
	}
	if options.PID == 0 {
		options.PID = os.Getpid()
	}
	if options.RelayVersion == "" {
		options.RelayVersion = "dev"
	}
	return options
}

// Tick runs one fresh read-only observation and records its digest. It does not
// take the watcher lock, mutate runtime state, or wake anyone, so it is the
// manual path that works with no Herdr and alongside a running watcher.
func Tick(ctx context.Context, slug string, options Options) (Digest, error) {
	options = normalizedOptions(options)
	target, err := options.Locate(slug)
	if err != nil {
		return Digest{}, err
	}
	state, err := ReadState(slug)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Digest{}, err
	}
	mode := options.Mode
	if state.Mode != "" {
		mode = state.Mode
	}
	observation, err := options.Observe(ctx, target)
	if err != nil {
		return Digest{}, fmt.Errorf("observe pull request #%d for project %q: %w", target.PRNumber, slug, err)
	}
	digest := BuildDigest(slug, mode, observation, options.Now().UTC())
	if digest.Fingerprint != "" {
		if err := WriteDigest(digest); err != nil {
			return Digest{}, err
		}
	}
	return digest, nil
}

// Run holds the watcher singleton lock and observes the project's pull request
// until it reaches a terminal state, the context is canceled, or observation
// fails repeatedly. Every start — first or restart — begins with an immediate
// observation that wakes the owner if the pull request already needs attention,
// because the watcher asserts nothing locally about what an owner has already
// done: only the current remote truth decides.
func Run(ctx context.Context, slug string, options Options) (retErr error) {
	options = normalizedOptions(options)
	target, err := options.Locate(slug)
	if err != nil {
		return err
	}
	owner, err := OwnerSlug(options.Mode, slug, options.Owner)
	if err != nil {
		return err
	}
	lock, err := Acquire(slug)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, lock.Release())
	}()

	events := newEventLog(options.Out, options.Err, options.Location)
	now := options.Now().UTC()
	if _, err := UpdateState(slug, func(state State) (State, error) {
		state.Mode = options.Mode
		state.OwnerSlug = owner
		state.PID = options.PID
		state.TabID = options.TabID
		state.PaneID = options.PaneID
		state.RelayVersion = options.RelayVersion
		state.Status = StatusRunning
		state.StartedAt = now.Format(time.RFC3339)
		state.PRNumber = target.PRNumber
		state.PRURL = target.PRURL
		// Every start restarts the backoff. A restart is an operator action, and
		// resuming a slow cadence would make a freshly started watcher look
		// asleep for up to an hour.
		state.ScheduledChecks = 0
		state.DelaySeconds = int64(FastCadence / time.Second)
		state.NextCheckAt = now.Add(FastCadence).Format(time.RFC3339)
		state.ConsecutiveErrors = 0
		state.Error = ""
		state.Warning = ""
		state.StopReason = ""
		// A restart is the sanctioned way to resume automatic wakes after an
		// uncertain delivery, because the operator has seen the composer.
		state.WakesSuppressed = false
		state.UpdatedAt = now.Format(time.RFC3339)
		return state, nil
	}); err != nil {
		return err
	}
	if err := events.started(now, slug, options.Mode, owner, target.PRNumber); err != nil {
		return recordFailure(slug, now, err)
	}

	runner := &checkRunner{
		slug:    slug,
		owner:   owner,
		target:  target,
		options: options,
		events:  events,
	}
	done, err := runner.run(ctx, true)
	if err != nil || done {
		return err
	}
	ticker := options.Ticker(options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			now := options.Now().UTC()
			if _, err := UpdateState(slug, func(state State) (State, error) {
				state.Status = StatusStopped
				state.StopReason = "context canceled"
				state.NextCheckAt = ""
				state.UpdatedAt = now.Format(time.RFC3339)
				return state, nil
			}); err != nil {
				return err
			}
			return events.stopped(now, slug, "context canceled")
		case <-ticker.C():
			now := options.Now().UTC()
			due, err := runner.due(now)
			if err != nil {
				return errors.Join(err, events.failure(now, err.Error()))
			}
			if !due {
				continue
			}
			done, err := runner.run(ctx, false)
			if err != nil || done {
				return err
			}
		}
	}
}

// checkRunner performs one observation and everything that follows from it.
type checkRunner struct {
	slug    string
	owner   string
	target  Target
	options Options
	events  eventLog
}

// due reports whether the next scheduled check has come around, reading the
// latest persisted state so a schedule another process changed takes effect on
// the next internal wake.
func (r *checkRunner) due(now time.Time) (bool, error) {
	state, err := ReadState(r.slug)
	if err != nil {
		return false, err
	}
	if state.NextCheckAt == "" {
		return true, nil
	}
	next, err := time.Parse(time.RFC3339, state.NextCheckAt)
	if err != nil {
		return false, fmt.Errorf("parse next pr watch check %q: %w", state.NextCheckAt, err)
	}
	return !now.Before(next), nil
}

// run performs one observation. immediate marks the observation a watcher runs
// as soon as it starts, which is not a scheduled check but may still wake the
// owner. It reports whether the watcher should stop.
func (r *checkRunner) run(ctx context.Context, immediate bool) (bool, error) {
	now := r.options.Now().UTC()
	observation, observeErr := r.options.Observe(ctx, r.target)
	if observeErr != nil {
		return r.recordObservationError(now, observeErr)
	}

	digest := BuildDigest(r.slug, r.options.Mode, observation, now)
	if digest.Fingerprint != "" {
		if err := WriteDigest(digest); err != nil {
			return true, errors.Join(err, r.events.failure(now, err.Error()))
		}
	}
	state, schedule, err := r.recordObservation(now, digest, immediate)
	if err != nil {
		return true, err
	}
	label := "start"
	if !immediate {
		label = fmt.Sprintf("check n=%d", state.ScheduledChecks)
	}
	if err := r.events.observation(now, label, digest, schedule.delay); err != nil {
		return true, recordFailure(r.slug, now, err)
	}
	if digest.Complete {
		return true, r.recordComplete(now, digest)
	}
	terminal, err := r.wake(now, schedule, digest)
	if err != nil {
		return true, err
	}
	if terminal {
		return true, nil
	}
	final, err := ReadState(r.slug)
	if err != nil {
		return true, err
	}
	if err := r.events.nextCheck(now, final.NextCheckAt, time.Duration(final.DelaySeconds)*time.Second); err != nil {
		return true, recordFailure(r.slug, now, err)
	}
	if err := Prune(r.slug, MaxRetainedDigests, digest.Fingerprint); err != nil {
		return true, errors.Join(err, r.events.failure(now, err.Error()))
	}
	return false, nil
}

// schedule is what one observation decided about the watcher's cadence.
type schedule struct {
	// delay is the interval before the next scheduled check.
	delay time.Duration
	// consumedChecks is the count this observation would restore the watcher to
	// if its wake never reached the owner, so an undelivered wake spends no
	// backoff step.
	consumedChecks int
}

// recordObservation folds one observation into the runtime record and returns
// what it decided about the cadence. The backoff restarts whenever the pull
// request changed under the watcher: a new head SHA, or a different set of
// actionable items — including attention appearing or clearing entirely. A
// pending check that is still pending changes neither, so it never resets.
func (r *checkRunner) recordObservation(
	now time.Time, digest Digest, immediate bool,
) (State, schedule, error) {
	var next schedule
	state, err := UpdateState(r.slug, func(state State) (State, error) {
		next.consumedChecks = state.ScheduledChecks
		switch {
		case immediate:
		case state.HeadSHA != digest.HeadSHA, state.CurrentFingerprint != digest.Fingerprint:
			state.ScheduledChecks = 0
			next.consumedChecks = 0
		default:
			state.ScheduledChecks++
		}
		delay := CadenceFor(state.ScheduledChecks + 1)
		next.delay = delay
		state.Status = StatusRunning
		state.LastCheckAt = now.Format(time.RFC3339)
		state.NextCheckAt = now.Add(delay).Format(time.RFC3339)
		state.DelaySeconds = int64(delay / time.Second)
		state.HeadSHA = digest.HeadSHA
		state.PRState = digest.PR.State
		state.PRNumber = digest.PR.Number
		state.CurrentFingerprint = digest.Fingerprint
		state.ActionableCount = len(digest.Items)
		state.AttentionPending = len(digest.Items) > 0
		state.ConsecutiveErrors = 0
		state.Error = ""
		state.UpdatedAt = now.Format(time.RFC3339)
		return state, nil
	})
	return state, next, err
}

// wake hands actionable attention to the exact owner session and reports
// whether the watcher reached a terminal state. Immediately before prompting it
// re-reads the runtime record under the state lock, so a wake is never
// delivered for an observation that is no longer current.
func (r *checkRunner) wake(now time.Time, observed schedule, digest Digest) (bool, error) {
	if len(digest.Items) == 0 {
		return false, nil
	}
	current, err := ReadStateLocked(r.slug)
	if err != nil {
		return false, err
	}
	if !wakeStillCurrent(current, digest) {
		return false, r.events.wakeSkipped(now, r.owner, digest.Fingerprint)
	}
	outcome := WakeOutcome{Kind: WakeSuppressed, Owner: r.owner}
	if !current.WakesSuppressed {
		outcome = Wake(r.options.Client, r.owner, r.slug, digest.Fingerprint)
	}
	// An owner that was not there to take the attention must not consume a
	// backoff step: the pull request did not get quieter, the delivery failed.
	hold := holdsFastCadence(outcome.Kind)
	updated, err := UpdateState(r.slug, func(next State) (State, error) {
		next.LastWakeAt = now.Format(time.RFC3339)
		next.LastWakeStatus = string(outcome.Kind)
		next.LastWakeFingerprint = digest.Fingerprint
		next.AttentionPending = !outcome.Delivered()
		next.Warning = outcome.Error
		if outcome.Kind == WakeUncertain {
			next.WakesSuppressed = true
		}
		if hold {
			next.ScheduledChecks = observed.consumedChecks
			next.DelaySeconds = int64(FastCadence / time.Second)
			next.NextCheckAt = now.Add(FastCadence).Format(time.RFC3339)
		}
		return next, nil
	})
	if err != nil {
		return false, err
	}
	if err := r.events.wake(now, r.slug, outcome, digest.Fingerprint); err != nil {
		return false, err
	}
	if outcome.Delivered() && digest.PR.State == "CLOSED" {
		return true, r.recordClosed(now, updated, digest)
	}
	return false, nil
}

// wakeStillCurrent reports whether the digest a wake was built from is still
// the watcher's current attention. A watcher that stopped, completed, or moved
// on to another observation has nothing to hand over.
func wakeStillCurrent(state State, digest Digest) bool {
	return state.Status == StatusRunning &&
		state.AttentionPending &&
		state.CurrentFingerprint == digest.Fingerprint
}

// holdsFastCadence reports whether an undelivered wake should hold the fast
// cadence instead of consuming a backoff step. A missing, duplicated, busy, or
// failed owner may be there on the next check; a suppressed or uncertain
// delivery needs an operator and a restart, so it backs off normally.
func holdsFastCadence(kind WakeKind) bool {
	switch kind {
	case WakeOwnerMissing, WakeOwnerDuplicated, WakeOwnerBusy, WakeFailed:
		return true
	}
	return false
}

func (r *checkRunner) recordComplete(now time.Time, digest Digest) error {
	if _, err := UpdateState(r.slug, func(state State) (State, error) {
		state.Status = StatusComplete
		state.StopReason = "pull request merged"
		state.NextCheckAt = ""
		state.AttentionPending = false
		state.UpdatedAt = now.Format(time.RFC3339)
		return state, nil
	}); err != nil {
		return err
	}
	return r.events.complete(now, r.slug, digest.PR.Number)
}

// recordClosed ends a watch on a pull request that was closed without merging,
// once its owner has actually been handed the escalation. The watcher cannot
// act on a closed pull request, and leaving one running would be a process that
// never finishes.
func (r *checkRunner) recordClosed(now time.Time, state State, digest Digest) error {
	if _, err := UpdateState(r.slug, func(next State) (State, error) {
		next.Status = StatusComplete
		next.StopReason = "pull request closed without merging; escalation delivered to " + state.OwnerSlug
		next.NextCheckAt = ""
		next.AttentionPending = false
		next.UpdatedAt = now.Format(time.RFC3339)
		return next, nil
	}); err != nil {
		return err
	}
	return r.events.closed(now, r.slug, digest.PR.Number, r.owner)
}

// recordObservationError treats a GitHub failure as an error, never as a quiet
// no-action. The watcher retries at the fast cadence and stops after three
// consecutive failures so a blind watcher is visible.
func (r *checkRunner) recordObservationError(now time.Time, cause error) (bool, error) {
	wrapped := fmt.Errorf(
		"observe pull request #%d for project %q: %w", r.target.PRNumber, r.slug, cause,
	)
	state, err := UpdateState(r.slug, func(state State) (State, error) {
		state.ConsecutiveErrors++
		state.Error = wrapped.Error()
		state.DelaySeconds = int64(FastCadence / time.Second)
		state.NextCheckAt = now.Add(FastCadence).Format(time.RFC3339)
		state.UpdatedAt = now.Format(time.RFC3339)
		if state.ConsecutiveErrors >= maxConsecutiveErrors {
			state.Status = StatusFailed
			state.NextCheckAt = ""
		}
		return state, nil
	})
	if err != nil {
		return true, errors.Join(wrapped, err)
	}
	if state.Status == StatusFailed {
		failure := r.events.failure(now, fmt.Sprintf(
			"pr watch failed project=%s after %d consecutive observation errors: %v",
			r.slug, state.ConsecutiveErrors, cause,
		))
		return true, errors.Join(wrapped, failure)
	}
	return false, r.events.failure(now, fmt.Sprintf(
		"%v; retrying in %s", wrapped, cadenceLabel(FastCadence),
	))
}

// recordFailure records why a watcher stopped when its own events could not be
// written. The pane is gone, so the reason has to survive in runtime state.
func recordFailure(slug string, now time.Time, cause error) error {
	_, err := UpdateState(slug, func(state State) (State, error) {
		state.Status = StatusFailed
		state.Error = cause.Error()
		state.UpdatedAt = now.Format(time.RFC3339)
		return state, nil
	})
	return errors.Join(cause, err)
}
