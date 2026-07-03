package cli

import (
	"fmt"

	"github.com/ronaknnathani/relay/internal/config"
	"github.com/spf13/cobra"
)

func newCmdConfig() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read relay configuration",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "branch-prefix",
		Short: "Print the configured branch prefix",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := config.Ensure()
			if err != nil {
				return err
			}
			fmt.Println(cfg.BranchPrefix)
			return nil
		},
	})
	return cmd
}
