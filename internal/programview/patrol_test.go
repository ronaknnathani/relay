package programview

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ronaknnathani/relay/internal/program"
)

func TestBuildReadsPatrolRuntimeWithoutTreatingMissingAsWarning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, err := program.New("patrol-view", "Patrol view", t.TempDir(), "copilot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}

	missing, err := Build(p.Slug, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Patrol.Running || missing.Patrol.Status != "not-running" ||
		missing.SourceHealth.Patrol.Status != "ok" ||
		len(missing.SourceHealth.Patrol.Warnings) != 0 {
		t.Fatalf("missing patrol = dto %+v source %+v", missing.Patrol, missing.SourceHealth.Patrol)
	}

	runtimeDir := filepath.Join(program.RelayDir(), "run", p.Slug)
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := `{
  "schema": "relay.patrol.v1",
  "version": 1,
  "program_slug": "patrol-view",
  "status": "running",
  "last_tick_at": "2026-08-26T18:00:00Z",
  "next_tick_at": "2026-08-26T18:15:00Z",
  "delay_seconds": 900,
  "reasons": [{"code":"open-decision:d1","text":"Decision d1 is awaiting resolution."}],
  "cto_present": true,
  "error": ""
}`
	path := filepath.Join(runtimeDir, "patrol.json")
	if err := os.WriteFile(path, []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Build(p.Slug, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Patrol.Status != "not-running" || got.Patrol.Running ||
		got.Patrol.DelaySeconds != 900 || got.Patrol.CTOPresent ||
		len(got.Patrol.Reasons) != 1 || got.Patrol.Reasons[0].Code != "open-decision:d1" {
		t.Fatalf("stale patrol dto = %+v", got.Patrol)
	}

	lockPath := filepath.Join(runtimeDir, "patrol.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
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
	got, err = Build(p.Slug, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Patrol.Status != "running" || !got.Patrol.Running {
		t.Fatalf("live patrol dto = %+v", got.Patrol)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missingLive, err := Build(p.Slug, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !missingLive.Patrol.Running || missingLive.Patrol.Status != "running" ||
		missingLive.SourceHealth.Patrol.Status != "degraded" {
		t.Fatalf("live patrol with missing state = dto %+v source %+v", missingLive.Patrol, missingLive.SourceHealth.Patrol)
	}

	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	corrupt, err := Build(p.Slug, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !corrupt.Patrol.Running || corrupt.Patrol.Status != "running" ||
		corrupt.SourceHealth.Patrol.Status != "degraded" ||
		!strings.Contains(strings.Join(corrupt.SourceHealth.Patrol.Warnings, "\n"), "patrol.json") {
		t.Fatalf("corrupt patrol = dto %+v source %+v", corrupt.Patrol, corrupt.SourceHealth.Patrol)
	}
}

func TestBuildWarnsWhenProgramAgentCannotExposeNamedCTO(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, err := program.New("codex-view", "Codex view", t.TempDir(), "codex", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Create(p); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(program.RelayDir(), "run", p.Slug)
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "patrol.json"), []byte(`{
  "program_slug": "codex-view",
  "status": "running",
  "reasons": [],
  "cto_present": true
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(runtimeDir, "patrol.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()

	got, err := Build(p.Slug, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Patrol.Running || got.Patrol.CTOPresent ||
		!strings.Contains(got.Patrol.Warning, "codex") ||
		!strings.Contains(got.Patrol.Warning, "named sessions") ||
		got.SourceHealth.Patrol.Status != "degraded" {
		t.Fatalf("unsupported patrol agent = dto %+v source %+v", got.Patrol, got.SourceHealth.Patrol)
	}
}
