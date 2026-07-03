package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/launcher"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

type newOpts struct {
	task     string
	name     string
	quick    bool
	noLaunch bool
	agent    string
	workflow string
}

// defaultWorkflow is the workflow skill launched when none is specified.
const defaultWorkflow = "deliver-pr"

// newCmdNew exposes `relay new <task>` explicitly. The public form is
// `relay "<task>"`, so this command is hidden. Flags are local here since
// root's matching flags are no longer persistent (see root.go).
func newCmdNew(_ *rootFlags) *cobra.Command {
	var (
		name      string
		quick     bool
		noLaunch  bool
		agentName string
		workflow  string
	)
	cmd := &cobra.Command{
		Use:    "new <task>",
		Short:  "Create a new project and launch the coding agent",
		Args:   cobra.MinimumNArgs(1),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runNew(newOpts{
				task:     strings.Join(args, " "),
				name:     name,
				quick:    quick,
				noLaunch: noLaunch,
				agent:    agentName,
				workflow: workflow,
			})
		},
	}
	cmd.Flags().BoolVar(&quick, "quick", false, "skip brainstorming")
	cmd.Flags().BoolVar(&noLaunch, "no-launch", false, "create project but don't launch the coding agent")
	cmd.Flags().StringVarP(&name, "name", "n", "", "custom project slug")
	cmd.Flags().StringVar(&agentName, "agent", "", "coding agent to launch (default from config)")
	cmd.Flags().StringVar(&workflow, "workflow", defaultWorkflow, "workflow skill to launch (deliver-pr or stack-ship)")
	return cmd
}

func runNew(opts newOpts) error {
	slug := opts.name
	if slug == "" {
		slug = project.DeriveSlug(opts.task)
	}
	if slug == "" {
		return fmt.Errorf("could not derive slug from task description")
	}

	projDir := filepath.Join(project.ActiveDir(), slug)
	if _, err := os.Stat(projDir); err == nil {
		return fmt.Errorf("project %q already exists. Use: relay resume %s", slug, slug)
	}

	repoRoot, err := gitx.RepoRoot()
	if err != nil {
		return fmt.Errorf("locate repo root: %w", err)
	}
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}

	baseBranch := gitx.DetectDefaultBranch(repoRoot)
	if baseBranch == "" {
		return fmt.Errorf("could not determine default branch (no origin/HEAD, main, or master)")
	}

	cfg, err := config.Ensure()
	if err != nil {
		return err
	}

	a, err := agent.Get(agent.ResolveName(opts.agent, "", cfg.DefaultAgent))
	if err != nil {
		return err
	}

	branch := cfg.BranchPrefix + slug
	if gitx.BranchExists(repoRoot, branch) {
		if gitx.IsBranchReachable(repoRoot, branch, baseBranch) {
			if err := gitx.DeleteBranch(repoRoot, branch); err != nil {
				return fmt.Errorf("branch %q exists (merged) but could not delete: %w", branch, err)
			}
		} else {
			return fmt.Errorf("branch %q already exists and is not merged. Use: relay -n <different-name> %q", branch, opts.task)
		}
	}

	startPoint := baseBranch
	if gitx.HasOrigin(repoRoot) {
		if out, err := gitx.Fetch(repoRoot, baseBranch); err != nil {
			ui.Warn("%s\n%s", err, out)
		}
		if gitx.RevParse(repoRoot, "origin/"+baseBranch) != "" {
			startPoint = "origin/" + baseBranch
		}
	}
	startSHA := gitx.RevParse(repoRoot, startPoint)

	worktreeDir := filepath.Join(repoRoot, ".worktrees", cfg.WorktreePrefix()+slug)
	if err := gitx.WorktreeAdd(repoRoot, worktreeDir, branch, startPoint); err != nil {
		return err
	}

	if err := os.MkdirAll(projDir, 0755); err != nil {
		return fmt.Errorf("create project dir: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	wf := opts.workflow
	if wf == "" {
		wf = defaultWorkflow
	}
	m := project.Manifest{
		Slug:            slug,
		Title:           opts.task,
		Repo:            repoRoot,
		Branch:          branch,
		Agent:           a.Name(),
		BaseBranch:      baseBranch,
		StartSHA:        startSHA,
		Worktree:        &worktreeDir,
		Status:          "initialized",
		Workflow:        wf,
		Phase:           "plan",
		Created:         now,
		PR:              project.PRInfo{},
		PhasesCompleted: []string{"init"},
		PhasesRemaining: project.AllPhases,
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), slug), m); err != nil {
		return err
	}

	files := map[string]string{
		"task.md":  "# Task\n\n" + opts.task + "\n",
		"notes.md": "# " + slug + " — Notes\n\nScratchpad for ideas, context, and observations.\n",
		"todos.md": "# " + slug + " — TODOs\n\n- [ ] ...\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(projDir, name), []byte(content), 0644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	fmt.Println()
	fmt.Printf("  %s\n", ui.Color(ui.Bold+ui.White, "Project created"))
	ui.PrintField("Slug", slug)
	ui.PrintField("Branch", branch)
	ui.PrintField("Worktree", worktreeDir)
	ui.PrintField("Project", projDir)
	fmt.Println()

	if opts.noLaunch {
		return nil
	}

	fmt.Printf("  %s\n", ui.Color(ui.Dim, fmt.Sprintf("Launching %s…", a.Name())))
	fmt.Println()

	mode := "full"
	if opts.quick {
		mode = "quick"
	}
	systemPrompt := fmt.Sprintf("Active relay project: %s. Workflow: %s. Mode: %s.", slug, wf, mode)
	o := agent.LaunchOptions{
		Worktree:       worktreeDir,
		ProjectDir:     projDir,
		SystemPrompt:   systemPrompt,
		SessionName:    "relay:" + slug,
		Command:        wf,
		CommandArgs:    slug,
		PermissionMode: cfg.PermissionMode,
	}
	return launcher.Launch(a, o)
}
