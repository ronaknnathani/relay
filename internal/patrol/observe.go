package patrol

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
)

const (
	attentionDelay = 15 * time.Minute
	normalDelay    = 30 * time.Minute
)

// Observation is one read-only patrol assessment.
type Observation struct {
	ProgramSlug          string   `json:"program_slug"`
	ProgramState         string   `json:"program_state"`
	Stop                 bool     `json:"stop"`
	StopReason           string   `json:"stop_reason"`
	DelaySeconds         int64    `json:"delay_seconds"`
	Reasons              []Reason `json:"reasons"`
	AttentionKeys        []string `json:"attention_keys"`
	AttentionFingerprint string   `json:"attention_fingerprint"`
}

func observeSnapshot(snapshot programview.Snapshot, contractDrifts []Reason) Observation {
	observation := Observation{
		ProgramSlug:   snapshot.Program.Slug,
		ProgramState:  snapshot.Program.State,
		Reasons:       []Reason{},
		AttentionKeys: []string{},
	}
	if snapshot.Program.Archived {
		observation.Stop = true
		observation.StopReason = "program archived"
		return observation
	}
	switch program.State(snapshot.Program.State) {
	case program.StateCompleted:
		observation.Stop = true
		observation.StopReason = "program completed"
		return observation
	case program.StateAbandoned:
		observation.Stop = true
		observation.StopReason = "program abandoned"
		return observation
	}

	reasons := make(map[string]Reason)
	keys := make(map[string]string)
	add := func(code, text string) {
		reasons[code] = Reason{Code: code, Text: text}
		keys[code] = code
	}
	// addKeyed keeps the human-facing reason code stable while letting the
	// attention fingerprint depend on finer-grained durable identity.
	addKeyed := func(code, key, text string) {
		reasons[code] = Reason{Code: code, Text: text}
		keys[code] = key
	}
	state := program.State(snapshot.Program.State)
	for _, decision := range snapshot.OpenDecisions {
		add("open-decision:"+decision.ID, fmt.Sprintf("Decision %s is awaiting resolution.", decision.ID))
	}
	if state == program.StatePendingApproval {
		add("awaiting-approval", "Program approval is awaiting the CEO.")
	}
	for _, item := range snapshot.Items {
		if item.ProjectSlug != "" && item.Mailbox.Outbox > 0 {
			code := "unread-worker-outbox:" + item.ID
			addKeyed(
				code,
				code+":"+strings.Join(sortedCopy(item.Mailbox.OutboxIDs), ","),
				fmt.Sprintf("Item %s has %d unread worker outbox message(s).", item.ID, item.Mailbox.Outbox),
			)
		}
	}

	switch state {
	case program.StateActive:
		for _, id := range snapshot.Plan.Ready {
			add("ready-item:"+id, fmt.Sprintf("Item %s is ready to dispatch.", id))
		}
		for _, id := range snapshot.Plan.Orphaned {
			add("orphan-item:"+id, fmt.Sprintf("Item %s has an orphaned child project.", id))
		}
		addBlockedReasons(snapshot, add)
		addMissingWorkerReasons(snapshot, add)
		addMergedCleanupReasons(snapshot, add)
		for _, item := range snapshot.Items {
			if item.ProjectSlug == "" || item.Child == nil {
				continue
			}
			if item.Status != string(program.ItemDispatched) &&
				item.Status != string(program.ItemInReview) &&
				item.Status != string(program.ItemBlocked) {
				continue
			}
			phase := childPhase(item)
			if phase == "" || phase == "clarify" || phase == "plan" {
				label := phase
				if label == "" {
					label = "empty"
				}
				add(
					"early-child-phase:"+item.ID+":"+label,
					fmt.Sprintf("Item %s child is in the %s phase.", item.ID, label),
				)
			}
		}
		for _, warning := range snapshot.SourceHealth.Projects.Warnings {
			code := "project-warning:" + stableCode(warning)
			add(code, warning)
		}
		for _, drift := range contractDrifts {
			add(drift.Code, drift.Text)
		}
	case program.StateHeld:
		addBlockedReasons(snapshot, add)
		addMissingWorkerReasons(snapshot, add)
		addMergedCleanupReasons(snapshot, add)
	case program.StateDraft, program.StatePendingApproval:
		// Draft programs observe only mail and decisions; ready work is not
		// actionable before approval.
	}

	codes := make([]string, 0, len(reasons))
	for code := range reasons {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		observation.Reasons = append(observation.Reasons, reasons[code])
		observation.AttentionKeys = append(observation.AttentionKeys, keys[code])
	}
	if len(observation.AttentionKeys) > 0 {
		sum := sha256.Sum256([]byte(strings.Join(observation.AttentionKeys, "\n")))
		observation.AttentionFingerprint = fmt.Sprintf("%x", sum[:])
	}
	switch {
	case state == program.StateDraft || state == program.StatePendingApproval:
		observation.DelaySeconds = int64(normalDelay / time.Second)
	case len(observation.AttentionKeys) > 0:
		observation.DelaySeconds = int64(attentionDelay / time.Second)
	default:
		observation.DelaySeconds = int64(normalDelay / time.Second)
	}
	return observation
}

func addBlockedReasons(snapshot programview.Snapshot, add func(string, string)) {
	for _, item := range snapshot.Items {
		if item.Status == string(program.ItemBlocked) {
			add("blocked-item:"+item.ID, fmt.Sprintf("Item %s is blocked.", item.ID))
		}
	}
}

func addMissingWorkerReasons(snapshot programview.Snapshot, add func(string, string)) {
	if snapshot.SourceHealth.Herdr.Status != "ok" {
		return
	}
	for _, item := range snapshot.Items {
		if item.Status != string(program.ItemDispatched) && item.Status != string(program.ItemInReview) {
			continue
		}
		if item.ProjectSlug != "" && item.Worker == nil {
			add("missing-worker:"+item.ID, fmt.Sprintf("Item %s has no live Herdr worker.", item.ID))
		}
	}
}

// addMergedCleanupReasons reports merged work that is still holding runtime.
//
// A merged item is finished, but until its watcher is stopped, its worker
// session and tab are gone, and its child project is archived, it is still
// occupying a Herdr tab, still polling GitHub, and still keeping a worktree and
// branch on disk. The reason stays until all of that is retired, so the tech
// lead is woken to run cleanup rather than accumulating dead sessions.
func addMergedCleanupReasons(snapshot programview.Snapshot, add func(string, string)) {
	for _, item := range snapshot.Items {
		if item.Status != string(program.ItemMerged) || item.ProjectSlug == "" {
			continue
		}
		outstanding := mergedCleanupOutstanding(item)
		if len(outstanding) == 0 {
			continue
		}
		add(
			"merged-worker-cleanup:"+item.ID,
			fmt.Sprintf("Item %s merged but still holds %s.", item.ID, strings.Join(outstanding, " and ")),
		)
	}
}

// mergedCleanupOutstanding names what a merged item has not yet released.
func mergedCleanupOutstanding(item programview.ItemDTO) []string {
	var outstanding []string
	if item.Worker != nil {
		outstanding = append(outstanding, "a live worker session")
	}
	if item.Child == nil {
		return outstanding
	}
	if !item.Child.Manifest.Archived {
		outstanding = append(outstanding, "an active child project")
	} else if item.Child.Manifest.WorktreePresent {
		outstanding = append(outstanding, "an uncleaned worktree")
	}
	if item.Child.Watcher != nil && item.Child.Watcher.Running {
		outstanding = append(outstanding, "a running pull request watcher")
	}
	return outstanding
}

func childPhase(item programview.ItemDTO) string {
	if item.Child.Workflow != nil {
		return strings.ToLower(strings.TrimSpace(item.Child.Workflow.CurrentPhase))
	}
	return strings.ToLower(strings.TrimSpace(item.Child.Manifest.Phase))
}

func stableCode(value string) string {
	var result strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			result.WriteRune(r)
			dash = false
			continue
		}
		if result.Len() > 0 && !dash {
			result.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(result.String(), "-")
}

func contractDriftReasons(snapshot programview.Snapshot) []Reason {
	if snapshot.Program.Archived {
		return []Reason{}
	}
	root := program.ProgramDir(program.ActiveDir(), snapshot.Program.Slug)
	reasons := []Reason{}
	for _, contract := range snapshot.Contracts {
		path := filepath.Join(root, filepath.FromSlash(contract.Path))
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			reasons = append(reasons, Reason{
				Code: "contract-warning:" + contract.Ref,
				Text: fmt.Sprintf("Contract %s has an invalid artifact path.", contract.Ref),
			})
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			reasons = append(reasons, Reason{
				Code: "contract-warning:" + contract.Ref,
				Text: fmt.Sprintf("Contract %s could not be read: %v", contract.Ref, err),
			})
			continue
		}
		sum := sha256.Sum256(data)
		actual := fmt.Sprintf("%x", sum[:])
		if !strings.EqualFold(actual, strings.TrimSpace(contract.SHA256)) {
			reasons = append(reasons, Reason{
				Code: "contract-drift:" + contract.Ref,
				Text: fmt.Sprintf("Contract %s content no longer matches its recorded SHA256.", contract.Ref),
			})
		}
	}
	sort.Slice(reasons, func(i, j int) bool { return reasons[i].Code < reasons[j].Code })
	return reasons
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
