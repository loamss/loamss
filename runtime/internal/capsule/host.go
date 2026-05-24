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

	// capsuleTools tracks which mcp.Registry entries came from a
	// capsule (vs runtime-provided). Used by the RuntimeHandler to
	// reject cross-capsule callbacks — capsule A asking the runtime
	// to call capsule B's tool is explicitly deferred to v0.2 per
	// capsule-spec.md §Open questions. The map key is the tool
	// name (the namespaced form, e.g. "drafter.draft_reply"); the
	// value is the capsule name that owns it (kept for diagnostics).
	capsuleToolsMu sync.RWMutex
	capsuleTools   map[string]string
}

// NewHost constructs a Host. tools is the mcp.Registry where
// capsule tools will be mounted — same registry the runtime
// already uses for client.info / memory.show / etc.
func NewHost(store *Store, engine *permission.Engine, w audit.Writer, tools *mcp.Registry, logger *slog.Logger) *Host {
	if logger == nil {
		logger = slog.Default()
	}
	return &Host{
		store:        store,
		engine:       engine,
		audit:        w,
		tools:        tools,
		logger:       logger,
		clients:      make(map[string]*Client),
		capsuleTools: make(map[string]string),
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
			continue
		}
		h.capsuleToolsMu.Lock()
		h.capsuleTools[tool.Name] = c.Name
		h.capsuleToolsMu.Unlock()
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

// runtimeHandler returns the RuntimeHandler the Client invokes
// when the capsule sends an inbound request (capsule → runtime
// callback path). The handler:
//
//  1. Accepts `tools/call` as the one supported method. Capsules
//     speak MCP; calling back into the runtime is just another
//     tools/call on the runtime's side of the connection. Any
//     other method returns -32601.
//
//  2. Resolves the requested tool against the runtime's mcp.Registry.
//     Rejects cross-capsule calls (capsule A asking the runtime to
//     call capsule B's tool) with -32601 — explicitly deferred per
//     capsule-spec.md §Open questions.
//
//  3. Runs permission.Engine.Check with the capsule as the principal.
//     The capsule's grants (issued at install time per its manifest's
//     `permissions:` section) are the ones consulted. Deny → -32001;
//     approval required → -32002 with approval_id; allow → proceed.
//
//  4. Invokes the runtime tool's Handler. The result + audit emission
//     mirror the external-client tools/call path in mcp/tools.go;
//     audit entries use the capsule actor kind so consumers can
//     filter by who.
//
// The shape mirrors mcp.Handler.handleToolsCall but operates against
// the capsule principal and bypasses the HTTP request envelope.
// Sharing more code would require an additional refactor of mcp's
// dispatch path; keeping the two paths separate is OK for now —
// they're each ~50 lines and divergence is unlikely.
func (h *Host) runtimeHandler(capsuleName string) RuntimeHandler {
	principal := permission.Principal{Kind: permission.PrincipalCapsule, ID: capsuleName}
	return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method != "tools/call" {
			return nil, &mcp.RPCError{
				Code:    -32601,
				Message: "capsule callback: only tools/call is supported, got " + method,
			}
		}
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &mcp.RPCError{
				Code:    -32602,
				Message: "capsule callback: decoding params: " + err.Error(),
			}
		}
		if p.Name == "" {
			return nil, &mcp.RPCError{Code: -32602, Message: "tool name is required"}
		}

		// Reject cross-capsule callbacks.
		h.capsuleToolsMu.RLock()
		owner, isCapsuleTool := h.capsuleTools[p.Name]
		h.capsuleToolsMu.RUnlock()
		if isCapsuleTool {
			h.logger.Info("capsule callback: cross-capsule call rejected",
				"caller", capsuleName, "tool", p.Name, "owner", owner)
			return nil, &mcp.RPCError{
				Code:    -32601,
				Message: "cross-capsule callbacks are not yet supported (capsule " + capsuleName + " tried to call " + p.Name + ", owned by " + owner + ")",
			}
		}

		tool, ok := h.tools.Get(p.Name)
		if !ok {
			return nil, &mcp.RPCError{
				Code:    -32003,
				Message: "unknown tool: " + p.Name,
			}
		}

		// Permission check (skip when the tool has no Capability,
		// matching the external-client semantics — client.info is
		// such a tool).
		var grantID string
		if tool.Capability != "" {
			scope, err := h.scopeFor(tool, p.Arguments)
			if err != nil {
				return nil, &mcp.RPCError{
					Code:    -32602,
					Message: "scope projection failed: " + err.Error(),
				}
			}
			decision, err := h.engine.Check(ctx, permission.CheckRequest{
				Principal:      principal,
				Capability:     tool.Capability,
				AttemptedScope: scope,
				Rationale:      "capsule " + capsuleName + " → " + p.Name,
			})
			if err != nil {
				return nil, &mcp.RPCError{Code: -32603, Message: "permission check failed: " + err.Error()}
			}
			switch decision.Decision {
			case permission.DecisionDeny:
				return nil, &mcp.RPCError{
					Code:    -32001,
					Message: "permission denied",
					Data: map[string]any{
						"capability": tool.Capability,
						"reason":     decision.Reason,
						"capsule":    capsuleName,
					},
				}
			case permission.DecisionApprovalRequired:
				return nil, &mcp.RPCError{
					Code:    -32002,
					Message: "user approval required",
					Data: map[string]any{
						"capability":  tool.Capability,
						"approval_id": decision.ApprovalID,
						"capsule":     capsuleName,
					},
				}
			case permission.DecisionAllow:
				grantID = decision.GrantID
			}
		}

		// Execute. The runtime tool's Handler returns mcp.ToolResult;
		// we marshal it as the MCP tools/call result shape.
		in := mcp.ToolInput{
			Args:      p.Arguments,
			Principal: principal,
			GrantID:   grantID,
		}
		res, err := tool.Handler(ctx, in)
		if err != nil {
			h.recordToolFailed(ctx, principal, tool.Name, err)
			return nil, &mcp.RPCError{
				Code:    -32099,
				Message: "tool execution failed: " + err.Error(),
				Data:    map[string]any{"tool": tool.Name},
			}
		}
		h.recordToolInvoked(ctx, principal, tool.Name, tool.Capability, grantID)

		// Shape into the MCP tools/call result envelope.
		return map[string]any{
			"content": res.Content,
			"isError": res.IsError,
		}, nil
	}
}

// scopeFor projects the tool call's arguments through the tool's
// ScopeOf function to produce the attempted-scope map the engine's
// Check uses. Tools without a ScopeOf return nil scope (matches an
// unrestricted grant). Mirrors the helper in mcp/tools.go but
// duplicates the small body to avoid exporting an mcp internal.
func (h *Host) scopeFor(t mcp.Tool, args json.RawMessage) (map[string]any, error) {
	if t.ScopeOf == nil {
		return nil, nil
	}
	return t.ScopeOf(args)
}

// recordToolInvoked emits the tool.invoked audit entry for a
// capsule-initiated call. Distinct from the external-client path
// only in the actor kind — that lets audit consumers filter by who.
func (h *Host) recordToolInvoked(ctx context.Context, p permission.Principal, toolName, capability, grantID string) {
	entry := audit.Entry{
		Type:    "tool.invoked",
		Actor:   audit.Actor{Kind: audit.ActorCapsule, ID: p.ID},
		Subject: &audit.Subject{Kind: audit.SubjectTool, ID: toolName},
		Outcome: audit.OutcomeSuccess,
		Data: map[string]any{
			"tool":       toolName,
			"capability": capability,
			"via":        "capsule_callback",
		},
	}
	if grantID != "" {
		entry.Data["grant_id"] = grantID
	}
	_, _ = h.audit.Append(ctx, entry)
}

func (h *Host) recordToolFailed(ctx context.Context, p permission.Principal, toolName string, err error) {
	entry := audit.Entry{
		Type:    "tool.failed",
		Actor:   audit.Actor{Kind: audit.ActorCapsule, ID: p.ID},
		Subject: &audit.Subject{Kind: audit.SubjectTool, ID: toolName},
		Outcome: audit.OutcomeError,
		Data: map[string]any{
			"tool":  toolName,
			"error": err.Error(),
			"via":   "capsule_callback",
		},
	}
	_, _ = h.audit.Append(ctx, entry)
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
