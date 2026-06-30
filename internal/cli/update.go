package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/ronaknnathani/relay/internal/project"
	"github.com/spf13/cobra"
)

func newCmdUpdate() *cobra.Command {
	return &cobra.Command{
		Use:                "update <slug> [flags]",
		Short:              "Update manifest fields",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE:               runUpdate,
	}
}

func runUpdate(cmd *cobra.Command, args []string) error {
	// DisableFlagParsing means --help reaches us as args[0]; honor it before
	// treating it as a slug.
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		return cmd.Help()
	}
	slug := args[0]
	args = args[1:]
	if len(args) == 0 {
		return fmt.Errorf("usage: relay update <slug> [flags]")
	}
	path, err := project.Find(slug)
	if err != nil {
		return err
	}
	m, err := project.Load(path)
	if err != nil {
		return err
	}

	for i := 0; i < len(args); i++ {
		flag := args[i]
		if i+1 >= len(args) {
			return fmt.Errorf("missing value for %s", flag)
		}
		val := args[i+1]
		i++
		switch flag {
		case "--phase":
			m.Phase = val
		case "--status":
			m.Status = val
		case "--worktree":
			m.Worktree = strPtr(val)
		case "--title":
			m.Title = val
		case "--archived":
			if val == "now" {
				now := time.Now().UTC().Format(time.RFC3339)
				m.Archived = &now
			} else {
				m.Archived = strPtr(val)
			}
		case "--pr.number":
			n, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("invalid --pr.number %q: %w", val, err)
			}
			m.PR.Number = &n
		case "--pr.url":
			m.PR.URL = strPtr(val)
		case "--pr.ci_status":
			m.PR.CIStatus = strPtr(val)
		case "--set":
			if err := project.ApplySet(&m, val); err != nil {
				return err
			}
		case "--add":
			if err := project.ApplyAdd(&m, val); err != nil {
				return err
			}
		case "--remove":
			if err := project.ApplyRemove(&m, val); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown flag: %s", flag)
		}
	}
	if err := project.Save(path, m); err != nil {
		return err
	}
	return nil
}

func strPtr(s string) *string { return &s }
