package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	memadapter "github.com/loamss/loamss/runtime/internal/adapter/memory"
	"github.com/loamss/loamss/runtime/internal/config"
	memlayer "github.com/loamss/loamss/runtime/internal/memory"
)

// `loamss memory` inspects the semantic memory layer that sits above
// the memory adapter. Entities (people, organizations) and threads
// (Gmail-thread groupings today) are derived from the metadata
// sources write at sync time.

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Inspect the semantic memory layer (entities, threads)",
	Long: `Browse the entities and threads the memory layer derives from
ingested data. Today the layer recognizes people / organizations
(via email-shaped headers) and conversation threads (via Gmail's
thread_id). Future sources contribute their own resolvers.

Subcommands:
  entities list/show/entries   browse derived entities
  threads  list/show/entries   browse derived threads`,
}

// --- entities ----------------------------------------------------------

var entitiesCmd = &cobra.Command{
	Use:   "entities",
	Short: "Browse derived entities (people, organizations)",
}

var (
	entitiesListNamespace string
	entitiesListKind      string
	entitiesListAlias     string
	entitiesListLimit     int
	entitiesListJSON      bool
)

var entitiesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List entities the memory layer has derived",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		deps, err := openMemoryDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()
		ents, err := deps.layer.ListEntities(cmd.Context(), memlayer.EntityFilter{
			Namespace: entitiesListNamespace,
			Kind:      memlayer.EntityKind(entitiesListKind),
			Alias:     entitiesListAlias,
			Limit:     entitiesListLimit,
		})
		if err != nil {
			return err
		}
		return emitEntities(cmd.OutOrStdout(), ents, entitiesListJSON)
	},
}

var entitiesShowJSON bool

var entitiesShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show one entity in detail",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openMemoryDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()
		e, err := deps.layer.GetEntity(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if entitiesShowJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(e)
		}
		renderEntity(cmd.OutOrStdout(), e)
		return nil
	},
}

var (
	entitiesEntriesLimit int
	entitiesEntriesJSON  bool
)

var entitiesEntriesCmd = &cobra.Command{
	Use:   "entries <entity-id>",
	Short: "List memory entries involving an entity (newest first)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openMemoryDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()
		refs, err := deps.layer.EntriesByEntity(cmd.Context(), args[0], entitiesEntriesLimit)
		if err != nil {
			return err
		}
		return emitMemoryEntries(cmd.OutOrStdout(), refs, entitiesEntriesJSON)
	},
}

// --- threads -----------------------------------------------------------

var threadsCmd = &cobra.Command{
	Use:   "threads",
	Short: "Browse derived conversation threads",
}

var (
	threadsListNamespace string
	threadsListLimit     int
	threadsListJSON      bool
)

var threadsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List threads the memory layer has derived",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		deps, err := openMemoryDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()
		ts, err := deps.layer.ListThreads(cmd.Context(), memlayer.ThreadFilter{
			Namespace: threadsListNamespace,
			Limit:     threadsListLimit,
		})
		if err != nil {
			return err
		}
		return emitThreads(cmd.OutOrStdout(), ts, threadsListJSON)
	},
}

var threadsShowJSON bool

var threadsShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show one thread in detail",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openMemoryDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()
		t, err := deps.layer.GetThread(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if threadsShowJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(t)
		}
		renderThread(cmd.OutOrStdout(), t)
		return nil
	},
}

var (
	threadsEntriesLimit int
	threadsEntriesJSON  bool
)

var threadsEntriesCmd = &cobra.Command{
	Use:   "entries <thread-id>",
	Short: "List memory entries in a thread (reading order; oldest first)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openMemoryDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()
		refs, err := deps.layer.EntriesByThread(cmd.Context(), args[0], threadsEntriesLimit)
		if err != nil {
			return err
		}
		return emitMemoryEntries(cmd.OutOrStdout(), refs, threadsEntriesJSON)
	},
}

// --- rendering ---------------------------------------------------------

func emitEntities(w io.Writer, list []memlayer.Entity, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		for _, e := range list {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	}
	if len(list) == 0 {
		_, _ = fmt.Fprintln(w, "no entities derived yet (run `loamss source sync ...` first)")
		return nil
	}
	_, _ = fmt.Fprintf(w, "%-32s %-12s %-20s %-6s %s\n",
		"ID", "KIND", "NAMESPACE", "COUNT", "CANONICAL")
	for _, e := range list {
		_, _ = fmt.Fprintf(w, "%-32s %-12s %-20s %-6d %s\n",
			e.ID, string(e.Kind), e.Namespace, e.EntryCount, e.Canonical)
	}
	return nil
}

func renderEntity(w io.Writer, e *memlayer.Entity) {
	_, _ = fmt.Fprintf(w, "ID:           %s\n", e.ID)
	_, _ = fmt.Fprintf(w, "Kind:         %s\n", string(e.Kind))
	_, _ = fmt.Fprintf(w, "Canonical:    %s\n", e.Canonical)
	_, _ = fmt.Fprintf(w, "Namespace:    %s\n", e.Namespace)
	_, _ = fmt.Fprintf(w, "Entry count:  %d\n", e.EntryCount)
	if !e.FirstSeen.IsZero() {
		_, _ = fmt.Fprintf(w, "First seen:   %s\n", e.FirstSeen.Local().Format(time.RFC1123))
	}
	if !e.LastSeen.IsZero() {
		_, _ = fmt.Fprintf(w, "Last seen:    %s\n", e.LastSeen.Local().Format(time.RFC1123))
	}
	if len(e.Aliases) > 0 {
		_, _ = fmt.Fprintln(w, "Aliases:")
		for _, a := range e.Aliases {
			_, _ = fmt.Fprintf(w, "  %s (%s)\n", a.Value, string(a.Kind))
		}
	}
}

func emitThreads(w io.Writer, list []memlayer.Thread, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		for _, t := range list {
			if err := enc.Encode(t); err != nil {
				return err
			}
		}
		return nil
	}
	if len(list) == 0 {
		_, _ = fmt.Fprintln(w, "no threads derived yet")
		return nil
	}
	_, _ = fmt.Fprintf(w, "%-32s %-20s %-6s %s\n",
		"ID", "NAMESPACE", "COUNT", "SUBJECT")
	for _, t := range list {
		subj := t.Subject
		if subj == "" {
			subj = "(no subject)"
		}
		if len(subj) > 60 {
			subj = subj[:57] + "..."
		}
		_, _ = fmt.Fprintf(w, "%-32s %-20s %-6d %s\n",
			t.ID, t.Namespace, t.EntryCount, subj)
	}
	return nil
}

func renderThread(w io.Writer, t *memlayer.Thread) {
	_, _ = fmt.Fprintf(w, "ID:            %s\n", t.ID)
	_, _ = fmt.Fprintf(w, "Namespace:     %s\n", t.Namespace)
	_, _ = fmt.Fprintf(w, "External ID:   %s\n", t.ExternalID)
	if t.Subject != "" {
		_, _ = fmt.Fprintf(w, "Subject:       %s\n", t.Subject)
	}
	_, _ = fmt.Fprintf(w, "Entry count:   %d\n", t.EntryCount)
	if !t.FirstSeen.IsZero() {
		_, _ = fmt.Fprintf(w, "First seen:    %s\n", t.FirstSeen.Local().Format(time.RFC1123))
	}
	if !t.LastSeen.IsZero() {
		_, _ = fmt.Fprintf(w, "Last seen:     %s\n", t.LastSeen.Local().Format(time.RFC1123))
	}
}

func emitMemoryEntries(w io.Writer, refs []memlayer.EntryRef, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		for _, r := range refs {
			if err := enc.Encode(r); err != nil {
				return err
			}
		}
		return nil
	}
	if len(refs) == 0 {
		_, _ = fmt.Fprintln(w, "no entries linked")
		return nil
	}
	_, _ = fmt.Fprintf(w, "%-24s %-32s %-6s %s\n", "NAMESPACE", "ENTRY ID", "ROLE", "DATE")
	for _, r := range refs {
		date := ""
		if !r.Date.IsZero() {
			date = r.Date.Local().Format("2006-01-02 15:04:05")
		}
		role := string(r.Role)
		if role == "" {
			role = "-"
		}
		entryID := r.ID
		if len(entryID) > 30 {
			entryID = entryID[:27] + "..."
		}
		_, _ = fmt.Fprintf(w, "%-24s %-32s %-6s %s\n", r.Namespace, entryID, role, date)
	}
	_, _ = fmt.Fprintln(w,
		strings.Repeat(" ", 0)+"\nUse `loamss memory show <id>` (Phase 2) or query via MCP to read entry content.")
	return nil
}

// --- deps --------------------------------------------------------------

type memoryDeps struct {
	adapter memadapter.Adapter
	store   *memlayer.Store
	layer   memlayer.Layer
}

func (d *memoryDeps) Close() {
	if d.layer != nil {
		_ = d.layer.Close()
	}
	if d.adapter != nil {
		_ = d.adapter.Close(context.Background())
	}
}

func openMemoryDeps(cmd *cobra.Command) (*memoryDeps, error) {
	cfg := config.From(cmd.Context())
	if cfg == nil {
		return nil, errors.New("no config attached to context (programming error in CLI wiring)")
	}
	ctx := cmd.Context()

	adapter, err := memadapter.New(cfg.Memory.Adapter)
	if err != nil {
		return nil, fmt.Errorf("constructing memory adapter %q: %w", cfg.Memory.Adapter, err)
	}
	if err := adapter.Init(ctx, cfg.Memory.Config); err != nil {
		_ = adapter.Close(context.Background())
		return nil, fmt.Errorf("initializing memory adapter: %w", err)
	}
	store, err := memlayer.OpenStore(ctx, filepath.Join(cfg.Runtime.DataDir, "runtime.db"))
	if err != nil {
		_ = adapter.Close(context.Background())
		return nil, fmt.Errorf("opening memory layer store: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
	return &memoryDeps{
		adapter: adapter,
		store:   store,
		layer:   memlayer.New(adapter, store, logger),
	}, nil
}

func init() {
	entitiesListCmd.Flags().StringVar(&entitiesListNamespace, "namespace", "", "restrict to a memory namespace")
	entitiesListCmd.Flags().StringVar(&entitiesListKind, "kind", "", "restrict to a kind (person | organization)")
	entitiesListCmd.Flags().StringVar(&entitiesListAlias, "alias", "", "find entities whose aliases include this value")
	entitiesListCmd.Flags().IntVar(&entitiesListLimit, "limit", 50, "max entities to return")
	entitiesListCmd.Flags().BoolVar(&entitiesListJSON, "json", false, "output as JSONL")
	entitiesCmd.AddCommand(entitiesListCmd)

	entitiesShowCmd.Flags().BoolVar(&entitiesShowJSON, "json", false, "output as JSON")
	entitiesCmd.AddCommand(entitiesShowCmd)

	entitiesEntriesCmd.Flags().IntVar(&entitiesEntriesLimit, "limit", 50, "max entries to return")
	entitiesEntriesCmd.Flags().BoolVar(&entitiesEntriesJSON, "json", false, "output as JSONL")
	entitiesCmd.AddCommand(entitiesEntriesCmd)

	memoryCmd.AddCommand(entitiesCmd)

	threadsListCmd.Flags().StringVar(&threadsListNamespace, "namespace", "", "restrict to a memory namespace")
	threadsListCmd.Flags().IntVar(&threadsListLimit, "limit", 50, "max threads to return")
	threadsListCmd.Flags().BoolVar(&threadsListJSON, "json", false, "output as JSONL")
	threadsCmd.AddCommand(threadsListCmd)

	threadsShowCmd.Flags().BoolVar(&threadsShowJSON, "json", false, "output as JSON")
	threadsCmd.AddCommand(threadsShowCmd)

	threadsEntriesCmd.Flags().IntVar(&threadsEntriesLimit, "limit", 50, "max entries to return")
	threadsEntriesCmd.Flags().BoolVar(&threadsEntriesJSON, "json", false, "output as JSONL")
	threadsCmd.AddCommand(threadsEntriesCmd)

	memoryCmd.AddCommand(threadsCmd)
	rootCmd.AddCommand(memoryCmd)
}
