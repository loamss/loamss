// Package cli wires the loamss command tree.
//
// The full CLI surface is documented in cli.md at the repo root.
// Subcommands land progressively as Phase 1 components are built.
package cli

import "github.com/spf13/cobra"

// rootCmd is the loamss binary's top-level command.
var rootCmd = &cobra.Command{
	Use:   "loamss",
	Short: "Personal data infrastructure",
	Long: `Loamss is open-source personal data infrastructure.

The runtime ingests data into user-owned storage, builds a durable
memory layer on top, and exposes governed views to MCP-speaking
consumers. See https://github.com/loamss/loamss for the specs.`,
	SilenceUsage: true,
}

// Execute runs the root command.
//
// Errors are returned to main, which prints them to stderr and
// sets a non-zero exit code. Cobra's default usage-on-error
// behavior is suppressed (SilenceUsage above) — usage is only
// printed when explicitly requested via --help.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
