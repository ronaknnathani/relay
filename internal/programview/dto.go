package programview

// SchemaVersion identifies the program snapshot JSON contract.
const SchemaVersion = "relay.program.v1"

// Snapshot is the complete read-only JSON model for one Relay program.
type Snapshot struct {
	Schema            string          `json:"schema"`
	GeneratedAt       string          `json:"generated_at"`
	DetailItem        string          `json:"detail_item"`
	Program           ProgramDTO      `json:"program"`
	Patrol            PatrolDTO       `json:"patrol"`
	Progress          ProgressDTO     `json:"progress"`
	Plan              PlanDTO         `json:"plan"`
	Graph             GraphDTO        `json:"graph"`
	Items             []ItemDTO       `json:"items"`
	Contracts         []ContractDTO   `json:"contracts"`
	OpenDecisions     []DecisionDTO   `json:"open_decisions"`
	ResolvedDecisions []DecisionDTO   `json:"resolved_decisions"`
	ProgramArtifacts  []ArtifactDTO   `json:"program_artifacts"`
	Warnings          []string        `json:"warnings"`
	SourceHealth      SourceHealthDTO `json:"source_health"`
}

// PatrolDTO contains the read-only adaptive patrol runtime summary.
type PatrolDTO struct {
	Status             string            `json:"status"`
	Running            bool              `json:"running"`
	DelaySeconds       int64             `json:"delay_seconds"`
	LastTickAt         string            `json:"last_tick_at"`
	NextTickAt         string            `json:"next_tick_at"`
	Reasons            []PatrolReasonDTO `json:"reasons"`
	CTOPresent         bool              `json:"cto_present"`
	DoorbellSuppressed bool              `json:"doorbell_suppressed"`
	Turn               PatrolTurnDTO     `json:"turn"`
	Error              string            `json:"error"`
	Warning            string            `json:"warning"`
}

// PatrolTurnDTO summarizes the last live CTO doorbell attempt. Session and log
// fields remain for backward-compatible decoding of older patrol state.
type PatrolTurnDTO struct {
	Status    string `json:"status"`
	SessionID string `json:"session_id"`
	LogPath   string `json:"log_path"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Error     string `json:"error"`
	Failures  int    `json:"failures"`
}

// PatrolReasonDTO explains why the patrol selected its current cadence.
type PatrolReasonDTO struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// ProgramDTO contains durable program metadata.
type ProgramDTO struct {
	Revision            int    `json:"revision"`
	Slug                string `json:"slug"`
	Title               string `json:"title"`
	DisplayTitle        string `json:"display_title"`
	Summary             string `json:"summary"`
	Repo                string `json:"repo"`
	State               string `json:"state"`
	Agent               string `json:"agent"`
	MaxOpenPRs          int    `json:"max_open_prs"`
	Archived            bool   `json:"archived"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
	ApprovalRequestedAt string `json:"approval_requested_at"`
	ApprovedAt          string `json:"approved_at"`
	ApprovedBy          string `json:"approved_by"`
	HeldAt              string `json:"held_at"`
	CompletedAt         string `json:"completed_at"`
	AbandonedAt         string `json:"abandoned_at"`
}

// ProgressDTO contains aggregate item counts.
type ProgressDTO struct {
	Total      int `json:"total"`
	Pending    int `json:"pending"`
	Dispatched int `json:"dispatched"`
	InReview   int `json:"in_review"`
	Blocked    int `json:"blocked"`
	Merged     int `json:"merged"`
	Canceled   int `json:"cancelled"` //nolint:misspell // Persisted V1 schema spelling.
	Completed  int `json:"completed"`
	Percent    int `json:"percent"`
}

// PlanDTO contains the derived governance queue.
type PlanDTO struct {
	Ready         []string         `json:"ready"`
	InFlight      []string         `json:"in_flight"`
	Blocked       []BlockedPlanDTO `json:"blocked"`
	Orphaned      []string         `json:"orphaned"`
	OpenDecisions []string         `json:"open_decisions"`
	Capacity      CapacityDTO      `json:"capacity"`
	NextAction    string           `json:"next_action"`
	NextCommand   string           `json:"next_command"`
}

// BlockedPlanDTO identifies a blocked item and its reasons.
type BlockedPlanDTO struct {
	ItemID  string   `json:"item_id"`
	Reasons []string `json:"reasons"`
}

// CapacityDTO contains pull request capacity.
type CapacityDTO struct {
	Limit     int `json:"limit"`
	Open      int `json:"open"`
	Reserved  int `json:"reserved"`
	Available int `json:"available"`
}

// GraphDTO contains a stable dependency graph layout.
type GraphDTO struct {
	Nodes  []GraphNodeDTO `json:"nodes"`
	Edges  []GraphEdgeDTO `json:"edges"`
	Layers [][]string     `json:"layers"`
	Cyclic bool           `json:"cyclic"`
}

// GraphNodeDTO is one graph node.
type GraphNodeDTO struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Lane  string `json:"lane"`
	Layer int    `json:"layer"`
}

// GraphEdgeDTO is a dependency-to-dependent edge.
type GraphEdgeDTO struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ItemDTO contains durable and observed work item state.
type ItemDTO struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	Title        string            `json:"title"`
	Priority     string            `json:"priority"`
	Status       string            `json:"status"`
	Lane         string            `json:"lane"`
	Repo         string            `json:"repo"`
	ProjectSlug  string            `json:"project_slug"`
	Ready        bool              `json:"ready"`
	Orphaned     bool              `json:"orphaned"`
	Reasons      []string          `json:"reasons"`
	Dependencies []string          `json:"dependencies"`
	Dependents   []string          `json:"dependents"`
	Contracts    []string          `json:"contracts"`
	Notes        []string          `json:"notes"`
	Timestamps   ItemTimestampsDTO `json:"timestamps"`
	Grant        *GrantDTO         `json:"grant,omitempty"`
	Child        *ChildDTO         `json:"child,omitempty"`
	RecordedPR   *PullRequestDTO   `json:"recorded_pr,omitempty"`
	LivePR       *PullRequestDTO   `json:"live_pr,omitempty"`
	Worker       *WorkerDTO        `json:"worker,omitempty"`
	Mailbox      MailboxDTO        `json:"mailbox"`
	Decisions    []DecisionDTO     `json:"decisions"`
	Artifacts    []ArtifactDTO     `json:"artifacts"`
	Warnings     []string          `json:"warnings"`
}

// ItemTimestampsDTO contains work item lifecycle timestamps.
type ItemTimestampsDTO struct {
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	DispatchedAt string `json:"dispatched_at"`
	InReviewAt   string `json:"in_review_at"`
	MergedAt     string `json:"merged_at"`
	CanceledAt   string `json:"cancelled_at"` //nolint:misspell // Persisted V1 schema spelling.
}

// GrantDTO contains a recorded open-PR grant.
type GrantDTO struct {
	GrantedAt string `json:"granted_at"`
	GrantedBy string `json:"granted_by"`
}

// ChildDTO contains a linked Relay project manifest and workflow state.
type ChildDTO struct {
	Manifest ChildManifestDTO  `json:"manifest"`
	Workflow *WorkflowStateDTO `json:"workflow,omitempty"`
}

// ChildManifestDTO contains linked project metadata.
type ChildManifestDTO struct {
	Slug       string `json:"slug"`
	Title      string `json:"title"`
	Repo       string `json:"repo"`
	Branch     string `json:"branch"`
	BaseBranch string `json:"base_branch"`
	StartSHA   string `json:"start_sha"`
	Worktree   string `json:"worktree"`
	Status     string `json:"status"`
	Workflow   string `json:"workflow"`
	Phase      string `json:"phase"`
	Merged     bool   `json:"merged"`
	Archived   bool   `json:"archived"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// WorkflowStateDTO contains the current child workflow phase state.
type WorkflowStateDTO struct {
	Workflow     string             `json:"workflow"`
	CurrentPhase string             `json:"current_phase"`
	Order        []string           `json:"order"`
	Phases       []WorkflowPhaseDTO `json:"phases"`
	UpdatedAt    string             `json:"updated_at"`
}

// WorkflowPhaseDTO is one ordered workflow phase.
type WorkflowPhaseDTO struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Artifact string `json:"artifact"`
	Task     string `json:"task"`
}

// PullRequestDTO contains a recorded or live pull request.
type PullRequestDTO struct {
	Number         int    `json:"number"`
	Ref            string `json:"ref"`
	URL            string `json:"url"`
	State          string `json:"state"`
	Draft          bool   `json:"draft"`
	Mergeable      string `json:"mergeable"`
	ReviewDecision string `json:"review_decision"`
	Checks         string `json:"checks"`
	Title          string `json:"title"`
	UpdatedAt      string `json:"updated_at"`
	Stale          bool   `json:"stale"`
	FetchedAt      string `json:"fetched_at"`
	StaleReason    string `json:"stale_reason"`
}

// WorkerDTO contains a matched live Herdr worker.
type WorkerDTO struct {
	Status          string `json:"status"`
	PaneID          string `json:"pane_id"`
	TabID           string `json:"tab_id"`
	WorkspaceID     string `json:"workspace_id"`
	TerminalTitle   string `json:"terminal_title"`
	CWD             string `json:"cwd"`
	ForegroundCWD   string `json:"foreground_cwd"`
	NativeSessionID string `json:"native_session_id"`
}

// MailboxDTO contains unread child mailbox counts and the exact unread message
// identifiers. The identifiers let the patrol notice a second message on an
// item whose unread count is unchanged.
type MailboxDTO struct {
	Available bool     `json:"available"`
	Inbox     int      `json:"inbox"`
	Outbox    int      `json:"outbox"`
	InboxIDs  []string `json:"inbox_ids"`
	OutboxIDs []string `json:"outbox_ids"`
}

// DecisionDTO contains an open or resolved governance decision.
type DecisionDTO struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	RaisedBy    string   `json:"raised_by"`
	ItemID      string   `json:"item_id"`
	ContractRef string   `json:"contract_ref"`
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	Answer      string   `json:"answer"`
	ResolvedBy  string   `json:"resolved_by"`
	CreatedAt   string   `json:"created_at"`
	ResolvedAt  string   `json:"resolved_at"`
}

// ContractDTO contains contract metadata and optional selected content.
type ContractDTO struct {
	Name            string      `json:"name"`
	Version         int         `json:"version"`
	Ref             string      `json:"ref"`
	Path            string      `json:"path"`
	SHA256          string      `json:"sha256"`
	Status          string      `json:"status"`
	PublishedAt     string      `json:"published_at"`
	ApprovedAt      string      `json:"approved_at"`
	ApprovedBy      string      `json:"approved_by"`
	RejectedAt      string      `json:"rejected_at"`
	RejectedBy      string      `json:"rejected_by"`
	RejectionReason string      `json:"rejection_reason"`
	Artifact        ArtifactDTO `json:"artifact"`
}

// ArtifactDTO contains safe filesystem metadata and optional text.
type ArtifactDTO struct {
	Name      string  `json:"name"`
	Path      string  `json:"path"`
	Present   bool    `json:"present"`
	Size      int64   `json:"size"`
	UpdatedAt string  `json:"updated_at"`
	Truncated bool    `json:"truncated"`
	Text      *string `json:"text,omitempty"`
}

// SourceHealthDTO reports whether optional local/external sources degraded.
type SourceHealthDTO struct {
	Projects SourceDTO `json:"projects"`
	GitHub   SourceDTO `json:"github"`
	Herdr    SourceDTO `json:"herdr"`
	Mailbox  SourceDTO `json:"mailbox"`
	Patrol   SourceDTO `json:"patrol"`
}

// SourceDTO reports one source status and warnings.
type SourceDTO struct {
	Status   string   `json:"status"`
	Warnings []string `json:"warnings"`
}
