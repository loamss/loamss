package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/config"
)

// auditDBPath returns the on-disk path of the audit hot store
// derived from the resolved config. Currently fixed at
// <data_dir>/audit.db; will become configurable when audit.spec's
// AuditConfig grows a HotStoreDir field.
func auditDBPath(cfg *config.Config) string {
	return filepath.Join(cfg.Runtime.DataDir, "audit.db")
}

// openAuditWriterForCmd is the cobra-bridge convenience that reads
// the resolved config from cmd's context and delegates to
// openAuditWriter (defined in database.go) for the actual driver
// resolution. Caller is responsible for Close.
func openAuditWriterForCmd(cmd *cobra.Command) (*audit.Store, error) {
	cfg := config.From(cmd.Context())
	if cfg == nil {
		return nil, errors.New("no config attached to context (programming error in CLI wiring)")
	}
	w, err := openAuditWriter(cmd.Context(), cfg)
	if err != nil {
		return nil, fmt.Errorf("opening audit log: %w", err)
	}
	return w, nil
}

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Inspect the audit log",
	Long: `Inspect and verify the audit log. The audit log records every
gated operation the runtime performs — capsule data accesses, external
client requests, grant changes, approvals, and consequential actions.
See audit-spec.md for the schema.

Subcommands:
  tail     show the most recent entries
  log      query entries with filters
  verify   check the hash-chain integrity
  export   stream all entries as JSONL`,
}

// --- tail --------------------------------------------------------------

var (
	auditTailLimit int
	auditTailJSON  bool
)

var auditTailCmd = &cobra.Command{
	Use:   "tail",
	Short: "Show the most recent audit entries",
	Long: `Print the N most recent entries (default 50), oldest first.

v0.1 is snapshot-style: it does not follow new entries. Real-time
streaming (tail -f semantics) arrives with the daemon's subscription
mechanism in a future release.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		w, err := openAuditWriterForCmd(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = w.Close(cmd.Context()) }()

		// Reverse-order query with a limit gives us the last N entries
		// efficiently. We then reverse the slice for display so the
		// user sees them oldest-of-the-tail first (matching what `tail`
		// does on a file).
		entries, err := w.Query(cmd.Context(), audit.Filter{
			Limit:   auditTailLimit,
			Reverse: true,
		})
		if err != nil {
			return err
		}
		// Reverse in place: oldest of the tail at the top.
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
		return emitEntries(cmd.OutOrStdout(), entries, auditTailJSON)
	},
}

// --- log ---------------------------------------------------------------

var (
	auditLogSince     string
	auditLogUntil     string
	auditLogTypes     []string
	auditLogActorKind string
	auditLogActorID   string
	auditLogSubject   string
	auditLogOutcomes  []string
	auditLogLimit     int
	auditLogJSON      bool
)

var auditLogCmd = &cobra.Command{
	Use:   "log",
	Short: "Query audit entries with filters",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		w, err := openAuditWriterForCmd(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = w.Close(cmd.Context()) }()

		f := audit.Filter{
			ActorKind: audit.ActorKind(auditLogActorKind),
			ActorID:   auditLogActorID,
			SubjectID: auditLogSubject,
			Types:     auditLogTypes,
			Limit:     auditLogLimit,
		}
		if auditLogSince != "" {
			t, err := parseTimeFlag(auditLogSince)
			if err != nil {
				return fmt.Errorf("--since: %w", err)
			}
			f.Since = t
		}
		if auditLogUntil != "" {
			t, err := parseTimeFlag(auditLogUntil)
			if err != nil {
				return fmt.Errorf("--until: %w", err)
			}
			f.Until = t
		}
		for _, o := range auditLogOutcomes {
			f.Outcomes = append(f.Outcomes, audit.Outcome(o))
		}

		entries, err := w.Query(cmd.Context(), f)
		if err != nil {
			return err
		}
		return emitEntries(cmd.OutOrStdout(), entries, auditLogJSON)
	},
}

// parseTimeFlag accepts RFC3339 timestamps and relative durations
// (e.g., "1h", "24h", "7d"). 7d is interpreted as 7×24h since the
// stdlib doesn't parse "d" natively.
func parseTimeFlag(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	// Support "Nd" by translating to hours.
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days >= 0 {
			return time.Now().Add(-time.Duration(days) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q (use RFC3339, e.g. 2026-01-01T00:00:00Z, or a duration like 24h or 7d)", s)
}

// --- verify ------------------------------------------------------------

var auditVerifyJSON bool

var auditVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Check the audit log's hash-chain integrity",
	Long: `Replay the chain from genesis through head, recomputing each
hash. Reports the first break, if any. Exits 0 on clean chain;
non-zero on any inconsistency.

Use this to confirm the on-disk audit log hasn't been tampered with
between runtime sessions, or to debug a writer bug. See audit-spec.md
§Tamper-evidence for the algorithm.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		w, err := openAuditWriterForCmd(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = w.Close(cmd.Context()) }()

		r, err := w.Verify(cmd.Context())
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if auditVerifyJSON {
			return json.NewEncoder(out).Encode(r)
		}
		if r.Valid {
			_, err := fmt.Fprintf(out, "✓ Chain integrity verified (%d entries)\n", r.EntriesChecked)
			return err
		}
		_, _ = fmt.Fprintf(out,
			"✗ Chain broken at %s\n  Reason: %s\n  Entries checked before break: %d\n",
			r.BrokenAt, r.Reason, r.EntriesChecked)
		// Returning a sentinel error makes the process exit non-zero
		// without cobra re-printing our message.
		cmd.SilenceErrors = true
		return errAuditVerifyFailed
	},
}

var errAuditVerifyFailed = errors.New("audit verify: chain broken")

// --- export ------------------------------------------------------------

var auditExportLimit int

var auditExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Stream all audit entries as JSONL",
	Long: `Emit every entry as a JSON object on its own line, oldest
first. Suitable for piping into long-term retention, compliance archives,
or third-party log analysis.

The output is the canonical Entry shape — exactly what re-importing
into another runtime expects.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		w, err := openAuditWriterForCmd(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = w.Close(cmd.Context()) }()

		// Limit defaults to 0 → audit.Writer applies its own default.
		// For export we override that: emit everything by paging in
		// chunks until we run out. v0.1's writer doesn't have
		// cursor-based pagination; we use a large single Query for now.
		// When chains grow past memory, this gets streaming pagination.
		f := audit.Filter{Limit: auditExportLimit}
		entries, err := w.Query(cmd.Context(), f)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		for _, e := range entries {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	},
}

// --- shared rendering --------------------------------------------------

// emitEntries writes the entries to w. If asJSON is true, one JSON
// object per line; otherwise a human-readable summary.
func emitEntries(w io.Writer, entries []audit.Entry, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		for _, e := range entries {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	}
	if len(entries) == 0 {
		_, err := fmt.Fprintln(w, "(no entries)")
		return err
	}
	var b strings.Builder
	for _, e := range entries {
		subject := ""
		if e.Subject != nil {
			subject = fmt.Sprintf("  → %s/%s", e.Subject.Kind, e.Subject.ID)
		}
		fmt.Fprintf(&b, "%s  %s  %-20s  %s/%s  %s%s\n",
			e.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
			e.ID,
			e.Type,
			e.Actor.Kind, e.Actor.ID,
			e.Outcome,
			subject,
		)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func init() {
	// tail
	auditTailCmd.Flags().IntVarP(&auditTailLimit, "limit", "n", 50, "show the most recent N entries")
	auditTailCmd.Flags().BoolVar(&auditTailJSON, "json", false, "output as JSONL instead of human-readable")
	auditCmd.AddCommand(auditTailCmd)

	// log
	auditLogCmd.Flags().StringVar(&auditLogSince, "since", "", "include entries on or after this time (RFC3339 or duration like 24h, 7d)")
	auditLogCmd.Flags().StringVar(&auditLogUntil, "until", "", "include entries on or before this time")
	auditLogCmd.Flags().StringSliceVar(&auditLogTypes, "type", nil, "filter by event type (repeatable)")
	auditLogCmd.Flags().StringVar(&auditLogActorKind, "actor-kind", "", "filter by actor kind (user, capsule, client, runtime, system)")
	auditLogCmd.Flags().StringVar(&auditLogActorID, "actor-id", "", "filter by actor id")
	auditLogCmd.Flags().StringVar(&auditLogSubject, "subject-id", "", "filter by subject id")
	auditLogCmd.Flags().StringSliceVar(&auditLogOutcomes, "outcome", nil, "filter by outcome (repeatable)")
	auditLogCmd.Flags().IntVarP(&auditLogLimit, "limit", "n", 100, "maximum number of entries to return")
	auditLogCmd.Flags().BoolVar(&auditLogJSON, "json", false, "output as JSONL instead of human-readable")
	auditCmd.AddCommand(auditLogCmd)

	// verify
	auditVerifyCmd.Flags().BoolVar(&auditVerifyJSON, "json", false, "output the verify report as JSON")
	auditCmd.AddCommand(auditVerifyCmd)

	// export
	auditExportCmd.Flags().IntVar(&auditExportLimit, "limit", 1000000, "maximum number of entries to emit (safety bound)")
	auditCmd.AddCommand(auditExportCmd)

	rootCmd.AddCommand(auditCmd)
}
