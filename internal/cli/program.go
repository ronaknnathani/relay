package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ronaknnathani/relay/internal/agent"
	"github.com/ronaknnathani/relay/internal/config"
	"github.com/ronaknnathani/relay/internal/gitx"
	"github.com/ronaknnathani/relay/internal/program"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/spf13/cobra"
)

type programNewOpts struct {
	goal       string
	name       string
	repo       string
	agent      string
	maxOpenPRs int
	noLaunch   bool
}

func newCmdProgram() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "program",
		Short: "Govern coordinated programs of Relay projects",
	}
	cmd.AddCommand(
		newCmdProgramNew(),
		newCmdProgramResume(),
		newCmdProgramStatus(),
		newCmdProgramQueue(),
		newCmdProgramSubmit(),
		newCmdProgramApprove(),
		newCmdProgramHold(),
		newCmdProgramRelease(),
		newCmdProgramFinish(),
		newCmdProgramAbandon(),
		newCmdProgramItem(),
		newCmdProgramContract(),
		newCmdProgramDecision(),
		newCmdProgramDispatch(),
		newCmdProgramCanOpenPR(),
		newCmdProgramTick(),
	)
	return cmd
}

func newCmdProgramNew() *cobra.Command {
	var opts programNewOpts
	cmd := &cobra.Command{
		Use:   "new <goal>",
		Short: "Create a governed program and launch its CTO",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.goal = strings.Join(args, " ")
			return runProgramNew(cmd.OutOrStdout(), opts)
		},
	}
	cmd.Flags().StringVarP(&opts.name, "name", "n", "", "custom program slug")
	cmd.Flags().StringVar(&opts.repo, "repo", "", "repository path (default current repository)")
	cmd.Flags().StringVar(&opts.agent, "agent", "", "coding agent to launch (default from config)")
	cmd.Flags().IntVar(&opts.maxOpenPRs, "max-open-prs", 3, "maximum concurrent recorded pull requests")
	cmd.Flags().BoolVar(&opts.noLaunch, "no-launch", false, "create the program without launching the CTO")
	return cmd
}

func runProgramNew(out io.Writer, opts programNewOpts) error {
	slug := opts.name
	if slug == "" {
		slug = project.DeriveSlug(opts.goal)
	}
	if slug == "" {
		return fmt.Errorf("could not derive slug from program goal")
	}
	if err := project.ValidateSlug(slug); err != nil {
		return fmt.Errorf("program slug: %w", err)
	}
	if err := ensureProgramDoesNotExist(slug); err != nil {
		return err
	}

	repoRoot, err := resolveProgramRepo(opts.repo)
	if err != nil {
		return err
	}
	cfg, err := config.EnsureForAgent(opts.agent)
	if err != nil {
		return err
	}
	a, err := agent.Get(agent.ResolveName(opts.agent, "", cfg.DefaultAgent))
	if err != nil {
		return err
	}
	p, err := program.New(slug, opts.goal, repoRoot, a.Name(), opts.maxOpenPRs)
	if err != nil {
		return fmt.Errorf("create program %q: %w", slug, err)
	}
	if err := program.Create(p); err != nil {
		return err
	}
	programDir := program.ProgramDir(program.ActiveDir(), slug)
	if err := createProgramFiles(programDir, p.Title); err != nil {
		if removeErr := os.RemoveAll(programDir); removeErr != nil {
			return errors.Join(err, fmt.Errorf("clean up program directory %s: %w", programDir, removeErr))
		}
		return err
	}

	fmt.Fprintf(out, "Program created\n")
	fmt.Fprintf(out, "  Program: %s\n", slug)
	fmt.Fprintf(out, "  Repo: %s\n", repoRoot)
	fmt.Fprintf(out, "  Directory: %s\n", programDir)
	if opts.noLaunch {
		return nil
	}

	fmt.Fprintf(out, "Launching %s as CTO...\n", a.Name())
	return launchAgent(a, programLaunchOptions(p, programDir, cfg.PermissionModeFor(a.Name())))
}

func newCmdProgramResume() *cobra.Command {
	var agentName string
	cmd := &cobra.Command{
		Use:   "resume <slug>",
		Short: "Launch a fresh CTO re-entry for an active program",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramResume(cmd.OutOrStdout(), args[0], agentName)
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "coding agent to launch (default from program)")
	return cmd
}

func runProgramResume(out io.Writer, slug, agentName string) error {
	path, p, err := loadActiveProgram(slug)
	if err != nil {
		return err
	}
	cfg, err := config.EnsureForAgent(agentName)
	if err != nil {
		return err
	}
	a, err := agent.Get(agent.ResolveName(agentName, p.Agent, cfg.DefaultAgent))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Program: %s\n", p.Slug)
	fmt.Fprintf(out, "State: %s\n", p.State)
	fmt.Fprintf(out, "Open decisions: %d\n", len(p.OpenDecisions()))
	fmt.Fprintf(out, "Launching %s as CTO...\n", a.Name())
	return launchAgent(a, programLaunchOptions(p, filepath.Dir(path), cfg.PermissionModeFor(a.Name())))
}

func loadActiveProgram(slug string) (string, program.Program, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return "", program.Program{}, fmt.Errorf("program slug: %w", err)
	}
	path := program.ManifestPath(program.ActiveDir(), slug)
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return "", program.Program{}, fmt.Errorf("stat active program %q at %s: %w", slug, path, err)
		}
		if _, archivedErr := os.Stat(program.ManifestPath(program.ArchivedDir(), slug)); archivedErr == nil {
			return "", program.Program{}, fmt.Errorf("program %q is not active", slug)
		} else if !os.IsNotExist(archivedErr) {
			return "", program.Program{}, fmt.Errorf("stat archived program %q: %w", slug, archivedErr)
		}
		return "", program.Program{}, fmt.Errorf("program %q not found", slug)
	}
	p, err := program.Load(path)
	if err != nil {
		return "", program.Program{}, err
	}
	return path, p, nil
}

type programStatusOpts struct {
	slug         string
	showArchived bool
	showAll      bool
	jsonOutput   bool
}

type programDetailOutput struct {
	Program program.Program `json:"program"`
	Plan    program.View    `json:"plan"`
}

type programQueueOutput struct {
	Program     string        `json:"program"`
	State       program.State `json:"state"`
	View        program.View  `json:"view"`
	OrphanIDs   []string      `json:"orphan_ids,omitempty"`
	NextCommand string        `json:"next_command"`
}

func newCmdProgramStatus() *cobra.Command {
	var opts programStatusOpts
	cmd := &cobra.Command{
		Use:   "status [slug]",
		Short: "Show program status",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.slug = args[0]
			}
			return runProgramStatus(cmd.OutOrStdout(), opts)
		},
	}
	cmd.Flags().BoolVar(&opts.jsonOutput, "json", false, "output as JSON")
	cmd.Flags().BoolVar(&opts.showArchived, "archived", false, "show archived programs")
	cmd.Flags().BoolVar(&opts.showAll, "all", false, "show active and archived programs")
	return cmd
}

func runProgramStatus(out io.Writer, opts programStatusOpts) error {
	if opts.slug != "" {
		path, err := program.Find(opts.slug)
		if err != nil {
			return err
		}
		p, err := program.Load(path)
		if err != nil {
			return err
		}
		views, err := buildProgramProjectViews(p)
		if err != nil {
			return err
		}
		detail := programDetailOutput{Program: p, Plan: p.Plan(views)}
		if opts.jsonOutput {
			return writeProgramJSON(out, detail)
		}
		renderProgramDetail(out, detail)
		return nil
	}

	var active, archived []program.Program
	var err error
	if opts.showAll || !opts.showArchived {
		active, err = program.LoadAll(program.ActiveDir())
		if err != nil {
			return err
		}
		sortPrograms(active)
	}
	if opts.showArchived || opts.showAll {
		archived, err = program.LoadAll(program.ArchivedDir())
		if err != nil {
			return err
		}
		sortPrograms(archived)
	}
	if opts.jsonOutput {
		return writeProgramJSON(out, struct {
			Active   []program.Program `json:"active"`
			Archived []program.Program `json:"archived"`
		}{Active: active, Archived: archived})
	}
	if opts.showAll || !opts.showArchived {
		renderProgramList(out, "Active programs", active)
	}
	if opts.showArchived || opts.showAll {
		renderProgramList(out, "Archived programs", archived)
	}
	return nil
}

func newCmdProgramQueue() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "queue <slug>",
		Short: "Show the derived program work queue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramQueue(cmd.OutOrStdout(), args[0], jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func runProgramQueue(out io.Writer, slug string, jsonOutput bool) error {
	path, err := program.Find(slug)
	if err != nil {
		return err
	}
	p, err := program.Load(path)
	if err != nil {
		return err
	}
	views, err := buildProgramProjectViews(p)
	if err != nil {
		return err
	}
	return printProgramQueue(out, p, p.Plan(views), jsonOutput)
}

func newCmdProgramTick() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "tick <slug>",
		Short: "Reconcile a program with read-only child project state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramTick(cmd.OutOrStdout(), args[0], jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func runProgramTick(out io.Writer, slug string, jsonOutput bool) error {
	path, p, err := loadActiveProgram(slug)
	if err != nil {
		return err
	}
	programDir := filepath.Dir(path)
	if err := p.VerifyHashes(programDir); err != nil {
		return err
	}
	views, err := buildProgramProjectViews(p)
	if err != nil {
		return err
	}
	before := make(map[string]program.ItemStatus, len(p.Items))
	for _, item := range p.Items {
		before[item.ID] = item.Status
	}
	result, err := p.Reconcile(views)
	if err != nil {
		return err
	}
	if result.Changed {
		if err := program.Save(path, p); err != nil {
			return err
		}
		for _, item := range p.Items {
			if previous := before[item.ID]; previous != item.Status {
				message := fmt.Sprintf("Item %s changed from %s to %s", item.ID, previous, item.Status)
				if err := program.AppendProgress(program.ProgressPath(programDir), message); err != nil {
					return err
				}
			}
		}
		p, err = program.Load(path)
		if err != nil {
			return err
		}
	}
	return printProgramQueueResult(out, p, p.Plan(views), result.OrphanIDs, jsonOutput)
}

func newCmdProgramSubmit() *cobra.Command {
	return &cobra.Command{
		Use:   "submit <slug>",
		Short: "Submit a draft program for approval",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramTransition(cmd.OutOrStdout(), args[0], program.StatePendingApproval, "", "Submitted for approval", false)
		},
	}
}

func newCmdProgramApprove() *cobra.Command {
	var by string
	cmd := &cobra.Command{
		Use:   "approve <slug>",
		Short: "Approve and activate a submitted program",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateProgramApproval(args[0]); err != nil {
				return err
			}
			return runProgramTransition(cmd.OutOrStdout(), args[0], program.StateActive, by, "Approved by "+by, false)
		},
	}
	cmd.Flags().StringVar(&by, "by", "ceo", "approver")
	return cmd
}

func validateProgramApproval(slug string) error {
	path, p, err := loadActiveProgram(slug)
	if err != nil {
		return err
	}
	goalPath := filepath.Join(filepath.Dir(path), "goal.md")
	goal, err := os.ReadFile(goalPath)
	if err != nil {
		return fmt.Errorf("approve program %q: read goal %s: %w", slug, goalPath, err)
	}
	if strings.Contains(string(goal), "_TBD_") {
		return fmt.Errorf("approve program %q: goal.md still contains _TBD_ sections", slug)
	}
	if len(p.Items) == 0 {
		return fmt.Errorf("approve program %q: at least one work item is required", slug)
	}
	if decisions := p.OpenDecisions(); len(decisions) > 0 {
		return fmt.Errorf("approve program %q: resolve open decision(s) first: %s", slug, decisionIDs(decisions))
	}
	return nil
}

func newCmdProgramHold() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "hold <slug>",
		Short: "Place an active program on hold",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("hold program %q: --reason is required", args[0])
			}
			return runProgramTransition(cmd.OutOrStdout(), args[0], program.StateHeld, "cto", "Placed on hold: "+reason, false)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason for the hold")
	return cmd
}

func newCmdProgramRelease() *cobra.Command {
	return &cobra.Command{
		Use:   "release <slug>",
		Short: "Release a held program",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramTransition(cmd.OutOrStdout(), args[0], program.StateActive, "cto", "Released from hold", false)
		},
	}
}

func newCmdProgramFinish() *cobra.Command {
	return &cobra.Command{
		Use:   "finish <slug>",
		Short: "Complete and archive a program",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProgramTransition(cmd.OutOrStdout(), args[0], program.StateCompleted, "cto", "Program completed", true)
		},
	}
}

func newCmdProgramAbandon() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "abandon <slug>",
		Short: "Abandon and archive a program",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("abandon program %q: --reason is required", args[0])
			}
			return runProgramTransition(cmd.OutOrStdout(), args[0], program.StateAbandoned, "cto", "Program abandoned: "+reason, true)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason for abandoning the program")
	return cmd
}

func runProgramTransition(out io.Writer, slug string, next program.State, by, progressMessage string, archive bool) error {
	path, p, err := loadActiveProgram(slug)
	if err != nil {
		return err
	}
	if err := p.Transition(next, by); err != nil {
		return err
	}
	if err := program.Save(path, p); err != nil {
		return err
	}
	if err := program.AppendProgress(program.ProgressPath(filepath.Dir(path)), progressMessage); err != nil {
		return err
	}
	if archive {
		if err := program.Archive(slug); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Program %s is now %s\n", slug, next)
	return nil
}

func newCmdProgramContract() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "contract",
		Short: "Manage immutable program contracts",
	}
	cmd.AddCommand(
		newCmdProgramContractPublish(),
		newCmdProgramContractApprove(),
		newCmdProgramContractReject(),
		newCmdProgramContractList(),
	)
	return cmd
}

func newCmdProgramContractPublish() *cobra.Command {
	var sourcePath string
	cmd := &cobra.Command{
		Use:   "publish <program> <name>",
		Short: "Publish a new immutable contract version",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(sourcePath) == "" {
				return fmt.Errorf("publish contract: --file is required")
			}
			return runProgramContractPublish(cmd.OutOrStdout(), args[0], strings.Join(args[1:], " "), sourcePath)
		},
	}
	cmd.Flags().StringVar(&sourcePath, "file", "", "source contract file")
	return cmd
}

func runProgramContractPublish(out io.Writer, slug, name, sourcePath string) error {
	path, p, err := loadActiveProgram(slug)
	if err != nil {
		return err
	}
	contract, err := p.PublishContract(filepath.Dir(path), name, sourcePath)
	if err != nil {
		return err
	}
	if err := program.Save(path, p); err != nil {
		removeErr := os.Remove(filepath.Join(filepath.Dir(path), filepath.FromSlash(contract.Path)))
		return errors.Join(err, removeErr)
	}
	decisionID := ""
	for _, decision := range p.Decisions {
		if decision.ContractRef == contract.Ref && decision.ResolvedAt == "" {
			decisionID = decision.ID
		}
	}
	message := fmt.Sprintf("Published contract %s (sha256 %s)", contract.Ref, contract.SHA256)
	if decisionID != "" {
		message += "; opened decision " + decisionID
	}
	if err := program.AppendDecisionLog(program.DecisionLogPath(filepath.Dir(path)), message); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s %s\n", contract.Ref, contract.SHA256)
	return nil
}

func newCmdProgramContractApprove() *cobra.Command {
	var by string
	cmd := &cobra.Command{
		Use:   "approve <program> <ref>",
		Short: "Approve a published contract",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, p, err := loadActiveProgram(args[0])
			if err != nil {
				return err
			}
			if err := p.ApproveContract(args[1], by); err != nil {
				return err
			}
			if err := program.Save(path, p); err != nil {
				return err
			}
			if err := program.AppendDecisionLog(program.DecisionLogPath(filepath.Dir(path)),
				fmt.Sprintf("Contract %s approved by %s", args[1], by)); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), args[1])
			return nil
		},
	}
	cmd.Flags().StringVar(&by, "by", "ceo", "approver")
	return cmd
}

func newCmdProgramContractReject() *cobra.Command {
	var by, reason string
	cmd := &cobra.Command{
		Use:   "reject <program> <ref>",
		Short: "Reject a published contract",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("reject contract %q: --reason is required", args[1])
			}
			path, p, err := loadActiveProgram(args[0])
			if err != nil {
				return err
			}
			if err := p.RejectContract(args[1], by, reason); err != nil {
				return err
			}
			if err := program.Save(path, p); err != nil {
				return err
			}
			message := fmt.Sprintf("Contract %s rejected by %s: %s", args[1], by, reason)
			if err := program.AppendDecisionLog(program.DecisionLogPath(filepath.Dir(path)), message); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), args[1])
			return nil
		},
	}
	cmd.Flags().StringVar(&by, "by", "ceo", "rejector")
	cmd.Flags().StringVar(&reason, "reason", "", "reason for rejection")
	return cmd
}

func newCmdProgramContractList() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list <program>",
		Short: "List program contracts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := program.Find(args[0])
			if err != nil {
				return err
			}
			p, err := program.Load(path)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeProgramJSON(cmd.OutOrStdout(), p.Contracts)
			}
			for _, contract := range p.Contracts {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %-8s %s\n", contract.Ref, contract.Status, contract.SHA256)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func newCmdProgramDecision() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "decision",
		Short: "Manage program governance decisions",
	}
	cmd.AddCommand(
		newCmdProgramDecisionOpen(),
		newCmdProgramDecisionResolve(),
	)
	return cmd
}

func newCmdProgramDecisionOpen() *cobra.Command {
	var question, itemID, kind, raisedBy, optionsText string
	cmd := &cobra.Command{
		Use:   "open <program>",
		Short: "Open a governance decision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(question) == "" {
				return fmt.Errorf("open decision: --question is required")
			}
			path, p, err := loadActiveProgram(args[0])
			if err != nil {
				return err
			}
			decision, err := p.OpenDecision(program.Decision{
				Kind:     program.DecisionKind(kind),
				RaisedBy: program.RaisedBy(raisedBy),
				ItemID:   itemID,
				Question: question,
				Options:  parseProgramOptions(optionsText),
			})
			if err != nil {
				return err
			}
			if err := program.Save(path, p); err != nil {
				return err
			}
			message := fmt.Sprintf("Opened decision %s: %s", decision.ID, decision.Question)
			if decision.ItemID != "" {
				message += " (item " + decision.ItemID + ")"
			}
			if err := program.AppendDecisionLog(program.DecisionLogPath(filepath.Dir(path)), message); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), decision.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&question, "question", "", "decision question")
	cmd.Flags().StringVar(&itemID, "item", "", "related work item ID")
	cmd.Flags().StringVar(&kind, "kind", string(program.DecisionQuestion), "decision kind")
	cmd.Flags().StringVar(&raisedBy, "raised-by", string(program.RaisedByCTO), "decision raiser")
	cmd.Flags().StringVar(&optionsText, "options", "", "pipe-separated decision options")
	return cmd
}

func newCmdProgramDecisionResolve() *cobra.Command {
	var answer, by string
	cmd := &cobra.Command{
		Use:   "resolve <program> <id>",
		Short: "Resolve an open governance decision",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(answer) == "" {
				return fmt.Errorf("resolve decision %q: --answer is required", args[1])
			}
			path, p, err := loadActiveProgram(args[0])
			if err != nil {
				return err
			}
			if err := p.ResolveDecision(args[1], answer, by); err != nil {
				return err
			}
			if err := program.Save(path, p); err != nil {
				return err
			}
			if err := program.AppendDecisionLog(program.DecisionLogPath(filepath.Dir(path)),
				fmt.Sprintf("Resolved decision %s by %s: %s", args[1], by, answer)); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), args[1])
			return nil
		},
	}
	cmd.Flags().StringVar(&answer, "answer", "", "decision answer")
	cmd.Flags().StringVar(&by, "by", "ceo", "decision maker")
	return cmd
}

func parseProgramOptions(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	raw := strings.Split(value, "|")
	options := make([]string, 0, len(raw))
	for _, option := range raw {
		if option = strings.TrimSpace(option); option != "" {
			options = append(options, option)
		}
	}
	return options
}

func printProgramQueue(out io.Writer, p program.Program, view program.View, jsonOutput bool) error {
	return printProgramQueueResult(out, p, view, nil, jsonOutput)
}

func printProgramQueueResult(
	out io.Writer,
	p program.Program,
	view program.View,
	orphanIDs []string,
	jsonOutput bool,
) error {
	queue := programQueueOutput{
		Program:     p.Slug,
		State:       p.State,
		View:        view,
		OrphanIDs:   orphanIDs,
		NextCommand: nextProgramCommand(p, view),
	}
	if len(orphanIDs) > 0 {
		queue.NextCommand = fmt.Sprintf(
			`relay program item block %s %s --reason "linked child project is missing or archived without verified merge"`,
			p.Slug, orphanIDs[0],
		)
	}
	if jsonOutput {
		return writeProgramJSON(out, queue)
	}
	renderProgramView(out, queue)
	return nil
}

func buildProgramProjectViews(p program.Program) ([]program.ProjectView, error) {
	manifests, err := project.LoadAll(project.ActiveDir())
	if err != nil {
		return nil, err
	}
	views := make([]program.ProjectView, 0, len(manifests))
	active := make(map[string]bool, len(manifests))
	for _, manifest := range manifests {
		if manifest.Repo != p.Repo {
			continue
		}
		view, err := activeProjectView(manifest)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
		active[manifest.Slug] = true
	}

	linked := make(map[string]bool)
	for _, item := range p.Items {
		if item.ProjectSlug == "" || active[item.ProjectSlug] || linked[item.ProjectSlug] {
			continue
		}
		linked[item.ProjectSlug] = true
		path := project.ManifestPath(project.ArchivedDir(), item.ProjectSlug)
		manifest, err := project.Load(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				views = append(views, program.ProjectView{
					Slug:     item.ProjectSlug,
					Repo:     p.Repo,
					Orphaned: true,
				})
				continue
			}
			return nil, err
		}
		hasPR, prRef, err := projectPR(manifest, "")
		if err != nil {
			return nil, err
		}
		views = append(views, program.ProjectView{
			Slug:     manifest.Slug,
			Repo:     manifest.Repo,
			HasPR:    hasPR,
			PRRef:    prRef,
			Merged:   manifest.Merged,
			Archived: true,
			Orphaned: !manifest.Merged,
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Slug < views[j].Slug })
	return views, nil
}

func activeProjectView(manifest project.Manifest) (program.ProjectView, error) {
	hasPR, prRef, err := projectPR(manifest, project.StatePath(manifest.Slug))
	if err != nil {
		return program.ProjectView{}, err
	}
	base := manifest.BaseBranch
	if base == "" {
		base = gitx.DetectDefaultBranch(manifest.Repo)
	}
	baseRef := base
	if base != "" && gitx.HasOrigin(manifest.Repo) && gitx.RevParse(manifest.Repo, "origin/"+base) != "" {
		baseRef = "origin/" + base
	}
	merged := baseRef != "" && gitx.IsWorkMerged(manifest.Repo, manifest.Branch, baseRef, manifest.StartSHA)
	return program.ProjectView{
		Slug:   manifest.Slug,
		Repo:   manifest.Repo,
		HasPR:  hasPR,
		PRRef:  prRef,
		Merged: merged,
	}, nil
}

func projectPR(manifest project.Manifest, statePath string) (bool, string, error) {
	if statePath != "" {
		if _, err := os.Stat(statePath); err == nil {
			state, err := project.LoadState(statePath)
			if err != nil {
				return false, "", err
			}
			if state.PR.URL != "" {
				return true, state.PR.URL, nil
			}
			if state.PR.Number > 0 {
				return true, "#" + strconv.Itoa(state.PR.Number), nil
			}
		} else if !os.IsNotExist(err) {
			return false, "", fmt.Errorf("stat project state %s: %w", statePath, err)
		}
	}
	if manifest.PR.URL != nil && *manifest.PR.URL != "" {
		return true, *manifest.PR.URL, nil
	}
	if manifest.PR.Number != nil && *manifest.PR.Number > 0 {
		return true, "#" + strconv.Itoa(*manifest.PR.Number), nil
	}
	return false, "", nil
}

func nextProgramCommand(p program.Program, view program.View) string {
	switch {
	case strings.HasPrefix(view.NextAction, "resolve "):
		id := strings.TrimPrefix(view.NextAction, "resolve ")
		for _, decision := range p.Decisions {
			if decision.ID == id && decision.Kind == program.DecisionContract {
				return fmt.Sprintf("relay program contract approve %s %s --by ceo", p.Slug, decision.ContractRef)
			}
		}
		return fmt.Sprintf("relay program decision resolve %s %s --answer <answer>", p.Slug, id)
	case view.NextAction == "request approval":
		return "relay program submit " + p.Slug
	case view.NextAction == "approve program":
		return "relay program approve " + p.Slug
	case view.NextAction == "resume program":
		return "relay program release " + p.Slug
	case strings.HasPrefix(view.NextAction, "dispatch "):
		return fmt.Sprintf("relay program dispatch %s %s", p.Slug, strings.TrimPrefix(view.NextAction, "dispatch "))
	case view.NextAction == "reconcile in-flight work":
		return "relay program tick " + p.Slug
	case view.NextAction == "complete program":
		return "relay program finish " + p.Slug
	case len(view.Blocked) > 0:
		blocked := view.Blocked[0]
		return fmt.Sprintf("blocked: %s (%s)", blocked.Item.ID, strings.Join(blocked.Reasons, "; "))
	default:
		return "no action"
	}
}

func writeProgramJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode program JSON: %w", err)
	}
	return nil
}

func sortPrograms(programs []program.Program) {
	sort.Slice(programs, func(i, j int) bool { return programs[i].Slug < programs[j].Slug })
}

func renderProgramList(out io.Writer, label string, programs []program.Program) {
	fmt.Fprintf(out, "%s\n", label)
	for _, p := range programs {
		fmt.Fprintf(out, "  %-24s %-18s %s\n", p.Slug, p.State, p.Title)
	}
}

func renderProgramDetail(out io.Writer, detail programDetailOutput) {
	renderProgramView(out, programQueueOutput{
		Program:     detail.Program.Slug,
		State:       detail.Program.State,
		View:        detail.Plan,
		NextCommand: nextProgramCommand(detail.Program, detail.Plan),
	})
	fmt.Fprintf(out, "Title: %s\n", detail.Program.Title)
	fmt.Fprintf(out, "Repo: %s\n", detail.Program.Repo)
}

func renderProgramView(out io.Writer, queue programQueueOutput) {
	fmt.Fprintf(out, "Program: %s\n", queue.Program)
	fmt.Fprintf(out, "State: %s\n", queue.State)
	fmt.Fprintf(out, "Capacity: %d/%d open, %d available\n",
		queue.View.Capacity.Open, queue.View.Capacity.Limit, queue.View.Capacity.Available)
	fmt.Fprintf(out, "Ready: %s\n", workItemSummary(queue.View.Ready))
	fmt.Fprintf(out, "In flight: %s\n", workItemSummary(queue.View.InFlight))
	fmt.Fprintf(out, "Blocked: %d\n", len(queue.View.Blocked))
	if len(queue.OrphanIDs) > 0 {
		fmt.Fprintf(out, "Orphaned: %s\n", strings.Join(queue.OrphanIDs, ", "))
	}
	fmt.Fprintf(out, "Open decisions: %d\n", len(queue.View.OpenDecisions))
	fmt.Fprintf(out, "Next: %s\n", queue.NextCommand)
}

func workItemSummary(items []program.WorkItem) string {
	if len(items) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.ID+" "+item.Title)
	}
	return strings.Join(parts, "; ")
}

func ensureProgramDoesNotExist(slug string) error {
	for _, dir := range []string{program.ActiveDir(), program.ArchivedDir()} {
		path := program.ManifestPath(dir, slug)
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("program %q already exists", slug)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("check program %q at %s: %w", slug, path, err)
		}
	}
	return nil
}

func resolveProgramRepo(repo string) (string, error) {
	candidate := repo
	if candidate == "" {
		candidate = "."
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve repository path %q: %w", candidate, err)
	}
	command := exec.Command("git", "-C", absolute, "rev-parse", "--show-toplevel")
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("repository %s is not a git repository: %s", absolute, detail)
	}
	root := strings.TrimSpace(string(output))
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize repository root %s: %w", root, err)
	}
	return canonical, nil
}

func createProgramFiles(dir, title string) error {
	files := []struct {
		name    string
		content string
	}{
		{
			name: "goal.md",
			content: fmt.Sprintf("# %s\n\n## Approved outcome\n\n_TBD_\n\n## Priorities\n\n_TBD_\n\n"+
				"## Architecture\n\n_TBD_\n\n## Guardrails\n\n_TBD_\n", title),
		},
		{name: "decisions.md", content: "# Decisions\n\n"},
		{name: "progress.md", content: "# Progress\n\n- Program created in draft state.\n"},
	}
	for _, file := range files {
		path := filepath.Join(dir, file.name)
		handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return fmt.Errorf("create program file %s: %w", path, err)
		}
		if _, err := handle.WriteString(file.content); err != nil {
			return fmt.Errorf("write program file %s: %w", path, errors.Join(err, handle.Close()))
		}
		if err := handle.Close(); err != nil {
			return fmt.Errorf("close program file %s: %w", path, err)
		}
	}
	return nil
}

func programLaunchOptions(p program.Program, programDir, permissionMode string) agent.LaunchOptions {
	return agent.LaunchOptions{
		Worktree:       p.Repo,
		ProjectDir:     programDir,
		SystemPrompt:   fmt.Sprintf("Active relay program: %s. Role: CTO. Reconstruct governance state from the program directory before acting.", p.Slug),
		SessionName:    "relay:program:" + p.Slug,
		Command:        "cto",
		CommandArgs:    p.Slug,
		WorkflowGoal:   p.Title,
		PermissionMode: permissionMode,
	}
}
