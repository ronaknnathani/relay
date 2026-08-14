package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/spf13/cobra"
)

type programCanOpenPROutput struct {
	Program  string           `json:"program"`
	Item     string           `json:"item"`
	Allowed  bool             `json:"allowed"`
	Capacity program.Capacity `json:"capacity"`
}

func newCmdProgramCanOpenPR() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "can-open-pr <program> <item>",
		Short: "Check whether a managed worker may open a pull request",
		Long: "Check the current read-only PR capacity gate for a managed work item. " +
			"V1 is advisory and does not reserve capacity atomically across processes; a future controller will own atomic reservation.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramCanOpenPR(cmd.OutOrStdout(), args[0], args[1], jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output success and capacity as JSON")
	return cmd
}

// runProgramCanOpenPR is intentionally read-only. V1 makes no cross-process
// reservation; a future controller will own the atomic capacity decision.
func runProgramCanOpenPR(out io.Writer, programSlug, itemID string, jsonOutput bool) error {
	path, p, err := loadActiveProgram(programSlug)
	if err != nil {
		return err
	}
	if p.State != program.StateActive {
		return fmt.Errorf("can-open-pr for item %q: program %q is %s, want active", itemID, p.Slug, p.State)
	}
	item, ok := p.Item(itemID)
	if !ok {
		return fmt.Errorf("can-open-pr for item %q: item not found", itemID)
	}
	switch item.Status {
	case program.ItemDispatched, program.ItemInReview:
	default:
		return fmt.Errorf("can-open-pr for item %q: status %q is not dispatched or in-review", itemID, item.Status)
	}
	if item.ProjectSlug == "" {
		return fmt.Errorf("can-open-pr for item %q: managed project is not linked", itemID)
	}
	if decisions := p.OpenProgramDecisions(); len(decisions) > 0 {
		return fmt.Errorf("can-open-pr for item %q: unresolved program decision(s): %s",
			itemID, decisionIDs(decisions))
	}
	if decisions := p.OpenItemDecisions(itemID); len(decisions) > 0 {
		return fmt.Errorf("can-open-pr for item %q: unresolved item decision(s): %s",
			itemID, decisionIDs(decisions))
	}
	if err := p.VerifyHashes(filepath.Dir(path)); err != nil {
		return err
	}
	views, err := buildProgramProjectViews(p)
	if err != nil {
		return err
	}
	capacity := p.Plan(views).Capacity
	for _, view := range views {
		if item.Status == program.ItemInReview &&
			view.Slug == item.ProjectSlug && view.HasPR && !view.Merged && !view.Archived {
			return printCanOpenPR(out, p.Slug, item.ID, capacity, jsonOutput)
		}
	}
	if capacity.Available == 0 {
		return fmt.Errorf(
			"open PR capacity is full for program %q: %d/%d open; stop before open-pr and resume when capacity is available",
			p.Slug, capacity.Open, capacity.Limit,
		)
	}
	return printCanOpenPR(out, p.Slug, item.ID, capacity, jsonOutput)
}

func printCanOpenPR(out io.Writer, programSlug, itemID string, capacity program.Capacity, jsonOutput bool) error {
	result := programCanOpenPROutput{
		Program:  programSlug,
		Item:     itemID,
		Allowed:  true,
		Capacity: capacity,
	}
	if jsonOutput {
		return writeProgramJSON(out, result)
	}
	fmt.Fprintf(out, "can open PR: %d/%d open, %d available\n",
		capacity.Open, capacity.Limit, capacity.Available)
	return nil
}

func decisionIDs(decisions []program.Decision) string {
	ids := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		ids = append(ids, decision.ID)
	}
	return strings.Join(ids, ", ")
}
