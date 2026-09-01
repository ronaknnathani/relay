package prwatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// withRuntimeHome points ~/.relay at a per-test directory so runtime records
// never touch the developer's real watcher state.
func withRuntimeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func testDigest(t *testing.T, slug string, items []Item) Digest {
	t.Helper()
	return Digest{
		Project:     slug,
		Mode:        ModeStandalone,
		Fingerprint: Fingerprint(items),
		ObservedAt:  "2026-01-01T00:00:00Z",
		HeadSHA:     "abc123",
		PR:          PullRequest{Number: 7, State: "OPEN", HeadSHA: "abc123"},
		Items:       items,
		Waiting:     []string{},
	}
}

func TestRuntimePathsAreProjectScoped(t *testing.T) {
	home := withRuntimeHome(t)
	want := filepath.Join(home, ".relay", "run", "pr-watch", "demo")
	if got := RuntimeDir("demo"); got != want {
		t.Errorf("RuntimeDir = %q, want %q", got, want)
	}
	if got := StatePath("demo"); got != filepath.Join(want, "watch.json") {
		t.Errorf("StatePath = %q", got)
	}
	if got := WatchLockPath("demo"); got != filepath.Join(want, "watch.lock") {
		t.Errorf("WatchLockPath = %q", got)
	}
	if got := StateLockPath("demo"); got != filepath.Join(want, "state.lock") {
		t.Errorf("StateLockPath = %q", got)
	}
}

func TestRecordPathsRejectUnsafeNames(t *testing.T) {
	withRuntimeHome(t)
	valid := Fingerprint([]Item{{Key: "check:build:1:failure"}})
	if _, err := DigestPath("../escape", valid); err == nil {
		t.Error("DigestPath accepted a traversing slug")
	}
	if _, err := DigestPath("demo", "../../etc/passwd"); err == nil {
		t.Error("DigestPath accepted a traversing fingerprint")
	}
	if _, err := AcknowledgementPath("demo", strings.ToUpper(valid)); err == nil {
		t.Error("AcknowledgementPath accepted an uppercase fingerprint")
	}
}

func TestWriteDigestIsImmutableAndPrivate(t *testing.T) {
	withRuntimeHome(t)
	items := []Item{{Reason: ReasonNewComment, Source: SourceComment, ID: "1", Key: "comment:1:t0", Body: "secret review body"}}
	digest := testDigest(t, "demo", items)
	if err := WriteDigest(digest); err != nil {
		t.Fatalf("WriteDigest: %v", err)
	}

	path, err := DigestPath("demo", digest.Fingerprint)
	if err != nil {
		t.Fatalf("DigestPath: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat digest: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("digest mode = %v, want 0600", info.Mode().Perm())
	}

	rewritten := digest
	rewritten.ObservedAt = "2026-12-31T23:59:59Z"
	if err := WriteDigest(rewritten); err != nil {
		t.Fatalf("WriteDigest again: %v", err)
	}
	stored, err := ReadDigest("demo", digest.Fingerprint)
	if err != nil {
		t.Fatalf("ReadDigest: %v", err)
	}
	if stored.ObservedAt != digest.ObservedAt {
		t.Errorf("digest observed_at = %q, want the original %q", stored.ObservedAt, digest.ObservedAt)
	}
	if len(stored.Items) != 1 || stored.Items[0].Body != "secret review body" {
		t.Errorf("digest items = %+v, want the original body", stored.Items)
	}
}

func TestUpdateStateIsAtomicPrivateAndVersioned(t *testing.T) {
	withRuntimeHome(t)
	state, err := UpdateState("demo", func(state State) (State, error) {
		state.Status = StatusRunning
		state.Mode = ModeStandalone
		return state, nil
	})
	if err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	if state.Revision != 1 || state.Schema != SchemaVersion || state.Project != "demo" {
		t.Fatalf("state = %+v, want revision 1 with schema and project set", state)
	}
	info, err := os.Stat(StatePath("demo"))
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state mode = %v, want 0600", info.Mode().Perm())
	}

	next, err := UpdateState("demo", func(state State) (State, error) {
		state.ScheduledChecks = 3
		return state, nil
	})
	if err != nil {
		t.Fatalf("UpdateState again: %v", err)
	}
	if next.Revision != 2 || next.Status != StatusRunning {
		t.Errorf("state = %+v, want revision 2 carrying the earlier status", next)
	}
}

func TestUpdateStateMutationErrorLeavesRecordUnchanged(t *testing.T) {
	withRuntimeHome(t)
	if _, err := UpdateState("demo", func(state State) (State, error) {
		state.ScheduledChecks = 1
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	wantErr := errors.New("mutation failed")
	if _, err := UpdateState("demo", func(State) (State, error) {
		return State{}, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("UpdateState error = %v, want %v", err, wantErr)
	}
	state, err := ReadState("demo")
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if state.Revision != 1 || state.ScheduledChecks != 1 {
		t.Errorf("state = %+v, want the record from before the failed mutation", state)
	}
}

func TestConcurrentUpdateStateSerializesRevisions(t *testing.T) {
	withRuntimeHome(t)
	const writers = 8
	var wait sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := UpdateState("demo", func(state State) (State, error) {
				state.ActionableCount++
				return state, nil
			})
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent UpdateState: %v", err)
		}
	}
	state, err := ReadState("demo")
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if state.Revision != writers || state.ActionableCount != writers {
		t.Errorf("state = revision %d count %d, want %d for both", state.Revision, state.ActionableCount, writers)
	}
}

func TestReadStateReportsAbsentRecord(t *testing.T) {
	withRuntimeHome(t)
	if _, err := ReadState("demo"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadState = %v, want os.ErrNotExist", err)
	}
}

func TestAcknowledgeRecordsResetsAndIsIdempotent(t *testing.T) {
	withRuntimeHome(t)
	items := []Item{
		{Reason: ReasonNewComment, Source: SourceComment, ID: "1", Key: "comment:1:t0"},
		{Reason: ReasonFailingCheck, Source: SourceCheck, ID: "build", Key: "check:build:9:failure"},
	}
	digest := testDigest(t, "demo", items)
	if err := WriteDigest(digest); err != nil {
		t.Fatalf("WriteDigest: %v", err)
	}
	if _, err := UpdateState("demo", func(state State) (State, error) {
		state.ScheduledChecks = 6
		state.AttentionPending = true
		state.CurrentFingerprint = digest.Fingerprint
		state.NextCheckAt = "2026-01-01T01:00:00Z"
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ack, state, err := Acknowledge("demo", digest.Fingerprint, OutcomeHandled, now)
	if err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if ack.Outcome != OutcomeHandled || ack.HeadSHA != "abc123" {
		t.Errorf("ack = %+v, want handled outcome carrying the head sha", ack)
	}
	wantKeys := ItemKeys(items)
	if strings.Join(ack.Keys, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("ack keys = %v, want %v", ack.Keys, wantKeys)
	}
	if state.ScheduledChecks != 0 {
		t.Errorf("scheduled checks = %d, want 0 after an acknowledgement", state.ScheduledChecks)
	}
	if want := now.Add(FastCadence).Format(time.RFC3339); state.NextCheckAt != want {
		t.Errorf("next check = %q, want %q", state.NextCheckAt, want)
	}
	if state.AttentionPending {
		t.Error("attention stayed pending after an acknowledgement")
	}
	if !state.Acknowledged("comment:1:t0") || !state.Acknowledged("check:build:9:failure") {
		t.Errorf("acknowledged keys = %v, want both item keys", state.AcknowledgedKeys)
	}
	path, err := AcknowledgementPath("demo", digest.Fingerprint)
	if err != nil {
		t.Fatalf("AcknowledgementPath: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat acknowledgement: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("acknowledgement mode = %v, want 0600", info.Mode().Perm())
	}

	later := now.Add(30 * time.Minute)
	repeat, repeatState, err := Acknowledge("demo", digest.Fingerprint, OutcomeHandled, later)
	if err != nil {
		t.Fatalf("repeat Acknowledge: %v", err)
	}
	if repeat.AcknowledgedAt != ack.AcknowledgedAt {
		t.Errorf("repeat acknowledged_at = %q, want the immutable %q", repeat.AcknowledgedAt, ack.AcknowledgedAt)
	}
	if repeatState.NextCheckAt != state.NextCheckAt {
		t.Errorf("repeat next check = %q, want the unchanged %q", repeatState.NextCheckAt, state.NextCheckAt)
	}
}

func TestAcknowledgeRejectsConflictingOutcome(t *testing.T) {
	withRuntimeHome(t)
	digest := testDigest(t, "demo", []Item{{Key: "comment:1:t0"}})
	if err := WriteDigest(digest); err != nil {
		t.Fatalf("WriteDigest: %v", err)
	}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if _, _, err := Acknowledge("demo", digest.Fingerprint, OutcomeHandled, now); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	_, _, err := Acknowledge("demo", digest.Fingerprint, OutcomeEscalated, now)
	if !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("conflicting Acknowledge = %v, want ErrOutcomeConflict", err)
	}
	stored, err := ReadAcknowledgement("demo", digest.Fingerprint)
	if err != nil {
		t.Fatalf("ReadAcknowledgement: %v", err)
	}
	if stored.Outcome != OutcomeHandled {
		t.Errorf("stored outcome = %q, want the immutable handled", stored.Outcome)
	}
}

func TestAcknowledgeRejectsMissingDigest(t *testing.T) {
	withRuntimeHome(t)
	absent := Fingerprint([]Item{{Key: "comment:404:t0"}})
	_, _, err := Acknowledge("demo", absent, OutcomeHandled, time.Now())
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Acknowledge = %v, want a missing digest error", err)
	}
	if !strings.Contains(err.Error(), "relay pr watch tick") {
		t.Errorf("error %q does not name the recovery command", err)
	}
}

func TestPruneKeepsCurrentAndUnacknowledgedRecords(t *testing.T) {
	withRuntimeHome(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fingerprints := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		digest := testDigest(t, "demo", []Item{{Key: fmt.Sprintf("comment:%d:t0", i)}})
		if err := WriteDigest(digest); err != nil {
			t.Fatalf("WriteDigest: %v", err)
		}
		path, err := DigestPath("demo", digest.Fingerprint)
		if err != nil {
			t.Fatalf("DigestPath: %v", err)
		}
		stamp := now.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		fingerprints = append(fingerprints, digest.Fingerprint)
	}
	// Acknowledge the three oldest digests; only acknowledged records may be pruned.
	for _, fingerprint := range fingerprints[:3] {
		if _, _, err := Acknowledge("demo", fingerprint, OutcomeHandled, now); err != nil {
			t.Fatalf("Acknowledge: %v", err)
		}
	}
	current := fingerprints[0]

	if err := Prune("demo", 4, 200, current); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	for i, fingerprint := range fingerprints {
		path, err := DigestPath("demo", fingerprint)
		if err != nil {
			t.Fatalf("DigestPath: %v", err)
		}
		_, statErr := os.Stat(path)
		removed := errors.Is(statErr, os.ErrNotExist)
		wantRemoved := i == 1 || i == 2
		if removed != wantRemoved {
			t.Errorf("digest %d removed = %t, want %t", i, removed, wantRemoved)
		}
	}
}

func TestLockIsASingleton(t *testing.T) {
	withRuntimeHome(t)
	running, err := IsRunning("demo")
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if running {
		t.Fatal("IsRunning = true before any watcher started")
	}
	lock, err := Acquire("demo")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			t.Fatalf("Release: %v", err)
		}
	}()
	if _, err := Acquire("demo"); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire = %v, want ErrAlreadyRunning", err)
	}
	running, err = IsRunning("demo")
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if !running {
		t.Error("IsRunning = false while the watcher lock is held")
	}
}
