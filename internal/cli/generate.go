package cli

import (
	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/generate"
	"github.com/spf13/cobra"
)

// newCmdGenerate exposes `relay generate`, which renders the agent-neutral
// workflow source into an installable per-agent package. It is hidden because
// it is a setup helper, not part of the daily
// project workflow.
func newCmdGenerate() *cobra.Command {
	var (
		agentName string
		src       string
		out       string
	)
	cmd := &cobra.Command{
		Use:    "generate",
		Short:  "Render the workflow source into agent packages",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			// No --agent: render every registered agent into its install path
			// (~/.relay/agents/<name>), unless --out overrides the destination.
			if agentName == "" {
				for _, name := range agent.Names() {
					a, err := agent.Get(name)
					if err != nil {
						return err
					}
					if err := generate.Generate(a, src, destFor(name, out)); err != nil {
						return err
					}
				}
				return nil
			}
			a, err := agent.Get(agentName)
			if err != nil {
				return err
			}
			return generate.Generate(a, src, destFor(agentName, out))
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "agent to render (empty renders all)")
	cmd.Flags().StringVar(&src, "src", ".", "workflow source directory")
	cmd.Flags().StringVar(&out, "out", "", "output directory (default: each agent's install path)")
	return cmd
}

// destFor resolves the output directory for an agent: --out when set, otherwise
// the agent's stable install path under ~/.relay/agents/<name>.
func destFor(name, out string) string {
	if out != "" {
		return out
	}
	return agent.PackageDir(name)
}
