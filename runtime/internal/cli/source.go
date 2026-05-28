package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	memadapter "github.com/loamss/loamss/runtime/internal/adapter/memory"
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/chroma"   // registers memory:chroma
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/pgvector" // registers memory:pgvector
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/qdrant"   // registers memory:qdrant
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/sqlite"   // registers memory:sqlite-vec
	"github.com/loamss/loamss/runtime/internal/adapter/model"
	_ "github.com/loamss/loamss/runtime/internal/adapter/model/anthropic" // registers model:anthropic
	_ "github.com/loamss/loamss/runtime/internal/adapter/model/dummy"     // registers model:dummy
	_ "github.com/loamss/loamss/runtime/internal/adapter/model/none"      // registers model:none
	_ "github.com/loamss/loamss/runtime/internal/adapter/model/ollama"    // registers model:ollama
	_ "github.com/loamss/loamss/runtime/internal/adapter/model/openai"    // registers model:openai
	"github.com/loamss/loamss/runtime/internal/adapter/storage"
	_ "github.com/loamss/loamss/runtime/internal/adapter/storage/fsencrypted" // registers storage:fs-encrypted
	_ "github.com/loamss/loamss/runtime/internal/adapter/storage/gcs"         // registers storage:gcs
	_ "github.com/loamss/loamss/runtime/internal/adapter/storage/s3"          // registers storage:s3
	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/database"
	memlayer "github.com/loamss/loamss/runtime/internal/memory"
	"github.com/loamss/loamss/runtime/internal/source"
)

// `loamss source` is the data-source lifecycle CLI. Subcommands cover
// the full lifecycle a configured source goes through:
//
//   add          register a new instance of a registered source adapter
//   list         show all configured sources
//   show         show one configured source in detail
//   authenticate run the interactive auth handshake (code-paste in v0.1)
//   sync         trigger one synchronization pass
//   remove       delete the source, its credentials, and stop future syncs
//
// Source connectors themselves (source:gmail, source:calendar, …) are
// registered at process startup via blank imports in cmd/loamss/main.go
// — the same pattern adapter packages use.

var sourceCmd = &cobra.Command{
	Use:   "source",
	Short: "Manage data sources (ingestion connectors)",
	Long: `Sources pull data from external systems (Gmail, Calendar, Slack, …)
into the user's storage and memory.

Subcommands:
  add          register a new source instance
  list         list configured sources
  show         show a source in detail
  authenticate run the auth handshake for a source
  sync         trigger one sync pass
  remove       delete a source (drops credentials, stops future syncs)`,
}

// --- add ---------------------------------------------------------------

var (
	sourceAddName         string
	sourceAddConfig       []string
	sourceAddJSON         bool
	sourceAddAuthenticate bool
)

var sourceAddCmd = &cobra.Command{
	Use:   "add <adapter-id>",
	Short: "Register a new source instance",
	Long: `Register a new instance of a source adapter (e.g. "source:gmail").

The --name flag chooses the user-visible handle; it must be unique
across all configured sources and is used as the principal id in
audit entries and as the memory namespace the source writes into.

--config supplies the source-specific configuration as repeatable
key=value flags. The source validates the shape during Init; bad
config surfaces as an error here, not silently at the next sync.

` + "`loamss source add`" + ` does not run the interactive auth flow by
default. Pass --authenticate to chain into ` + "`loamss source authenticate`" + `
immediately, or run that command separately later.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		adapterID := args[0]
		if !isValidSourceID(adapterID) {
			return fmt.Errorf("invalid source adapter id %q: expected \"source:<name>\"", adapterID)
		}
		if strings.TrimSpace(sourceAddName) == "" {
			return errors.New("--name is required")
		}
		cfgMap, err := parseKVFlags(sourceAddConfig)
		if err != nil {
			return fmt.Errorf("--config: %w", err)
		}

		deps, err := openSourceDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		// Validate the adapter id is registered before we touch the
		// store. Saves the user from a "registered? what does that
		// even mean?" support ticket later.
		if !sourceRegistered(adapterID) {
			return fmt.Errorf("source adapter %q is not registered (known: %s)",
				adapterID, strings.Join(source.Registered(), ", "))
		}

		// Persist the record.
		ctx := cmd.Context()
		out, err := deps.store.Insert(ctx, source.Configured{
			Name:      sourceAddName,
			AdapterID: adapterID,
			Config:    cfgMap,
		})
		if err != nil {
			return err
		}

		// Audit.
		_, _ = deps.audit.Append(ctx, audit.Entry{
			Type:    "source.added",
			Actor:   audit.Actor{Kind: audit.ActorUser, ID: "cli"},
			Subject: &audit.Subject{Kind: audit.SubjectSource, ID: out.Name},
			Outcome: audit.OutcomeSuccess,
			Data: map[string]any{
				"adapter_id":        adapterID,
				"source_id":         out.ID,
				"config_keys":       sortedKeys(cfgMap),
				"will_authenticate": sourceAddAuthenticate,
			},
		})

		if sourceAddJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"✓ Added source %q (%s, %s)\n", out.Name, out.AdapterID, out.ID)
		if !sourceAddAuthenticate {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"  Next: loamss source authenticate %s\n", out.Name)
			return nil
		}
		// Chain into authenticate.
		_, _ = fmt.Fprintln(cmd.OutOrStdout())
		return runSourceAuthenticate(cmd, deps, out.Name)
	},
}

// --- list --------------------------------------------------------------

var sourceListJSON bool

var sourceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured sources",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		deps, err := openSourceDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()
		all, err := deps.store.List(cmd.Context())
		if err != nil {
			return err
		}
		return emitSources(cmd.OutOrStdout(), all, sourceListJSON)
	},
}

// --- show --------------------------------------------------------------

var sourceShowJSON bool

var sourceShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a configured source in detail",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openSourceDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()
		c, err := deps.store.Get(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if sourceShowJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(c)
		}
		renderSourceDetail(cmd.OutOrStdout(), c)
		return nil
	},
}

// --- authenticate ------------------------------------------------------

var sourceAuthenticateCmd = &cobra.Command{
	Use:   "authenticate <name>",
	Short: "Run the interactive auth handshake for a source",
	Long: `Walk a configured source through its auth handshake.

v0.1 supports two flow kinds:

  none         the source needs no interactive auth (e.g., it reads
               static config). CompleteAuth is called with empty params
               so the source can validate the static credential.
  code_paste   the source returns a URL; the CLI prints it; the user
               opens the URL, completes the flow there, and pastes the
               returned code back. The CLI hands the code to CompleteAuth.

Browser-redirect and device-code flows arrive with the Gmail connector.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openSourceDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()
		return runSourceAuthenticate(cmd, deps, args[0])
	},
}

func runSourceAuthenticate(cmd *cobra.Command, deps *sourceDeps, name string) error {
	ctx := cmd.Context()
	c, err := deps.store.Get(ctx, name)
	if err != nil {
		return err
	}

	src, err := buildSourceInstance(ctx, deps, c)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close(ctx) }()

	flow, err := src.BeginAuth(ctx)
	if err != nil {
		return fmt.Errorf("BeginAuth: %w", err)
	}

	switch flow.Kind {
	case source.AuthFlowNone:
		if err := src.CompleteAuth(ctx, map[string]string{}); err != nil {
			return fmt.Errorf("CompleteAuth: %w", err)
		}
	case source.AuthFlowBrowser:
		if flow.URL != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"Open this URL in your browser:\n\n  %s\n\n", flow.URL)
		}
		if flow.Instructions != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n\n", flow.Instructions)
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(),
			"Waiting for the browser callback (Ctrl-C to cancel)...")
		// CompleteAuth blocks on the source's loopback listener; the
		// source captures the redirect itself.
		if err := src.CompleteAuth(ctx, map[string]string{}); err != nil {
			return fmt.Errorf("CompleteAuth: %w", err)
		}
	case source.AuthFlowCodePaste:
		if flow.URL != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"Open this URL in your browser:\n\n  %s\n\n", flow.URL)
		}
		if flow.Instructions != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n\n", flow.Instructions)
		}
		_, _ = fmt.Fprint(cmd.OutOrStdout(), "Paste the returned code: ")
		reader := bufio.NewReader(cmd.InOrStdin())
		code, _ := reader.ReadString('\n')
		code = strings.TrimSpace(code)
		if code == "" {
			return errors.New("no code entered")
		}
		if err := src.CompleteAuth(ctx, map[string]string{"code": code}); err != nil {
			return fmt.Errorf("CompleteAuth: %w", err)
		}
	default:
		return fmt.Errorf("auth flow %q not yet supported by the CLI", flow.Kind)
	}

	_, _ = deps.audit.Append(ctx, audit.Entry{
		Type:    "source.authenticated",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: "cli"},
		Subject: &audit.Subject{Kind: audit.SubjectSource, ID: c.Name},
		Outcome: audit.OutcomeSuccess,
		Data: map[string]any{
			"adapter_id": c.AdapterID,
			"flow_kind":  string(flow.Kind),
		},
	})

	_, _ = fmt.Fprintf(cmd.OutOrStdout(),
		"✓ Authenticated source %q\n  Next: loamss source sync %s\n", c.Name, c.Name)
	return nil
}

// --- sync --------------------------------------------------------------

var sourceSyncJSON bool

var sourceSyncCmd = &cobra.Command{
	Use:   "sync <name>",
	Short: "Run one synchronization pass for a source",
	Long: `Drive one Sync pass for the named source.

The runtime hands the source its persisted cursor and writes back
whatever cursor the source returns. Counters (records added/updated,
bytes ingested) plus any per-record errors are reported and persisted
as the source's last_sync state.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openSourceDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		ctx := cmd.Context()
		name := args[0]
		c, err := deps.store.Get(ctx, name)
		if err != nil {
			return err
		}
		src, err := buildSourceInstance(ctx, deps, c)
		if err != nil {
			return err
		}
		defer func() { _ = src.Close(ctx) }()

		result, syncErr := source.RunSync(ctx, src, deps.store, deps.audit, c,
			source.RunSyncActor{Kind: audit.ActorUser, ID: "cli"})
		if syncErr != nil {
			return fmt.Errorf("sync %s: %w", c.Name, syncErr)
		}

		if sourceSyncJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(result.Summary)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"✓ Synced %q: %d added, %d updated, %d bytes, %d errors (%v)\n",
			c.Name, result.RecordsAdded, result.RecordsUpdated,
			result.BytesIngested, result.Errors,
			result.Finished.Sub(result.Started).Round(time.Millisecond))
		return nil
	},
}

// --- remove ------------------------------------------------------------

var (
	sourceRemoveYes    bool
	sourceRemoveReason string
)

var sourceRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Delete a configured source",
	Long: `Delete a configured source. The credential blob is removed from
storage; the cursor and any in-flight sync state are discarded.
Idempotent on an already-deleted source.

Prompts for confirmation when stdin is a terminal; pass --yes to skip.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := openSourceDeps(cmd)
		if err != nil {
			return err
		}
		defer deps.Close()

		ctx := cmd.Context()
		name := args[0]
		c, err := deps.store.Get(ctx, name)
		if err != nil {
			return err
		}
		if !sourceRemoveYes && isTerminal(os.Stdin) {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"About to remove source %q (%s).\nCredentials will be deleted from storage; previously-ingested data is left in place.\nContinue? [y/N] ",
				c.Name, c.AdapterID)
			reader := bufio.NewReader(cmd.InOrStdin())
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer != "y" && answer != "yes" {
				return errors.New("aborted")
			}
		}

		credStore := source.NewStorageCredentialStore(deps.storage, c.Name)
		_ = credStore.Delete(ctx)

		if err := deps.store.Delete(ctx, c.Name); err != nil {
			return err
		}

		_, _ = deps.audit.Append(ctx, audit.Entry{
			Type:    "source.removed",
			Actor:   audit.Actor{Kind: audit.ActorUser, ID: "cli"},
			Subject: &audit.Subject{Kind: audit.SubjectSource, ID: c.Name},
			Outcome: audit.OutcomeSuccess,
			Data: map[string]any{
				"adapter_id": c.AdapterID,
				"reason":     sourceRemoveReason,
			},
		})

		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "✓ Removed source %q\n", c.Name)
		return nil
	},
}

// --- shared rendering --------------------------------------------------

func emitSources(w io.Writer, list []source.Configured, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		for _, c := range list {
			c.Config = maskedConfig(c.Config)
			if err := enc.Encode(c); err != nil {
				return err
			}
		}
		return nil
	}
	if len(list) == 0 {
		_, _ = fmt.Fprintln(w, "no sources configured")
		return nil
	}
	_, _ = fmt.Fprintf(w, "%-24s %-20s %-10s %s\n", "NAME", "ADAPTER", "STATUS", "LAST SYNC")
	for _, c := range list {
		status := c.LastSyncStatus
		if status == "" {
			status = "—"
		}
		lastSync := "never"
		if !c.LastSyncAt.IsZero() {
			lastSync = c.LastSyncAt.Local().Format("2006-01-02 15:04:05")
		}
		_, _ = fmt.Fprintf(w, "%-24s %-20s %-10s %s\n", c.Name, c.AdapterID, status, lastSync)
	}
	return nil
}

func renderSourceDetail(w io.Writer, c *source.Configured) {
	_, _ = fmt.Fprintf(w, "Source:        %s\n", c.Name)
	_, _ = fmt.Fprintf(w, "Adapter:       %s\n", c.AdapterID)
	_, _ = fmt.Fprintf(w, "ID:            %s\n", c.ID)
	_, _ = fmt.Fprintf(w, "Added:         %s\n", c.AddedAt.Local().Format(time.RFC1123))
	_, _ = fmt.Fprintf(w, "Updated:       %s\n", c.UpdatedAt.Local().Format(time.RFC1123))
	if c.LastSyncAt.IsZero() {
		_, _ = fmt.Fprintln(w, "Last sync:     never")
	} else {
		_, _ = fmt.Fprintf(w, "Last sync:     %s (%s)\n",
			c.LastSyncAt.Local().Format(time.RFC1123), c.LastSyncStatus)
	}
	if len(c.Config) > 0 {
		_, _ = fmt.Fprintln(w, "Config:")
		for _, k := range sortedKeys(c.Config) {
			_, _ = fmt.Fprintf(w, "  %s: %v\n", k, displayConfigValue(k, c.Config[k]))
		}
	}
	if len(c.LastSyncSummary) > 0 {
		_, _ = fmt.Fprintln(w, "Last sync summary:")
		for _, k := range sortedKeys(c.LastSyncSummary) {
			_, _ = fmt.Fprintf(w, "  %s: %v\n", k, c.LastSyncSummary[k])
		}
	}
}

// sensitiveConfigKeyParts identify config keys whose values should never
// be rendered to a terminal or to the JSONL list output. The match is a
// case-insensitive substring on the key, so "client_secret",
// "api_key", "OAuth-Token", etc. all hit.
var sensitiveConfigKeyParts = []string{"secret", "password", "token", "api_key", "credential"}

func isSensitiveConfigKey(key string) bool {
	lk := strings.ToLower(key)
	for _, part := range sensitiveConfigKeyParts {
		if strings.Contains(lk, part) {
			return true
		}
	}
	return false
}

// displayConfigValue returns the value to print for a config entry,
// substituting "(set, hidden)" / "(unset)" for sensitive keys. Use it
// for human-readable and list-JSONL output; the per-source `show --json`
// path stays verbatim because it is an opted-in programmatic surface.
func displayConfigValue(key string, v any) any {
	if !isSensitiveConfigKey(key) {
		return v
	}
	if isEmptyConfigValue(v) {
		return "(unset)"
	}
	return "(set, hidden)"
}

func isEmptyConfigValue(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
		return true
	}
	return false
}

func maskedConfig(in map[string]any) map[string]any {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = displayConfigValue(k, v)
	}
	return out
}

// --- deps + helpers ----------------------------------------------------

// sourceDeps bundles the persistence + storage handles the source CLI
// commands share. Lifetime: one set per CLI invocation.
type sourceDeps struct {
	store         *source.Store
	audit         *audit.SQLite
	storage       storage.Adapter
	memAdapter    memadapter.Adapter
	memLayer      memlayer.Layer
	modelAdapters []model.Adapter
	logger        *slog.Logger
	db            *database.Database // owning handle; closed last
}

func (d *sourceDeps) Close() {
	if d.store != nil {
		_ = d.store.Close()
	}
	if d.audit != nil {
		_ = d.audit.Close(context.Background())
	}
	if d.storage != nil {
		_ = d.storage.Close(context.Background())
	}
	if d.memLayer != nil {
		_ = d.memLayer.Close()
	}
	if d.memAdapter != nil {
		_ = d.memAdapter.Close(context.Background())
	}
	for _, a := range d.modelAdapters {
		_ = a.Close(context.Background())
	}
	if d.db != nil {
		_ = d.db.Close()
	}
}

func openSourceDeps(cmd *cobra.Command) (*sourceDeps, error) {
	cfg := config.From(cmd.Context())
	if cfg == nil {
		return nil, errors.New("no config attached to context (programming error in CLI wiring)")
	}
	ctx := cmd.Context()

	// Open the shared runtime database (SQLite by default; Postgres
	// when configured). source.Store + memlayer.Store both ride on
	// this one handle.
	db, err := openRuntimeDB(ctx, cfg)
	if err != nil {
		return nil, err
	}

	store, err := source.OpenStoreWith(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	w, err := audit.OpenSQLite(ctx, filepath.Join(cfg.Runtime.DataDir, "audit.db"))
	if err != nil {
		_ = store.Close()
		_ = db.Close()
		return nil, err
	}
	stor, err := storage.New(cfg.Storage.Adapter)
	if err != nil {
		_ = store.Close()
		_ = w.Close(context.Background())
		_ = db.Close()
		return nil, fmt.Errorf("constructing storage adapter %q: %w", cfg.Storage.Adapter, err)
	}
	if err := stor.Init(ctx, cfg.Storage.Config); err != nil {
		_ = store.Close()
		_ = w.Close(context.Background())
		_ = db.Close()
		return nil, fmt.Errorf("initializing storage adapter: %w", err)
	}

	// Memory adapter + layer. The adapter is the vector store the
	// source writes into via the layer; the layer's derived state
	// (entities + threads) goes into the same runtime database the
	// source store uses.
	memAdapter, err := memadapter.New(cfg.Memory.Adapter)
	if err != nil {
		_ = store.Close()
		_ = w.Close(context.Background())
		_ = stor.Close(context.Background())
		_ = db.Close()
		return nil, fmt.Errorf("constructing memory adapter %q: %w", cfg.Memory.Adapter, err)
	}
	if err := memAdapter.Init(ctx, cfg.Memory.Config); err != nil {
		_ = store.Close()
		_ = w.Close(context.Background())
		_ = stor.Close(context.Background())
		_ = db.Close()
		return nil, fmt.Errorf("initializing memory adapter: %w", err)
	}
	layerStore, err := memlayer.OpenStoreWith(ctx, db)
	if err != nil {
		_ = store.Close()
		_ = w.Close(context.Background())
		_ = stor.Close(context.Background())
		_ = memAdapter.Close(context.Background())
		_ = db.Close()
		return nil, fmt.Errorf("opening memory layer store: %w", err)
	}
	logger := newLogger(cfg.Log, cmd.ErrOrStderr())

	// Model adapters for auto-embedding on sync. Without an embedder
	// wired here, `loamss source sync` lands content into the memory
	// adapter but with no vectors, so memory.query returns empty even
	// when the user has an embedding model configured. We only open
	// adapters when the user has actually configured `models:` —
	// opening model:none as a fallback would add a chatty startup log
	// to every CLI invocation for no behavior gain (no embedder means
	// the layer just stores without vectors, same as before).
	var modelAdapters []model.Adapter
	var embedder memlayer.Embedder
	if len(cfg.Models) > 0 {
		modelAdapters, err = openAllModelAdapters(ctx, cfg.Models)
		if err != nil {
			_ = store.Close()
			_ = w.Close(context.Background())
			_ = stor.Close(context.Background())
			_ = memAdapter.Close(context.Background())
			_ = layerStore.Close()
			return nil, fmt.Errorf("opening model adapters: %w", err)
		}
		embedAdapter := pickEmbeddingAdapter(ctx, modelAdapters, logger)
		embedder = &embeddingAdapterBridge{
			adapter: embedAdapter,
			picker:  pickEmbeddingModelID,
		}
	}
	layer := memlayer.New(memAdapter, layerStore, embedder, logger)

	return &sourceDeps{
		store:         store,
		audit:         w,
		storage:       stor,
		memAdapter:    memAdapter,
		memLayer:      layer,
		modelAdapters: modelAdapters,
		logger:        logger,
		db:            db,
	}, nil
}

// buildSourceInstance constructs and Inits the source connector
// declared by c.AdapterID, wiring it to the user's storage adapter
// and a per-source credential store. Thin wrapper around
// source.Build — the CLI scopes the logger first (so log lines
// carry source_name + source_id attributes) then hands the env
// over to the shared builder.
func buildSourceInstance(ctx context.Context, deps *sourceDeps, c *source.Configured) (source.Source, error) {
	scoped := deps.logger.With("source_name", c.Name, "source_id", c.AdapterID)
	return source.Build(ctx, source.BuildEnv{
		Storage: deps.storage,
		Memory:  memoryBridge{layer: deps.memLayer},
		Logger:  slogShim{scoped},
	}, c)
}

// memoryBridge adapts memlayer.Layer to the narrow
// source.MemoryAdapter interface that source connectors see. The
// two types carry identical fields under different package names;
// the bridge translates without touching the data.
//
// Without this bridge the source package would need to import
// memlayer directly, coupling the source SPI to the layer's
// concrete type — which we don't want.
type memoryBridge struct {
	layer memlayer.Layer
}

func (b memoryBridge) Upsert(ctx context.Context, entry source.MemoryEntry) error {
	return b.layer.Upsert(ctx, memlayer.Entry{
		Namespace:  entry.Namespace,
		ID:         entry.ID,
		Content:    entry.Content,
		Metadata:   entry.Metadata,
		Embeddings: entry.Embeddings,
	})
}

func (b memoryBridge) Delete(ctx context.Context, namespace, id string) error {
	return b.layer.Delete(ctx, namespace, id)
}

// slogShim adapts *slog.Logger to the narrow source.Logger interface.
// Sources see only the four canonical levels; slog's structured args
// flow through unchanged.
type slogShim struct{ l *slog.Logger }

func (s slogShim) Info(msg string, args ...any)  { s.l.Info(msg, args...) }
func (s slogShim) Warn(msg string, args ...any)  { s.l.Warn(msg, args...) }
func (s slogShim) Error(msg string, args ...any) { s.l.Error(msg, args...) }
func (s slogShim) Debug(msg string, args ...any) { s.l.Debug(msg, args...) }

func sourceRegistered(id string) bool {
	for _, registered := range source.Registered() {
		if registered == id {
			return true
		}
	}
	return false
}

func isValidSourceID(id string) bool {
	if !strings.HasPrefix(id, "source:") {
		return false
	}
	name := strings.TrimPrefix(id, "source:")
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func parseKVFlags(flags []string) (map[string]any, error) {
	out := map[string]any{}
	for _, f := range flags {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("expected key=value, got %q", f)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			return nil, fmt.Errorf("empty key in %q", f)
		}
		out[k] = v
	}
	return out, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func init() {
	sourceAddCmd.Flags().StringVar(&sourceAddName, "name", "", "user-visible handle (required)")
	sourceAddCmd.Flags().StringArrayVar(&sourceAddConfig, "config", nil, "key=value source config (repeatable)")
	sourceAddCmd.Flags().BoolVar(&sourceAddJSON, "json", false, "output the new record as JSON")
	sourceAddCmd.Flags().BoolVar(&sourceAddAuthenticate, "authenticate", false, "chain into `source authenticate` after add")
	sourceCmd.AddCommand(sourceAddCmd)

	sourceListCmd.Flags().BoolVar(&sourceListJSON, "json", false, "output as JSONL")
	sourceCmd.AddCommand(sourceListCmd)

	sourceShowCmd.Flags().BoolVar(&sourceShowJSON, "json", false, "output as JSON")
	sourceCmd.AddCommand(sourceShowCmd)

	sourceCmd.AddCommand(sourceAuthenticateCmd)

	sourceSyncCmd.Flags().BoolVar(&sourceSyncJSON, "json", false, "output the summary as JSON")
	sourceCmd.AddCommand(sourceSyncCmd)

	sourceRemoveCmd.Flags().BoolVarP(&sourceRemoveYes, "yes", "y", false, "skip the confirmation prompt")
	sourceRemoveCmd.Flags().StringVar(&sourceRemoveReason, "reason", "", "optional note recorded in the audit entry")
	sourceCmd.AddCommand(sourceRemoveCmd)

	rootCmd.AddCommand(sourceCmd)
}
