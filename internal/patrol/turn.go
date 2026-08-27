package patrol

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
)

// rearmInterval re-runs a bounded turn for unchanged attention, so a CTO turn
// that was interrupted or inconclusive is retried without spinning.
const rearmInterval = 2 * time.Hour

// turnFailureLimit suppresses automatic turns after this many consecutive
// failures. Suppression clears when attention changes or the patrol restarts.
const turnFailureLimit = 3

// TurnStatus is the outcome of one bounded automated CTO turn.
type TurnStatus string

// Bounded turn outcomes.
const (
	TurnSucceeded TurnStatus = "succeeded"
	TurnFailed    TurnStatus = "failed"
	TurnTimedOut  TurnStatus = "timed-out"
	TurnSkipped   TurnStatus = "skipped"
)

// TurnRequest describes the bounded automated CTO turn the patrol wants run.
type TurnRequest struct {
	ProgramSlug string
	Fingerprint string
	Reasons     []Reason
}

// TurnResult summarizes what one bounded automated CTO turn did.
type TurnResult struct {
	Status    TurnStatus
	SessionID string
	LogPath   string
	StartedAt string
	EndedAt   string
	Reason    string
	Error     string
}

// TurnRunner starts exactly one fresh, bounded, same-role CTO session that
// reconstructs durable state and exits. It never resumes the live CEO-facing
// session and never sends keystrokes to a Herdr pane.
type TurnRunner interface {
	RunTurn(ctx context.Context, request TurnRequest) (TurnResult, error)
}

// Notifier optionally raises a desktop notification after a bounded turn.
// Correctness never depends on it: every notifier error is ignored.
type Notifier interface {
	ShowNotification(title, body string) error
}

// requestCTOTurn runs at most one bounded automated CTO turn for the current
// attention. It returns a warning string instead of an error because a turn
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
	if ctoErr != nil {
		if errors.Is(ctoErr, herdr.ErrNoLiveCTO) {
			return ""
		}
		// Ambiguous CTO ownership is not a silent skip: the CEO has to close the
		// duplicate pane before automation can safely act on this program.
		return fmt.Sprintf(
			"skipped the bounded CTO turn for program %q: %s",
			observation.ProgramSlug, ctoErr,
		)
	}
	// Herdr status is used only to confirm the CEO-facing CTO exists and is not
	// mid-turn. The pane is never focused or typed into.
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
			"bounded CTO turns for program %q are suppressed after %d consecutive failures (%s); "+
				"they resume when attention changes or after `relay program patrol stop`/`start`",
			observation.ProgramSlug, state.TurnFailures, lastTurnDetail(state),
		)
	}
	if runner == nil {
		return fmt.Sprintf(
			"run a bounded CTO turn for program %q: no turn runner is configured",
			observation.ProgramSlug,
		)
	}

	result, err := runner.RunTurn(ctx, TurnRequest{
		ProgramSlug: observation.ProgramSlug,
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
		return ""
	case TurnSkipped:
		// A skip is a legitimate outcome, not a failure: another writer held the
		// lock or the CTO stopped being idle between the gate and the turn.
		return fmt.Sprintf(
			"bounded CTO turn for program %q was skipped: %s",
			observation.ProgramSlug, skipReason(result),
		)
	default:
		state.TurnFailures++
		warning := fmt.Sprintf(
			"bounded CTO turn for program %q %s: %s",
			observation.ProgramSlug, result.Status, turnError(result),
		)
		if state.TurnFailures >= turnFailureLimit {
			warning += fmt.Sprintf(
				"; automatic turns are now suppressed after %d consecutive failures "+
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
	body := fmt.Sprintf("Bounded CTO turn for %s %s.", slug, result.Status)
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
