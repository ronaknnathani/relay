package patrol

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
)

// rearmInterval rings the live CTO again for unchanged attention without
// turning every patrol observation into a prompt.
const rearmInterval = 2 * time.Hour

// turnFailureLimit suppresses automatic doorbells after this many consecutive
// failures. Suppression clears when attention changes or the patrol restarts.
const turnFailureLimit = 3

// TurnStatus is the outcome of one live CTO doorbell attempt.
type TurnStatus string

// Live CTO doorbell outcomes.
const (
	TurnSucceeded TurnStatus = "succeeded"
	TurnFailed    TurnStatus = "failed"
	TurnTimedOut  TurnStatus = "timed-out"
	TurnSkipped   TurnStatus = "skipped"
	TurnUncertain TurnStatus = "uncertain"
)

// TurnRequest describes the live CTO doorbell the patrol wants delivered.
type TurnRequest struct {
	ProgramSlug string
	PaneID      string
	Fingerprint string
	Reasons     []Reason
}

// TurnResult summarizes one live CTO doorbell attempt.
type TurnResult struct {
	Status    TurnStatus
	SessionID string
	LogPath   string
	StartedAt string
	EndedAt   string
	Reason    string
	Error     string
}

// TurnRunner delivers one prompt to the existing CEO-facing CTO session.
type TurnRunner interface {
	RunTurn(ctx context.Context, request TurnRequest) (TurnResult, error)
}

// Notifier optionally raises a desktop notification after a doorbell attempt.
// Correctness never depends on it: every notifier error is ignored.
type Notifier interface {
	ShowNotification(title, body string) error
}

// requestCTOTurn rings the live CTO at most once for the current attention. It
// returns a warning string instead of an error because a delivery
// problem must degrade the patrol, never stop it.
func requestCTOTurn(
	ctx context.Context,
	state *State,
	observation Observation,
	agents []herdr.Agent,
	runner TurnRunner,
	notifier Notifier,
	now time.Time,
) string {
	cto, ctoErr := herdr.FindLiveCTO(agents, observation.ProgramSlug)
	// CTO presence means exactly one identified owner. Zero owners and two
	// rival owners are both reported as absent, because neither state gives the
	// patrol a CTO it may act beside.
	state.CTOPresent = ctoErr == nil
	if observation.AttentionFingerprint == "" {
		state.AttentionFingerprint = ""
		state.LastNotifiedAt = ""
		state.LastTurnFingerprint = ""
		state.TurnFailures = 0
		return ""
	}
	if state.DoorbellSuppressed {
		return fmt.Sprintf(
			"live CTO doorbells for program %q are suppressed after an unconfirmed delivery; "+
				"inspect and clear the CTO composer, then restart the patrol",
			observation.ProgramSlug,
		)
	}
	if ctoErr != nil {
		if errors.Is(ctoErr, herdr.ErrNoLiveCTO) {
			return ""
		}
		// Ambiguous CTO ownership is not a silent skip: the CEO has to close the
		// duplicate pane before automation can safely act on this program.
		return fmt.Sprintf(
			"skipped the live CTO doorbell for program %q: %s",
			observation.ProgramSlug, ctoErr,
		)
	}
	switch cto.Status {
	case herdr.StatusIdle, herdr.StatusDone:
	default:
		return ""
	}

	// The failure budget is counted against the attention that was attempted,
	// not against the armed fingerprint: a failed turn deliberately leaves the
	// fingerprint unarmed so the next tick retries the same attention.
	if observation.AttentionFingerprint != state.LastTurnFingerprint {
		state.TurnFailures = 0
	}
	changed := observation.AttentionFingerprint != state.AttentionFingerprint
	if !changed && !rearmed(state.LastNotifiedAt, now) {
		return ""
	}
	if state.TurnFailures >= turnFailureLimit {
		return fmt.Sprintf(
			"live CTO doorbells for program %q are suppressed after %d consecutive failures (%s); "+
				"they resume when attention changes or after `relay program patrol stop`/`start`",
			observation.ProgramSlug, state.TurnFailures, lastTurnDetail(state),
		)
	}
	if runner == nil {
		return fmt.Sprintf(
			"ring the live CTO for program %q: no doorbell runner is configured",
			observation.ProgramSlug,
		)
	}

	result, err := runner.RunTurn(ctx, TurnRequest{
		ProgramSlug: observation.ProgramSlug,
		PaneID:      cto.PaneID,
		Fingerprint: observation.AttentionFingerprint,
		Reasons:     nonNilReasons(observation.Reasons),
	})
	if err != nil {
		result.Status = TurnFailed
		if result.Error == "" {
			result.Error = err.Error()
		}
	}
	state.LastTurnFingerprint = observation.AttentionFingerprint
	recordTurn(state, result)
	notify(notifier, observation.ProgramSlug, result)

	switch result.Status {
	case TurnSucceeded:
		state.AttentionFingerprint = observation.AttentionFingerprint
		state.LastNotifiedAt = now.UTC().Format(time.RFC3339)
		state.TurnFailures = 0
		state.DoorbellSuppressed = false
		return ""
	case TurnUncertain:
		state.DoorbellSuppressed = true
		return fmt.Sprintf(
			"live CTO doorbell for program %q was not confirmed: %s; "+
				"automatic retries are suppressed until the CTO composer is inspected and the patrol is restarted",
			observation.ProgramSlug, turnError(result),
		)
	case TurnSkipped:
		return fmt.Sprintf(
			"live CTO doorbell for program %q was skipped: %s",
			observation.ProgramSlug, skipReason(result),
		)
	default:
		state.TurnFailures++
		warning := fmt.Sprintf(
			"live CTO doorbell for program %q %s: %s",
			observation.ProgramSlug, result.Status, turnError(result),
		)
		if state.TurnFailures >= turnFailureLimit {
			warning += fmt.Sprintf(
				"; automatic doorbells are now suppressed after %d consecutive failures "+
					"until attention changes or the patrol is restarted",
				state.TurnFailures,
			)
		}
		return warning
	}
}

func rearmed(lastNotifiedAt string, now time.Time) bool {
	last, err := time.Parse(time.RFC3339, lastNotifiedAt)
	if err != nil {
		return true
	}
	return !now.Before(last.Add(rearmInterval))
}

func recordTurn(state *State, result TurnResult) {
	status := result.Status
	if status == "" {
		status = TurnFailed
	}
	state.LastTurnStatus = string(status)
	state.LastTurnSessionID = result.SessionID
	state.LastTurnLogPath = result.LogPath
	state.LastTurnStartedAt = result.StartedAt
	state.LastTurnEndedAt = result.EndedAt
	switch status {
	case TurnSucceeded:
		state.LastTurnError = ""
	case TurnSkipped:
		state.LastTurnError = skipReason(result)
	default:
		state.LastTurnError = turnError(result)
	}
}

func notify(notifier Notifier, slug string, result TurnResult) {
	if notifier == nil {
		return
	}
	body := fmt.Sprintf("Live CTO doorbell for %s %s.", slug, result.Status)
	if detail := turnError(result); detail != "" && result.Status != TurnSucceeded {
		body += " " + detail
	}
	// Best effort only: a missing desktop session must not affect the turn.
	_ = notifier.ShowNotification("Relay program needs attention", body)
}

func turnError(result TurnResult) string {
	if result.Error != "" {
		return result.Error
	}
	if result.Reason != "" {
		return result.Reason
	}
	return "no detail was reported"
}

func skipReason(result TurnResult) string {
	if result.Reason != "" {
		return result.Reason
	}
	return "no reason was reported"
}

func lastTurnDetail(state *State) string {
	if state.LastTurnError != "" {
		return state.LastTurnError
	}
	return "no detail was recorded"
}
