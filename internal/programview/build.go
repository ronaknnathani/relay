package programview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

const defaultArtifactLimit int64 = 128 * 1024

var childArtifactNames = []string{
	"assignment.md",
	"task.md",
	"requirements.md",
	"clarify.md",
	"plan.md",
	"notes.md",
	"todos.md",
	"progress.md",
	"tradeoffs.md",
	"questions.md",
	"follow-ups.md",
	"review.md",
	"validation.md",
	"pr-body.md",
	"context.md",
}

// AgentLister loads live Herdr agents.
type AgentLister interface {
	Agents() ([]herdr.Agent, error)
}

// Options supplies optional snapshot detail and external sources.
type Options struct {
	Now    func() time.Time
	GitHub Fetcher
	// PRIndex overrides the authoritative pull request lifecycle source. When
	// it is nil, snapshots resolve the same GitHub state the strict CLI uses.
	PRIndex       PRIndex
	Agents        AgentLister
	ArtifactLimit int64
	DetailItem    string
}

// Build constructs a read-only snapshot of an active or archived program.
func Build(slug string, options Options) (Snapshot, error) {
	path, err := program.Find(slug)
	if err != nil {
		return Snapshot{}, err
	}
	p, err := program.Load(path)
	if err != nil {
		return Snapshot{}, err
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	limit := options.ArtifactLimit
	if limit <= 0 {
		limit = defaultArtifactLimit
	}
	programDir := filepath.Dir(path)
	snapshot := emptySnapshot()
	generatedAt := now().UTC()
	snapshot.Schema = SchemaVersion
	snapshot.GeneratedAt = generatedAt.Format(time.RFC3339)
	snapshot.Program = programDTO(p, path)
	snapshot.Patrol = patrolDTO(p.Slug, p.Agent, &snapshot)
	detailItem := selectedItem(p.Items, options.DetailItem)
	if options.DetailItem != "" && detailItem == "" {
		snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf("detail item %q not found", options.DetailItem))
	}
	snapshot.DetailItem = detailItem

	options.GitHub = newMemoFetcher(options.GitHub)
	views, projectWarnings, err := projectViewsForSnapshot(p, options)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load child project views: %w", err)
	}
	for _, warning := range projectWarnings {
		addSourceWarning(&snapshot, &snapshot.SourceHealth.Projects, warning.Message)
	}
	observed, orphanIDs, reconcileErr := reconcileSnapshot(p, views, generatedAt)
	if reconcileErr != nil {
		addSourceWarning(&snapshot, &snapshot.SourceHealth.Projects, reconcileErr.Error())
	}
	plan := observed.Plan(views)

	snapshot.Progress = progressDTO(observed.Items)
	snapshot.Plan = planDTO(observed, plan, orphanIDs)
	snapshot.Graph = graphDTO(observed.Items)
	if snapshot.Graph.Cyclic {
		snapshot.Warnings = append(snapshot.Warnings, "program dependency graph contains a cycle")
	}
	snapshot.ProgramArtifacts = programArtifacts(programDir, limit, &snapshot.Warnings)
	snapshot.Program.DisplayTitle, snapshot.Program.Summary =
		displayIdentity(p.Title, artifactBody(snapshot.ProgramArtifacts, "goal.md"))
	snapshot.OpenDecisions, snapshot.ResolvedDecisions = decisionLists(observed.Decisions)

	agents := []herdr.Agent{}
	agentsErr := error(nil)
	if options.Agents != nil {
		snapshot.SourceHealth.Herdr.Status = "ok"
		agents, agentsErr = options.Agents.Agents()
		if agentsErr != nil {
			addSourceWarning(&snapshot, &snapshot.SourceHealth.Herdr, fmt.Sprintf("list Herdr agents: %v", agentsErr))
		}
	}
	if options.GitHub != nil {
		snapshot.SourceHealth.GitHub.Status = "ok"
	}
	orphaned := make(map[string]bool, len(orphanIDs))
	for _, id := range orphanIDs {
		orphaned[id] = true
	}
	snapshot.Items = buildItems(
		context.Background(), observed, plan, orphaned, detailItem, limit,
		options.GitHub, agents, agentsErr, &snapshot,
	)
	snapshot.Contracts = buildContracts(programDir, observed.Contracts, selectedContractRefs(observed.Items, detailItem), limit, &snapshot.Warnings)
	return snapshot, nil
}

// projectViewsForSnapshot resolves child state with the same authoritative
// GitHub lifecycle overlay the strict CLI applies, so read-only surfaces report
// identical capacity, status, and readiness. A configured pull request fetcher
// is reused as the lifecycle source instead of running a second GitHub query.
func projectViewsForSnapshot(p program.Program, options Options) ([]program.ProjectView, []ProjectWarning, error) {
	index := options.PRIndex
	if index == nil {
		index = NewFetcherPRIndex(context.Background(), p.Repo, options.GitHub)
	}
	if index != nil {
		return ProjectViewsWithPRIndex(p, index)
	}
	return ProjectViews(p)
}

func emptySnapshot() Snapshot {
	return Snapshot{
		Items:             []ItemDTO{},
		Contracts:         []ContractDTO{},
		OpenDecisions:     []DecisionDTO{},
		ResolvedDecisions: []DecisionDTO{},
		ProgramArtifacts:  []ArtifactDTO{},
		Warnings:          []string{},
		Plan: PlanDTO{
			Ready:         []string{},
			InFlight:      []string{},
			Blocked:       []BlockedPlanDTO{},
			Orphaned:      []string{},
			OpenDecisions: []string{},
		},
		Graph: GraphDTO{
			Nodes:  []GraphNodeDTO{},
			Edges:  []GraphEdgeDTO{},
			Layers: [][]string{},
		},
		SourceHealth: SourceHealthDTO{
			Projects: newSource("ok"),
			GitHub:   newSource("unavailable"),
			Herdr:    newSource("unavailable"),
			Mailbox:  newSource("ok"),
			Patrol:   newSource("ok"),
		},
		Patrol: PatrolDTO{Status: "not-running", Reasons: []PatrolReasonDTO{}},
	}
}

func patrolDTO(slug, agentName string, snapshot *Snapshot) PatrolDTO {
	result := PatrolDTO{Status: "not-running", Reasons: []PatrolReasonDTO{}}
	capabilityWarning := patrolAgentWarning(agentName)
	if capabilityWarning != "" {
		result.Warning = capabilityWarning
		addSourceWarning(snapshot, &snapshot.SourceHealth.Patrol, capabilityWarning)
	}
	lockPath := filepath.Join(program.RelayDir(), "run", slug, "patrol.lock")
	running, err := patrollock.IsHeld(lockPath)
	if err != nil {
		addSourceWarning(snapshot, &snapshot.SourceHealth.Patrol, fmt.Sprintf("inspect patrol lock %s: %v", lockPath, err))
		return result
	}
	if running {
		result.Status = "running"
		result.Running = true
	}
	path := filepath.Join(program.RelayDir(), "run", slug, "patrol.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if running {
				addSourceWarning(snapshot, &snapshot.SourceHealth.Patrol, fmt.Sprintf("patrol state %s is missing while lock is held", path))
			}
			return result
		}
		addSourceWarning(snapshot, &snapshot.SourceHealth.Patrol, fmt.Sprintf("read patrol state %s: %v", path, err))
		return result
	}
	// The inline decoder mirrors patrol.State's own compatibility rule. It
	// cannot reuse the type because patrol depends on programview, so decoding
	// here keeps the read-only view free of a package cycle. Pointer fields
	// distinguish an absent flag from an explicit false: the canonical
	// `tl_present` wins whenever both are present.
	var state struct {
		ProgramSlug        string            `json:"program_slug"`
		Status             string            `json:"status"`
		DelaySeconds       int64             `json:"delay_seconds"`
		LastTickAt         string            `json:"last_tick_at"`
		NextTickAt         string            `json:"next_tick_at"`
		Reasons            []PatrolReasonDTO `json:"reasons"`
		TLPresent          *bool             `json:"tl_present"`
		CTOPresent         *bool             `json:"cto_present"`
		DoorbellSuppressed bool              `json:"doorbell_suppressed"`
		LastTurnStatus     string            `json:"last_turn_status"`
		LastTurnSessionID  string            `json:"last_turn_session_id"`
		LastTurnLogPath    string            `json:"last_turn_log_path"`
		LastTurnStartedAt  string            `json:"last_turn_started_at"`
		LastTurnEndedAt    string            `json:"last_turn_ended_at"`
		LastTurnError      string            `json:"last_turn_error"`
		TurnFailures       int               `json:"turn_failures"`
		Error              string            `json:"error"`
		Warning            string            `json:"warning"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		addSourceWarning(snapshot, &snapshot.SourceHealth.Patrol, fmt.Sprintf("parse patrol state %s: %v", path, err))
		return result
	}
	if state.ProgramSlug != slug {
		addSourceWarning(
			snapshot,
			&snapshot.SourceHealth.Patrol,
			fmt.Sprintf("patrol state %s belongs to program %q, not %q", path, state.ProgramSlug, slug),
		)
		return result
	}
	reasons := append([]PatrolReasonDTO(nil), state.Reasons...)
	if reasons == nil {
		reasons = []PatrolReasonDTO{}
	}
	if state.Warning != "" {
		addSourceWarning(snapshot, &snapshot.SourceHealth.Patrol, state.Warning)
	}
	status := "not-running"
	if running {
		status = "running"
	}
	return PatrolDTO{
		Status: status, Running: running,
		DelaySeconds: state.DelaySeconds, LastTickAt: state.LastTickAt,
		NextTickAt: state.NextTickAt, Reasons: reasons,
		TLPresent:          running && firstBool(state.TLPresent, state.CTOPresent) && capabilityWarning == "",
		DoorbellSuppressed: state.DoorbellSuppressed, Error: state.Error,
		Turn: PatrolTurnDTO{
			Status:    state.LastTurnStatus,
			SessionID: state.LastTurnSessionID,
			LogPath:   state.LastTurnLogPath,
			StartedAt: state.LastTurnStartedAt,
			EndedAt:   state.LastTurnEndedAt,
			Error:     state.LastTurnError,
			Failures:  state.TurnFailures,
		},
		Warning: strings.Join(nonEmptyStrings(capabilityWarning, state.Warning), "; "),
	}
}

func patrolAgentWarning(name string) string {
	a, err := agent.Get(name)
	if err != nil {
		return fmt.Sprintf("patrol cannot validate tech lead identity for agent %q: %v", name, err)
	}
	capabilities := a.Capabilities()
	if !capabilities.NamedSessions {
		return fmt.Sprintf(
			"patrol cannot notify the tech lead for agent %s because its launch adapter cannot carry named sessions",
			a.Name(),
		)
	}
	return ""
}

// firstBool resolves the canonical presence flag ahead of the retired one, so
// a patrol record that somehow carries both is read the same way patrol reads
// it.
func firstBool(values ...*bool) bool {
	for _, value := range values {
		if value != nil {
			return *value
		}
	}
	return false
}

func nonEmptyStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}

func newSource(status string) SourceDTO {
	return SourceDTO{Status: status, Warnings: []string{}}
}

func programDTO(p program.Program, path string) ProgramDTO {
	return ProgramDTO{
		Revision:            p.Revision,
		Slug:                p.Slug,
		Title:               p.Title,
		Repo:                p.Repo,
		State:               string(p.State),
		Agent:               p.Agent,
		MaxOpenPRs:          p.MaxOpenPRs,
		Archived:            pathWithinRoot(path, program.ArchivedDir()),
		CreatedAt:           p.CreatedAt,
		UpdatedAt:           p.UpdatedAt,
		ApprovalRequestedAt: p.ApprovalRequestedAt,
		ApprovedAt:          p.ApprovedAt,
		ApprovedBy:          p.ApprovedBy,
		HeldAt:              p.HeldAt,
		CompletedAt:         p.CompletedAt,
		AbandonedAt:         p.AbandonedAt,
	}
}

func progressDTO(items []program.WorkItem) ProgressDTO {
	progress := ProgressDTO{Total: len(items)}
	for _, item := range items {
		switch item.Status {
		case program.ItemPending:
			progress.Pending++
		case program.ItemDispatched:
			progress.Dispatched++
		case program.ItemInReview:
			progress.InReview++
		case program.ItemBlocked:
			progress.Blocked++
		case program.ItemMerged:
			progress.Merged++
		case program.ItemCancelled:
			progress.Canceled++
		}
	}
	progress.Completed = progress.Merged + progress.Canceled
	if progress.Total > 0 {
		progress.Percent = progress.Completed * 100 / progress.Total
	}
	return progress
}

func selectedItem(items []program.WorkItem, requested string) string {
	for _, item := range items {
		if item.ID == requested {
			return requested
		}
	}
	return ""
}

func reconcileSnapshot(p program.Program, views []program.ProjectView, now time.Time) (program.Program, []string, error) {
	observed := p
	originalState := observed.State
	if originalState == program.StateCompleted || originalState == program.StateAbandoned {
		observed.State = program.StateDraft
	}
	before := make(map[string]program.ItemStatus, len(observed.Items))
	for _, item := range observed.Items {
		before[item.ID] = item.Status
	}
	result, err := observed.Reconcile(views)
	observed.State = originalState
	if err != nil {
		return p, []string{}, fmt.Errorf("reconcile child project views: %w", err)
	}
	timestamp := now.UTC().Format(time.RFC3339)
	for i := range observed.Items {
		item := &observed.Items[i]
		if before[item.ID] == item.Status {
			continue
		}
		item.UpdatedAt = timestamp
		switch item.Status {
		case program.ItemInReview:
			item.InReviewAt = timestamp
		case program.ItemMerged:
			item.MergedAt = timestamp
		}
	}
	return observed, nonNilStrings(result.OrphanIDs), nil
}

func planDTO(p program.Program, view program.View, orphanIDs []string) PlanDTO {
	result := PlanDTO{
		Ready:         itemIDs(view.Ready),
		InFlight:      itemIDs(view.InFlight),
		Blocked:       make([]BlockedPlanDTO, 0, len(view.Blocked)),
		Orphaned:      nonNilStrings(orphanIDs),
		OpenDecisions: make([]string, 0, len(view.OpenDecisions)),
		Capacity: CapacityDTO{
			Limit:     view.Capacity.Limit,
			Open:      view.Capacity.Open,
			Reserved:  view.Capacity.Reserved,
			Available: view.Capacity.Available,
		},
		NextAction:  view.NextAction,
		NextCommand: NextCommand(p, view),
	}
	for _, blocked := range view.Blocked {
		result.Blocked = append(result.Blocked, BlockedPlanDTO{
			ItemID:  blocked.Item.ID,
			Reasons: nonNilStrings(blocked.Reasons),
		})
	}
	for _, decision := range view.OpenDecisions {
		result.OpenDecisions = append(result.OpenDecisions, decision.ID)
	}
	return result
}

func itemIDs(items []program.WorkItem) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.ID)
	}
	return result
}

func decisionLists(decisions []program.Decision) ([]DecisionDTO, []DecisionDTO) {
	open := []DecisionDTO{}
	resolved := []DecisionDTO{}
	for _, decision := range decisions {
		dto := decisionDTO(decision)
		if decision.ResolvedAt == "" {
			open = append(open, dto)
		} else {
			resolved = append(resolved, dto)
		}
	}
	sort.Slice(open, func(i, j int) bool { return numberedID(open[i].ID) < numberedID(open[j].ID) })
	sort.Slice(resolved, func(i, j int) bool { return numberedID(resolved[i].ID) < numberedID(resolved[j].ID) })
	return open, resolved
}

func decisionDTO(decision program.Decision) DecisionDTO {
	options := append([]string(nil), decision.Options...)
	if options == nil {
		options = []string{}
	}
	return DecisionDTO{
		ID:          decision.ID,
		Kind:        string(decision.Kind),
		RaisedBy:    string(decision.RaisedBy),
		ItemID:      decision.ItemID,
		ContractRef: decision.ContractRef,
		Question:    decision.Question,
		Options:     options,
		Answer:      decision.Answer,
		ResolvedBy:  decision.ResolvedBy,
		CreatedAt:   decision.CreatedAt,
		ResolvedAt:  decision.ResolvedAt,
	}
}

func buildItems(
	ctx context.Context,
	p program.Program,
	plan program.View,
	orphaned map[string]bool,
	detailItem string,
	limit int64,
	github Fetcher,
	agents []herdr.Agent,
	agentsErr error,
	snapshot *Snapshot,
) []ItemDTO {
	items := append([]program.WorkItem(nil), p.Items...)
	sort.Slice(items, func(i, j int) bool { return numberedID(items[i].ID) < numberedID(items[j].ID) })
	dependents := dependentIDs(items)
	ready := make(map[string]bool, len(plan.Ready))
	reasons := make(map[string][]string, len(plan.Blocked))
	for _, item := range plan.Ready {
		ready[item.ID] = true
	}
	for _, blocked := range plan.Blocked {
		reasons[blocked.Item.ID] = append([]string(nil), blocked.Reasons...)
	}

	result := make([]ItemDTO, 0, len(items))
	for _, item := range items {
		dto := ItemDTO{
			ID:           item.ID,
			Kind:         string(item.Kind),
			Title:        item.Title,
			Priority:     string(item.Priority),
			Status:       string(item.Status),
			Lane:         string(item.Status),
			Repo:         item.Repo,
			ProjectSlug:  item.ProjectSlug,
			Ready:        ready[item.ID],
			Orphaned:     orphaned[item.ID],
			Reasons:      nonNilStrings(reasons[item.ID]),
			Dependencies: nonNilStrings(item.Dependencies),
			Dependents:   nonNilStrings(dependents[item.ID]),
			Contracts:    nonNilStrings(item.ContractRefs),
			Notes:        nonNilStrings(item.Notes),
			Timestamps: ItemTimestampsDTO{
				CreatedAt:    item.CreatedAt,
				UpdatedAt:    item.UpdatedAt,
				DispatchedAt: item.DispatchedAt,
				InReviewAt:   item.InReviewAt,
				MergedAt:     item.MergedAt,
				CanceledAt:   item.CancelledAt,
			},
			Mailbox:   emptyMailbox(),
			Decisions: itemDecisions(p.Decisions, item.ID),
			Artifacts: missingChildArtifacts(),
			Warnings:  []string{},
		}
		if dto.Orphaned {
			dto.Reasons = append(dto.Reasons, "linked child project is missing or archived without verified merge")
		}
		if item.PRGrantedAt != "" {
			dto.Grant = &GrantDTO{GrantedAt: item.PRGrantedAt, GrantedBy: item.PRGrantedBy}
		}
		childDir, manifest, archived, childErr := loadChild(item.ProjectSlug)
		if childErr != nil && item.ProjectSlug != "" {
			dto.Warnings = append(dto.Warnings, childErr.Error())
		}
		if agentsErr != nil && item.ProjectSlug != "" {
			dto.Warnings = append(dto.Warnings, fmt.Sprintf("Herdr unavailable: %v", agentsErr))
		}
		if childErr == nil && item.ProjectSlug != "" {
			dto.Child = childDTO(manifest, childDir, archived, &dto.Warnings)
			dto.Artifacts = childArtifacts(childDir, detailItem == item.ID, limit, &dto.Warnings)
			dto.Mailbox = mailboxCounts(childDir, item.ID, snapshot, &dto.Warnings)
			dto.RecordedPR = recordedPullRequest(manifest, filepath.Join(childDir, "state.json"), item.PRRef, &dto.Warnings)
			worktree := ""
			if manifest.Worktree != nil {
				worktree = *manifest.Worktree
			}
			if agentsErr == nil {
				if agent, ok := herdr.FindLiveWorker(agents, manifest.Slug, manifest.Repo, worktree); ok {
					dto.Worker = workerDTO(agent)
				}
			}
		} else if item.PRRef != "" {
			dto.RecordedPR = pullRequestFromRef(item.PRRef)
		}
		if item.ProjectSlug != "" && childErr != nil {
			dto.Mailbox.Available = false
			addSourceWarning(snapshot, &snapshot.SourceHealth.Mailbox, fmt.Sprintf("item %s mailbox unavailable: child project %q not found", item.ID, item.ProjectSlug))
		}
		if github != nil && dto.RecordedPR != nil {
			live, err := github.Fetch(ctx, p.Repo, dto.RecordedPR.Ref)
			if err != nil {
				message := fmt.Sprintf("item %s GitHub refresh failed: %v", item.ID, err)
				dto.Warnings = append(dto.Warnings, message)
				addSourceWarning(snapshot, &snapshot.SourceHealth.GitHub, message)
				if live.Stale {
					dto.LivePR = &live
				}
			} else {
				dto.LivePR = &live
			}
		}
		result = append(result, dto)
	}
	return result
}

func dependentIDs(items []program.WorkItem) map[string][]string {
	result := make(map[string][]string, len(items))
	for _, item := range items {
		if _, exists := result[item.ID]; !exists {
			result[item.ID] = []string{}
		}
		for _, dependency := range item.Dependencies {
			result[dependency] = append(result[dependency], item.ID)
		}
	}
	for id := range result {
		sort.Slice(result[id], func(i, j int) bool {
			return numberedID(result[id][i]) < numberedID(result[id][j])
		})
	}
	return result
}

func itemDecisions(decisions []program.Decision, itemID string) []DecisionDTO {
	result := []DecisionDTO{}
	for _, decision := range decisions {
		if decision.ItemID == itemID {
			result = append(result, decisionDTO(decision))
		}
	}
	sort.Slice(result, func(i, j int) bool { return numberedID(result[i].ID) < numberedID(result[j].ID) })
	return result
}

func loadChild(slug string) (string, project.Manifest, bool, error) {
	if slug == "" {
		return "", project.Manifest{}, false, nil
	}
	path, err := project.Find(slug)
	if err != nil {
		return "", project.Manifest{}, false, fmt.Errorf("load child project %q: %w", slug, err)
	}
	manifest, err := project.Load(path)
	if err != nil {
		return "", project.Manifest{}, false, err
	}
	return filepath.Dir(path), manifest, pathWithinRoot(path, project.ArchivedDir()), nil
}

func childDTO(manifest project.Manifest, childDir string, archived bool, warnings *[]string) *ChildDTO {
	worktree := ""
	if manifest.Worktree != nil {
		worktree = *manifest.Worktree
	}
	child := &ChildDTO{Manifest: ChildManifestDTO{
		Slug:       manifest.Slug,
		Title:      manifest.Title,
		Repo:       manifest.Repo,
		Branch:     manifest.Branch,
		BaseBranch: manifest.BaseBranch,
		StartSHA:   manifest.StartSHA,
		Worktree:   worktree,
		Status:     manifest.Status,
		Workflow:   manifest.Workflow,
		Phase:      manifest.Phase,
		Merged:     manifest.Merged,
		Archived:   archived,
		CreatedAt:  manifest.Created,
		UpdatedAt:  manifest.Updated,
	}}
	statePath := filepath.Join(childDir, "state.json")
	state, err := project.LoadState(statePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			*warnings = append(*warnings, fmt.Sprintf("load child workflow state %s: %v", statePath, err))
		}
		return child
	}
	phases := make([]WorkflowPhaseDTO, 0, len(state.Order))
	current := ""
	for _, name := range state.Order {
		phase := state.Phases[name]
		phases = append(phases, WorkflowPhaseDTO{
			Name: name, Status: phase.Status, Artifact: phase.Artifact, Task: phase.Task,
		})
		if current == "" && phase.Status != project.PhaseDone {
			current = name
		}
	}
	child.Workflow = &WorkflowStateDTO{
		Workflow: state.Workflow, CurrentPhase: current, Order: nonNilStrings(state.Order),
		Phases: phases, UpdatedAt: state.Updated,
	}
	return child
}

func mailboxCounts(childDir, itemID string, snapshot *Snapshot, warnings *[]string) MailboxDTO {
	inbox, inboxErr := mailbox.List(childDir, mailbox.Inbox)
	outbox, outboxErr := mailbox.List(childDir, mailbox.Outbox)
	if inboxErr == nil && outboxErr == nil {
		return MailboxDTO{
			Available: true,
			Inbox:     len(inbox), Outbox: len(outbox),
			InboxIDs: sortedMessageIDs(inbox), OutboxIDs: sortedMessageIDs(outbox),
		}
	}
	if missingMailboxOnly(inboxErr, outboxErr) {
		return emptyMailbox()
	}
	var messages []string
	if inboxErr != nil {
		messages = append(messages, inboxErr.Error())
	}
	if outboxErr != nil {
		messages = append(messages, outboxErr.Error())
	}
	message := fmt.Sprintf("item %s mailbox unavailable: %s", itemID, strings.Join(messages, "; "))
	*warnings = append(*warnings, message)
	addSourceWarning(snapshot, &snapshot.SourceHealth.Mailbox, message)
	return emptyMailbox()
}

func emptyMailbox() MailboxDTO {
	return MailboxDTO{InboxIDs: []string{}, OutboxIDs: []string{}}
}

func sortedMessageIDs(messages []mailbox.Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	sort.Strings(ids)
	return ids
}

func missingMailboxOnly(errs ...error) bool {
	missing := false
	for _, err := range errs {
		if err == nil {
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false
		}
		missing = true
	}
	return missing
}

func recordedPullRequest(manifest project.Manifest, statePath, fallback string, warnings *[]string) *PullRequestDTO {
	hasPR, ref, err := RecordedPR(manifest, statePath)
	if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("load recorded PR for %s: %v", manifest.Slug, err))
		ref = fallback
		hasPR = ref != ""
	}
	if !hasPR && fallback != "" {
		ref = fallback
		hasPR = true
	}
	if !hasPR {
		return nil
	}
	result := pullRequestFromRef(ref)
	state, stateErr := project.LoadState(statePath)
	if stateErr == nil {
		if state.PR.Number > 0 {
			result.Number = state.PR.Number
		}
		if state.PR.URL != "" {
			result.URL = state.PR.URL
		}
	}
	if manifest.PR.Number != nil && result.Number == 0 {
		result.Number = *manifest.PR.Number
	}
	if manifest.PR.URL != nil && result.URL == "" {
		result.URL = *manifest.PR.URL
	}
	if manifest.PR.CIStatus != nil {
		result.Checks = *manifest.PR.CIStatus
	}
	return result
}

func pullRequestFromRef(ref string) *PullRequestDTO {
	result := &PullRequestDTO{Ref: ref, Checks: "unknown"}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		result.URL = ref
	}
	number := strings.TrimPrefix(ref, "#")
	if parsed, err := strconv.Atoi(number); err == nil {
		result.Number = parsed
	}
	return result
}

func workerDTO(agent herdr.Agent) *WorkerDTO {
	return &WorkerDTO{
		Status: string(agent.Status), PaneID: agent.PaneID, TabID: agent.TabID,
		WorkspaceID: agent.WorkspaceID, TerminalTitle: agent.TerminalTitle,
		CWD: agent.CWD, ForegroundCWD: agent.ForegroundCWD,
		NativeSessionID: agent.NativeSessionID,
	}
}

func buildContracts(
	programDir string,
	contracts []program.Contract,
	selected map[string]bool,
	limit int64,
	warnings *[]string,
) []ContractDTO {
	result := make([]ContractDTO, 0, len(contracts))
	for _, contract := range contracts {
		artifact, err := readArtifact(programDir, contract.Path, limit, selected[contract.Ref])
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("contract %s: %v", contract.Ref, err))
		}
		result = append(result, ContractDTO{
			Name: contract.Name, Version: contract.Version, Ref: contract.Ref,
			Path: contract.Path, SHA256: contract.SHA256, Status: string(contract.Status),
			PublishedAt: contract.PublishedAt, ApprovedAt: contract.ApprovedAt,
			ApprovedBy: contract.ApprovedBy, RejectedAt: contract.RejectedAt,
			RejectedBy: contract.RejectedBy, RejectionReason: contract.RejectionReason,
			Artifact: artifact,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Ref < result[j].Ref })
	return result
}

func selectedContractRefs(items []program.WorkItem, detailItem string) map[string]bool {
	result := make(map[string]bool)
	for _, item := range items {
		if item.ID != detailItem {
			continue
		}
		for _, ref := range item.ContractRefs {
			result[ref] = true
		}
	}
	return result
}

func nonNilStrings(values []string) []string {
	result := append([]string(nil), values...)
	if result == nil {
		return []string{}
	}
	return result
}

func addSourceWarning(snapshot *Snapshot, source *SourceDTO, warning string) {
	source.Status = "degraded"
	source.Warnings = append(source.Warnings, warning)
	snapshot.Warnings = append(snapshot.Warnings, warning)
}

func numberedID(id string) int {
	number, err := strconv.Atoi(strings.TrimLeft(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"))
	if err != nil {
		return 0
	}
	return number
}

func programArtifacts(programDir string, limit int64, warnings *[]string) []ArtifactDTO {
	artifacts := make([]ArtifactDTO, 0, 3)
	for _, name := range []string{"goal.md", "decisions.md", "progress.md"} {
		artifact, err := readArtifact(programDir, name, limit, true)
		if err != nil {
			*warnings = append(*warnings, err.Error())
		}
		if artifact.Text == nil {
			artifact.Text = stringPointer("")
		}
		if name == "goal.md" && !artifact.Present {
			*warnings = append(*warnings, "program artifact goal.md is missing")
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts
}

// artifactBody returns the text of one program artifact, or "" when the file
// is absent or was not loaded.
func artifactBody(artifacts []ArtifactDTO, name string) string {
	for _, artifact := range artifacts {
		if artifact.Name == name && artifact.Present && artifact.Text != nil {
			return *artifact.Text
		}
	}
	return ""
}

func missingChildArtifacts() []ArtifactDTO {
	result := make([]ArtifactDTO, 0, len(childArtifactNames))
	for _, name := range childArtifactNames {
		result = append(result, ArtifactDTO{Name: name, Path: name})
	}
	return result
}

func childArtifacts(childDir string, includeText bool, limit int64, warnings *[]string) []ArtifactDTO {
	result := make([]ArtifactDTO, 0, len(childArtifactNames))
	for _, name := range childArtifactNames {
		artifact, err := readArtifact(childDir, name, limit, includeText)
		if err != nil {
			*warnings = append(*warnings, err.Error())
		}
		result = append(result, artifact)
	}
	return result
}

func readArtifact(root, relative string, limit int64, includeText bool) (ArtifactDTO, error) {
	artifact := ArtifactDTO{Name: filepath.Base(relative), Path: filepath.ToSlash(relative)}
	path, err := containedPath(root, relative)
	if err != nil {
		return artifact, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return artifact, nil
		}
		return artifact, fmt.Errorf("read artifact metadata %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return artifact, fmt.Errorf("read artifact %s: not a regular file", path)
	}
	artifact.Present = true
	artifact.Size = info.Size()
	artifact.UpdatedAt = info.ModTime().UTC().Format(time.RFC3339Nano)
	if !includeText {
		return artifact, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return artifact, fmt.Errorf("read artifact %s: %w", path, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return artifact, fmt.Errorf("read artifact %s: %w", path, err)
	}
	artifact.Truncated = info.Size() > limit
	artifact.Text = stringPointer(string(data))
	return artifact, nil
}

func containedPath(root, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("artifact path %q must be relative", relative)
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve artifact root %s: %w", root, err)
	}
	if resolvedRoot, resolveErr := filepath.EvalSymlinks(cleanRoot); resolveErr == nil {
		cleanRoot = resolvedRoot
	}
	path := filepath.Join(cleanRoot, filepath.Clean(filepath.FromSlash(relative)))
	if !pathWithinRoot(path, cleanRoot) {
		return "", fmt.Errorf("artifact path %q escapes %s", relative, cleanRoot)
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && !pathWithinRoot(resolved, cleanRoot) {
		return "", fmt.Errorf("artifact path %q resolves outside %s", relative, cleanRoot)
	}
	return path, nil
}

func pathWithinRoot(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func stringPointer(value string) *string {
	return &value
}
