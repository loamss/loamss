// Command loamss is the Loamss runtime CLI.
//
// See cli.md in the repo root for the full surface; only the `version`
// subcommand is wired up at this stage.
package main

import (
	"fmt"
	"os"

	"github.com/loamss/loamss/runtime/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
