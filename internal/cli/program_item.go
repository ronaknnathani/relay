package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/spf13/cobra"
)

func newCmdProgramItem() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "item",
		Short: "Manage governed program work items",
	}
	cmd.AddCommand(
		newCmdProgramItemAdd(),
		newCmdProgramItemList(),
		newCmdProgramItemUpdate(),
		newCmdProgramItemBlock(),
		newCmdProgramItemUnblock(),
		newCmdProgramItemCancel(),
		newCmdProgramItemLink(),
	)
	return cmd
}

func newCmdProgramItemAdd() *cobra.Command {
	var priorityText, dependenciesText, contractsText string
	var notes []string
	cmd := &cobra.Command{
		Use:   "add <program> <title>",
		Short: "Add a work item",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requirePlanShapingTurn("relay program item add"); err != nil {
				return err
			}
			dependencies, err := parseProgramCSV(dependenciesText, "dependency")
			if err != nil {
				return err
			}
			contracts, err := parseProgramCSV(contractsText, "contract")
			if err != nil {
				return err
			}
			return runProgramItemAdd(cmd.OutOrStdout(), args[0], program.WorkItem{
				Title:        strings.Join(args[1:], " "),
				Priority:     program.Priority(priorityText),
				Dependencies: dependencies,
				ContractRefs: contracts,
				Notes:        notes,
			})
		},
	}
	cmd.Flags().StringVar(&priorityText, "priority", string(program.PriorityP2), "priority (P0, P1, P2, or P3)")
	cmd.Flags().StringVar(&dependenciesText, "depends-on", "", "comma-separated dependency item IDs")
	cmd.Flags().StringVar(&contractsText, "contract", "", "comma-separated contract references")
	cmd.Flags().StringArrayVar(&notes, "notes", nil, "note to append (repeatable)")
	return cmd
}

func runProgramItemAdd(out io.Writer, slug string, candidate program.WorkItem) error {
	path, p, err := loadActiveProgram(slug)
	if err != nil {
		return err
	}
	item, err := p.AddItem(candidate)
	if err != nil {
		return err
	}
	if err := saveProgramMutation(path, p, fmt.Sprintf("Added item %s: %s", item.ID, item.Title)); err != nil {
		return err
	}
	fmt.Fprintln(out, item.ID)
	return nil
}

func newCmdProgramItemList() *cobra.Command {
	var jsonOutput bool
	var status string
	cmd := &cobra.Command{
		Use:   "list <program>",
		Short: "List program work items",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramItemList(cmd.OutOrStdout(), args[0], status, jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	cmd.Flags().StringVar(&status, "status", "", "filter by item status")
	return cmd
}

func runProgramItemList(out io.Writer, slug, status string, jsonOutput bool) error {
	path, err := program.Find(slug)
	if err != nil {
		return err
	}
	p, err := program.Load(path)
	if err != nil {
		return err
	}
	if status != "" && !validProgramItemStatus(program.ItemStatus(status)) {
		return fmt.Errorf("unsupported item status %q", status)
	}
	items := make([]program.WorkItem, 0, len(p.Items))
	for _, item := range p.Items {
		if status == "" || item.Status == program.ItemStatus(status) {
			items = append(items, item)
		}
	}
	if jsonOutput {
		return writeProgramJSON(out, items)
	}
	for _, item := range items {
		fmt.Fprintf(out, "%s  %-10s %-2s %s\n", item.ID, item.Status, item.Priority, item.Title)
	}
	return nil
}

func newCmdProgramItemUpdate() *cobra.Command {
	var title, priorityText, addDepText, removeDepText, addContractText, removeContractText, note string
	cmd := &cobra.Command{
		Use:   "update <program> <item>",
		Short: "Update work item metadata",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requirePlanShapingTurn("relay program item update"); err != nil {
				return err
			}
			update := program.ItemUpdate{}
			if cmd.Flags().Changed("title") {
				update.Title = &title
			}
			if cmd.Flags().Changed("priority") {
				priority := program.Priority(priorityText)
				update.Priority = &priority
			}
			var err error
			if update.AddDependencies, err = parseProgramCSV(addDepText, "dependency"); err != nil {
				return err
			}
			if update.RemoveDependencies, err = parseProgramCSV(removeDepText, "dependency"); err != nil {
				return err
			}
			if update.AddContractRefs, err = parseProgramCSV(addContractText, "contract"); err != nil {
				return err
			}
			if update.RemoveContractRefs, err = parseProgramCSV(removeContractText, "contract"); err != nil {
				return err
			}
			if cmd.Flags().Changed("note") {
				update.Note = note
			}
			return runProgramItemUpdate(cmd.OutOrStdout(), args[0], args[1], update)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "replacement title")
	cmd.Flags().StringVar(&priorityText, "priority", "", "replacement priority")
	cmd.Flags().StringVar(&addDepText, "add-dep", "", "comma-separated dependencies to add")
	cmd.Flags().StringVar(&removeDepText, "remove-dep", "", "comma-separated dependencies to remove")
	cmd.Flags().StringVar(&addContractText, "add-contract", "", "comma-separated contracts to add")
	cmd.Flags().StringVar(&removeContractText, "remove-contract", "", "comma-separated contracts to remove")
	cmd.Flags().StringVar(&note, "note", "", "note to append")
	return cmd
}

func runProgramItemUpdate(out io.Writer, slug, itemID string, update program.ItemUpdate) error {
	path, p, err := loadActiveProgram(slug)
	if err != nil {
		return err
	}
	if err := p.UpdateItem(itemID, update); err != nil {
		return err
	}
	if err := saveProgramMutation(path, p, "Updated item "+itemID); err != nil {
		return err
	}
	fmt.Fprintln(out, itemID)
	return nil
}

func newCmdProgramItemBlock() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "block <program> <item>",
		Short: "Block a work item",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("block item %q: --reason is required", args[1])
			}
			return mutateProgramItem(cmd.OutOrStdout(), args[0], args[1], "Blocked item "+args[1]+": "+reason,
				func(p *program.Program) error { return p.BlockItem(args[1], reason) })
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason the item is blocked")
	return cmd
}

func newCmdProgramItemUnblock() *cobra.Command {
	return &cobra.Command{
		Use:   "unblock <program> <item>",
		Short: "Unblock a work item",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutateProgramItem(cmd.OutOrStdout(), args[0], args[1], "Unblocked item "+args[1],
				func(p *program.Program) error { return p.UnblockItem(args[1]) })
		},
	}
}

func newCmdProgramItemCancel() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "cancel <program> <item>",
		Short: "Cancel a work item",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requirePlanShapingTurn("relay program item cancel"); err != nil {
				return err
			}
			message := "Canceled item " + args[1]
			if strings.TrimSpace(reason) != "" {
				message += ": " + reason
			}
			return mutateProgramItem(cmd.OutOrStdout(), args[0], args[1], message,
				func(p *program.Program) error { return p.CancelItem(args[1], reason) })
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason for cancellation")
	return cmd
}

func newCmdProgramItemLink() *cobra.Command {
	var projectSlug string
	cmd := &cobra.Command{
		Use:   "link <program> <item>",
		Short: "Link a work item to an existing Relay project",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(projectSlug) == "" {
				return fmt.Errorf("link item %q: --project is required", args[1])
			}
			return mutateProgramItem(cmd.OutOrStdout(), args[0], args[1],
				fmt.Sprintf("Linked item %s to project %s", args[1], projectSlug),
				func(p *program.Program) error { return p.LinkItem(args[1], projectSlug) })
		},
	}
	cmd.Flags().StringVar(&projectSlug, "project", "", "Relay project slug")
	return cmd
}

func mutateProgramItem(out io.Writer, slug, itemID, progressMessage string, mutate func(*program.Program) error) error {
	path, p, err := loadActiveProgram(slug)
	if err != nil {
		return err
	}
	if err := mutate(&p); err != nil {
		return err
	}
	if err := saveProgramMutation(path, p, progressMessage); err != nil {
		return err
	}
	fmt.Fprintln(out, itemID)
	return nil
}

func saveProgramMutation(path string, p program.Program, progressMessage string) error {
	if err := program.Save(path, p); err != nil {
		return err
	}
	if err := appendProgramProgress(filepath.Dir(path), progressMessage); err != nil {
		return err
	}
	return nil
}

func parseProgramCSV(value, label string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("%s list contains an empty value", label)
		}
		result = append(result, part)
	}
	return result, nil
}

func validProgramItemStatus(status program.ItemStatus) bool {
	switch status {
	case program.ItemPending, program.ItemDispatched, program.ItemInReview,
		program.ItemBlocked, program.ItemMerged, program.ItemCancelled:
		return true
	default:
		return false
	}
}
