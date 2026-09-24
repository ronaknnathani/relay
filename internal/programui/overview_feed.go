package programui

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/ronaknnathani/relay/internal/programview"
)

type overviewFeed struct {
	mu         sync.Mutex
	snapshot   programview.OverviewSnapshot
	expiresAt  time.Time
	ttl        time.Duration
	now        func() time.Time
	refresh    func() (programview.OverviewSnapshot, error)
	refreshing bool
	encoded    []byte
}

func newOverviewFeed(
	seed programview.OverviewSnapshot,
	ttl time.Duration,
	now func() time.Time,
	refresh func() (programview.OverviewSnapshot, error),
) *overviewFeed {
	if now == nil {
		now = time.Now
	}
	seed.Refresh = programview.RefreshDTO{Status: "fresh"}
	return &overviewFeed{
		snapshot:  seed,
		expiresAt: now().Add(ttl),
		ttl:       ttl,
		now:       now,
		refresh:   refresh,
		encoded:   encodeOverview(seed),
	}
}

func (f *overviewFeed) Get() programview.OverviewSnapshot {
	snapshot, _ := f.response()
	return snapshot
}

func (f *overviewFeed) response() (programview.OverviewSnapshot, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.refreshing && !f.now().Before(f.expiresAt) {
		f.startRefreshLocked()
	}
	snapshot := f.snapshot
	if f.refreshing {
		snapshot.Refresh.Refreshing = true
		return snapshot, encodeOverview(snapshot)
	}
	return snapshot, f.encoded
}

func (f *overviewFeed) Refresh() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.refreshing {
		f.startRefreshLocked()
	}
}

func (f *overviewFeed) startRefreshLocked() {
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
			f.snapshot = overviewWithRefreshError(f.snapshot, err)
		}
		f.encoded = encodeOverview(f.snapshot)
		f.expiresAt = f.now().Add(f.ttl)
		f.refreshing = false
	}()
}

func encodeOverview(snapshot programview.OverviewSnapshot) []byte {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil
	}
	return encoded
}

func overviewWithRefreshError(
	snapshot programview.OverviewSnapshot,
	refreshErr error,
) programview.OverviewSnapshot {
	message := "refresh program overview: " + refreshErr.Error()
	snapshot.Refresh = programview.RefreshDTO{Status: "failed", Error: message}
	diagnostic := programview.OverviewDiagnosticDTO{Directory: ".", Message: message}
	for _, existing := range snapshot.Diagnostics {
		if existing == diagnostic {
			return snapshot
		}
	}
	snapshot.Diagnostics = append(append([]programview.OverviewDiagnosticDTO(nil), snapshot.Diagnostics...), diagnostic)
	return snapshot
}
