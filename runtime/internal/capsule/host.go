package capsule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/mcp"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// Host supervises every installed capsule's subprocess: starts one
// Client per Installed record, mounts each capsule's advertised
// tools into the runtime's mcp.Registry, and tears everything down
// cleanly on Stop.
//
// One Host per runtime daemon. Constructed at start.go time, after
// the permission store + audit writer + adapters are ready and
// before the HTTP listener begins serving (so external clients see
// capsule tools in tools/list from the first request).
//
// Capsule callbacks (capsule → runtime memory.query / files.read /
// model.call) flow through a RuntimeHandler the Host installs on
// every Client. The handler currently rejects all inbound capsule
// calls with -32601; the full implementation (permission.Check
// against the capsule's grants + dispatch through the runtime's
// existing tool registry + audit) lands in a follow-up commit.
type Host struct {
	store  *Store
	engine *permission.Engine
	audit  audit.Writer
	tools  *mcp.Registry
	logger *slog.Logger

	mu      sync.Mutex
	clients map[string]*Client
}

// NewHost constructs a Host. tools is the mcp.Registry where
// capsule tools will be mounted — same registry the runtime
// already uses for client.info / memory.show / etc.
func NewHost(store *Store, engine *permission.Engine, w audit.Writer, tools *mcp.Registry, logger *slog.Logger) *Host {
	if logger == nil {
		logger = slog.Default()
	}
	return &Host{
		store:   store,
		engine:  engine,
		audit:   w,
		tools:   tools,
		logger:  logger,
		clients: make(map[string]*Client),
	}
}

// Start spawns one Client per persisted capsule. Each Client runs
// the initialize + tools/list handshake, then the Host registers
// the capsule's advertised tools into the mcp.Registry so external
// MCP clients see them.
//
// If any capsule fails to start, Start logs the error and continues
// with the remaining capsules — one broken capsule shouldn't block
// the rest. The supervisor will surface persistent failures via the
// `loamss capsule list` status field once that lands.
//
// Returns the count of successfully-started capsules.
func (h *Host) Start(ctx context.Context) (int, error) {
	caps, err := h.store.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("capsule host: listing capsules: %w", err)
	}
	started := 0
	for i := range caps {
		c := caps[i]
		if err := h.StartOne(ctx, c); err != nil {
			h.logger.Warn("capsule host: failed to start capsule",
				"name", c.Name, "version", c.Version, "err", err)
			continue
		}
		started++
	}
	h.logger.Info("capsule host: started", "started", started, "total", len(caps))
	return started, nil
}

// StartOne brings one capsule online: spawn → handshake → register
// tools. Idempotent — starting a capsule that's already in the
// clients map returns nil. Used by Start for batch startup and by
// (future) `loamss capsule install` to bring a freshly-installed
// capsule live without a daemon restart.
func (h *Host) StartOne(ctx context.Context, c Installed) error {
	h.mu.Lock()
	if _, exists := h.clients[c.Name]; exists {
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()

	client := NewClient(c, h.logger, h.runtimeHandler(c.Name))
	if err := client.Start(ctx); err != nil {
		return err
	}

	h.mu.Lock()
	h.clients[c.Name] = client
	h.mu.Unlock()

	// Mount the capsule's advertised tools into the runtime
	// registry. Each tool's Handler delegates to Client.CallTool;
	// the capsule's principal isn't checked here because the
	// capsule's tools are surfaces, not capabilities — the
	// permission gate lives on the runtime methods the CAPSULE
	// calls (via the RuntimeHandler).
	for _, t := range client.Tools() {
		tool := h.mountTool(c.Name, t, client)
		if err := h.tools.Register(tool); err != nil {
			h.logger.Warn("capsule host: tool registration failed",
				"capsule", c.Name, "tool", t.Name, "err", err)
		}
	}
	return nil
}

// mountTool turns a capsule's tool advertisement into an mcp.Tool
// the runtime's registry can dispatch. The tool name is namespaced
// with the capsule's name to avoid collisions across capsules:
// "<capsule>.<tool>" (e.g., "email-drafter.draft_reply").
//
// The Capability is empty (auth-only at the dispatcher level). This
// is the v0.1 contract: external clients can call any capsule tool
// once authenticated; the capsule's own grants gate what the
// capsule can do internally. Future versions may add per-tool
// capability gates if the model proves too permissive in practice.
func (h *Host) mountTool(capsuleName string, t ToolAdvertisement, client *Client) mcp.Tool {
	qualifiedName := capsuleName + "." + t.Name
	return mcp.Tool{
		Name:        qualifiedName,
		Description: t.Description,
		Capability:  "", // auth-only; see comment above
		InputSchema: t.InputSchema,
		Handler: func(ctx context.Context, in mcp.ToolInput) (mcp.ToolResult, error) {
			// Forward to the capsule. The args from the external
			// caller pass through verbatim.
			resp, err := client.CallTool(ctx, t.Name, in.Args)
			if err != nil {
				return mcp.ToolResult{}, fmt.Errorf("capsule %s: %w", capsuleName, err)
			}
			if resp.Error != nil {
				return mcp.ToolResult{}, errors.New(resp.Error.Message)
			}
			// The capsule's tool result is already MCP-shaped
			// ({content: [...]}). Decode and pass through.
			rb, err := json.Marshal(resp.Result)
			if err != nil {
				return mcp.ToolResult{}, fmt.Errorf("re-encoding capsule result: %w", err)
			}
			var result mcp.ToolResult
			if err := json.Unmarshal(rb, &result); err != nil {
				// Capsule returned a non-MCP-shaped value; wrap it
				// as a single text content block.
				return mcp.ToolResult{
					Content: []mcp.Content{{Type: "text", Text: string(rb)}},
				}, nil
			}
			return result, nil
		},
	}
}

// runtimeHandler returns the RuntimeHandler the Client will invoke
// when the capsule sends an inbound request (capsule → runtime
// callback path). v0.1 stub: every inbound call returns
// method-not-found. The follow-up commit fills this in with the
// real dispatch chain — permission.Check against the capsule's
// grants → execute through the runtime's adapter layer → audit.
//
// The capsule name parameter is captured for the future
// principal-aware dispatch.
func (h *Host) runtimeHandler(capsuleName string) RuntimeHandler {
	_ = capsuleName
	return func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		// TODO(capsule callbacks): map method to a runtime tool,
		// check the capsule's grants, dispatch, emit audit. For
		// now, capsules cannot call back into the runtime.
		return nil, &mcp.RPCError{
			Code:    -32601,
			Message: "method not yet supported by the runtime: " + method,
		}
	}
}

// Stop gracefully shuts down every Client. Returns the first error
// encountered; remaining Clients are still stopped. Bounded by ctx;
// if the deadline elapses, in-flight Clients escalate to SIGKILL.
//
// Idempotent — Stop on an already-stopped Host is a no-op (the
// clients map is emptied on first Stop).
func (h *Host) Stop(ctx context.Context) error {
	h.mu.Lock()
	clients := h.clients
	h.clients = make(map[string]*Client)
	h.mu.Unlock()

	var firstErr error
	for name, client := range clients {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := client.Stop(stopCtx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("stopping %s: %w", name, err)
		}
		cancel()
	}
	return firstErr
}

// Client returns the running Client for a capsule by name, or nil.
// Exposed so the supervisor + future `loamss capsule status` can
// query per-capsule state.
func (h *Host) Client(name string) *Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clients[name]
}

// Running returns the names of all currently-running capsules.
func (h *Host) Running() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.clients))
	for name := range h.clients {
		out = append(out, name)
	}
	return out
}
