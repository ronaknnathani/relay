package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/mailbox"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/spf13/cobra"
)

type programDispatchOpts struct {
	name   string
	agent  string
	launch bool
}

type dispatchChildPreparation struct {
	created projectCreateResult
	reused  bool
}

func newCmdProgramDispatch() *cobra.Command {
	var opts programDispatchOpts
	cmd := &cobra.Command{
		Use:   "dispatch <program> <item>",
		Short: "Create a managed Relay child project for a ready work item",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramDispatch(cmd.OutOrStdout(), args[0], args[1], opts)
		},
	}
	cmd.Flags().StringVarP(&opts.name, "name", "n", "", "custom child project slug")
	cmd.Flags().StringVar(&opts.agent, "agent", "", "coding agent to launch (default from program)")
	cmd.Flags().BoolVar(&opts.launch, "launch", false, "launch deliver-pr after dispatch state is durable")
	return cmd
}

func runProgramDispatch(out io.Writer, programSlug, itemID string, opts programDispatchOpts) error {
	path, p, err := loadActiveProgram(programSlug)
	if err != nil {
		return err
	}
	if p.State != program.StateActive {
		return fmt.Errorf("dispatch item %q: program %q is %s, want active", itemID, p.Slug, p.State)
	}
	item, ok := p.Item(itemID)
	if !ok {
		return fmt.Errorf("dispatch item %q: item not found", itemID)
	}
	dispatchAgent := opts.agent
	if dispatchAgent == "" {
		dispatchAgent = p.Agent
	}
	if _, err := requireManagedHerdr(
		"relay program dispatch", dispatchAgent, fmt.Sprintf("program %q", p.Slug), true,
	); err != nil {
		return err
	}
	childSlug := item.ProjectSlug
	if childSlug != "" && opts.name != "" && opts.name != childSlug {
		return fmt.Errorf("dispatch item %q: already linked to project %q; --name %q conflicts", itemID, childSlug, opts.name)
	}
	if childSlug == "" {
		childSlug = opts.name
	}
	if childSlug == "" {
		childSlug = defaultDispatchSlug(p.Slug, item.ID)
	}
	if err := project.ValidateSlug(childSlug); err != nil {
		return fmt.Errorf("dispatch item %q: child project slug: %w", itemID, err)
	}

	dispatched := p
	if err := dispatched.DispatchItem(item.ID, childSlug); err != nil {
		return err
	}
	programDir := filepath.Dir(path)
	if err := p.VerifyHashes(programDir); err != nil {
		return err
	}

	created, reused, err := prepareDispatchChild(p, item, childSlug, dispatchAgent, item.ProjectSlug != "")
	if err != nil {
		return fmt.Errorf("dispatch item %q: prepare child project: %w", itemID, err)
	}
	if err := bindDispatchIdentity(&dispatched, item.ID, created.manifest); err != nil {
		return fmt.Errorf("dispatch item %q: bind child project identity: %w", itemID, err)
	}
	if err := mailbox.Ensure(created.projectDir); err != nil {
		return fmt.Errorf("dispatch item %q: ensure child mailbox: %w", itemID, err)
	}
	contracts, err := copyDispatchContracts(programDir, created.projectDir, p.Contracts, item.ContractRefs)
	if err != nil {
		return fmt.Errorf("dispatch item %q: %w", itemID, err)
	}
	if err := writeDispatchAssignment(created.projectDir, p, item, contracts); err != nil {
		return fmt.Errorf("dispatch item %q: %w", itemID, err)
	}

	if err := program.Save(path, dispatched); err != nil {
		retained := "created and retained"
		if reused {
			retained = "reused and retained"
		}
		return fmt.Errorf(
			"child project %q was %s, but program dispatch state could not be saved: %w; repair with: relay program item link %s %s --project %s",
			childSlug, retained, err, p.Slug, item.ID, childSlug,
		)
	}
	progress := fmt.Sprintf("Dispatched item %s to project %s", item.ID, childSlug)
	if err := appendProgramProgress(programDir, progress); err != nil {
		return err
	}

	fmt.Fprintf(out, "Dispatched %s to %s\n", item.ID, childSlug)
	if !opts.launch {
		fmt.Fprintf(out, "relay program worker start %s %s\n", p.Slug, item.ID)
		return nil
	}
	fmt.Fprintf(out, "Launching %s...\n", created.agent.Name())
	systemPrompt := fmt.Sprintf(
		"Active relay project: %s. Workflow: %s. Delivery mode: %s.",
		childSlug, defaultWorkflow, deliveryModeForPrompt(created.manifest.DeliveryMode),
	)
	launchOpts := relayLaunchOptions(
		created.worktreeDir,
		created.projectDir,
		systemPrompt,
		childSlug,
		defaultWorkflow,
		item.Title,
		created.config.PermissionModeFor(created.agent.Name()),
	)
	return launchAgent(created.agent, launchOpts)
}

func bindDispatchIdentity(
	p *program.Program, itemID string, manifest project.Manifest,
) error {
	if manifest.Worktree == nil || strings.TrimSpace(*manifest.Worktree) == "" {
		return fmt.Errorf("child project %q has no worktree identity", manifest.Slug)
	}
	for i := range p.Items {
		if p.Items[i].ID != itemID {
			continue
		}
		p.Items[i].ProjectBranch = manifest.Branch
		p.Items[i].ProjectWorktree = *manifest.Worktree
		return p.Validate()
	}
	return fmt.Errorf("item not found")
}

func prepareDispatchChild(
	p program.Program,
	item program.WorkItem,
	childSlug, agentName string,
	reuseLinked bool,
) (projectCreateResult, bool, error) {
	if !reuseLinked {
		created, err := createDispatchChild(p, item, childSlug, agentName)
		return created, false, err
	}
	repoRoot, err := resolveDispatchItemRepository(item)
	if err != nil {
		return projectCreateResult{}, false, err
	}
	prepared, err := withProjectLifecycleLock(childSlug, func() (dispatchChildPreparation, error) {
		manifestPath := project.ManifestPath(project.ActiveDir(), childSlug)
		manifest, err := project.Load(manifestPath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return dispatchChildPreparation{}, err
			}
			created, createErr := createProjectLocked(projectCreateOpts{
				task:        item.Title,
				name:        childSlug,
				agent:       agentName,
				workflow:    defaultWorkflow,
				repo:        repoRoot,
				program:     p.Slug,
				programItem: item.ID,
			}, childSlug)
			return dispatchChildPreparation{created: created}, createErr
		}
		created, adoptErr := adoptDispatchChild(p, item, manifestPath, manifest, agentName)
		return dispatchChildPreparation{created: created, reused: true}, adoptErr
	})
	return prepared.created, prepared.reused, err
}

func adoptDispatchChild(
	p program.Program,
	item program.WorkItem,
	manifestPath string,
	manifest project.Manifest,
	agentName string,
) (projectCreateResult, error) {
	childSlug := manifest.Slug
	expectedSlug := item.ProjectSlug
	if expectedSlug == "" {
		expectedSlug = filepath.Base(filepath.Dir(manifestPath))
	}
	if childSlug != expectedSlug {
		return projectCreateResult{}, fmt.Errorf(
			"active child manifest %s has slug %q, want %q", manifestPath, childSlug, expectedSlug,
		)
	}
	if manifest.Repo != item.Repo {
		return projectCreateResult{}, fmt.Errorf(
			"active child %q repo %q does not match item repo %q", expectedSlug, manifest.Repo, item.Repo,
		)
	}
	if manifest.Program != "" && manifest.Program != p.Slug {
		return projectCreateResult{}, fmt.Errorf(
			"active child %q belongs to program %q, not %q", expectedSlug, manifest.Program, p.Slug,
		)
	}
	if manifest.ProgramItem != "" && manifest.ProgramItem != item.ID {
		return projectCreateResult{}, fmt.Errorf(
			"active child %q belongs to item %q, not %q", expectedSlug, manifest.ProgramItem, item.ID,
		)
	}
	if manifest.Branch == "" || manifest.Worktree == nil ||
		strings.TrimSpace(*manifest.Worktree) == "" {
		return projectCreateResult{}, fmt.Errorf(
			"active child %q has no complete branch/worktree identity", expectedSlug,
		)
	}
	if err := validateManagedChildResourceIdentity(item, manifest); err != nil {
		return projectCreateResult{}, fmt.Errorf(
			"active child %q cannot be safely reused: %w; inspect the child manifest and registered "+
				"worktree, then repair or remove the stale child metadata before retrying",
			expectedSlug, err,
		)
	}
	cfg, err := config.EnsureForAgent(agentName)
	if err != nil {
		return projectCreateResult{}, err
	}
	a, err := agent.Get(agent.ResolveName(agentName, "", cfg.DefaultAgent))
	if err != nil {
		return projectCreateResult{}, err
	}
	manifest.Program = p.Slug
	manifest.ProgramItem = item.ID
	if err := project.Save(manifestPath, manifest); err != nil {
		return projectCreateResult{}, err
	}
	return projectCreateResult{
		manifest:    manifest,
		projectDir:  filepath.Dir(manifestPath),
		worktreeDir: *manifest.Worktree,
		agent:       a,
		config:      cfg,
	}, nil
}

func validateManagedChildResourceIdentity(
	item program.WorkItem, manifest project.Manifest,
) error {
	if manifest.Repo != item.Repo {
		return fmt.Errorf(
			"repository identity does not match dispatch (manifest %q, item %q)",
			manifest.Repo, item.Repo,
		)
	}
	if manifest.Worktree == nil || strings.TrimSpace(*manifest.Worktree) == "" ||
		strings.TrimSpace(manifest.Branch) == "" {
		return fmt.Errorf("manifest has no complete branch/worktree identity")
	}
	repoRoot, err := gitx.CanonicalRepositoryRoot(manifest.Repo)
	if err != nil {
		return fmt.Errorf("resolve repository root %s: %w", manifest.Repo, err)
	}
	worktree, err := gitx.CanonicalPath(strings.TrimSpace(*manifest.Worktree))
	if err != nil {
		return fmt.Errorf("resolve worktree %s: %w", *manifest.Worktree, err)
	}
	worktreeRoot := filepath.Join(repoRoot, ".worktrees")
	worktreeRoot, err = gitx.CanonicalPath(worktreeRoot)
	if err != nil {
		return fmt.Errorf("resolve managed worktree root %s: %w", worktreeRoot, err)
	}
	if filepath.Dir(worktree) != worktreeRoot {
		return fmt.Errorf(
			"worktree %s is not an exclusive direct child of %s",
			worktree, worktreeRoot,
		)
	}
	state, found, err := gitx.RegisteredWorktreeState(repoRoot, worktree)
	if err != nil {
		return fmt.Errorf("inspect registered worktree %s: %w", worktree, err)
	}
	if !found {
		return fmt.Errorf("worktree %s is not registered in repository %s", worktree, repoRoot)
	}
	expectedBranch := "refs/heads/" + manifest.Branch
	if state.Detached || state.Branch != expectedBranch {
		return fmt.Errorf(
			"worktree %s is attached to %q, want %q",
			worktree, state.Branch, expectedBranch,
		)
	}
	branchTip, found, err := gitx.LocalBranchTip(repoRoot, manifest.Branch)
	if err != nil {
		return fmt.Errorf("resolve branch %q tip: %w", manifest.Branch, err)
	}
	if !found {
		return fmt.Errorf("branch %q is not present", manifest.Branch)
	}
	if state.Head != branchTip {
		return fmt.Errorf(
			"worktree %s HEAD %s does not match branch %q tip %s",
			worktree, state.Head, manifest.Branch, branchTip,
		)
	}
	if err := requireExclusiveProjectResources(activeManifestPath(manifest), nil); err != nil {
		return err
	}
	return nil
}

func createDispatchChild(
	p program.Program,
	item program.WorkItem,
	childSlug, agentName string,
) (projectCreateResult, error) {
	repoRoot, err := resolveDispatchItemRepository(item)
	if err != nil {
		return projectCreateResult{}, err
	}
	return createProject(projectCreateOpts{
		task:        item.Title,
		name:        childSlug,
		agent:       agentName,
		workflow:    defaultWorkflow,
		repo:        repoRoot,
		program:     p.Slug,
		programItem: item.ID,
	})
}

func resolveDispatchItemRepository(item program.WorkItem) (string, error) {
	repoRoot, err := gitx.CanonicalRepositoryRoot(item.Repo)
	if err != nil {
		return "", fmt.Errorf(
			"resolve item repository %s: %w", item.Repo, err,
		)
	}
	return repoRoot, nil
}

func defaultDispatchSlug(programSlug, itemID string) string {
	const maxLength = 40
	suffix := "-" + itemID
	available := maxLength - len(suffix)
	prefix := programSlug
	if len(prefix) > available {
		var shortened strings.Builder
		for _, r := range prefix {
			if shortened.Len()+utf8.RuneLen(r) > available {
				break
			}
			shortened.WriteRune(r)
		}
		prefix = strings.TrimRight(shortened.String(), "-")
	}
	return prefix + suffix
}

func copyDispatchContracts(programDir, childDir string, contracts []program.Contract, refs []string) ([]program.Contract, error) {
	byRef := make(map[string]program.Contract, len(contracts))
	for _, contract := range contracts {
		byRef[contract.Ref] = contract
	}
	copied := make([]program.Contract, 0, len(refs))
	for _, ref := range refs {
		contract, ok := byRef[ref]
		if !ok {
			return nil, fmt.Errorf("copy contract %q: contract not found", ref)
		}
		if contract.Status != program.ContractApproved {
			return nil, fmt.Errorf("copy contract %q: status %q is not approved", ref, contract.Status)
		}
		source := filepath.Join(programDir, filepath.FromSlash(contract.Path))
		data, err := os.ReadFile(source)
		if err != nil {
			return nil, fmt.Errorf("copy contract %q: read %s: %w", ref, source, err)
		}
		if err := verifyContractBytes(ref, data, contract.SHA256); err != nil {
			return nil, err
		}
		target := filepath.Join(childDir, filepath.FromSlash(contract.Path))
		existing, err := os.ReadFile(target)
		if err == nil {
			if err := verifyContractBytes(ref, existing, contract.SHA256); err != nil {
				return nil, fmt.Errorf("verify existing contract snapshot at %s: %w", target, err)
			}
			copied = append(copied, contract)
			continue
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read existing contract snapshot %q at %s: %w", ref, target, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, fmt.Errorf("copy contract %q: create directory %s: %w", ref, filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, data, 0o444); err != nil {
			return nil, fmt.Errorf("copy contract %q to %s: %w", ref, target, err)
		}
		copiedData, err := os.ReadFile(target)
		if err != nil {
			return nil, fmt.Errorf("verify copied contract %q at %s: %w", ref, target, err)
		}
		if err := verifyContractBytes(ref, copiedData, contract.SHA256); err != nil {
			return nil, fmt.Errorf("verify copied contract at %s: %w", target, err)
		}
		copied = append(copied, contract)
	}
	return copied, nil
}

func verifyContractBytes(ref string, data []byte, want string) error {
	actual := fmt.Sprintf("%x", sha256.Sum256(data))
	if actual != want {
		return fmt.Errorf("verify contract %q: sha256 mismatch: got %s, want %s", ref, actual, want)
	}
	return nil
}

func writeDispatchAssignment(projectDir string, p program.Program, item program.WorkItem, contracts []program.Contract) error {
	dependencies := "-"
	if len(item.Dependencies) > 0 {
		dependencies = strings.Join(item.Dependencies, ", ")
	}
	var contractLines strings.Builder
	if len(contracts) == 0 {
		contractLines.WriteString("- None\n")
	}
	for _, contract := range contracts {
		fmt.Fprintf(&contractLines, "- `%s`\n  - SHA-256: `%s`\n  - Local metadata path: `%s`\n",
			contract.Ref, contract.SHA256, filepath.ToSlash(contract.Path))
	}
	content := fmt.Sprintf(`# Managed work assignment

Program: %s
Program goal: %s
Work item: %s
Title: %s
Priority: %s
Dependency IDs: %s

## Binding contracts

%s
These contracts define architecture and constraints, not a line-level implementation plan.
Run adaptive delivery route-first. Perform clarification and planning only when the persisted route
selects those phases. If planning is selected, send the required plan-review message below and stop
for the response before implementation. If planning is skipped, do not synthesize that handoff.
You remain responsible for repository delivery within the binding contracts.

## Escalation

Managed workers never write program state directly. Do not open program decisions or edit program files.
For every issue, deviation, review request, or pre-PR request, send durable mail with the exact command below and stop.

- PR capacity gate before open-pr:
  `+"`relay program can-open-pr %s %s`"+`
- Question for the tech lead:
  `+"`relay program message send %s %s --kind question --body \"<describe the issue and requested decision>\"`"+`
- Contract, scope, dependency, or risk conflict:
  `+"`relay program message send %s %s --kind conflict --body \"<describe the conflict, impact, and requested decision>\"`"+`
- Plan needing tech lead or CEO review:
  `+"`relay program message send %s %s --kind plan --body \"<describe the plan and requested review>\"`"+`
- Before requesting an open-PR grant, inspect your unread outbox:
  `+"`relay program message outbox %s %s --json`"+`
  Do not send another pr-open request while one is unread.
- If no unread pr-open request exists, send exactly one and stop:
  `+"`relay program message send %s %s --kind pr-open --body \"<request an open-PR capacity grant>\"`"+`
- On resume, proceed only after the inbox contains the tech lead's grant-approved instruction. Keep that
  instruction unread, run the recorded can-open-pr command, then run open-pr. If open-pr fails, leave
  the instruction unread; acknowledge the grant inbox message only after open-pr succeeds and the PR
  is recorded.
- A tech lead-worker conflict is escalated to the CEO for resolution. Do not continue the affected work while it is unresolved.
`,
		p.Slug, p.Title, item.ID, item.Title, item.Priority, dependencies, contractLines.String(),
		p.Slug, item.ID,
		p.Slug, item.ID,
		p.Slug, item.ID,
		p.Slug, item.ID,
		p.Slug, item.ID,
		p.Slug, item.ID,
	)
	path := filepath.Join(projectDir, "assignment.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write assignment %s: %w", path, err)
	}
	return nil
}
