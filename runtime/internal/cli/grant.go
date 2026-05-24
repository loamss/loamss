package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/loamss/loamss/runtime/internal/permission"
)

var grantCmd = &cobra.Command{
	Use:   "grant",
	Short: "Inspect and revoke capability grants",
	Long: `Manage the grants the runtime uses to gate every capsule and
external-client access. Issuing grants happens through the pairing
and capsule-install flows (the runtime never auto-issues from a CLI
prompt); this command tree exposes inspection and revocation.

Subcommands:
  list     list grants, optionally filtered by principal / capability / status
  show     show a single grant in full
  revoke   revoke a grant (idempotent; emits grant.revoke audit entry)`,
}

// --- list --------------------------------------------------------------

var (
	grantListPrincipalKind string
	grantListPrincipalID   string
	grantListCapability    string
	grantListStatus        string
	grantListLimit         int
	grantListJSON          bool
)

var grantListCmd = &cobra.Command{
	Use:   "list",
	Short: "List capability grants",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		deps, err := openPermissionDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		f := permission.GrantFilter{
			PrincipalKind: permission.PrincipalKind(grantListPrincipalKind),
			PrincipalID:   grantListPrincipalID,
			Capability:    grantListCapability,
			Status:        grantListStatus,
			Limit:         grantListLimit,
		}
		grants, err := deps.store.ListGrants(cmd.Context(), f)
		if err != nil {
			return err
		}
		return emitGrants(cmd.OutOrStdout(), grants, grantListJSON)
	},
}

// --- show --------------------------------------------------------------

var grantShowJSON bool

var grantShowCmd = &cobra.Command{
	Use:   "show <grant-id>",
	Short: "Show a single grant in detail",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openPermissionDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		g, err := deps.store.GetGrant(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if grantShowJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(g)
		}
		return renderGrantDetail(cmd.OutOrStdout(), g)
	},
}

// --- revoke ------------------------------------------------------------

var (
	grantRevokeReason string
	grantRevokeYes    bool
)

var grantRevokeCmd = &cobra.Command{
	Use:   "revoke <grant-id>",
	Short: "Revoke a grant",
	Long: `Revoke marks a grant as no longer active. Subsequent Check
calls against it will deny. Revocations are immediate — there is no
grace period. Idempotent: revoking an already-revoked grant succeeds
silently.

Prompts for confirmation when stdin is a terminal; pass --yes to skip.
Emits a grant.revoke audit entry on first revocation.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openPermissionDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()
		id := args[0]

		// Fetch first so we can show what's about to be revoked.
		g, err := deps.store.GetGrant(cmd.Context(), id)
		if err != nil {
			return err
		}
		if g.RevokedAt != nil {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Grant %s is already revoked (at %s)\n", g.ID, g.RevokedAt.UTC().Format(time.RFC3339))
			return nil
		}

		if !grantRevokeYes && isTerminal(os.Stdin) {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"About to revoke grant %s\n  principal:  %s/%s\n  capability: %s\nContinue? [y/N] ",
				g.ID, g.Principal.Kind, g.Principal.ID, g.Capability)
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer != "y" && answer != "yes" {
				return errors.New("aborted")
			}
		}

		if err := deps.engine.RevokeGrant(cmd.Context(), id, "user", grantRevokeReason); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "✓ Revoked grant %s\n", g.ID)
		return err
	},
}

// --- shared rendering --------------------------------------------------

// emitGrants writes the grants to w. asJSON => JSONL of full Grant
// objects; otherwise a table.
func emitGrants(w io.Writer, grants []permission.Grant, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		for _, g := range grants {
			if err := enc.Encode(g); err != nil {
				return err
			}
		}
		return nil
	}
	if len(grants) == 0 {
		_, err := fmt.Fprintln(w, "(no grants)")
		return err
	}
	var b strings.Builder
	now := time.Now()
	for _, g := range grants {
		status := "active"
		switch {
		case g.RevokedAt != nil:
			status = "revoked"
		case g.ExpiresAt != nil && !now.Before(*g.ExpiresAt):
			status = "expired"
		}
		fmt.Fprintf(&b, "%s  %s/%s  %-25s  %-8s  issued=%s",
			g.ID,
			g.Principal.Kind, g.Principal.ID,
			g.Capability,
			status,
			g.IssuedAt.UTC().Format("2006-01-02T15:04:05Z"),
		)
		if g.ExpiresAt != nil {
			fmt.Fprintf(&b, "  expires=%s", g.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"))
		}
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func renderGrantDetail(w io.Writer, g *permission.Grant) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Grant %s\n\n", g.ID)
	fmt.Fprintf(&b, "  Principal:     %s/%s\n", g.Principal.Kind, g.Principal.ID)
	fmt.Fprintf(&b, "  Capability:    %s\n", g.Capability)
	fmt.Fprintf(&b, "  Framing:       %s\n", g.Framing)
	fmt.Fprintf(&b, "  Issued:        %s\n", g.IssuedAt.UTC().Format(time.RFC3339Nano))
	if g.ExpiresAt != nil {
		fmt.Fprintf(&b, "  Expires:       %s\n", g.ExpiresAt.UTC().Format(time.RFC3339Nano))
	}
	if g.RevokedAt != nil {
		fmt.Fprintf(&b, "  Revoked:       %s\n", g.RevokedAt.UTC().Format(time.RFC3339Nano))
	}
	fmt.Fprintf(&b, "  Approval req:  %v\n", g.RequiresUserApproval)
	if g.Rationale != "" {
		fmt.Fprintf(&b, "  Rationale:     %s\n", g.Rationale)
	}
	if g.UserNote != "" {
		fmt.Fprintf(&b, "  User note:     %s\n", g.UserNote)
	}
	if len(g.Scope) > 0 {
		fmt.Fprintln(&b, "  Scope:")
		raw, _ := json.MarshalIndent(g.Scope, "    ", "  ")
		b.WriteString("    ")
		b.Write(raw)
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// isTerminal returns true if f is connected to a character device
// (i.e., a terminal). Used to skip the confirmation prompt during
// scripted/piped use.
func isTerminal(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

func init() {
	// list
	grantListCmd.Flags().StringVar(&grantListPrincipalKind, "principal-kind", "", "filter by principal kind (capsule|client)")
	grantListCmd.Flags().StringVar(&grantListPrincipalID, "principal-id", "", "filter by principal id")
	grantListCmd.Flags().StringVar(&grantListCapability, "capability", "", "filter by capability name")
	grantListCmd.Flags().StringVar(&grantListStatus, "status", "active", "active | revoked | expired | all")
	grantListCmd.Flags().IntVarP(&grantListLimit, "limit", "n", 100, "maximum number of grants to return")
	grantListCmd.Flags().BoolVar(&grantListJSON, "json", false, "output as JSONL")
	grantCmd.AddCommand(grantListCmd)

	// show
	grantShowCmd.Flags().BoolVar(&grantShowJSON, "json", false, "output as JSON")
	grantCmd.AddCommand(grantShowCmd)

	// revoke
	grantRevokeCmd.Flags().StringVar(&grantRevokeReason, "reason", "", "optional note recorded with the audit entry")
	grantRevokeCmd.Flags().BoolVarP(&grantRevokeYes, "yes", "y", false, "skip the confirmation prompt")
	grantCmd.AddCommand(grantRevokeCmd)

	rootCmd.AddCommand(grantCmd)
}
