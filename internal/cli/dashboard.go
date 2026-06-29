package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/ronaknnathani/relay/internal/dashboard"
	"github.com/ronaknnathani/relay/internal/project"
	"github.com/ronaknnathani/relay/internal/ui"
	"github.com/spf13/cobra"
)

func newCmdDashboard() *cobra.Command {
	return &cobra.Command{
		Use:   "dashboard",
		Short: "Open the HTML dashboard",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDashboard()
		},
	}
}

func runDashboard() error {
	repo := dashboard.RepoDirFromExe()
	if repo == "" {
		return fmt.Errorf("could not determine repo location from binary path")
	}
	active, err := project.LoadAll(project.ActiveDir())
	if err != nil {
		return err
	}
	archived, err := project.LoadAll(project.ArchivedDir())
	if err != nil {
		return err
	}
	if err := dashboard.Write(repo, active, archived); err != nil {
		return err
	}
	dashPath := filepath.Join(repo, "dashboard", "index.html")
	if _, err := os.Stat(dashPath); err != nil {
		return fmt.Errorf("dashboard not found at %s: %w", dashPath, err)
	}
	opener := "open"
	if runtime.GOOS == "linux" {
		opener = "xdg-open"
	}
	if err := exec.Command(opener, dashPath).Run(); err != nil {
		return fmt.Errorf("%s %s: %w", opener, dashPath, err)
	}
	fmt.Printf("  %s\n", ui.Color(ui.Dim, "Dashboard opened in browser."))
	return nil
}
