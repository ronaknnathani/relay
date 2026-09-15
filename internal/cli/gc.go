package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

var (
	errGCCompletedWithErrors = errors.New("relay gc completed with errors")
	gcArchiveProject         = archiveProject
)

type gcRefreshKey struct {
	repo string
	base string
}

type gcRefreshResult struct {
	diagnostic string
	err        error
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
	refreshes := make(map[gcRefreshKey]gcRefreshResult)
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
		refresh, found := refreshes[key]
		if !found {
			refresh.diagnostic, refresh.err = gitx.Fetch(m.Repo, base)
			refreshes[key] = refresh
			if refresh.err != nil {
				ui.Warn(
					"refresh repository %s base %s: %s",
					m.Repo, base, formatGCRefreshError(refresh),
				)
			}
		}

		merged := false
		var evaluationErr error
		if refresh.err == nil {
			if m.StartSHA == "" {
				evaluationErr = fmt.Errorf("project %s has no start_sha", m.Slug)
			} else {
				merged, evaluationErr = gitx.WorkMerged(
					m.Repo, m.Branch, "origin/"+base, m.StartSHA,
				)
			}
		}
		if !merged {
			var prErr error
			merged, prErr = resolveRecordedPullRequestMerge(m, m.Slug)
			if !merged {
				if refresh.err != nil {
					hadErrors = true
				}
				if evaluationErr != nil {
					ui.Warn("evaluate project %s: %s", m.Slug, evaluationErr)
					hadErrors = true
				}
				if prErr != nil {
					ui.Warn("evaluate project %s: %s", m.Slug, prErr)
					hadErrors = true
				}
			}
		}
		if !merged {
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

func formatGCRefreshError(refresh gcRefreshResult) string {
	if refresh.diagnostic == "" {
		return refresh.err.Error()
	}
	return refresh.err.Error() + "\n" + strings.TrimSpace(refresh.diagnostic)
}
