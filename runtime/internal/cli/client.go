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

var clientCmd = &cobra.Command{
	Use:   "client",
	Short: "Manage external MCP clients paired with the runtime",
	Long: `Pair, inspect, and revoke external MCP clients. Pairing is the
bootstrapping primitive: ` + "`loamss client pair`" + ` generates a one-time
code, the client redeems it (today via ` + "`loamss client pair complete`" + `,
eventually via the MCP /pair endpoint), and the runtime issues a
bearer credential the client uses on every subsequent request.

Grants are issued separately via ` + "`loamss grant create`" + ` — the pairing
code itself carries no scope. A freshly paired client can authenticate
but cannot read or write anything until the user grants capabilities.

Subcommands:
  pair       generate a one-time code
  pair complete <code>   redeem a code, prints the bearer credential once
  list       list paired clients
  show       show a client in detail
  revoke     revoke a client (cascade-revokes all its grants)`,
}

// --- pair --------------------------------------------------------------

var (
	clientPairName string
	clientPairTTL  time.Duration
	clientPairJSON bool
)

var clientPairCmd = &cobra.Command{
	Use:   "pair",
	Short: "Generate a one-time pairing code for a new external client",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		deps, err := openPermissionDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		if strings.TrimSpace(clientPairName) == "" {
			return errors.New("--name is required")
		}
		p, err := deps.engine.CreatePairingCode(cmd.Context(),
			clientPairName, "user", clientPairTTL)
		if err != nil {
			return err
		}
		if clientPairJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(p)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"Pairing code for %q:\n\n  %s\n\nExpires at %s (%s from now)\n\n"+
				"Have the client redeem with:\n  loamss client pair complete %s\n",
			p.ClientName,
			p.Code,
			p.ExpiresAt.Local().Format(time.RFC1123),
			time.Until(p.ExpiresAt).Round(time.Second),
			p.Code,
		)
		return nil
	},
}

// --- pair complete -----------------------------------------------------

var clientPairCompleteJSON bool

var clientPairCompleteCmd = &cobra.Command{
	Use:   "complete <code>",
	Short: "Redeem a pairing code and issue a bearer credential",
	Long: `Redeem a one-time pairing code. The runtime mints a per-client
bearer credential and prints it exactly once — there is no way to
recover it later. The client must store it immediately.

In the eventual end-to-end flow, this step is performed by the
client itself against the runtime's /pair endpoint; the CLI form
exists for development and for users who paste the credential
manually.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openPermissionDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		c, token, err := deps.engine.RedeemPairingCode(cmd.Context(),
			args[0], map[string]any{"paired_via": "cli"})
		if err != nil {
			return err
		}
		if clientPairCompleteJSON {
			// JSON path: include the token (caller asked for it).
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(struct {
				Client *permission.Client `json:"client"`
				Token  string             `json:"token"`
			}{Client: c, Token: token})
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"✓ Paired client %q (%s)\n\n"+
				"Bearer credential (save this now — it cannot be recovered):\n\n  %s\n\n"+
				"The credential is the only secret; the client id (%s) is public.\n"+
				"Next: issue grants via `loamss grant create --principal-kind client --principal-id %s --capability ...`\n",
			c.Name, c.ID, token, c.ID, c.ID,
		)
		return nil
	},
}

// --- list --------------------------------------------------------------

var (
	clientListStatus string
	clientListLimit  int
	clientListJSON   bool
)

var clientListCmd = &cobra.Command{
	Use:   "list",
	Short: "List paired clients",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		deps, err := openPermissionDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		clients, err := deps.store.ListClients(cmd.Context(), permission.ClientFilter{
			Status: clientListStatus,
			Limit:  clientListLimit,
		})
		if err != nil {
			return err
		}
		return emitClients(cmd.OutOrStdout(), clients, clientListJSON)
	},
}

// --- show --------------------------------------------------------------

var clientShowJSON bool

var clientShowCmd = &cobra.Command{
	Use:   "show <client-id>",
	Short: "Show a paired client in detail",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openPermissionDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		c, err := deps.store.GetClient(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if clientShowJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(c)
		}
		return renderClientDetail(cmd.OutOrStdout(), c)
	},
}

// --- revoke ------------------------------------------------------------

var (
	clientRevokeReason string
	clientRevokeYes    bool
)

var clientRevokeCmd = &cobra.Command{
	Use:   "revoke <client-id>",
	Short: "Revoke a paired client",
	Long: `Revoke a client. The client's bearer credential stops working
immediately, and every grant the client holds is cascade-revoked in
the same operation. Idempotent on already-revoked clients.

Prompts for confirmation when stdin is a terminal; pass --yes to
skip. Emits client.revoked plus a grant.revoke entry per cascaded
grant.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openPermissionDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()
		id := args[0]

		c, err := deps.store.GetClient(cmd.Context(), id)
		if err != nil {
			return err
		}
		if c.RevokedAt != nil {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"Client %s (%q) is already revoked (at %s)\n",
				c.ID, c.Name, c.RevokedAt.UTC().Format(time.RFC3339))
			return nil
		}

		if !clientRevokeYes && isTerminal(os.Stdin) {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"About to revoke client %s (%q).\n"+
					"All grants held by this client will be cascade-revoked.\n"+
					"Continue? [y/N] ",
				c.ID, c.Name)
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer != "y" && answer != "yes" {
				return errors.New("aborted")
			}
		}

		if err := deps.engine.RevokeClient(cmd.Context(), id, "user", clientRevokeReason); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "✓ Revoked client %s (%q)\n", c.ID, c.Name)
		return nil
	},
}

// --- shared rendering --------------------------------------------------

func emitClients(w io.Writer, clients []permission.Client, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		for _, c := range clients {
			if err := enc.Encode(c); err != nil {
				return err
			}
		}
		return nil
	}
	if len(clients) == 0 {
		_, err := fmt.Fprintln(w, "(no clients)")
		return err
	}
	var b strings.Builder
	for _, c := range clients {
		status := "active"
		if c.RevokedAt != nil {
			status = "revoked"
		}
		lastSeen := "never"
		if c.LastSeenAt != nil {
			lastSeen = c.LastSeenAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		fmt.Fprintf(&b, "%s  %-25s  %-7s  paired=%s  last_seen=%s\n",
			c.ID,
			truncate(c.Name, 25),
			status,
			c.PairedAt.UTC().Format("2006-01-02T15:04:05Z"),
			lastSeen,
		)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func renderClientDetail(w io.Writer, c *permission.Client) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Client %s\n\n", c.ID)
	fmt.Fprintf(&b, "  Name:        %s\n", c.Name)
	status := "active"
	if c.RevokedAt != nil {
		status = "revoked"
	}
	fmt.Fprintf(&b, "  Status:      %s\n", status)
	fmt.Fprintf(&b, "  Paired:      %s\n", c.PairedAt.UTC().Format(time.RFC3339Nano))
	if c.LastSeenAt != nil {
		fmt.Fprintf(&b, "  Last seen:   %s\n", c.LastSeenAt.UTC().Format(time.RFC3339Nano))
	} else {
		fmt.Fprintf(&b, "  Last seen:   (never)\n")
	}
	if c.RevokedAt != nil {
		fmt.Fprintf(&b, "  Revoked:     %s\n", c.RevokedAt.UTC().Format(time.RFC3339Nano))
	}
	if len(c.Metadata) > 0 {
		fmt.Fprintln(&b, "  Metadata:")
		raw, _ := json.MarshalIndent(c.Metadata, "    ", "  ")
		b.WriteString("    ")
		b.Write(raw)
		b.WriteByte('\n')
	}
	// Deliberately omit credential_hash — it's an implementation
	// detail, never useful to display, and showing it nudges users
	// toward thinking it's recoverable. It is not.
	_, err := io.WriteString(w, b.String())
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func init() {
	clientPairCmd.Flags().StringVar(&clientPairName, "name", "", "display name for the client (required)")
	clientPairCmd.Flags().DurationVar(&clientPairTTL, "ttl", 0, "code lifetime (default 10m)")
	clientPairCmd.Flags().BoolVar(&clientPairJSON, "json", false, "emit the code as JSON")
	clientCmd.AddCommand(clientPairCmd)

	clientPairCompleteCmd.Flags().BoolVar(&clientPairCompleteJSON, "json", false, "emit the credential as JSON")
	clientPairCmd.AddCommand(clientPairCompleteCmd)

	clientListCmd.Flags().StringVar(&clientListStatus, "status", "active", "active | revoked | all")
	clientListCmd.Flags().IntVarP(&clientListLimit, "limit", "n", 100, "maximum clients to return")
	clientListCmd.Flags().BoolVar(&clientListJSON, "json", false, "output as JSONL")
	clientCmd.AddCommand(clientListCmd)

	clientShowCmd.Flags().BoolVar(&clientShowJSON, "json", false, "output as JSON")
	clientCmd.AddCommand(clientShowCmd)

	clientRevokeCmd.Flags().StringVar(&clientRevokeReason, "reason", "", "optional note recorded with the audit entry")
	clientRevokeCmd.Flags().BoolVarP(&clientRevokeYes, "yes", "y", false, "skip the confirmation prompt")
	clientCmd.AddCommand(clientRevokeCmd)

	rootCmd.AddCommand(clientCmd)
}
