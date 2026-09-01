package prwatch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ronaknnathani/relay/internal/herdr"
)

// watchHarness drives Run with a manual clock and a manual ticker so cadence
// assertions are exact and no test waits on wall-clock time.
type watchHarness struct {
	t           *testing.T
	slug        string
	mu          sync.Mutex
	now         time.Time
	observation Observation
	observeErr  error
	observed    int
	ticks       chan time.Time
	out         *signalWriter
	err         *signalWriter
	client      *fakeOwnerClient
	done        chan error
	cancel      context.CancelFunc
	stopOnce    sync.Once
}

// signalWriter records output and signals whenever a completed observation
// prints, which is the synchronization point the tests use.
type signalWriter struct {
	mu     sync.Mutex
	buffer strings.Builder
	signal chan string
}

func newSignalWriter() *signalWriter {
	return &signalWriter{signal: make(chan string, 64)}
}

func (w *signalWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.buffer.Write(data)
	w.mu.Unlock()
	line := string(data)
	select {
	case w.signal <- line:
	default:
	}
	return len(data), nil
}

func (w *signalWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

// awaitLine waits for an event line containing want.
func (w *signalWriter) awaitLine(t *testing.T, want string) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line := <-w.signal:
			if strings.Contains(line, want) {
				return line
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q; output so far:\n%s", want, w.String())
		}
	}
}

func newWatchHarness(t *testing.T, mode Mode, owner string, observation Observation) *watchHarness {
	t.Helper()
	return newWatchHarnessWithClient(t, mode, owner, observation, nil)
}

// newWatchHarnessWithClient starts a watcher whose Herdr double is configured
// before the watcher's first observation runs, which is the only way to observe
// an undelivered wake on the very first check.
func newWatchHarnessWithClient(
	t *testing.T, mode Mode, owner string, observation Observation, configure func(*fakeOwnerClient),
) *watchHarness {
	t.Helper()
	withRuntimeHome(t)
	harness := &watchHarness{
		t:           t,
		slug:        "demo",
		now:         time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC),
		observation: observation,
		ticks:       make(chan time.Time),
		out:         newSignalWriter(),
		err:         newSignalWriter(),
		client: &fakeOwnerClient{agents: []herdr.Agent{
			liveAgent("relay:"+ownerOrProject(owner, "demo"), "pane-owner", herdr.StatusIdle),
		}},
		done: make(chan error, 1),
	}
	if configure != nil {
		configure(harness.client)
	}
	ctx, cancel := context.WithCancel(context.Background())
	harness.cancel = cancel
	options := Options{
		Mode:   mode,
		Owner:  owner,
		Now:    harness.clock,
		Ticker: func(time.Duration) Ticker { return manualTicker{ticks: harness.ticks} },
		Locate: func(slug string) (Target, error) {
			return Target{Slug: slug, Dir: t.TempDir(), PRNumber: 42}, nil
		},
		Observe: harness.observe,
		Client:  harness.client,
		Out:     harness.out,
		Err:     harness.err,
		PID:     4242,
	}
	go func() { harness.done <- Run(ctx, harness.slug, options) }()
	t.Cleanup(harness.stop)
	return harness
}

func ownerOrProject(owner, project string) string {
	if owner != "" {
		return owner
	}
	return project
}

func (h *watchHarness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *watchHarness) setNow(at time.Time) {
	h.mu.Lock()
	h.now = at
	h.mu.Unlock()
}

func (h *watchHarness) setObservation(observation Observation, err error) {
	h.mu.Lock()
	h.observation = observation
	h.observeErr = err
	h.mu.Unlock()
}

func (h *watchHarness) observe(context.Context, Target) (Observation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.observed++
	return h.observation, h.observeErr
}

func (h *watchHarness) observations() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.observed
}

// tick fires the internal watcher wakeup.
func (h *watchHarness) tick() {
	h.t.Helper()
	select {
	case h.ticks <- h.clock():
	case <-time.After(5 * time.Second):
		h.t.Fatal("watcher did not consume an internal tick")
	}
}

func (h *watchHarness) state() State {
	h.t.Helper()
	state, err := ReadState(h.slug)
	if err != nil {
		h.t.Fatalf("ReadState: %v", err)
	}
	return state
}

// stop cancels the watcher and waits for it to return. Every harness registers
// it as a cleanup so a watcher goroutine can never outlive the test that set
// HOME, which would write runtime records into the developer's real ~/.relay.
func (h *watchHarness) stop() {
	h.t.Helper()
	h.stopOnce.Do(func() {
		h.cancel()
		select {
		case err := <-h.done:
			if err != nil {
				h.t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			h.t.Error("watcher did not stop")
		}
	})
}

// awaitExit waits for a watcher that stops on its own and marks the harness
// stopped, so the registered cleanup does not wait for a second exit.
func (h *watchHarness) awaitExit() {
	h.t.Helper()
	h.stopOnce.Do(func() {
		select {
		case err := <-h.done:
			if err != nil {
				h.t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			h.t.Error("watcher did not stop on its own")
		}
		h.cancel()
	})
}

type manualTicker struct {
	ticks chan time.Time
}

func (t manualTicker) C() <-chan time.Time { return t.ticks }
func (t manualTicker) Stop()               {}

func quietObservation() Observation {
	pr := openPR()
	pr.ReviewDecision = "REVIEW_REQUIRED"
	return Observation{PR: pr, Checks: []Check{{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"}}}
}

func actionableObservation() Observation {
	return Observation{
		PR:       openPR(),
		Comments: []Activity{human("1", "reviewer", "please rename this", "2026-01-01T00:00:00Z")},
	}
}

// runScheduledCheck advances the clock to the recorded next check and fires the
// internal ticker, returning the state the check produced.
func (h *watchHarness) runScheduledCheck() State {
	h.t.Helper()
	next, err := time.Parse(time.RFC3339, h.state().NextCheckAt)
	if err != nil {
		h.t.Fatalf("parse next check: %v", err)
	}
	h.setNow(next)
	h.tick()
	h.out.awaitLine(h.t, "next check at=")
	return h.state()
}

func TestCadenceRunsFifteenThirtyThenSixtyMinuteChecks(t *testing.T) {
	harness := newWatchHarness(t, ModeStandalone, "", quietObservation())
	start := harness.clock()

	harness.out.awaitLine(t, "next check at=")
	immediate := harness.state()
	if immediate.ScheduledChecks != 0 {
		t.Fatalf("scheduled checks after the start observation = %d, want 0: it is not a scheduled check",
			immediate.ScheduledChecks)
	}
	if want := start.Add(FastCadence).Format(time.RFC3339); immediate.NextCheckAt != want {
		t.Fatalf("next check after the start observation = %q, want %q", immediate.NextCheckAt, want)
	}

	// The interval before each scheduled check: 15 minutes for checks 1-4, 30
	// for 5-6, and 60 from 7 onward.
	wantMinutes := []float64{15, 15, 15, 15, 30, 30, 60, 60}
	previous := start
	for i, minutes := range wantMinutes {
		state := harness.runScheduledCheck()
		if state.ScheduledChecks != i+1 {
			t.Fatalf("scheduled checks = %d, want %d", state.ScheduledChecks, i+1)
		}
		at, err := time.Parse(time.RFC3339, state.LastCheckAt)
		if err != nil {
			t.Fatalf("parse last check: %v", err)
		}
		if got := at.Sub(previous).Minutes(); got != minutes {
			t.Fatalf("check %d ran %.0f minutes after the previous one, want %.0f", i+1, got, minutes)
		}
		previous = at
	}
}

func TestInternalTickBeforeTheScheduledCheckIsSilent(t *testing.T) {
	harness := newWatchHarness(t, ModeStandalone, "", quietObservation())
	harness.out.awaitLine(t, "next check at=")

	before := harness.out.String()
	observations := harness.observations()
	harness.setNow(harness.clock().Add(30 * time.Second))
	harness.tick()
	// A second tick is only consumed once the first has been handled, which
	// makes the silent path observable without sleeping.
	harness.setNow(harness.clock().Add(30 * time.Second))
	harness.tick()

	if got := harness.out.String(); got != before {
		t.Errorf("internal tick printed %q, want silence", strings.TrimPrefix(got, before))
	}
	if got := harness.observations(); got != observations {
		t.Errorf("observations = %d, want %d: an early tick observed GitHub", got, observations)
	}
}

func TestNewHeadResetsTheCadence(t *testing.T) {
	harness := newWatchHarness(t, ModeStandalone, "", quietObservation())
	harness.out.awaitLine(t, "next check at=")
	for i := 0; i < 5; i++ {
		harness.runScheduledCheck()
	}
	if got := harness.state().DelaySeconds; got != int64(MediumCadence/time.Second) {
		t.Fatalf("cadence = %ds, want the 30m backoff before the reset", got)
	}

	pushed := quietObservation()
	pushed.PR.HeadSHA = "head999"
	harness.setObservation(pushed, nil)
	state := harness.runScheduledCheck()

	if state.ScheduledChecks != 0 {
		t.Errorf("scheduled checks = %d, want 0 after a new head", state.ScheduledChecks)
	}
	if state.DelaySeconds != int64(FastCadence/time.Second) {
		t.Errorf("cadence = %ds, want 15m after a new head", state.DelaySeconds)
	}
	if state.HeadSHA != "head999" {
		t.Errorf("head = %q, want the new head", state.HeadSHA)
	}
}

func TestPendingCheckTransitionAloneDoesNotResetTheCadence(t *testing.T) {
	harness := newWatchHarness(t, ModeStandalone, "", quietObservation())
	harness.out.awaitLine(t, "next check at=")
	for i := 0; i < 5; i++ {
		harness.runScheduledCheck()
	}

	pending := quietObservation()
	pending.Checks = []Check{{Name: "build", Status: "IN_PROGRESS"}}
	harness.setObservation(pending, nil)
	state := harness.runScheduledCheck()

	if state.ScheduledChecks != 6 {
		t.Errorf("scheduled checks = %d, want 6: a pending check is not a reset", state.ScheduledChecks)
	}
	if state.DelaySeconds != int64(SlowCadence/time.Second) {
		t.Errorf("cadence = %ds, want the 60m backoff to continue", state.DelaySeconds)
	}
}

func TestClearedAttentionResetsTheCadence(t *testing.T) {
	harness := newWatchHarness(t, ModeStandalone, "", actionableObservation())
	harness.out.awaitLine(t, "owner wake delivered")
	harness.out.awaitLine(t, "next check at=")
	for i := 0; i < 4; i++ {
		harness.runScheduledCheck()
	}
	state := harness.state()
	if state.ScheduledChecks != 4 || state.CurrentFingerprint == "" {
		t.Fatalf("state = %+v, want four scheduled checks with actionable attention", state)
	}

	// The reviewer's comment was answered on the pull request, so the next
	// observation carries no actionable item at all. An emptied fingerprint is
	// still a change, and a changed pull request restarts the fast cadence.
	harness.setObservation(quietObservation(), nil)
	cleared := harness.runScheduledCheck()
	if cleared.ScheduledChecks != 0 || cleared.CurrentFingerprint != "" {
		t.Fatalf("state = %+v, want the cadence reset by cleared attention", cleared)
	}
	if cleared.DelaySeconds != int64(FastCadence/time.Second) {
		t.Errorf("cadence = %ds, want 15m after attention cleared", cleared.DelaySeconds)
	}
	if cleared.AttentionPending {
		t.Error("attention stayed pending after the pull request went quiet")
	}
}

func TestFirstStartWakesImmediatelyForExistingAttention(t *testing.T) {
	harness := newWatchHarness(t, ModeStandalone, "", actionableObservation())
	line := harness.out.awaitLine(t, "owner wake delivered")
	if !strings.Contains(line, "pane=pane-owner") {
		t.Errorf("wake line = %q, want the exact owner pane", line)
	}
	if harness.client.promptCount() != 1 {
		t.Fatalf("prompts = %v, want exactly one wake from the first observation", harness.client.prompts)
	}
	state := harness.state()
	if !strings.Contains(harness.client.promptTexts()[0], state.CurrentFingerprint) {
		t.Errorf("prompt = %q, want the current fingerprint", harness.client.promptTexts()[0])
	}
	if state.LastWakeStatus != string(WakeDelivered) || state.AttentionPending {
		t.Errorf("state = %+v, want a delivered wake", state)
	}
	if state.ScheduledChecks != 0 {
		t.Errorf("scheduled checks = %d, want 0: the first observation is not a scheduled check",
			state.ScheduledChecks)
	}
}

func TestRestartResetsTheScheduleAndWakesAgain(t *testing.T) {
	harness := newWatchHarness(t, ModeStandalone, "", actionableObservation())
	harness.out.awaitLine(t, "owner wake delivered")
	harness.out.awaitLine(t, "next check at=")
	for i := 0; i < 5; i++ {
		harness.runScheduledCheck()
	}
	before := harness.state()
	if before.ScheduledChecks != 5 || before.DelaySeconds != int64(MediumCadence/time.Second) {
		t.Fatalf("state = %+v, want the 30m backoff before the restart", before)
	}
	harness.stop()

	restarted := newWatchHarnessReusingHome(t, harness)
	restarted.out.awaitLine(t, "owner wake delivered")
	state := restarted.state()
	if state.ScheduledChecks != 0 {
		t.Errorf("scheduled checks = %d, want 0 after a restart", state.ScheduledChecks)
	}
	if state.DelaySeconds != int64(FastCadence/time.Second) {
		t.Errorf("cadence = %ds, want 15m after a restart", state.DelaySeconds)
	}
	if state.CurrentFingerprint != before.CurrentFingerprint || state.HeadSHA != before.HeadSHA {
		t.Errorf("state = %+v, want the observation identity preserved across the restart", state)
	}
	if restarted.client.promptCount() != 1 {
		t.Errorf("prompts = %v, want the restart to wake for the attention that is still there",
			restarted.client.prompts)
	}
}

// newWatchHarnessReusingHome restarts a watcher against the runtime state an
// earlier harness left behind, without re-pointing HOME.
func newWatchHarnessReusingHome(t *testing.T, previous *watchHarness) *watchHarness {
	t.Helper()
	harness := &watchHarness{
		t:           t,
		slug:        previous.slug,
		now:         previous.clock().Add(time.Hour),
		observation: previous.observation,
		ticks:       make(chan time.Time),
		out:         newSignalWriter(),
		err:         newSignalWriter(),
		client: &fakeOwnerClient{agents: []herdr.Agent{
			liveAgent("relay:demo", "pane-owner", herdr.StatusIdle),
		}},
		done: make(chan error, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	harness.cancel = cancel
	options := Options{
		Mode:   ModeStandalone,
		Now:    harness.clock,
		Ticker: func(time.Duration) Ticker { return manualTicker{ticks: harness.ticks} },
		Locate: func(slug string) (Target, error) {
			return Target{Slug: slug, Dir: t.TempDir(), PRNumber: 42}, nil
		},
		Observe: harness.observe,
		Client:  harness.client,
		Out:     harness.out,
		Err:     harness.err,
		PID:     4243,
	}
	go func() { harness.done <- Run(ctx, harness.slug, options) }()
	t.Cleanup(harness.stop)
	return harness
}

func TestMergedPullRequestCompletesSilently(t *testing.T) {
	merged := quietObservation()
	merged.PR.State = "MERGED"
	harness := newWatchHarness(t, ModeStandalone, "", merged)

	harness.out.awaitLine(t, "pr watch complete")
	harness.awaitExit()

	if harness.client.promptCount() != 0 {
		t.Errorf("a merged pull request woke the owner with %v", harness.client.promptTexts())
	}
	state := harness.state()
	if state.Status != StatusComplete || state.AttentionPending {
		t.Errorf("state = %+v, want a completed watch", state)
	}
	if got := harness.err.String(); got != "" {
		t.Errorf("stderr = %q, want silence", got)
	}
}

func TestMergedStackFrontWakesTheOrchestrator(t *testing.T) {
	merged := quietObservation()
	merged.PR.State = "MERGED"
	harness := newWatchHarness(t, ModeStack, "stack-run", merged)

	harness.out.awaitLine(t, "owner wake delivered")
	state := harness.state()
	if state.Status == StatusComplete {
		t.Fatal("stack front watcher completed instead of staying up to retarget")
	}
	if state.CurrentFingerprint == "" {
		t.Fatal("stack front merge produced no actionable digest")
	}
	digest, err := ReadDigest(harness.slug, state.CurrentFingerprint)
	if err != nil {
		t.Fatalf("ReadDigest: %v", err)
	}
	if len(digest.Items) != 1 || digest.Items[0].Reason != ReasonStackFrontMerged {
		t.Errorf("digest items = %+v, want the stack front merge", digest.Items)
	}
}

func TestUncertainDeliverySuppressesFurtherWakes(t *testing.T) {
	harness := newWatchHarnessWithClient(t, ModeStandalone, "", actionableObservation(),
		func(client *fakeOwnerClient) { client.promptErr = wrapUncertain() })

	harness.err.awaitLine(t, "owner wake uncertain")
	if !harness.state().WakesSuppressed {
		t.Fatal("uncertain delivery did not suppress further wakes")
	}

	prompts := harness.client.promptCount()
	harness.runScheduledCheck()
	if harness.client.promptCount() != prompts {
		t.Errorf("prompts = %d, want no wake while suppressed", harness.client.promptCount())
	}
	harness.err.awaitLine(t, "owner wake suppressed")
}

func wrapUncertain() error {
	return errors.Join(herdr.ErrPromptDeliveryUncertain, errors.New("prompt may be staged"))
}

func TestBusyOwnerHoldsTheFastCadenceUntilAWakeIsDelivered(t *testing.T) {
	harness := newWatchHarnessWithClient(t, ModeStandalone, "", actionableObservation(),
		func(client *fakeOwnerClient) {
			client.agents = []herdr.Agent{liveAgent("relay:demo", "pane-owner", herdr.StatusWorking)}
		})
	harness.err.awaitLine(t, "owner wake owner-busy")
	if !harness.state().AttentionPending {
		t.Error("attention stopped being pending after a busy owner")
	}
	if harness.client.promptCount() != 0 {
		t.Errorf("a busy owner was prompted with %v", harness.client.promptTexts())
	}
	harness.out.awaitLine(t, "next check at=")

	// A busy owner never took the attention, so no scheduled check may be
	// spent: the watcher stays at the fast cadence however long it lasts.
	for i := 0; i < 6; i++ {
		harness.setNow(harness.clock().Add(FastCadence))
		harness.tick()
		harness.err.awaitLine(t, "owner wake owner-busy")
		harness.out.awaitLine(t, "next check at=")
		state := harness.state()
		if state.ScheduledChecks != 0 {
			t.Fatalf("scheduled checks = %d after busy wake %d, want 0", state.ScheduledChecks, i+1)
		}
		if state.DelaySeconds != int64(FastCadence/time.Second) {
			t.Fatalf("cadence = %ds after busy wake %d, want 15m", state.DelaySeconds, i+1)
		}
	}

	// Once the owner is free, the delivered wake lets the backoff progress.
	harness.client.setAgents([]herdr.Agent{liveAgent("relay:demo", "pane-owner", herdr.StatusIdle)})
	state := harness.runScheduledCheck()
	if state.ScheduledChecks != 1 || state.DelaySeconds != int64(FastCadence/time.Second) {
		t.Errorf("state = %+v, want the first scheduled check consumed after delivery", state)
	}
}

func TestMissingOwnerHoldsTheFastCadence(t *testing.T) {
	harness := newWatchHarnessWithClient(t, ModeStandalone, "", actionableObservation(),
		func(client *fakeOwnerClient) { client.agents = nil })
	harness.err.awaitLine(t, "owner wake owner-missing")
	harness.out.awaitLine(t, "next check at=")

	for i := 0; i < 5; i++ {
		harness.setNow(harness.clock().Add(FastCadence))
		harness.tick()
		harness.err.awaitLine(t, "owner wake owner-missing")
		harness.out.awaitLine(t, "next check at=")
	}
	state := harness.state()
	if state.ScheduledChecks != 0 || state.DelaySeconds != int64(FastCadence/time.Second) {
		t.Errorf("state = %+v, want the fast cadence held for a missing owner", state)
	}
	if !state.AttentionPending {
		t.Error("attention stopped being pending with no owner to hand it to")
	}
}

func TestObservationErrorsRetryThenFailTheWatcher(t *testing.T) {
	withRuntimeHome(t)
	out, errOut := newSignalWriter(), newSignalWriter()
	var clock struct {
		sync.Mutex
		now time.Time
	}
	clock.now = time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	now := func() time.Time {
		clock.Lock()
		defer clock.Unlock()
		return clock.now
	}
	advance := func(delay time.Duration) {
		clock.Lock()
		clock.now = clock.now.Add(delay)
		clock.Unlock()
	}
	ticks := make(chan time.Time)
	options := Options{
		Mode:   ModeStandalone,
		Now:    now,
		Ticker: func(time.Duration) Ticker { return manualTicker{ticks: ticks} },
		Locate: func(slug string) (Target, error) {
			return Target{Slug: slug, Dir: t.TempDir(), PRNumber: 42}, nil
		},
		Observe: func(context.Context, Target) (Observation, error) {
			return Observation{}, errors.New("gh: HTTP 500")
		},
		Client: &fakeOwnerClient{},
		Out:    out,
		Err:    errOut,
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- Run(ctx, "demo", options) }()

	// Each retry is awaited before the clock moves again, so the watcher always
	// observes at the exact scheduled time.
	errOut.awaitLine(t, "gh: HTTP 500")
	for i := 0; i < 2; i++ {
		advance(FastCadence)
		select {
		case ticks <- now():
		case err := <-done:
			t.Fatalf("watcher stopped early: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("watcher did not consume a tick")
		}
		errOut.awaitLine(t, "gh: HTTP 500")
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("Run = %v, want the observation error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("watcher did not fail after repeated observation errors; stderr=%s", errOut.String())
	}
	state, err := ReadState("demo")
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if state.Status != StatusFailed || state.ConsecutiveErrors != maxConsecutiveErrors {
		t.Errorf("state = %+v, want a failed watcher after %d errors", state, maxConsecutiveErrors)
	}
}

func TestWatcherEventsCarryNoPullRequestContent(t *testing.T) {
	observation := actionableObservation()
	observation.PR.Title = "Add the secret widget"
	harness := newWatchHarness(t, ModeStandalone, "", observation)
	harness.out.awaitLine(t, "owner wake delivered")

	events := harness.out.String() + harness.err.String()
	for _, forbidden := range []string{
		"please rename this", "Add the secret widget", "reviewer",
	} {
		if strings.Contains(events, forbidden) {
			t.Errorf("watcher events leaked %q:\n%s", forbidden, events)
		}
	}
	state := harness.state()
	if strings.Contains(events, state.CurrentFingerprint) {
		t.Errorf("watcher events printed the whole fingerprint:\n%s", events)
	}
	if !strings.Contains(events, "fp="+state.CurrentFingerprint[:shortFingerprintLength]) {
		t.Errorf("watcher events dropped the short fingerprint:\n%s", events)
	}
}

func TestTickObservesWithoutMutatingWatcherState(t *testing.T) {
	withRuntimeHome(t)
	observation := actionableObservation()
	options := Options{
		Mode: ModeStandalone,
		Now:  func() time.Time { return time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC) },
		Locate: func(slug string) (Target, error) {
			return Target{Slug: slug, Dir: t.TempDir(), PRNumber: 42}, nil
		},
		Observe: func(context.Context, Target) (Observation, error) { return observation, nil },
	}
	digest, err := Tick(context.Background(), "demo", options)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(digest.Items) != 1 || digest.Items[0].Reason != ReasonNewComment {
		t.Fatalf("digest items = %+v, want the new comment", digest.Items)
	}
	stored, err := ReadDigest("demo", digest.Fingerprint)
	if err != nil {
		t.Fatalf("ReadDigest: %v", err)
	}
	if stored.Items[0].Body != "please rename this" {
		t.Errorf("stored digest = %+v, want the comment body", stored.Items[0])
	}
	if _, err := ReadState("demo"); err == nil {
		t.Error("Tick wrote watcher state; it must stay read-only toward the schedule")
	}
}

func TestTickReportsObservationFailures(t *testing.T) {
	withRuntimeHome(t)
	_, err := Tick(context.Background(), "demo", Options{
		Locate: func(slug string) (Target, error) {
			return Target{Slug: slug, Dir: t.TempDir(), PRNumber: 42}, nil
		},
		Observe: func(context.Context, Target) (Observation, error) {
			return Observation{}, errors.New("gh: not authenticated")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("Tick = %v, want the observation failure", err)
	}
}

func TestRunRefusesASecondWatcher(t *testing.T) {
	harness := newWatchHarness(t, ModeStandalone, "", quietObservation())
	harness.out.awaitLine(t, "next check at=")

	err := Run(context.Background(), harness.slug, Options{
		Mode: ModeStandalone,
		Locate: func(slug string) (Target, error) {
			return Target{Slug: slug, Dir: t.TempDir(), PRNumber: 42}, nil
		},
		Observe: func(context.Context, Target) (Observation, error) { return quietObservation(), nil },
	})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run = %v, want ErrAlreadyRunning", err)
	}
}
