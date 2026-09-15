package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

var (
	errGCCompletedWithErrors = errors.New("relay gc completed with errors")
	gcArchiveProject         = archiveMergedProject
)

type gcRefreshKey struct {
	repo string
	base string
}

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
	loadResults, err := project.LoadAllResults(project.ActiveDir())
	if err != nil {
		return err
	}
	refreshErrors := make(map[gcRefreshKey]error)
	hadErrors := false
	for _, loadResult := range loadResults {
		if loadResult.Err != nil {
			ui.Warn("load project metadata %s: %s", loadResult.Path, loadResult.Err)
			hadErrors = true
			continue
		}
		m := loadResult.Manifest
		if m.Program != "" || m.ProgramItem != "" {
			programSlug, itemID := m.Program, m.ProgramItem
			if programSlug == "" {
				programSlug = "<program>"
			}
			if itemID == "" {
				itemID = "<item>"
			}
			fmt.Printf(
				"[relay] Skipping %s: managed by a program; use 'relay program worker cleanup %s %s'.\n",
				loadResult.Name, programSlug, itemID,
			)
			continue
		}
		if err := validateGCManifest(loadResult); err != nil {
			ui.Warn("%s", err)
			hadErrors = true
			continue
		}
		base := m.BaseBranch
		if base == "" {
			base = gitx.DetectDefaultBranch(m.Repo)
		}
		if base == "" {
			ui.Warn("evaluate project %s in %s: cannot determine default branch", m.Slug, m.Repo)
			hadErrors = true
			continue
		}
		key := gcRefreshKey{repo: m.Repo, base: base}
		refreshErr, found := refreshErrors[key]
		if !found {
			diagnostic, err := gitx.Fetch(m.Repo, base)
			refreshErr = err
			if refreshErr != nil && diagnostic != "" {
				refreshErr = fmt.Errorf("%w\n%s", refreshErr, diagnostic)
			}
			refreshErrors[key] = refreshErr
			if refreshErr != nil {
				ui.Warn(
					"refresh repository %s base %s: %s",
					m.Repo, base, refreshErr,
				)
			}
		}

		var merged bool
		var evaluationErr error
		if refreshErr == nil {
			if m.StartSHA == "" {
				evaluationErr = fmt.Errorf("project %s has no start_sha", m.Slug)
			} else {
				merged, evaluationErr = gitx.WorkMerged(
					m.Repo, m.Branch, "origin/"+base, m.StartSHA,
				)
			}
		}
		var prErr error
		if !merged {
			merged, prErr = resolveRecordedPullRequestMerge(m, m.Slug)
		}
		if !merged {
			if refreshErr != nil {
				hadErrors = true
			}
			for _, err := range []error{evaluationErr, prErr} {
				if err == nil {
					continue
				}
				ui.Warn("evaluate project %s: %s", m.Slug, err)
				hadErrors = true
			}
			continue
		}
		fmt.Printf("[relay] Branch %s is merged. Archiving project %s.\n", m.Branch, m.Slug)
		result, err := gcArchiveProject(m.Slug, true)
		if err != nil {
			ui.Warn("archive %s: %s", m.Slug, err)
			hadErrors = true
			continue
		}
		renderArchive(os.Stdout, result)
		if len(result.Warnings) > 0 {
			hadErrors = true
		}
	}
	if hadErrors {
		return errGCCompletedWithErrors
	}
	return nil
}

func validateGCManifest(result project.ManifestLoadResult) error {
	m := result.Manifest
	if err := project.ValidateSlug(m.Slug); err != nil {
		return fmt.Errorf("invalid project metadata %s: %w", result.Path, err)
	}
	if m.Slug != result.Name {
		return fmt.Errorf(
			"invalid project metadata %s: slug %q does not match directory %q",
			result.Path, m.Slug, result.Name,
		)
	}
	if m.Repo == "" {
		return fmt.Errorf("invalid project metadata %s: repository is empty", result.Path)
	}
	if m.Branch == "" {
		return fmt.Errorf("invalid project metadata %s: branch is empty", result.Path)
	}
	return nil
}
