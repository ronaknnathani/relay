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
	systemPrompt := fmt.Sprintf("Active relay project: %s. Workflow: %s. Mode: full.", childSlug, defaultWorkflow)
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
	manifestPath := project.ManifestPath(project.ActiveDir(), childSlug)
	manifest, err := project.Load(manifestPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return projectCreateResult{}, false, err
		}
		created, createErr := createDispatchChild(p, item, childSlug, agentName)
		return created, false, createErr
	}
	if manifest.Slug != childSlug {
		return projectCreateResult{}, false, fmt.Errorf(
			"active child manifest %s has slug %q, want %q", manifestPath, manifest.Slug, childSlug,
		)
	}
	if manifest.Repo != p.Repo {
		return projectCreateResult{}, false, fmt.Errorf(
			"active child %q repo %q does not match program repo %q", childSlug, manifest.Repo, p.Repo,
		)
	}
	if manifest.Program != "" && manifest.Program != p.Slug {
		return projectCreateResult{}, false, fmt.Errorf(
			"active child %q belongs to program %q, not %q", childSlug, manifest.Program, p.Slug,
		)
	}
	if manifest.ProgramItem != "" && manifest.ProgramItem != item.ID {
		return projectCreateResult{}, false, fmt.Errorf(
			"active child %q belongs to item %q, not %q", childSlug, manifest.ProgramItem, item.ID,
		)
	}
	cfg, err := config.EnsureForAgent(agentName)
	if err != nil {
		return projectCreateResult{}, false, err
	}
	a, err := agent.Get(agent.ResolveName(agentName, "", cfg.DefaultAgent))
	if err != nil {
		return projectCreateResult{}, false, err
	}
	manifest.Program = p.Slug
	manifest.ProgramItem = item.ID
	if err := project.Save(manifestPath, manifest); err != nil {
		return projectCreateResult{}, false, err
	}
	worktreeDir := ""
	if manifest.Worktree != nil {
		worktreeDir = *manifest.Worktree
	}
	return projectCreateResult{
		manifest:    manifest,
		projectDir:  filepath.Dir(manifestPath),
		worktreeDir: worktreeDir,
		agent:       a,
		config:      cfg,
	}, true, nil
}

func createDispatchChild(
	p program.Program,
	item program.WorkItem,
	childSlug, agentName string,
) (projectCreateResult, error) {
	return createProject(projectCreateOpts{
		task:        item.Title,
		name:        childSlug,
		agent:       agentName,
		workflow:    defaultWorkflow,
		repo:        p.Repo,
		program:     p.Slug,
		programItem: item.ID,
	})
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
You own clarify and plan and must perform both independently within the binding contracts.

## Escalation

Managed workers never write program state directly. Do not open program decisions or edit program files.
For every issue, deviation, review request, or pre-PR request, send durable mail with the exact command below and stop.

- PR capacity gate before open-pr:
  `+"`relay program can-open-pr %s %s`"+`
- Question for the CTO:
  `+"`relay program message send %s %s --kind question --body \"<describe the issue and requested decision>\"`"+`
- Contract, scope, dependency, or risk conflict:
  `+"`relay program message send %s %s --kind conflict --body \"<describe the conflict, impact, and requested decision>\"`"+`
- Plan needing CTO or CEO review:
  `+"`relay program message send %s %s --kind plan --body \"<describe the plan and requested review>\"`"+`
- Before requesting an open-PR grant, inspect your unread outbox:
  `+"`relay program message outbox %s %s --json`"+`
  Do not send another pr-open request while one is unread.
- If no unread pr-open request exists, send exactly one and stop:
  `+"`relay program message send %s %s --kind pr-open --body \"<request an open-PR capacity grant>\"`"+`
- On resume, proceed only after the inbox contains the CTO's grant-approved instruction. Keep that
  instruction unread, run the recorded can-open-pr command, then run open-pr. If open-pr fails, leave
  the instruction unread; acknowledge the grant inbox message only after open-pr succeeds and the PR
  is recorded.
- A CTO-worker conflict is escalated to the CEO for resolution. Do not continue the affected work while it is unresolved.
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
