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
		_, err := fmt.Fprintf(cmd.OutOrStdout(),
			"loamss %s\n"+
				"  commit:     %s\n"+
				"  built:      %s\n"+
				"  go version: %s\n"+
				"  platform:   %s/%s\n",
			version, commit, buildDate,
			runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return err
	},
}
