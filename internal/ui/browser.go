package ui

import (
	"fmt"
	"os/exec"
	"runtime"
)

type browserRunner func(name string, args ...string) error

// OpenBrowser opens target with the operating system browser launcher.
func OpenBrowser(target string) error {
	return openBrowser(target, runtime.GOOS, func(name string, args ...string) error {
		return exec.Command(name, args...).Run()
	})
}

func openBrowser(target, goos string, run browserRunner) error {
	opener := "open"
	if goos == "linux" {
		opener = "xdg-open"
	}
	if err := run(opener, target); err != nil {
		return fmt.Errorf("%s %s: %w", opener, target, err)
	}
	return nil
}
