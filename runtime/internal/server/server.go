// Package server hosts the runtime's HTTP surface. In v0.1 it serves a
// minimal /healthz endpoint; the MCP surface, console API, and other
// Phase 1 components mount onto the same mux as they land.
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
)

// Options configures a new Server.
type Options struct {
	// Addr is the host:port to bind to (e.g. "127.0.0.1:7777"). Required.
	Addr string

	// Logger is used for server-level events (start, shutdown, request errors).
	// Required.
	Logger *slog.Logger

	// Version is the runtime's build version, reported in /healthz responses
	// so external probes can verify which build is running.
	Version string
}

// Server wraps the underlying http.Server with a stable API surface and
// keeps internal concerns (mux setup, shutdown coordination) encapsulated.
type Server struct {
	httpSrv *http.Server
	logger  *slog.Logger
	addr    string
	version string
}

// New constructs a Server. The HTTP listener is not bound until
// ListenAndServe or Serve is called.
func New(opts Options) *Server {
	s := &Server{
		addr:    opts.Addr,
		logger:  opts.Logger,
		version: opts.Version,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)

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

// --- handlers -----------------------------------------------------------

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
