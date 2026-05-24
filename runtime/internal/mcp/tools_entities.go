package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/loamss/loamss/runtime/internal/memory"
)

// entities.* MCP tools expose the memory layer's entity views to
// paired clients and capsules. All three are gated on `memory.read`
// — entities are a derived projection of memory entries the caller
// has already permissioned, not a new data class.
//
// Tools registered here:
//
//   entities.list      list entities, optionally filtered
//   entities.show      fetch a single entity by id
//   entities.entries   list entry refs involving an entity

// --- entities.list ----------------------------------------------------

type entitiesListArgs struct {
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Alias     string `json:"alias,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// NewEntitiesListTool builds the entities.list tool.
func NewEntitiesListTool(layer memory.Layer) Tool {
	return Tool{
		Name: "entities.list",
		Description: "List entities the memory layer has derived (people, " +
			"organizations). Filterable by namespace, kind, or alias " +
			"(email address / name). Requires memory.read.",
		Capability: "memory.read",
		InputSchema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "namespace": {"type": "string"},
                "kind":      {"type": "string", "enum": ["person", "organization"]},
                "alias":     {"type": "string"},
                "limit":     {"type": "integer", "minimum": 1, "maximum": 1000}
            },
            "additionalProperties": false
        }`),
		Handler: func(ctx context.Context, in ToolInput) (ToolResult, error) {
			var a entitiesListArgs
			if len(in.Args) > 0 {
				if err := json.Unmarshal(in.Args, &a); err != nil {
					return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
				}
			}
			ents, err := layer.ListEntities(ctx, memory.EntityFilter{
				Namespace: a.Namespace,
				Kind:      memory.EntityKind(a.Kind),
				Alias:     a.Alias,
				Limit:     a.Limit,
			})
			if err != nil {
				return ToolResult{}, fmt.Errorf("%w: %v", ErrToolBackend, err)
			}
			c, err := JSONContent(map[string]any{
				"entities": ents,
				"count":    len(ents),
			})
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Content: []Content{c}}, nil
		},
	}
}

// --- entities.show ----------------------------------------------------

type entitiesShowArgs struct {
	ID string `json:"id"`
}

// NewEntitiesShowTool builds the entities.show tool.
func NewEntitiesShowTool(layer memory.Layer) Tool {
	return Tool{
		Name: "entities.show",
		Description: "Fetch a single entity by id (e.g., \"ent_01H...\"). " +
			"Returns the canonical name, kind, aliases, time range, and " +
			"entry count. Requires memory.read.",
		Capability: "memory.read",
		InputSchema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "id": {"type": "string", "minLength": 1}
            },
            "required": ["id"],
            "additionalProperties": false
        }`),
		Handler: func(ctx context.Context, in ToolInput) (ToolResult, error) {
			var a entitiesShowArgs
			if err := json.Unmarshal(in.Args, &a); err != nil {
				return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
			}
			e, err := layer.GetEntity(ctx, a.ID)
			if err != nil {
				if errors.Is(err, memory.ErrEntityNotFound) {
					return ToolResult{
						Content: []Content{TextContent("entity not found: " + a.ID)},
						IsError: true,
					}, nil
				}
				return ToolResult{}, fmt.Errorf("%w: %v", ErrToolBackend, err)
			}
			c, err := JSONContent(e)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Content: []Content{c}}, nil
		},
	}
}

// --- entities.entries -------------------------------------------------

type entitiesEntriesArgs struct {
	ID    string `json:"id"`
	Limit int    `json:"limit,omitempty"`
}

// NewEntitiesEntriesTool builds the entities.entries tool, which
// returns the memory-entry references that involve a given entity
// (from / to / cc / bcc). Newest-first. Use entities.show first to
// find the id; pass that id here to drill into entries.
func NewEntitiesEntriesTool(layer memory.Layer) Tool {
	return Tool{
		Name: "entities.entries",
		Description: "List memory entries involving a given entity, " +
			"newest-first. Each ref carries the role the entity played " +
			"(from / to / cc / bcc) and a date when known. Use memory.show " +
			"on the returned ids to fetch entry content. Requires memory.read.",
		Capability: "memory.read",
		InputSchema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "id":    {"type": "string", "minLength": 1},
                "limit": {"type": "integer", "minimum": 1, "maximum": 1000}
            },
            "required": ["id"],
            "additionalProperties": false
        }`),
		Handler: func(ctx context.Context, in ToolInput) (ToolResult, error) {
			var a entitiesEntriesArgs
			if err := json.Unmarshal(in.Args, &a); err != nil {
				return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
			}
			if a.ID == "" {
				return ToolResult{}, fmt.Errorf("invalid arguments: id is required")
			}
			refs, err := layer.EntriesByEntity(ctx, a.ID, a.Limit)
			if err != nil {
				return ToolResult{}, fmt.Errorf("%w: %v", ErrToolBackend, err)
			}
			c, err := JSONContent(map[string]any{
				"entity_id": a.ID,
				"entries":   refs,
				"count":     len(refs),
			})
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Content: []Content{c}}, nil
		},
	}
}
