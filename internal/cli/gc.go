package cli

import (
	"fmt"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

func newCmdGC() *cobra.Command {
	return &cobra.Command{
		Use:   "gc",
		Short: "Archive projects whose branches have been merged",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runGC()
		},
	}
}

func runGC() error {
	manifests, err := project.LoadAll(project.ActiveDir())
	if err != nil {
		return err
	}
	for _, m := range manifests {
		if m.Repo == "" || m.Branch == "" {
			continue
		}
		base := m.BaseBranch
		if base == "" {
			base = gitx.DetectDefaultBranch(m.Repo)
		}
		if base == "" {
			fmt.Printf("[relay] Skipping %s: cannot determine default branch.\n", m.Slug)
			continue
		}
		if _, err := gitx.Fetch(m.Repo, base); err != nil {
			ui.Warn("fetch %s in %s: %s", base, m.Repo, err)
		}
		if !gitx.IsWorkMerged(m.Repo, m.Branch, base, m.StartSHA) {
			continue
		}
		fmt.Printf("[relay] Branch %s is merged. Archiving project %s.\n", m.Branch, m.Slug)
		if err := runArchive(m.Slug, true); err != nil {
			ui.Warn("archive %s: %s", m.Slug, err)
		}
	}
	return nil
}
