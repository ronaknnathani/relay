// Command relay is the entrypoint for the AI-powered development workflow CLI.
// All logic lives under internal/cli; this file only wires the entrypoint
// and converts errors to exit codes.
package main

import (
	"fmt"
	"os"

	"github.com/ronaknnathani/relay/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "relay: %s\n", err)
		os.Exit(1)
	}
}
