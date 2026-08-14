// Package program defines Relay's durable program governance model.
package program

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/project"
)

// State is a program lifecycle state.
type State string

// Program lifecycle states.
const (
	StateDraft           State = "draft"
	StatePendingApproval State = "pending-approval"
	StateActive          State = "active"
	StateHeld            State = "held"
	StateCompleted       State = "completed"
	StateAbandoned       State = "abandoned"
)

// ItemKind identifies a kind of governed work.
type ItemKind string

// V1 supports change work items only.
const ItemKindChange ItemKind = "change"

// Priority orders work from most to least important.
type Priority string

// Supported work item priorities.
const (
	PriorityP0 Priority = "P0"
	PriorityP1 Priority = "P1"
	PriorityP2 Priority = "P2"
	PriorityP3 Priority = "P3"
)

// ItemStatus is a work item's lifecycle status.
type ItemStatus string

// Supported work item statuses.
const (
	ItemPending    ItemStatus = "pending"
	ItemDispatched ItemStatus = "dispatched"
	ItemInReview   ItemStatus = "in-review"
	ItemBlocked    ItemStatus = "blocked"
	ItemMerged     ItemStatus = "merged"
	ItemCancelled  ItemStatus = "cancelled" //nolint:misspell // Persisted V1 schema spelling.
)

// ContractStatus is a contract's approval status.
type ContractStatus string

// Supported contract statuses.
const (
	ContractPending  ContractStatus = "pending"
	ContractApproved ContractStatus = "approved"
	ContractRejected ContractStatus = "rejected"
)

// DecisionKind identifies the reason a decision was opened.
type DecisionKind string

// Supported decision kinds.
const (
	DecisionQuestion DecisionKind = "question"
	DecisionConflict DecisionKind = "conflict"
	DecisionContract DecisionKind = "contract"
)

// RaisedBy identifies who raised a decision.
type RaisedBy string

// Supported decision raisers.
const (
	RaisedByCTO    RaisedBy = "cto"
	RaisedByWorker RaisedBy = "worker"
)

// Program is the durable governance record for a coordinated body of work.
type Program struct {
	Revision            int        `json:"revision"`
	Slug                string     `json:"slug"`
	Title               string     `json:"title"`
	Repo                string     `json:"repo"`
	State               State      `json:"state"`
	Agent               string     `json:"agent"`
	MaxOpenPRs          int        `json:"max_open_prs"`
	CreatedAt           string     `json:"created_at"`
	UpdatedAt           string     `json:"updated_at"`
	ApprovalRequestedAt string     `json:"approval_requested_at,omitempty"`
	ApprovedAt          string     `json:"approved_at,omitempty"`
	ApprovedBy          string     `json:"approved_by,omitempty"`
	HeldAt              string     `json:"held_at,omitempty"`
	CompletedAt         string     `json:"completed_at,omitempty"`
	AbandonedAt         string     `json:"abandoned_at,omitempty"`
	Items               []WorkItem `json:"items"`
	Contracts           []Contract `json:"contracts"`
	Decisions           []Decision `json:"decisions"`
}

// WorkItem is one governed change within a program.
type WorkItem struct {
	ID            string     `json:"id"`
	Kind          ItemKind   `json:"kind"`
	Title         string     `json:"title"`
	Priority      Priority   `json:"priority"`
	Status        ItemStatus `json:"status"`
	Dependencies  []string   `json:"dependencies"`
	ContractRefs  []string   `json:"contract_refs"`
	Repo          string     `json:"repo"`
	ProjectSlug   string     `json:"project_slug,omitempty"`
	PRRef         string     `json:"pr_ref,omitempty"`
	Notes         []string   `json:"notes"`
	BlockedReason string     `json:"blocked_reason,omitempty"`
	CreatedAt     string     `json:"created_at"`
	UpdatedAt     string     `json:"updated_at"`
	DispatchedAt  string     `json:"dispatched_at,omitempty"`
	InReviewAt    string     `json:"in_review_at,omitempty"`
	MergedAt      string     `json:"merged_at,omitempty"`
	CancelledAt   string     `json:"cancelled_at,omitempty"` //nolint:misspell // Persisted V1 schema spelling.
}

// ItemUpdate describes an atomic work item metadata update.
type ItemUpdate struct {
	Title              *string
	Priority           *Priority
	AddDependencies    []string
	RemoveDependencies []string
	AddContractRefs    []string
	RemoveContractRefs []string
	Note               string
}

// Contract records an immutable, versioned contract file.
type Contract struct {
	Name            string         `json:"name"`
	Version         int            `json:"version"`
	Ref             string         `json:"ref"`
	Path            string         `json:"path"`
	SHA256          string         `json:"sha256"`
	Status          ContractStatus `json:"status"`
	PublishedAt     string         `json:"published_at"`
	ApprovedAt      string         `json:"approved_at,omitempty"`
	ApprovedBy      string         `json:"approved_by,omitempty"`
	RejectedAt      string         `json:"rejected_at,omitempty"`
	RejectedBy      string         `json:"rejected_by,omitempty"`
	RejectionReason string         `json:"rejection_reason,omitempty"`
}

// Decision records an open or resolved governance decision.
type Decision struct {
	ID          string       `json:"id"`
	Kind        DecisionKind `json:"kind"`
	RaisedBy    RaisedBy     `json:"raised_by"`
	ItemID      string       `json:"item_id,omitempty"`
	ContractRef string       `json:"contract_ref,omitempty"`
	Question    string       `json:"question"`
	Options     []string     `json:"options"`
	Answer      string       `json:"answer,omitempty"`
	ResolvedBy  string       `json:"resolved_by,omitempty"`
	CreatedAt   string       `json:"created_at"`
	ResolvedAt  string       `json:"resolved_at,omitempty"`
}

// New constructs a valid draft program. maxOpenPRs is stored exactly as
// supplied and must be positive.
func New(slug, title, repo, agent string, maxOpenPRs int) (Program, error) {
	now := timestamp()
	p := Program{
		Revision:   1,
		Slug:       slug,
		Title:      title,
		Repo:       repo,
		State:      StateDraft,
		Agent:      agent,
		MaxOpenPRs: maxOpenPRs,
		CreatedAt:  now,
		UpdatedAt:  now,
		Items:      []WorkItem{},
		Contracts:  []Contract{},
		Decisions:  []Decision{},
	}
	if err := p.Validate(); err != nil {
		return Program{}, err
	}
	return p, nil
}

// Validate checks every structural invariant required by program mutations
// and persistence.
func (p Program) Validate() error {
	var errs []error
	if p.Revision < 0 {
		errs = append(errs, fmt.Errorf("program revision must be non-negative, got %d", p.Revision))
	}
	if err := project.ValidateSlug(p.Slug); err != nil {
		errs = append(errs, fmt.Errorf("program slug: %w", err))
	}
	if strings.TrimSpace(p.Title) == "" {
		errs = append(errs, errors.New("program title is required"))
	}
	if strings.TrimSpace(p.Repo) == "" {
		errs = append(errs, errors.New("program repo is required"))
	}
	if strings.TrimSpace(p.Agent) == "" {
		errs = append(errs, errors.New("program agent is required"))
	}
	if p.MaxOpenPRs <= 0 {
		errs = append(errs, fmt.Errorf("program max_open_prs must be positive, got %d", p.MaxOpenPRs))
	}
	if !validState(p.State) {
		errs = append(errs, fmt.Errorf("program state %q is unsupported", p.State))
	}
	validateTimestamp(&errs, "program created_at", p.CreatedAt, true)
	validateTimestamp(&errs, "program updated_at", p.UpdatedAt, true)
	validateTimestamp(&errs, "program approval_requested_at", p.ApprovalRequestedAt, false)
	validateTimestamp(&errs, "program approved_at", p.ApprovedAt, false)
	validateTimestamp(&errs, "program held_at", p.HeldAt, false)
	validateTimestamp(&errs, "program completed_at", p.CompletedAt, false)
	validateTimestamp(&errs, "program abandoned_at", p.AbandonedAt, false)
	if (p.State == StatePendingApproval || stateNeedsApproval(p.State)) && p.ApprovalRequestedAt == "" {
		errs = append(errs, fmt.Errorf("program state %q requires approval_requested_at", p.State))
	}
	if stateNeedsApproval(p.State) && (p.ApprovedAt == "" || strings.TrimSpace(p.ApprovedBy) == "") {
		errs = append(errs, fmt.Errorf("program state %q requires approved_at and approved_by", p.State))
	}
	if p.State == StateHeld && p.HeldAt == "" {
		errs = append(errs, errors.New("program held state requires held_at"))
	}
	if p.State == StateCompleted && p.CompletedAt == "" {
		errs = append(errs, errors.New("program completed state requires completed_at"))
	}
	if p.State == StateAbandoned && p.AbandonedAt == "" {
		errs = append(errs, errors.New("program abandoned state requires abandoned_at"))
	}

	items := make(map[string]WorkItem, len(p.Items))
	projectSlugs := make(map[string]string, len(p.Items))
	for i, item := range p.Items {
		context := fmt.Sprintf("item[%d]", i)
		if _, err := parseNumberedID(item.ID, "w"); err != nil {
			errs = append(errs, fmt.Errorf("%s id: %w", context, err))
		} else if _, exists := items[item.ID]; exists {
			errs = append(errs, fmt.Errorf("%s duplicate id %q", context, item.ID))
		}
		items[item.ID] = item
		if item.Kind != ItemKindChange {
			errs = append(errs, fmt.Errorf("item %q kind %q is unsupported", item.ID, item.Kind))
		}
		if strings.TrimSpace(item.Title) == "" {
			errs = append(errs, fmt.Errorf("item %q title is required", item.ID))
		}
		if !validPriority(item.Priority) {
			errs = append(errs, fmt.Errorf("item %q priority %q is unsupported", item.ID, item.Priority))
		}
		if !validItemStatus(item.Status) {
			errs = append(errs, fmt.Errorf("item %q status %q is unsupported", item.ID, item.Status))
		}
		if item.Repo != p.Repo {
			errs = append(errs, fmt.Errorf("item %q repo %q must equal program repo %q in V1", item.ID, item.Repo, p.Repo))
		}
		if item.ProjectSlug != "" {
			if err := project.ValidateSlug(item.ProjectSlug); err != nil {
				errs = append(errs, fmt.Errorf("item %q project_slug: %w", item.ID, err))
			}
			if item.Status != ItemCancelled {
				if existingID, exists := projectSlugs[item.ProjectSlug]; exists {
					errs = append(errs, fmt.Errorf(
						"item %q project_slug %q is already linked to item %q",
						item.ID, item.ProjectSlug, existingID,
					))
				} else {
					projectSlugs[item.ProjectSlug] = item.ID
				}
			}
		}
		if item.PRRef != "" && item.ProjectSlug == "" {
			errs = append(errs, fmt.Errorf("item %q has pr_ref without project_slug", item.ID))
		}
		if (item.Status == ItemDispatched || item.Status == ItemInReview || item.Status == ItemMerged) && item.ProjectSlug == "" {
			errs = append(errs, fmt.Errorf("item %q status %q requires project_slug", item.ID, item.Status))
		}
		if item.Status == ItemInReview && item.PRRef == "" {
			errs = append(errs, fmt.Errorf("item %q status %q requires pr_ref", item.ID, item.Status))
		}
		if item.Status == ItemBlocked && strings.TrimSpace(item.BlockedReason) == "" {
			errs = append(errs, fmt.Errorf("item %q blocked status requires blocked_reason", item.ID))
		}
		if item.Status != ItemBlocked && item.BlockedReason != "" {
			errs = append(errs, fmt.Errorf("item %q status %q cannot have blocked_reason", item.ID, item.Status))
		}
		validateTimestamp(&errs, fmt.Sprintf("item %q created_at", item.ID), item.CreatedAt, true)
		validateTimestamp(&errs, fmt.Sprintf("item %q updated_at", item.ID), item.UpdatedAt, true)
		validateTimestamp(&errs, fmt.Sprintf("item %q dispatched_at", item.ID), item.DispatchedAt, false)
		validateTimestamp(&errs, fmt.Sprintf("item %q in_review_at", item.ID), item.InReviewAt, false)
		validateTimestamp(&errs, fmt.Sprintf("item %q merged_at", item.ID), item.MergedAt, false)
		validateTimestamp(&errs, fmt.Sprintf("item %q %s_at", item.ID, ItemCancelled), item.CancelledAt, false)
		if (item.Status == ItemDispatched || item.Status == ItemInReview || item.Status == ItemMerged) && item.DispatchedAt == "" {
			errs = append(errs, fmt.Errorf("item %q status %q requires dispatched_at", item.ID, item.Status))
		}
		if item.Status == ItemInReview && item.InReviewAt == "" {
			errs = append(errs, fmt.Errorf("item %q status %q requires in_review_at", item.ID, item.Status))
		}
		if item.Status == ItemMerged && item.MergedAt == "" {
			errs = append(errs, fmt.Errorf("item %q status %q requires merged_at", item.ID, item.Status))
		}
		if item.Status == ItemCancelled && item.CancelledAt == "" {
			errs = append(errs, fmt.Errorf("item %q status %q requires %s_at", item.ID, item.Status, ItemCancelled))
		}
	}

	contracts := make(map[string]Contract, len(p.Contracts))
	for i, contract := range p.Contracts {
		context := fmt.Sprintf("contract[%d]", i)
		if contract.Name == "" || safeContractName(contract.Name) != contract.Name {
			errs = append(errs, fmt.Errorf("%s name %q is not a safe contract name", context, contract.Name))
		}
		if contract.Version <= 0 {
			errs = append(errs, fmt.Errorf("%s version must be positive, got %d", context, contract.Version))
		}
		expectedRef := fmt.Sprintf("%s@v%d", contract.Name, contract.Version)
		if contract.Ref != expectedRef {
			errs = append(errs, fmt.Errorf("%s ref %q must be %q", context, contract.Ref, expectedRef))
		}
		if _, exists := contracts[contract.Ref]; exists {
			errs = append(errs, fmt.Errorf("%s duplicate ref %q", context, contract.Ref))
		}
		contracts[contract.Ref] = contract
		expectedPath := filepath.ToSlash(filepath.Join("contracts", contract.Name, fmt.Sprintf("v%d.md", contract.Version)))
		if filepath.ToSlash(contract.Path) != expectedPath {
			errs = append(errs, fmt.Errorf("contract %q path %q must be %q", contract.Ref, contract.Path, expectedPath))
		}
		if strings.TrimSpace(contract.SHA256) == "" {
			errs = append(errs, fmt.Errorf("contract %q sha256 is required", contract.Ref))
		}
		if contract.Status != ContractPending && contract.Status != ContractApproved && contract.Status != ContractRejected {
			errs = append(errs, fmt.Errorf("contract %q status %q is unsupported", contract.Ref, contract.Status))
		}
		validateTimestamp(&errs, fmt.Sprintf("contract %q published_at", contract.Ref), contract.PublishedAt, true)
		validateTimestamp(&errs, fmt.Sprintf("contract %q approved_at", contract.Ref), contract.ApprovedAt, false)
		validateTimestamp(&errs, fmt.Sprintf("contract %q rejected_at", contract.Ref), contract.RejectedAt, false)
		if contract.Status == ContractApproved && (contract.ApprovedAt == "" || strings.TrimSpace(contract.ApprovedBy) == "") {
			errs = append(errs, fmt.Errorf("contract %q approved status requires approved_at and approved_by", contract.Ref))
		}
		if contract.Status == ContractRejected &&
			(contract.RejectedAt == "" || strings.TrimSpace(contract.RejectedBy) == "" || strings.TrimSpace(contract.RejectionReason) == "") {
			errs = append(errs, fmt.Errorf("contract %q rejected status requires rejected_at, rejected_by, and rejection_reason", contract.Ref))
		}
		if contract.Status != ContractApproved && (contract.ApprovedAt != "" || contract.ApprovedBy != "") {
			errs = append(errs, fmt.Errorf("contract %q status %q cannot have approval fields", contract.Ref, contract.Status))
		}
		if contract.Status != ContractRejected &&
			(contract.RejectedAt != "" || contract.RejectedBy != "" || contract.RejectionReason != "") {
			errs = append(errs, fmt.Errorf("contract %q status %q cannot have rejection fields", contract.Ref, contract.Status))
		}
	}

	for _, item := range p.Items {
		seenDependencies := make(map[string]bool, len(item.Dependencies))
		for _, dependency := range item.Dependencies {
			if dependency == item.ID {
				errs = append(errs, fmt.Errorf("item %q cannot depend on itself", item.ID))
			}
			if seenDependencies[dependency] {
				errs = append(errs, fmt.Errorf("item %q has duplicate dependency %q", item.ID, dependency))
			}
			seenDependencies[dependency] = true
			if _, exists := items[dependency]; !exists {
				errs = append(errs, fmt.Errorf("item %q dependency %q does not exist", item.ID, dependency))
			}
		}
		seenContracts := make(map[string]bool, len(item.ContractRefs))
		for _, ref := range item.ContractRefs {
			if _, _, err := ParseContractRef(ref); err != nil {
				errs = append(errs, fmt.Errorf("item %q contract ref %q: %w", item.ID, ref, err))
				continue
			}
			if seenContracts[ref] {
				errs = append(errs, fmt.Errorf("item %q has duplicate contract ref %q", item.ID, ref))
			}
			seenContracts[ref] = true
			if _, exists := contracts[ref]; !exists {
				errs = append(errs, fmt.Errorf("item %q contract %q does not resolve", item.ID, ref))
			}
		}
	}
	if cycle := dependencyCycle(p.Items, items); len(cycle) > 0 {
		errs = append(errs, fmt.Errorf("dependency cycle: %s", strings.Join(cycle, " -> ")))
	}

	decisions := make(map[string]bool, len(p.Decisions))
	for i, decision := range p.Decisions {
		context := fmt.Sprintf("decision[%d]", i)
		if _, err := parseNumberedID(decision.ID, "d"); err != nil {
			errs = append(errs, fmt.Errorf("%s id: %w", context, err))
		} else if decisions[decision.ID] {
			errs = append(errs, fmt.Errorf("%s duplicate id %q", context, decision.ID))
		}
		decisions[decision.ID] = true
		if !validDecisionKind(decision.Kind) {
			errs = append(errs, fmt.Errorf("decision %q kind %q is unsupported", decision.ID, decision.Kind))
		}
		if decision.RaisedBy != RaisedByCTO && decision.RaisedBy != RaisedByWorker {
			errs = append(errs, fmt.Errorf("decision %q raised_by %q is unsupported", decision.ID, decision.RaisedBy))
		}
		if strings.TrimSpace(decision.Question) == "" {
			errs = append(errs, fmt.Errorf("decision %q question is required", decision.ID))
		}
		if decision.ItemID != "" {
			if _, exists := items[decision.ItemID]; !exists {
				errs = append(errs, fmt.Errorf("decision %q item %q does not exist", decision.ID, decision.ItemID))
			}
		}
		if decision.Kind == DecisionContract {
			if _, _, err := ParseContractRef(decision.ContractRef); err != nil {
				errs = append(errs, fmt.Errorf("decision %q contract_ref: %w", decision.ID, err))
			} else if _, exists := contracts[decision.ContractRef]; !exists {
				errs = append(errs, fmt.Errorf("decision %q contract %q does not resolve", decision.ID, decision.ContractRef))
			}
		} else if decision.ContractRef != "" {
			errs = append(errs, fmt.Errorf("decision %q kind %q cannot have contract_ref", decision.ID, decision.Kind))
		}
		validateTimestamp(&errs, fmt.Sprintf("decision %q created_at", decision.ID), decision.CreatedAt, true)
		validateTimestamp(&errs, fmt.Sprintf("decision %q resolved_at", decision.ID), decision.ResolvedAt, false)
		resolutionFields := 0
		if strings.TrimSpace(decision.Answer) != "" {
			resolutionFields++
		}
		if strings.TrimSpace(decision.ResolvedBy) != "" {
			resolutionFields++
		}
		if decision.ResolvedAt != "" {
			resolutionFields++
		}
		if resolutionFields != 0 && resolutionFields != 3 {
			errs = append(errs, fmt.Errorf("decision %q resolution requires answer, resolved_by, and resolved_at", decision.ID))
		}
	}
	return errors.Join(errs...)
}

// Transition moves the program to next when the lifecycle transition is legal.
// by is required when approving, holding, completing, or abandoning a program.
func (p *Program) Transition(next State, by string) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("transition program %q: current program is invalid: %w", p.Slug, err)
	}
	if p.State == StateCompleted || p.State == StateAbandoned {
		return fmt.Errorf("transition program %q: state %q is terminal", p.Slug, p.State)
	}
	if next == StateAbandoned {
		if strings.TrimSpace(by) == "" {
			return fmt.Errorf("transition program %q to abandoned: by is required", p.Slug)
		}
	} else if !legalProgramTransition(p.State, next) {
		return fmt.Errorf("transition program %q: %q -> %q is not allowed", p.Slug, p.State, next)
	}
	if next == StateActive && p.State == StatePendingApproval && strings.TrimSpace(by) == "" {
		return fmt.Errorf("transition program %q to active: approver is required", p.Slug)
	}
	if (next == StateHeld || next == StateCompleted) && strings.TrimSpace(by) == "" {
		return fmt.Errorf("transition program %q to %s: by is required", p.Slug, next)
	}
	if next == StateCompleted {
		for _, item := range p.Items {
			if item.Status != ItemMerged && item.Status != ItemCancelled {
				return fmt.Errorf("complete program %q: item %q is %q, want merged or %s", p.Slug, item.ID, item.Status, ItemCancelled)
			}
		}
	}

	nextProgram := *p
	now := timestamp()
	nextProgram.State = next
	nextProgram.UpdatedAt = now
	switch next {
	case StatePendingApproval:
		nextProgram.ApprovalRequestedAt = now
	case StateActive:
		if p.State == StatePendingApproval {
			nextProgram.ApprovedAt = now
			nextProgram.ApprovedBy = by
		}
	case StateHeld:
		nextProgram.HeldAt = now
	case StateCompleted:
		nextProgram.CompletedAt = now
	case StateAbandoned:
		nextProgram.AbandonedAt = now
	}
	if err := nextProgram.Validate(); err != nil {
		return fmt.Errorf("transition program %q to %q: %w", p.Slug, next, err)
	}
	*p = nextProgram
	return nil
}

// AddItem appends a pending work item and assigns the next max+1 work item ID.
func (p *Program) AddItem(item WorkItem) (WorkItem, error) {
	if err := p.Validate(); err != nil {
		return WorkItem{}, fmt.Errorf("add item: current program is invalid: %w", err)
	}
	if err := p.ensureMutable("add item"); err != nil {
		return WorkItem{}, err
	}
	if item.Status != "" && item.Status != ItemPending {
		return WorkItem{}, fmt.Errorf("add item: initial status %q must be pending", item.Status)
	}
	now := timestamp()
	item.ID = nextNumberedID(p.Items, func(item WorkItem) string { return item.ID }, "w")
	if item.Kind == "" {
		item.Kind = ItemKindChange
	}
	if item.Status == "" {
		item.Status = ItemPending
	}
	if item.Repo == "" {
		item.Repo = p.Repo
	}
	item.CreatedAt = now
	item.UpdatedAt = now
	item.DispatchedAt = ""
	item.InReviewAt = ""
	item.MergedAt = ""
	item.CancelledAt = ""
	item.BlockedReason = ""

	next := *p
	next.Items = append(append([]WorkItem(nil), p.Items...), item)
	next.UpdatedAt = now
	if err := next.Validate(); err != nil {
		return WorkItem{}, fmt.Errorf("add item %q: %w", item.Title, err)
	}
	*p = next
	return item, nil
}

// Item returns the work item with id.
func (p Program) Item(id string) (WorkItem, bool) {
	for _, item := range p.Items {
		if item.ID == id {
			return item, true
		}
	}
	return WorkItem{}, false
}

// UpdateItem atomically updates a work item's metadata and validates the full
// program before committing the mutation.
func (p *Program) UpdateItem(id string, update ItemUpdate) error {
	if update.Title == nil && update.Priority == nil &&
		len(update.AddDependencies) == 0 && len(update.RemoveDependencies) == 0 &&
		len(update.AddContractRefs) == 0 && len(update.RemoveContractRefs) == 0 &&
		strings.TrimSpace(update.Note) == "" {
		return fmt.Errorf("update item %q: no changes requested", id)
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("update item %q: current program is invalid: %w", id, err)
	}
	if err := p.ensureMutable("update item " + id); err != nil {
		return err
	}
	index, err := p.itemIndex(id)
	if err != nil {
		return err
	}

	next := p.copyWithItems()
	item := &next.Items[index]
	item.Dependencies = append([]string(nil), item.Dependencies...)
	item.ContractRefs = append([]string(nil), item.ContractRefs...)
	item.Notes = append([]string(nil), item.Notes...)
	if update.Title != nil {
		item.Title = *update.Title
	}
	if update.Priority != nil {
		item.Priority = *update.Priority
	}
	item.Dependencies, err = updateStringSet(item.Dependencies, update.AddDependencies, update.RemoveDependencies, "dependency")
	if err != nil {
		return fmt.Errorf("update item %q: %w", id, err)
	}
	item.ContractRefs, err = updateStringSet(item.ContractRefs, update.AddContractRefs, update.RemoveContractRefs, "contract")
	if err != nil {
		return fmt.Errorf("update item %q: %w", id, err)
	}
	if strings.TrimSpace(update.Note) != "" {
		item.Notes = append(item.Notes, update.Note)
	}
	now := timestamp()
	item.UpdatedAt = now
	next.UpdatedAt = now
	if err := next.Validate(); err != nil {
		return fmt.Errorf("update item %q: %w", id, err)
	}
	*p = next
	return nil
}

// LinkItem associates a pending item with an existing Relay project.
func (p *Program) LinkItem(id, projectSlug string) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("link item %q: current program is invalid: %w", id, err)
	}
	if err := p.ensureMutable("link item " + id); err != nil {
		return err
	}
	if err := project.ValidateSlug(projectSlug); err != nil {
		return fmt.Errorf("link item %q: project slug: %w", id, err)
	}
	index, err := p.itemIndex(id)
	if err != nil {
		return err
	}
	item := p.Items[index]
	if item.Status != ItemPending {
		return fmt.Errorf("link item %q: status %q is not pending", id, item.Status)
	}
	if item.ProjectSlug != "" && item.ProjectSlug != projectSlug {
		return fmt.Errorf("link item %q: already linked to project %q", id, item.ProjectSlug)
	}
	next := p.copyWithItems()
	next.Items[index].ProjectSlug = projectSlug
	next.Items[index].UpdatedAt = timestamp()
	next.UpdatedAt = next.Items[index].UpdatedAt
	if err := next.Validate(); err != nil {
		return fmt.Errorf("link item %q: %w", id, err)
	}
	*p = next
	return nil
}

// DispatchItem moves a ready pending item to dispatched. An optional project
// slug links the item as part of the same mutation.
func (p *Program) DispatchItem(id string, projectSlug ...string) error {
	if len(projectSlug) > 1 {
		return fmt.Errorf("dispatch item %q: expected at most one project slug", id)
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("dispatch item %q: current program is invalid: %w", id, err)
	}
	if err := p.ensureMutable("dispatch item " + id); err != nil {
		return err
	}
	index, err := p.itemIndex(id)
	if err != nil {
		return err
	}
	if p.Items[index].Status != ItemPending {
		return fmt.Errorf("dispatch item %q: status %q is not pending", id, p.Items[index].Status)
	}
	linkedSlug := p.Items[index].ProjectSlug
	if len(projectSlug) == 1 {
		if err := project.ValidateSlug(projectSlug[0]); err != nil {
			return fmt.Errorf("dispatch item %q: project slug: %w", id, err)
		}
		if linkedSlug != "" && linkedSlug != projectSlug[0] {
			return fmt.Errorf("dispatch item %q: already linked to project %q", id, linkedSlug)
		}
		linkedSlug = projectSlug[0]
	}
	if linkedSlug == "" {
		return fmt.Errorf("dispatch item %q: project slug is required", id)
	}
	_, blocked := p.Readiness()
	for _, candidate := range blocked {
		if candidate.Item.ID == id {
			return fmt.Errorf("dispatch item %q: not ready: %s", id, strings.Join(candidate.Reasons, "; "))
		}
	}
	now := timestamp()
	next := p.copyWithItems()
	next.Items[index].ProjectSlug = linkedSlug
	next.Items[index].Status = ItemDispatched
	next.Items[index].DispatchedAt = now
	next.Items[index].UpdatedAt = now
	next.UpdatedAt = now
	if err := next.Validate(); err != nil {
		return fmt.Errorf("dispatch item %q: %w", id, err)
	}
	*p = next
	return nil
}

// BlockItem moves pending, dispatched, or in-review work to blocked.
func (p *Program) BlockItem(id, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("block item %q: reason is required", id)
	}
	return p.mutateItemStatus(id, ItemBlocked, func(item *WorkItem, now string) error {
		switch item.Status {
		case ItemPending, ItemDispatched, ItemInReview:
		default:
			return fmt.Errorf("status %q cannot be blocked", item.Status)
		}
		item.BlockedReason = reason
		return nil
	})
}

// UnblockItem restores a blocked item to pending, dispatched, or in-review
// based on its recorded project and PR.
func (p *Program) UnblockItem(id string) error {
	return p.mutateItemStatus(id, "", func(item *WorkItem, now string) error {
		if item.Status != ItemBlocked {
			return fmt.Errorf("status %q is not blocked", item.Status)
		}
		item.BlockedReason = ""
		switch {
		case item.PRRef != "":
			item.Status = ItemInReview
			if item.InReviewAt == "" {
				item.InReviewAt = now
			}
		case item.DispatchedAt != "":
			item.Status = ItemDispatched
		default:
			item.Status = ItemPending
		}
		return nil
	})
}

// CancelItem moves any nonterminal item to canceled.
func (p *Program) CancelItem(id, note string) error {
	return p.mutateItemStatus(id, ItemCancelled, func(item *WorkItem, now string) error {
		if item.Status == ItemMerged || item.Status == ItemCancelled {
			return fmt.Errorf("status %q is terminal", item.Status)
		}
		item.BlockedReason = ""
		item.CancelledAt = now
		if strings.TrimSpace(note) != "" {
			item.Notes = append(item.Notes, note)
		}
		return nil
	})
}

func (p *Program) mutateItemStatus(id string, status ItemStatus, mutate func(*WorkItem, string) error) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("mutate item %q: current program is invalid: %w", id, err)
	}
	if err := p.ensureMutable("mutate item " + id); err != nil {
		return err
	}
	index, err := p.itemIndex(id)
	if err != nil {
		return err
	}
	now := timestamp()
	next := p.copyWithItems()
	previous := next.Items[index].Status
	if err := mutate(&next.Items[index], now); err != nil {
		return fmt.Errorf("mutate item %q: %w", id, err)
	}
	if status != "" {
		next.Items[index].Status = status
	}
	next.Items[index].UpdatedAt = now
	next.UpdatedAt = now
	if err := next.Validate(); err != nil {
		return fmt.Errorf("mutate item %q from %q: %w", id, previous, err)
	}
	*p = next
	return nil
}

func (p Program) itemIndex(id string) (int, error) {
	for i := range p.Items {
		if p.Items[i].ID == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("item %q not found", id)
}

func (p Program) copyWithItems() Program {
	next := p
	next.Items = append([]WorkItem(nil), p.Items...)
	return next
}

func updateStringSet(current, additions, removals []string, label string) ([]string, error) {
	remove := make(map[string]bool, len(removals))
	for _, value := range removals {
		if remove[value] {
			return nil, fmt.Errorf("duplicate %s removal %q", label, value)
		}
		remove[value] = true
	}
	for _, value := range additions {
		if remove[value] {
			return nil, fmt.Errorf("%s %q cannot be added and removed together", label, value)
		}
	}
	result := make([]string, 0, len(current)+len(additions))
	for _, value := range current {
		if remove[value] {
			delete(remove, value)
			continue
		}
		result = append(result, value)
	}
	if len(remove) > 0 {
		for _, value := range removals {
			if remove[value] {
				return nil, fmt.Errorf("%s %q is not present", label, value)
			}
		}
	}
	return append(result, additions...), nil
}

func (p Program) ensureMutable(action string) error {
	if p.State == StateCompleted || p.State == StateAbandoned {
		return fmt.Errorf("%s: program %q state %q is terminal", action, p.Slug, p.State)
	}
	return nil
}

func timestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func validateTimestamp(errs *[]error, name, value string, required bool) {
	if value == "" {
		if required {
			*errs = append(*errs, fmt.Errorf("%s is required", name))
		}
		return
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		*errs = append(*errs, fmt.Errorf("%s %q is not RFC3339: %v", name, value, err))
	}
}

func validState(state State) bool {
	switch state {
	case StateDraft, StatePendingApproval, StateActive, StateHeld, StateCompleted, StateAbandoned:
		return true
	default:
		return false
	}
}

func stateNeedsApproval(state State) bool {
	return state == StateActive || state == StateHeld || state == StateCompleted
}

func legalProgramTransition(from, to State) bool {
	switch from {
	case StateDraft:
		return to == StatePendingApproval
	case StatePendingApproval:
		return to == StateActive
	case StateActive:
		return to == StateHeld || to == StateCompleted
	case StateHeld:
		return to == StateActive || to == StateCompleted
	default:
		return false
	}
}

func validPriority(priority Priority) bool {
	return priority == PriorityP0 || priority == PriorityP1 || priority == PriorityP2 || priority == PriorityP3
}

func validItemStatus(status ItemStatus) bool {
	switch status {
	case ItemPending, ItemDispatched, ItemInReview, ItemBlocked, ItemMerged, ItemCancelled:
		return true
	default:
		return false
	}
}

func validDecisionKind(kind DecisionKind) bool {
	return kind == DecisionQuestion || kind == DecisionConflict || kind == DecisionContract
}

func parseNumberedID(id, prefix string) (int, error) {
	if !strings.HasPrefix(id, prefix) || len(id) == len(prefix) {
		return 0, fmt.Errorf("%q must have the form %s<N>", id, prefix)
	}
	number, err := strconv.Atoi(strings.TrimPrefix(id, prefix))
	if err != nil || number <= 0 || strconv.Itoa(number) != strings.TrimPrefix(id, prefix) {
		return 0, fmt.Errorf("%q must have the form %s<N> with N >= 1", id, prefix)
	}
	return number, nil
}

func nextNumberedID[T any](values []T, id func(T) string, prefix string) string {
	maximum := 0
	for _, value := range values {
		number, err := parseNumberedID(id(value), prefix)
		if err == nil && number > maximum {
			maximum = number
		}
	}
	return fmt.Sprintf("%s%d", prefix, maximum+1)
}

func dependencyCycle(ordered []WorkItem, items map[string]WorkItem) []string {
	const (
		unseen = iota
		visiting
		visited
	)
	state := make(map[string]int, len(items))
	var stack []string
	var visit func(string) []string
	visit = func(id string) []string {
		state[id] = visiting
		stack = append(stack, id)
		for _, dependency := range items[id].Dependencies {
			if _, exists := items[dependency]; !exists {
				continue
			}
			if state[dependency] == visiting {
				for i, stacked := range stack {
					if stacked == dependency {
						return append(append([]string(nil), stack[i:]...), dependency)
					}
				}
			}
			if state[dependency] == unseen {
				if cycle := visit(dependency); len(cycle) > 0 {
					return cycle
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = visited
		return nil
	}
	for _, item := range ordered {
		if state[item.ID] == unseen {
			if cycle := visit(item.ID); len(cycle) > 0 {
				return cycle
			}
		}
	}
	return nil
}
