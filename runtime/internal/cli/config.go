package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/loamss/loamss/runtime/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect and (eventually) modify the runtime configuration",
	Long: `The 'config' command tree exposes the resolved runtime configuration.

In v0.1, only 'config show' is implemented. 'config get', 'config set',
and 'config edit' land alongside the init command — see cli.md for the
full planned surface.`,
}

var configShowJSON bool

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the resolved runtime configuration",
	Long: `Print the configuration that the runtime would use, after merging
defaults, the config file (if present), and environment variables. Use
this to debug "is loamss seeing what I think it is" questions.

By default the output is YAML. Pass --json for JSON.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg := config.From(cmd.Context())
		if cfg == nil {
			return fmt.Errorf("no config attached to context (this is a programming error in the CLI wiring)")
		}

		out := cmd.OutOrStdout()
		if configShowJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(cfg)
		}
		enc := yaml.NewEncoder(out)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(cfg)
	},
}

func init() {
	configShowCmd.Flags().BoolVar(&configShowJSON, "json", false, "output as JSON instead of YAML")
	configCmd.AddCommand(configShowCmd)
	rootCmd.AddCommand(configCmd)
}
