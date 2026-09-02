package program

import (
	"fmt"
	"sort"
)

// BlockedItem pairs a work item with all currently known readiness blockers.
type BlockedItem struct {
	Item    WorkItem `json:"item"`
	Reasons []string `json:"reasons"`
}

// Readiness derives ready pending items and blocked pending items.
func (p Program) Readiness() ([]WorkItem, []BlockedItem) {
	items := make(map[string]WorkItem, len(p.Items))
	contracts := make(map[string]Contract, len(p.Contracts))
	for _, item := range p.Items {
		items[item.ID] = item
	}
	for _, contract := range p.Contracts {
		contracts[contract.Ref] = contract
	}
	programDecisions := p.OpenProgramDecisions()

	var ready []WorkItem
	var blocked []BlockedItem
	for _, item := range p.Items {
		if item.Status != ItemPending {
			continue
		}
		var reasons []string
		if p.State != StateActive {
			reasons = append(reasons, fmt.Sprintf("program state is %s", p.State))
		}
		for _, decision := range programDecisions {
			reasons = append(reasons, "open program decision "+decision.ID)
		}
		for _, decision := range p.OpenItemDecisions(item.ID) {
			reasons = append(reasons, "open decision "+decision.ID)
		}
		for _, dependency := range item.Dependencies {
			status := items[dependency].Status
			if status != ItemMerged {
				reasons = append(reasons, fmt.Sprintf("dependency %s is %s", dependency, status))
			}
		}
		for _, ref := range item.ContractRefs {
			status := contracts[ref].Status
			if status != ContractApproved {
				reasons = append(reasons, fmt.Sprintf("contract %s is %s", ref, status))
			}
		}
		if len(reasons) == 0 {
			ready = append(ready, item)
			continue
		}
		blocked = append(blocked, BlockedItem{Item: item, Reasons: reasons})
	}
	sort.Slice(ready, func(i, j int) bool {
		left := priorityRank(ready[i].Priority)
		right := priorityRank(ready[j].Priority)
		if left != right {
			return left < right
		}
		return itemNumber(ready[i].ID) < itemNumber(ready[j].ID)
	})
	sort.Slice(blocked, func(i, j int) bool {
		return itemNumber(blocked[i].Item.ID) < itemNumber(blocked[j].Item.ID)
	})
	return ready, blocked
}

func priorityRank(priority Priority) int {
	switch priority {
	case PriorityP0:
		return 0
	case PriorityP1:
		return 1
	case PriorityP2:
		return 2
	default:
		return 3
	}
}

func itemNumber(id string) int {
	number, _ := parseNumberedID(id, "w")
	return number
}
