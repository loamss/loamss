// Command loamss is the Loamss runtime CLI.
//
// See cli.md in the repo root for the full surface; subcommands land
// progressively as Phase 1 components are built.
package main

import (
	"os"

	"github.com/loamss/loamss/runtime/internal/cli"
)

func main() {
	// Cobra prints "Error: <message>" itself when a RunE returns an error
	// (SilenceErrors is false on rootCmd). We only need to convert the
	// non-nil-error case into a non-zero exit; the message has already
	// been emitted to stderr.
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
