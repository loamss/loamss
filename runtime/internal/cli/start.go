package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/sqlite" // registers memory:sqlite
	"github.com/loamss/loamss/runtime/internal/adapter/model"
	_ "github.com/loamss/loamss/runtime/internal/adapter/model/anthropic" // registers model:anthropic
	_ "github.com/loamss/loamss/runtime/internal/adapter/model/dummy"     // registers model:dummy
	_ "github.com/loamss/loamss/runtime/internal/adapter/model/none"      // registers model:none
	_ "github.com/loamss/loamss/runtime/internal/adapter/model/ollama"    // registers model:ollama
	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/mcp"
	memlayer "github.com/loamss/loamss/runtime/internal/memory"
	"github.com/loamss/loamss/runtime/internal/permission"
	"github.com/loamss/loamss/runtime/internal/server"
)

var startShutdownTimeout time.Duration

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the Loamss runtime daemon",
	Long: `Start the Loamss runtime daemon in the foreground.

v0.1 binds the configured listen_addr and serves a minimal HTTP
surface: /healthz returns a JSON status object including the runtime
version. The MCP surface, capsule host, permission framework, and
other Phase 1 components arrive in subsequent commits and mount onto
the same listener.

Press Ctrl-C (or send SIGTERM) to shut down gracefully. The shutdown
timeout governs how long the runtime waits for in-flight requests
to complete before forcing the listener closed.`,
	Args: cobra.NoArgs,
	RunE: runStart,
}

func runStart(cmd *cobra.Command, _ []string) error {
	cfg := config.From(cmd.Context())
	if cfg == nil {
		return fmt.Errorf("no config attached to context (programming error in the CLI wiring)")
	}

	logger := newLogger(cfg.Log, cmd.ErrOrStderr())
	logger.Info("loamss starting",
		"version", version,
		"commit", commit,
		"data_dir", cfg.Runtime.DataDir,
		"listen_addr", cfg.Runtime.ListenAddr,
	)

	// Open the permission store + audit writer; both live for the
	// runtime's full lifetime and are shared with the MCP handler.
	ctx := cmd.Context()
	store, err := permission.Open(ctx, filepath.Join(cfg.Runtime.DataDir, "runtime.db"))
	if err != nil {
		return fmt.Errorf("opening permission store: %w", err)
	}
	defer func() { _ = store.Close() }()

	auditWriter, err := audit.OpenSQLite(ctx, filepath.Join(cfg.Runtime.DataDir, "audit.db"))
	if err != nil {
		return fmt.Errorf("opening audit log: %w", err)
	}
	defer func() { _ = auditWriter.Close(context.Background()) }()

	engine := permission.NewEngine(store, auditWriter)

	// Memory adapter — used by memory.show, memory.query, and the
	// memory:// resource provider.
	memAdapter, err := memory.New(cfg.Memory.Adapter)
	if err != nil {
		return fmt.Errorf("constructing memory adapter %q: %w", cfg.Memory.Adapter, err)
	}
	if err := memAdapter.Init(ctx, cfg.Memory.Config); err != nil {
		return fmt.Errorf("initializing memory adapter: %w", err)
	}
	defer func() { _ = memAdapter.Close(context.Background()) }()

	// Memory layer — sits above the adapter, derives entities + threads
	// from the metadata sources write. Its own SQLite tables live in
	// runtime.db; opening here lets MCP tools (entities.*, threads.*)
	// share the same Layer instance the source CLI writes through.
	memLayerStore, err := memlayer.OpenStore(ctx, filepath.Join(cfg.Runtime.DataDir, "runtime.db"))
	if err != nil {
		return fmt.Errorf("opening memory layer store: %w", err)
	}
	defer func() { _ = memLayerStore.Close() }()
	memLayer := memlayer.New(memAdapter, memLayerStore, logger)

	// Model adapters — multiple may be configured (e.g., Anthropic
	// for generation + an OpenAI/voyage-style adapter for embeddings).
	// We open every configured adapter; memory.query picks an
	// embedding-capable one via the embedding-router below. The
	// full model router (per-task routing rules + cost ceilings +
	// data-class filters) is future work.
	//
	// When zero adapters are configured, we fall back to model:none
	// so the runtime has a valid Adapter value to pass to memory.query
	// — semantic memory degrades gracefully via ErrModelDisabled.
	modelAdapters, err := openAllModelAdapters(ctx, cfg.Models)
	if err != nil {
		return err
	}
	defer func() {
		for _, a := range modelAdapters {
			_ = a.Close(context.Background())
		}
	}()

	// embeddingAdapter is the adapter memory.query uses for
	// query-text embedding. Selected by walking the configured
	// adapters and picking the first whose Models() advertises the
	// "embeddings" capability. When none match (e.g., user has only
	// model:anthropic configured), memory.query reports the
	// graceful-degradation isError so callers see a clear "no
	// embedding model" message rather than a cryptic failure.
	embeddingAdapter := pickEmbeddingAdapter(ctx, modelAdapters, logger)

	// Tool registry — runtime tools register at startup. Capsule-
	// provided tools will join the registry at install time
	// (Phase 1b).
	tools := mcp.NewRegistry()
	for _, t := range []mcp.Tool{
		mcp.NewClientInfoTool(),
		mcp.NewAuditReadTool(auditWriter),
		mcp.NewMemoryShowTool(memAdapter),
		mcp.NewMemoryQueryTool(memAdapter, embeddingAdapter),
		mcp.NewEntitiesListTool(memLayer),
		mcp.NewEntitiesShowTool(memLayer),
		mcp.NewEntitiesEntriesTool(memLayer),
		mcp.NewThreadsListTool(memLayer),
		mcp.NewThreadsShowTool(memLayer),
		mcp.NewThreadsEntriesTool(memLayer),
	} {
		if err := tools.Register(t); err != nil {
			return fmt.Errorf("registering tool %q: %w", t.Name, err)
		}
	}

	// Resource registry — runtime providers register at startup;
	// capsule providers join at install. memory:// is the one
	// default; storage://, capsule-specific schemes (e.g.,
	// vibez.content://) land as those subsystems are wired.
	resources := mcp.NewResourceRegistry()
	if err := resources.Register(mcp.NewMemoryResourceProvider(memAdapter)); err != nil {
		return fmt.Errorf("registering memory resource provider: %w", err)
	}

	// Capsule host — supervises all installed capsules. Spawns
	// each capsule's subprocess, runs the MCP handshake, mounts
	// every capsule's advertised tools into the runtime's tool
	// registry under <capsule>.<tool> names. Capsule callbacks
	// (capsule → runtime) flow through a stub handler today;
	// permission-checked dispatch lands in the next commit.
	capStore, err := capsule.OpenStore(ctx, filepath.Join(cfg.Runtime.DataDir, "runtime.db"))
	if err != nil {
		return fmt.Errorf("opening capsule store: %w", err)
	}
	defer func() { _ = capStore.Close() }()

	host := capsule.NewHost(capStore, engine, auditWriter, tools, logger)
	if _, err := host.Start(ctx); err != nil {
		return fmt.Errorf("starting capsule host: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := host.Stop(stopCtx); err != nil {
			logger.Warn("capsule host stop", "err", err)
		}
	}()

	srv := server.New(server.Options{
		Addr:      cfg.Runtime.ListenAddr,
		Logger:    logger,
		Version:   version,
		Engine:    engine,
		Audit:     auditWriter,
		Tools:     tools,
		Resources: resources,
	})

	stop := installSignalTrap(logger)
	return runServer(srv, stop, startShutdownTimeout, logger)
}

// runServer launches the server in a goroutine, waits for either it to
// exit or `stop` to close, then performs a bounded graceful shutdown.
// Separated from runStart so tests can drive it with a channel of their
// own without involving the OS signal machinery.
func runServer(srv *server.Server, stop <-chan struct{}, shutdownTimeout time.Duration, logger *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		// Server exited on its own (likely a bind failure).
		return err
	case <-stop:
		// Got a shutdown signal; fall through to graceful shutdown below.
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		return err
	}
	// Drain the now-stopped ListenAndServe goroutine.
	return <-errCh
}

// openAllModelAdapters opens every configured model adapter and
// returns the slice in config order. When zero adapters are
// configured, returns a single-element slice containing model:none
// so callers always have at least one valid Adapter to consult.
//
// The full model router (per-task routing rules + cost ceilings +
// data-class filters) is future work; for now this is a flat list
// and selection is by capability via pickEmbeddingAdapter.
func openAllModelAdapters(ctx context.Context, configs []config.AdapterConfig) ([]model.Adapter, error) {
	if len(configs) == 0 {
		a, err := model.New("model:none")
		if err != nil {
			return nil, fmt.Errorf("constructing model:none fallback: %w", err)
		}
		if err := a.Init(ctx, nil); err != nil {
			return nil, fmt.Errorf("initializing model:none: %w", err)
		}
		return []model.Adapter{a}, nil
	}
	out := make([]model.Adapter, 0, len(configs))
	for i, c := range configs {
		a, err := model.New(c.Adapter)
		if err != nil {
			// Close everything we already opened before returning.
			for _, prev := range out {
				_ = prev.Close(context.Background())
			}
			return nil, fmt.Errorf("constructing model adapter[%d] %q: %w", i, c.Adapter, err)
		}
		if err := a.Init(ctx, c.Config); err != nil {
			for _, prev := range out {
				_ = prev.Close(context.Background())
			}
			return nil, fmt.Errorf("initializing model adapter[%d] %q: %w", i, c.Adapter, err)
		}
		out = append(out, a)
	}
	return out, nil
}

// pickEmbeddingAdapter walks adapters and returns the first one
// whose Models() advertises the "embeddings" capability. Returns
// model:none if none match — memory.query then surfaces graceful
// degradation via ErrModelDisabled.
//
// The slow part (a Models() call per adapter) runs once at startup;
// the result is captured in memory.query's closure.
func pickEmbeddingAdapter(ctx context.Context, adapters []model.Adapter, logger *slog.Logger) model.Adapter {
	for _, a := range adapters {
		ms, err := a.Models(ctx)
		if err != nil {
			logger.Warn("model adapter Models() failed", "err", err)
			continue
		}
		for _, m := range ms {
			for _, cap := range m.Capabilities {
				if cap == "embeddings" {
					logger.Info("embedding adapter selected",
						"model", m.ID, "embedding_dim", m.EmbeddingDim)
					return a
				}
			}
		}
	}
	// Fallback. memory.query will report graceful degradation.
	logger.Info("no embedding-capable model adapter configured; memory.query will degrade gracefully")
	none, err := model.New("model:none")
	if err != nil {
		// model:none is registered in init() — this can only fail
		// if the registry got corrupted, which is unrecoverable.
		logger.Error("model:none unavailable", "err", err)
		return nil
	}
	if err := none.Init(ctx, nil); err != nil {
		logger.Error("model:none init failed", "err", err)
		return nil
	}
	return none
}

// installSignalTrap returns a channel that closes when SIGINT or SIGTERM
// is received. The handler is registered for the lifetime of the process.
func installSignalTrap(logger *slog.Logger) <-chan struct{} {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	stop := make(chan struct{})
	go func() {
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig.String())
		close(stop)
	}()
	return stop
}

// newLogger constructs an slog.Logger from the resolved log config,
// emitting to the given writer (typically stderr).
func newLogger(cfg config.LogConfig, w io.Writer) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler)
}

func init() {
	startCmd.Flags().DurationVar(&startShutdownTimeout, "shutdown-timeout", 5*time.Second,
		"how long to wait for in-flight requests during graceful shutdown")
	rootCmd.AddCommand(startCmd)
}
