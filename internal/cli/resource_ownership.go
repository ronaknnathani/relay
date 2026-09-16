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
	slug       string
	repository string
	commonDir  string
	branch     string
	worktree   string
}

type projectResourceOwner struct {
	slug string
	path string
}

type projectResourceClaim struct {
	identity        projectResourceIdentity
	branchUnknown   bool
	worktreeUnknown bool
}

type projectResourceOwnershipIssue struct {
	owner  projectResourceOwner
	claims []projectResourceClaim
	global bool
	err    error
}

type projectResourceOwnershipIndex struct {
	identitiesByPath map[string]projectResourceIdentity
	issuesByPath     map[string]projectResourceOwnershipIssue
	issues           []projectResourceOwnershipIssue
	branchOwners     map[string][]projectResourceOwner
	worktreeOwners   map[string][]projectResourceOwner
}

func loadProjectResourceOwnershipIndex(
	activeResults []project.ManifestLoadResult,
) projectResourceOwnershipIndex {
	index := projectResourceOwnershipIndex{
		identitiesByPath: make(map[string]projectResourceIdentity),
		issuesByPath:     make(map[string]projectResourceOwnershipIssue),
		branchOwners:     make(map[string][]projectResourceOwner),
		worktreeOwners:   make(map[string][]projectResourceOwner),
	}
	if activeResults == nil {
		var err error
		activeResults, err = project.LoadAllResults(project.ActiveDir())
		if err != nil {
			index.addIssue(projectResourceOwnershipIssue{
				owner:  projectResourceOwner{path: project.ActiveDir()},
				global: true,
				err:    fmt.Errorf("load active resource ownership: %w", err),
			})
			return index
		}
	}
	for _, result := range activeResults {
		if result.Err != nil {
			index.addIssue(newProjectResourceOwnershipIssue(
				result, true, false, false, fmt.Errorf(
					"load competing active project metadata %s: %w", result.Path, result.Err,
				),
			))
			continue
		}
		identity, err := canonicalActiveProjectIdentity(result)
		if err != nil {
			index.addIssue(newProjectResourceOwnershipIssue(result, false, false, false, err))
			continue
		}
		index.add(result.Manifest.Slug, result.Path, identity)
	}

	archivedResults, err := project.LoadAllResults(project.ArchivedDir())
	if err != nil {
		index.addIssue(projectResourceOwnershipIssue{
			owner:  projectResourceOwner{path: project.ArchivedDir()},
			global: true,
			err:    fmt.Errorf("load archived resource ownership: %w", err),
		})
		return index
	}
	for _, result := range archivedResults {
		if result.Err != nil {
			index.addIssue(newProjectResourceOwnershipIssue(
				result, true, true, false, fmt.Errorf(
					"load competing archived project metadata %s: %w", result.Path, result.Err,
				),
			))
			continue
		}
		identity, relevant, err := canonicalArchivedProjectIdentity(result)
		if err != nil {
			index.addIssue(newProjectResourceOwnershipIssue(
				result, false, true, result.Manifest.ArchiveCleanup == nil, err,
			))
			continue
		}
		if relevant {
			index.add(result.Manifest.Slug, result.Path, identity)
		}
	}
	return index
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
	if strings.TrimSpace(m.Branch) == "" {
		return projectResourceIdentity{}, fmt.Errorf(
			"canonicalize active project ownership %s: branch is empty", result.Path,
		)
	}
	return canonicalProjectResourceIdentity(
		m.Slug, m.Repo, m.Branch, worktreeValue(m), m.Worktree != nil, result.Path,
	)
}

func canonicalArchivedProjectIdentity(
	result project.ManifestLoadResult,
) (projectResourceIdentity, bool, error) {
	m := result.Manifest
	if m.ArchiveCleanup == nil {
		return canonicalLegacyArchivedProjectIdentity(result)
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
	worktree := ""
	if worktreePending {
		worktree = proof.Worktree
	}
	identity, err := canonicalProjectResourceIdentity(
		m.Slug, proof.Repository, proof.Branch, worktree, worktreePending, result.Path,
	)
	if err != nil {
		return projectResourceIdentity{}, false, err
	}
	if !branchPending {
		identity.branch = ""
	}
	return identity, true, nil
}

func canonicalLegacyArchivedProjectIdentity(
	result project.ManifestLoadResult,
) (projectResourceIdentity, bool, error) {
	m := result.Manifest
	if err := project.ValidateSlug(m.Slug); err != nil {
		return projectResourceIdentity{}, false, fmt.Errorf(
			"canonicalize legacy archived project ownership %s: %w", result.Path, err,
		)
	}
	if m.Slug != result.Name {
		return projectResourceIdentity{}, false, fmt.Errorf(
			"canonicalize legacy archived project ownership %s: slug %q does not match directory %q",
			result.Path, m.Slug, result.Name,
		)
	}
	var issues []error
	branchPresent := false
	if strings.TrimSpace(m.Branch) != "" {
		var err error
		branchPresent, err = archiveBranchExists(m.Repo, m.Branch)
		if err != nil {
			issues = append(issues, fmt.Errorf(
				"inspect legacy archived branch %q for %s: %w", m.Branch, result.Path, err,
			))
		}
	}
	worktreePresent := false
	worktree := worktreeValue(m)
	if worktree != "" {
		_, found, err := gitx.RegisteredWorktreeState(m.Repo, worktree)
		worktreePresent = found
		if err != nil {
			issues = append(issues, fmt.Errorf(
				"inspect legacy archived worktree %s for %s: %w", worktree, result.Path, err,
			))
		}
	}
	if len(issues) > 0 {
		return projectResourceIdentity{}, false, errors.Join(issues...)
	}
	if !branchPresent && !worktreePresent {
		return projectResourceIdentity{}, false, nil
	}
	identity, err := canonicalProjectResourceIdentity(
		m.Slug, m.Repo, m.Branch, worktree, worktreePresent, result.Path,
	)
	if err != nil {
		return projectResourceIdentity{}, false, err
	}
	if !branchPresent {
		identity.branch = ""
	}
	if !worktreePresent {
		identity.worktree = ""
	}
	return identity, true, nil
}

func canonicalProjectResourceIdentity(
	slug, repo, branch, worktree string,
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
	commonDir, err := gitx.CanonicalGitCommonDir(canonicalRepo)
	if err != nil {
		return projectResourceIdentity{}, fmt.Errorf(
			"canonicalize project ownership %s git common directory: %w", manifestPath, err,
		)
	}
	identity := projectResourceIdentity{
		slug:       slug,
		repository: canonicalRepo,
		commonDir:  commonDir,
	}
	if strings.TrimSpace(branch) != "" {
		branchRef, err := gitx.CanonicalBranchRef(canonicalRepo, branch)
		if err != nil {
			return projectResourceIdentity{}, fmt.Errorf(
				"canonicalize project ownership %s branch %q: %w", manifestPath, branch, err,
			)
		}
		identity.branch = commonDir + "\x00" + branchRef
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

func newProjectResourceOwnershipIssue(
	result project.ManifestLoadResult,
	incomplete bool,
	includeCleanupProof bool,
	allowFilesystemIdentity bool,
	err error,
) projectResourceOwnershipIssue {
	m := result.Manifest
	slug := trustedProjectResourceSlug(result)
	issue := projectResourceOwnershipIssue{
		owner: projectResourceOwner{
			slug: firstNonEmpty(m.Slug, result.Name),
			path: result.Path,
		},
		err: err,
	}
	candidates := []projectResourceCandidate{{
		slug:        slug,
		repository:  m.Repo,
		branch:      m.Branch,
		worktree:    worktreeValue(m),
		hasBranch:   strings.TrimSpace(m.Branch) != "",
		hasWorktree: m.Worktree != nil,
		incomplete:  incomplete,
	}}
	if includeCleanupProof && m.ArchiveCleanup != nil {
		proof := m.ArchiveCleanup
		candidates = append(candidates, projectResourceCandidate{
			slug:       slug,
			repository: proof.Repository,
			branch:     proof.Branch,
			worktree:   proof.Worktree,
			hasBranch: proof.BranchPresent ||
				proof.BranchState == project.ArchiveCleanupPending ||
				proof.BranchState == project.ArchiveCleanupClaimed ||
				proof.ExpectedBranchTip != "" ||
				strings.TrimSpace(proof.Branch) != "",
			hasWorktree: proof.WorktreePresent ||
				proof.WorktreeState == project.ArchiveCleanupPending ||
				proof.WorktreeState == project.ArchiveCleanupClaimed ||
				proof.ExpectedWorktreeTip != "" ||
				strings.TrimSpace(proof.Worktree) != "",
			expectedWorktreeBranch: proof.ExpectedWorktreeBranch,
			incomplete:             true,
		})
	}
	for _, candidate := range candidates {
		claims, verified := plausibleProjectResourceClaims(candidate, allowFilesystemIdentity)
		if !verified {
			issue.global = true
			continue
		}
		issue.claims = append(issue.claims, claims...)
	}
	if len(issue.claims) == 0 {
		issue.global = true
	}
	if includeCleanupProof && m.ArchiveCleanup != nil {
		issue.err = fmt.Errorf(
			"%w (manifest repository/branch/worktree %q/%q/%q; cleanup proof %q/%q/%q)",
			err,
			m.Repo, m.Branch, worktreeValue(m),
			m.ArchiveCleanup.Repository, m.ArchiveCleanup.Branch, m.ArchiveCleanup.Worktree,
		)
	}
	return issue
}

type projectResourceCandidate struct {
	slug                   string
	repository             string
	branch                 string
	worktree               string
	expectedWorktreeBranch string
	hasBranch              bool
	hasWorktree            bool
	incomplete             bool
}

func plausibleProjectResourceClaims(
	candidate projectResourceCandidate,
	allowFilesystemIdentity bool,
) ([]projectResourceClaim, bool) {
	repository, commonDir, repositoryVerified := plausibleRepositoryIdentity(
		candidate.repository, allowFilesystemIdentity,
	)
	if !repositoryVerified && !allowFilesystemIdentity {
		return nil, false
	}
	base := projectResourceIdentity{
		slug:       candidate.slug,
		repository: repository,
		commonDir:  commonDir,
	}
	claim := projectResourceClaim{identity: base}
	if strings.TrimSpace(candidate.branch) != "" {
		if commonDir == "" {
			claim.branchUnknown = true
		} else {
			branchRef, branchErr := gitx.CanonicalBranchRef(repository, candidate.branch)
			if branchErr == nil {
				claim.identity.branch = commonDir + "\x00" + branchRef
			} else {
				claim.branchUnknown = true
			}
		}
	} else {
		claim.branchUnknown = candidate.hasBranch || candidate.incomplete
	}
	if strings.TrimSpace(candidate.worktree) != "" {
		worktree, worktreeErr := gitx.CanonicalPath(candidate.worktree)
		if worktreeErr == nil {
			claim.identity.worktree = worktree
		} else {
			claim.worktreeUnknown = true
		}
	} else {
		claim.worktreeUnknown = candidate.hasWorktree || candidate.incomplete
	}
	if !repositoryVerified && claim.identity.slug == "" && claim.identity.worktree == "" {
		return nil, false
	}
	claims := []projectResourceClaim{claim}
	expectedBranch := strings.TrimSpace(candidate.expectedWorktreeBranch)
	if expectedBranch == "" {
		return claims, true
	}
	expectedBranch = strings.TrimPrefix(expectedBranch, "refs/heads/")
	if expectedBranch == candidate.branch {
		return claims, true
	}
	expectedClaim := projectResourceClaim{identity: base}
	if commonDir == "" {
		expectedClaim.branchUnknown = true
	} else {
		branchRef, branchErr := gitx.CanonicalBranchRef(repository, expectedBranch)
		if branchErr != nil {
			expectedClaim.branchUnknown = true
		} else {
			expectedClaim.identity.branch = commonDir + "\x00" + branchRef
		}
	}
	return append(claims, expectedClaim), true
}

func plausibleRepositoryIdentity(
	recorded string,
	allowFilesystemIdentity bool,
) (repository, commonDir string, verified bool) {
	if strings.TrimSpace(recorded) == "" {
		return "", "", false
	}
	repository, err := gitx.CanonicalRepositoryRoot(recorded)
	if err != nil {
		repository, err = gitx.RepositoryRoot(recorded)
		if err == nil {
			repository, err = gitx.CanonicalPath(repository)
		}
	}
	if err == nil {
		commonDir, err = gitx.CanonicalGitCommonDir(repository)
		if err == nil {
			return repository, commonDir, true
		}
	}
	if !allowFilesystemIdentity {
		return "", "", false
	}
	repository, err = gitx.CanonicalPath(recorded)
	if err != nil {
		return "", "", false
	}
	return repository, "", true
}

func trustedProjectResourceSlug(result project.ManifestLoadResult) string {
	if result.Manifest.Slug != result.Name {
		return ""
	}
	if err := project.ValidateSlug(result.Manifest.Slug); err != nil {
		return ""
	}
	return result.Manifest.Slug
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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

func (index *projectResourceOwnershipIndex) addIssue(issue projectResourceOwnershipIssue) {
	index.issuesByPath[issue.owner.path] = issue
	index.issues = append(index.issues, issue)
}

func (index projectResourceOwnershipIndex) requireExclusive(path string) error {
	if issue, ok := index.issuesByPath[path]; ok {
		return issue.err
	}
	identity, ok := index.identitiesByPath[path]
	if !ok {
		return fmt.Errorf("project ownership index has no identity for %s", path)
	}
	var issues []error
	if identity.branch != "" {
		if owners := index.branchOwners[identity.branch]; len(owners) != 1 || owners[0].path != path {
			issues = append(issues, duplicateResourceOwnershipError("branch", owners, path))
		}
	}
	if identity.worktree != "" {
		if owners := index.worktreeOwners[identity.worktree]; len(owners) != 1 || owners[0].path != path {
			issues = append(issues, duplicateResourceOwnershipError("worktree", owners, path))
		}
	}
	for _, issue := range index.issues {
		if issue.owner.path == path || !issue.couldConflict(identity) {
			continue
		}
		issues = append(issues, fmt.Errorf(
			"project ownership issue in %s could conflict with %s: %w",
			issue.owner.path, path, issue.err,
		))
	}
	return errors.Join(issues...)
}

func (issue projectResourceOwnershipIssue) couldConflict(
	identity projectResourceIdentity,
) bool {
	if issue.global {
		return true
	}
	for _, claim := range issue.claims {
		if claim.identity.slug != "" && claim.identity.slug == identity.slug {
			return true
		}
		if (claim.identity.branch != "" && claim.identity.branch == identity.branch) ||
			(claim.identity.worktree != "" && claim.identity.worktree == identity.worktree) {
			return true
		}
		sameRepository := claim.identity.repository == identity.repository
		sameCommonDir := claim.identity.commonDir != "" &&
			claim.identity.commonDir == identity.commonDir
		if (claim.branchUnknown || claim.worktreeUnknown) &&
			(sameRepository || sameCommonDir) {
			return true
		}
	}
	return false
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
	index := loadProjectResourceOwnershipIndex(activeResults)
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
