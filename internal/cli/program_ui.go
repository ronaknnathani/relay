package cli

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programui"
	"github.com/spf13/cobra"
)

var serveProgramUI = programui.Serve

func newCmdProgramUI() *cobra.Command {
	var port int
	var noOpen bool
	command := &cobra.Command{
		Use:   "ui <slug>",
		Short: "Serve the live local Program UI",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runProgramUI(command.Context(), command.OutOrStdout(), args[0], port, !noOpen)
		},
	}
	command.Flags().IntVar(&port, "port", 0, "localhost port (0 chooses an available port)")
	command.Flags().BoolVar(&noOpen, "no-open", false, "do not open the browser")
	return command
}

func runProgramUI(parent context.Context, out io.Writer, slug string, port int, open bool) error {
	path, err := program.Find(slug)
	if err != nil {
		return err
	}
	if _, err := program.Load(path); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveProgramUI(ctx, programui.Options{
		Slug: slug,
		Port: port,
		Open: open,
		Out:  out,
	})
}
