package patrol

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
)

func TestObserveCadenceMatrix(t *testing.T) {
	base := programview.Snapshot{
		Program: programview.ProgramDTO{Slug: "adaptive", State: string(program.StateActive)},
		Plan: programview.PlanDTO{
			Ready: []string{}, Blocked: []programview.BlockedPlanDTO{},
			Orphaned: []string{}, OpenDecisions: []string{},
		},
		Items:         []programview.ItemDTO{},
		OpenDecisions: []programview.DecisionDTO{},
		SourceHealth: programview.SourceHealthDTO{
			Projects: programview.SourceDTO{Status: "ok", Warnings: []string{}},
			Herdr:    programview.SourceDTO{Status: "ok", Warnings: []string{}},
		},
	}

	with := func(update func(*programview.Snapshot)) programview.Snapshot {
		snapshot := base
		snapshot.Plan.Ready = append([]string(nil), base.Plan.Ready...)
		snapshot.Plan.Blocked = append([]programview.BlockedPlanDTO(nil), base.Plan.Blocked...)
		snapshot.Plan.Orphaned = append([]string(nil), base.Plan.Orphaned...)
		snapshot.Items = append([]programview.ItemDTO(nil), base.Items...)
		snapshot.OpenDecisions = append([]programview.DecisionDTO(nil), base.OpenDecisions...)
		update(&snapshot)
		return snapshot
	}
	linked := programview.ItemDTO{
		ID: "w1", Status: string(program.ItemDispatched), ProjectSlug: "child",
		Mailbox: programview.MailboxDTO{Available: true},
		Worker:  &programview.WorkerDTO{Status: "working"},
		Child: &programview.ChildDTO{
			Workflow: &programview.WorkflowStateDTO{CurrentPhase: "implement"},
		},
	}
	cloneLinked := func() programview.ItemDTO {
		item := linked
		child := *linked.Child
		workflow := *linked.Child.Workflow
		child.Workflow = &workflow
		item.Child = &child
		return item
	}
	tests := []struct {
		name      string
		snapshot  programview.Snapshot
		delay     time.Duration
		stop      bool
		reasonIDs []string
	}{
		{name: "active calm", snapshot: base, delay: 30 * time.Minute},
		{name: "active unread outbox", snapshot: with(func(s *programview.Snapshot) {
			item := cloneLinked()
			item.Mailbox.Outbox = 1
			s.Items = []programview.ItemDTO{item}
		}), delay: 15 * time.Minute, reasonIDs: []string{"unread-worker-outbox:w1"}},
		{name: "active open decision", snapshot: with(func(s *programview.Snapshot) {
			s.OpenDecisions = []programview.DecisionDTO{{ID: "d1"}}
		}), delay: 15 * time.Minute, reasonIDs: []string{"open-decision:d1"}},
		{name: "active ready", snapshot: with(func(s *programview.Snapshot) {
			s.Plan.Ready = []string{"w1"}
		}), delay: 15 * time.Minute, reasonIDs: []string{"ready-item:w1"}},
		{name: "active blocked", snapshot: with(func(s *programview.Snapshot) {
			s.Items = []programview.ItemDTO{{ID: "w1", Status: string(program.ItemBlocked)}}
		}), delay: 15 * time.Minute, reasonIDs: []string{"blocked-item:w1"}},
		{name: "dependency blocked pending is normal", snapshot: with(func(s *programview.Snapshot) {
			s.Plan.Blocked = []programview.BlockedPlanDTO{{
				ItemID: "w1", Reasons: []string{"dependency w0 is dispatched"},
			}}
			s.Items = []programview.ItemDTO{{ID: "w1", Status: string(program.ItemPending)}}
		}), delay: 30 * time.Minute},
		{name: "active orphan", snapshot: with(func(s *programview.Snapshot) {
			s.Plan.Orphaned = []string{"w1"}
		}), delay: 15 * time.Minute, reasonIDs: []string{"orphan-item:w1"}},
		{name: "active missing worker", snapshot: with(func(s *programview.Snapshot) {
			item := cloneLinked()
			item.Worker = nil
			s.Items = []programview.ItemDTO{item}
		}), delay: 15 * time.Minute, reasonIDs: []string{"missing-worker:w1"}},
		{name: "active early child phase", snapshot: with(func(s *programview.Snapshot) {
			item := cloneLinked()
			item.Child.Workflow.CurrentPhase = "plan"
			s.Items = []programview.ItemDTO{item}
		}), delay: 15 * time.Minute, reasonIDs: []string{"early-child-phase:w1:plan"}},
		{name: "merged child phase ignored", snapshot: with(func(s *programview.Snapshot) {
			item := cloneLinked()
			item.Status = string(program.ItemMerged)
			item.Child.Workflow.CurrentPhase = "plan"
			s.Items = []programview.ItemDTO{item}
		}), delay: 30 * time.Minute},
		{name: "active project warning", snapshot: with(func(s *programview.Snapshot) {
			s.SourceHealth.Projects = programview.SourceDTO{
				Status: "degraded", Warnings: []string{"child state unreadable"},
			}
		}), delay: 15 * time.Minute, reasonIDs: []string{"project-warning:child-state-unreadable"}},
		{name: "draft ignores ready", snapshot: with(func(s *programview.Snapshot) {
			s.Program.State = string(program.StateDraft)
			s.Plan.Ready = []string{"w1"}
		}), delay: 30 * time.Minute},
		{name: "draft unread outbox stays slow", snapshot: with(func(s *programview.Snapshot) {
			s.Program.State = string(program.StateDraft)
			item := cloneLinked()
			item.Mailbox.Outbox = 1
			s.Items = []programview.ItemDTO{item}
		}), delay: 30 * time.Minute, reasonIDs: []string{"unread-worker-outbox:w1"}},
		{name: "pending approval", snapshot: with(func(s *programview.Snapshot) {
			s.Program.State = string(program.StatePendingApproval)
		}), delay: 30 * time.Minute, reasonIDs: []string{"awaiting-approval"}},
		{name: "held ignores ready", snapshot: with(func(s *programview.Snapshot) {
			s.Program.State = string(program.StateHeld)
			s.Plan.Ready = []string{"w1"}
		}), delay: 30 * time.Minute},
		{name: "held blocked", snapshot: with(func(s *programview.Snapshot) {
			s.Program.State = string(program.StateHeld)
			s.Items = []programview.ItemDTO{{ID: "w1", Status: string(program.ItemBlocked)}}
		}), delay: 15 * time.Minute, reasonIDs: []string{"blocked-item:w1"}},
		{name: "completed stops", snapshot: with(func(s *programview.Snapshot) {
			s.Program.State = string(program.StateCompleted)
		}), stop: true},
		{name: "abandoned stops", snapshot: with(func(s *programview.Snapshot) {
			s.Program.State = string(program.StateAbandoned)
		}), stop: true},
		{name: "archived stops", snapshot: with(func(s *programview.Snapshot) {
			s.Program.Archived = true
		}), stop: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := observeSnapshot(test.snapshot, nil)
			if got.Stop != test.stop || time.Duration(got.DelaySeconds)*time.Second != test.delay {
				t.Fatalf("observation = %+v, want stop=%t delay=%s", got, test.stop, test.delay)
			}
			codes := make([]string, 0, len(got.Reasons))
			for _, reason := range got.Reasons {
				codes = append(codes, reason.Code)
			}
			wantCodes := append([]string(nil), test.reasonIDs...)
			if wantCodes == nil {
				wantCodes = []string{}
			}
			if !reflect.DeepEqual(codes, wantCodes) {
				t.Fatalf("reason codes = %v, want %v", codes, test.reasonIDs)
			}
		})
	}
}

func TestContractHashDriftIsAttentionNotFatal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(program.ProgramDir(program.ActiveDir(), "adaptive"), "contracts", "api", "v1.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte("original\n"))
	snapshot := programview.Snapshot{
		Program: programview.ProgramDTO{
			Slug: "adaptive", State: string(program.StateActive),
		},
		Contracts: []programview.ContractDTO{{
			Ref: "api@v1", Path: "contracts/api/v1.md", SHA256: fmt.Sprintf("%x", expected[:]),
		}},
		Plan: programview.PlanDTO{
			Ready: []string{}, Blocked: []programview.BlockedPlanDTO{},
			Orphaned: []string{}, OpenDecisions: []string{},
		},
		Items:         []programview.ItemDTO{},
		OpenDecisions: []programview.DecisionDTO{},
		SourceHealth: programview.SourceHealthDTO{
			Projects: programview.SourceDTO{Status: "ok", Warnings: []string{}},
			Herdr:    programview.SourceDTO{Status: "ok", Warnings: []string{}},
		},
	}

	drifts := contractDriftReasons(snapshot)
	got := observeSnapshot(snapshot, drifts)
	if got.Stop || got.DelaySeconds != int64((15*time.Minute)/time.Second) ||
		len(got.Reasons) != 1 || got.Reasons[0].Code != "contract-drift:api@v1" {
		t.Fatalf("contract drift observation = %+v", got)
	}
}

// A second unread outbox message on the same item keeps the reason code stable
// but must change the attention fingerprint, so the next patrol tick runs a
// live tech lead wake instead of deduplicating genuinely new mail away.
func TestUnreadOutboxAttentionKeyTracksMessageIdentifiers(t *testing.T) {
	snapshot := func(ids ...string) programview.Snapshot {
		return programview.Snapshot{
			Program: programview.ProgramDTO{Slug: "mail", State: string(program.StateActive)},
			Plan: programview.PlanDTO{
				Ready: []string{}, Blocked: []programview.BlockedPlanDTO{},
				Orphaned: []string{}, OpenDecisions: []string{},
			},
			Items: []programview.ItemDTO{{
				ID: "w1", Status: string(program.ItemDispatched), ProjectSlug: "child",
				Worker: &programview.WorkerDTO{Status: "working"},
				Child: &programview.ChildDTO{
					Workflow: &programview.WorkflowStateDTO{CurrentPhase: "implement"},
				},
				Mailbox: programview.MailboxDTO{
					Available: true, Outbox: len(ids), OutboxIDs: ids,
				},
			}},
			OpenDecisions: []programview.DecisionDTO{},
			SourceHealth: programview.SourceHealthDTO{
				Projects: programview.SourceDTO{Status: "ok", Warnings: []string{}},
				Herdr:    programview.SourceDTO{Status: "ok", Warnings: []string{}},
			},
		}
	}

	one := observeSnapshot(snapshot("m-1"), nil)
	if len(one.Reasons) != 1 || one.Reasons[0].Code != "unread-worker-outbox:w1" {
		t.Fatalf("reason codes = %+v, want a stable unread-worker-outbox:w1 code", one.Reasons)
	}
	if len(one.AttentionKeys) != 1 || !strings.Contains(one.AttentionKeys[0], "m-1") {
		t.Fatalf("attention keys = %v, want the unread message id", one.AttentionKeys)
	}

	same := observeSnapshot(snapshot("m-1"), nil)
	if same.AttentionFingerprint != one.AttentionFingerprint {
		t.Fatal("identical unread mail changed the fingerprint")
	}

	// Sorted order makes the fingerprint independent of mailbox listing order.
	reordered := observeSnapshot(snapshot("m-2", "m-1"), nil)
	sorted := observeSnapshot(snapshot("m-1", "m-2"), nil)
	if reordered.AttentionFingerprint != sorted.AttentionFingerprint {
		t.Fatal("unread mail ordering changed the fingerprint")
	}
	if sorted.AttentionFingerprint == one.AttentionFingerprint {
		t.Fatal("a new message on the same item did not change the fingerprint")
	}
	if len(sorted.Reasons) != 1 || sorted.Reasons[0].Code != "unread-worker-outbox:w1" {
		t.Fatalf("second message changed the reason code: %+v", sorted.Reasons)
	}

	// Replacing a message keeps the count but must still change the fingerprint.
	replaced := observeSnapshot(snapshot("m-3"), nil)
	if replaced.AttentionFingerprint == one.AttentionFingerprint {
		t.Fatal("a different message with the same count did not change the fingerprint")
	}
}
