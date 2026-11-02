package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ronaknnathani/relay/internal/herdr"
	"github.com/spf13/cobra"
)

const desktopHooksFile = "relay-herdr-app.json"

func newCmdHerdrApp() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "herdr-app",
		Short: "Run Relay from the Herdr desktop app instead of the Herdr TUI",
		Long: "Relay detects the Herdr desktop app from HC_TERMINAL_ID and drives it through hc.\n" +
			"The app learns whether an agent is idle or working only from OSC 7501 status reports,\n" +
			"so install the agent hooks once with `relay herdr-app install-hooks`.",
	}
	cmd.AddCommand(newCmdHerdrAppReport(), newCmdHerdrAppInstallHooks())
	return cmd
}

func newCmdHerdrAppReport() *cobra.Command {
	var kind, app string
	cmd := &cobra.Command{
		Use:    "report <idle|working|done|blocked|error>",
		Short:  "Report the calling agent's status to the Herdr desktop app (hook target)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var input []byte
			if stdin, ok := cmd.InOrStdin().(*os.File); ok && !isTerminalReader(stdin) {
				input, _ = io.ReadAll(io.LimitReader(stdin, 1<<20))
			}
			// A hook failure must never interrupt the agent, so report and exit 0.
			if err := herdr.ReportDesktopStatus(args[0], kind, app, input); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "relay herdr-app report: %v\n", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "why the agent is blocked: permission, question or auth")
	cmd.Flags().StringVar(&app, "app", "", "agent name shown by the app")
	return cmd
}

func newCmdHerdrAppInstallHooks() *cobra.Command {
	var uninstall, print bool
	cmd := &cobra.Command{
		Use:   "install-hooks",
		Short: "Install Copilot CLI hooks that report agent status to the Herdr desktop app",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := filepath.Join(copilotHome(), "hooks", desktopHooksFile)
			if uninstall {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", path)
				return nil
			}
			relayPath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate the relay executable: %w", err)
			}
			if resolved, err := filepath.EvalSymlinks(relayPath); err == nil {
				relayPath = resolved
			}
			content, err := copilotDesktopHooks(relayPath)
			if err != nil {
				return err
			}
			if print {
				_, err := cmd.OutOrStdout().Write(content)
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, content, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Installed %s\nRestart running Copilot sessions to load the hooks.\n", path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "remove the hooks file")
	cmd.Flags().BoolVar(&print, "print", false, "print the hooks file instead of writing it")
	return cmd
}

func copilotHome() string {
	if home := os.Getenv("COPILOT_HOME"); home != "" {
		return home
	}
	return filepath.Join(os.Getenv("HOME"), ".copilot")
}

// copilotDesktopHooks maps Copilot CLI lifecycle events to desktop statuses.
// A turn starts with the user's prompt and ends at agentStop. sessionEnd clears
// the status, so a shell left behind in the tab is no longer an agent. postToolUse
// clears a blocked status once a permission prompt or question is answered.
// preToolUse is left out: it fires before the permission check, so it can't
// clear blocked, and it would run on every tool call.
func copilotDesktopHooks(relayPath string) ([]byte, error) {
	type hook struct {
		Type       string   `json:"type"`
		Exec       string   `json:"exec"`
		Args       []string `json:"args"`
		Matcher    string   `json:"matcher,omitempty"`
		TimeoutSec int      `json:"timeoutSec"`
	}
	report := func(state, kind, matcher string) []hook {
		args := []string{"herdr-app", "report", state, "--app", "copilot"}
		if kind != "" {
			args = append(args, "--kind", kind)
		}
		return []hook{{Type: "command", Exec: relayPath, Args: args, Matcher: matcher, TimeoutSec: 5}}
	}
	notification := append(
		report("blocked", "permission", "permission_prompt"),
		report("blocked", "question", "elicitation_dialog")...,
	)
	config := map[string]any{
		"version": 1,
		"hooks": map[string][]hook{
			"sessionStart":        report("idle", "", ""),
			"userPromptSubmitted": report("working", "", ""),
			"postToolUse":         report("working", "", ""),
			"postToolUseFailure":  report("working", "", ""),
			"agentStop":           report("idle", "", ""),
			"sessionEnd":          report("clear", "", ""),
			"notification":        notification,
		},
	}
	content, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}
