package programui

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/programview"
)

func TestSnapshotFeedSingleFlightsAndRetainsLastSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 14, 20, 0, 0, 0, time.UTC)
	release := make(chan struct{})
	var calls atomic.Int32
	feed := newSnapshotFeed(
		programview.Snapshot{GeneratedAt: "seed", Warnings: []string{}},
		time.Second,
		func() time.Time { return now },
		func() (programview.Snapshot, error) {
			call := calls.Add(1)
			<-release
			if call == 1 {
				return programview.Snapshot{GeneratedAt: "fresh", Warnings: []string{}}, nil
			}
			return programview.Snapshot{}, errors.New("refresh failed")
		},
	)
	feed.Refresh()

	var wait sync.WaitGroup
	for range 12 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if got := feed.Get(); got.GeneratedAt != "seed" {
				t.Errorf("snapshot during refresh = %q, want seed", got.GeneratedAt)
			}
		}()
	}
	wait.Wait()
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
	close(release)
	eventually(t, time.Second, func() bool { return feed.Get().GeneratedAt == "fresh" })

	now = now.Add(2 * time.Second)
	if got := feed.Get(); got.GeneratedAt != "fresh" {
		t.Fatalf("expired snapshot = %q, want retained fresh snapshot", got.GeneratedAt)
	}
	eventually(t, time.Second, func() bool {
		got := feed.Get()
		return calls.Load() == 2 && got.GeneratedAt == "fresh" &&
			len(got.Warnings) == 1 && got.Warnings[0] == "refresh program snapshot: refresh failed"
	})
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not satisfied before timeout")
		}
		time.Sleep(time.Millisecond)
	}
}
