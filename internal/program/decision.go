package program

import (
	"fmt"
	"sort"
	"strings"
)

// OpenDecision appends an unresolved decision and assigns the next max+1 ID.
// It is idempotent: when a currently-open decision already has the same kind,
// item, contract reference, and normalized question, that decision is returned
// unchanged and created is false. Repeated automated turns therefore cannot
// stack duplicate governance questions on the CEO.
func (p *Program) OpenDecision(decision Decision) (result Decision, created bool, err error) {
	if err := p.Validate(); err != nil {
		return Decision{}, false, fmt.Errorf("open decision: current program is invalid: %w", err)
	}
	if err := p.ensureMutable("open decision"); err != nil {
		return Decision{}, false, err
	}
	if existing, found := p.findOpenDuplicate(decision); found {
		return existing, false, nil
	}
	now := timestamp()
	decision.ID = nextNumberedID(p.Decisions, func(decision Decision) string { return decision.ID }, "d")
	decision.CreatedAt = now
	decision.Answer = ""
	decision.ResolvedBy = ""
	decision.ResolvedAt = ""

	next := *p
	next.Decisions = append(append([]Decision(nil), p.Decisions...), decision)
	next.UpdatedAt = now
	if err := next.Validate(); err != nil {
		return Decision{}, false, fmt.Errorf("open decision %q: %w", decision.Question, err)
	}
	*p = next
	return decision, true, nil
}

// findOpenDuplicate returns the currently-open decision that already asks the
// same governance question. Raiser and options are intentionally not part of
// the identity: the same question raised by the worker and by the CTO, or with
// a shortened option list, is still one decision for the CEO.
func (p Program) findOpenDuplicate(decision Decision) (Decision, bool) {
	question := normalizedQuestion(decision.Question)
	if question == "" {
		return Decision{}, false
	}
	for _, existing := range p.Decisions {
		if existing.ResolvedAt != "" {
			continue
		}
		if existing.Kind == decision.Kind &&
			existing.ItemID == decision.ItemID &&
			existing.ContractRef == decision.ContractRef &&
			normalizedQuestion(existing.Question) == question {
			return existing, true
		}
	}
	return Decision{}, false
}

func normalizedQuestion(question string) string {
	return strings.ToLower(strings.Join(strings.Fields(question), " "))
}

// ResolveDecision resolves an open decision exactly once.
func (p *Program) ResolveDecision(id, answer, by string) error {
	if strings.TrimSpace(answer) == "" {
		return fmt.Errorf("resolve decision %q: answer is required", id)
	}
	if strings.TrimSpace(by) == "" {
		return fmt.Errorf("resolve decision %q: by is required", id)
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("resolve decision %q: current program is invalid: %w", id, err)
	}
	if err := p.ensureMutable("resolve decision " + id); err != nil {
		return err
	}
	index := -1
	for i := range p.Decisions {
		if p.Decisions[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("decision %q not found", id)
	}
	if p.Decisions[index].ResolvedAt != "" {
		return fmt.Errorf("decision %q is already resolved", id)
	}
	if p.Decisions[index].Kind == DecisionContract {
		ref := p.Decisions[index].ContractRef
		return fmt.Errorf(
			"decision %q controls contract %q; use: relay program contract approve %s %s --by ceo; or: relay program contract reject %s %s --by ceo --reason <reason>",
			id, ref, p.Slug, ref, p.Slug, ref,
		)
	}
	now := timestamp()
	next := *p
	next.Decisions = append([]Decision(nil), p.Decisions...)
	next.Decisions[index].Answer = answer
	next.Decisions[index].ResolvedBy = by
	next.Decisions[index].ResolvedAt = now
	next.UpdatedAt = now
	if err := next.Validate(); err != nil {
		return fmt.Errorf("resolve decision %q: %w", id, err)
	}
	*p = next
	return nil
}

// OpenDecisions returns all unresolved decisions ordered by numeric ID.
func (p Program) OpenDecisions() []Decision {
	var result []Decision
	for _, decision := range p.Decisions {
		if decision.ResolvedAt == "" {
			result = append(result, decision)
		}
	}
	sortDecisions(result)
	return result
}

// OpenProgramDecisions returns unresolved decisions not scoped to an item.
func (p Program) OpenProgramDecisions() []Decision {
	var result []Decision
	for _, decision := range p.Decisions {
		if decision.ResolvedAt == "" && decision.ItemID == "" {
			result = append(result, decision)
		}
	}
	sortDecisions(result)
	return result
}

// OpenItemDecisions returns unresolved decisions scoped to itemID.
func (p Program) OpenItemDecisions(itemID string) []Decision {
	var result []Decision
	for _, decision := range p.Decisions {
		if decision.ResolvedAt == "" && decision.ItemID == itemID {
			result = append(result, decision)
		}
	}
	sortDecisions(result)
	return result
}

func sortDecisions(decisions []Decision) {
	sort.Slice(decisions, func(i, j int) bool {
		left, _ := parseNumberedID(decisions[i].ID, "d")
		right, _ := parseNumberedID(decisions[j].ID, "d")
		return left < right
	})
}
