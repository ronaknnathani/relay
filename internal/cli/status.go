package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/ronaknnathani/relay/internal/dashboard"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

type statusOpts struct {
	slug         string
	showArchived bool
	showAll      bool
	jsonOutput   bool
}

func newCmdStatus() *cobra.Command {
	var opts statusOpts
	cmd := &cobra.Command{
		Use:   "status [slug]",
		Short: "Show project status",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.slug = args[0]
			}
			return runStatus(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.showArchived, "archived", false, "show archived projects")
	cmd.Flags().BoolVar(&opts.showAll, "all", false, "show both active and archived")
	cmd.Flags().BoolVar(&opts.jsonOutput, "json", false, "output as JSON")
	return cmd
}

func runStatus(opts statusOpts) error {
	if opts.slug != "" {
		return runDetail(opts.slug, opts.jsonOutput)
	}
	return runList(opts.showArchived, opts.showAll, opts.jsonOutput)
}

func runList(showArchived, showAll, jsonOutput bool) error {
	active, err := project.LoadAll(project.ActiveDir())
	if err != nil {
		return err
	}
	var archived []project.Manifest
	if showArchived || showAll || jsonOutput {
		archived, err = project.LoadAll(project.ArchivedDir())
		if err != nil {
			return err
		}
	}

	if jsonOutput {
		fmt.Println(string(dashboard.Marshal(active, archived, time.Now())))
		if err := dashboard.Write(dashboard.RepoDirFromExe(), active, archived); err != nil {
			ui.Warn("could not write dashboard data: %s", err)
		}
		return nil
	}

	if showAll || !showArchived {
		printTable(active, "Active Projects")
	}
	if showAll || showArchived {
		printTable(archived, "Archived Projects")
	}
	if !showArchived && !showAll && len(active) == 0 {
		fmt.Println()
		fmt.Printf("  %s\n", ui.Color(ui.Dim, "No active projects. Start one with: relay \"<task>\""))
		fmt.Println()
	}
	return nil
}

func runDetail(slug string, jsonOutput bool) error {
	path, err := project.Find(slug)
	if err != nil {
		return err
	}
	if jsonOutput {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read manifest %s: %w", path, err)
		}
		fmt.Print(string(data))
		return nil
	}
	m, err := project.Load(path)
	if err != nil {
		return err
	}

	fmt.Println()
	ui.PrintField("Project", m.Slug)
	ui.PrintField("Title", orDash(m.Title))
	ui.PrintField("Status", ui.StatusColor(m.Status))
	ui.PrintField("Phase", ui.PhaseColor(m.Phase))
	ui.PrintField("Branch", m.Branch)
	ui.PrintField("Repo", orDash(m.Repo))
	ui.PrintField("Worktree", ptrOrDash(m.Worktree))
	ui.PrintField("Created", m.Created)
	ui.PrintField("Updated", m.Updated)
	if m.PR.Number != nil {
		url := ""
		if m.PR.URL != nil {
			url = " " + *m.PR.URL
		}
		ui.PrintField("PR", fmt.Sprintf("#%d%s", *m.PR.Number, url))
	}
	if m.PR.CIStatus != nil {
		ui.PrintField("CI", *m.PR.CIStatus)
	}
	if m.Archived != nil {
		ui.PrintField("Archived", *m.Archived)
	}
	fmt.Println()
	ui.PrintField("Phases completed", strings.Join(m.PhasesCompleted, ", "))
	ui.PrintField("Phases remaining", strings.Join(m.PhasesRemaining, ", "))
	fmt.Println()
	return nil
}

func printTable(manifests []project.Manifest, label string) {
	if len(manifests) == 0 {
		return
	}

	sorted := make([]project.Manifest, len(manifests))
	copy(sorted, manifests)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, rj := repoName(sorted[i].Repo), repoName(sorted[j].Repo)
		if ri != rj {
			return ri < rj
		}
		// Newer first within a repo; fall back to lexicographic on parse error.
		ti, errI := time.Parse(time.RFC3339, sorted[i].Updated)
		tj, errJ := time.Parse(time.RFC3339, sorted[j].Updated)
		if errI != nil || errJ != nil {
			return sorted[i].Updated > sorted[j].Updated
		}
		return ti.After(tj)
	})

	type row struct{ repo, session, path, age, status string }
	rows := make([]row, 0, len(sorted))
	for _, m := range sorted {
		rows = append(rows, row{
			repo:    repoName(m.Repo),
			session: m.Slug,
			path:    ptrOrDash(m.Worktree),
			age:     ui.RelativeTime(m.Updated),
			status:  m.Status,
		})
	}

	repoW, sessionW, ageW, statusW := len("REPO"), len("SESSION"), len("AGE"), len("STATUS")
	for _, r := range rows {
		if w := len(r.repo); w > repoW {
			repoW = w
		}
		if w := len(r.session); w > sessionW {
			sessionW = w
		}
		if w := len(r.age); w > ageW {
			ageW = w
		}
		if w := len(r.status); w > statusW {
			statusW = w
		}
	}
	// Borders + padding overhead: 6 vertical bars + 5 columns * 2 padding = 16.
	const borderOverhead = 16
	pathW := max(ui.TerminalWidth()-repoW-sessionW-ageW-statusW-borderOverhead, 30)

	pathMax := tw.NewMapper[int, int]().Set(2, pathW) // column index 2 = PATH
	cfg := tablewriter.Config{
		Header: tw.CellConfig{
			Alignment: tw.CellAlignment{Global: tw.AlignLeft},
		},
		Row: tw.CellConfig{
			Alignment:    tw.CellAlignment{Global: tw.AlignLeft},
			ColMaxWidths: tw.CellWidth{PerColumn: pathMax},
			Formatting:   tw.CellFormatting{AutoWrap: tw.WrapNormal},
			Merging:      tw.CellMerging{Mode: tw.MergeHierarchical},
		},
	}
	rendition := tw.Rendition{
		Symbols: tw.NewSymbols(tw.StyleLight),
		Borders: tw.Border{Top: tw.On, Bottom: tw.On, Left: tw.On, Right: tw.On},
		Settings: tw.Settings{
			Separators: tw.Separators{BetweenColumns: tw.On, BetweenRows: tw.On},
		},
	}

	fmt.Println()
	fmt.Printf("  %s\n", ui.Color(ui.Bold+ui.White, label))

	table := tablewriter.NewTable(os.Stdout,
		tablewriter.WithConfig(cfg),
		tablewriter.WithRenderer(renderer.NewBlueprint(rendition)),
	)
	table.Header("REPO", "SESSION", "PATH", "AGE", "STATUS")
	for _, r := range rows {
		if err := table.Append(r.repo, r.session, r.path, r.age, ui.StatusColor(r.status)); err != nil {
			ui.Warn("append row: %s", err)
			continue
		}
	}
	if err := table.Render(); err != nil {
		ui.Warn("render table: %s", err)
	}
	fmt.Println()
}

func repoName(repo string) string {
	if repo == "" {
		return "-"
	}
	return filepath.Base(repo)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func ptrOrDash(p *string) string {
	if p == nil || *p == "" {
		return "-"
	}
	return *p
}
