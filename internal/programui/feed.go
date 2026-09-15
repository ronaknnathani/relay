package programui

import (
	"fmt"
	"sync"
	"time"

	"github.com/ronaknnathani/relay/internal/programview"
)

type snapshotFeed struct {
	mu         sync.Mutex
	snapshot   programview.Snapshot
	expiresAt  time.Time
	ttl        time.Duration
	now        func() time.Time
	refresh    func() (programview.Snapshot, error)
	refreshing bool
}

func newSnapshotFeed(
	seed programview.Snapshot,
	ttl time.Duration,
	now func() time.Time,
	refresh func() (programview.Snapshot, error),
) *snapshotFeed {
	if now == nil {
		now = time.Now
	}
	return &snapshotFeed{
		snapshot: seed, expiresAt: now().Add(ttl), ttl: ttl, now: now, refresh: refresh,
	}
}

func (f *snapshotFeed) Get() programview.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.refreshing && !f.now().Before(f.expiresAt) {
		f.startRefreshLocked()
	}
	return f.snapshot
}

func (f *snapshotFeed) Refresh() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.refreshing {
		f.startRefreshLocked()
	}
}

func (f *snapshotFeed) startRefreshLocked() {
	if f.refresh == nil {
		f.expiresAt = f.now().Add(f.ttl)
		return
	}
	f.refreshing = true
	go func() {
		snapshot, err := f.refresh()
		f.mu.Lock()
		defer f.mu.Unlock()
		if err == nil {
			f.snapshot = snapshot
		} else {
			f.snapshot = snapshotWithRefreshError(f.snapshot, err)
		}
		f.expiresAt = f.now().Add(f.ttl)
		f.refreshing = false
	}()
}

func snapshotWithRefreshError(snapshot programview.Snapshot, refreshErr error) programview.Snapshot {
	message := fmt.Sprintf("refresh program snapshot: %v", refreshErr)
	snapshot.Warnings = appendUnique(append([]string(nil), snapshot.Warnings...), message)
	snapshot.SourceHealth.GitHub.Warnings = appendUnique(
		append([]string(nil), snapshot.SourceHealth.GitHub.Warnings...), message,
	)
	snapshot.SourceHealth.Herdr.Warnings = appendUnique(
		append([]string(nil), snapshot.SourceHealth.Herdr.Warnings...), message,
	)
	snapshot.SourceHealth.GitHub.Status = "degraded"
	snapshot.SourceHealth.Herdr.Status = "degraded"
	return snapshot
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
