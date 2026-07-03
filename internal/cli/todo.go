package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/todo"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

func newCmdTodo() *cobra.Command {
	return &cobra.Command{
		Use:                "todo [list|done <id>|<description>]",
		Short:              "Manage repo-local todos",
		DisableFlagParsing: true,
		RunE:               runTodo,
	}
}

func runTodo(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return runTodoList()
	}
	// DisableFlagParsing means --help reaches us as a positional arg; honor it
	// explicitly so `relay todo --help` doesn't get added as a todo.
	if args[0] == "--help" || args[0] == "-h" {
		return cmd.Help()
	}
	switch args[0] {
	case "list":
		return runTodoList()
	case "done":
		if len(args) < 2 {
			return fmt.Errorf("usage: relay todo done <id>")
		}
		id, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid todo id %q: %w", args[1], err)
		}
		return runTodoDone(id)
	case "add":
		if len(args) < 2 {
			return fmt.Errorf("usage: relay todo add <description>")
		}
		return runTodoAdd(strings.Join(args[1:], " "))
	default:
		return runTodoAdd(strings.Join(args, " "))
	}
}

func runTodoAdd(description string) error {
	path, err := todo.FilePath()
	if err != nil {
		return err
	}
	todos := todo.Load(path)
	id := todo.NextID(todos)
	t := todo.Todo{
		ID:          id,
		Description: description,
		Source:      gitx.CurrentBranch(),
		Created:     time.Now().UTC().Format(time.RFC3339),
		Done:        false,
	}
	todos = append(todos, t)
	if err := todo.Save(path, todos); err != nil {
		return err
	}
	fmt.Printf("Added todo #%d: %s\n", id, description)
	return nil
}

func runTodoList() error {
	path, err := todo.FilePath()
	if err != nil {
		return err
	}
	todos := todo.Load(path)
	pending := todo.Pending(todos)
	if len(pending) == 0 {
		fmt.Println("  No pending todos.")
		return nil
	}

	idW, srcW, crtW := len("ID"), len("SOURCE"), len("CREATED")
	for _, t := range pending {
		if w := len(fmt.Sprintf("#%d", t.ID)); w > idW {
			idW = w
		}
		if w := len(t.Source); w > srcW {
			srcW = w
		}
		if w := len(ui.RelativeTime(t.Created)); w > crtW {
			crtW = w
		}
	}
	// Borders: 5 vertical lines. Padding: 4 cols x 2 chars = 8. Total fixed: 13.
	descW := max(ui.TerminalWidth()-idW-srcW-crtW-13, 30)

	descMax := tw.NewMapper[int, int]().Set(1, descW)
	cfg := tablewriter.Config{
		Header: tw.CellConfig{
			Alignment: tw.CellAlignment{Global: tw.AlignLeft},
		},
		Row: tw.CellConfig{
			Alignment:    tw.CellAlignment{Global: tw.AlignLeft},
			ColMaxWidths: tw.CellWidth{PerColumn: descMax},
			Formatting:   tw.CellFormatting{AutoWrap: tw.WrapNormal},
		},
	}
	rendition := tw.Rendition{
		Symbols: tw.NewSymbols(tw.StyleLight),
		Borders: tw.Border{Top: tw.On, Bottom: tw.On, Left: tw.On, Right: tw.On},
		Settings: tw.Settings{
			Separators: tw.Separators{BetweenColumns: tw.On, BetweenRows: tw.On},
		},
	}

	table := tablewriter.NewTable(os.Stdout,
		tablewriter.WithConfig(cfg),
		tablewriter.WithRenderer(renderer.NewBlueprint(rendition)),
	)
	table.Header("ID", "DESCRIPTION", "SOURCE", "CREATED")
	for _, t := range pending {
		if err := table.Append(
			fmt.Sprintf("#%d", t.ID),
			t.Description,
			t.Source,
			ui.RelativeTime(t.Created),
		); err != nil {
			ui.Warn("append row: %s", err)
			continue
		}
	}
	if err := table.Render(); err != nil {
		ui.Warn("render table: %s", err)
	}
	return nil
}

func runTodoDone(id int) error {
	path, err := todo.FilePath()
	if err != nil {
		return err
	}
	todos := todo.Load(path)
	updated, ok := todo.MarkDone(todos, id)
	if !ok {
		return fmt.Errorf("todo #%d not found", id)
	}
	if err := todo.Save(path, todos); err != nil {
		return err
	}
	fmt.Printf("Marked todo #%d as done: %s\n", id, updated.Description)
	return nil
}
