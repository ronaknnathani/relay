package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Phase status values. A workflow phase moves pending -> in-progress -> done.
const (
	PhasePending    = "pending"
	PhaseInProgress = "in-progress"
	PhaseDone       = "done"
)

// PhaseState is the status of a single workflow phase, plus optional progress
// detail: the artifact it produced and a free-form task marker (e.g. "3/7").
type PhaseState struct {
	Status   string `json:"status"`
	Artifact string `json:"artifact,omitempty"`
	Task     string `json:"task,omitempty"`
}

// PRRef records the pull request a project produced.
type PRRef struct {
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
}

// WorkflowState is the resumable, machine-owned state for one project's run
// through a workflow. The relay binary is its sole writer; skills mutate it
// only through `relay state`, never by editing JSON by hand, so the schema
// stays valid across agents (Claude/Copilot/Codex). Order is the canonical
// phase sequence the workflow skill declares at init; Phases tracks each
// phase's status. The current/next phase is derived from Order + Phases rather
// than stored, so there is a single source of truth.
//
// Concurrency: SaveState is atomic (unique temp + rename), so a reader never
// sees a half-written file. The load-modify-save sequence in each `relay state`
// command is not locked, so concurrent writers to the same slug are
// last-write-wins. This is acceptable because callers serialize writes per
// project (one writer per branch/run), which the orchestrator guarantees.
type WorkflowState struct {
	Slug     string                `json:"slug"`
	Workflow string                `json:"workflow"`
	Order    []string              `json:"order"`
	Phases   map[string]PhaseState `json:"phases"`
	PR       PRRef                 `json:"pr"`
	Updated  string                `json:"updated"`
}

// validStatus reports whether s is one of the three phase statuses.
func validStatus(s string) bool {
	return s == PhasePending || s == PhaseInProgress || s == PhaseDone
}

// ValidateSlug rejects slugs that could escape the project tree or are unsafe
// as a path segment. The state commands resolve files from a slug, so an
// unsanitized `..`-bearing slug would otherwise write outside the project dir.
func ValidateSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("slug cannot be empty")
	}
	if strings.ContainsAny(slug, "/\\") || strings.Contains(slug, "..") || strings.HasPrefix(slug, ".") {
		return fmt.Errorf("invalid slug %q: must be a single path segment without '/', '..', or a leading '.'", slug)
	}
	return nil
}

// StatePath returns the state.json path for an active project slug.
func StatePath(slug string) string {
	return filepath.Join(ActiveDir(), slug, "state.json")
}

// NewState builds a fresh state for a workflow with every phase pending.
// It errors on an empty order or duplicate phase names so a malformed
// declaration fails at init rather than producing an unusable state machine.
func NewState(slug, workflow string, order []string) (WorkflowState, error) {
	if len(order) == 0 {
		return WorkflowState{}, fmt.Errorf("workflow %q: phase order is empty", workflow)
	}
	// Copy the order so a later mutation of the caller's slice cannot diverge
	// the canonical order from the phases map keys.
	ordered := append([]string(nil), order...)
	phases := make(map[string]PhaseState, len(ordered))
	for _, p := range ordered {
		if _, dup := phases[p]; dup {
			return WorkflowState{}, fmt.Errorf("workflow %q: duplicate phase %q", workflow, p)
		}
		phases[p] = PhaseState{Status: PhasePending}
	}
	return WorkflowState{Slug: slug, Workflow: workflow, Order: ordered, Phases: phases}, nil
}

// validate asserts the structural invariants the state machine relies on:
// non-empty order, no duplicate phases, every order entry present in Phases,
// and every status valid. LoadState calls it so a partially-written, drifted,
// or hand-edited file (the cross-agent source of truth any agent may have
// touched) fails loudly at load instead of misbehaving downstream.
func (ws WorkflowState) validate() error {
	if len(ws.Order) == 0 {
		return fmt.Errorf("state %q: empty phase order", ws.Slug)
	}
	seen := make(map[string]bool, len(ws.Order))
	for _, p := range ws.Order {
		if seen[p] {
			return fmt.Errorf("state %q: duplicate phase %q in order", ws.Slug, p)
		}
		seen[p] = true
		ph, ok := ws.Phases[p]
		if !ok {
			return fmt.Errorf("state %q: phase %q in order but missing from phases", ws.Slug, p)
		}
		if !validStatus(ph.Status) {
			return fmt.Errorf("state %q: phase %q has invalid status %q", ws.Slug, p, ph.Status)
		}
	}
	return nil
}

// LoadState reads, decodes, and validates a state file from path.
func LoadState(path string) (WorkflowState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return WorkflowState{}, fmt.Errorf("read state %s: %w", path, err)
	}
	var ws WorkflowState
	if err := json.Unmarshal(data, &ws); err != nil {
		return WorkflowState{}, fmt.Errorf("parse state %s: %w", path, err)
	}
	if err := ws.validate(); err != nil {
		return WorkflowState{}, fmt.Errorf("invalid state %s: %w", path, err)
	}
	return ws, nil
}

// marshalState stamps Updated and renders ws as indented JSON with a trailing
// newline.
func marshalState(ws WorkflowState) ([]byte, error) {
	ws.Updated = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode state: %w", err)
	}
	return append(data, '\n'), nil
}

// SaveState writes ws to path atomically (unique temp file + rename), stamping
// Updated, so a reader never sees a partial write and concurrent writers never
// share a temp file.
func SaveState(path string, ws WorkflowState) error {
	data, err := marshalState(ws)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// CreateState writes a new state file, failing if one already exists, so init
// cannot clobber an in-progress run (no check-then-write race). The returned
// error satisfies errors.Is(err, fs.ErrExist) when the file is already present.
func CreateState(path string, ws WorkflowState) error {
	data, err := marshalState(ws)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Next returns the first phase in Order that is not yet done, or "" when every
// phase is done. A resume-first skill calls this to learn what to do next: an
// interrupted in-progress phase is returned so the run continues it.
func (ws WorkflowState) Next() string {
	for _, p := range ws.Order {
		if ws.Phases[p].Status != PhaseDone {
			return p
		}
	}
	return ""
}

// Current returns the phase the run is on: the in-progress phase if one exists,
// otherwise the first not-done phase, otherwise "" when all phases are done.
func (ws WorkflowState) Current() string {
	for _, p := range ws.Order {
		if ws.Phases[p].Status == PhaseInProgress {
			return p
		}
	}
	return ws.Next()
}

// After returns the phase that follows the given phase in Order, or "" when
// the phase is last or unknown.
func (ws WorkflowState) After(phase string) string {
	for i, p := range ws.Order {
		if p == phase && i+1 < len(ws.Order) {
			return ws.Order[i+1]
		}
	}
	return ""
}

// SetPhase updates one phase's status and, when non-empty, its artifact/task.
// It errors on an unknown phase or invalid status. Any status-to-status move is
// allowed, including a backward one (e.g. done -> in-progress), so a skill can
// deliberately re-open a phase to redo it during a fix or resume.
func (ws *WorkflowState) SetPhase(name, status, artifact, task string) error {
	ph, ok := ws.Phases[name]
	if !ok {
		return fmt.Errorf("unknown phase %q", name)
	}
	if !validStatus(status) {
		return fmt.Errorf("invalid status %q (want pending|in-progress|done)", status)
	}
	ph.Status = status
	if artifact != "" {
		ph.Artifact = artifact
	}
	if task != "" {
		ph.Task = task
	}
	ws.Phases[name] = ph
	return nil
}

// Advance marks the current phase (the one Current reports) done and returns
// the next not-done phase ("" when the run is now complete). It errors when all
// phases are already done, or when the state is corrupt enough that the current
// phase is unknown, so neither case is silently ignored.
func (ws *WorkflowState) Advance() (string, error) {
	cur := ws.Current()
	if cur == "" {
		return "", fmt.Errorf("all phases already done")
	}
	if err := ws.SetPhase(cur, PhaseDone, "", ""); err != nil {
		return "", fmt.Errorf("advance %q: %w", cur, err)
	}
	return ws.Next(), nil
}

// SetPR records the pull request the project produced. A zero number or empty
// URL is ignored so a partial update (number now, URL later) does not clobber
// an already-set field; clearing a recorded PR is intentionally not supported.
func (ws *WorkflowState) SetPR(number int, url string) {
	if number != 0 {
		ws.PR.Number = number
	}
	if url != "" {
		ws.PR.URL = url
	}
}

// ProgressPath returns the progress.md path for an active project slug.
func ProgressPath(slug string) string {
	return filepath.Join(ActiveDir(), slug, "progress.md")
}

// AppendProgress appends a timestamped line to path, creating it if needed.
// progress.md is the human-readable, append-only audit trail that a restarted
// agent reads to see what already happened.
func AppendProgress(path, msg string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	stamp := time.Now().UTC().Format(time.RFC3339)
	if _, err := fmt.Fprintf(f, "- %s  %s\n", stamp, msg); err != nil {
		return fmt.Errorf("append %s: %w", path, err)
	}
	return nil
}
