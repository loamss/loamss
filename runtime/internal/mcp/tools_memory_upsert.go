package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	memlayer "github.com/loamss/loamss/runtime/internal/memory"
)

// memory.upsert is the runtime tool capsule ingestors call to
// write an entry into memory. Routes through memory.Layer.Upsert
// so the same entity + thread derivation runs whether the writer
// is an in-tree source connector or a capsule ingestor.
//
// Gated on memory.write. Scope is empty for now — narrowing by
// namespace can come later if it's worth the manifest noise.
//
// Embeddings: the caller may pre-compute the vector and pass it
// in. When absent, the layer will fall back to a no-vector entry
// (the metadata + content are still indexed; only semantic search
// won't find it). Capsule ingestors typically leave embeddings
// empty and let a downstream organizer compute them.

type memoryUpsertArgs struct {
	Namespace  string         `json:"namespace"`
	ID         string         `json:"id"`
	Content    string         `json:"content,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Embeddings []float32      `json:"embeddings,omitempty"`
}

type memoryUpsertResult struct {
	OK        bool   `json:"ok"`
	Namespace string `json:"namespace"`
	ID        string `json:"id"`
}

// NewMemoryUpsertTool builds the memory.upsert tool. layer is the
// memory.Layer the runtime already wired for the threads/entities
// tools — same instance, so a capsule writing here is immediately
// visible via threads.list / entities.list / memory.query.
func NewMemoryUpsertTool(layer memlayer.Layer) Tool {
	return Tool{
		Name: "memory.upsert",
		Description: "Write an entry into memory. The runtime's memory layer " +
			"derives entities + threads from the metadata automatically. " +
			"Requires the memory.write capability. " +
			"Typically called by ingestor capsules; external clients with " +
			"memory.write can also use it.",
		Capability: "memory.write",
		InputSchema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "namespace":  {"type": "string", "minLength": 1, "maxLength": 256},
                "id":         {"type": "string", "minLength": 1, "maxLength": 512},
                "content":    {"type": "string"},
                "metadata":   {"type": "object"},
                "embeddings": {"type": "array", "items": {"type": "number"}}
            },
            "required": ["namespace", "id"],
            "additionalProperties": false
        }`),
		Handler: makeMemoryUpsertHandler(layer),
	}
}

func makeMemoryUpsertHandler(
	layer memlayer.Layer,
) func(context.Context, ToolInput) (ToolResult, error) {
	return func(ctx context.Context, in ToolInput) (ToolResult, error) {
		var args memoryUpsertArgs
		if err := json.Unmarshal(in.Args, &args); err != nil {
			return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
		}
		if args.Namespace == "" {
			return ToolResult{}, errors.New("invalid arguments: namespace is required")
		}
		if args.ID == "" {
			return ToolResult{}, errors.New("invalid arguments: id is required")
		}
		err := layer.Upsert(ctx, memlayer.Entry{
			Namespace:  args.Namespace,
			ID:         args.ID,
			Content:    args.Content,
			Metadata:   args.Metadata,
			Embeddings: args.Embeddings,
		})
		if err != nil {
			return ToolResult{}, fmt.Errorf("%w: memory.upsert: %v", ErrToolBackend, err)
		}
		content, err := JSONContent(memoryUpsertResult{
			OK:        true,
			Namespace: args.Namespace,
			ID:        args.ID,
		})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Content: []Content{content}}, nil
	}
}
