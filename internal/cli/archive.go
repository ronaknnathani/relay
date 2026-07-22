package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

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
	)
	if m.Branch != "" && gitx.BranchExists(m.Repo, m.Branch) {
		base := m.BaseBranch
		if base == "" {
			base = gitx.DetectDefaultBranch(m.Repo)
		}
		merged := false
		if base != "" {
			if gitx.HasOrigin(m.Repo) && gitx.RevParse(m.Repo, "origin/"+base) != "" {
				merged = gitx.IsBranchReachable(m.Repo, m.Branch, "origin/"+base)
			}
			if !merged {
				merged = gitx.IsBranchReachable(m.Repo, m.Branch, base)
			}
		}
		switch {
		case merged:
			deleteBranchAfter = true
		case force:
			deleteBranchAfter, forceDeleteBranchAfter = true, true
		default:
			return fmt.Errorf("branch %q has unmerged work; re-run with --force to delete it anyway, or merge it first", m.Branch)
		}
	}

	if m.Worktree != nil && *m.Worktree != "" {
		worktree := *m.Worktree
		safeRelayAgentsOnly := false
		if !force {
			// Classifier failures fall through to normal removal to preserve the
			// existing worktree error and --force hint.
			if safe, err := gitx.WorktreeHasOnlyRelayGeneratedAgentsMD(worktree); err == nil {
				safeRelayAgentsOnly = safe
			}
		}
		if err := gitx.WorktreeRemove(m.Repo, worktree, force || safeRelayAgentsOnly); err != nil {
			if !force && !safeRelayAgentsOnly {
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
