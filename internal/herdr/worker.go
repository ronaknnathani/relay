package herdr

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// FindLiveWorker returns the first live Herdr agent whose Relay title and
// working directories identify the child project.
func FindLiveWorker(agents []Agent, childSlug, repo, worktree string) (Agent, bool) {
	if childSlug == "" || (repo == "" && worktree == "") {
		return Agent{}, false
	}
	wantTitle := "relay:" + childSlug
	for _, agent := range agents {
		worktreeOwner := pathWithin(agent.CWD, worktree) ||
			pathWithin(agent.ForegroundCWD, worktree)
		repoOwner := sameCanonicalPath(agent.CWD, repo) &&
			matchesRelayTitle(agent.TerminalTitle, wantTitle)
		if worktreeOwner || repoOwner {
			return agent, true
		}
	}
	return Agent{}, false
}

// ErrNoLiveTL reports that no live Herdr agent carries a program's tech-lead
// identity.
var ErrNoLiveTL = errors.New("no live CEO-facing tech lead session")

// ErrNoLiveProjectOwner reports that no live Herdr agent carries a project's
// Relay identity.
var ErrNoLiveProjectOwner = errors.New("no live Relay project owner session")

// DuplicateProjectOwnerError reports that more than one live Herdr agent
// claims one project identity. Ownership is ambiguous, so no caller may act.
type DuplicateProjectOwnerError struct {
	Slug    string
	PaneIDs []string
}

func (e *DuplicateProjectOwnerError) Error() string {
	return fmt.Sprintf(
		"project %q has %d live Relay sessions (panes %s); exactly one session owns a project—"+
			"focus each pane with `herdr agent focus <pane>`, exit all but one, then retry",
		e.Slug, len(e.PaneIDs), strings.Join(e.PaneIDs, ", "),
	)
}

// FindLiveProjectOwner returns the single live Herdr agent whose terminal title
// is exactly one project's Relay identity. Zero matches return
// ErrNoLiveProjectOwner and more than one return a *DuplicateProjectOwnerError.
// A near-miss title such as "relay:foo-bar" never matches "relay:foo", and
// working-directory proximity is deliberately not an ownership signal because
// several sessions can share one repository.
func FindLiveProjectOwner(agents []Agent, slug string) (Agent, error) {
	if slug == "" {
		return Agent{}, fmt.Errorf("%w: no project was named", ErrNoLiveProjectOwner)
	}
	identity := "relay:" + slug
	var matches []Agent
	for _, agent := range agents {
		if matchesRelayTitle(agent.TerminalTitle, identity) {
			matches = append(matches, agent)
		}
	}
	switch len(matches) {
	case 0:
		return Agent{}, fmt.Errorf("%w for project %q", ErrNoLiveProjectOwner, slug)
	case 1:
		return matches[0], nil
	}
	panes := make([]string, 0, len(matches))
	for _, match := range matches {
		panes = append(panes, match.PaneID)
	}
	return Agent{}, &DuplicateProjectOwnerError{Slug: slug, PaneIDs: panes}
}

// DuplicateTLError reports that more than one live Herdr agent claims one
// program's tech-lead identity. Ownership is ambiguous, so no caller may act.
type DuplicateTLError struct {
	ProgramSlug string
	PaneIDs     []string
}

func (e *DuplicateTLError) Error() string {
	return fmt.Sprintf(
		"program %q has %d live tech lead sessions (panes %s); exactly one CEO-facing tech lead owns a program—"+
			"focus each pane with `herdr agent focus <pane>`, exit all but one, then retry",
		e.ProgramSlug, len(e.PaneIDs), strings.Join(e.PaneIDs, ", "),
	)
}

// FindLiveTL returns the single live Herdr agent with the exact Relay program
// identity. Zero matches return ErrNoLiveTL and more than one return a
// *DuplicateTLError; a near-miss title such as "relay:program:<slug>-other"
// is never a match. Working-directory proximity is intentionally not an
// ownership signal because multiple program tech leads can share one
// repository.
func FindLiveTL(agents []Agent, programSlug string) (Agent, error) {
	if programSlug == "" {
		return Agent{}, fmt.Errorf("%w: no program was named", ErrNoLiveTL)
	}
	identity := "relay:program:" + programSlug
	var matches []Agent
	for _, agent := range agents {
		if matchesRelayTitle(agent.TerminalTitle, identity) {
			matches = append(matches, agent)
		}
	}
	switch len(matches) {
	case 0:
		return Agent{}, fmt.Errorf("%w for program %q", ErrNoLiveTL, programSlug)
	case 1:
		return matches[0], nil
	}
	panes := make([]string, 0, len(matches))
	for _, match := range matches {
		panes = append(panes, match.PaneID)
	}
	return Agent{}, &DuplicateTLError{ProgramSlug: programSlug, PaneIDs: panes}
}

func matchesRelayTitle(title, identity string) bool {
	return title == identity || strings.HasPrefix(title, identity+" - ")
}

func sameCanonicalPath(left, right string) bool {
	return strings.TrimSpace(left) != "" &&
		strings.TrimSpace(right) != "" &&
		canonicalPath(left) == canonicalPath(right)
}

func pathWithin(path, root string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	relative, err := filepath.Rel(canonicalPath(root), canonicalPath(path))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// WorkerName derives a stable Herdr agent name from a program and work item.
func WorkerName(programSlug, itemID string) string {
	raw := programSlug + "-" + itemID
	name := sanitizeWorkerName(raw)
	if len(name) <= 32 {
		return name
	}
	sum := sha256.Sum256([]byte(raw))
	suffix := fmt.Sprintf("-%x", sum[:4])
	prefix := strings.TrimRight(name[:32-len(suffix)], "-_")
	return prefix + suffix
}

func sanitizeWorkerName(value string) string {
	value = strings.ToLower(value)
	var name strings.Builder
	name.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			name.WriteRune(r)
		default:
			name.WriteByte('-')
		}
	}
	result := strings.Trim(name.String(), "-_")
	if result == "" {
		result = "worker"
	}
	if result[0] < 'a' || result[0] > 'z' {
		result = "w-" + result
	}
	return result
}

func canonicalPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(absolute)
}
