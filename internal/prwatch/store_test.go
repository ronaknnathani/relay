package prwatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	if _, err := DigestPath("demo", strings.ToUpper(valid)); err == nil {
		t.Error("DigestPath accepted an uppercase fingerprint")
	}
}

func TestWriteDigestIsPrivateAndRefreshesTheSnapshot(t *testing.T) {
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

	// The same activity observed again on a new head must not serve the old
	// snapshot: a reader acting on a stale head SHA or merge state acts wrong.
	refreshed := digest
	refreshed.ObservedAt = "2026-12-31T23:59:59Z"
	refreshed.HeadSHA = "def456"
	refreshed.PR.HeadSHA = "def456"
	refreshed.PR.MergeStateStatus = "DIRTY"
	refreshed.PR.ReviewDecision = "CHANGES_REQUESTED"
	refreshed.PR.AutoMerge = true
	refreshed.Waiting = []string{WaitingChecksPending}
	refreshed.Items = []Item{{
		Reason: ReasonNewComment, Source: SourceComment, ID: "1", Key: "comment:1:t0",
		Body: "secret review body, edited",
	}}
	if err := WriteDigest(refreshed); err != nil {
		t.Fatalf("WriteDigest again: %v", err)
	}
	stored, err := ReadDigest("demo", digest.Fingerprint)
	if err != nil {
		t.Fatalf("ReadDigest: %v", err)
	}
	if stored.ObservedAt != refreshed.ObservedAt || stored.HeadSHA != "def456" {
		t.Errorf("digest = %+v, want the refreshed observation", stored)
	}
	if stored.PR.MergeStateStatus != "DIRTY" || stored.PR.ReviewDecision != "CHANGES_REQUESTED" || !stored.PR.AutoMerge {
		t.Errorf("digest pr = %+v, want the refreshed pull request metadata", stored.PR)
	}
	if len(stored.Waiting) != 1 || stored.Waiting[0] != WaitingChecksPending {
		t.Errorf("digest waiting = %v, want the refreshed waiting codes", stored.Waiting)
	}
	if len(stored.Items) != 1 || stored.Items[0].Body != "secret review body, edited" {
		t.Errorf("digest items = %+v, want the refreshed body", stored.Items)
	}
	if stored.Fingerprint != digest.Fingerprint {
		t.Errorf("digest fingerprint = %q, want the stable %q", stored.Fingerprint, digest.Fingerprint)
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

func TestPruneKeepsTheCurrentAndNewestDigests(t *testing.T) {
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
	// The oldest digest is the one the watcher currently carries, so pruning to
	// four records has to drop the next two instead.
	current := fingerprints[0]

	if err := Prune("demo", 4, current); err != nil {
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

// A record written by an older relay carried acknowledgement fields and an
// acknowledgements directory. Reading one must not fail, and the next state
// write must not carry those fields forward.
func TestLegacyAcknowledgementStateLoadsAndIsRewrittenWithoutIt(t *testing.T) {
	withRuntimeHome(t)
	if err := os.MkdirAll(RuntimeDir("demo"), 0o700); err != nil {
		t.Fatalf("create runtime dir: %v", err)
	}
	legacyAcks := filepath.Join(RuntimeDir("demo"), "acknowledgements")
	if err := os.MkdirAll(legacyAcks, 0o700); err != nil {
		t.Fatalf("create legacy acknowledgements dir: %v", err)
	}
	legacy := `{
  "schema": "relay.prwatch.v1",
  "version": 1,
  "revision": 4,
  "project": "demo",
  "mode": "standalone",
  "owner_slug": "demo",
  "scheduled_checks": 3,
  "baselined": true,
  "current_fingerprint": "` + strings.Repeat("a", 64) + `",
  "acknowledged_fingerprint": "` + strings.Repeat("b", 64) + `",
  "acknowledged_outcome": "handled",
  "acknowledged_at": "2026-01-01T00:00:00Z",
  "acknowledged_keys": ["comment:1:t0", "check:build:9:failure"]
}
`
	if err := os.WriteFile(StatePath("demo"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	state, err := ReadState("demo")
	if err != nil {
		t.Fatalf("ReadState of a legacy record: %v", err)
	}
	if state.ScheduledChecks != 3 || state.CurrentFingerprint != strings.Repeat("a", 64) {
		t.Errorf("state = %+v, want the legacy record's live fields", state)
	}

	if _, err := UpdateState("demo", func(state State) (State, error) {
		state.ScheduledChecks = 0
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	data, err := os.ReadFile(StatePath("demo"))
	if err != nil {
		t.Fatalf("read rewritten state: %v", err)
	}
	for _, forbidden := range []string{"acknowledged", "baselined"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("rewritten state still carries %q:\n%s", forbidden, data)
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

// The runtime directory is exactly four things. Nothing records that attention
// was handled, so there is nowhere for such a record to live.
func TestRuntimeDirectoryHoldsNoAcknowledgementRecords(t *testing.T) {
	withRuntimeHome(t)
	digest := testDigest(t, "demo", []Item{{Key: "comment:1:t0"}})
	if err := WriteDigest(digest); err != nil {
		t.Fatalf("WriteDigest: %v", err)
	}
	if _, err := UpdateState("demo", func(state State) (State, error) {
		state.Status = StatusRunning
		state.CurrentFingerprint = digest.Fingerprint
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	if _, err := ReadStateLocked("demo"); err != nil {
		t.Fatalf("ReadStateLocked: %v", err)
	}
	if err := Prune("demo", MaxRetainedDigests, digest.Fingerprint); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	entries, err := os.ReadDir(RuntimeDir("demo"))
	if err != nil {
		t.Fatalf("read runtime dir: %v", err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	sort.Strings(got)
	want := []string{"digests", "state.lock", "watch.json"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("runtime directory = %v, want %v", got, want)
	}
}
