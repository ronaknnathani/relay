package cli

import (
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
)

func resolveRecordedPullRequestMerge(m project.Manifest, slug string) (bool, error) {
	hasPR, ref, err := programview.RecordedPR(m, project.StatePath(slug))
	if err != nil {
		return false, fmt.Errorf("resolve recorded pull request for %s: %w", slug, err)
	}
	if !hasPR {
		return false, nil
	}
	safeRef := gitx.SanitizeDiagnostic(ref)
	proof, err := loadArchivePullRequestProof(m.Repo, ref)
	if err != nil {
		return false, fmt.Errorf("lookup recorded pull request %s for %s: %w", safeRef, slug, err)
	}
	if proof.State != programview.PRStateMerged {
		return false, nil
	}
	repository, err := loadArchiveRepository(m.Repo)
	if err != nil {
		return false, fmt.Errorf("resolve repository for recorded pull request %s for %s: %w", safeRef, slug, err)
	}
	if !strings.EqualFold(proof.Repository, repository) {
		return false, fmt.Errorf(
			"recorded pull request %s for %s belongs to repository %q, want %q",
			safeRef, slug, proof.Repository, repository,
		)
	}
	branchTip, found, err := gitx.LocalBranchTip(m.Repo, m.Branch)
	if err != nil {
		return false, fmt.Errorf("resolve branch %q tip for recorded pull request %s for %s: %w", m.Branch, safeRef, slug, err)
	}
	if !found {
		if proof.HeadBranch != m.Branch {
			return false, fmt.Errorf(
				"recorded pull request %s for %s head branch %q does not match manifest branch %q",
				safeRef, slug, proof.HeadBranch, m.Branch,
			)
		}
		if m.Worktree != nil && *m.Worktree != "" {
			worktreeHead, worktreeFound, err := gitx.WorktreeHead(*m.Worktree)
			if err != nil {
				return false, fmt.Errorf(
					"resolve worktree HEAD for recorded pull request %s for %s: %w",
					safeRef, slug, err,
				)
			}
			if worktreeFound {
				if proof.HeadSHA == "" {
					return false, fmt.Errorf("recorded pull request %s for %s has no head SHA", safeRef, slug)
				}
				if worktreeHead != proof.HeadSHA {
					return false, fmt.Errorf(
						"recorded pull request %s for %s head %s does not match worktree HEAD %s",
						safeRef, slug, proof.HeadSHA, worktreeHead,
					)
				}
			}
		}
		return true, nil
	}
	if proof.HeadSHA == "" {
		return false, fmt.Errorf("recorded pull request %s for %s has no head SHA", safeRef, slug)
	}
	if branchTip != proof.HeadSHA {
		return false, fmt.Errorf(
			"recorded pull request %s for %s head %s does not match branch %q tip %s",
			safeRef, slug, proof.HeadSHA, m.Branch, branchTip,
		)
	}
	return true, nil
}

// recordedPullRequestMerged reports whether the project's recorded pull request
// is merged on GitHub. An unavailable or failing GitHub stays conservative.
func recordedPullRequestMerged(m project.Manifest, slug string) bool {
	merged, _ := resolveRecordedPullRequestMerge(m, slug)
	return merged
}

// recordedPullRequestMergeOnce memoizes the detailed lookup so archive asks
// GitHub at most once per run, and only when the answer is still needed.
func recordedPullRequestMergeOnce(m project.Manifest, slug string) func() (bool, error) {
	var (
		resolved bool
		merged   bool
		err      error
	)
	return func() (bool, error) {
		if !resolved {
			resolved = true
			merged, err = resolveRecordedPullRequestMerge(m, slug)
		}
		return merged, err
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
	Slug            string   `json:"slug"`
	Worktree        string   `json:"worktree,omitempty"`
	WorktreeRemoved bool     `json:"worktree_removed"`
	Branch          string   `json:"branch,omitempty"`
	BranchDeleted   bool     `json:"branch_deleted"`
	Merged          bool     `json:"merged"`
	ArchivedPath    string   `json:"archived_path"`
	Warnings        []string `json:"warnings"`
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
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Green, "Archived:"), result.Slug)
	if result.WorktreeRemoved {
		fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Dim, "Worktree removed:"), result.Worktree)
	}
	if !result.BranchDeleted && result.Branch != "" && len(result.Warnings) > 0 {
		fmt.Fprintf(out, "  %s %s\n", ui.Color(ui.Yellow, "Branch still present:"), result.Branch)
	}
	fmt.Fprintln(out)
}

// archiveProject removes a project's worktree, decides its branch's fate, and
// moves its metadata from active to archived. With force it deliberately
// discards dirty and untracked files in the worktree and force-deletes an
// unmerged branch: the caller has already decided that work is finished.
func archiveProject(slug string, force bool) (archiveResult, error) {
	return archiveProjectWithMergeProof(slug, force, false)
}

func archiveMergedProject(slug string, force bool) (archiveResult, error) {
	return archiveProjectWithMergeProof(slug, force, true)
}

func archiveProjectWithMergeProof(slug string, force, mergeProven bool) (archiveResult, error) {
	srcDir := filepath.Join(project.ActiveDir(), slug)
	manifestPath := filepath.Join(srcDir, "manifest.json")
	m, err := project.Load(manifestPath)
	if err != nil {
		return archiveResult{}, fmt.Errorf("project not found in active: %s: %w", slug, err)
	}
	result := archiveResult{Slug: slug, Branch: m.Branch, Warnings: []string{}}

	// Decide branch fate up front so we don't tear down the worktree and
	// then fail on an unmerged branch with no recovery path.
	var (
		deleteBranchAfter      bool
		forceDeleteBranchAfter bool
		workMerged             = mergeProven
		branchExists           = m.Branch != "" && gitx.BranchExists(m.Repo, m.Branch)
	)
	// The recorded pull request is authoritative about merge state and is
	// resolved lazily, at most once, so a locally merged branch costs no GitHub
	// call and a branch that is already deleted is still evaluated.
	recordedMerge := recordedPullRequestMergeOnce(m, slug)
	if branchExists {
		base := m.BaseBranch
		if base == "" {
			base = gitx.DetectDefaultBranch(m.Repo)
		}
		reachable := false
		if base != "" {
			if gitx.HasOrigin(m.Repo) && gitx.RevParse(m.Repo, "origin/"+base) != "" {
				reachable = gitx.IsBranchReachable(m.Repo, m.Branch, "origin/"+base)
				workMerged = workMerged || gitx.IsWorkMerged(m.Repo, m.Branch, "origin/"+base, m.StartSHA)
			}
			if !reachable {
				reachable = gitx.IsBranchReachable(m.Repo, m.Branch, base)
			}
			if !workMerged {
				workMerged = gitx.IsWorkMerged(m.Repo, m.Branch, base, m.StartSHA)
			}
		}
		switch {
		case force:
			deleteBranchAfter, forceDeleteBranchAfter = true, true
		case reachable:
			deleteBranchAfter = true
		default:
			// A squashed or rebased merge leaves no local ancestry, so trust
			// the recorded pull request when its identity matches the branch.
			pullRequestMerged, err := recordedMerge()
			if err != nil {
				return archiveResult{}, err
			}
			if !pullRequestMerged {
				return archiveResult{}, fmt.Errorf("branch %q has unmerged work; re-run with --force to delete it anyway, or merge it first", m.Branch)
			}
			deleteBranchAfter, forceDeleteBranchAfter = true, true
		}
	}
	// A missing local branch is not evidence of abandoned work: the branch is
	// usually already deleted precisely because its pull request merged.
	if !workMerged {
		pullRequestMerged, err := recordedMerge()
		if err != nil {
			if !force && branchExists {
				return archiveResult{}, err
			}
			result.Warnings = append(result.Warnings, err.Error())
		} else {
			workMerged = pullRequestMerged
		}
	}

	if m.Worktree != nil && *m.Worktree != "" {
		worktree := *m.Worktree
		result.Worktree = worktree
		if err := gitx.WorktreeRemove(m.Repo, worktree, force); err != nil {
			if !force {
				return archiveResult{}, fmt.Errorf("%w\nhint: use --force to remove worktrees with untracked/modified files", err)
			}
			return archiveResult{}, err
		}
		result.WorktreeRemoved = true
	}

	if deleteBranchAfter {
		var branchDeleteErr error
		if forceDeleteBranchAfter {
			branchDeleteErr = gitx.ForceDeleteBranch(m.Repo, m.Branch)
		} else {
			branchDeleteErr = gitx.DeleteBranch(m.Repo, m.Branch)
		}
		if branchDeleteErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"%s\nhint: delete manually with 'git branch -D %s'", branchDeleteErr, m.Branch,
			))
		} else {
			result.BranchDeleted = true
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	m.Status = "archived"
	m.Archived = &now
	m.Merged = workMerged
	result.Merged = workMerged

	dstDir := filepath.Join(project.ArchivedDir(), slug)
	if err := os.MkdirAll(project.ArchivedDir(), 0755); err != nil {
		return archiveResult{}, fmt.Errorf("create archived dir: %w", err)
	}
	if err := os.Rename(srcDir, dstDir); err != nil {
		return archiveResult{}, fmt.Errorf("move project to archived: %w", err)
	}
	if err := project.Save(filepath.Join(dstDir, "manifest.json"), m); err != nil {
		return archiveResult{}, err
	}
	result.ArchivedPath = dstDir
	return result, nil
}
