package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/loamss/loamss/runtime/internal/permission"
)

var approveCmd = &cobra.Command{
	Use:   "approve",
	Short: "Manage pending approval requests",
	Long: `Resolve approval requests that the check engine enqueued for
consequential actions (sends, writes, configurable per-capability).

Subcommands:
  list                       list pending approvals
  show <id>                  show a pending approval in detail
  grant <id> [--note ...]    approve the request
  deny  <id> [--note ...]    deny the request

Granting causes the original caller's WaitForApproval to resume with
DecisionAllow; denying yields DecisionDeny. Both emit the
corresponding audit entries (approval.granted / approval.denied).`,
}

// --- list --------------------------------------------------------------

var approveListJSON bool

var approveListCmd = &cobra.Command{
	Use:   "list",
	Short: "List pending approval requests",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		deps, err := openPermissionDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		pending, err := deps.store.ListPendingApprovals(cmd.Context())
		if err != nil {
			return err
		}
		return emitApprovals(cmd.OutOrStdout(), pending, approveListJSON)
	},
}

// --- show --------------------------------------------------------------

var approveShowJSON bool

var approveShowCmd = &cobra.Command{
	Use:   "show <approval-id>",
	Short: "Show a pending approval in detail",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openPermissionDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		a, err := deps.store.GetApproval(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if approveShowJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(a)
		}
		return renderApprovalDetail(cmd.OutOrStdout(), a)
	},
}

// --- grant -------------------------------------------------------------

var approveGrantNote string

var approveGrantCmd = &cobra.Command{
	Use:   "grant <approval-id>",
	Short: "Approve a pending request",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openPermissionDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		if err := deps.engine.ResolveApproval(cmd.Context(), args[0],
			permission.ApprovalGranted, "user", approveGrantNote); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "✓ Approval %s granted\n", args[0])
		return err
	},
}

// --- deny --------------------------------------------------------------

var approveDenyNote string

var approveDenyCmd = &cobra.Command{
	Use:   "deny <approval-id>",
	Short: "Deny a pending request",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openPermissionDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		if err := deps.engine.ResolveApproval(cmd.Context(), args[0],
			permission.ApprovalDenied, "user", approveDenyNote); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "✗ Approval %s denied\n", args[0])
		return err
	},
}

// --- shared rendering --------------------------------------------------

func emitApprovals(w io.Writer, items []permission.PendingApproval, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		for _, a := range items {
			if err := enc.Encode(a); err != nil {
				return err
			}
		}
		return nil
	}
	if len(items) == 0 {
		_, err := fmt.Fprintln(w, "(no pending approvals)")
		return err
	}
	var b strings.Builder
	for _, a := range items {
		fmt.Fprintf(&b, "%s  %s/%s  %-25s  requested=%s",
			a.ID,
			a.Principal.Kind, a.Principal.ID,
			a.Capability,
			a.RequestedAt.UTC().Format("2006-01-02T15:04:05Z"))
		if a.Rationale != "" {
			fmt.Fprintf(&b, "  %q", a.Rationale)
		}
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func renderApprovalDetail(w io.Writer, a *permission.PendingApproval) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Approval %s\n\n", a.ID)
	fmt.Fprintf(&b, "  Principal:    %s/%s\n", a.Principal.Kind, a.Principal.ID)
	fmt.Fprintf(&b, "  Capability:   %s\n", a.Capability)
	fmt.Fprintf(&b, "  Grant id:     %s\n", a.GrantID)
	fmt.Fprintf(&b, "  State:        %s\n", a.State)
	fmt.Fprintf(&b, "  Requested:    %s\n", a.RequestedAt.UTC().Format(time.RFC3339Nano))
	if a.Rationale != "" {
		fmt.Fprintf(&b, "  Rationale:    %s\n", a.Rationale)
	}
	if a.DecidedAt != nil {
		fmt.Fprintf(&b, "  Decided:      %s\n", a.DecidedAt.UTC().Format(time.RFC3339Nano))
		fmt.Fprintf(&b, "  Decided by:   %s\n", a.DecidedBy)
		if a.DecisionNote != "" {
			fmt.Fprintf(&b, "  Decision note: %s\n", a.DecisionNote)
		}
	}
	if len(a.AttemptedScope) > 0 {
		fmt.Fprintln(&b, "  Attempted scope:")
		raw, _ := json.MarshalIndent(a.AttemptedScope, "    ", "  ")
		b.WriteString("    ")
		b.Write(raw)
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func init() {
	approveListCmd.Flags().BoolVar(&approveListJSON, "json", false, "output as JSONL")
	approveCmd.AddCommand(approveListCmd)

	approveShowCmd.Flags().BoolVar(&approveShowJSON, "json", false, "output as JSON")
	approveCmd.AddCommand(approveShowCmd)

	approveGrantCmd.Flags().StringVar(&approveGrantNote, "note", "", "optional note recorded with the audit entry")
	approveCmd.AddCommand(approveGrantCmd)

	approveDenyCmd.Flags().StringVar(&approveDenyNote, "note", "", "optional note recorded with the audit entry")
	approveCmd.AddCommand(approveDenyCmd)

	rootCmd.AddCommand(approveCmd)
}
