package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
)

// audit.read is the read-side tool over the runtime's audit log.
// Gated on the `audit.read` capability (canonical, in the
// permission registry). Returns entries matching a filter the
// caller supplies in the tool's input.
//
// Why this is a tool, not a resource: audit entries don't have
// stable URIs the way memory entities do. The query shape (filter
// + limit) maps naturally to tool args rather than resource paths.

type auditReadArgs struct {
	// Types optionally narrows by audit-entry type ("grant.create",
	// "tool.invoked", ...). Empty = all.
	Types []string `json:"types,omitempty"`

	// ActorKind narrows by actor kind ("user", "client", "capsule").
	ActorKind string `json:"actor_kind,omitempty"`

	// ActorID narrows by actor id.
	ActorID string `json:"actor_id,omitempty"`

	// Since narrows to entries at or after this RFC 3339 timestamp.
	Since string `json:"since,omitempty"`

	// Until narrows to entries strictly before this RFC 3339
	// timestamp.
	Until string `json:"until,omitempty"`

	// Limit caps the result set. Default 100; max 1000 (hard cap
	// enforced server-side so a client can't read the whole log
	// in one call).
	Limit int `json:"limit,omitempty"`

	// Reverse, if true, returns newest-first.
	Reverse bool `json:"reverse,omitempty"`
}

// NewAuditReadTool registers the tool. Audit writer comes from the
// MCP handler's deps; the closure captures it.
func NewAuditReadTool(w audit.Writer) Tool {
	return Tool{
		Name: "audit.read",
		Description: "Read entries from the runtime audit log, filtered by type, actor, time range, or all. " +
			"Requires the audit.read capability. Useful for self-audit workflows where a client reviews its own activity.",
		Capability: "audit.read",
		InputSchema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "types":      {"type": "array", "items": {"type": "string"}},
                "actor_kind": {"type": "string", "enum": ["user","client","capsule","runtime","system"]},
                "actor_id":   {"type": "string"},
                "since":      {"type": "string", "format": "date-time"},
                "until":      {"type": "string", "format": "date-time"},
                "limit":      {"type": "integer", "minimum": 1, "maximum": 1000},
                "reverse":    {"type": "boolean"}
            },
            "additionalProperties": false
        }`),
		// No ScopeOf yet: the audit.read capability ships with an
		// empty scope schema in the canonical registry. When we
		// add scope (e.g., types/data_classes narrowing), this
		// projects args into the scope shape so Check can match.
		Handler: makeAuditReadHandler(w),
	}
}

func makeAuditReadHandler(w audit.Writer) func(context.Context, ToolInput) (ToolResult, error) {
	return func(ctx context.Context, in ToolInput) (ToolResult, error) {
		var args auditReadArgs
		if len(in.Args) > 0 {
			if err := json.Unmarshal(in.Args, &args); err != nil {
				return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
			}
		}
		filter := audit.Filter{
			Types:     args.Types,
			ActorKind: audit.ActorKind(args.ActorKind),
			ActorID:   args.ActorID,
			Limit:     args.Limit,
			Reverse:   args.Reverse,
		}
		if filter.Limit <= 0 || filter.Limit > 1000 {
			filter.Limit = 100
		}
		if args.Since != "" {
			t, err := time.Parse(time.RFC3339, args.Since)
			if err != nil {
				return ToolResult{}, fmt.Errorf("invalid arguments: since must be RFC 3339: %w", err)
			}
			filter.Since = t
		}
		if args.Until != "" {
			t, err := time.Parse(time.RFC3339, args.Until)
			if err != nil {
				return ToolResult{}, fmt.Errorf("invalid arguments: until must be RFC 3339: %w", err)
			}
			filter.Until = t
		}

		entries, err := w.Query(ctx, filter)
		if err != nil {
			return ToolResult{}, fmt.Errorf("%w: %v", ErrToolBackend, err)
		}

		// Return as a JSON Content block with {entries: [...], count}.
		payload := map[string]any{
			"entries": entries,
			"count":   len(entries),
		}
		c, err := JSONContent(payload)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Content: []Content{c}}, nil
	}
}
