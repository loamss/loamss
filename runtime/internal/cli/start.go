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

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/mcp"
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
	tools := mcp.NewRegistry()

	srv := server.New(server.Options{
		Addr:    cfg.Runtime.ListenAddr,
		Logger:  logger,
		Version: version,
		Engine:  engine,
		Audit:   auditWriter,
		Tools:   tools,
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
