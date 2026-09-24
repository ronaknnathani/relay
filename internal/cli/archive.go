package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/patrollock"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

const projectLifecycleLockTimeout = 30 * time.Second

var (
	loadArchivePullRequestProof = programview.GitHubPullRequestProof
	loadArchiveRepository       = programview.GitHubRepository
	saveArchiveManifest         = project.Save
	archiveBranchExists         = gitx.LocalBranchExists
	archiveForceDeleteBranchAt  = gitx.ForceDeleteBranchAt
	archiveRemoveBranchConfig   = gitx.RemoveBranchConfig
	archiveWorktreeRemove       = gitx.WorktreeRemoveAt
	archiveRename               = os.Rename
	archiveRemoveFile           = os.Remove
)

type recordedPullRequestEvidence struct {
	Merged  bool
	HeadSHA string
}

func resolveRecordedPullRequestEvidence(
	m project.Manifest, slug string,
) (recordedPullRequestEvidence, error) {
	hasPR, ref, err := programview.RecordedPR(m, project.StatePath(slug))
	if err != nil {
		return recordedPullRequestEvidence{}, fmt.Errorf(
			"resolve recorded pull request for %s: %w", slug, err,
		)
	}
	if !hasPR {
		return recordedPullRequestEvidence{}, nil
	}
	safeRef := gitx.SanitizeDiagnostic(ref)
	proof, err := loadArchivePullRequestProof(m.Repo, ref)
	if err != nil {
		return recordedPullRequestEvidence{}, fmt.Errorf(
			"lookup recorded pull request %s for %s: %w", safeRef, slug, err,
		)
	}
	if proof.State != programview.PRStateMerged {
		return recordedPullRequestEvidence{}, nil
	}
	repository, err := loadArchiveRepository(m.Repo)
	if err != nil {
		return recordedPullRequestEvidence{}, fmt.Errorf(
			"resolve repository for recorded pull request %s for %s: %w", safeRef, slug, err,
		)
	}
	if !strings.EqualFold(proof.Repository, repository) {
		return recordedPullRequestEvidence{}, fmt.Errorf(
			"recorded pull request %s for %s belongs to repository %q, want %q",
			safeRef, slug, proof.Repository, repository,
		)
	}
	base := m.BaseBranch
	if base == "" {
		base, err = gitx.DetectDefaultBranchWithError(m.Repo)
		if err != nil {
			return recordedPullRequestEvidence{}, fmt.Errorf(
				"resolve base branch for recorded pull request %s for %s: %w",
				safeRef, slug, err,
			)
		}
	}
	if proof.BaseBranch != base {
		return recordedPullRequestEvidence{}, fmt.Errorf(
			"recorded pull request %s for %s base branch %q does not match manifest base branch %q",
			safeRef, slug, proof.BaseBranch, base,
		)
	}
	if proof.HeadBranch != m.Branch {
		return recordedPullRequestEvidence{}, fmt.Errorf(
			"recorded pull request %s for %s head branch %q does not match manifest branch %q",
			safeRef, slug, proof.HeadBranch, m.Branch,
		)
	}
	branchTip, found, err := gitx.LocalBranchTip(m.Repo, m.Branch)
	if err != nil {
		return recordedPullRequestEvidence{}, fmt.Errorf(
			"resolve branch %q tip for recorded pull request %s for %s: %w",
			m.Branch, safeRef, slug, err,
		)
	}
	worktreeState := gitx.WorktreeState{}
	worktreeFound := false
	if m.Worktree != nil && *m.Worktree != "" {
		worktreeState, worktreeFound, err = gitx.RegisteredWorktreeState(m.Repo, *m.Worktree)
		if err != nil {
			return recordedPullRequestEvidence{}, fmt.Errorf(
				"resolve worktree HEAD for recorded pull request %s for %s: %w",
				safeRef, slug, err,
			)
		}
		if worktreeFound && !worktreeState.Detached &&
			worktreeState.Branch != "refs/heads/"+m.Branch {
			return recordedPullRequestEvidence{}, fmt.Errorf(
				"recorded pull request %s for %s worktree %s is attached to %q, want %q",
				safeRef, slug, *m.Worktree, worktreeState.Branch, "refs/heads/"+m.Branch,
			)
		}
	}
	if (found || worktreeFound) && proof.HeadSHA == "" {
		return recordedPullRequestEvidence{}, fmt.Errorf(
			"recorded pull request %s for %s has no head SHA", safeRef, slug,
		)
	}
	if found && branchTip != proof.HeadSHA {
		return recordedPullRequestEvidence{}, fmt.Errorf(
			"recorded pull request %s for %s head %s does not match branch %q tip %s",
			safeRef, slug, proof.HeadSHA, m.Branch, branchTip,
		)
	}
	if worktreeFound && worktreeState.Head != proof.HeadSHA {
		return recordedPullRequestEvidence{}, fmt.Errorf(
			"recorded pull request %s for %s head %s does not match worktree HEAD %s",
			safeRef, slug, proof.HeadSHA, worktreeState.Head,
		)
	}
	return recordedPullRequestEvidence{Merged: true, HeadSHA: proof.HeadSHA}, nil
}

func resolveRecordedPullRequestMerge(m project.Manifest, slug string) (bool, error) {
	evidence, err := resolveRecordedPullRequestEvidence(m, slug)
	return evidence.Merged, err
}

// recordedPullRequestMerged reports whether the project's recorded pull request
// is merged on GitHub. An unavailable or failing GitHub stays conservative.
func recordedPullRequestMerged(m project.Manifest, slug string) bool {
	merged, _ := resolveRecordedPullRequestMerge(m, slug)
	return merged
}

// recordedPullRequestMergeOnce memoizes the detailed lookup so archive asks
// GitHub at most once per run, and only when the answer is still needed.
func recordedPullRequestMergeOnce(
	m project.Manifest, slug string,
) func() (recordedPullRequestEvidence, error) {
	var (
		resolved bool
		evidence recordedPullRequestEvidence
		err      error
	)
	return func() (recordedPullRequestEvidence, error) {
		if !resolved {
			resolved = true
			evidence, err = resolveRecordedPullRequestEvidence(m, slug)
		}
		return evidence, err
	}
}

func newCmdArchive() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "archive <slug>",
		Short: "Archive project and remove worktree",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runArchive(args[0], force)
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "force-remove worktree with dirty files")
	return cmd
}

// archiveResult is what archiving one project did. Returning it instead of
// printing lets a caller that owns its own output — a JSON command, for
// instance — report the same facts without archive writing into its stream.
type archiveResult struct {
	Slug                  string   `json:"slug"`
	Worktree              string   `json:"worktree,omitempty"`
	WorktreeRemoved       bool     `json:"worktree_removed"`
	Branch                string   `json:"branch,omitempty"`
	BranchDeleted         bool     `json:"branch_deleted"`
	BranchDeletionWarning string   `json:"branch_deletion_warning,omitempty"`
	BranchCleanupCommand  string   `json:"branch_cleanup_command,omitempty"`
	Merged                bool     `json:"merged"`
	MetadataTransition    string   `json:"metadata_transition"`
	ProjectLocation       string   `json:"project_location"`
	ProjectPath           string   `json:"project_path"`
	ArchivedPath          string   `json:"archived_path"`
	Warnings              []string `json:"warnings"`
}

const (
	archiveTransitionNone               = "none"
	archiveTransitionStaged             = "staged"
	archiveTransitionRolledBack         = "rolled-back"
	archiveTransitionRollbackIncomplete = "rollback-incomplete"
	archiveLocationActive               = "active"
	archiveLocationArchived             = "archived"
	archiveLocationUnknown              = "unknown"
)

// runArchive archives a project and prints the human-facing report.
func runArchive(slug string, force bool) error {
	result, err := archiveProject(slug, force)
	if err == nil {
		renderArchive(os.Stdout, result)
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	activeDir := filepath.Join(project.ActiveDir(), slug)
	if _, activeErr := os.Lstat(activeDir); activeErr == nil {
		return err
	} else if !errors.Is(activeErr, os.ErrNotExist) {
		return fmt.Errorf("inspect active project %s: %w", activeDir, activeErr)
	}
	result, archivedErr := retryStandaloneArchivedProjectCleanup(slug, force)
	if archivedErr != nil {
		if errors.Is(archivedErr, os.ErrNotExist) {
			return err
		}
		return archivedErr
	}
	renderArchivedCleanupRetry(os.Stdout, result)
	return nil
}

// renderArchive prints archive's long-standing text output, including the
// branch-deletion warning it has always written to stderr.
func renderArchive(out io.Writer, result archiveResult) {
	renderArchiveWarnings(result)
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Green, "Archived:"), result.Slug)
	if result.WorktreeRemoved {
		fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Dim, "Worktree removed:"), result.Worktree)
	}
	if result.BranchDeletionWarning != "" && !result.BranchDeleted {
		fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Yellow, "Branch still present:"), result.Branch)
	} else if result.BranchDeletionWarning != "" {
		fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Yellow, "Branch config cleanup incomplete:"), result.Branch)
	}
	fmt.Fprintln(out)
}

func renderArchivedCleanupRetry(out io.Writer, result archiveResult) {
	renderArchivedCleanupResult(out, result, result.BranchDeletionWarning != "")
}

func renderArchivedCleanupFailure(out io.Writer, result archiveResult) {
	renderArchivedCleanupResult(out, result, true)
}

func renderArchivedCleanupResult(out io.Writer, result archiveResult, incomplete bool) {
	renderArchiveWarnings(result)
	fmt.Fprintln(out)
	if incomplete {
		fmt.Fprintf(
			out, "  %s %s\n",
			ui.Color(ui.Yellow, "Archived cleanup incomplete:"), result.Slug,
		)
	} else {
		fmt.Fprintf(
			out, "  %s %s\n",
			ui.Color(ui.Green, "Archived cleanup complete:"), result.Slug,
		)
	}
	if result.WorktreeRemoved {
		fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Dim, "Worktree removed:"), result.Worktree)
	}
	switch {
	case result.BranchDeletionWarning != "" && !result.BranchDeleted:
		fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Yellow, "Branch still present:"), result.Branch)
	case result.BranchDeletionWarning != "":
		fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Yellow, "Branch config cleanup incomplete:"), result.Branch)
	case result.BranchDeleted:
		fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Dim, "Branch removed:"), result.Branch)
	}
	fmt.Fprintln(out)
}

func renderArchiveWarnings(result archiveResult) {
	for _, warning := range result.Warnings {
		ui.Warn("%s", warning)
	}
	if result.BranchDeletionWarning != "" {
		ui.Warn("%s", result.BranchDeletionWarning)
	}
}

type archiveProofKind string

const (
	archiveProofForced        archiveProofKind = "forced"
	archiveProofReachable     archiveProofKind = "reachable"
	archiveProofPullRequest   archiveProofKind = "pull-request"
	archiveProofMissingBranch archiveProofKind = "missing-branch"
	archiveProofFreshUpstream archiveProofKind = "fresh-upstream"
)

type archiveProofSnapshot struct {
	Slug                   string
	Repository             string
	Branch                 string
	Worktree               string
	HasWorktree            bool
	ManifestJSON           string
	Kind                   archiveProofKind
	BranchPresent          bool
	ExpectedBranchTip      string
	WorktreePresent        bool
	ExpectedWorktreeTip    string
	ExpectedWorktreeBranch string
	WorktreeDetached       bool
	AuthoritativeCommit    string
	ForceAuthorized        bool
	Merged                 bool
}

type archiveDecision struct {
	proof    archiveProofSnapshot
	warnings []string
}

// archiveProject computes its own proof snapshot at the archive boundary.
func archiveProject(slug string, force bool) (archiveResult, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return archiveResult{}, err
	}
	manifestPath := project.ManifestPath(project.ActiveDir(), slug)
	m, err := project.Load(manifestPath)
	if err != nil {
		return archiveResult{}, fmt.Errorf("project not found in active: %s: %w", slug, err)
	}
	if m.Slug != slug {
		return archiveResult{}, fmt.Errorf(
			"project manifest slug %q does not match requested slug %q", m.Slug, slug,
		)
	}
	decision, err := decideArchive(m, slug, force)
	if err != nil {
		return archiveResult{}, err
	}
	result, err := archiveProjectWithProof(decision.proof, force)
	result.Warnings = append(result.Warnings, decision.warnings...)
	if err != nil {
		return result, err
	}
	return result, nil
}

func decideArchive(m project.Manifest, slug string, force bool) (archiveDecision, error) {
	if m.Program != "" || m.ProgramItem != "" {
		if m.Program != "" && m.ProgramItem != "" {
			return archiveDecision{}, fmt.Errorf(
				"active project %q is managed by program %s/%s; use: relay program worker cleanup %s %s",
				slug, m.Program, m.ProgramItem, m.Program, m.ProgramItem,
			)
		}
		return archiveDecision{}, fmt.Errorf(
			"active project %q has incomplete program ownership metadata; inspect %s and use the program worker cleanup lifecycle",
			slug, project.ManifestPath(project.ActiveDir(), slug),
		)
	}
	proof, err := newArchiveProofSnapshot(m, archiveProofForced, false)
	if err != nil {
		return archiveDecision{}, err
	}
	proof.ForceAuthorized = force
	recordedMerge := recordedPullRequestMergeOnce(m, slug)
	var warnings []string

	base := m.BaseBranch
	var evaluationErr error
	if base == "" {
		base, evaluationErr = gitx.DetectDefaultBranchWithError(m.Repo)
	}
	if evaluationErr == nil {
		baseRef := "refs/remotes/origin/" + base
		if gitx.RevParse(m.Repo, baseRef) == "" {
			evaluationErr = fmt.Errorf("configured upstream base %s is unavailable", baseRef)
		} else {
			var (
				tip    string
				merged bool
			)
			tip, merged, evaluationErr = gitx.WorkMergedInto(
				m.Repo, m.Branch, baseRef, m.StartSHA,
			)
			if evaluationErr == nil && merged {
				proof, evaluationErr = newMergedBranchArchiveProof(
					m, archiveProofReachable, tip,
				)
				if evaluationErr == nil {
					proof.ForceAuthorized = force
					return archiveDecision{proof: proof}, nil
				}
			}
			if evaluationErr == nil && proof.BranchPresent &&
				tip == gitx.RevParse(m.Repo, strings.TrimSpace(m.StartSHA)+"^{commit}") {
				proof.AuthoritativeCommit = tip
				proof.Kind = archiveProofReachable
				if err := validateArchiveWorktreeBinding(proof); err != nil {
					return archiveDecision{}, err
				}
				return archiveDecision{proof: proof}, nil
			}
		}
	}

	evidence, prErr := recordedMerge()
	if prErr == nil && evidence.Merged {
		candidate := proof
		candidate.AuthoritativeCommit = evidence.HeadSHA
		prErr = validatePullRequestProofTips(candidate, evidence.HeadSHA)
		if prErr == nil {
			candidate.Kind = archiveProofPullRequest
			candidate.Merged = true
			candidate.ForceAuthorized = force
			return archiveDecision{proof: candidate}, nil
		}
	}
	if !force {
		detail := ""
		if evaluationErr != nil {
			detail = fmt.Sprintf("; Git verification detail: %s", evaluationErr)
		}
		if prErr != nil {
			detail += fmt.Sprintf("; recorded pull request lookup detail: %s", prErr)
		}
		return archiveDecision{}, fmt.Errorf(
			"branch %q merge could not be verified; merge it first or re-run with --force to delete it anyway%s",
			m.Branch, detail,
		)
	}
	if evaluationErr != nil {
		warnings = append(warnings, evaluationErr.Error())
	}
	if prErr != nil {
		warnings = append(warnings, prErr.Error())
	}
	if err := validateArchiveWorktreeBinding(proof); err != nil {
		return archiveDecision{}, err
	}
	if proof.BranchPresent {
		proof.AuthoritativeCommit = proof.ExpectedBranchTip
		proof.Kind = archiveProofForced
	} else {
		proof.Kind = archiveProofMissingBranch
	}
	return archiveDecision{proof: proof, warnings: warnings}, nil
}

func newArchiveProofSnapshot(
	m project.Manifest, kind archiveProofKind, merged bool,
) (archiveProofSnapshot, error) {
	if err := validateManifestWorktreeMetadata(m); err != nil {
		return archiveProofSnapshot{}, err
	}
	manifest, err := json.Marshal(m)
	if err != nil {
		return archiveProofSnapshot{}, fmt.Errorf("snapshot project %s manifest: %w", m.Slug, err)
	}
	proof := archiveProofSnapshot{
		Slug:         m.Slug,
		Repository:   m.Repo,
		Branch:       m.Branch,
		ManifestJSON: string(manifest),
		Kind:         kind,
		Merged:       merged,
	}
	if m.Worktree != nil {
		proof.HasWorktree = true
		proof.Worktree = *m.Worktree
	}
	if m.Branch != "" {
		proof.BranchPresent, err = archiveBranchExists(m.Repo, m.Branch)
		if err != nil {
			return archiveProofSnapshot{}, fmt.Errorf(
				"inspect branch %q for project %s: %w", m.Branch, m.Slug, err,
			)
		}
		if proof.BranchPresent {
			var found bool
			proof.ExpectedBranchTip, found, err = gitx.LocalBranchTip(m.Repo, m.Branch)
			if err != nil {
				return archiveProofSnapshot{}, fmt.Errorf(
					"resolve branch %q tip for project %s: %w", m.Branch, m.Slug, err,
				)
			}
			if !found {
				return archiveProofSnapshot{}, fmt.Errorf(
					"branch %q for project %s disappeared during inspection", m.Branch, m.Slug,
				)
			}
		}
	}
	if proof.HasWorktree && proof.Worktree != "" {
		var state gitx.WorktreeState
		state, proof.WorktreePresent, err = gitx.RegisteredWorktreeState(
			m.Repo, proof.Worktree,
		)
		if err != nil {
			return archiveProofSnapshot{}, fmt.Errorf(
				"resolve worktree HEAD for project %s: %w", m.Slug, err,
			)
		}
		proof.ExpectedWorktreeTip = state.Head
		proof.ExpectedWorktreeBranch = state.Branch
		proof.WorktreeDetached = state.Detached
	}
	return proof, nil
}

func newMergedBranchArchiveProof(
	m project.Manifest, kind archiveProofKind, expectedTip string,
) (archiveProofSnapshot, error) {
	proof, err := newArchiveProofSnapshot(m, kind, true)
	if err != nil {
		return archiveProofSnapshot{}, err
	}
	if !proof.BranchPresent {
		return archiveProofSnapshot{}, fmt.Errorf(
			"branch %q for project %s disappeared after merge proof", m.Branch, m.Slug,
		)
	}
	if proof.ExpectedBranchTip != expectedTip {
		return archiveProofSnapshot{}, fmt.Errorf(
			"branch tip changed from %s to %s for project %s after merge proof",
			expectedTip, proof.ExpectedBranchTip, m.Slug,
		)
	}
	proof.AuthoritativeCommit = expectedTip
	if proof.WorktreePresent && proof.ExpectedWorktreeTip != expectedTip {
		return archiveProofSnapshot{}, fmt.Errorf(
			"worktree HEAD %s for project %s does not match branch %q tip %s",
			proof.ExpectedWorktreeTip, m.Slug, m.Branch, expectedTip,
		)
	}
	if err := validateArchiveWorktreeBinding(proof); err != nil {
		return archiveProofSnapshot{}, err
	}
	return proof, nil
}

func newPullRequestArchiveProof(
	m project.Manifest, evidence recordedPullRequestEvidence,
) (archiveProofSnapshot, error) {
	proof, err := newArchiveProofSnapshot(m, archiveProofPullRequest, true)
	if err != nil {
		return archiveProofSnapshot{}, err
	}
	proof.AuthoritativeCommit = evidence.HeadSHA
	if err := validatePullRequestProofTips(proof, evidence.HeadSHA); err != nil {
		return archiveProofSnapshot{}, err
	}
	return proof, nil
}

func validatePullRequestProofTips(proof archiveProofSnapshot, expectedTip string) error {
	if proof.BranchPresent && proof.ExpectedBranchTip != expectedTip {
		return fmt.Errorf(
			"recorded pull request head %s does not match branch %q tip %s",
			expectedTip, proof.Branch, proof.ExpectedBranchTip,
		)
	}
	if proof.WorktreePresent && proof.ExpectedWorktreeTip != expectedTip {
		return fmt.Errorf(
			"recorded pull request head %s does not match worktree HEAD %s",
			expectedTip, proof.ExpectedWorktreeTip,
		)
	}
	if err := validateArchiveWorktreeBinding(proof); err != nil {
		return err
	}
	return nil
}

func validateArchiveWorktreeBinding(proof archiveProofSnapshot) error {
	if !proof.WorktreePresent {
		return nil
	}
	if proof.WorktreeDetached {
		if proof.AuthoritativeCommit == "" {
			return fmt.Errorf(
				"worktree %s for project %s is detached without an authoritative project commit; "+
					"preserving it for manual inspection",
				proof.Worktree, proof.Slug,
			)
		}
		if proof.ExpectedWorktreeTip != proof.AuthoritativeCommit {
			return fmt.Errorf(
				"worktree %s for project %s is detached at %s, want authoritative project commit %s",
				proof.Worktree, proof.Slug, proof.ExpectedWorktreeTip, proof.AuthoritativeCommit,
			)
		}
		return nil
	}
	expectedBranch := "refs/heads/" + proof.Branch
	if proof.ExpectedWorktreeBranch != expectedBranch {
		return fmt.Errorf(
			"worktree %s for project %s is attached to %q, want %q",
			proof.Worktree, proof.Slug, proof.ExpectedWorktreeBranch, expectedBranch,
		)
	}
	return nil
}

func archiveProjectWithProof(proof archiveProofSnapshot, force bool) (archiveResult, error) {
	return withArchiveCleanupLock(proof.Slug, func() (archiveResult, error) {
		return archiveProjectWithProofLocked(proof, force)
	})
}

func archiveProjectWithProofLocked(proof archiveProofSnapshot, force bool) (archiveResult, error) {
	force = force || proof.ForceAuthorized
	proof.ForceAuthorized = force
	result := archiveResult{
		Slug:               proof.Slug,
		Branch:             proof.Branch,
		Merged:             proof.Merged,
		MetadataTransition: archiveTransitionNone,
		ProjectLocation:    archiveLocationUnknown,
		Warnings:           []string{},
	}
	if proof.HasWorktree {
		result.Worktree = proof.Worktree
	}
	m, err := validateArchiveProof(proof)
	if err != nil {
		locationErr := applyCurrentProjectLocation(&result, proof.Slug)
		return result, errors.Join(err, locationErr)
	}
	if err := requireExclusiveProjectResources(activeManifestPath(m), nil); err != nil {
		locationErr := applyCurrentProjectLocation(&result, proof.Slug)
		return result, errors.Join(fmt.Errorf(
			"archive project %s: %w", proof.Slug, err,
		), locationErr)
	}
	applyArchiveMetadataState(&result, archiveMetadataState{
		transition: archiveTransitionNone,
		location:   archiveLocationActive,
		path:       filepath.Join(project.ActiveDir(), proof.Slug),
	})
	now := time.Now().UTC().Format(time.RFC3339)
	m.Status = "archived"
	m.Archived = &now
	m.Merged = proof.Merged
	m.ArchiveCleanup = archiveCleanupProof(proof)

	srcDir := filepath.Join(project.ActiveDir(), proof.Slug)
	dstDir := filepath.Join(project.ArchivedDir(), proof.Slug)
	stage, err := stageArchivedProject(srcDir, dstDir, m)
	applyArchiveMetadataState(&result, stage)
	if err != nil {
		return result, err
	}
	m, err = loadArchivedCleanupManifest(proof.Slug)
	if err != nil {
		rollback := stage.rollback()
		applyArchiveMetadataState(&result, rollback)
		if rollback.err != nil {
			return result, fmt.Errorf(
				"reload archived cleanup manifest: %w; rollback archive metadata: %v",
				err, rollback.err,
			)
		}
		return result, fmt.Errorf("reload archived cleanup manifest: %w", err)
	}

	if proof.HasWorktree && proof.Worktree != "" {
		if err := validateArchiveTips(proof); err != nil {
			rollback := stage.rollback()
			applyArchiveMetadataState(&result, rollback)
			if rollback.err != nil {
				err = fmt.Errorf("%w; rollback archive metadata: %v", err, rollback.err)
				return result, err
			}
			return result, err
		}
		if proof.WorktreePresent {
			if err := claimArchivedWorktreeCleanup(&m); err != nil {
				rollback := stage.rollback()
				applyArchiveMetadataState(&result, rollback)
				if rollback.err != nil {
					return result, fmt.Errorf(
						"claim worktree %s cleanup: %w; rollback archive metadata: %v",
						proof.Worktree, err, rollback.err,
					)
				}
				return result, fmt.Errorf("claim worktree %s cleanup: %w", proof.Worktree, err)
			}
			if err := archiveWorktreeRemove(
				proof.Repository,
				proof.Worktree,
				gitx.WorktreeState{
					Head:     proof.ExpectedWorktreeTip,
					Branch:   proof.ExpectedWorktreeBranch,
					Detached: proof.WorktreeDetached,
				},
				force,
			); err != nil {
				present, unchanged, inspectErr := archiveWorktreeStateAfterFailure(proof)
				if inspectErr == nil && unchanged {
					rollback := stage.rollback()
					applyArchiveMetadataState(&result, rollback)
					if rollback.err != nil {
						err = fmt.Errorf("%w; rollback archive metadata: %v", err, rollback.err)
						return result, err
					}
					if !force {
						return result, fmt.Errorf(
							"%w\nhint: use --force to remove worktrees with untracked/modified files", err,
						)
					}
					return result, err
				}
				if inspectErr == nil {
					result.WorktreeRemoved = !present
				}
				return result, fmt.Errorf(
					"remove worktree %s after claiming cleanup: %w; cleanup outcome is ambiguous and "+
						"will not be retried automatically; inspect the archived project and worktree manually",
					proof.Worktree, errors.Join(err, inspectErr),
				)
			}
			result.WorktreeRemoved = true
			if err := consumeArchivedWorktreeProof(&m, project.ArchiveCleanupClaimed); err != nil {
				return result, fmt.Errorf(
					"worktree %s was removed, but its archived cleanup proof could not be consumed: %w",
					proof.Worktree, err,
				)
			}
		}
	}

	if proof.BranchPresent {
		if err := claimArchivedBranchCleanup(&m); err != nil {
			return result, fmt.Errorf("claim branch %q cleanup: %w", proof.Branch, err)
		}
		if err := archiveForceDeleteBranchAt(
			proof.Repository, proof.Branch, proof.ExpectedBranchTip,
		); err != nil {
			if recoveryErr := setBranchDeletionRecovery(
				&result, &m, proof.Repository, proof.Branch, proof.ExpectedBranchTip, err,
			); recoveryErr != nil {
				return result, recoveryErr
			}
		} else {
			result.BranchDeleted = true
			if err := consumeArchivedBranchProof(&m, project.ArchiveCleanupClaimed); err != nil {
				return result, fmt.Errorf(
					"branch %q was deleted, but its archived cleanup proof could not be consumed: %w",
					proof.Branch, err,
				)
			}
		}
	}
	return result, nil
}

func applyCurrentProjectLocation(result *archiveResult, slug string) error {
	activePath := filepath.Join(project.ActiveDir(), slug)
	archivedPath := filepath.Join(project.ArchivedDir(), slug)
	active, activeErr := manifestExists(project.ManifestPath(project.ActiveDir(), slug))
	archived, archivedErr := manifestExists(project.ManifestPath(project.ArchivedDir(), slug))
	if activeErr != nil || archivedErr != nil {
		applyArchiveMetadataState(result, archiveMetadataState{
			transition: archiveTransitionNone,
			location:   archiveLocationUnknown,
		})
		return errors.Join(activeErr, archivedErr)
	}
	switch {
	case active && !archived:
		applyArchiveMetadataState(result, archiveMetadataState{
			transition: archiveTransitionNone,
			location:   archiveLocationActive,
			path:       activePath,
		})
	case archived && !active:
		applyArchiveMetadataState(result, archiveMetadataState{
			transition: archiveTransitionNone,
			location:   archiveLocationArchived,
			path:       archivedPath,
		})
	default:
		applyArchiveMetadataState(result, archiveMetadataState{
			transition: archiveTransitionNone,
			location:   archiveLocationUnknown,
		})
	}
	return nil
}

func manifestExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect project manifest %s: %w", path, err)
	}
	return true, nil
}

func archiveWorktreeStateAfterFailure(
	proof archiveProofSnapshot,
) (present, unchanged bool, err error) {
	state, found, err := gitx.RegisteredWorktreeState(proof.Repository, proof.Worktree)
	if err != nil {
		return false, false, err
	}
	return found, found &&
		state.Head == proof.ExpectedWorktreeTip &&
		state.Branch == proof.ExpectedWorktreeBranch &&
		state.Detached == proof.WorktreeDetached, nil
}

func validateArchiveProof(proof archiveProofSnapshot) (project.Manifest, error) {
	if proof.Kind == "" {
		return project.Manifest{}, fmt.Errorf("archive proof for %s has no proof kind", proof.Slug)
	}
	manifestPath := project.ManifestPath(project.ActiveDir(), proof.Slug)
	m, err := project.Load(manifestPath)
	if err != nil {
		return project.Manifest{}, fmt.Errorf(
			"project not found in active: %s: %w", proof.Slug, err,
		)
	}
	worktree, hasWorktree := "", m.Worktree != nil
	if hasWorktree {
		worktree = *m.Worktree
	}
	if m.Slug != proof.Slug || m.Repo != proof.Repository || m.Branch != proof.Branch ||
		hasWorktree != proof.HasWorktree || worktree != proof.Worktree {
		return project.Manifest{}, fmt.Errorf(
			"project %s manifest changed after %s proof", proof.Slug, proof.Kind,
		)
	}
	manifest, err := json.Marshal(m)
	if err != nil {
		return project.Manifest{}, fmt.Errorf("snapshot project %s manifest: %w", proof.Slug, err)
	}
	if string(manifest) != proof.ManifestJSON {
		return project.Manifest{}, fmt.Errorf(
			"project %s manifest changed after %s proof", proof.Slug, proof.Kind,
		)
	}
	if err := validateArchiveTips(proof); err != nil {
		return project.Manifest{}, err
	}
	return m, nil
}

func validateArchiveTips(proof archiveProofSnapshot) error {
	current, err := newArchiveProofSnapshot(
		project.Manifest{
			Slug: proof.Slug, Repo: proof.Repository, Branch: proof.Branch,
			Worktree: archiveWorktreePointer(proof),
		},
		proof.Kind,
		proof.Merged,
	)
	if err != nil {
		return err
	}
	if current.BranchPresent != proof.BranchPresent {
		return fmt.Errorf("branch presence changed for project %s after %s proof", proof.Slug, proof.Kind)
	}
	if proof.BranchPresent && current.ExpectedBranchTip != proof.ExpectedBranchTip {
		return fmt.Errorf(
			"branch tip changed from %s to %s for project %s after %s proof",
			proof.ExpectedBranchTip, current.ExpectedBranchTip, proof.Slug, proof.Kind,
		)
	}
	if current.WorktreePresent != proof.WorktreePresent {
		return fmt.Errorf("worktree presence changed for project %s after %s proof", proof.Slug, proof.Kind)
	}
	if proof.WorktreePresent && current.ExpectedWorktreeTip != proof.ExpectedWorktreeTip {
		return fmt.Errorf(
			"worktree tip changed from %s to %s for project %s after %s proof",
			proof.ExpectedWorktreeTip, current.ExpectedWorktreeTip, proof.Slug, proof.Kind,
		)
	}
	if proof.WorktreePresent &&
		(current.ExpectedWorktreeBranch != proof.ExpectedWorktreeBranch ||
			current.WorktreeDetached != proof.WorktreeDetached) {
		return fmt.Errorf(
			"worktree identity changed for project %s after %s proof",
			proof.Slug, proof.Kind,
		)
	}
	return nil
}

func archiveWorktreePointer(proof archiveProofSnapshot) *string {
	if !proof.HasWorktree {
		return nil
	}
	worktree := proof.Worktree
	return &worktree
}

func projectLifecycleLockPath(slug string) string {
	// Keep the established filename so new-project creation coordinates with
	// archive cleanup performed by older Relay processes.
	return filepath.Join(project.RelayDir(), "run", "projects", slug, "archive-cleanup.lock")
}

func withProjectLifecycleLock[T any](
	slug string, operation func() (T, error),
) (result T, retErr error) {
	if err := project.ValidateSlug(slug); err != nil {
		return result, err
	}
	path := projectLifecycleLockPath(slug)
	lock, err := patrollock.AcquireWait(path, projectLifecycleLockTimeout)
	if err != nil {
		if errors.Is(err, patrollock.ErrLocked) {
			return result, fmt.Errorf(
				"another lifecycle operation for project %q has held %s for longer than %s; retry after it finishes",
				slug, path, projectLifecycleLockTimeout,
			)
		}
		return result, err
	}
	defer func() {
		if err := lock.Release(); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()
	return operation()
}

func withArchiveCleanupLock(
	slug string, operation func() (archiveResult, error),
) (archiveResult, error) {
	return withProjectLifecycleLock(slug, operation)
}

func loadArchivedCleanupManifest(slug string) (project.Manifest, error) {
	path := project.ManifestPath(project.ArchivedDir(), slug)
	m, err := project.Load(path)
	if err != nil {
		return project.Manifest{}, fmt.Errorf("load archived cleanup manifest for %s: %w", slug, err)
	}
	if m.Slug != slug {
		return project.Manifest{}, fmt.Errorf(
			"archived project manifest slug %q does not match requested slug %q", m.Slug, slug,
		)
	}
	return m, nil
}

func retryArchivedProjectCleanup(m project.Manifest) (archiveResult, error) {
	return retryArchivedProjectCleanupWithForce(m, false)
}

func retryArchivedProjectCleanupWithForce(
	m project.Manifest, force bool,
) (archiveResult, error) {
	if err := project.ValidateSlug(m.Slug); err != nil {
		return archiveResult{}, err
	}
	return withArchiveCleanupLock(m.Slug, func() (archiveResult, error) {
		current, err := loadArchivedCleanupManifest(m.Slug)
		if err != nil {
			return archiveResult{}, err
		}
		return retryArchivedProjectCleanupLocked(current, force)
	})
}

func retryStandaloneArchivedProjectCleanup(
	slug string, force bool,
) (archiveResult, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return archiveResult{}, err
	}
	return withArchiveCleanupLock(slug, func() (archiveResult, error) {
		m, err := loadRecoverableArchivedCleanupManifest(slug)
		if err != nil {
			return archiveResult{}, err
		}
		if m.Program != "" || m.ProgramItem != "" {
			if m.Program != "" && m.ProgramItem != "" {
				return archiveResult{}, fmt.Errorf(
					"archived project %q is managed by program %s/%s; retry with: relay program worker cleanup %s %s",
					slug, m.Program, m.ProgramItem, m.Program, m.ProgramItem,
				)
			}
			return archiveResult{}, fmt.Errorf(
				"archived project %q has incomplete program ownership metadata; inspect %s and use the program worker cleanup lifecycle",
				slug, project.ManifestPath(project.ArchivedDir(), slug),
			)
		}
		return retryArchivedProjectCleanupLocked(m, force)
	})
}

func loadRecoverableArchivedCleanupManifest(slug string) (project.Manifest, error) {
	manifestPath := project.ManifestPath(project.ArchivedDir(), slug)
	installed, installedErr := project.Load(manifestPath)
	if installedErr == nil && installed.ArchiveCleanup != nil {
		if installed.Slug != slug {
			return project.Manifest{}, fmt.Errorf(
				"archived project manifest slug %q does not match requested slug %q",
				installed.Slug, slug,
			)
		}
		return installed, nil
	}

	dir := filepath.Dir(manifestPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if installedErr != nil {
			return project.Manifest{}, fmt.Errorf(
				"load archived cleanup manifest for %s: %w", slug, installedErr,
			)
		}
		return project.Manifest{}, fmt.Errorf("inspect archived project directory %s: %w", dir, err)
	}
	var stagedPaths []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".manifest.archived-") {
			stagedPaths = append(stagedPaths, filepath.Join(dir, entry.Name()))
		}
	}
	if len(stagedPaths) == 0 {
		if installedErr != nil {
			return project.Manifest{}, fmt.Errorf(
				"load archived cleanup manifest for %s: %w", slug, installedErr,
			)
		}
		if installed.Slug != slug {
			return project.Manifest{}, fmt.Errorf(
				"archived project manifest slug %q does not match requested slug %q",
				installed.Slug, slug,
			)
		}
		return installed, nil
	}
	if len(stagedPaths) != 1 {
		return project.Manifest{}, stagedArchiveRecoveryError(
			slug, fmt.Sprintf("found %d staged archived manifests", len(stagedPaths)),
		)
	}
	stagedPath := stagedPaths[0]
	info, err := os.Lstat(stagedPath)
	if err != nil {
		return project.Manifest{}, stagedArchiveRecoveryError(slug, err.Error())
	}
	if !info.Mode().IsRegular() {
		return project.Manifest{}, stagedArchiveRecoveryError(
			slug, fmt.Sprintf("%s is not a regular file", stagedPath),
		)
	}
	staged, err := project.Load(stagedPath)
	if err != nil {
		return project.Manifest{}, stagedArchiveRecoveryError(slug, err.Error())
	}
	if staged.Slug != slug || staged.Status != "archived" || staged.Archived == nil ||
		staged.ArchiveCleanup == nil {
		return project.Manifest{}, stagedArchiveRecoveryError(
			slug, fmt.Sprintf("%s is not a complete archived manifest for %q", stagedPath, slug),
		)
	}
	if _, err := validateArchivedCleanupProof(staged); err != nil {
		return project.Manifest{}, stagedArchiveRecoveryError(slug, err.Error())
	}

	switch {
	case installedErr == nil:
		if !sameArchiveRecoveryIdentity(installed, staged) {
			return project.Manifest{}, stagedArchiveRecoveryError(
				slug, fmt.Sprintf(
					"installed manifest %s does not match staged resource identity", manifestPath,
				),
			)
		}
		installedInfo, err := os.Lstat(manifestPath)
		if err != nil {
			return project.Manifest{}, stagedArchiveRecoveryError(slug, err.Error())
		}
		if !installedInfo.Mode().IsRegular() {
			return project.Manifest{}, stagedArchiveRecoveryError(
				slug, fmt.Sprintf("%s is not a regular file", manifestPath),
			)
		}
		if err := archiveRename(stagedPath, manifestPath); err != nil {
			return project.Manifest{}, fmt.Errorf(
				"recover staged archived manifest %s: install over verified source manifest: %w",
				stagedPath, err,
			)
		}
	case errors.Is(installedErr, os.ErrNotExist):
		if err := os.Link(stagedPath, manifestPath); err != nil {
			return project.Manifest{}, fmt.Errorf(
				"recover staged archived manifest %s without overwriting %s: %w",
				stagedPath, manifestPath, err,
			)
		}
		if err := archiveRemoveFile(stagedPath); err != nil {
			return project.Manifest{}, fmt.Errorf(
				"recover staged archived manifest %s: remove staged link after installation: %w",
				stagedPath, err,
			)
		}
	default:
		return project.Manifest{}, stagedArchiveRecoveryError(slug, installedErr.Error())
	}
	return loadArchivedCleanupManifest(slug)
}

func sameArchiveRecoveryIdentity(installed, staged project.Manifest) bool {
	installedWorktree, stagedWorktree := "", ""
	if installed.Worktree != nil {
		installedWorktree = *installed.Worktree
	}
	if staged.Worktree != nil {
		stagedWorktree = *staged.Worktree
	}
	return installed.Slug == staged.Slug &&
		installed.Repo == staged.Repo &&
		installed.Branch == staged.Branch &&
		installedWorktree == stagedWorktree &&
		installed.Program == staged.Program &&
		installed.ProgramItem == staged.ProgramItem
}

func stagedArchiveRecoveryError(slug, detail string) error {
	return fmt.Errorf(
		"archived project %q has unsafe staged archive metadata: %s; resources were preserved. "+
			"Inspect %s, keep exactly one valid .manifest.archived-* file, and rerun: relay archive %s",
		slug, detail, filepath.Join(project.ArchivedDir(), slug), slug,
	)
}

func validateManifestWorktreeMetadata(m project.Manifest) error {
	if m.Worktree != nil && strings.TrimSpace(*m.Worktree) == "" {
		return fmt.Errorf(
			"project %s has malformed worktree metadata: worktree is present but empty; "+
				"preserving project resources for manual repair",
			m.Slug,
		)
	}
	return nil
}

func retryArchivedProjectCleanupLocked(
	m project.Manifest, force bool,
) (archiveResult, error) {
	result := archiveResult{
		Slug:               m.Slug,
		Branch:             m.Branch,
		Merged:             m.Merged,
		MetadataTransition: archiveTransitionStaged,
		ProjectLocation:    archiveLocationArchived,
		ProjectPath:        filepath.Join(project.ArchivedDir(), m.Slug),
		ArchivedPath:       filepath.Join(project.ArchivedDir(), m.Slug),
		Warnings:           []string{},
	}
	if m.Worktree != nil {
		result.Worktree = *m.Worktree
	}
	if err := validateManifestWorktreeMetadata(m); err != nil {
		return result, err
	}
	if m.ArchiveCleanup == nil {
		if err := validateLegacyArchivedCleanup(m); err != nil {
			return result, err
		}
		return result, nil
	}
	proof, err := validateArchivedCleanupProof(m)
	if err != nil {
		return result, err
	}
	if proof.BranchState != project.ArchiveCleanupDone ||
		proof.WorktreeState != project.ArchiveCleanupDone {
		if err := requireExclusiveProjectResources(
			project.ManifestPath(project.ArchivedDir(), m.Slug), nil,
		); err != nil {
			return result, fmt.Errorf("finish archived cleanup for %s: %w", m.Slug, err)
		}
	}
	force = force || proof.ForceAuthorized
	current, err := validateArchivedCleanupResources(proof)
	if err != nil {
		return result, err
	}
	switch proof.WorktreeState {
	case project.ArchiveCleanupClaimed:
		if current.worktreePresent {
			return result, claimedCleanupError(&m, "worktree", proof.Worktree, "")
		}
		if err := consumeArchivedWorktreeProof(&m, project.ArchiveCleanupClaimed); err != nil {
			return result, fmt.Errorf(
				"finish archived worktree cleanup for %s: worktree is absent, but its claimed cleanup "+
					"state could not be completed: %w",
				m.Slug, err,
			)
		}
	case project.ArchiveCleanupPending:
		if !current.worktreePresent {
			if err := consumeArchivedWorktreeProof(&m, project.ArchiveCleanupPending); err != nil {
				return result, fmt.Errorf(
					"finish archived worktree cleanup for %s: worktree is absent, but its cleanup proof "+
						"could not be consumed: %w",
					m.Slug, err,
				)
			}
			break
		}
		if err := claimArchivedWorktreeCleanup(&m); err != nil {
			return result, fmt.Errorf(
				"finish archived worktree cleanup for %s: claim removal: %w", m.Slug, err,
			)
		}
		if err := archiveWorktreeRemove(
			m.Repo,
			*m.Worktree,
			gitx.WorktreeState{
				Head:     proof.ExpectedWorktreeTip,
				Branch:   proof.ExpectedWorktreeBranch,
				Detached: proof.WorktreeDetached,
			},
			force,
		); err != nil {
			if !force {
				state, found, inspectErr := gitx.RegisteredWorktreeState(m.Repo, *m.Worktree)
				unchanged := found &&
					state.Head == proof.ExpectedWorktreeTip &&
					state.Branch == proof.ExpectedWorktreeBranch &&
					state.Detached == proof.WorktreeDetached
				if inspectErr == nil && unchanged {
					if resetErr := resetArchivedWorktreeCleanup(&m); resetErr != nil {
						return result, fmt.Errorf(
							"finish archived worktree cleanup for %s: removal failed without force: %w; "+
								"restore retry authorization: %v",
							m.Slug, err, resetErr,
						)
					}
					return result, fmt.Errorf(
						"finish archived worktree cleanup for %s: cleanup remains incomplete: %w\n"+
							"hint: retry with: relay archive %s --force",
						m.Slug, err, m.Slug,
					)
				}
			}
			return result, fmt.Errorf(
				"finish archived worktree cleanup for %s: removal was claimed and will not be "+
					"retried automatically: %w; inspect %s and remove it manually if it still belongs "+
					"to this project",
				m.Slug, err, *m.Worktree,
			)
		}
		result.WorktreeRemoved = true
		if err := consumeArchivedWorktreeProof(&m, project.ArchiveCleanupClaimed); err != nil {
			return result, fmt.Errorf(
				"finish archived worktree cleanup for %s: worktree was removed, but its claimed cleanup "+
					"state could not be completed: %w",
				m.Slug, err,
			)
		}
	}
	latestBranchPresent, err := validateArchivedBranchCleanupResource(proof)
	if err != nil {
		return result, err
	}
	if !current.branchPresent && latestBranchPresent {
		return result, archivedCleanupDriftError(
			proof,
			fmt.Sprintf("branch %q appeared during worktree cleanup", proof.Branch),
		)
	}
	current.branchPresent = latestBranchPresent
	if !current.branchPresent {
		if proof.BranchState != project.ArchiveCleanupDone {
			if err := archiveRemoveBranchConfig(m.Repo, m.Branch); err != nil {
				command := manualBranchConfigRemoveCommand(m.Repo, m.Branch)
				return result, newManualCleanupError(fmt.Errorf(
					"finish archived branch cleanup for %s: branch ref is absent, but its local "+
						"config could not be removed: %w; retry with: %s",
					m.Slug, err, command,
				), command)
			}
			if err := consumeArchivedBranchProof(&m, proof.BranchState); err != nil {
				return result, fmt.Errorf(
					"finish archived branch cleanup for %s: branch is absent, but its cleanup proof "+
						"could not be consumed: %w",
					m.Slug, err,
				)
			}
		}
		return result, nil
	}
	if proof.BranchState == project.ArchiveCleanupClaimed {
		return result, claimedCleanupError(
			&m, "branch", proof.Branch, proof.ExpectedBranchTip,
		)
	}
	if err := claimArchivedBranchCleanup(&m); err != nil {
		return result, fmt.Errorf(
			"finish archived branch cleanup for %s: claim deletion: %w", m.Slug, err,
		)
	}
	if err := archiveForceDeleteBranchAt(
		m.Repo, m.Branch, proof.ExpectedBranchTip,
	); err != nil {
		if recoveryErr := setBranchDeletionRecovery(
			&result, &m, m.Repo, m.Branch, proof.ExpectedBranchTip, err,
		); recoveryErr != nil {
			return result, recoveryErr
		}
		return result, nil
	}
	result.BranchDeleted = true
	if err := consumeArchivedBranchProof(&m, project.ArchiveCleanupClaimed); err != nil {
		return result, fmt.Errorf(
			"finish archived branch cleanup for %s: branch was deleted, but its cleanup proof "+
				"could not be consumed: %w",
			m.Slug, err,
		)
	}
	return result, nil
}

func claimedCleanupError(
	m *project.Manifest, resource, identity, expectedSHA string,
) error {
	if resource == "branch" {
		worktree, checkedOut, err := gitx.BranchCheckoutWorktree(m.Repo, identity)
		if err != nil {
			return fmt.Errorf(
				"finish archived branch cleanup for %s: cleanup was already claimed and its outcome "+
					"is ambiguous; inspect whether branch %q is checked out before taking any "+
					"destructive action: %w",
				m.Slug, identity, err,
			)
		}
		if checkedOut {
			return fmt.Errorf(
				"finish archived branch cleanup for %s: cleanup was already claimed and its outcome "+
					"is ambiguous; will not retry removal of %s automatically: %w",
				m.Slug, identity, checkedOutBranchCleanupError(m.Slug, identity, worktree),
			)
		}
		manual := manualBranchDeleteAtCommand(m.Repo, identity, expectedSHA)
		return newManualCleanupError(fmt.Errorf(
			"finish archived branch cleanup for %s: cleanup was already claimed and its outcome is ambiguous; "+
				"will not retry removal of %s; inspect it and remove it manually only if it still belongs "+
				"to this archived project\nhint: delete the unchanged ref with: %s\n"+
				"if branch config remains, remove it with: %s",
			m.Slug, identity, manual, manualBranchConfigRemoveCommand(m.Repo, identity),
		), manual)
	}
	manual := "git -C " + shellQuote(m.Repo) + " worktree remove --force " + shellQuote(identity)
	return newManualCleanupError(fmt.Errorf(
		"finish archived %s cleanup for %s: cleanup was already claimed and its outcome is ambiguous; "+
			"will not retry removal of %s; inspect it and remove it manually only if it still belongs "+
			"to this archived project\nhint: remove manually with: %s",
		resource, m.Slug, identity, manual,
	), manual)
}

type manualCleanupError struct {
	cause   error
	command string
}

func newManualCleanupError(cause error, command string) error {
	return &manualCleanupError{cause: cause, command: command}
}

func (e *manualCleanupError) Error() string { return e.cause.Error() }

func (e *manualCleanupError) Unwrap() error { return e.cause }

func manualCleanupCommand(err error) string {
	var manualErr *manualCleanupError
	if errors.As(err, &manualErr) {
		return manualErr.command
	}
	return ""
}

type archivedCleanupResources struct {
	branchPresent   bool
	worktreePresent bool
}

func archiveCleanupProof(proof archiveProofSnapshot) *project.ArchiveCleanupProof {
	branchState := project.ArchiveCleanupDone
	if proof.BranchPresent {
		branchState = project.ArchiveCleanupPending
	}
	worktreeState := project.ArchiveCleanupDone
	if proof.WorktreePresent {
		worktreeState = project.ArchiveCleanupPending
	}
	return &project.ArchiveCleanupProof{
		Repository:             proof.Repository,
		Branch:                 proof.Branch,
		Worktree:               proof.Worktree,
		BranchPresent:          proof.BranchPresent,
		ExpectedBranchTip:      proof.ExpectedBranchTip,
		BranchState:            branchState,
		WorktreePresent:        proof.WorktreePresent,
		ExpectedWorktreeTip:    proof.ExpectedWorktreeTip,
		ExpectedWorktreeBranch: proof.ExpectedWorktreeBranch,
		WorktreeDetached:       proof.WorktreeDetached,
		AuthoritativeCommit:    proof.AuthoritativeCommit,
		ForceAuthorized:        proof.ForceAuthorized,
		WorktreeState:          worktreeState,
	}
}

func claimArchivedWorktreeCleanup(m *project.Manifest) error {
	return updateArchivedCleanupProof(m, func(proof *project.ArchiveCleanupProof) error {
		if cleanupState(proof.WorktreeState, proof.WorktreePresent) != project.ArchiveCleanupPending {
			return fmt.Errorf("worktree cleanup state is %q, want pending", proof.WorktreeState)
		}
		proof.WorktreeState = project.ArchiveCleanupClaimed
		return nil
	})
}

func resetArchivedWorktreeCleanup(m *project.Manifest) error {
	return updateArchivedCleanupProof(m, func(proof *project.ArchiveCleanupProof) error {
		if cleanupState(proof.WorktreeState, proof.WorktreePresent) != project.ArchiveCleanupClaimed {
			return fmt.Errorf("worktree cleanup state is %q, want claimed", proof.WorktreeState)
		}
		proof.WorktreeState = project.ArchiveCleanupPending
		return nil
	})
}

func claimArchivedBranchCleanup(m *project.Manifest) error {
	return updateArchivedCleanupProof(m, func(proof *project.ArchiveCleanupProof) error {
		if cleanupState(proof.BranchState, proof.BranchPresent) != project.ArchiveCleanupPending {
			return fmt.Errorf("branch cleanup state is %q, want pending", proof.BranchState)
		}
		proof.BranchState = project.ArchiveCleanupClaimed
		return nil
	})
}

func resetArchivedBranchCleanup(m *project.Manifest) error {
	return updateArchivedCleanupProof(m, func(proof *project.ArchiveCleanupProof) error {
		if cleanupState(proof.BranchState, proof.BranchPresent) != project.ArchiveCleanupClaimed {
			return fmt.Errorf("branch cleanup state is %q, want claimed", proof.BranchState)
		}
		proof.BranchState = project.ArchiveCleanupPending
		return nil
	})
}

func consumeArchivedWorktreeProof(
	m *project.Manifest, expected project.ArchiveCleanupState,
) error {
	return updateArchivedCleanupProof(m, func(proof *project.ArchiveCleanupProof) error {
		if cleanupState(proof.WorktreeState, proof.WorktreePresent) != expected {
			return fmt.Errorf(
				"worktree cleanup state is %q, want %q", proof.WorktreeState, expected,
			)
		}
		proof.WorktreeState = project.ArchiveCleanupDone
		proof.WorktreePresent = false
		proof.ExpectedWorktreeTip = ""
		proof.ExpectedWorktreeBranch = ""
		proof.WorktreeDetached = false
		return nil
	})
}

func consumeArchivedBranchProof(
	m *project.Manifest, expected project.ArchiveCleanupState,
) error {
	return updateArchivedCleanupProof(m, func(proof *project.ArchiveCleanupProof) error {
		if cleanupState(proof.BranchState, proof.BranchPresent) != expected {
			return fmt.Errorf(
				"branch cleanup state is %q, want %q", proof.BranchState, expected,
			)
		}
		proof.BranchState = project.ArchiveCleanupDone
		proof.BranchPresent = false
		proof.ExpectedBranchTip = ""
		return nil
	})
}

func updateArchivedCleanupProof(
	m *project.Manifest, update func(*project.ArchiveCleanupProof) error,
) error {
	if m.ArchiveCleanup == nil {
		return fmt.Errorf("archive cleanup proof is missing")
	}
	next := *m
	proof := *m.ArchiveCleanup
	next.ArchiveCleanup = &proof
	if err := update(next.ArchiveCleanup); err != nil {
		return err
	}
	if err := persistArchivedCleanupProof(next); err != nil {
		return err
	}
	*m = next
	return nil
}

func persistArchivedCleanupProof(m project.Manifest) error {
	path := project.ManifestPath(project.ArchivedDir(), m.Slug)
	temp, err := os.CreateTemp(filepath.Dir(path), ".manifest.cleanup-*")
	if err != nil {
		return fmt.Errorf("create atomic archived cleanup proof in %s: %w", filepath.Dir(path), err)
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		err = archiveCleanupError(err, archiveRemoveFile(tempPath))
		return fmt.Errorf("close atomic archived cleanup proof %s: %w", tempPath, err)
	}
	if err := saveArchiveManifest(tempPath, m); err != nil {
		err = archiveCleanupError(err, archiveRemoveFile(tempPath))
		return fmt.Errorf("write atomic archived cleanup proof %s: %w", tempPath, err)
	}
	if err := archiveRename(tempPath, path); err != nil {
		err = archiveCleanupError(err, archiveRemoveFile(tempPath))
		return fmt.Errorf("install atomic archived cleanup proof %s: %w", path, err)
	}
	return nil
}

func cleanupState(
	state project.ArchiveCleanupState, present bool,
) project.ArchiveCleanupState {
	if state != "" {
		return state
	}
	if present {
		return project.ArchiveCleanupPending
	}
	return project.ArchiveCleanupDone
}

func validateCleanupState(
	resource string, state project.ArchiveCleanupState, present bool,
) (project.ArchiveCleanupState, error) {
	state = cleanupState(state, present)
	switch state {
	case project.ArchiveCleanupPending, project.ArchiveCleanupClaimed:
		if !present {
			return "", fmt.Errorf(
				"%s cleanup state %q has no recorded resource", resource, state,
			)
		}
	case project.ArchiveCleanupDone:
		if present {
			return "", fmt.Errorf(
				"%s cleanup state is done but the cleanup proof still records it as present", resource,
			)
		}
	default:
		return "", fmt.Errorf("%s cleanup state %q is invalid", resource, state)
	}
	return state, nil
}

func normalizeArchivedCleanupStates(
	proof *project.ArchiveCleanupProof,
) error {
	branchState, err := validateCleanupState("branch", proof.BranchState, proof.BranchPresent)
	if err != nil {
		return err
	}
	worktreeState, err := validateCleanupState(
		"worktree", proof.WorktreeState, proof.WorktreePresent,
	)
	if err != nil {
		return err
	}
	proof.BranchState = branchState
	proof.WorktreeState = worktreeState
	return nil
}

func validateLegacyArchivedCleanup(m project.Manifest) error {
	var issues []string
	if m.Branch != "" {
		present, err := archiveBranchExists(m.Repo, m.Branch)
		switch {
		case err != nil:
			issues = append(issues, fmt.Sprintf("inspect archived branch %q: %s", m.Branch, err))
		case present:
			issues = append(issues, fmt.Sprintf("branch %q is still present", m.Branch))
		}
	}
	if m.Worktree != nil && *m.Worktree != "" {
		_, present, err := gitx.RegisteredWorktreeState(m.Repo, *m.Worktree)
		switch {
		case err != nil:
			issues = append(issues, fmt.Sprintf("inspect archived worktree %s: %s", *m.Worktree, err))
		case present:
			issues = append(issues, fmt.Sprintf("worktree %s is still present", *m.Worktree))
		}
	}
	if len(issues) == 0 {
		return nil
	}
	return fmt.Errorf(
		"archived project %s has no durable cleanup proof and cleanup is incomplete or uncertain: %s; "+
			"preserving worktree and branch for manual inspection",
		m.Slug, strings.Join(issues, "; "),
	)
}

func validateArchivedCleanupProof(m project.Manifest) (project.ArchiveCleanupProof, error) {
	if m.ArchiveCleanup == nil {
		return project.ArchiveCleanupProof{}, fmt.Errorf(
			"archived project %s has no durable cleanup proof; preserving worktree and branch; "+
				"inspect them and remove them manually only if they still belong to this archived project",
			m.Slug,
		)
	}
	proof := *m.ArchiveCleanup
	if err := normalizeArchivedCleanupStates(&proof); err != nil {
		return project.ArchiveCleanupProof{}, fmt.Errorf(
			"archived project %s cleanup proof has ambiguous cleanup state: %w; preserving resources "+
				"for manual inspection",
			m.Slug, err,
		)
	}
	worktree := ""
	if m.Worktree != nil {
		worktree = *m.Worktree
	}
	if proof.Repository != m.Repo || proof.Branch != m.Branch || proof.Worktree != worktree {
		return project.ArchiveCleanupProof{}, fmt.Errorf(
			"archived project %s cleanup proof does not match its recorded repository, branch, or worktree; "+
				"preserving resources for manual inspection",
			m.Slug,
		)
	}
	if proof.BranchPresent == (proof.ExpectedBranchTip == "") {
		return project.ArchiveCleanupProof{}, fmt.Errorf(
			"archived project %s cleanup proof has ambiguous branch state; preserving resources for manual inspection",
			m.Slug,
		)
	}
	if proof.BranchPresent &&
		(!isCanonicalCommitID(proof.ExpectedBranchTip) ||
			!isCanonicalCommitID(proof.AuthoritativeCommit) ||
			proof.ExpectedBranchTip != proof.AuthoritativeCommit) {
		return project.ArchiveCleanupProof{}, fmt.Errorf(
			"archived project %s cleanup proof does not bind branch %q to one canonical authoritative "+
				"commit; preserving resources for manual inspection",
			m.Slug, proof.Branch,
		)
	}
	if proof.WorktreePresent {
		if proof.Worktree == "" || proof.ExpectedWorktreeTip == "" ||
			(proof.ExpectedWorktreeBranch == "") == !proof.WorktreeDetached {
			return project.ArchiveCleanupProof{}, fmt.Errorf(
				"archived project %s cleanup proof has ambiguous worktree state; preserving resources for manual inspection",
				m.Slug,
			)
		}
		if !proof.WorktreeDetached && proof.ExpectedWorktreeBranch != "refs/heads/"+proof.Branch {
			return project.ArchiveCleanupProof{}, fmt.Errorf(
				"archived project %s cleanup proof records worktree %s on branch %q, want %q; "+
					"preserving resources for manual inspection",
				m.Slug, proof.Worktree, proof.ExpectedWorktreeBranch, "refs/heads/"+proof.Branch,
			)
		}
		if proof.WorktreeDetached &&
			(proof.AuthoritativeCommit == "" || proof.ExpectedWorktreeTip != proof.AuthoritativeCommit) {
			return project.ArchiveCleanupProof{}, fmt.Errorf(
				"archived project %s cleanup proof does not bind detached worktree %s to an "+
					"authoritative project commit; preserving resources for manual inspection",
				m.Slug, proof.Worktree,
			)
		}
	} else if proof.ExpectedWorktreeTip != "" || proof.ExpectedWorktreeBranch != "" ||
		proof.WorktreeDetached {
		return project.ArchiveCleanupProof{}, fmt.Errorf(
			"archived project %s cleanup proof has ambiguous worktree state; preserving resources for manual inspection",
			m.Slug,
		)
	}
	return proof, nil
}

func isCanonicalCommitID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validateArchivedCleanupResources(
	proof project.ArchiveCleanupProof,
) (archivedCleanupResources, error) {
	branchPresent, err := validateArchivedBranchCleanupResource(proof)
	if err != nil {
		return archivedCleanupResources{}, err
	}

	worktreePresent := false
	if proof.Worktree != "" {
		worktreeState, found, err := gitx.RegisteredWorktreeState(
			proof.Repository, proof.Worktree,
		)
		if err != nil {
			return archivedCleanupResources{}, archivedCleanupDriftError(proof, err.Error())
		}
		worktreePresent = found
		if !proof.WorktreePresent && worktreePresent {
			return archivedCleanupResources{}, archivedCleanupDriftError(
				proof, fmt.Sprintf("worktree path %s was registered after the original cleanup proof", proof.Worktree),
			)
		}
		if worktreePresent &&
			(worktreeState.Head != proof.ExpectedWorktreeTip ||
				worktreeState.Branch != proof.ExpectedWorktreeBranch ||
				worktreeState.Detached != proof.WorktreeDetached) {
			return archivedCleanupResources{}, archivedCleanupDriftError(
				proof, fmt.Sprintf("worktree path %s was reused or changed", proof.Worktree),
			)
		}
	}
	return archivedCleanupResources{
		branchPresent: branchPresent, worktreePresent: worktreePresent,
	}, nil
}

func validateArchivedBranchCleanupResource(
	proof project.ArchiveCleanupProof,
) (bool, error) {
	if proof.Branch == "" {
		return false, nil
	}
	branchTip, found, err := gitx.LocalBranchTip(proof.Repository, proof.Branch)
	if err != nil {
		return false, fmt.Errorf("inspect archived branch %q: %w", proof.Branch, err)
	}
	if !proof.BranchPresent && found {
		return false, archivedCleanupDriftError(
			proof, fmt.Sprintf("branch %q appeared after the original cleanup proof", proof.Branch),
		)
	}
	if found && branchTip != proof.ExpectedBranchTip {
		return false, archivedCleanupDriftError(
			proof,
			fmt.Sprintf(
				"branch %q advanced from %s to %s",
				proof.Branch, proof.ExpectedBranchTip, branchTip,
			),
		)
	}
	return found, nil
}

func archivedCleanupDriftError(proof project.ArchiveCleanupProof, detail string) error {
	return fmt.Errorf(
		"archived cleanup proof no longer matches current resources: %s; preserving worktree and branch; "+
			"inspect %s and branch %q in %s before removing them manually",
		detail, proof.Worktree, proof.Branch, proof.Repository,
	)
}

func manualBranchDeleteAtCommand(repo, branch, expectedSHA string) string {
	return "git -C " + shellQuote(repo) + " update-ref -d " +
		shellQuote("refs/heads/"+branch) + " " + shellQuote(expectedSHA)
}

func manualBranchConfigRemoveCommand(repo, branch string) string {
	return "git -C " + shellQuote(repo) + " config --local --remove-section " +
		shellQuote("branch."+branch)
}

func setBranchDeletionRecovery(
	result *archiveResult,
	m *project.Manifest,
	repo, branch, expectedSHA string,
	deleteErr error,
) error {
	tip, found, inspectErr := gitx.LocalBranchTip(repo, branch)
	switch {
	case inspectErr != nil:
		result.BranchDeletionWarning = fmt.Sprintf(
			"%s; inspect branch after cleanup failure: %v",
			deleteErr, inspectErr,
		)
	case !found:
		result.BranchDeleted = true
		result.BranchCleanupCommand = manualBranchConfigRemoveCommand(repo, branch)
		result.BranchDeletionWarning = fmt.Sprintf(
			"%s\nbranch ref was deleted; finish config cleanup with: %s",
			deleteErr, result.BranchCleanupCommand,
		)
	case expectedSHA == "":
		result.BranchDeletionWarning = fmt.Sprintf(
			"%s\nbranch cleanup has no expected commit; inspect branch %q before deleting it",
			deleteErr, branch,
		)
	case tip != expectedSHA:
		result.BranchDeletionWarning = fmt.Sprintf(
			"%s\nbranch %q now points at %s instead of expected commit %s; "+
				"inspect it before taking any destructive action",
			deleteErr, branch, tip, expectedSHA,
		)
	default:
		var checkedOutErr *gitx.BranchCheckedOutError
		if errors.As(deleteErr, &checkedOutErr) && checkedOutErr.Branch == branch {
			if err := resetArchivedBranchCleanup(m); err != nil {
				return fmt.Errorf(
					"branch %q deletion was blocked because it is checked out at expected commit %s, "+
						"but reset its cleanup obligation for retry: %w",
					branch, expectedSHA, err,
				)
			}
			result.BranchDeletionWarning = errors.Join(
				deleteErr,
				checkedOutBranchCleanupError(result.Slug, branch, checkedOutErr.Worktree),
			).Error()
			return nil
		}
		result.BranchCleanupCommand = manualBranchDeleteAtCommand(repo, branch, expectedSHA)
		result.BranchDeletionWarning = fmt.Sprintf(
			"%s\ncleanup was already claimed and will not be retried automatically\n"+
				"hint: delete the unchanged ref with: %s\n"+
				"if branch config remains, remove it with: %s",
			deleteErr, result.BranchCleanupCommand,
			manualBranchConfigRemoveCommand(repo, branch),
		)
	}
	return nil
}

func checkedOutBranchCleanupError(slug, branch, worktree string) error {
	return fmt.Errorf(
		"branch %q is checked out in worktree %s; do not delete its ref directly. "+
			"Detach or remove that specific worktree, then rerun Relay cleanup for project %s",
		branch, worktree, slug,
	)
}

type archiveMetadataState struct {
	transition string
	location   string
	path       string
	rollback   func() archiveMetadataState
	err        error
}

func applyArchiveMetadataState(result *archiveResult, state archiveMetadataState) {
	result.MetadataTransition = state.transition
	result.ProjectLocation = state.location
	result.ProjectPath = state.path
	result.ArchivedPath = ""
	if state.location == archiveLocationArchived {
		result.ArchivedPath = state.path
	}
}

func stageArchivedProject(srcDir, dstDir string, m project.Manifest) (archiveMetadataState, error) {
	active := archiveMetadataState{
		transition: archiveTransitionNone,
		location:   archiveLocationActive,
		path:       srcDir,
	}
	manifestPath := filepath.Join(srcDir, "manifest.json")
	originalManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return active, fmt.Errorf("read active manifest for archive: %w", err)
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		return active, fmt.Errorf("stat active manifest for archive: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dstDir), 0755); err != nil {
		return active, fmt.Errorf("create archived dir: %w", err)
	}
	if _, err := os.Lstat(dstDir); err == nil {
		return active, fmt.Errorf("move project to archived: destination already exists: %s", dstDir)
	} else if !os.IsNotExist(err) {
		return active, fmt.Errorf("inspect archive destination %s: %w", dstDir, err)
	}

	staged, err := os.CreateTemp(srcDir, ".manifest.archived-*")
	if err != nil {
		return active, fmt.Errorf("stage archived manifest: %w", err)
	}
	stagedPath := staged.Name()
	if err := staged.Close(); err != nil {
		err = archiveCleanupError(err, archiveRemoveFile(stagedPath))
		return active, fmt.Errorf("stage archived manifest: close temporary file: %w", err)
	}
	if err := saveArchiveManifest(stagedPath, m); err != nil {
		err = archiveCleanupError(err, archiveRemoveFile(stagedPath))
		return active, fmt.Errorf("stage archived manifest: %w", err)
	}

	if err := archiveRename(srcDir, dstDir); err != nil {
		err = archiveCleanupError(err, archiveRemoveFile(stagedPath))
		return active, fmt.Errorf("move project to archived: %w", err)
	}
	archived := archiveMetadataState{
		transition: archiveTransitionRollbackIncomplete,
		location:   archiveLocationArchived,
		path:       dstDir,
	}
	stagedPath = filepath.Join(dstDir, filepath.Base(stagedPath))
	archivedManifestPath := filepath.Join(dstDir, "manifest.json")
	if err := archiveRename(stagedPath, archivedManifestPath); err != nil {
		rollbackErr := archiveRename(dstDir, srcDir)
		if rollbackErr != nil {
			return archived, fmt.Errorf(
				"install archived manifest: %w; rollback project directory: %v", err, rollbackErr,
			)
		}
		err = archiveCleanupError(err, archiveRemoveFile(filepath.Join(srcDir, filepath.Base(stagedPath))))
		active.transition = archiveTransitionRolledBack
		return active, fmt.Errorf("install archived manifest: %w", err)
	}

	stagedState := archiveMetadataState{
		transition: archiveTransitionStaged,
		location:   archiveLocationArchived,
		path:       dstDir,
	}
	stagedState.rollback = func() archiveMetadataState {
		if err := archiveRename(dstDir, srcDir); err != nil {
			return archiveMetadataState{
				transition: archiveTransitionRollbackIncomplete,
				location:   archiveLocationArchived,
				path:       dstDir,
				err:        fmt.Errorf("restore active project directory: %w", err),
			}
		}
		incomplete := archiveMetadataState{
			transition: archiveTransitionRollbackIncomplete,
			location:   archiveLocationActive,
			path:       srcDir,
		}
		rollbackFile, err := os.CreateTemp(srcDir, ".manifest.active-*")
		if err != nil {
			incomplete.err = fmt.Errorf("stage active manifest: %w", err)
			return incomplete
		}
		rollbackPath := rollbackFile.Name()
		if err := rollbackFile.Chmod(info.Mode().Perm()); err != nil {
			err = archiveCleanupError(err, rollbackFile.Close())
			err = archiveCleanupError(err, archiveRemoveFile(rollbackPath))
			incomplete.err = fmt.Errorf("stage active manifest permissions: %w", err)
			return incomplete
		}
		if _, err := rollbackFile.Write(originalManifest); err != nil {
			err = archiveCleanupError(err, rollbackFile.Close())
			err = archiveCleanupError(err, archiveRemoveFile(rollbackPath))
			incomplete.err = fmt.Errorf("stage active manifest contents: %w", err)
			return incomplete
		}
		if err := rollbackFile.Close(); err != nil {
			err = archiveCleanupError(err, archiveRemoveFile(rollbackPath))
			incomplete.err = fmt.Errorf("stage active manifest: close temporary file: %w", err)
			return incomplete
		}
		activeManifestPath := filepath.Join(srcDir, "manifest.json")
		if err := archiveRename(rollbackPath, activeManifestPath); err != nil {
			err = archiveCleanupError(err, archiveRemoveFile(rollbackPath))
			incomplete.err = fmt.Errorf("restore active manifest: %w", err)
			return incomplete
		}
		return archiveMetadataState{
			transition: archiveTransitionRolledBack,
			location:   archiveLocationActive,
			path:       srcDir,
		}
	}
	return stagedState, nil
}

func archiveCleanupError(cause, cleanupErr error) error {
	if cleanupErr == nil || os.IsNotExist(cleanupErr) {
		return cause
	}
	return fmt.Errorf("%w; remove staged manifest: %v", cause, cleanupErr)
}
