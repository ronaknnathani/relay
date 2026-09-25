package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/programui"
	"github.com/spf13/cobra"
)

var serveProgramUI = programui.ServeOrReuse

func newCmdProgramUI() *cobra.Command {
	var port int
	var noOpen bool
	command := &cobra.Command{
		Use:   "ui [slug]",
		Short: "Serve the live local Program UI",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			slug := ""
			if len(args) == 1 {
				slug = args[0]
			}
			return runProgramUI(
				command.Context(),
				command.OutOrStdout(),
				slug,
				port,
				command.Flags().Changed("port"),
				!noOpen,
			)
		},
	}
	command.Flags().IntVar(&port, "port", 0, "localhost port (0 chooses an available port)")
	command.Flags().BoolVar(&noOpen, "no-open", false, "do not open the browser")
	return command
}

func runProgramUI(
	parent context.Context,
	out io.Writer,
	slug string,
	port int,
	portExplicit bool,
	open bool,
) error {
	initialPath := "/"
	if slug != "" {
		path, err := program.Find(slug)
		if err != nil {
			return err
		}
		activePath := program.ManifestPath(program.ActiveDir(), slug)
		if filepath.Clean(path) != filepath.Clean(activePath) {
			return fmt.Errorf(
				"program %q is archived; the program UI shows active programs only",
				slug,
			)
		}
		if _, err := program.Load(path); err != nil {
			return err
		}
		initialPath = "/programs/" + url.PathEscape(slug) + "/"
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveProgramUI(ctx, programui.Options{
		Port:         port,
		PortExplicit: portExplicit,
		InitialPath:  initialPath,
		Open:         open,
		Out:          out,
	})
}
