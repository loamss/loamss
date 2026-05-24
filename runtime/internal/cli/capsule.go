package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/permission"
)

var capsuleCmd = &cobra.Command{
	Use:   "capsule",
	Short: "Inspect and install capsules",
	Long: `Capsules are packaged Loamss agents — sandboxed subprocesses that
expose MCP tools through the runtime. Each capsule declares its
required capabilities, the tools it provides, and the kind of model
it needs.

Subcommands:
  validate <path>   parse and validate a capsule's manifest

Install/list/show/uninstall and the subprocess host arrive in
subsequent commits. For now, validate is the standalone surface
capsule authors use to check their manifests before publishing.`,
}

// --- validate ----------------------------------------------------------

var capsuleValidateOffline bool

var capsuleValidateCmd = &cobra.Command{
	Use:   "validate <path>",
	Short: "Validate a capsule manifest against the v0.1 spec",
	Long: `Reads <path>/capsule.yaml (or <path> if it points directly at a
.yaml file), parses it, and runs the full validation suite:

  - spec_version is supported
  - name, version, author fields are well-formed
  - every declared capability is registered with the runtime
    (skipped with --offline; useful before the runtime is set up)
  - capabilities in reserved namespaces (audit.*, permission.*, ...)
    are rejected
  - tool input schemas are JSON-shaped with a "type" field
  - runtime.type is "subprocess" with a non-empty entrypoint
  - memory_extensions namespaces are reverse-DNS

Returns 0 on a clean manifest; non-zero with a punch list of every
failure on a manifest that needs work. Authors run this in CI before
publishing to the registry.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := readManifestFromPath(args[0])
		if err != nil {
			return err
		}

		m, err := capsule.Parse(data)
		if err != nil {
			return err
		}

		var reg capsule.CapabilityRegistry
		if !capsuleValidateOffline {
			// Open the permission store read-only to consult the
			// registered capabilities. We don't open the audit log
			// — validation emits no audit entries; it's read-only.
			cfg := config.From(cmd.Context())
			if cfg == nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: no config attached; validating offline\n")
			} else {
				store, err := permission.Open(cmd.Context(),
					filepath.Join(cfg.Runtime.DataDir, "runtime.db"))
				if err != nil {
					// Treat absence of a runtime as a soft signal:
					// warn, then fall back to offline. Capsule authors
					// on a developer machine without a configured
					// Loamss shouldn't be blocked from validating
					// their work.
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
						"warning: cannot open runtime permission store (%v); validating offline\n", err)
				} else {
					defer func() { _ = store.Close() }()
					reg = &permissionRegistryAdapter{store: store, ctx: cmd.Context()}
				}
			}
		}

		if err := m.Validate(reg); err != nil {
			// Aggregated errors print on separate lines for
			// readability — authors see a punch list, not one long
			// run-on sentence.
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "✗ Capsule %q (%s) has validation errors:\n", m.Name, m.Version)
			printAggregatedError(cmd.OutOrStdout(), err)
			return errors.New("validation failed")
		}

		// On success, print a short summary so the author sees what
		// the runtime understood.
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"✓ Capsule %q v%s validates clean\n  permissions:  %d capabilities\n  tools:        %d declared\n  runtime:      %s (%s)\n",
			m.Name, m.Version, len(m.Permissions), len(m.Tools),
			m.Runtime.Type, m.Runtime.Protocol,
		)
		if m.MemoryExtensions != nil && len(m.MemoryExtensions.EntityTypes) > 0 {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  memory ext:   %d entity types\n", len(m.MemoryExtensions.EntityTypes))
		}
		return nil
	},
}

// readManifestFromPath resolves a user-supplied path to manifest
// bytes. The path may point at a directory (resolved to
// <path>/capsule.yaml) or directly at a .yaml file.
func readManifestFromPath(p string) ([]byte, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	var manifestPath string
	if st.IsDir() {
		manifestPath = filepath.Join(p, "capsule.yaml")
	} else {
		manifestPath = p
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", manifestPath, err)
	}
	return data, nil
}

// permissionRegistryAdapter bridges permission.Store into the
// capsule.CapabilityRegistry interface. The adapter is read-only;
// validation never mutates the store.
type permissionRegistryAdapter struct {
	store *permission.Store
	ctx   context.Context //nolint:containedctx // adapter exists for the duration of one CLI call; context lifetime matches caller
}

func (a *permissionRegistryAdapter) HasCapability(name string) bool {
	_, err := a.store.GetCapability(a.ctx, name)
	return err == nil
}

// printAggregatedError walks an errors.Join chain (or any wrapped
// error) and prints each leaf on its own line, prefixed with " - ".
// Falls through to a single line when the error isn't a join.
func printAggregatedError(out interface{ Write([]byte) (int, error) }, err error) {
	// errors.Join returns a *joinError whose Error() method renders
	// each leaf on its own newline already. We just indent each line.
	for _, line := range splitLines(err.Error()) {
		_, _ = fmt.Fprintf(out, "  - %s\n", line)
	}
}

func splitLines(s string) []string {
	out := make([]string, 0, 8)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func init() {
	capsuleValidateCmd.Flags().BoolVar(&capsuleValidateOffline, "offline", false,
		"skip capability-registry checks (useful when no runtime is configured)")
	capsuleCmd.AddCommand(capsuleValidateCmd)
	rootCmd.AddCommand(capsuleCmd)
}
