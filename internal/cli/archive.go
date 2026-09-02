package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/programview"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

// loadArchivePRIndex resolves authoritative pull request state for archive.
var loadArchivePRIndex = programview.GitHubPRIndex

// recordedPullRequestMerged reports whether the project's recorded pull request
// is merged on GitHub. An unavailable or failing GitHub stays conservative.
func recordedPullRequestMerged(m project.Manifest, slug string) bool {
	hasPR, ref, err := programview.RecordedPR(m, project.StatePath(slug))
	if err != nil || !hasPR {
		return false
	}
	index := loadArchivePRIndex(m.Repo, []string{ref})
	if index == nil {
		return false
	}
	state, found := index.Lookup(ref)
	return found && state == programview.PRStateMerged
}

// recordedPullRequestMergedOnce memoizes recordedPullRequestMerged so archive
// asks GitHub at most once per run, and only when the answer is still needed.
func recordedPullRequestMergedOnce(m project.Manifest, slug string) func() bool {
	var (
		resolved bool
		merged   bool
	)
	return func() bool {
		if !resolved {
			resolved, merged = true, recordedPullRequestMerged(m, slug)
		}
		return merged
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
		workMerged             bool
	)
	// The recorded pull request is authoritative about merge state and is
	// resolved lazily, at most once, so a locally merged branch costs no GitHub
	// call and a branch that is already deleted is still evaluated.
	recordedMerged := recordedPullRequestMergedOnce(m, slug)
	if m.Branch != "" && gitx.BranchExists(m.Repo, m.Branch) {
		base := m.BaseBranch
		if base == "" {
			base = gitx.DetectDefaultBranch(m.Repo)
		}
		reachable := false
		if base != "" {
			if gitx.HasOrigin(m.Repo) && gitx.RevParse(m.Repo, "origin/"+base) != "" {
				reachable = gitx.IsBranchReachable(m.Repo, m.Branch, "origin/"+base)
				workMerged = gitx.IsWorkMerged(m.Repo, m.Branch, "origin/"+base, m.StartSHA)
			}
			if !reachable {
				reachable = gitx.IsBranchReachable(m.Repo, m.Branch, base)
			}
			if !workMerged {
				workMerged = gitx.IsWorkMerged(m.Repo, m.Branch, base, m.StartSHA)
			}
		}
		// A squashed or rebased merge leaves no local ancestry, so trust the
		// recorded pull request when GitHub reports it merged.
		pullRequestMerged := !workMerged && recordedMerged()
		switch {
		case reachable:
			deleteBranchAfter = true
		case pullRequestMerged, force:
			deleteBranchAfter, forceDeleteBranchAfter = true, true
		default:
			return archiveResult{}, fmt.Errorf("branch %q has unmerged work; re-run with --force to delete it anyway, or merge it first", m.Branch)
		}
	}
	// A missing local branch is not evidence of abandoned work: the branch is
	// usually already deleted precisely because its pull request merged.
	workMerged = workMerged || recordedMerged()

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
