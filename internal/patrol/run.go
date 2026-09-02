package patrol

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/programview"
)

type wallTicker struct {
	ticker *time.Ticker
}

func (t wallTicker) C() <-chan time.Time { return t.ticker.C }
func (t wallTicker) Stop()               { t.ticker.Stop() }

// Run holds the patrol singleton lock and observes until the program stops,
// the context is canceled, or three consecutive same-class errors occur.
func Run(ctx context.Context, slug string, options Options) (retErr error) {
	lock, err := Acquire(slug)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, lock.Release())
	}()

	options = normalizedRunOptions(options)
	events := newEventLog(options.Out, options.Err, options.Location)
	now := options.Now().UTC()
	state := State{
		Schema:       SchemaVersion,
		Version:      1,
		ProgramSlug:  slug,
		PID:          options.PID,
		RelayVersion: options.RelayVersion,
		Status:       StatusRunning,
		StartedAt:    now.Format(time.RFC3339),
		Reasons:      []Reason{},
		UpdatedAt:    now.Format(time.RFC3339),
	}
	if err := events.started(now, slug); err != nil {
		return failedRunState(events, &state, now, err)
	}
	if err := writeRunState(events, state, now); err != nil {
		return err
	}

	lastErrorClass := ""
	runTick := func() (bool, error) {
		tickNow := options.Now().UTC()
		observation, errorClass, tickErr := safeTick(slug, options)
		notificationWarning := ""
		state.LastTickAt = tickNow.Format(time.RFC3339)
		state.UpdatedAt = state.LastTickAt
		if tickErr != nil {
			state.Warning = ""
			if errorClass == lastErrorClass {
				state.ConsecutiveErrors++
			} else {
				lastErrorClass = errorClass
				state.ConsecutiveErrors = 1
			}
			state.Error = tickErr.Error()
			state.DelaySeconds = int64(attentionDelay / time.Second)
			state.NextTickAt = tickNow.Add(attentionDelay).Format(time.RFC3339)
			if err := events.retry(tickNow, fmt.Sprintf(
				"patrol observation failed: %v", tickErr,
			), state.DelaySeconds, state.NextTickAt); err != nil {
				return true, failedRunState(events, &state, tickNow, err)
			}
			if state.ConsecutiveErrors >= 3 {
				state.Status = StatusFailed
				failure := events.failure(tickNow, fmt.Sprintf(
					"patrol failed program=%s after %d consecutive errors", slug, state.ConsecutiveErrors,
				))
				return true, errors.Join(tickErr, failure, writeRunState(events, state, tickNow))
			}
			state.Status = StatusRunning
			return false, writeRunState(events, state, tickNow)
		}

		state.Reasons = nonNilReasons(observation.Reasons)
		state.DelaySeconds = observation.DelaySeconds
		state.StopReason = observation.StopReason
		if observation.Stop {
			state.Status = StatusStopped
			state.NextTickAt = ""
			state.Error = ""
			state.ConsecutiveErrors = 0
			if err := events.stopped(tickNow, slug, observation.StopReason); err != nil {
				return true, failedRunState(events, &state, tickNow, err)
			}
			return true, writeRunState(events, state, tickNow)
		}
		if observation.AttentionFingerprint == "" {
			state.AttentionFingerprint = ""
			state.LastNotifiedAt = ""
			state.LastTurnFingerprint = ""
			state.TurnFailures = 0
		}
		// The wake runs before anything is printed, because an undelivered wake
		// is part of what this tick decided. Only once the record is final does
		// the tick line quote a next tick a reader can trust.
		presenceFailure := ""
		woke := false
		wakeOutcome := turnOutcome{}
		agents, agentsErr := listAgents(options.Agents)
		if agentsErr != nil {
			state.TLPresent = false
			notificationWarning = agentsErr.Error()
			presenceFailure = fmt.Sprintf("%v; tech lead presence is unknown this tick", agentsErr)
		} else {
			wakeOutcome = requestTLTurn(
				ctx, &state, observation, agents, options.Turns, options.Notifier, tickNow,
			)
			notificationWarning = wakeOutcome.Warning
			woke = true
		}
		lastErrorClass = ""
		state.ConsecutiveErrors = 0
		state.Error = ""
		state.Warning = notificationWarning
		state.StopReason = ""
		state.Status = StatusRunning
		state.NextTickAt = tickNow.Add(time.Duration(state.DelaySeconds) * time.Second).Format(time.RFC3339)
		if err := events.tick(
			tickNow, observation.Reasons, state.DelaySeconds, state.NextTickAt,
		); err != nil {
			return true, failedRunState(events, &state, tickNow, err)
		}
		if presenceFailure != "" {
			if err := events.failure(tickNow, presenceFailure); err != nil {
				return true, failedRunState(events, &state, tickNow, err)
			}
		}
		if woke {
			if err := events.wake(tickNow, slug, wakeOutcome); err != nil {
				return true, failedRunState(events, &state, tickNow, err)
			}
		}
		return false, writeRunState(events, state, tickNow)
	}

	done, err := runTick()
	if err != nil || done {
		return err
	}
	ticker := options.Ticker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			now := options.Now().UTC()
			state.Status = StatusStopped
			state.StopReason = "context canceled"
			state.NextTickAt = ""
			state.Error = ""
			state.UpdatedAt = now.Format(time.RFC3339)
			return errors.Join(
				events.stopped(now, slug, state.StopReason), writeRunState(events, state, now),
			)
		case <-ticker.C():
			now := options.Now().UTC()
			next, err := time.Parse(time.RFC3339, state.NextTickAt)
			if err != nil {
				parseErr := fmt.Errorf("parse next patrol tick %q: %w", state.NextTickAt, err)
				return errors.Join(parseErr, events.failure(now, parseErr.Error()))
			}
			if now.Before(next) {
				continue
			}
			done, err := runTick()
			if err != nil || done {
				return err
			}
		}
	}
}

// writeRunState records runtime state and reports a failure to write it on
// stderr, because a patrol whose state file is stale silently misleads both
// `relay program patrol status` and the Program UI.
func writeRunState(events eventLog, state State, at time.Time) error {
	err := WriteState(state)
	if err == nil {
		return nil
	}
	return errors.Join(err, events.failure(at, fmt.Sprintf("write patrol runtime state: %v", err)))
}

// failedRunState records why a patrol stopped when its own events could not be
// written. The pane is gone, so the reason has to survive in runtime state.
func failedRunState(events eventLog, state *State, at time.Time, cause error) error {
	state.Status = StatusFailed
	state.Error = cause.Error()
	state.UpdatedAt = at.Format(time.RFC3339)
	return errors.Join(cause, writeRunState(events, *state, at))
}

func normalizedRunOptions(options Options) Options {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Ticker == nil {
		options.Ticker = func(interval time.Duration) Ticker {
			return wallTicker{ticker: time.NewTicker(interval)}
		}
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
	if options.Agents == nil {
		options.Agents = programview.NewHerdrAgentLister()
	}
	return options
}

func safeTick(slug string, options Options) (observation Observation, errorClass string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			observation = Observation{}
			errorClass = "panic"
			err = fmt.Errorf("patrol tick panic: %v", recovered)
		}
	}()
	observation, err = Tick(slug, options)
	if err != nil {
		return Observation{}, "observation", err
	}
	return observation, "", nil
}

func listAgents(lister programview.AgentLister) ([]herdr.Agent, error) {
	if lister == nil {
		return []herdr.Agent{}, nil
	}
	agents, err := lister.Agents()
	if err != nil {
		return nil, fmt.Errorf("list Herdr agents for patrol: %w", err)
	}
	if agents == nil {
		return []herdr.Agent{}, nil
	}
	return agents, nil
}
