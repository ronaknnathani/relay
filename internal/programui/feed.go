package programui

import (
	"encoding/json"
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
	encoded    []byte
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
	seed.Refresh = programview.RefreshDTO{Status: "partial"}
	feed := &snapshotFeed{
		snapshot: seed, expiresAt: now().Add(ttl), ttl: ttl, now: now, refresh: refresh,
	}
	feed.encoded = encodeSnapshot(seed)
	return feed
}

func (f *snapshotFeed) Get() programview.Snapshot {
	snapshot, _ := f.response()
	return snapshot
}

func (f *snapshotFeed) response() (programview.Snapshot, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.refreshing && !f.now().Before(f.expiresAt) {
		f.startRefreshLocked()
	}
	snapshot := f.snapshot
	if f.refreshing {
		snapshot.Refresh = programview.RefreshDTO{Status: "refreshing"}
		return snapshot, nil
	}
	return snapshot, f.encoded
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
			snapshot.Refresh = programview.RefreshDTO{Status: "fresh"}
			f.snapshot = snapshot
		} else {
			f.snapshot = snapshotWithRefreshError(f.snapshot, err)
		}
		f.encoded = encodeSnapshot(f.snapshot)
		f.expiresAt = f.now().Add(f.ttl)
		f.refreshing = false
	}()
}

func encodeSnapshot(snapshot programview.Snapshot) []byte {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil
	}
	return encoded
}

func snapshotWithRefreshError(snapshot programview.Snapshot, refreshErr error) programview.Snapshot {
	message := "refresh program snapshot: " + refreshErr.Error()
	snapshot.Warnings = appendUniqueCopy(snapshot.Warnings, message)
	snapshot.Refresh = programview.RefreshDTO{Status: "failed", Error: message}
	return snapshot
}

func appendUniqueCopy(values []string, value string) []string {
	copied := append([]string(nil), values...)
	for _, existing := range copied {
		if existing == value {
			return copied
		}
	}
	return append(copied, value)
}
