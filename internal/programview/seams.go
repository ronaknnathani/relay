// Package programview builds read-only views of Relay programs and their child projects.
package programview

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
)

// ProjectViews returns observed active and linked archived child project state
// with recorded pull requests overlaid by authoritative GitHub lifecycle state.
// Individually unreadable linked children degrade to unavailable views plus
// structured warnings instead of failing the whole program view.
func ProjectViews(p program.Program) ([]program.ProjectView, []ProjectWarning, error) {
	return projectViews(p, githubPRIndexForRefs)
}

// ProjectViewsWithPRIndex returns project state overlaid with an
// already-resolved pull request index.
func ProjectViewsWithPRIndex(p program.Program, index PRIndex) ([]program.ProjectView, []ProjectWarning, error) {
	return projectViews(p, staticPRIndexLoader(index))
}

func staticPRIndexLoader(index PRIndex) PRIndexLoader {
	return func(string, []string) PRIndex { return index }
}

// OverlayProjectViewsWithPRIndex returns a copy with recorded pull request
// lifecycle fields updated from an already-fetched index.
func OverlayProjectViewsWithPRIndex(views []program.ProjectView, index PRIndex) []program.ProjectView {
	result := append([]program.ProjectView(nil), views...)
	for i := range result {
		result[i] = overlayProjectView(result[i], index)
	}
	return result
}

// overlayProjectView applies authoritative GitHub lifecycle state to one child
// project view. A verified merge is never downgraded by a closed pull request,
// and archived children are reconciled the same way active children are.
func overlayProjectView(view program.ProjectView, index PRIndex) program.ProjectView {
	if index == nil || view.PRRef == "" || view.Unavailable {
		return view
	}
	state, found := index.Lookup(view.PRRef)
	if !found {
		return view
	}
	switch state {
	case PRStateOpen:
		view.HasPR = true
		view.Merged = false
		view.PRClosed = false
	case PRStateMerged:
		view.HasPR = false
		view.Merged = true
		view.PRClosed = false
	case PRStateClosed:
		view.HasPR = false
		view.PRClosed = !view.Merged
	}
	if view.Archived {
		view.Orphaned = !view.Merged
	}
	return view
}

// ProjectWarning identifies one child project whose observed state was unavailable.
type ProjectWarning struct {
	ProjectSlug string
	Message     string
}

func projectViews(p program.Program, load PRIndexLoader) ([]program.ProjectView, []ProjectWarning, error) {
	entries, err := os.ReadDir(project.ActiveDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("read active projects directory %s: %w", project.ActiveDir(), err)
	}

	linkedSlugs := make(map[string]bool)
	for _, item := range p.Items {
		if item.ProjectSlug != "" {
			linkedSlugs[item.ProjectSlug] = true
		}
	}

	views := make([]program.ProjectView, 0, len(entries))
	warnings := []ProjectWarning{}
	active := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !linkedSlugs[entry.Name()] {
			continue
		}
		manifest, loadErr := project.Load(project.ManifestPath(project.ActiveDir(), entry.Name()))
		if loadErr != nil {
			warnings = append(warnings, ProjectWarning{
				ProjectSlug: entry.Name(),
				Message:     fmt.Sprintf("load active project %q: %v", entry.Name(), loadErr),
			})
			views = append(views, unavailableProjectView(p, entry.Name(), p.Repo, project.Manifest{}))
			active[entry.Name()] = true
			continue
		}
		if manifest.Repo != p.Repo {
			continue
		}
		active[manifest.Slug] = true
		view, viewErr := ActiveProjectView(manifest)
		if viewErr != nil {
			warnings = append(warnings, ProjectWarning{
				ProjectSlug: manifest.Slug,
				Message:     fmt.Sprintf("load active project %q state: %v", manifest.Slug, viewErr),
			})
			views = append(views, unavailableProjectView(p, manifest.Slug, manifest.Repo, manifest))
			continue
		}
		views = append(views, view)
	}

	archivedSlugs := make(map[string]bool)
	for slug := range linkedSlugs {
		if !active[slug] {
			archivedSlugs[slug] = true
		}
	}
	archivedEntries := make(map[string]bool)
	if len(archivedSlugs) > 0 {
		entries, readErr := os.ReadDir(project.ArchivedDir())
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, warnings, fmt.Errorf("read archived projects directory %s: %w", project.ArchivedDir(), readErr)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				archivedEntries[entry.Name()] = true
			}
		}
	}
	for slug := range archivedSlugs {
		if !archivedEntries[slug] {
			views = append(views, program.ProjectView{Slug: slug, Repo: p.Repo, Orphaned: true})
			continue
		}
		manifest, loadErr := project.Load(project.ManifestPath(project.ArchivedDir(), slug))
		if loadErr != nil {
			warnings = append(warnings, ProjectWarning{
				ProjectSlug: slug,
				Message:     fmt.Sprintf("load archived project %q: %v", slug, loadErr),
			})
			views = append(views, unavailableProjectView(p, slug, p.Repo, project.Manifest{}))
			continue
		}
		hasPR, prRef, prErr := RecordedPR(manifest, "")
		if prErr != nil {
			warnings = append(warnings, ProjectWarning{
				ProjectSlug: slug,
				Message:     fmt.Sprintf("load archived project %q PR: %v", slug, prErr),
			})
			views = append(views, unavailableProjectView(p, slug, manifest.Repo, manifest))
			continue
		}
		views = append(views, program.ProjectView{
			Slug:     manifest.Slug,
			Repo:     manifest.Repo,
			HasPR:    hasPR,
			PRRef:    prRef,
			Merged:   manifest.Merged,
			Archived: true,
			Orphaned: !manifest.Merged,
		})
	}

	if load != nil {
		if index := load(p.Repo, recordedRefs(views)); index != nil {
			for i := range views {
				views[i] = overlayProjectView(views[i], index)
			}
		}
	}

	sort.Slice(views, func(i, j int) bool { return views[i].Slug < views[j].Slug })
	sort.Slice(warnings, func(i, j int) bool {
		if warnings[i].ProjectSlug == warnings[j].ProjectSlug {
			return warnings[i].Message < warnings[j].Message
		}
		return warnings[i].ProjectSlug < warnings[j].ProjectSlug
	})
	return views, warnings, nil
}

func recordedRefs(views []program.ProjectView) []string {
	refs := make([]string, 0, len(views))
	for _, view := range views {
		if view.PRRef != "" && !view.Unavailable {
			refs = append(refs, view.PRRef)
		}
	}
	return refs
}

// unavailableProjectView returns the most conservative view of a child project
// whose observed state could not be read: its recorded pull request still
// consumes capacity, and reconciliation never merges or orphans it.
func unavailableProjectView(
	p program.Program,
	slug string,
	repo string,
	manifest project.Manifest,
) program.ProjectView {
	if strings.TrimSpace(repo) == "" {
		repo = p.Repo
	}
	view := program.ProjectView{Slug: slug, Repo: repo, Unavailable: true}
	if hasPR, ref, err := RecordedPR(manifest, ""); err == nil && hasPR {
		view.HasPR = true
		view.PRRef = ref
		return view
	}
	for _, item := range p.Items {
		if item.ProjectSlug == slug && item.PRRef != "" &&
			(item.Status == program.ItemDispatched || item.Status == program.ItemInReview) {
			view.HasPR = true
			view.PRRef = item.PRRef
			return view
		}
	}
	return view
}

// ActiveProjectView returns observed state for one active child project.
func ActiveProjectView(manifest project.Manifest) (program.ProjectView, error) {
	return activeProjectView(manifest, project.StatePath(manifest.Slug))
}

func activeProjectView(manifest project.Manifest, statePath string) (program.ProjectView, error) {
	hasPR, prRef, err := RecordedPR(manifest, statePath)
	if err != nil {
		return program.ProjectView{}, err
	}
	base := manifest.BaseBranch
	if base == "" {
		base = gitx.DetectDefaultBranch(manifest.Repo)
	}
	baseRef := base
	if base != "" && gitx.HasOrigin(manifest.Repo) && gitx.RevParse(manifest.Repo, "origin/"+base) != "" {
		baseRef = "origin/" + base
	}
	merged := baseRef != "" && gitx.IsWorkMerged(manifest.Repo, manifest.Branch, baseRef, manifest.StartSHA)
	return program.ProjectView{
		Slug:   manifest.Slug,
		Repo:   manifest.Repo,
		HasPR:  hasPR,
		PRRef:  prRef,
		Merged: merged,
	}, nil
}

// RecordedPR returns the pull request recorded in workflow state or the manifest.
func RecordedPR(manifest project.Manifest, statePath string) (bool, string, error) {
	if statePath != "" {
		if _, err := os.Stat(statePath); err == nil {
			state, err := project.LoadState(statePath)
			if err != nil {
				return false, "", err
			}
			if state.PR.URL != "" {
				return true, state.PR.URL, nil
			}
			if state.PR.Number > 0 {
				return true, "#" + strconv.Itoa(state.PR.Number), nil
			}
		} else if !os.IsNotExist(err) {
			return false, "", fmt.Errorf("stat project state %s: %w", statePath, err)
		}
	}
	if manifest.PR.URL != nil && *manifest.PR.URL != "" {
		return true, *manifest.PR.URL, nil
	}
	if manifest.PR.Number != nil && *manifest.PR.Number > 0 {
		return true, "#" + strconv.Itoa(*manifest.PR.Number), nil
	}
	return false, "", nil
}

// NextCommand returns the CLI command for a program plan's next action.
func NextCommand(p program.Program, view program.View) string {
	switch {
	case strings.HasPrefix(view.NextAction, "resolve "):
		id := strings.TrimPrefix(view.NextAction, "resolve ")
		for _, decision := range p.Decisions {
			if decision.ID == id && decision.Kind == program.DecisionContract {
				return fmt.Sprintf("relay program contract approve %s %s --by ceo", p.Slug, decision.ContractRef)
			}
		}
		return fmt.Sprintf("relay program decision resolve %s %s --answer <answer>", p.Slug, id)
	case view.NextAction == "request approval":
		return "relay program submit " + p.Slug
	case view.NextAction == "approve program":
		return "relay program approve " + p.Slug
	case view.NextAction == "resume program":
		return "relay program release " + p.Slug
	case strings.HasPrefix(view.NextAction, "dispatch "):
		return fmt.Sprintf("relay program dispatch %s %s", p.Slug, strings.TrimPrefix(view.NextAction, "dispatch "))
	case view.NextAction == "reconcile in-flight work":
		return "relay program tick " + p.Slug
	case view.NextAction == "complete program":
		return "relay program finish " + p.Slug
	case len(view.Blocked) > 0:
		blocked := view.Blocked[0]
		return fmt.Sprintf("blocked: %s (%s)", blocked.Item.ID, strings.Join(blocked.Reasons, "; "))
	default:
		return "no action"
	}
}
