// Package server hosts the runtime's HTTP surface. It owns the
// http.Server lifecycle and the mux that wires together the public
// endpoints: /healthz, /version, /pair (pairing redemption), and
// /mcp (the MCP JSON-RPC + SSE surface).
//
// The HTTP layer is deliberately thin. Business logic lives in
// internal/permission (capability gating), internal/mcp (MCP
// protocol), and the adapter packages. server.go only does:
//
//   - Route registration and middleware composition
//   - Request/response shape for /pair (the one non-MCP JSON endpoint)
//   - Server lifecycle (listen, serve, graceful shutdown)
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/console"
	"github.com/loamss/loamss/runtime/internal/mcp"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// Options configures a new Server.
type Options struct {
	// Addr is the host:port to bind to (e.g. "127.0.0.1:7777"). Required.
	Addr string

	// Logger is used for server-level events (start, shutdown, request errors).
	// Required.
	Logger *slog.Logger

	// Version is the runtime's build version, reported in /healthz responses
	// and as the MCP server identity so external probes/clients can verify
	// which build is running.
	Version string

	// Engine is the permission framework. Required for the /pair and /mcp
	// endpoints; when nil, only /healthz and /version are mounted (matches
	// the v0.1 minimum and the existing tests that exercise just those).
	Engine *permission.Engine

	// Audit is the audit log writer. Required when Engine is set; the
	// MCP handler emits audit entries through it.
	Audit audit.Writer

	// Tools is the MCP tool registry. Required when Engine is set;
	// commit 1 registers nothing into it, but the registry must be
	// constructed so the MCP surface can advertise the (empty) tool
	// set in initialize.
	Tools *mcp.Registry

	// Resources is the MCP resource provider registry. Required
	// when Engine is set. The runtime registers a memory provider
	// at startup; capsule providers join later. resources/list and
	// resources/read dispatch through this.
	Resources *mcp.ResourceRegistry

	// ConfigPath is where /console/init writes the wizard's
	// collected configuration. Optional; when empty the handler
	// uses config.DefaultPath() (typically ~/.loamss/config.yaml).
	// Tests override this to land in a t.TempDir.
	ConfigPath string

	// BaseConfig is the currently-running runtime configuration.
	// /console/init starts from a clone of this when constructing
	// the file to persist, so the runtime/audit/log sections the
	// wizard doesn't collect carry forward from the live daemon
	// (rather than being reset to library defaults). Optional;
	// when nil the handler falls back to config.Default().
	BaseConfig *config.Config
}

// Server wraps the underlying http.Server with a stable API surface and
// keeps internal concerns (mux setup, shutdown coordination) encapsulated.
type Server struct {
	httpSrv *http.Server
	logger  *slog.Logger
	addr    string
	version string

	// engine + audit are non-nil iff the MCP surface is mounted.
	engine *permission.Engine
	audit  audit.Writer

	// configPath is where /console/init persists the wizard's payload.
	// Empty means "ask config.DefaultPath() at request time"; tests pass
	// an explicit path to redirect writes into a temp dir.
	configPath string

	// baseConfig is the daemon's currently-running config. Used as the
	// starting point for /console/init so the wizard preserves the
	// runtime/audit/log sections it doesn't collect (data_dir,
	// listen_addr, redaction_level, etc.). nil → handler falls back
	// to config.Default().
	baseConfig *config.Config
}

// New constructs a Server. The HTTP listener is not bound until
// ListenAndServe or Serve is called.
//
// When Options.Engine is nil, only the basic endpoints (/healthz,
// /version) are mounted — useful for tests and for very early-boot
// scenarios where the permission framework isn't ready yet. When
// Engine is provided, /pair and /mcp join the mux.
func New(opts Options) *Server {
	s := &Server{
		addr:       opts.Addr,
		logger:     opts.Logger,
		version:    opts.Version,
		engine:     opts.Engine,
		audit:      opts.Audit,
		configPath: opts.ConfigPath,
		baseConfig: opts.BaseConfig,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)

	// /console/init — accepts the first-run wizard's collected config
	// and (eventually) writes it to disk. v0.1 ships as a stub that
	// echoes back the payload + acknowledges receipt; full config-file
	// writing lands in the next commit. Restricted to localhost via the
	// listener binding (the runtime defaults to 127.0.0.1); the
	// endpoint takes no auth because the wizard runs before any client
	// has been paired.
	mux.HandleFunc("POST /console/init", s.handleConsoleInit)

	if opts.Engine != nil {
		// /pair: pairing redemption, unauthenticated by design (the
		// pairing code IS the auth token for this one request).
		mux.HandleFunc("POST /pair", s.handlePair)

		// /mcp: bearer-authenticated, dispatches MCP JSON-RPC (POST)
		// and SSE (GET). The mcp.Handler runs under the auth
		// middleware so by the time it sees a request, the principal
		// is in context.
		if opts.Tools == nil {
			panic("server: Options.Tools must be non-nil when Engine is non-nil")
		}
		if opts.Resources == nil {
			panic("server: Options.Resources must be non-nil when Engine is non-nil")
		}
		mcpHandler := mcp.NewHandler(mcp.Deps{
			Engine:        opts.Engine,
			Audit:         opts.Audit,
			Tools:         opts.Tools,
			Resources:     opts.Resources,
			Logger:        opts.Logger,
			ServerName:    "loamss",
			ServerVersion: opts.Version,
		})
		mux.Handle("/mcp", s.bearerAuthMiddleware(s.attachMCPPrincipal(mcpHandler)))
	}

	// Embedded console — registered LAST so it acts as the catch-all
	// for paths the API mux didn't claim. Go's http.ServeMux uses
	// longest-pattern-wins, so explicit routes like `GET /healthz`
	// and `POST /console/init` still win; "/" only handles what
	// nothing else matched.
	//
	// This is the architectural win: same-origin everything. The
	// wizard fetches /console/init from the same host it was served
	// from; no CORS preflight, no dev-vs-prod URL branching, no
	// version-skew between console and runtime.
	mux.Handle("/", console.Handler())

	s.httpSrv = &http.Server{
		Addr:    opts.Addr,
		Handler: mux,
		// Reasonable defaults to avoid slow-loris-style attacks once the
		// surface is exposed beyond localhost. ReadHeaderTimeout is the
		// minimum a public-facing Go HTTP server should set.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	return s
}

// attachMCPPrincipal copies the authenticated principal/client from
// the server's context keys into the mcp package's context keys so
// downstream MCP handlers (which know nothing about server's keys)
// can read them via mcp.PrincipalFromContext.
func (s *Server) attachMCPPrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := PrincipalFromContext(r.Context())
		c := ClientFromContext(r.Context())
		ctx := mcp.WithPrincipal(r.Context(), p, c)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Addr returns the configured bind address. After Serve has been called
// with a listener whose port was 0 (auto-assigned), use l.Addr() instead.
func (s *Server) Addr() string {
	return s.addr
}

// ListenAndServe binds the configured listen address and serves until
// Shutdown is invoked. Returns nil on clean shutdown; non-nil on bind
// or runtime errors.
func (s *Server) ListenAndServe() error {
	s.logger.Info("server: starting", "addr", s.addr, "version", s.version)
	if err := s.httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen: %w", err)
	}
	s.logger.Info("server: stopped")
	return nil
}

// Serve serves on a pre-bound listener. Useful in tests to claim a free
// port via net.Listen("tcp", "127.0.0.1:0") and pass the listener in.
func (s *Server) Serve(l net.Listener) error {
	s.logger.Info("server: starting", "addr", l.Addr().String(), "version", s.version)
	if err := s.httpSrv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	s.logger.Info("server: stopped")
	return nil
}

// Shutdown gracefully stops the server, waiting for in-flight requests
// to complete (bounded by the context's deadline).
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("server: shutting down")
	if err := s.httpSrv.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// --- basic handlers ----------------------------------------------------

// HealthzResponse is the JSON shape returned by /healthz.
type HealthzResponse struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(HealthzResponse{
		Status:  "ok",
		Version: s.version,
	}); err != nil {
		s.logger.Warn("encoding /healthz response", "err", err)
	}
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := fmt.Fprintln(w, s.version); err != nil {
		s.logger.Warn("writing /version response", "err", err)
	}
}
