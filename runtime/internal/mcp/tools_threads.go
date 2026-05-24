package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/loamss/loamss/runtime/internal/memory"
)

// threads.* MCP tools surface the conversational-thread groupings
// the memory layer derives from source-supplied metadata (Gmail
// thread_id today; Slack threads, calendar event series later).
// Gated on `memory.read` — threads are a derived projection of
// permissioned memory entries.

// --- threads.list -----------------------------------------------------

type threadsListArgs struct {
	Namespace string `json:"namespace,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// NewThreadsListTool builds the threads.list tool.
func NewThreadsListTool(layer memory.Layer) Tool {
	return Tool{
		Name: "threads.list",
		Description: "List conversation threads the memory layer has " +
			"derived. Newest-activity first. Filterable by namespace. " +
			"Requires memory.read.",
		Capability: "memory.read",
		InputSchema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "namespace": {"type": "string"},
                "limit":     {"type": "integer", "minimum": 1, "maximum": 1000}
            },
            "additionalProperties": false
        }`),
		Handler: func(ctx context.Context, in ToolInput) (ToolResult, error) {
			var a threadsListArgs
			if len(in.Args) > 0 {
				if err := json.Unmarshal(in.Args, &a); err != nil {
					return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
				}
			}
			ts, err := layer.ListThreads(ctx, memory.ThreadFilter{
				Namespace: a.Namespace,
				Limit:     a.Limit,
			})
			if err != nil {
				return ToolResult{}, fmt.Errorf("%w: %v", ErrToolBackend, err)
			}
			c, err := JSONContent(map[string]any{
				"threads": ts,
				"count":   len(ts),
			})
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Content: []Content{c}}, nil
		},
	}
}

// --- threads.show -----------------------------------------------------

type threadsShowArgs struct {
	ID string `json:"id"`
}

// NewThreadsShowTool builds the threads.show tool.
func NewThreadsShowTool(layer memory.Layer) Tool {
	return Tool{
		Name: "threads.show",
		Description: "Fetch a single thread by id (e.g., \"thr_01H...\"). " +
			"Returns subject, time range, namespace + external id (Gmail " +
			"thread_id), and entry count. Requires memory.read.",
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
			var a threadsShowArgs
			if err := json.Unmarshal(in.Args, &a); err != nil {
				return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
			}
			t, err := layer.GetThread(ctx, a.ID)
			if err != nil {
				if errors.Is(err, memory.ErrThreadNotFound) {
					return ToolResult{
						Content: []Content{TextContent("thread not found: " + a.ID)},
						IsError: true,
					}, nil
				}
				return ToolResult{}, fmt.Errorf("%w: %v", ErrToolBackend, err)
			}
			c, err := JSONContent(t)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Content: []Content{c}}, nil
		},
	}
}

// --- threads.entries --------------------------------------------------

type threadsEntriesArgs struct {
	ID    string `json:"id"`
	Limit int    `json:"limit,omitempty"`
}

// NewThreadsEntriesTool builds the threads.entries tool, which
// returns entry references for a thread in reading order (oldest-
// first — natural for a conversation).
func NewThreadsEntriesTool(layer memory.Layer) Tool {
	return Tool{
		Name: "threads.entries",
		Description: "List memory entries belonging to a given thread in " +
			"reading order (oldest-first). Use memory.show on the " +
			"returned ids to fetch entry content. Requires memory.read.",
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
			var a threadsEntriesArgs
			if err := json.Unmarshal(in.Args, &a); err != nil {
				return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
			}
			if a.ID == "" {
				return ToolResult{}, fmt.Errorf("invalid arguments: id is required")
			}
			refs, err := layer.EntriesByThread(ctx, a.ID, a.Limit)
			if err != nil {
				return ToolResult{}, fmt.Errorf("%w: %v", ErrToolBackend, err)
			}
			c, err := JSONContent(map[string]any{
				"thread_id": a.ID,
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
