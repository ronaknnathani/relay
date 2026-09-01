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
	Out          io.Writer
	Err          io.Writer
	PID          int
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
	digest := BuildDigest(slug, mode, observation, state, options.Now().UTC())
	if digest.Fingerprint != "" {
		if err := WriteDigest(digest); err != nil {
			return Digest{}, err
		}
	}
	return digest, nil
}

// Run holds the watcher singleton lock and observes the project's pull request
// until it merges, the context is canceled, or observation fails repeatedly.
// The first-ever start records a baseline without waking anyone; a restart may
// wake for attention that is still unacknowledged.
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

	events := newEventLog(options.Out, options.Err)
	now := options.Now().UTC()
	state, err := UpdateState(slug, func(state State) (State, error) {
		state.Mode = options.Mode
		state.OwnerSlug = owner
		state.PID = options.PID
		state.RelayVersion = options.RelayVersion
		state.Status = StatusRunning
		state.StartedAt = now.Format(time.RFC3339)
		state.PRNumber = target.PRNumber
		state.PRURL = target.PRURL
		state.ConsecutiveErrors = 0
		state.Error = ""
		state.Warning = ""
		state.StopReason = ""
		// A restart is the sanctioned way to resume automatic wakes after an
		// uncertain delivery, because the operator has seen the composer.
		state.WakesSuppressed = false
		state.UpdatedAt = now.Format(time.RFC3339)
		return state, nil
	})
	if err != nil {
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
	done, err := runner.run(ctx, !state.Baselined)
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
// latest persisted state so an acknowledgement recorded by another process
// takes effect on the next internal wake.
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

// run performs one observation. baseline marks the first-ever observation,
// which records attention without waking anyone. It reports whether the
// watcher should stop.
func (r *checkRunner) run(ctx context.Context, baseline bool) (bool, error) {
	now := r.options.Now().UTC()
	observation, observeErr := r.options.Observe(ctx, r.target)
	if observeErr != nil {
		return r.recordObservationError(now, observeErr)
	}

	var digest Digest
	var delay time.Duration
	state, err := UpdateState(r.slug, func(state State) (State, error) {
		digest = BuildDigest(r.slug, r.options.Mode, observation, state, now)
		if !baseline {
			state.ScheduledChecks++
		}
		// A new head means the pull request changed under the watcher, so the
		// backoff restarts at the fast cadence.
		if state.HeadSHA != "" && state.HeadSHA != digest.HeadSHA {
			state.ScheduledChecks = 0
		}
		delay = CadenceFor(state.ScheduledChecks + 1)
		state.Baselined = true
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
	if err != nil {
		return true, err
	}
	if digest.Fingerprint != "" {
		if err := WriteDigest(digest); err != nil {
			return true, errors.Join(err, r.events.failure(now, err.Error()))
		}
	}
	label := "baseline"
	if !baseline {
		label = fmt.Sprintf("check n=%d", state.ScheduledChecks)
	}
	if err := r.events.observation(now, label, digest, delay); err != nil {
		return true, recordFailure(r.slug, now, err)
	}
	if digest.Complete {
		return true, r.recordComplete(now, digest)
	}
	if err := r.wake(now, state, digest, baseline); err != nil {
		return true, err
	}
	if err := r.events.nextCheck(now, state.NextCheckAt, delay); err != nil {
		return true, recordFailure(r.slug, now, err)
	}
	if err := Prune(r.slug, MaxRetainedDigests, MaxRetainedAcknowledgements, digest.Fingerprint); err != nil {
		return true, errors.Join(err, r.events.failure(now, err.Error()))
	}
	return false, nil
}

// wake hands actionable attention to the exact owner session, unless this is
// the first-ever baseline or a previous uncertain delivery suppressed wakes.
func (r *checkRunner) wake(now time.Time, state State, digest Digest, baseline bool) error {
	if len(digest.Items) == 0 {
		return nil
	}
	if baseline {
		return r.events.baselineHeld(now, r.owner)
	}
	outcome := WakeOutcome{Kind: WakeSuppressed, Owner: r.owner}
	if !state.WakesSuppressed {
		outcome = Wake(r.options.Client, r.owner, r.slug, digest.Fingerprint)
	}
	if _, err := UpdateState(r.slug, func(state State) (State, error) {
		state.LastWakeAt = now.Format(time.RFC3339)
		state.LastWakeStatus = string(outcome.Kind)
		state.LastWakeFingerprint = digest.Fingerprint
		state.AttentionPending = !outcome.Delivered()
		state.Warning = outcome.Error
		if outcome.Kind == WakeUncertain {
			state.WakesSuppressed = true
		}
		state.UpdatedAt = now.Format(time.RFC3339)
		return state, nil
	}); err != nil {
		return err
	}
	return r.events.wake(now, r.slug, outcome, digest.Fingerprint)
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
