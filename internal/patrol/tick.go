package patrol

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/project"
)

// SnapshotBuilder constructs one read-only program snapshot.
type SnapshotBuilder func(string, programview.Options) (programview.Snapshot, error)

// Ticker provides patrol wakeups.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// TickerFactory creates the low-frequency patrol wakeup ticker.
type TickerFactory func(time.Duration) Ticker

// Options supplies patrol runtime seams.
type Options struct {
	Now           func() time.Time
	Ticker        TickerFactory
	BuildSnapshot SnapshotBuilder
	Agents        programview.AgentLister
	// Turns rings the existing live tech lead session when attention changes.
	Turns TurnRunner
	// Notifier optionally raises a best-effort desktop notification.
	Notifier Notifier
	// Out receives routine operational patrol events and Err receives the
	// outcomes that leave attention undelivered. The foreground `relay program
	// patrol run` process supplies its own stdout and stderr, which makes the
	// Herdr patrol pane the patrol's log; nothing is ever written to a file. A
	// nil writer is silent.
	Out io.Writer
	Err io.Writer
	// Location is the zone pane events are stamped in. It defaults to the
	// host's own zone, because the pane is read by whoever is sitting in front
	// of it; only a test pins it, and nothing persisted is affected.
	Location     *time.Location
	PID          int
	RelayVersion string
}

// Tick returns one read-only diagnostic observation. It does not acquire the
// patrol lock, write runtime state, or ring the live tech lead.
func Tick(slug string, options Options) (Observation, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return Observation{}, fmt.Errorf("patrol program slug: %w", err)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	builder := options.BuildSnapshot
	if builder == nil {
		absent, err := activeAndArchivedAbsent(slug)
		if err != nil {
			return Observation{}, err
		}
		if absent {
			return Observation{
				ProgramSlug: slug, Stop: true, StopReason: "program manifest absent",
				Reasons: []Reason{}, AttentionKeys: []string{},
			}, nil
		}
		builder = programview.Build
	}
	agents := options.Agents
	if agents == nil {
		agents = programview.NewHerdrAgentLister()
	}
	snapshot, err := builder(slug, programview.Options{Now: now, Agents: agents})
	if err != nil {
		return Observation{}, fmt.Errorf("build patrol snapshot for program %q: %w", slug, err)
	}
	observation := observeSnapshot(snapshot, contractDriftReasons(snapshot))
	if observation.ProgramSlug == "" {
		observation.ProgramSlug = slug
	}
	return observation, nil
}

func activeAndArchivedAbsent(slug string) (bool, error) {
	for _, dir := range []string{program.ActiveDir(), program.ArchivedDir()} {
		path := program.ManifestPath(dir, slug)
		if _, err := os.Stat(path); err == nil {
			return false, nil
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("stat program manifest %s: %w", path, err)
		}
	}
	return true, nil
}
