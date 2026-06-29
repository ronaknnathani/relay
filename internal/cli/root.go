// Package cli wires the Cobra command tree for the `relay` CLI.
package cli

import (
	"fmt"
	"strings"

	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

// Execute runs the root command and returns any error to main.
func Execute() error {
	ui.InitColor()
	root := newRootCmd()
	return root.Execute()
}

// rootFlags holds the flags the root command exposes.
// `--no-color` is persistent (applies to all subcommands).
// `--no-launch`, `--quick`, and `--name`/`-n` are local to root: they only
// make sense for the implicit "new project" form (`relay "<task>"`) and
// would be misleading on subcommands like `status` or `archive`.
type rootFlags struct {
	noColor  bool
	quick    bool
	noLaunch bool
	name     string
	agent    string
	workflow string
}

func newRootCmd() *cobra.Command {
	flags := &rootFlags{}
	cmd := &cobra.Command{
		Use:           "relay",
		Short:         "AI-powered development workflow",
		Long:          "AI-powered development workflow for coding agents.\n\nWith no args: list active projects.\nWith a task description: create a new project and launch the agent.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			if flags.noColor {
				ui.DisableColor()
			}
		},
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				// New-project flags only make sense when creating a project.
				// If the user passed any of them with no task, they meant to
				// create one — surface that as a usage error rather than
				// silently listing status.
				if flags.quick || flags.noLaunch || flags.name != "" {
					return fmt.Errorf("usage: relay \"<task description>\"")
				}
				return runStatus(statusOpts{})
			}
			return runNew(newOpts{
				task:     strings.Join(args, " "),
				name:     flags.name,
				quick:    flags.quick,
				noLaunch: flags.noLaunch,
				agent:    flags.agent,
				workflow: flags.workflow,
			})
		},
	}
	cmd.PersistentFlags().BoolVar(&flags.noColor, "no-color", false, "disable colored output")
	// Local (not persistent): these belong to the implicit "new project" form.
	cmd.Flags().BoolVar(&flags.quick, "quick", false, "skip brainstorming (when creating a new project)")
	cmd.Flags().BoolVar(&flags.noLaunch, "no-launch", false, "create project but don't launch the coding agent")
	cmd.Flags().StringVarP(&flags.name, "name", "n", "", "custom project slug")
	cmd.Flags().StringVar(&flags.agent, "agent", "", "coding agent to launch (default from config)")
	cmd.Flags().StringVar(&flags.workflow, "workflow", defaultWorkflow, "workflow skill to launch (deliver-pr or stack-ship)")

	cmd.AddCommand(
		newCmdNew(flags),
		newCmdResume(),
		newCmdStatus(),
		newCmdUpdate(),
		newCmdArchive(),
		newCmdDashboard(),
		newCmdGC(),
		newCmdTodo(),
		newCmdGenerate(),
		newCmdState(),
	)
	return cmd
}
