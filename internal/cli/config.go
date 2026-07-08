package cli

import (
	"fmt"
	"strings"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/spf13/cobra"
)

func newCmdConfig() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read or update relay configuration",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "branch-prefix [prefix]",
		Short: "Print or update the configured branch prefix",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := config.EnsureBase()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				fmt.Println(cfg.BranchPrefix)
				return nil
			}
			cfg, err = config.SetBranchPrefix(cfg, args[0])
			if err != nil {
				return err
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Println(cfg.BranchPrefix)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "default-agent [agent]",
		Short: "Print or update the default coding agent",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := config.EnsureBase()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				fmt.Println(cfg.DefaultAgent)
				return nil
			}
			cfg, err = config.SetDefaultAgent(cfg, args[0])
			if err != nil {
				return err
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Println(cfg.DefaultAgent)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "permission-mode <agent> [mode]",
		Short: "Print or update one agent's permission mode",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			agentName := strings.TrimSpace(args[0])
			a, err := agent.Get(agentName)
			if err != nil {
				return err
			}
			cfg, err := config.EnsureBase()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				mode := cfg.PermissionModeFor(a.Name())
				if mode == "" {
					return fmt.Errorf("permission mode for %s is not configured; choose one of: %s", a.Name(), strings.Join(a.PermissionModes(), ", "))
				}
				fmt.Println(mode)
				return nil
			}
			cfg, err = config.SetAgentPermissionMode(cfg, a.Name(), args[1])
			if err != nil {
				return err
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Println(cfg.PermissionModeFor(a.Name()))
			return nil
		},
	})
	return cmd
}
