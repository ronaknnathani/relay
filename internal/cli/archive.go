package cli

import (
	"fmt"
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

func runArchive(slug string, force bool) error {
	srcDir := filepath.Join(project.ActiveDir(), slug)
	manifestPath := filepath.Join(srcDir, "manifest.json")
	m, err := project.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("project not found in active: %s: %w", slug, err)
	}

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
			return fmt.Errorf("branch %q has unmerged work; re-run with --force to delete it anyway, or merge it first", m.Branch)
		}
	}
	// A missing local branch is not evidence of abandoned work: the branch is
	// usually already deleted precisely because its pull request merged.
	workMerged = workMerged || recordedMerged()

	if m.Worktree != nil && *m.Worktree != "" {
		worktree := *m.Worktree
		if err := gitx.WorktreeRemove(m.Repo, worktree, force); err != nil {
			if !force {
				return fmt.Errorf("%w\nhint: use --force to remove worktrees with untracked/modified files", err)
			}
			return err
		}
	}

	var branchDeleteErr error
	if deleteBranchAfter {
		if forceDeleteBranchAfter {
			branchDeleteErr = gitx.ForceDeleteBranch(m.Repo, m.Branch)
		} else {
			branchDeleteErr = gitx.DeleteBranch(m.Repo, m.Branch)
		}
		if branchDeleteErr != nil {
			ui.Warn("%s\nhint: delete manually with 'git branch -D %s'", branchDeleteErr, m.Branch)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	m.Status = "archived"
	m.Archived = &now
	m.Merged = workMerged

	dstDir := filepath.Join(project.ArchivedDir(), slug)
	if err := os.MkdirAll(project.ArchivedDir(), 0755); err != nil {
		return fmt.Errorf("create archived dir: %w", err)
	}
	if err := os.Rename(srcDir, dstDir); err != nil {
		return fmt.Errorf("move project to archived: %w", err)
	}
	if err := project.Save(filepath.Join(dstDir, "manifest.json"), m); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("  %s %s\n", ui.Color(ui.Green, "Archived:"), slug)
	if m.Worktree != nil {
		fmt.Printf("  %s %s\n", ui.Color(ui.Dim, "Worktree removed:"), *m.Worktree)
	}
	if branchDeleteErr != nil {
		fmt.Printf("  %s %s\n", ui.Color(ui.Yellow, "Branch still present:"), m.Branch)
	}
	fmt.Println()
	return nil
}
