package cli

import (
	"bufio"
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
	"golang.org/x/term"
)

type newOpts struct {
	task     string
	name     string
	quick    bool
	noLaunch bool
	agent    string
	workflow string
	reclaim  bool
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
		reclaim   bool
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
				reclaim:  reclaim,
			})
		},
	}
	cmd.Flags().BoolVar(&quick, "quick", false, "skip brainstorming")
	cmd.Flags().BoolVar(&noLaunch, "no-launch", false, "create project but don't launch the coding agent")
	cmd.Flags().StringVarP(&name, "name", "n", "", "custom project slug")
	cmd.Flags().StringVar(&agentName, "agent", "", "coding agent to launch (default from config)")
	cmd.Flags().StringVar(&workflow, "workflow", defaultWorkflow, "workflow skill to launch (deliver-pr or stack-ship)")
	cmd.Flags().BoolVar(&reclaim, "reclaim", false, "reclaim leftover branch/worktree from an interrupted setup without prompting")
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
	// Guard against a custom --name that could escape the project/worktree
	// trees (e.g. "../foo"); reclaim below removes paths derived from the slug.
	if err := project.ValidateSlug(slug); err != nil {
		return err
	}

	projDir := filepath.Join(project.ActiveDir(), slug)
	manifestPath := project.ManifestPath(project.ActiveDir(), slug)
	if _, err := os.Stat(manifestPath); err == nil {
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

	cfg, err := config.EnsureForAgent(opts.agent)
	if err != nil {
		return err
	}

	a, err := agent.Get(agent.ResolveName(opts.agent, "", cfg.DefaultAgent))
	if err != nil {
		return err
	}

	branch := cfg.BranchPrefix + slug
	worktreeDir := filepath.Join(repoRoot, ".worktrees", cfg.WorktreePrefix()+slug)

	// A branch, worktree, or project dir with no valid manifest is leftover
	// state from an interrupted or failed setup (e.g. Ctrl+C before the manifest
	// was written). Detect it and offer to reclaim so the same slug is reusable.
	branchExists := gitx.BranchExists(repoRoot, branch)
	if branchExists || pathExists(worktreeDir) || pathExists(projDir) {
		// "safe" leftovers can be reclaimed without prompting non-interactively:
		// the branch has no unique commits AND the worktree holds no uncommitted
		// or untracked work. Anything else needs explicit consent (--reclaim or a
		// TTY prompt) so we never silently discard the user's changes.
		safe := (!branchExists || branchMerged(repoRoot, branch, baseBranch)) &&
			worktreeReclaimSafe(repoRoot, worktreeDir)
		proceed, err := decideReclaim(opts.reclaim, slug, branch, worktreeDir, projDir, branchExists, safe)
		if err != nil {
			return err
		}
		if !proceed {
			return fmt.Errorf("aborted: leftover state for %q is still present. Reclaim it, choose a different name with -n, or remove it manually", slug)
		}
		if err := reclaimLeftovers(repoRoot, branch, worktreeDir, projDir); err != nil {
			return err
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
		PermissionMode: cfg.PermissionModeFor(a.Name()),
	}
	return launcher.Launch(a, o)
}

// pathExists reports whether a filesystem path exists (file, dir, or symlink).
func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// worktreeReclaimSafe reports whether the leftover worktree at dir can be
// removed without discarding user work. An absent dir is safe; a registered
// worktree is safe only when it is clean (no uncommitted/untracked changes); a
// leftover directory that git does not track is safe only when it is empty.
func worktreeReclaimSafe(repoRoot, dir string) bool {
	if !pathExists(dir) {
		return true
	}
	registered, err := gitx.IsWorktree(repoRoot, dir)
	if err != nil {
		return false
	}
	if registered {
		clean, err := gitx.WorktreeClean(dir)
		return err == nil && clean
	}
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) == 0
}

// branchMerged reports whether branch's work is already contained in base
// (locally or on origin), i.e. deleting branch loses no unique commits.
func branchMerged(repo, branch, base string) bool {
	if base == "" {
		return false
	}
	if gitx.HasOrigin(repo) && gitx.RevParse(repo, "origin/"+base) != "" {
		if gitx.IsBranchReachable(repo, branch, "origin/"+base) {
			return true
		}
	}
	return gitx.IsBranchReachable(repo, branch, base)
}

// leftoverDesc summarizes which leftover artifacts exist for a slug.
func leftoverDesc(branch, worktreeDir, projDir string, branchExists bool) string {
	var parts []string
	if branchExists {
		parts = append(parts, "branch "+branch)
	}
	if pathExists(worktreeDir) {
		parts = append(parts, "worktree "+worktreeDir)
	}
	if pathExists(projDir) {
		parts = append(parts, "project dir "+projDir)
	}
	return strings.Join(parts, ", ")
}

// decideReclaim determines whether to reclaim leftover state. With --reclaim it
// proceeds unconditionally. Otherwise, on a TTY it prompts (defaulting to yes
// when the leftover is safe to delete, no when it may hold unmerged commits or
// uncommitted work); non-interactively it auto-reclaims safe leftovers and
// refuses unsafe ones.
func decideReclaim(force bool, slug, branch, worktreeDir, projDir string, branchExists, safe bool) (bool, error) {
	desc := leftoverDesc(branch, worktreeDir, projDir, branchExists)
	if force {
		fmt.Printf("  %s\n", ui.Color(ui.Dim, fmt.Sprintf("Reclaiming leftover state for %q: %s.", slug, desc)))
		return true, nil
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Println()
		ui.Warn("Found leftover state from an interrupted setup for %q: %s.", slug, desc)
		if !safe {
			ui.Warn("It may contain unmerged commits or uncommitted work that will be permanently deleted if you reclaim.")
		}
		return promptYesNo(fmt.Sprintf("Reclaim and recreate %q?", slug), safe)
	}
	if safe {
		fmt.Printf("  %s\n", ui.Color(ui.Dim, fmt.Sprintf("Reclaiming leftover state for %q: %s.", slug, desc)))
		return true, nil
	}
	return false, fmt.Errorf("leftover state for %q: %s — it may contain unmerged commits or uncommitted work. Re-run with --reclaim to discard it, or use: relay -n <different-name> %s", slug, desc, slug)
}

// promptYesNo asks a yes/no question, returning defaultYes on an empty answer.
func promptYesNo(question string, defaultYes bool) (bool, error) {
	hint := "[y/N]"
	if defaultYes {
		hint = "[Y/n]"
	}
	fmt.Printf("%s %s ", question, hint)
	raw, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return defaultYes, nil
	}
	return raw == "y" || raw == "yes", nil
}

// reclaimLeftovers removes leftover state for a slug: the worktree (force, since
// no manifest means no agent ran there), the branch (force-deleted once the
// worktree is gone), and the project dir. Each removal is announced so the user
// sees exactly what was reclaimed.
func reclaimLeftovers(repoRoot, branch, worktreeDir, projDir string) error {
	if pathExists(worktreeDir) {
		if err := gitx.WorktreeRemove(repoRoot, worktreeDir, true); err != nil {
			return fmt.Errorf("reclaim worktree %s: %w", worktreeDir, err)
		}
		fmt.Printf("  %s %s\n", ui.Color(ui.Dim, "Removed worktree:"), worktreeDir)
	}
	if gitx.BranchExists(repoRoot, branch) {
		if err := gitx.ForceDeleteBranch(repoRoot, branch); err != nil {
			return fmt.Errorf("reclaim branch %q: %w", branch, err)
		}
		fmt.Printf("  %s %s\n", ui.Color(ui.Dim, "Deleted branch:"), branch)
	}
	if pathExists(projDir) {
		if err := os.RemoveAll(projDir); err != nil {
			return fmt.Errorf("reclaim project dir %s: %w", projDir, err)
		}
		fmt.Printf("  %s %s\n", ui.Color(ui.Dim, "Removed project dir:"), projDir)
	}
	return nil
}
