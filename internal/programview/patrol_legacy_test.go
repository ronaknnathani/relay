package programview

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ronaknnathani/relay/internal/program"
)

// heldPatrolState writes one patrol record for slug and holds its lock so the
// read-only view reports the patrol as running.
func heldPatrolState(t *testing.T, slug, state string) program.Program {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	p, err := program.New(slug, "Presence view", t.TempDir(), "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(program.RelayDir(), "run", slug)
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "patrol.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(runtimeDir, "patrol.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	})
	return p
}

// The read-only view applies the same compatibility rule as the patrol itself,
// so a record written before the tech-lead rename still shows presence.
func TestBuildReadsRetiredPatrolPresenceField(t *testing.T) {
	p := heldPatrolState(t, "legacy-presence", `{
  "program_slug": "legacy-presence",
  "status": "running",
  "reasons": [],
  "cto_present": true
}`)
	got, err := Build(p.Slug, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Patrol.TLPresent {
		t.Fatalf("patrol dto = %+v, want tech lead presence from the retired field", got.Patrol)
	}
}

// When both fields are present the canonical one wins, matching patrol.State.
func TestBuildPrefersCanonicalPatrolPresenceOverRetired(t *testing.T) {
	p := heldPatrolState(t, "both-presence", `{
  "program_slug": "both-presence",
  "status": "running",
  "reasons": [],
  "tl_present": false,
  "cto_present": true
}`)
	got, err := Build(p.Slug, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Patrol.TLPresent {
		t.Fatalf("patrol dto = %+v, want the canonical false to win", got.Patrol)
	}
}
