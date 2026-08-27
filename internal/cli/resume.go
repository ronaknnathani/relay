package cli

import (
	"fmt"
	"path/filepath"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

func newCmdResume() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <slug>",
		Short: "Resume project at current phase",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runResume(args[0])
		},
	}
}

func runResume(slug string) error {
	path, err := project.Find(slug)
	if err != nil {
		return err
	}
	m, err := project.Load(path)
	if err != nil {
		return err
	}
	if m.Worktree == nil || *m.Worktree == "" {
		return fmt.Errorf("project %q has no worktree", slug)
	}
	if m.Phase == "done" {
		return fmt.Errorf("project %q is complete. Run: relay archive %s", slug, slug)
	}
	if err := guardManagedHerdrResume(m); err != nil {
		return err
	}

	cfg, err := config.EnsureForAgent(m.Agent)
	if err != nil {
		return err
	}

	a, err := agent.Get(agent.ResolveName("", m.Agent, cfg.DefaultAgent))
	if err != nil {
		return err
	}

	// Relaunch the project's workflow skill; it is resume-first and reconstructs
	// its position from `relay state`. Fall back to the legacy phase→batch
	// mapping for older manifests written before the workflow field existed.
	cmd := resumeCommand(m)
	fmt.Println()
	fmt.Printf("  %s\n", ui.Color(ui.Bold+ui.White, "Resuming project"))
	ui.PrintField("Slug", slug)
	ui.PrintField("Workflow", cmd)
	fmt.Println()
	fmt.Printf("  %s\n", ui.Color(ui.Dim, fmt.Sprintf("Launching %s…", a.Name())))
	fmt.Println()

	systemPrompt := fmt.Sprintf("Active relay project: %s. Workflow: %s.", slug, cmd)
	o := relayLaunchOptions(*m.Worktree, filepath.Dir(path), systemPrompt, slug, cmd, m.Title, cfg.PermissionModeFor(a.Name()))
	return launchAgent(a, o)
}

// guardManagedHerdrResume enforces the managed-session contract: every managed
// child runs under Herdr with exactly one live owner. Standalone Relay projects
// are unaffected.
func guardManagedHerdrResume(manifest project.Manifest) error {
	if manifest.Program == "" || manifest.ProgramItem == "" {
		return nil
	}
	subject := fmt.Sprintf("managed child project %q", manifest.Slug)
	readiness, err := requireManagedHerdr("relay resume "+manifest.Slug, manifest.Agent, subject, false)
	if err != nil {
		return err
	}
	owner, ok := herdr.FindLiveWorker(readiness.Agents, manifest.Slug, manifest.Repo, *manifest.Worktree)
	if !ok {
		return nil
	}
	return fmt.Errorf(
		"project %q already has another live Herdr owner in pane %s; focus it with: herdr agent focus %s",
		manifest.Slug, owner.PaneID, owner.PaneID,
	)
}
