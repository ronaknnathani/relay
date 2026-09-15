package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

var (
	loadArchivePullRequestProof = programview.GitHubPullRequestProof
	loadArchiveRepository       = programview.GitHubRepository
	saveArchiveManifest         = project.Save
	archiveBranchExists         = gitx.LocalBranchExists
	archiveForceDeleteBranchAt  = gitx.ForceDeleteBranchAt
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
	branchTip, found, err := gitx.LocalBranchTip(m.Repo, m.Branch)
	if err != nil {
		return recordedPullRequestEvidence{}, fmt.Errorf(
			"resolve branch %q tip for recorded pull request %s for %s: %w",
			m.Branch, safeRef, slug, err,
		)
	}
	if !found && proof.HeadBranch != m.Branch {
		return recordedPullRequestEvidence{}, fmt.Errorf(
			"recorded pull request %s for %s head branch %q does not match manifest branch %q",
			safeRef, slug, proof.HeadBranch, m.Branch,
		)
	}
	worktreeHead := ""
	worktreeFound := false
	if m.Worktree != nil && *m.Worktree != "" {
		worktreeHead, worktreeFound, err = gitx.WorktreeHead(m.Repo, *m.Worktree)
		if err != nil {
			return recordedPullRequestEvidence{}, fmt.Errorf(
				"resolve worktree HEAD for recorded pull request %s for %s: %w",
				safeRef, slug, err,
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
	if worktreeFound && worktreeHead != proof.HeadSHA {
		return recordedPullRequestEvidence{}, fmt.Errorf(
			"recorded pull request %s for %s head %s does not match worktree HEAD %s",
			safeRef, slug, proof.HeadSHA, worktreeHead,
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
	Merged                bool     `json:"merged"`
	ArchivedPath          string   `json:"archived_path"`
	Warnings              []string `json:"warnings"`
}

// runArchive archives a project and prints the human-facing report.
func runArchive(slug string, force bool) error {
	result, err := archiveProject(slug, force)
	if err != nil {
		return err
	}
	renderArchive(os.Stdout, result)
	return nil
}

// renderArchive prints archive's long-standing text output, including the
// branch-deletion warning it has always written to stderr.
func renderArchive(out io.Writer, result archiveResult) {
	for _, warning := range result.Warnings {
		ui.Warn("%s", warning)
	}
	if result.BranchDeletionWarning != "" {
		ui.Warn("%s", result.BranchDeletionWarning)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Green, "Archived:"), result.Slug)
	if result.WorktreeRemoved {
		fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Dim, "Worktree removed:"), result.Worktree)
	}
	if result.BranchDeletionWarning != "" {
		fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Yellow, "Branch still present:"), result.Branch)
	}
	fmt.Fprintln(out)
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
	Slug                string
	Repository          string
	Branch              string
	Worktree            string
	HasWorktree         bool
	ManifestJSON        string
	Kind                archiveProofKind
	BranchPresent       bool
	ExpectedBranchTip   string
	WorktreePresent     bool
	ExpectedWorktreeTip string
	Merged              bool
}

type archiveDecision struct {
	proof    archiveProofSnapshot
	warnings []string
}

// archiveProject computes its own proof snapshot at the archive boundary.
func archiveProject(slug string, force bool) (archiveResult, error) {
	manifestPath := project.ManifestPath(project.ActiveDir(), slug)
	m, err := project.Load(manifestPath)
	if err != nil {
		return archiveResult{}, fmt.Errorf("project not found in active: %s: %w", slug, err)
	}
	decision, err := decideArchive(m, slug, force)
	if err != nil {
		return archiveResult{}, err
	}
	result, err := archiveProjectWithProof(decision.proof, force)
	if err != nil {
		return archiveResult{}, err
	}
	result.Warnings = append(result.Warnings, decision.warnings...)
	return result, nil
}

func decideArchive(m project.Manifest, slug string, force bool) (archiveDecision, error) {
	proof, err := newArchiveProofSnapshot(m, archiveProofForced, false)
	if err != nil {
		return archiveDecision{}, err
	}
	recordedMerge := recordedPullRequestMergeOnce(m, slug)
	reachable := false
	workMerged := false
	var warnings []string
	if proof.BranchPresent {
		base := m.BaseBranch
		if base == "" {
			base = gitx.DetectDefaultBranch(m.Repo)
		}
		if base != "" {
			for _, baseRef := range []string{
				"refs/remotes/origin/" + base,
				"refs/heads/" + base,
			} {
				if gitx.RevParse(m.Repo, baseRef) == "" {
					continue
				}
				reachable, err = gitx.CommitReachable(
					m.Repo, proof.ExpectedBranchTip, baseRef,
				)
				if err != nil {
					return archiveDecision{}, fmt.Errorf(
						"evaluate branch %q against %s: %w", m.Branch, baseRef, err,
					)
				}
				if !workMerged {
					tip, merged, mergeErr := gitx.WorkMergedTip(
						m.Repo, m.Branch, baseRef, m.StartSHA,
					)
					if mergeErr != nil {
						warnings = append(warnings, fmt.Sprintf(
							"evaluate merged work for branch %q against %s: %s",
							m.Branch, baseRef, mergeErr,
						))
					} else if tip == proof.ExpectedBranchTip {
						workMerged = merged
					}
				}
				if reachable {
					break
				}
			}
		}
		switch {
		case !force && !reachable:
			evidence, evidenceErr := recordedMerge()
			if evidenceErr != nil {
				return archiveDecision{}, fmt.Errorf(
					"branch %q has unmerged work; re-run with --force to delete it anyway, or merge it first; recorded pull request lookup failed: %w",
					m.Branch, evidenceErr,
				)
			}
			if !evidence.Merged {
				return archiveDecision{}, fmt.Errorf(
					"branch %q has unmerged work; re-run with --force to delete it anyway, or merge it first",
					m.Branch,
				)
			}
			if err := validatePullRequestProofTips(proof, evidence.HeadSHA); err != nil {
				return archiveDecision{}, err
			}
			proof.Kind = archiveProofPullRequest
			workMerged = true
		case force:
			proof.Kind = archiveProofForced
		default:
			proof.Kind = archiveProofReachable
		}
	} else {
		proof.Kind = archiveProofMissingBranch
	}

	if !workMerged {
		evidence, evidenceErr := recordedMerge()
		if evidenceErr != nil {
			warnings = append(warnings, evidenceErr.Error())
		} else if evidence.Merged {
			if err := validatePullRequestProofTips(proof, evidence.HeadSHA); err != nil {
				warnings = append(warnings, err.Error())
			} else {
				workMerged = true
				proof.Kind = archiveProofPullRequest
			}
		}
	}
	proof.Merged = workMerged
	return archiveDecision{proof: proof, warnings: warnings}, nil
}

func newArchiveProofSnapshot(
	m project.Manifest, kind archiveProofKind, merged bool,
) (archiveProofSnapshot, error) {
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
		proof.ExpectedWorktreeTip, proof.WorktreePresent, err = gitx.WorktreeHead(
			m.Repo, proof.Worktree,
		)
		if err != nil {
			return archiveProofSnapshot{}, fmt.Errorf(
				"resolve worktree HEAD for project %s: %w", m.Slug, err,
			)
		}
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
	if proof.WorktreePresent && proof.ExpectedWorktreeTip != expectedTip {
		return archiveProofSnapshot{}, fmt.Errorf(
			"worktree HEAD %s for project %s does not match branch %q tip %s",
			proof.ExpectedWorktreeTip, m.Slug, m.Branch, expectedTip,
		)
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
	return nil
}

func archiveProjectWithProof(proof archiveProofSnapshot, force bool) (archiveResult, error) {
	m, err := validateArchiveProof(proof)
	if err != nil {
		return archiveResult{}, err
	}
	result := archiveResult{
		Slug: proof.Slug, Branch: proof.Branch, Merged: proof.Merged, Warnings: []string{},
	}
	now := time.Now().UTC().Format(time.RFC3339)
	m.Status = "archived"
	m.Archived = &now
	m.Merged = proof.Merged

	srcDir := filepath.Join(project.ActiveDir(), proof.Slug)
	dstDir := filepath.Join(project.ArchivedDir(), proof.Slug)
	rollbackMetadata, err := stageArchivedProject(srcDir, dstDir, m)
	if err != nil {
		return archiveResult{}, err
	}
	result.ArchivedPath = dstDir

	if proof.HasWorktree && proof.Worktree != "" {
		result.Worktree = proof.Worktree
		if err := validateArchiveTips(proof); err != nil {
			if rollbackErr := rollbackMetadata(); rollbackErr != nil {
				err = fmt.Errorf("%w; rollback archive metadata: %v", err, rollbackErr)
			}
			return archiveResult{}, err
		}
		if err := gitx.WorktreeRemove(proof.Repository, proof.Worktree, force); err != nil {
			if rollbackErr := rollbackMetadata(); rollbackErr != nil {
				err = fmt.Errorf("%w; rollback archive metadata: %v", err, rollbackErr)
			}
			if !force {
				return archiveResult{}, fmt.Errorf(
					"%w\nhint: use --force to remove worktrees with untracked/modified files", err,
				)
			}
			return archiveResult{}, err
		}
		result.WorktreeRemoved = true
	}

	if proof.BranchPresent {
		if err := archiveForceDeleteBranchAt(
			proof.Repository, proof.Branch, proof.ExpectedBranchTip,
		); err != nil {
			result.BranchDeletionWarning = fmt.Sprintf(
				"%s\nhint: delete manually with: %s",
				err, manualBranchDeleteCommand(proof.Repository, proof.Branch),
			)
		} else {
			result.BranchDeleted = true
		}
	}
	return result, nil
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
	return nil
}

func archiveWorktreePointer(proof archiveProofSnapshot) *string {
	if !proof.HasWorktree {
		return nil
	}
	worktree := proof.Worktree
	return &worktree
}

func retryArchivedProjectCleanup(m project.Manifest) (archiveResult, error) {
	result := archiveResult{
		Slug:         m.Slug,
		Branch:       m.Branch,
		Merged:       m.Merged,
		ArchivedPath: filepath.Join(project.ArchivedDir(), m.Slug),
		Warnings:     []string{},
	}
	if m.Worktree != nil && *m.Worktree != "" {
		result.Worktree = *m.Worktree
		worktreePresent := pathExists(*m.Worktree)
		if err := gitx.WorktreeRemove(m.Repo, *m.Worktree, true); err != nil {
			return result, fmt.Errorf("finish archived worktree cleanup for %s: %w", m.Slug, err)
		}
		result.WorktreeRemoved = worktreePresent
	}
	if m.Branch == "" {
		return result, nil
	}
	branchExists, err := archiveBranchExists(m.Repo, m.Branch)
	if err != nil {
		return result, fmt.Errorf(
			"inspect archived branch %q for project %s: %w", m.Branch, m.Slug, err,
		)
	}
	if !branchExists {
		return result, nil
	}
	branchTip, found, err := gitx.LocalBranchTip(m.Repo, m.Branch)
	if err != nil {
		return result, fmt.Errorf(
			"resolve archived branch %q tip for project %s: %w", m.Branch, m.Slug, err,
		)
	}
	if !found {
		return result, nil
	}
	if err := archiveForceDeleteBranchAt(m.Repo, m.Branch, branchTip); err != nil {
		result.BranchDeletionWarning = fmt.Sprintf(
			"%s\nhint: delete manually with: %s",
			err, manualBranchDeleteCommand(m.Repo, m.Branch),
		)
		return result, nil
	}
	result.BranchDeleted = true
	return result, nil
}

func manualBranchDeleteCommand(repo, branch string) string {
	return "git -C " + shellQuote(repo) + " branch -D " + shellQuote(branch)
}

func stageArchivedProject(srcDir, dstDir string, m project.Manifest) (func() error, error) {
	manifestPath := filepath.Join(srcDir, "manifest.json")
	originalManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read active manifest for archive: %w", err)
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("stat active manifest for archive: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dstDir), 0755); err != nil {
		return nil, fmt.Errorf("create archived dir: %w", err)
	}
	if _, err := os.Lstat(dstDir); err == nil {
		return nil, fmt.Errorf("move project to archived: destination already exists: %s", dstDir)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect archive destination %s: %w", dstDir, err)
	}

	staged, err := os.CreateTemp(srcDir, ".manifest.archived-*")
	if err != nil {
		return nil, fmt.Errorf("stage archived manifest: %w", err)
	}
	stagedPath := staged.Name()
	if err := staged.Close(); err != nil {
		err = archiveCleanupError(err, archiveRemoveFile(stagedPath))
		return nil, fmt.Errorf("stage archived manifest: close temporary file: %w", err)
	}
	if err := saveArchiveManifest(stagedPath, m); err != nil {
		err = archiveCleanupError(err, archiveRemoveFile(stagedPath))
		return nil, fmt.Errorf("stage archived manifest: %w", err)
	}

	if err := archiveRename(srcDir, dstDir); err != nil {
		err = archiveCleanupError(err, archiveRemoveFile(stagedPath))
		return nil, fmt.Errorf("move project to archived: %w", err)
	}
	stagedPath = filepath.Join(dstDir, filepath.Base(stagedPath))
	archivedManifestPath := filepath.Join(dstDir, "manifest.json")
	if err := archiveRename(stagedPath, archivedManifestPath); err != nil {
		rollbackErr := archiveRename(dstDir, srcDir)
		if rollbackErr != nil {
			return nil, fmt.Errorf(
				"install archived manifest: %w; rollback project directory: %v", err, rollbackErr,
			)
		}
		err = archiveCleanupError(err, archiveRemoveFile(filepath.Join(srcDir, filepath.Base(stagedPath))))
		return nil, fmt.Errorf("install archived manifest: %w", err)
	}

	rollback := func() error {
		rollbackPath := filepath.Join(dstDir, ".manifest.active")
		if err := os.WriteFile(rollbackPath, originalManifest, info.Mode().Perm()); err != nil {
			return fmt.Errorf("stage active manifest: %w", err)
		}
		if err := archiveRename(rollbackPath, archivedManifestPath); err != nil {
			err = archiveCleanupError(err, archiveRemoveFile(rollbackPath))
			return fmt.Errorf("restore active manifest: %w", err)
		}
		if err := archiveRename(dstDir, srcDir); err != nil {
			return fmt.Errorf("restore active project directory: %w", err)
		}
		return nil
	}
	return rollback, nil
}

func archiveCleanupError(cause, cleanupErr error) error {
	if cleanupErr == nil || os.IsNotExist(cleanupErr) {
		return cause
	}
	return fmt.Errorf("%w; remove staged manifest: %v", cause, cleanupErr)
}
