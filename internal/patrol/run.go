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
	if err := WriteState(state); err != nil {
		return err
	}

	lastErrorClass := ""
	runTick := func() (bool, error) {
		tickNow := options.Now().UTC()
		observation, errorClass, tickErr := safeTick(slug, options)
		notificationWarning := ""
		state.LastTickAt = tickNow.Format(time.RFC3339)
		state.UpdatedAt = state.LastTickAt
		if tickErr == nil {
			state.Reasons = nonNilReasons(observation.Reasons)
			state.DelaySeconds = observation.DelaySeconds
			state.StopReason = observation.StopReason
			if observation.Stop {
				state.Status = StatusStopped
				state.NextTickAt = ""
				state.Error = ""
				state.ConsecutiveErrors = 0
				return true, WriteState(state)
			}
			if observation.AttentionFingerprint == "" {
				state.AttentionFingerprint = ""
				state.LastNotifiedAt = ""
				state.LastTurnFingerprint = ""
				state.TurnFailures = 0
			}
			agents, agentsErr := listAgents(options.Agents)
			if agentsErr != nil {
				state.TLPresent = false
				notificationWarning = agentsErr.Error()
			} else {
				notificationWarning = requestTLTurn(
					ctx, &state, observation, agents, options.Turns, options.Notifier, tickNow,
				).Warning
			}
		}
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
			if state.ConsecutiveErrors >= 3 {
				state.Status = StatusFailed
				if err := WriteState(state); err != nil {
					return true, errors.Join(tickErr, err)
				}
				return true, tickErr
			}
			state.Status = StatusRunning
			return false, WriteState(state)
		}
		lastErrorClass = ""
		state.ConsecutiveErrors = 0
		state.Error = ""
		state.Warning = notificationWarning
		state.StopReason = ""
		state.Status = StatusRunning
		state.NextTickAt = tickNow.Add(time.Duration(state.DelaySeconds) * time.Second).Format(time.RFC3339)
		return false, WriteState(state)
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
			return WriteState(state)
		case <-ticker.C():
			now := options.Now().UTC()
			next, err := time.Parse(time.RFC3339, state.NextTickAt)
			if err != nil {
				return fmt.Errorf("parse next patrol tick %q: %w", state.NextTickAt, err)
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

func normalizedRunOptions(options Options) Options {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Ticker == nil {
		options.Ticker = func(interval time.Duration) Ticker {
			return wallTicker{ticker: time.NewTicker(interval)}
		}
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
