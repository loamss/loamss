package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/database"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/permission"
)

var capsuleCmd = &cobra.Command{
	Use:   "capsule",
	Short: "Inspect, install, and remove capsules",
	Long: `Capsules are packaged Loamss agents — sandboxed subprocesses that
expose MCP tools through the runtime. Each capsule declares its
required capabilities, the tools it provides, and the kind of model
it needs.

Subcommands:
  validate <path>      parse and validate a capsule's manifest
  install <path>       validate, copy code, issue grants, install
  list                 list installed capsules
  show <name>          show an installed capsule in detail
  uninstall <name>     revoke grants, remove code, delete record

The subprocess host (actually running capsule code) arrives in a
subsequent commit; installation today persists the record + grants
so the runtime knows the capsule is present.`,
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
				db, err := openRuntimeDB(cmd.Context(), cfg)
				if err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
						"warning: cannot open runtime database (%v); validating offline\n", err)
				} else {
					store, err := permission.OpenWith(cmd.Context(), db)
					if err != nil {
						_ = db.Close()
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
							"warning: cannot open runtime permission store (%v); validating offline\n", err)
					} else {
						defer func() {
							_ = store.Close()
							_ = db.Close()
						}()
						reg = &permissionRegistryAdapter{store: store, ctx: cmd.Context()}
					}
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

// --- install -----------------------------------------------------------

var (
	capsuleInstallYes  bool
	capsuleInstallJSON bool
)

var capsuleInstallCmd = &cobra.Command{
	Use:   "install <path>",
	Short: "Install a capsule onto this runtime",
	Long: `Validates the manifest at <path>, copies the capsule's code into the
data directory, issues grants for every declared permission, and
persists the install record. Each issued grant produces a
grant.create audit entry; the install itself emits capsule.installed.

Prompts on a terminal for confirmation; pass --yes to skip. Fails
fast on validation errors; partial state is rolled back on grant or
persistence failures.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openCapsuleDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		// Human-readable mode: show the permission slip BEFORE doing
		// anything destructive. JSON mode skips this so the output
		// is a single decodable object.
		if !capsuleInstallJSON {
			if _, err := loadAndDisplayManifest(cmd, args[0]); err != nil {
				return err
			}
		}

		if !capsuleInstallYes && isTerminal(os.Stdin) {
			_, _ = fmt.Fprint(cmd.OutOrStdout(), "\nProceed with install? [y/N] ")
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer != "y" && answer != "yes" {
				return errors.New("aborted")
			}
		}

		result, err := deps.installer.Install(cmd.Context(), args[0], "user")
		if err != nil {
			return err
		}
		if capsuleInstallJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"\n✓ Installed %s@%s\n  install_path:  %s\n  grants issued: %d\n",
			result.Capsule.Name, result.Capsule.Version,
			result.Capsule.InstallPath, len(result.GrantIDs),
		)
		return nil
	},
}

// loadAndDisplayManifest parses the manifest and prints the
// permission slip — the list of capabilities the capsule will be
// granted. The user sees this before any persistent change.
func loadAndDisplayManifest(cmd *cobra.Command, sourcePath string) (*capsule.Manifest, error) {
	data, err := readManifestFromPath(sourcePath)
	if err != nil {
		return nil, err
	}
	m, err := capsule.Parse(data)
	if err != nil {
		return nil, err
	}
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "Capsule %s@%s\n", m.Name, m.Version)
	if m.Description != "" {
		_, _ = fmt.Fprintf(out, "  %s\n", m.Description)
	}
	_, _ = fmt.Fprintf(out, "  Author:  %s\n", m.Author.Name)
	_, _ = fmt.Fprintf(out, "\nThis capsule requests the following capabilities:\n")
	for _, p := range m.Permissions {
		marker := " "
		if p.RequiresUserApproval {
			marker = "!"
		}
		_, _ = fmt.Fprintf(out, "  %s %-25s  %s\n", marker, p.Capability, p.Rationale)
		if len(p.Scope) > 0 {
			raw, _ := json.Marshal(p.Scope)
			_, _ = fmt.Fprintf(out, "      scope: %s\n", string(raw))
		}
	}
	if len(m.Tools) > 0 {
		_, _ = fmt.Fprintf(out, "\nIt will expose these tools:\n")
		for _, t := range m.Tools {
			_, _ = fmt.Fprintf(out, "  - %s: %s\n", t.Name, t.Description)
		}
	}
	return m, nil
}

// --- list --------------------------------------------------------------

var capsuleListJSON bool

var capsuleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed capsules",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		deps, err := openCapsuleDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		caps, err := deps.store.List(cmd.Context())
		if err != nil {
			return err
		}
		if capsuleListJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			for _, c := range caps {
				if err := enc.Encode(c); err != nil {
					return err
				}
			}
			return nil
		}
		if len(caps) == 0 {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "(no capsules installed)")
			return nil
		}
		for _, c := range caps {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s@%s  %s  installed=%s\n",
				c.Name, c.Version, c.AuthorName,
				c.InstalledAt.UTC().Format("2006-01-02T15:04:05Z"),
			)
		}
		return nil
	},
}

// --- show --------------------------------------------------------------

var capsuleShowJSON bool

var capsuleShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show an installed capsule in detail",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openCapsuleDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		c, err := deps.store.Get(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if capsuleShowJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(c)
		}
		out := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(out, "Capsule %s@%s\n\n", c.Name, c.Version)
		_, _ = fmt.Fprintf(out, "  Author:        %s\n", c.AuthorName)
		if c.AuthorURL != "" {
			_, _ = fmt.Fprintf(out, "  Author URL:    %s\n", c.AuthorURL)
		}
		_, _ = fmt.Fprintf(out, "  Spec version:  %s\n", c.SpecVersion)
		_, _ = fmt.Fprintf(out, "  Installed:     %s\n", c.InstalledAt.UTC().Format("2006-01-02T15:04:05Z"))
		_, _ = fmt.Fprintf(out, "  Install path:  %s\n", c.InstallPath)
		_, _ = fmt.Fprintf(out, "  Tools:         %d\n", len(c.Manifest.Tools))
		_, _ = fmt.Fprintf(out, "  Permissions:   %d\n", len(c.Manifest.Permissions))
		for _, p := range c.Manifest.Permissions {
			_, _ = fmt.Fprintf(out, "    - %s\n", p.Capability)
		}
		return nil
	},
}

// --- uninstall ---------------------------------------------------------

var (
	capsuleUninstallYes    bool
	capsuleUninstallReason string
)

var capsuleUninstallCmd = &cobra.Command{
	Use:   "uninstall <name>",
	Short: "Uninstall a capsule",
	Long: `Removes the capsule's install record, cascade-revokes every grant
attached to the capsule principal, and deletes the on-disk install
directory. Idempotent on missing capsule (returns an error;
re-running after a partial cleanup is safe). Each revoked grant
emits a grant.revoke audit entry; the uninstall itself emits
capsule.uninstalled.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openCapsuleDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		name := args[0]
		c, err := deps.store.Get(cmd.Context(), name)
		if err != nil {
			return err
		}
		if !capsuleUninstallYes && isTerminal(os.Stdin) {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"About to uninstall capsule %s@%s.\nAll grants attached to this capsule will be cascade-revoked.\nContinue? [y/N] ",
				c.Name, c.Version)
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer != "y" && answer != "yes" {
				return errors.New("aborted")
			}
		}
		if err := deps.installer.Uninstall(cmd.Context(), name, "user", capsuleUninstallReason); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "✓ Uninstalled %s@%s\n", c.Name, c.Version)
		return nil
	},
}

// --- shared deps -------------------------------------------------------

// capsuleDeps bundles everything install/list/show/uninstall need:
// permission store + engine + audit + capsule store + installer.
// Constructed at the start of every capsule-touching CLI subcommand
// and closed before return.
type capsuleDeps struct {
	store     *capsule.Store
	permStore *permission.Store
	audit     *audit.SQLite
	engine    *permission.Engine
	installer *capsule.Installer
	db        *database.Database // owning handle; closed last
}

// Close releases every handle. Errors are logged on a best-effort
// basis; CLI exit dominates.
func (d *capsuleDeps) Close() {
	if d.store != nil {
		_ = d.store.Close()
	}
	if d.permStore != nil {
		_ = d.permStore.Close()
	}
	if d.audit != nil {
		_ = d.audit.Close(context.Background())
	}
	if d.db != nil {
		_ = d.db.Close()
	}
}

// openCapsuleDeps opens the capsule + permission stores against the
// shared runtime database (SQLite by default; Postgres when
// configured) and the audit writer at its conventional path.
func openCapsuleDeps(cmd *cobra.Command) (*capsuleDeps, error) {
	cfg := config.From(cmd.Context())
	if cfg == nil {
		return nil, errors.New("no config attached to context (programming error in CLI wiring)")
	}
	db, err := openRuntimeDB(cmd.Context(), cfg)
	if err != nil {
		return nil, err
	}
	permStore, err := permission.OpenWith(cmd.Context(), db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	capStore, err := capsule.OpenStoreWith(cmd.Context(), db)
	if err != nil {
		_ = permStore.Close()
		_ = db.Close()
		return nil, err
	}
	w, err := audit.OpenSQLite(cmd.Context(), filepath.Join(cfg.Runtime.DataDir, "audit.db"))
	if err != nil {
		_ = permStore.Close()
		_ = capStore.Close()
		_ = db.Close()
		return nil, err
	}
	engine := permission.NewEngine(permStore, w)
	installer := capsule.NewInstaller(capStore, engine, w,
		filepath.Join(cfg.Runtime.DataDir, "capsules"))
	return &capsuleDeps{
		store:     capStore,
		permStore: permStore,
		audit:     w,
		engine:    engine,
		installer: installer,
		db:        db,
	}, nil
}

func init() {
	capsuleValidateCmd.Flags().BoolVar(&capsuleValidateOffline, "offline", false,
		"skip capability-registry checks (useful when no runtime is configured)")
	capsuleCmd.AddCommand(capsuleValidateCmd)

	capsuleInstallCmd.Flags().BoolVarP(&capsuleInstallYes, "yes", "y", false, "skip the confirmation prompt")
	capsuleInstallCmd.Flags().BoolVar(&capsuleInstallJSON, "json", false, "output as JSON")
	capsuleCmd.AddCommand(capsuleInstallCmd)

	capsuleListCmd.Flags().BoolVar(&capsuleListJSON, "json", false, "output as JSONL")
	capsuleCmd.AddCommand(capsuleListCmd)

	capsuleShowCmd.Flags().BoolVar(&capsuleShowJSON, "json", false, "output as JSON")
	capsuleCmd.AddCommand(capsuleShowCmd)

	capsuleUninstallCmd.Flags().BoolVarP(&capsuleUninstallYes, "yes", "y", false, "skip the confirmation prompt")
	capsuleUninstallCmd.Flags().StringVar(&capsuleUninstallReason, "reason", "", "optional note recorded in the audit entry")
	capsuleCmd.AddCommand(capsuleUninstallCmd)

	rootCmd.AddCommand(capsuleCmd)
}
