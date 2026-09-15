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
		programview.Snapshot{
			GeneratedAt: "seed",
			Warnings:    []string{},
			SourceHealth: programview.SourceHealthDTO{
				GitHub: programview.SourceDTO{Status: "ok", Warnings: []string{"existing GitHub warning"}},
				Herdr:  programview.SourceDTO{Status: "ok", Warnings: []string{}},
			},
		},
		time.Second,
		func() time.Time { return now },
		func() (programview.Snapshot, error) {
			call := calls.Add(1)
			<-release
			if call == 1 {
				return programview.Snapshot{
					GeneratedAt: "fresh",
					Warnings:    []string{},
					Items: []programview.ItemDTO{{
						ID:     "w1",
						LivePR: &programview.PullRequestDTO{Number: 42},
						Worker: &programview.WorkerDTO{PaneID: "pane-42"},
					}},
					SourceHealth: programview.SourceHealthDTO{
						GitHub: programview.SourceDTO{
							Status: "ok", Warnings: []string{"existing GitHub warning"},
						},
						Herdr: programview.SourceDTO{Status: "ok", Warnings: []string{}},
					},
				}, nil
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
			got := feed.Get()
			if got.GeneratedAt != "seed" ||
				got.Refresh != (programview.RefreshDTO{Status: "refreshing"}) {
				t.Errorf("snapshot during refresh = %+v", got)
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
	eventually(t, time.Second, func() bool {
		got := feed.Get()
		return got.GeneratedAt == "fresh" &&
			got.Refresh == (programview.RefreshDTO{Status: "fresh"})
	})

	now = now.Add(2 * time.Second)
	if got := feed.Get(); got.GeneratedAt != "fresh" {
		t.Fatalf("expired snapshot = %q, want retained fresh snapshot", got.GeneratedAt)
	}
	eventually(t, time.Second, func() bool {
		got := feed.Get()
		return calls.Load() == 2 && got.GeneratedAt == "fresh" &&
			len(got.Warnings) == 1 && got.Warnings[0] == "refresh program snapshot: refresh failed" &&
			got.Refresh == (programview.RefreshDTO{
				Status: "failed",
				Error:  "refresh program snapshot: refresh failed",
			}) &&
			got.SourceHealth.GitHub.Status == "ok" &&
			len(got.SourceHealth.GitHub.Warnings) == 1 &&
			got.SourceHealth.GitHub.Warnings[0] == "existing GitHub warning" &&
			got.SourceHealth.Herdr.Status == "ok" &&
			len(got.SourceHealth.Herdr.Warnings) == 0 &&
			len(got.Items) == 1 && got.Items[0].LivePR != nil &&
			got.Items[0].LivePR.Number == 42 && got.Items[0].Worker != nil &&
			got.Items[0].Worker.PaneID == "pane-42"
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
