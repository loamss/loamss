package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// Deps bundles the dependencies the MCP handler needs to fulfill
// requests. All fields are required at construction time; nil
// fields produce a panic in NewHandler rather than at request time.
type Deps struct {
	Engine        *permission.Engine
	Audit         audit.Writer
	Tools         *Registry
	Logger        *slog.Logger
	ServerName    string // e.g. "loamss"
	ServerVersion string // build version, surfaced in initialize result
}

// Handler is the MCP HTTP entry point. It implements http.Handler
// for both POST /mcp (JSON-RPC requests) and GET /mcp (SSE stream).
// The auth middleware MUST sit in front; Handler reads the
// authenticated Principal from request context and rejects unauth'd
// calls with -32603 (internal error) — those should never reach
// here in a correctly configured server.
type Handler struct {
	deps Deps
}

// NewHandler constructs a Handler. All Deps fields must be non-nil.
func NewHandler(d Deps) *Handler {
	if d.Engine == nil {
		panic("mcp: NewHandler: Engine is required")
	}
	if d.Audit == nil {
		panic("mcp: NewHandler: Audit is required")
	}
	if d.Tools == nil {
		panic("mcp: NewHandler: Tools registry is required")
	}
	if d.Logger == nil {
		panic("mcp: NewHandler: Logger is required")
	}
	return &Handler{deps: d}
}

// ServeHTTP dispatches by method:
//
//   - POST → JSON-RPC request (one or batch, though batch is deferred)
//   - GET  → SSE stream (server→client events; heartbeat scaffold)
//   - other → 405
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.servePost(w, r)
	case http.MethodGet:
		h.serveSSE(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// servePost handles one JSON-RPC request. Batch requests are
// deferred to a future commit — they're optional in JSON-RPC 2.0
// and adding them now invites edge cases (notifications-in-batch,
// partial failure shapes) that we don't need for v0.1.
func (h *Handler) servePost(w http.ResponseWriter, r *http.Request) {
	// Bound the request body. MCP requests are small JSON; 1 MiB is
	// plenty and bounds runaway uploads.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeJSONRPC(w, parseErrorResponse())
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONRPC(w, parseErrorResponse())
		return
	}
	if req.JSONRPC != jsonRPCVersion {
		writeJSONRPC(w, invalidRequestResponse(req.ID,
			fmt.Sprintf("jsonrpc must be %q", jsonRPCVersion)))
		return
	}
	if req.Method == "" {
		writeJSONRPC(w, invalidRequestResponse(req.ID, "method is required"))
		return
	}

	// Notifications never produce a response per JSON-RPC 2.0.
	if req.IsNotification() {
		h.handleNotification(r, req)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := h.dispatch(r, req)
	writeJSONRPC(w, resp)
}

// handleNotification handles a JSON-RPC notification (no id; no
// response). v0.1 acknowledges the standard `notifications/*`
// methods (cancelled, initialized) by logging at debug and moves on;
// unknown notifications are also debug-logged. Per spec, errors in
// notifications produce no wire response.
func (h *Handler) handleNotification(r *http.Request, req Request) {
	h.deps.Logger.Debug("mcp notification", "method", req.Method)
	_ = r
}

// dispatch routes the request to its method handler. Returns the
// Response to write back; never panics. Method handlers live in
// per-method files (initialize.go, ...).
func (h *Handler) dispatch(r *http.Request, req Request) Response {
	// Programming-error guard: the auth middleware should have put
	// a Principal in context for every request that reaches us.
	// initialize is the one exception in some MCP setups, but in
	// Loamss every MCP request requires authentication because the
	// surface is exposed only to paired clients.
	if PrincipalFromContext(r.Context()) == nil {
		return internalErrorResponse(req.ID, "missing principal")
	}

	switch req.Method {
	case "initialize":
		return h.handleInitialize(r, req)
	case "tools/list":
		return h.handleToolsList(r, req)
	case "tools/call":
		return h.handleToolsCall(r, req)
	// resources/list, resources/read land in commit 3.
	default:
		return methodNotFoundResponse(req.ID, req.Method)
	}
}

// writeJSONRPC encodes resp into the response writer with the
// JSON-RPC content type. Errors writing the body are logged
// elsewhere (the http.ResponseWriter swallows them); we cannot
// recover from a partial-write.
func writeJSONRPC(w http.ResponseWriter, resp Response) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// --- context helpers ---------------------------------------------------

// Context keys are defined in this package — not in `server` — so the
// import graph stays one-directional (server imports mcp; mcp does not
// import server). The server's auth middleware calls WithPrincipal
// just before delegating to a Handler; per-method handlers and tools
// read the principal via PrincipalFromContext.

type ctxKey int

const (
	ctxKeyPrincipal ctxKey = iota
	ctxKeyClient
)

// WithPrincipal attaches the authenticated principal + client to ctx.
// Returns a new context; the caller propagates it (e.g.,
// r.WithContext(ctx) for an http.Request).
func WithPrincipal(ctx context.Context, p *permission.Principal, c *permission.Client) context.Context {
	ctx = context.WithValue(ctx, ctxKeyPrincipal, p)
	ctx = context.WithValue(ctx, ctxKeyClient, c)
	return ctx
}

// PrincipalFromContext returns the authenticated Principal or nil
// when the context did not pass through WithPrincipal.
func PrincipalFromContext(ctx context.Context) *permission.Principal {
	v, _ := ctx.Value(ctxKeyPrincipal).(*permission.Principal)
	return v
}

// ClientFromContext returns the authenticated Client or nil.
func ClientFromContext(ctx context.Context) *permission.Client {
	v, _ := ctx.Value(ctxKeyClient).(*permission.Client)
	return v
}
