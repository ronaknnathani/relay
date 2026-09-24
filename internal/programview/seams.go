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
	return func(string, []string) PRIndexLoadResult {
		return PRIndexLoadResult{Index: index}
	}
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

	type linkedChild struct {
		itemID string
		repo   string
	}
	linkedChildren := make(map[string]linkedChild)
	for _, item := range p.Items {
		if item.ProjectSlug != "" {
			linkedChildren[item.ProjectSlug] = linkedChild{itemID: item.ID, repo: item.Repo}
		}
	}

	views := make([]program.ProjectView, 0, len(entries))
	warnings := []ProjectWarning{}
	active := make(map[string]bool, len(entries))
	repositories := make(map[string]projectRepositoryState)
	for _, entry := range entries {
		expected, linked := linkedChildren[entry.Name()]
		if !entry.IsDir() || !linked {
			continue
		}
		active[entry.Name()] = true
		manifest, loadErr := project.Load(project.ManifestPath(project.ActiveDir(), entry.Name()))
		if loadErr != nil {
			warnings = append(warnings, ProjectWarning{
				ProjectSlug: entry.Name(),
				Message:     fmt.Sprintf("load active project %q: %v", entry.Name(), loadErr),
			})
			views = append(views, unavailableProjectView(p, entry.Name(), expected.repo, project.Manifest{}))
			continue
		}
		if ownershipErr := validateLinkedChildManifest(
			p.Slug, entry.Name(), expected.itemID, expected.repo, manifest,
		); ownershipErr != nil {
			warnings = append(warnings, ProjectWarning{
				ProjectSlug: entry.Name(),
				Message:     fmt.Sprintf("active project %q does not match linked item %s: %v", entry.Name(), expected.itemID, ownershipErr),
			})
			continue
		}
		repository, found := repositories[manifest.Repo]
		if !found {
			repository = loadProjectRepositoryState(manifest.Repo, manifest.BaseBranch == "")
			repositories[manifest.Repo] = repository
		} else if manifest.BaseBranch == "" && repository.defaultBranch == "" && repository.err == nil {
			repository = loadProjectRepositoryState(manifest.Repo, true)
			repositories[manifest.Repo] = repository
		}
		if repository.err != nil {
			warnings = append(warnings, ProjectWarning{
				ProjectSlug: entry.Name(),
				Message: fmt.Sprintf(
					"inspect active project %q repository %s: %v",
					entry.Name(), manifest.Repo, repository.err,
				),
			})
			views = append(views, unavailableProjectView(p, entry.Name(), expected.repo, manifest))
			continue
		}
		view, viewErr := activeProjectViewWithRepository(
			manifest, project.StatePath(entry.Name()), repository,
		)
		if viewErr != nil {
			warnings = append(warnings, ProjectWarning{
				ProjectSlug: entry.Name(),
				Message:     fmt.Sprintf("load active project %q state: %v", entry.Name(), viewErr),
			})
			views = append(views, unavailableProjectView(p, entry.Name(), expected.repo, manifest))
			continue
		}
		views = append(views, view)
	}

	archivedSlugs := make(map[string]bool)
	for slug := range linkedChildren {
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
		expected := linkedChildren[slug]
		if !archivedEntries[slug] {
			views = append(views, program.ProjectView{Slug: slug, Repo: expected.repo, Orphaned: true})
			continue
		}
		manifest, loadErr := project.Load(project.ManifestPath(project.ArchivedDir(), slug))
		if loadErr != nil {
			warnings = append(warnings, ProjectWarning{
				ProjectSlug: slug,
				Message:     fmt.Sprintf("load archived project %q: %v", slug, loadErr),
			})
			views = append(views, unavailableProjectView(p, slug, expected.repo, project.Manifest{}))
			continue
		}
		if ownershipErr := validateLinkedChildManifest(
			p.Slug, slug, expected.itemID, expected.repo, manifest,
		); ownershipErr != nil {
			warnings = append(warnings, ProjectWarning{
				ProjectSlug: slug,
				Message:     fmt.Sprintf("archived project %q does not match linked item %s: %v", slug, expected.itemID, ownershipErr),
			})
			continue
		}
		repository, found := repositories[manifest.Repo]
		if !found {
			repository = loadProjectRepositoryState(manifest.Repo, false)
			repositories[manifest.Repo] = repository
		}
		if repository.err != nil {
			warnings = append(warnings, ProjectWarning{
				ProjectSlug: slug,
				Message: fmt.Sprintf(
					"inspect archived project %q repository %s: %v",
					slug, manifest.Repo, repository.err,
				),
			})
			views = append(views, unavailableProjectView(p, slug, expected.repo, manifest))
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
		viewsByRepo := make(map[string][]int)
		for i, view := range views {
			if view.PRRef != "" && !view.Unavailable {
				viewsByRepo[view.Repo] = append(viewsByRepo[view.Repo], i)
			}
		}
		for repo, indexes := range viewsByRepo {
			refs := make([]string, 0, len(indexes))
			for _, index := range indexes {
				refs = append(refs, views[index].PRRef)
			}
			result := load(repo, refs)
			if result.Index != nil {
				for _, viewIndex := range indexes {
					views[viewIndex] = overlayProjectView(views[viewIndex], result.Index)
				}
			}
			for _, loadErr := range result.Errors {
				for _, viewIndex := range indexes {
					if strings.TrimSpace(views[viewIndex].PRRef) != strings.TrimSpace(loadErr.Ref) {
						continue
					}
					warnings = append(warnings, ProjectWarning{
						ProjectSlug: views[viewIndex].Slug,
						Message:     loadErr.Err.Error(),
					})
				}
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
	repository := loadProjectRepositoryState(manifest.Repo, manifest.BaseBranch == "")
	if repository.err != nil {
		return program.ProjectView{}, repository.err
	}
	return activeProjectViewWithRepository(manifest, statePath, repository)
}

type projectRepositoryState struct {
	defaultBranch string
	err           error
}

func loadProjectRepositoryState(repo string, needsDefaultBranch bool) projectRepositoryState {
	_, err := gitx.CanonicalRepositoryRoot(repo)
	if err != nil {
		return projectRepositoryState{err: err}
	}
	if !needsDefaultBranch {
		return projectRepositoryState{}
	}
	defaultBranch, err := gitx.DetectDefaultBranchWithError(repo)
	if err != nil {
		return projectRepositoryState{err: err}
	}
	return projectRepositoryState{defaultBranch: defaultBranch}
}

func activeProjectViewWithRepository(
	manifest project.Manifest,
	statePath string,
	repository projectRepositoryState,
) (program.ProjectView, error) {
	hasPR, prRef, err := RecordedPR(manifest, statePath)
	if err != nil {
		return program.ProjectView{}, err
	}
	base := manifest.BaseBranch
	if base == "" {
		base = repository.defaultBranch
	}
	baseRef := base
	if base != "" {
		remoteRef := "refs/remotes/origin/" + base
		remoteExists, err := gitx.RefExists(manifest.Repo, remoteRef)
		if err != nil {
			return program.ProjectView{}, fmt.Errorf("inspect base branch %q: %w", base, err)
		}
		if remoteExists {
			baseRef = remoteRef
		} else {
			baseRef = "refs/heads/" + base
		}
	}
	merged := false
	if baseRef != "" {
		merged, err = gitx.WorkMerged(manifest.Repo, manifest.Branch, baseRef, manifest.StartSHA)
		if err != nil {
			return program.ProjectView{}, fmt.Errorf(
				"inspect merge state for branch %q against %q: %w",
				manifest.Branch, baseRef, err,
			)
		}
	}
	return program.ProjectView{
		Slug:   manifest.Slug,
		Repo:   manifest.Repo,
		HasPR:  hasPR,
		PRRef:  prRef,
		Merged: merged,
	}, nil
}

func validateLinkedChildManifest(
	programSlug, linkedSlug, itemID, repo string,
	manifest project.Manifest,
) error {
	if manifest.Slug != linkedSlug || manifest.Program != programSlug ||
		manifest.ProgramItem != itemID || manifest.Repo != repo {
		return fmt.Errorf(
			"slug=%q program=%q item=%q repo=%q, want slug=%q program=%q item=%q repo=%q",
			manifest.Slug, manifest.Program, manifest.ProgramItem, manifest.Repo,
			linkedSlug, programSlug, itemID, repo,
		)
	}
	return nil
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
