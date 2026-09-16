package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/prwatch"
	"github.com/spf13/cobra"
)

var (
	readPRForRecording = func(
		ctx context.Context,
		worktree string,
		number int,
	) (prwatch.PullRequest, error) {
		return prwatch.NewClient(prwatch.NewCLIRunner(0), worktree).PullRequest(ctx, number)
	}
	findPRsForRecording = func(
		ctx context.Context,
		worktree string,
		head string,
		base string,
	) ([]prwatch.PullRequest, error) {
		return prwatch.NewClient(prwatch.NewCLIRunner(0), worktree).
			FindOpenPullRequests(ctx, head, base)
	}
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
		newCmdStateDispatch(),
		newCmdStateWorker(),
		newCmdStateFinish(),
		newCmdStateEvidence(),
		newCmdStateAdvance(),
		newCmdStatePR(),
		newCmdStateFinal(),
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
	ws, _, err := loadStateAt(slug)
	return ws, err
}

func loadStateAt(slug string) (project.WorkflowState, string, error) {
	if err := project.ValidateSlug(slug); err != nil {
		return project.WorkflowState{}, "", err
	}
	manifestPath, err := project.Find(slug)
	if err != nil {
		path := project.StatePath(slug)
		ws, stateErr := project.LoadState(path)
		if errors.Is(stateErr, fs.ErrNotExist) {
			return project.WorkflowState{}, "", fmt.Errorf(
				"no state for %q (run `relay state init %s` first)", slug, slug,
			)
		}
		if stateErr != nil {
			return project.WorkflowState{}, "", stateErr
		}
		if ws.Slug != slug {
			return project.WorkflowState{}, "", fmt.Errorf(
				"state slug %q does not match selected project %q", ws.Slug, slug,
			)
		}
		return ws, path, nil
	}
	manifest, err := project.Load(manifestPath)
	if err != nil {
		return project.WorkflowState{}, "", err
	}
	if manifest.Slug != slug {
		return project.WorkflowState{}, "", fmt.Errorf(
			"manifest slug %q does not match selected project %q", manifest.Slug, slug,
		)
	}
	path := filepath.Join(filepath.Dir(manifestPath), "state.json")
	ws, err := project.LoadState(path)
	if errors.Is(err, fs.ErrNotExist) {
		return project.WorkflowState{}, "", fmt.Errorf(
			"no state for %q (run `relay state init %s` first)", slug, slug,
		)
	}
	if err != nil {
		return project.WorkflowState{}, "", err
	}
	if ws.Slug != slug {
		return project.WorkflowState{}, "", fmt.Errorf(
			"state slug %q does not match selected project %q", ws.Slug, slug,
		)
	}
	return ws, path, nil
}

func newCmdStateInit() *cobra.Command {
	var workflow, phases string
	cmd := &cobra.Command{
		Use:   "init <slug>",
		Short: "Initialize state.json for a workflow run (every phase pending)",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			slug := args[0]
			if err := project.ValidateSlug(slug); err != nil {
				return err
			}
			ws, err := project.NewState(slug, workflow, splitPhases(phases))
			if err != nil {
				return err
			}
			statePath := project.StatePath(slug)
			if _, err := os.Stat(statePath); err == nil {
				return fmt.Errorf(
					"state already initialized for %q (use `relay state next %s`)", slug, slug,
				)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("inspect state for %q: %w", slug, err)
			}
			coordinatorToken := ""
			if ws.UsesAdaptiveDelivery() {
				coordinatorToken, err = randomToken(32)
				if err != nil {
					return fmt.Errorf("generate coordinator capability: %w", err)
				}
				sum := sha256.Sum256([]byte(coordinatorToken))
				ws.CoordinatorHash = fmt.Sprintf("%x", sum)
			}
			if coordinatorToken != "" {
				if err := json.NewEncoder(os.Stdout).Encode(struct {
					Next             string `json:"next"`
					CoordinatorToken string `json:"coordinator_token"`
				}{Next: ws.Next(), CoordinatorToken: coordinatorToken}); err != nil {
					return fmt.Errorf("publish coordinator capability: %w", err)
				}
			}
			if err := project.CreateState(statePath, ws); err != nil {
				if errors.Is(err, fs.ErrExist) {
					return fmt.Errorf("state already initialized for %q (use `relay state next %s`)", slug, slug)
				}
				return err
			}
			if coordinatorToken != "" {
				return nil
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
			ws, statePath, err := loadStateAt(slug)
			if err != nil {
				return err
			}
			if ws.UsesAdaptiveDelivery() {
				return fmt.Errorf(
					"adaptive delivery cannot use state set; use state dispatch and state finish",
				)
			}
			if err := ws.SetPhase(phase, status, artifact, task); err != nil {
				return err
			}
			return project.SaveState(statePath, ws)
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
		RunE: func(command *cobra.Command, args []string) error {
			slug := args[0]
			ws, statePath, err := loadStateAt(slug)
			if err != nil {
				return err
			}
			if ws.UsesAdaptiveDelivery() {
				return fmt.Errorf(
					"adaptive delivery cannot use state advance; use state finish with an outcome",
				)
			}
			next, err := ws.Advance()
			if err != nil {
				return err
			}
			if err := project.SaveState(statePath, ws); err != nil {
				return err
			}
			fmt.Println(next)
			return nil
		},
	}
}

func newCmdStatePR() *cobra.Command {
	var number int
	var url, dispatchToken string
	var reconcile bool
	cmd := &cobra.Command{
		Use:   "pr <slug>",
		Short: "Record the pull request the project produced",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			slug := args[0]
			ws, statePath, err := loadStateAt(slug)
			if err != nil {
				return err
			}
			if ws.UsesAdaptiveDelivery() {
				if reconcile && (number > 0 || strings.TrimSpace(url) != "") {
					return fmt.Errorf("--reconcile cannot be combined with --number or --url")
				}
				if !reconcile && (number <= 0 || strings.TrimSpace(url) == "") {
					return fmt.Errorf("adaptive PR recording requires --number and --url together")
				}
				manifestPath, err := project.Find(slug)
				if err != nil {
					return err
				}
				manifest, err := project.Load(manifestPath)
				if err != nil {
					return err
				}
				if manifest.Worktree == nil || strings.TrimSpace(*manifest.Worktree) == "" {
					return fmt.Errorf("project %q has no worktree", slug)
				}
				var remotePR prwatch.PullRequest
				if reconcile {
					if ws.PendingPR.Number > 0 {
						remotePR, err = readPRForRecording(
							command.Context(), *manifest.Worktree, ws.PendingPR.Number,
						)
						if err != nil {
							return fmt.Errorf(
								"reconcile pending pull request #%d for project %q: %w",
								ws.PendingPR.Number, slug, err,
							)
						}
						number, url = ws.PendingPR.Number, ws.PendingPR.URL
					} else {
						prs, findErr := findPRsForRecording(
							command.Context(), *manifest.Worktree, manifest.Branch, manifest.BaseBranch,
						)
						if findErr != nil {
							return fmt.Errorf("reconcile pull request for project %q: %w", slug, findErr)
						}
						if len(prs) != 1 {
							return fmt.Errorf(
								"reconcile pull request for project %q: found %d open matches for %s -> %s",
								slug, len(prs), manifest.Branch, manifest.BaseBranch,
							)
						}
						remotePR = prs[0]
						number, url = remotePR.Number, remotePR.URL
					}
				} else {
					remotePR, err = readPRForRecording(command.Context(), *manifest.Worktree, number)
					if err != nil {
						return fmt.Errorf("verify pull request #%d for project %q: %w", number, slug, err)
					}
				}
				if err := validatePRCandidate(manifest, remotePR, number, url); err != nil {
					return err
				}
				if !reconcile && remotePR.State != prwatch.StateOpen {
					return fmt.Errorf("pull request #%d is %s, want OPEN", number, remotePR.State)
				}
				if remotePR.State == prwatch.StateClosed {
					return fmt.Errorf(
						"pending pull request #%d closed without merging; record a failed final result",
						number,
					)
				}
				if remotePR.State != prwatch.StateOpen && remotePR.State != prwatch.StateMerged {
					return fmt.Errorf(
						"pull request #%d is %s, want OPEN or MERGED", number, remotePR.State,
					)
				}
				ws.PendingPR = project.PRRef{Number: number, URL: url}
				snapshot, err := projectSnapshot(slug)
				if err != nil {
					return savePendingPRError(statePath, ws, err)
				}
				if err := ws.ValidateOpenPRReadiness(snapshot, true); err != nil {
					return savePendingPRError(statePath, ws, err)
				}
				if err := validatePRMetadata(
					manifest, snapshot, remotePR, remotePR.State == prwatch.StateOpen,
				); err != nil {
					return savePendingPRError(statePath, ws, err)
				}
				if err := consumePhaseDispatchToken(
					&ws, "open-pr", dispatchScopeResult, dispatchToken,
				); err != nil {
					return err
				}
				if ws.PR.Number > 0 &&
					(ws.PR.Number != number || ws.PR.URL != url) {
					return fmt.Errorf(
						"adaptive PR recording cannot replace existing PR %d at %s",
						ws.PR.Number, ws.PR.URL,
					)
				}
				dispatchID := ws.Phases["open-pr"].Dispatch.ID
				ws.PR = project.PRRef{Number: number, URL: url}
				ws.PendingPR = project.PRRef{}
				result := project.FinalResult{
					Status: "opened", PRNumber: number, PRURL: url,
					RouteRevision: ws.Route.Revision, RouteDigest: ws.Route.Digest,
					Snapshot: snapshot, DispatchID: dispatchID,
				}
				if err := project.ValidateFinalResult(result); err != nil {
					return err
				}
				ws.FinalResult = &result
				if err := ws.SetPhaseWithDelivery(
					"open-pr", project.PhaseDone, "", "", "", project.PhaseOutcomeMaterial,
					"", time.Now().UTC().Format(time.RFC3339),
				); err != nil {
					return err
				}
				return project.SaveState(statePath, ws)
			} else {
				if reconcile {
					return fmt.Errorf("--reconcile requires an adaptive delivery state")
				}
				ws.SetPR(number, url)
				if ws.PR.Number <= 0 || strings.TrimSpace(ws.PR.URL) == "" {
					ws.FinalResult = nil
					return project.SaveState(statePath, ws)
				}
			}
			result := project.FinalResult{
				Status: "opened", PRNumber: ws.PR.Number, PRURL: ws.PR.URL,
			}
			if err := project.ValidateFinalResult(result); err != nil {
				return err
			}
			ws.FinalResult = &result
			return project.SaveState(statePath, ws)
		},
	}
	cmd.Flags().IntVar(&number, "number", 0, "PR number")
	cmd.Flags().StringVar(&url, "url", "", "PR url")
	cmd.Flags().StringVar(&dispatchToken, "dispatch-token", "", "token returned by state dispatch")
	cmd.Flags().BoolVar(
		&reconcile, "reconcile", false,
		"find and verify the unique open PR for the recorded branch and base",
	)
	return cmd
}

func validatePRCandidate(
	manifest project.Manifest,
	remote prwatch.PullRequest,
	number int,
	url string,
) error {
	if remote.Number != number || remote.URL != url {
		return fmt.Errorf(
			"pull request identity mismatch: GitHub returned #%d at %s, want #%d at %s",
			remote.Number, remote.URL, number, url,
		)
	}
	if remote.Repo == "" {
		return fmt.Errorf("pull request #%d has no canonical repository identity", number)
	}
	parsedURL, err := neturl.Parse(remote.URL)
	if err != nil {
		return fmt.Errorf("parse pull request URL %q: %w", remote.URL, err)
	}
	wantPath := "/" + strings.Trim(remote.Repo, "/") + "/pull/" + fmt.Sprint(number)
	if strings.TrimSuffix(parsedURL.Path, "/") != wantPath {
		return fmt.Errorf(
			"pull request #%d URL %q does not belong to repository %q",
			number, remote.URL, remote.Repo,
		)
	}
	if remote.HeadRef != manifest.Branch {
		return fmt.Errorf(
			"pull request #%d head branch %q does not match project branch %q",
			number, remote.HeadRef, manifest.Branch,
		)
	}
	return nil
}

func validatePRMetadata(
	manifest project.Manifest,
	snapshot project.RepositorySnapshot,
	remote prwatch.PullRequest,
	requireBaseSHA bool,
) error {
	if manifest.RemoteBaseSHA == "" {
		return fmt.Errorf("project %q has no immutable remote base binding", manifest.Slug)
	}
	if remote.HeadSHA != snapshot.HeadSHA {
		return fmt.Errorf(
			"pull request #%d head SHA %s does not match reviewed head %s",
			remote.Number, remote.HeadSHA, snapshot.HeadSHA,
		)
	}
	if remote.BaseRef != manifest.BaseBranch || remote.BaseRef != snapshot.BaseRef {
		return fmt.Errorf(
			"pull request #%d base %q does not match bound project base %q",
			remote.Number, remote.BaseRef, manifest.BaseBranch,
		)
	}
	if requireBaseSHA &&
		(remote.BaseSHA != manifest.RemoteBaseSHA ||
			remote.BaseSHA != snapshot.BaseTipSHA) {
		return fmt.Errorf(
			"pull request #%d base SHA %s does not match reviewed remote base %s; "+
				"rebind with `relay route base %s --base %s --sha %s` before reconciliation",
			remote.Number, remote.BaseSHA, manifest.RemoteBaseSHA,
			manifest.Slug, manifest.BaseBranch, remote.BaseSHA,
		)
	}
	return nil
}

func savePendingPRError(path string, state project.WorkflowState, cause error) error {
	if err := project.SaveState(path, state); err != nil {
		return errors.Join(cause, fmt.Errorf("record pending PR for reconciliation: %w", err))
	}
	return fmt.Errorf(
		"%w; pull request #%d was retained for `relay state pr %s --reconcile`",
		cause, state.PendingPR.Number, state.Slug,
	)
}

func newCmdStateFinal() *cobra.Command {
	var reason, dispatchToken string
	cmd := &cobra.Command{
		Use:   "final <slug> <opened|blocked|failed>",
		Short: "Record the final delivery result",
		Args:  cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			ws, statePath, err := loadStateAt(args[0])
			if err != nil {
				return err
			}
			if ws.UsesAdaptiveDelivery() && args[1] == "opened" {
				return fmt.Errorf("adaptive opened results must be recorded with `relay state pr`")
			}
			result := project.FinalResult{Status: args[1], Reason: reason}
			if !ws.UsesAdaptiveDelivery() {
				result.PRNumber = ws.PR.Number
				result.PRURL = ws.PR.URL
			}
			if err := project.ValidateFinalResult(result); err != nil {
				return err
			}
			if result.Status == "opened" {
				if err := validateAdaptiveOpenPRSuccess(args[0], ws); err != nil {
					return err
				}
			}
			if ws.UsesAdaptiveDelivery() {
				if ws.PendingPR.Number > 0 || ws.PendingPR.URL != "" {
					manifestPath, err := project.Find(args[0])
					if err != nil {
						return err
					}
					manifest, err := project.Load(manifestPath)
					if err != nil {
						return err
					}
					if manifest.Worktree == nil || strings.TrimSpace(*manifest.Worktree) == "" {
						return fmt.Errorf("project %q has no worktree", args[0])
					}
					remotePR, err := readPRForRecording(
						command.Context(), *manifest.Worktree, ws.PendingPR.Number,
					)
					if err != nil {
						return fmt.Errorf(
							"verify pending pull request #%d: %w", ws.PendingPR.Number, err,
						)
					}
					if err := validatePRCandidate(
						manifest, remotePR, ws.PendingPR.Number, ws.PendingPR.URL,
					); err != nil {
						return err
					}
					switch remotePR.State {
					case prwatch.StateMerged:
						return fmt.Errorf(
							"pending pull request #%d is merged; reconcile it instead of recording failure",
							ws.PendingPR.Number,
						)
					case prwatch.StateClosed:
						ws.PendingPR = project.PRRef{}
					default:
						return fmt.Errorf(
							"record %s final result: pending pull request #%d is still %s",
							result.Status, ws.PendingPR.Number, remotePR.State,
						)
					}
				}
				if err := ws.ValidateAdaptiveFinish("open-pr", project.PhaseBlocked); err != nil {
					return fmt.Errorf("record %s final result: %w", result.Status, err)
				}
				if err := consumePhaseDispatchToken(
					&ws, "open-pr", dispatchScopeResult, dispatchToken,
				); err != nil {
					return fmt.Errorf("record %s final result: %w", result.Status, err)
				}
				ws.PR = project.PRRef{}
				ws.FinalResult = &result
				if err := ws.SetPhaseWithDelivery(
					"open-pr", project.PhaseBlocked, reason, "", "", "", "",
					time.Now().UTC().Format(time.RFC3339),
				); err != nil {
					return err
				}
				return project.SaveState(statePath, ws)
			}
			ws.FinalResult = &result
			return project.SaveState(statePath, ws)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "required reason for blocked or failed delivery")
	cmd.Flags().StringVar(&dispatchToken, "dispatch-token", "", "token returned by state dispatch")
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
			manifestPath, err := project.Find(slug)
			if err != nil {
				return err
			}
			msg := strings.Join(args[1:], " ")
			return project.AppendProgress(filepath.Join(filepath.Dir(manifestPath), "progress.md"), msg)
		},
	}
}
