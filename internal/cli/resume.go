package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
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
	m, state, err := project.LoadEffectiveProject(path)
	if err != nil {
		return err
	}
	if m.Worktree == nil || *m.Worktree == "" {
		return fmt.Errorf("project %q has no worktree", slug)
	}
	complete, err := resumeCompletionIsFresh(path, m, state)
	if err != nil {
		return err
	}
	if complete {
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
	coordinatorToken, err := rotateCoordinatorForResume(path, cmd)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Printf("  %s\n", ui.Color(ui.Bold+ui.White, "Resuming project"))
	ui.PrintField("Slug", slug)
	ui.PrintField("Workflow", cmd)
	fmt.Println()
	fmt.Printf("  %s\n", ui.Color(ui.Dim, fmt.Sprintf("Launching %s…", a.Name())))
	fmt.Println()

	deliveryMode := deliveryModeForPrompt(m.DeliveryMode)
	systemPrompt := fmt.Sprintf("Active relay project: %s. Workflow: %s. Delivery mode: %s.", slug, cmd, deliveryMode)
	if coordinatorToken != "" {
		handoffPath, err := writeCoordinatorHandoff(filepath.Dir(path), coordinatorToken)
		if err != nil {
			return err
		}
		systemPrompt += " Read and delete the coordinator capability file before continuing: " +
			handoffPath + ". Keep its contents out of worker prompts."
	}
	o := relayLaunchOptions(*m.Worktree, filepath.Dir(path), systemPrompt, slug, cmd, m.Title, cfg.PermissionModeFor(a.Name()))
	return launchAgent(a, o)
}

func resumeCompletionIsFresh(
	manifestPath string,
	manifest project.Manifest,
	state *project.WorkflowState,
) (bool, error) {
	if manifest.Phase != "done" {
		return false, nil
	}
	if state == nil || !state.UsesAdaptiveDelivery() {
		return true, nil
	}
	raw, err := project.Load(manifestPath)
	if err != nil {
		return false, err
	}
	snapshot, err := project.RepositorySnapshotForManifest(raw, filepath.Dir(manifestPath))
	if err != nil {
		return false, err
	}
	return state.Route != nil &&
		state.FinalResult != nil &&
		state.FinalResult.Status == "opened" &&
		state.Route.Snapshot == snapshot &&
		state.FinalResult.RouteRevision == state.Route.Revision &&
		state.FinalResult.RouteDigest == state.Route.Digest &&
		state.FinalResult.Snapshot == snapshot, nil
}

func writeCoordinatorHandoff(projectDir, token string) (string, error) {
	path := filepath.Join(projectDir, ".coordinator-capability")
	file, err := os.CreateTemp(projectDir, ".coordinator-capability-*")
	if err != nil {
		return "", fmt.Errorf("create coordinator capability handoff in %s: %w", projectDir, err)
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("secure coordinator capability handoff %s: %w", tempPath, err)
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write coordinator capability handoff %s: %w", tempPath, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close coordinator capability handoff %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", fmt.Errorf("publish coordinator capability handoff %s: %w", path, err)
	}
	return path, nil
}

func rotateCoordinatorForResume(manifestPath, workflow string) (string, error) {
	if workflow != "deliver-pr" {
		return "", nil
	}
	statePath := filepath.Join(filepath.Dir(manifestPath), "state.json")
	state, err := project.LoadState(statePath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !state.UsesAdaptiveDelivery() {
		return "", nil
	}
	token, err := randomToken(32)
	if err != nil {
		return "", fmt.Errorf("rotate coordinator capability for %q: %w", state.Slug, err)
	}
	sum := sha256.Sum256([]byte(token))
	state.CoordinatorHash = fmt.Sprintf("%x", sum)
	if err := project.SaveState(statePath, state); err != nil {
		return "", fmt.Errorf("persist coordinator capability for %q: %w", state.Slug, err)
	}
	return token, nil
}

func deliveryModeForPrompt(mode string) string {
	if mode == "" {
		return "legacy"
	}
	return mode
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
