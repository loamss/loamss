package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// These are populated at build time via -ldflags. See the Makefile.
// Defaults make `go run` and `go install` produce useful output too.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Long:  "Print the runtime version, commit, build date, Go version, and platform.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "loamss %s\n", version)
		fmt.Fprintf(out, "  commit:     %s\n", commit)
		fmt.Fprintf(out, "  built:      %s\n", buildDate)
		fmt.Fprintf(out, "  go version: %s\n", runtime.Version())
		fmt.Fprintf(out, "  platform:   %s/%s\n", runtime.GOOS, runtime.GOARCH)
		return nil
	},
}
