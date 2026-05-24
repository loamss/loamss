package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
)

// memory.show retrieves a single memory entry by id from the
// memory adapter. Gated on `memory.read` (canonical capability).
// The id is opaque to the runtime — it's whatever the memory
// adapter returned at Upsert time; capsules / connectors hold
// these ids in their own bookkeeping.
//
// memory.query (semantic search) waits on a model adapter so the
// runtime can embed a text query into a vector. Until then,
// memory.show is the read path: callers must already know the id
// they want.

type memoryShowArgs struct {
	// ID is the memory entry id to fetch. Required.
	ID string `json:"id"`
}

// NewMemoryShowTool builds the tool. The memory adapter is held
// via closure capture — same pattern as audit.read.
func NewMemoryShowTool(m memory.Adapter) Tool {
	return Tool{
		Name: "memory.show",
		Description: "Fetch a single memory entry by id. Requires the memory.read capability. " +
			"Use for `get` workflows where the caller already knows the entry id; semantic search arrives with memory.query.",
		Capability: "memory.read",
		InputSchema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "id": {"type": "string", "minLength": 1}
            },
            "required": ["id"],
            "additionalProperties": false
        }`),
		// memory.read's scope schema covers data_classes_included /
		// _excluded, entities, and time_range. The id-based fetch
		// doesn't naturally project to those scope fields; the
		// adapter returns the entry's metadata which the engine
		// could then post-check, but post-check inside a tool
		// dispatcher conflates allow-with-stripping vs deny. v0.1
		// behavior: if the grant has a non-trivial scope on
		// memory.read, the Check passes against an empty scope only
		// when the schema treats absent fields as "no narrowing".
		// That's exactly how validateScope works in the engine, so
		// memory.show without a scope projection is permissive
		// w.r.t. data_class narrowing — a known limitation worth
		// noting until memory.query lands with proper scope handling.
		Handler: makeMemoryShowHandler(m),
	}
}

func makeMemoryShowHandler(m memory.Adapter) func(context.Context, ToolInput) (ToolResult, error) {
	return func(ctx context.Context, in ToolInput) (ToolResult, error) {
		var args memoryShowArgs
		if err := json.Unmarshal(in.Args, &args); err != nil {
			return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
		}
		if args.ID == "" {
			return ToolResult{}, fmt.Errorf("invalid arguments: id is required")
		}
		entry, err := m.Get(ctx, args.ID)
		if err != nil {
			if errors.Is(err, memory.ErrNotFound) {
				return ToolResult{
					Content: []Content{TextContent("memory entry not found: " + args.ID)},
					IsError: true,
				}, nil
			}
			return ToolResult{}, fmt.Errorf("%w: %v", ErrToolBackend, err)
		}
		// Return id + metadata + vector size; vectors themselves are
		// not meaningful to AI consumers and would be huge.
		payload := map[string]any{
			"id":          entry.ID,
			"metadata":    entry.Metadata,
			"vector_size": len(entry.Vector),
		}
		c, err := JSONContent(payload)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Content: []Content{c}}, nil
	}
}
