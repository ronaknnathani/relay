package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/project"
)

type projectResourceIdentity struct {
	repository string
	branch     string
	worktree   string
}

type projectResourceOwner struct {
	slug string
	path string
}

type projectResourceOwnershipIndex struct {
	identitiesByPath map[string]projectResourceIdentity
	branchOwners     map[string][]projectResourceOwner
	worktreeOwners   map[string][]projectResourceOwner
}

func loadProjectResourceOwnershipIndex(
	activeResults []project.ManifestLoadResult,
) (projectResourceOwnershipIndex, []error) {
	index := projectResourceOwnershipIndex{
		identitiesByPath: make(map[string]projectResourceIdentity),
		branchOwners:     make(map[string][]projectResourceOwner),
		worktreeOwners:   make(map[string][]projectResourceOwner),
	}
	var issues []error
	if activeResults == nil {
		var err error
		activeResults, err = project.LoadAllResults(project.ActiveDir())
		if err != nil {
			return index, []error{fmt.Errorf("load active resource ownership: %w", err)}
		}
	}
	for _, result := range activeResults {
		if result.Err != nil {
			issues = append(issues, fmt.Errorf(
				"load competing active project metadata %s: %w", result.Path, result.Err,
			))
			continue
		}
		identity, err := canonicalActiveProjectIdentity(result)
		if err != nil {
			issues = append(issues, err)
			continue
		}
		index.add(result.Manifest.Slug, result.Path, identity)
	}

	archivedResults, err := project.LoadAllResults(project.ArchivedDir())
	if err != nil {
		issues = append(issues, fmt.Errorf("load archived resource ownership: %w", err))
		return index, issues
	}
	for _, result := range archivedResults {
		if result.Err != nil {
			issues = append(issues, fmt.Errorf(
				"load competing archived project metadata %s: %w", result.Path, result.Err,
			))
			continue
		}
		identity, relevant, err := canonicalArchivedProjectIdentity(result)
		if err != nil {
			issues = append(issues, err)
			continue
		}
		if relevant {
			index.add(result.Manifest.Slug, result.Path, identity)
		}
	}
	return index, issues
}

func canonicalActiveProjectIdentity(
	result project.ManifestLoadResult,
) (projectResourceIdentity, error) {
	m := result.Manifest
	if err := project.ValidateSlug(m.Slug); err != nil {
		return projectResourceIdentity{}, fmt.Errorf(
			"canonicalize active project ownership %s: %w", result.Path, err,
		)
	}
	if m.Slug != result.Name {
		return projectResourceIdentity{}, fmt.Errorf(
			"canonicalize active project ownership %s: slug %q does not match directory %q",
			result.Path, m.Slug, result.Name,
		)
	}
	if (m.Program == "") != (m.ProgramItem == "") {
		return projectResourceIdentity{}, fmt.Errorf(
			"canonicalize active project ownership %s: program ownership requires both "+
				"program and program_item; preserving project resources",
			result.Path,
		)
	}
	return canonicalProjectResourceIdentity(
		m.Repo, m.Branch, worktreeValue(m), m.Worktree != nil, result.Path,
	)
}

func canonicalArchivedProjectIdentity(
	result project.ManifestLoadResult,
) (projectResourceIdentity, bool, error) {
	m := result.Manifest
	if m.ArchiveCleanup == nil {
		return projectResourceIdentity{}, false, nil
	}
	proof, err := validateArchivedCleanupProof(m)
	if err != nil {
		return projectResourceIdentity{}, false, fmt.Errorf(
			"canonicalize archived project ownership %s: %w", result.Path, err,
		)
	}
	branchPending := proof.BranchState == project.ArchiveCleanupPending ||
		proof.BranchState == project.ArchiveCleanupClaimed
	worktreePending := proof.WorktreeState == project.ArchiveCleanupPending ||
		proof.WorktreeState == project.ArchiveCleanupClaimed
	if !branchPending && !worktreePending {
		return projectResourceIdentity{}, false, nil
	}
	identity, err := canonicalProjectResourceIdentity(
		proof.Repository, proof.Branch, proof.Worktree, worktreePending, result.Path,
	)
	if err != nil {
		return projectResourceIdentity{}, false, err
	}
	if !branchPending {
		identity.branch = ""
	}
	return identity, true, nil
}

func canonicalProjectResourceIdentity(
	repo, branch, worktree string,
	requireWorktree bool,
	manifestPath string,
) (projectResourceIdentity, error) {
	if strings.TrimSpace(repo) == "" {
		return projectResourceIdentity{}, fmt.Errorf(
			"canonicalize project ownership %s: repository is empty", manifestPath,
		)
	}
	canonicalRepo, err := gitx.CanonicalRepositoryRoot(repo)
	if err != nil {
		return projectResourceIdentity{}, fmt.Errorf(
			"canonicalize project ownership %s repository %s: %w", manifestPath, repo, err,
		)
	}
	branchRef, err := gitx.CanonicalBranchRef(canonicalRepo, branch)
	if err != nil {
		return projectResourceIdentity{}, fmt.Errorf(
			"canonicalize project ownership %s branch %q: %w", manifestPath, branch, err,
		)
	}
	identity := projectResourceIdentity{
		repository: canonicalRepo,
		branch:     canonicalRepo + "\x00" + branchRef,
	}
	if strings.TrimSpace(worktree) == "" {
		if requireWorktree {
			return projectResourceIdentity{}, fmt.Errorf(
				"canonicalize project ownership %s: worktree is present but empty; "+
					"preserving project resources",
				manifestPath,
			)
		}
		return identity, nil
	}
	identity.worktree, err = gitx.CanonicalPath(worktree)
	if err != nil {
		return projectResourceIdentity{}, fmt.Errorf(
			"canonicalize project ownership %s worktree %s: %w", manifestPath, worktree, err,
		)
	}
	return identity, nil
}

func (index *projectResourceOwnershipIndex) add(
	slug, path string, identity projectResourceIdentity,
) {
	owner := projectResourceOwner{slug: slug, path: path}
	index.identitiesByPath[path] = identity
	if identity.branch != "" {
		index.branchOwners[identity.branch] = append(index.branchOwners[identity.branch], owner)
	}
	if identity.worktree != "" {
		index.worktreeOwners[identity.worktree] = append(index.worktreeOwners[identity.worktree], owner)
	}
}

func (index projectResourceOwnershipIndex) requireExclusive(path string) error {
	identity, ok := index.identitiesByPath[path]
	if !ok {
		return fmt.Errorf("project ownership index has no identity for %s", path)
	}
	var issues []error
	if owners := index.branchOwners[identity.branch]; len(owners) != 1 || owners[0].path != path {
		issues = append(issues, duplicateResourceOwnershipError("branch", owners, path))
	}
	if identity.worktree != "" {
		if owners := index.worktreeOwners[identity.worktree]; len(owners) != 1 || owners[0].path != path {
			issues = append(issues, duplicateResourceOwnershipError("worktree", owners, path))
		}
	}
	return errors.Join(issues...)
}

func duplicateResourceOwnershipError(
	resource string, owners []projectResourceOwner, targetPath string,
) error {
	var competing []string
	for _, owner := range owners {
		if owner.path == targetPath {
			continue
		}
		competing = append(competing, fmt.Sprintf("project %q in %s", owner.slug, owner.path))
	}
	if len(competing) == 0 {
		return fmt.Errorf(
			"%s ownership for %s is missing from the ownership index; preserving resources",
			resource, targetPath,
		)
	}
	return fmt.Errorf(
		"%s owned by %s is also claimed by %s; preserving all resources until duplicate "+
			"project metadata is repaired",
		resource, targetPath, strings.Join(competing, ", "),
	)
}

func requireExclusiveProjectResources(
	manifestPath string, activeResults []project.ManifestLoadResult,
) error {
	index, issues := loadProjectResourceOwnershipIndex(activeResults)
	if len(issues) > 0 {
		return fmt.Errorf(
			"cannot prove exclusive project resource ownership: %w",
			errors.Join(issues...),
		)
	}
	if err := index.requireExclusive(manifestPath); err != nil {
		return fmt.Errorf("cannot prove exclusive project resource ownership: %w", err)
	}
	return nil
}

func worktreeValue(m project.Manifest) string {
	if m.Worktree == nil {
		return ""
	}
	return strings.TrimSpace(*m.Worktree)
}

func activeManifestPath(m project.Manifest) string {
	return filepath.Join(project.ActiveDir(), m.Slug, "manifest.json")
}
