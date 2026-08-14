package program

import (
	"fmt"
	"sort"
)

// ProjectView is the caller-supplied state of a Relay-managed child project.
// Reconciliation never reads git, GitHub, or disk directly.
type ProjectView struct {
	Slug     string
	Repo     string
	HasPR    bool
	PRRef    string
	Merged   bool
	Archived bool
	Orphaned bool
}

// ReconcileResult reports whether reconciliation changed the program and
// which linked items have no usable child project.
type ReconcileResult struct {
	Changed   bool
	OrphanIDs []string
}

// Capacity reports the configured and currently available PR capacity.
type Capacity struct {
	Limit     int
	Open      int
	Available int
}

// View is the pure derived planning view of a program.
type View struct {
	Ready         []WorkItem
	Blocked       []BlockedItem
	InFlight      []WorkItem
	OpenDecisions []Decision
	Capacity      Capacity
	NextAction    string
}

// Reconcile applies caller-supplied child project state to linked work items.
func (p *Program) Reconcile(projects []ProjectView) (ReconcileResult, error) {
	if err := p.Validate(); err != nil {
		return ReconcileResult{}, fmt.Errorf("reconcile program %q: current program is invalid: %w", p.Slug, err)
	}
	if err := p.ensureMutable("reconcile program " + p.Slug); err != nil {
		return ReconcileResult{}, err
	}
	views := make(map[string]ProjectView, len(projects))
	for _, view := range projects {
		if view.Slug == "" {
			return ReconcileResult{}, fmt.Errorf("reconcile program %q: project view slug is required", p.Slug)
		}
		if _, exists := views[view.Slug]; exists {
			return ReconcileResult{}, fmt.Errorf("reconcile program %q: duplicate project view %q", p.Slug, view.Slug)
		}
		if view.HasPR && view.PRRef == "" {
			return ReconcileResult{}, fmt.Errorf("reconcile program %q: project %q has_pr requires pr_ref", p.Slug, view.Slug)
		}
		views[view.Slug] = view
	}

	next := p.copyWithItems()
	result := ReconcileResult{}
	now := timestamp()
	for i := range next.Items {
		item := &next.Items[i]
		if item.ProjectSlug == "" || item.Status == ItemMerged || item.Status == ItemCancelled {
			continue
		}
		view, exists := views[item.ProjectSlug]
		if !exists || view.Orphaned || (view.Archived && !view.Merged) {
			if item.Status != ItemBlocked {
				result.OrphanIDs = append(result.OrphanIDs, item.ID)
			}
			continue
		}
		if view.Repo != p.Repo {
			return ReconcileResult{}, fmt.Errorf("reconcile item %q: project %q repo %q does not match program repo %q", item.ID, view.Slug, view.Repo, p.Repo)
		}
		changed := false
		if view.HasPR && item.PRRef != view.PRRef {
			item.PRRef = view.PRRef
			changed = true
		}
		if item.Status == ItemDispatched || item.Status == ItemInReview {
			switch {
			case view.Merged && item.Status != ItemMerged:
				item.Status = ItemMerged
				item.MergedAt = now
				changed = true
			case view.HasPR && item.Status == ItemDispatched:
				item.Status = ItemInReview
				item.InReviewAt = now
				changed = true
			}
		}
		if changed {
			item.UpdatedAt = now
			result.Changed = true
		}
	}
	sort.Slice(result.OrphanIDs, func(i, j int) bool {
		return itemNumber(result.OrphanIDs[i]) < itemNumber(result.OrphanIDs[j])
	})
	if !result.Changed {
		return result, nil
	}
	next.UpdatedAt = now
	if err := next.Validate(); err != nil {
		return ReconcileResult{}, fmt.Errorf("reconcile program %q: %w", p.Slug, err)
	}
	*p = next
	return result, nil
}

// Plan returns the pure derived governance view for the supplied active Relay
// projects. Only recorded, unmerged PRs in the program repo consume capacity.
func (p Program) Plan(projects []ProjectView) View {
	ready, blocked := p.Readiness()
	var inFlight []WorkItem
	for _, item := range p.Items {
		switch item.Status {
		case ItemDispatched, ItemInReview:
			inFlight = append(inFlight, item)
		case ItemBlocked:
			reasons := []string{item.BlockedReason}
			blocked = append(blocked, BlockedItem{Item: item, Reasons: reasons})
		}
	}
	sort.Slice(inFlight, func(i, j int) bool {
		return itemNumber(inFlight[i].ID) < itemNumber(inFlight[j].ID)
	})
	sort.Slice(blocked, func(i, j int) bool {
		return itemNumber(blocked[i].Item.ID) < itemNumber(blocked[j].Item.ID)
	})

	seen := make(map[string]bool, len(projects))
	open := 0
	for _, project := range projects {
		if seen[project.Slug] || project.Repo != p.Repo || !project.HasPR || project.Merged || project.Archived {
			continue
		}
		seen[project.Slug] = true
		open++
	}
	available := p.MaxOpenPRs - open
	if available < 0 {
		available = 0
	}
	view := View{
		Ready:         ready,
		Blocked:       blocked,
		InFlight:      inFlight,
		OpenDecisions: p.OpenDecisions(),
		Capacity: Capacity{
			Limit:     p.MaxOpenPRs,
			Open:      open,
			Available: available,
		},
	}
	switch {
	case len(view.OpenDecisions) > 0:
		view.NextAction = "resolve " + view.OpenDecisions[0].ID
	case p.State == StateDraft:
		view.NextAction = "request approval"
	case p.State == StatePendingApproval:
		view.NextAction = "approve program"
	case p.State == StateHeld:
		view.NextAction = "resume program"
	case len(ready) > 0:
		view.NextAction = "dispatch " + ready[0].ID
	case len(inFlight) > 0:
		view.NextAction = "reconcile in-flight work"
	case p.State == StateActive && allItemsTerminal(p.Items):
		view.NextAction = "complete program"
	default:
		view.NextAction = "no action"
	}
	return view
}

func allItemsTerminal(items []WorkItem) bool {
	for _, item := range items {
		if item.Status != ItemMerged && item.Status != ItemCancelled {
			return false
		}
	}
	return true
}
