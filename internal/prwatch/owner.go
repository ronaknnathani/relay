package prwatch

import (
	"errors"
	"fmt"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/project"
)

// OwnerClient is the Herdr surface the watcher uses to wake an owner.
type OwnerClient interface {
	Agents() ([]herdr.Agent, error)
	PromptAgent(target, text string) error
}

// WakeKind is the outcome of one owner wake attempt.
type WakeKind string

// Wake outcomes. Only WakeDelivered handed the attention over; every other
// outcome leaves it pending until the next scheduled check.
const (
	WakeDelivered       WakeKind = "delivered"
	WakeSuppressed      WakeKind = "suppressed"
	WakeOwnerMissing    WakeKind = "owner-missing"
	WakeOwnerDuplicated WakeKind = "owner-duplicated"
	WakeOwnerBusy       WakeKind = "owner-busy"
	WakeFailed          WakeKind = "failed"
	// WakeUncertain means Herdr may have staged the prompt without submitting
	// it. Retrying can duplicate text in the owner's composer, so automatic
	// wakes stop until the watcher is restarted.
	WakeUncertain WakeKind = "uncertain"
)

// WakeOutcome records one owner wake attempt without carrying any payload.
type WakeOutcome struct {
	Kind   WakeKind     `json:"kind"`
	Owner  string       `json:"owner"`
	PaneID string       `json:"pane_id,omitempty"`
	Panes  []string     `json:"panes,omitempty"`
	Status herdr.Status `json:"status,omitempty"`
	Error  string       `json:"error,omitempty"`
}

// Delivered reports whether the attention reached its owner.
func (o WakeOutcome) Delivered() bool { return o.Kind == WakeDelivered }

// OwnerSlug returns the session slug whose live pane the watcher wakes.
// Standalone and managed projects are owned by the project's own session — a
// managed program's worker, never its tech lead — so an owner other than the
// project is rejected rather than silently accepted. A stack watcher must name
// the orchestrator explicitly, because the front project's session is not the
// one that advances the stack.
func OwnerSlug(mode Mode, projectSlug, owner string) (string, error) {
	if err := project.ValidateSlug(projectSlug); err != nil {
		return "", fmt.Errorf("pr watch project slug: %w", err)
	}
	if owner != "" {
		if err := project.ValidateSlug(owner); err != nil {
			return "", fmt.Errorf("pr watch owner slug: %w", err)
		}
	}
	if mode == ModeStack {
		if owner == "" {
			return "", fmt.Errorf(
				"pr watch mode stack requires --owner <stack-orchestrator-slug>: "+
					"the front project %q is watched on the orchestrator's behalf", projectSlug,
			)
		}
		return owner, nil
	}
	if owner != "" && owner != projectSlug {
		return "", fmt.Errorf(
			"pr watch mode %s owns project %q, so --owner %q is not a valid owner; "+
				"use --mode stack --owner %s to watch on another session's behalf",
			mode, projectSlug, owner, owner,
		)
	}
	return projectSlug, nil
}

// WakePrompt is the payload-free instruction the owner receives. It names the
// project and the digest to read; every body stays in the mode 0600 digest.
func WakePrompt(projectSlug, fingerprint string) string {
	return fmt.Sprintf(
		"Run pr-monitor once for project %s using watcher fingerprint %s.", projectSlug, fingerprint,
	)
}

// Wake prompts the exact live owner session. It never focuses a pane, never
// creates one, and never retries an uncertain delivery.
func Wake(client OwnerClient, ownerSlug, projectSlug, fingerprint string) WakeOutcome {
	outcome := WakeOutcome{Owner: ownerSlug}
	if client == nil {
		outcome.Kind = WakeFailed
		outcome.Error = "pr watch has no Herdr client"
		return outcome
	}
	agents, err := client.Agents()
	if err != nil {
		outcome.Kind = WakeFailed
		outcome.Error = fmt.Errorf("list Herdr agents for pr watch owner %q: %w", ownerSlug, err).Error()
		return outcome
	}
	owner, err := herdr.FindLiveProjectOwner(agents, ownerSlug)
	if err != nil {
		var duplicate *herdr.DuplicateProjectOwnerError
		switch {
		case errors.As(err, &duplicate):
			outcome.Kind = WakeOwnerDuplicated
			outcome.Panes = duplicate.PaneIDs
		default:
			outcome.Kind = WakeOwnerMissing
		}
		outcome.Error = err.Error()
		return outcome
	}
	outcome.PaneID = owner.PaneID
	outcome.Status = owner.Status
	switch owner.Status {
	case herdr.StatusIdle, herdr.StatusDone:
	default:
		outcome.Kind = WakeOwnerBusy
		outcome.Error = fmt.Sprintf(
			"owner %q is %s; attention stays pending until the next scheduled check",
			ownerSlug, owner.Status,
		)
		return outcome
	}
	if err := client.PromptAgent(owner.PaneID, WakePrompt(projectSlug, fingerprint)); err != nil {
		outcome.Kind = WakeFailed
		if errors.Is(err, herdr.ErrPromptDeliveryUncertain) {
			outcome.Kind = WakeUncertain
		}
		outcome.Error = err.Error()
		return outcome
	}
	outcome.Kind = WakeDelivered
	return outcome
}
