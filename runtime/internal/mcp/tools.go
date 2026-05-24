package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// tools/list and tools/call live here. The dispatcher in handler.go
// routes to handleToolsList / handleToolsCall; the bodies enforce the
// MCP semantics defined in mcp-surface.md §Tools.

// --- tools/list --------------------------------------------------------

// listedTool is the wire shape returned in tools/list. Mirrors MCP's
// expected schema: name, description, inputSchema.
type listedTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type listToolsResult struct {
	Tools []listedTool `json:"tools"`
}

// handleToolsList returns the registered tool set. No filtering by
// the caller's grants — the MCP spec is "advertise everything; let
// the client try and get authoritative deny from tools/call." That
// matches the upstream MCP model and avoids leaking grant state into
// the listing surface (which would be a side channel for callers to
// probe what capabilities they hold).
func (h *Handler) handleToolsList(_ *http.Request, req Request) Response {
	regs := h.deps.Tools.List()
	out := make([]listedTool, 0, len(regs))
	for _, t := range regs {
		schema := t.InputSchema
		if len(schema) == 0 {
			// MCP requires inputSchema to be a JSON Schema object;
			// emit a permissive empty schema if the tool didn't
			// provide one.
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, listedTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return successResponse(req.ID, listToolsResult{Tools: out})
}

// --- tools/call --------------------------------------------------------

// callToolParams is the params for tools/call: name + arguments.
// arguments is forwarded to the tool's Handler as raw JSON; the tool
// decodes into its own typed shape.
type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// callToolResult is the standard MCP shape for a tool result. Loamss
// tools return ToolResult, which we marshal directly into this
// envelope.
type callToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// handleToolsCall is the tool dispatcher. The flow:
//
//  1. Decode params; reject if name is missing.
//  2. Look up the tool; return -32003 (unknown tool) on miss.
//  3. Run permission.Check against the tool's declared capability
//     (unless the tool has no capability — e.g., client.info).
//     - Deny → -32001 with the engine's reason.
//     - ApprovalRequired → -32002 with the approval id; client polls.
//     - Allow → proceed.
//  4. Call the tool's Handler. Errors become -32099 (backend error).
//  5. Emit a tool.invoked audit entry; serialize the result into the
//     MCP envelope.
//
// The permission check and audit emission both go through
// permission.Engine (Check emits its own check.allow/check.deny
// entries); the tool.invoked entry sits on top to record the
// invocation outcome separately from the check decision. That lets
// audit consumers filter by "what tools were called?" without
// joining against check entries.
func (h *Handler) handleToolsCall(r *http.Request, req Request) Response {
	var params callToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return invalidParamsResponse(req.ID, "cannot decode tools/call params: "+err.Error())
	}
	if params.Name == "" {
		return invalidParamsResponse(req.ID, "tool name is required")
	}

	tool, ok := h.deps.Tools.Get(params.Name)
	if !ok {
		return errorResponse(req.ID, codeUnknownTool, "unknown tool: "+params.Name, nil)
	}

	principal := PrincipalFromContext(r.Context())
	client := ClientFromContext(r.Context())
	if principal == nil || client == nil {
		// auth middleware should have populated both — defensive.
		return internalErrorResponse(req.ID, "missing principal in tool dispatch")
	}

	// Permission check, unless the tool is auth-only (Capability == "").
	var grantID string
	if tool.Capability != "" {
		scope, err := scopeFor(tool, params.Arguments)
		if err != nil {
			return invalidParamsResponse(req.ID, "scope projection failed: "+err.Error())
		}
		decision, err := h.deps.Engine.Check(r.Context(), permission.CheckRequest{
			Principal:      *principal,
			Capability:     tool.Capability,
			AttemptedScope: scope,
			Rationale:      "MCP tools/call " + params.Name,
		})
		if err != nil {
			return internalErrorResponse(req.ID, "permission check failed: "+err.Error())
		}
		switch decision.Decision {
		case permission.DecisionDeny:
			return errorResponse(req.ID, codePermissionDenied, "permission denied", map[string]any{
				"capability": tool.Capability,
				"reason":     decision.Reason,
			})
		case permission.DecisionApprovalRequired:
			return errorResponse(req.ID, codeApprovalRequired, "user approval required", map[string]any{
				"capability":  tool.Capability,
				"approval_id": decision.ApprovalID,
				"poll_via":    "loamss approve list / show / grant / deny",
			})
		case permission.DecisionAllow:
			grantID = decision.GrantID
		}
	}

	in := ToolInput{
		Args:      params.Arguments,
		Principal: *principal,
		Client:    client,
		GrantID:   grantID,
	}
	res, err := tool.Handler(r.Context(), in)
	if err != nil {
		h.recordToolFailed(r.Context(), principal, tool, err)
		return errorResponse(req.ID, codeBackendError, "tool execution failed", map[string]any{
			"tool":  tool.Name,
			"error": err.Error(),
		})
	}

	h.recordToolInvoked(r.Context(), principal, tool, grantID)
	return successResponse(req.ID, callToolResult(res))
}

// scopeFor calls the tool's ScopeOf if present; otherwise returns a
// nil scope (which matches any grant whose scope is also empty).
func scopeFor(t Tool, args json.RawMessage) (map[string]any, error) {
	if t.ScopeOf == nil {
		return nil, nil
	}
	return t.ScopeOf(args)
}

// --- audit emission for tool invocations -------------------------------

func (h *Handler) recordToolInvoked(ctx context.Context, p *permission.Principal, t Tool, grantID string) {
	entry := audit.Entry{
		Type:    "tool.invoked",
		Actor:   audit.Actor{Kind: audit.ActorClient, ID: p.ID},
		Subject: &audit.Subject{Kind: audit.SubjectTool, ID: t.Name},
		Outcome: audit.OutcomeSuccess,
		Data: map[string]any{
			"tool":       t.Name,
			"capability": t.Capability,
		},
	}
	if grantID != "" {
		entry.Data["grant_id"] = grantID
	}
	_, _ = h.deps.Audit.Append(ctx, entry)
}

func (h *Handler) recordToolFailed(ctx context.Context, p *permission.Principal, t Tool, err error) {
	entry := audit.Entry{
		Type:    "tool.failed",
		Actor:   audit.Actor{Kind: audit.ActorClient, ID: p.ID},
		Subject: &audit.Subject{Kind: audit.SubjectTool, ID: t.Name},
		Outcome: audit.OutcomeError,
		Data: map[string]any{
			"tool":  t.Name,
			"error": err.Error(),
		},
	}
	_, _ = h.deps.Audit.Append(ctx, entry)
}

// --- error helpers -----------------------------------------------------

// errInvalidArgs wraps a tool-side argument decode error in an
// errorResponse-friendly shape. Tools call this from their Handler
// when their internal type unmarshal fails.
//
//nolint:unused // tools use this in commit 2 follow-ups
func errInvalidArgs(format string, a ...any) error {
	return fmt.Errorf("invalid arguments: "+format, a...)
}

// ErrToolBackend wraps a tool's downstream error. Tools that need to
// distinguish "input was bad" from "I couldn't talk to the backend"
// return this. The dispatcher maps both to -32099 today; future
// granularity (separate code for backend) lives here.
var ErrToolBackend = errors.New("mcp: tool backend error")
