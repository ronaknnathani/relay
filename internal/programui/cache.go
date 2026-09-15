package programui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/programview"
)

const (
	maxSnapshotEntries  = 64
	maxDetailItemLength = 32
	maxGitHubStaleAge   = 5 * time.Minute
	maxHerdrStaleAge    = 5 * time.Minute
)

type Builder func(slug, detailItem string) (programview.Snapshot, error)

type snapshotCache struct {
	mu        sync.Mutex
	ttl       time.Duration
	now       func() time.Time
	builder   Builder
	entries   map[string]snapshotEntry
	flights   map[string]*snapshotFlight
	nextOrder uint64
}

type snapshotEntry struct {
	snapshot  programview.Snapshot
	expiresAt time.Time
	order     uint64
}

type snapshotFlight struct {
	done     chan struct{}
	snapshot programview.Snapshot
	err      error
}

func newSnapshotCache(ttl time.Duration, now func() time.Time, builder Builder) *snapshotCache {
	if now == nil {
		now = time.Now
	}
	return &snapshotCache{
		ttl: ttl, now: now, builder: builder,
		entries: make(map[string]snapshotEntry),
		flights: make(map[string]*snapshotFlight),
	}
}

func (c *snapshotCache) Get(slug, detailItem string) (programview.Snapshot, error) {
	if normalized, ok := normalizeDetailItem(detailItem); ok {
		detailItem = normalized
	} else {
		detailItem = ""
	}
	key := slug + "\x00" + detailItem
	c.mu.Lock()
	now := c.now()
	c.removeExpired(now)
	if entry, ok := c.entries[key]; ok {
		c.mu.Unlock()
		return entry.snapshot, nil
	}
	if flight, ok := c.flights[key]; ok {
		c.mu.Unlock()
		<-flight.done
		return flight.snapshot, flight.err
	}
	flight := &snapshotFlight{done: make(chan struct{})}
	c.flights[key] = flight
	c.mu.Unlock()

	if c.builder == nil {
		flight.err = fmt.Errorf("program snapshot builder is not configured")
	} else {
		flight.snapshot, flight.err = c.builder(slug, detailItem)
	}

	c.mu.Lock()
	if flight.err == nil {
		c.removeExpired(c.now())
		if len(c.entries) >= maxSnapshotEntries {
			c.evictOldest()
		}
		c.nextOrder++
		c.entries[key] = snapshotEntry{
			snapshot: flight.snapshot, expiresAt: c.now().Add(c.ttl), order: c.nextOrder,
		}
	}
	delete(c.flights, key)
	close(flight.done)
	c.mu.Unlock()
	return flight.snapshot, flight.err
}

func (c *snapshotCache) removeExpired(now time.Time) {
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
}

func (c *snapshotCache) evictOldest() {
	oldestKey := ""
	var oldestOrder uint64
	for key, entry := range c.entries {
		if oldestKey == "" || entry.order < oldestOrder ||
			(entry.order == oldestOrder && key < oldestKey) {
			oldestKey = key
			oldestOrder = entry.order
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

func normalizeDetailItem(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	if len(value) < 2 || len(value) > maxDetailItemLength ||
		value[0] != 'w' || value[1] < '1' || value[1] > '9' {
		return "", false
	}
	for _, digit := range value[2:] {
		if digit < '0' || digit > '9' {
			return "", false
		}
	}
	return value, true
}

type githubCache struct {
	mu      sync.Mutex
	fetcher programview.Fetcher
	ttl     time.Duration
	now     func() time.Time
	entries map[string]githubEntry
	flights map[string]*githubFlight
}

type githubEntry struct {
	pullRequest programview.PullRequestDTO
	expiresAt   time.Time
	fetchedAt   time.Time
}

type githubFlight struct {
	done        chan struct{}
	pullRequest programview.PullRequestDTO
	err         error
}

type agentCache struct {
	mu        sync.Mutex
	lister    programview.AgentLister
	ttl       time.Duration
	now       func() time.Time
	agents    []herdr.Agent
	hasEntry  bool
	expiresAt time.Time
	fetchedAt time.Time
	flight    *agentFlight
}

type agentFlight struct {
	done   chan struct{}
	agents []herdr.Agent
	err    error
}

func newAgentCache(
	lister programview.AgentLister,
	ttl time.Duration,
	now func() time.Time,
) *agentCache {
	if now == nil {
		now = time.Now
	}
	return &agentCache{lister: lister, ttl: ttl, now: now}
}

func (c *agentCache) Agents() ([]herdr.Agent, error) {
	c.mu.Lock()
	now := c.now()
	if c.hasEntry && now.Before(c.expiresAt) {
		agents := append([]herdr.Agent(nil), c.agents...)
		c.mu.Unlock()
		return agents, nil
	}
	if c.flight != nil {
		flight := c.flight
		c.mu.Unlock()
		<-flight.done
		return append([]herdr.Agent(nil), flight.agents...), flight.err
	}
	flight := &agentFlight{done: make(chan struct{})}
	c.flight = flight
	stale := append([]herdr.Agent(nil), c.agents...)
	hasStale := c.hasEntry
	fetchedAt := c.fetchedAt
	c.mu.Unlock()

	if c.lister == nil {
		flight.err = fmt.Errorf("herdr agent lister is not configured")
	} else {
		flight.agents, flight.err = c.lister.Agents()
	}
	completedAt := c.now()
	if flight.err != nil && hasStale && completedAt.Sub(fetchedAt) <= maxHerdrStaleAge {
		flight.agents = stale
	}

	c.mu.Lock()
	if flight.err == nil {
		c.agents = append([]herdr.Agent(nil), flight.agents...)
		c.hasEntry = true
		c.fetchedAt = completedAt
		c.expiresAt = c.now().Add(c.ttl)
	} else if hasStale && completedAt.Sub(fetchedAt) > maxHerdrStaleAge {
		c.agents = nil
		c.hasEntry = false
		c.fetchedAt = time.Time{}
		c.expiresAt = time.Time{}
	}
	c.flight = nil
	close(flight.done)
	c.mu.Unlock()
	return append([]herdr.Agent(nil), flight.agents...), flight.err
}

func newGitHubCache(fetcher programview.Fetcher, ttl time.Duration, now func() time.Time) *githubCache {
	if now == nil {
		now = time.Now
	}
	return &githubCache{
		fetcher: fetcher, ttl: ttl, now: now,
		entries: make(map[string]githubEntry),
		flights: make(map[string]*githubFlight),
	}
}

func (c *githubCache) Fetch(ctx context.Context, repo, ref string) (programview.PullRequestDTO, error) {
	key := repo + "\x00" + ref
	c.mu.Lock()
	stale, hasStale := c.entries[key]
	if hasStale && c.now().Before(stale.expiresAt) {
		c.mu.Unlock()
		return stale.pullRequest, nil
	}
	if flight, ok := c.flights[key]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return programview.PullRequestDTO{}, ctx.Err()
		case <-flight.done:
			return flight.pullRequest, flight.err
		}
	}
	flight := &githubFlight{done: make(chan struct{})}
	c.flights[key] = flight
	c.mu.Unlock()

	refreshSucceeded := false
	if c.fetcher == nil {
		flight.err = fmt.Errorf("GitHub fetcher is not configured")
	} else {
		flight.pullRequest, flight.err = c.fetcher.Fetch(ctx, repo, ref)
		refreshSucceeded = flight.err == nil
	}
	fetchedAt := c.now()
	if refreshSucceeded {
		flight.pullRequest.Stale = false
		flight.pullRequest.StaleReason = ""
		flight.pullRequest.FetchedAt = fetchedAt.UTC().Format(time.RFC3339)
	} else if hasStale && fetchedAt.Sub(stale.fetchedAt) <= maxGitHubStaleAge {
		flight.pullRequest = stale.pullRequest
		flight.pullRequest.Stale = true
		flight.pullRequest.StaleReason = flight.err.Error()
	}

	c.mu.Lock()
	if refreshSucceeded {
		c.entries[key] = githubEntry{
			pullRequest: flight.pullRequest,
			expiresAt:   c.now().Add(c.ttl),
			fetchedAt:   fetchedAt,
		}
	} else if hasStale && fetchedAt.Sub(stale.fetchedAt) > maxGitHubStaleAge {
		delete(c.entries, key)
	}
	delete(c.flights, key)
	close(flight.done)
	c.mu.Unlock()
	return flight.pullRequest, flight.err
}
