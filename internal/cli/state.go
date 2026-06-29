package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/ronaknnathani/relay/internal/project"
	"github.com/spf13/cobra"
)

// newCmdState exposes `relay state`, the deterministic state machine that
// workflow skills use to track and resume their progress. Skills call these
// subcommands instead of reading or writing state.json themselves, so the
// schema stays valid across agents and the "write before you continue"
// invariant is enforced by the binary rather than by each skill.
func newCmdState() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Read and update a project's resumable workflow state",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(
		newCmdStateInit(),
		newCmdStateNext(),
		newCmdStateCurrent(),
		newCmdStateSet(),
		newCmdStateAdvance(),
		newCmdStatePR(),
		newCmdStateLog(),
	)
	return cmd
}

// splitPhases parses a comma-separated phase list, trimming whitespace and
// dropping empty entries.
func splitPhases(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// loadState validates the slug, then reads its state, mapping a missing file to
// an actionable error that points the caller at `relay state init`.
func loadState(slug string) (project.WorkflowState, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return project.WorkflowState{}, err
	}
	ws, err := project.LoadState(project.StatePath(slug))
	if errors.Is(err, fs.ErrNotExist) {
		return ws, fmt.Errorf("no state for %q (run `relay state init %s` first)", slug, slug)
	}
	return ws, err
}

func newCmdStateInit() *cobra.Command {
	var workflow, phases string
	cmd := &cobra.Command{
		Use:   "init <slug>",
		Short: "Initialize state.json for a workflow run (every phase pending)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			slug := args[0]
			if err := project.ValidateSlug(slug); err != nil {
				return err
			}
			ws, err := project.NewState(slug, workflow, splitPhases(phases))
			if err != nil {
				return err
			}
			if err := project.CreateState(project.StatePath(slug), ws); err != nil {
				if errors.Is(err, fs.ErrExist) {
					return fmt.Errorf("state already initialized for %q (use `relay state next %s`)", slug, slug)
				}
				return err
			}
			fmt.Println(ws.Next())
			return nil
		},
	}
	cmd.Flags().StringVar(&workflow, "workflow", "", "workflow name (e.g. deliver-pr)")
	cmd.Flags().StringVar(&phases, "phases", "", "comma-separated ordered phase list")
	_ = cmd.MarkFlagRequired("workflow")
	_ = cmd.MarkFlagRequired("phases")
	return cmd
}

func newCmdStateNext() *cobra.Command {
	return &cobra.Command{
		Use:   "next <slug>",
		Short: "Print the next not-done phase (empty when the run is complete)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			ws, err := loadState(args[0])
			if err != nil {
				return err
			}
			fmt.Println(ws.Next())
			return nil
		},
	}
}

func newCmdStateCurrent() *cobra.Command {
	return &cobra.Command{
		Use:   "current <slug>",
		Short: "Print a one-line digest of where the run is",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			ws, err := loadState(args[0])
			if err != nil {
				return err
			}
			cur := ws.Current()
			if cur == "" {
				fmt.Println(`phase= status=done next= task=""`)
				return nil
			}
			ph, ok := ws.Phases[cur]
			if !ok {
				return fmt.Errorf("state corrupt: phase %q in order but missing from phases", cur)
			}
			// task is quoted so a value with spaces stays one parseable field.
			fmt.Printf("phase=%s status=%s next=%s task=%q\n", cur, ph.Status, ws.After(cur), ph.Task)
			return nil
		},
	}
}

func newCmdStateSet() *cobra.Command {
	var artifact, task string
	cmd := &cobra.Command{
		Use:   "set <slug> <phase> <status>",
		Short: "Set a phase's status (pending|in-progress|done)",
		Args:  cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, args []string) error {
			slug, phase, status := args[0], args[1], args[2]
			ws, err := loadState(slug)
			if err != nil {
				return err
			}
			if err := ws.SetPhase(phase, status, artifact, task); err != nil {
				return err
			}
			return project.SaveState(project.StatePath(slug), ws)
		},
	}
	cmd.Flags().StringVar(&artifact, "artifact", "", "artifact the phase produced (e.g. plan.md)")
	cmd.Flags().StringVar(&task, "task", "", "free-form progress marker (e.g. 3/7)")
	return cmd
}

func newCmdStateAdvance() *cobra.Command {
	return &cobra.Command{
		Use:   "advance <slug>",
		Short: "Mark the current phase done and print the next phase",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			slug := args[0]
			ws, err := loadState(slug)
			if err != nil {
				return err
			}
			next, err := ws.Advance()
			if err != nil {
				return err
			}
			if err := project.SaveState(project.StatePath(slug), ws); err != nil {
				return err
			}
			fmt.Println(next)
			return nil
		},
	}
}

func newCmdStatePR() *cobra.Command {
	var number int
	var url string
	cmd := &cobra.Command{
		Use:   "pr <slug>",
		Short: "Record the pull request the project produced",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			slug := args[0]
			ws, err := loadState(slug)
			if err != nil {
				return err
			}
			ws.SetPR(number, url)
			return project.SaveState(project.StatePath(slug), ws)
		},
	}
	cmd.Flags().IntVar(&number, "number", 0, "PR number")
	cmd.Flags().StringVar(&url, "url", "", "PR url")
	return cmd
}

func newCmdStateLog() *cobra.Command {
	return &cobra.Command{
		Use:   "log <slug> <message>",
		Short: "Append a timestamped line to the project's progress.md",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			slug := args[0]
			if err := project.ValidateSlug(slug); err != nil {
				return err
			}
			msg := strings.Join(args[1:], " ")
			return project.AppendProgress(project.ProgressPath(slug), msg)
		},
	}
}
