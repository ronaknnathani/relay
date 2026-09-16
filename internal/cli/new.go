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
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type newOpts struct {
	task     string
	name     string
	quick    bool
	full     bool
	noLaunch bool
	agent    string
	workflow string
	reclaim  bool
	base     string
}

type projectCreateOpts struct {
	task         string
	name         string
	agent        string
	workflow     string
	deliveryMode string
	reclaim      bool
	repo         string
	program      string
	programItem  string
	base         string
}

type projectCreateResult struct {
	manifest    project.Manifest
	projectDir  string
	worktreeDir string
	agent       agent.Agent
	config      config.Config
}

// defaultWorkflow is the workflow skill launched when none is specified.
const defaultWorkflow = "deliver-pr"

// newCmdNew exposes `relay new <task>` explicitly. The public form is
// `relay "<task>"`, so this command is hidden. Flags are local here since
// root's matching flags are no longer persistent (see root.go).
func newCmdNew() *cobra.Command {
	opts := newOpts{workflow: defaultWorkflow}
	cmd := &cobra.Command{
		Use:    "new <task>",
		Short:  "Create a new project and launch the coding agent",
		Args:   cobra.MinimumNArgs(1),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			opts.task = strings.Join(args, " ")
			return runNew(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.quick, "quick", false, "deprecated alias for adaptive delivery")
	cmd.Flags().BoolVar(&opts.full, "full", false, "force the full delivery workflow")
	cmd.Flags().BoolVar(&opts.noLaunch, "no-launch", false, "create project but don't launch the coding agent")
	cmd.Flags().StringVarP(&opts.name, "name", "n", "", "custom project slug")
	cmd.Flags().StringVar(&opts.agent, "agent", "", "coding agent to launch (default from config)")
	cmd.Flags().StringVar(&opts.workflow, "workflow", defaultWorkflow, "workflow skill to launch (deliver-pr or stack-ship)")
	cmd.Flags().BoolVar(&opts.reclaim, "reclaim", false, "reclaim leftover branch/worktree from an interrupted setup without prompting")
	cmd.Flags().StringVar(&opts.base, "base", "", "base branch or commit for the new project")
	return cmd
}

func runNew(opts newOpts) error {
	if opts.quick && opts.full {
		return fmt.Errorf("--quick and --full cannot be used together")
	}
	deliveryMode := ""
	if opts.full {
		deliveryMode = project.DeliveryModeFull
	}
	created, err := createProject(projectCreateOpts{
		task:         opts.task,
		name:         opts.name,
		agent:        opts.agent,
		workflow:     opts.workflow,
		deliveryMode: deliveryMode,
		reclaim:      opts.reclaim,
		base:         opts.base,
	})
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("  %s\n", ui.Color(ui.Bold+ui.White, "Project created"))
	ui.PrintField("Slug", created.manifest.Slug)
	ui.PrintField("Branch", created.manifest.Branch)
	ui.PrintField("Worktree", created.worktreeDir)
	ui.PrintField("Project", created.projectDir)
	fmt.Println()

	if opts.noLaunch {
		return nil
	}

	fmt.Printf("  %s\n", ui.Color(ui.Dim, fmt.Sprintf("Launching %s…", created.agent.Name())))
	fmt.Println()

	systemPrompt := fmt.Sprintf("Active relay project: %s. Workflow: %s. Delivery mode: %s.",
		created.manifest.Slug, created.manifest.Workflow, created.manifest.DeliveryMode)
	o := relayLaunchOptions(
		created.worktreeDir,
		created.projectDir,
		systemPrompt,
		created.manifest.Slug,
		created.manifest.Workflow,
		created.manifest.Title,
		created.config.PermissionModeFor(created.agent.Name()),
	)
	return launchAgent(created.agent, o)
}

func createProject(opts projectCreateOpts) (projectCreateResult, error) {
	slug := opts.name
	if slug == "" {
		slug = project.DeriveSlug(opts.task)
	}
	if slug == "" {
		return projectCreateResult{}, fmt.Errorf("could not derive slug from task description")
	}
	// Guard against a custom --name that could escape the project/worktree
	// trees (e.g. "../foo"); reclaim below removes paths derived from the slug.
	if err := project.ValidateSlug(slug); err != nil {
		return projectCreateResult{}, err
	}
	return withProjectLifecycleLock(slug, func() (projectCreateResult, error) {
		return createProjectLocked(opts, slug)
	})
}

func createProjectLocked(opts projectCreateOpts, slug string) (projectCreateResult, error) {
	projDir := filepath.Join(project.ActiveDir(), slug)
	manifestPath := project.ManifestPath(project.ActiveDir(), slug)
	if _, err := os.Stat(manifestPath); err == nil {
		return projectCreateResult{}, fmt.Errorf("project %q already exists. Use: relay resume %s", slug, slug)
	}
	if err := requireArchivedSlugAvailable(slug); err != nil {
		return projectCreateResult{}, err
	}

	repoRoot := opts.repo
	if repoRoot == "" {
		var err error
		repoRoot, err = gitx.RepoRoot()
		if err != nil {
			return projectCreateResult{}, fmt.Errorf("locate repo root: %w", err)
		}
	}
	if repoRoot == "" {
		return projectCreateResult{}, fmt.Errorf("not in a git repository")
	}

	baseBranch := strings.TrimSpace(opts.base)
	explicitBase := baseBranch != ""
	if baseBranch == "" {
		baseBranch = gitx.DetectDefaultBranch(repoRoot)
	}
	if baseBranch == "" {
		return projectCreateResult{}, fmt.Errorf("could not determine default branch (no origin/HEAD, main, or master)")
	}

	startPoint := baseBranch
	remoteBaseSHA := ""
	if !explicitBase && gitx.HasOrigin(repoRoot) && gitx.ValidBranchName(repoRoot, baseBranch) {
		if out, err := gitx.Fetch(repoRoot, baseBranch); err != nil {
			ui.Warn("%s\n%s", err, out)
		}
		remoteRef := "origin/" + baseBranch
		if remoteSHA := gitx.RevParse(repoRoot, remoteRef); remoteSHA != "" {
			startPoint = remoteRef
			remoteBaseSHA = remoteSHA
		}
	}
	if gitx.RevParse(repoRoot, startPoint) == "" {
		return projectCreateResult{}, fmt.Errorf("base %q does not resolve to a commit", baseBranch)
	}

	cfg, err := config.EnsureForAgent(opts.agent)
	if err != nil {
		return projectCreateResult{}, err
	}

	a, err := agent.Get(agent.ResolveName(opts.agent, "", cfg.DefaultAgent))
	if err != nil {
		return projectCreateResult{}, err
	}

	branch := cfg.BranchPrefix + slug
	worktreeDir := filepath.Join(repoRoot, ".worktrees", cfg.WorktreePrefix()+slug)

	// A branch, worktree, or project dir with no valid manifest is leftover
	// state from an interrupted or failed setup (e.g. Ctrl+C before the manifest
	// was written). Detect it and offer to reclaim so the same slug is reusable.
	branchExists, err := gitx.LocalBranchExists(repoRoot, branch)
	if err != nil {
		return projectCreateResult{}, fmt.Errorf("inspect project branch %q: %w", branch, err)
	}
	if branchExists || pathExists(worktreeDir) || pathExists(projDir) {
		// "safe" leftovers can be reclaimed without prompting non-interactively:
		// the branch has no unique commits AND the worktree holds no uncommitted
		// or untracked work. Anything else needs explicit consent (--reclaim or a
		// TTY prompt) so we never silently discard the user's changes.
		safe := (!branchExists || branchMerged(repoRoot, branch, baseBranch)) &&
			worktreeReclaimSafe(repoRoot, worktreeDir)
		proceed, err := decideReclaim(opts.reclaim, slug, branch, worktreeDir, projDir, branchExists, safe)
		if err != nil {
			return projectCreateResult{}, err
		}
		if !proceed {
			return projectCreateResult{}, fmt.Errorf("aborted: leftover state for %q is still present. Reclaim it, choose a different name with -n, or remove it manually", slug)
		}
		if err := reclaimLeftovers(repoRoot, branch, worktreeDir, projDir); err != nil {
			return projectCreateResult{}, err
		}
	}

	startSHA := gitx.RevParse(repoRoot, startPoint)

	if err := gitx.WorktreeAdd(repoRoot, worktreeDir, branch, startPoint); err != nil {
		return projectCreateResult{}, err
	}

	if err := os.MkdirAll(projDir, 0755); err != nil {
		return projectCreateResult{}, fmt.Errorf("create project dir: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	wf := opts.workflow
	if wf == "" {
		wf = defaultWorkflow
	}
	deliveryMode := opts.deliveryMode
	if deliveryMode == "" {
		deliveryMode = project.DeliveryModeAdaptive
	}
	m := project.Manifest{
		Slug:            slug,
		Title:           opts.task,
		Repo:            repoRoot,
		Branch:          branch,
		Agent:           a.Name(),
		BaseBranch:      baseBranch,
		StartSHA:        startSHA,
		RemoteBaseSHA:   remoteBaseSHA,
		Worktree:        &worktreeDir,
		Status:          "initialized",
		Workflow:        wf,
		DeliveryMode:    deliveryMode,
		Program:         opts.program,
		ProgramItem:     opts.programItem,
		Phase:           "plan",
		Created:         now,
		PR:              project.PRInfo{},
		PhasesCompleted: []string{"init"},
		PhasesRemaining: project.AllPhases,
	}
	if err := project.Save(project.ManifestPath(project.ActiveDir(), slug), m); err != nil {
		return projectCreateResult{}, err
	}

	files := map[string]string{
		"task.md":  "# Task\n\n" + opts.task + "\n",
		"notes.md": "# " + slug + " — Notes\n\nScratchpad for ideas, context, and observations.\n",
		"todos.md": "# " + slug + " — TODOs\n\n- [ ] ...\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(projDir, name), []byte(content), 0644); err != nil {
			return projectCreateResult{}, fmt.Errorf("write %s: %w", name, err)
		}
	}

	return projectCreateResult{
		manifest:    m,
		projectDir:  projDir,
		worktreeDir: worktreeDir,
		agent:       a,
		config:      cfg,
	}, nil
}

func requireArchivedSlugAvailable(slug string) error {
	archivedDir := filepath.Join(project.ArchivedDir(), slug)
	if _, err := os.Lstat(archivedDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect archived project metadata %s: %w", archivedDir, err)
	}
	return fmt.Errorf(
		"project %q cannot be created because archived metadata still exists at %s; "+
			"choose a different project name, or move/remove that archived directory after preserving any history you need",
		slug, archivedDir,
	)
}

// pathExists reports whether a filesystem path exists (file, dir, or symlink).
func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// worktreeReclaimSafe reports whether the leftover worktree at dir can be
// removed without discarding user work. An absent dir is safe; a registered
// worktree is safe only when it is clean (no uncommitted/untracked changes); a
// leftover directory that git does not track is safe only when it is empty.
func worktreeReclaimSafe(repoRoot, dir string) bool {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return true
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return false
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
	branchRef := "refs/heads/" + branch
	remoteBaseRef := "refs/remotes/origin/" + base
	if gitx.HasOrigin(repo) && gitx.RevParse(repo, remoteBaseRef) != "" {
		if gitx.IsBranchReachable(repo, branchRef, remoteBaseRef) {
			return true
		}
	}
	return gitx.IsBranchReachable(repo, branchRef, "refs/heads/"+base)
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
		if err := gitx.WorktreeReclaim(repoRoot, worktreeDir, true); err != nil {
			return fmt.Errorf("reclaim worktree %s: %w", worktreeDir, err)
		}
		fmt.Printf("  %s %s\n", ui.Color(ui.Dim, "Removed worktree:"), worktreeDir)
	}
	branchExists, err := gitx.LocalBranchExists(repoRoot, branch)
	if err != nil {
		return fmt.Errorf("inspect reclaim branch %q: %w", branch, err)
	}
	if branchExists {
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
