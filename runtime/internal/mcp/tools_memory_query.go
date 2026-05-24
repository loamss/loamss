package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
	"github.com/loamss/loamss/runtime/internal/adapter/model"
)

// memory.query is the semantic-search tool: embed a text query via
// the configured model adapter, search the memory adapter for the
// k nearest neighbors, return ranked results. Gated on the
// memory.read capability (same as memory.show and the memory://
// resource provider, so a single grant unlocks all three surfaces).
//
// Graceful degradation: when the user has no model adapter
// configured (model:none, which advertises no models), Embed
// returns ErrModelDisabled. memory.query surfaces that as an
// isError result — the call doesn't fail at the RPC layer, but
// the client learns semantic search is unavailable. This is the
// "users without model access get a usable Loamss" contract from
// adapter-interface.md.

type memoryQueryArgs struct {
	// Query is the natural-language text to embed and search.
	Query string `json:"query"`

	// K is the number of nearest neighbors to return. Default 10;
	// hard cap of 100 (server-enforced so a client can't pull the
	// whole store in one call).
	K int `json:"k,omitempty"`

	// ModelID optionally overrides which embedding model to use.
	// When empty, memory.query picks the first model whose
	// Capabilities include "embeddings". This is the v0.1
	// dispatch — the model router (which will pick per-task) is
	// future work.
	ModelID string `json:"model_id,omitempty"`
}

// memoryQueryHit is one ranked search result. The vector itself is
// deliberately omitted — meaningless to AI consumers and wasteful
// of tokens. Distance follows the adapter's documented metric
// (memory:sqlite returns cosine distance; lower = more similar).
// Callers should treat the absolute value as adapter-specific and
// compare only within a single result set.
type memoryQueryHit struct {
	ID       string         `json:"id"`
	Distance float32        `json:"distance"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// memoryQueryResult is the JSON payload returned in the tool's text
// Content block. Carries the model that was used so callers can
// pin against a stable embedding space.
type memoryQueryResult struct {
	Query   string           `json:"query"`
	ModelID string           `json:"model_id"`
	Count   int              `json:"count"`
	Hits    []memoryQueryHit `json:"hits"`
}

// NewMemoryQueryTool builds the tool. memAdapter holds the vectors;
// modelAdapter does the embedding. Captured via closure so the
// runtime can swap either independently without re-registering
// the tool.
func NewMemoryQueryTool(memAdapter memory.Adapter, modelAdapter model.Adapter) Tool {
	return Tool{
		Name: "memory.query",
		Description: "Semantic search over the user's memory. Embeds the query text via the configured model adapter, " +
			"returns the k nearest entries. Requires the memory.read capability. " +
			"When the user has no model adapter configured, returns isError with an explanation.",
		Capability: "memory.read",
		InputSchema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "query":    {"type": "string", "minLength": 1},
                "k":        {"type": "integer", "minimum": 1, "maximum": 100},
                "model_id": {"type": "string"}
            },
            "required": ["query"],
            "additionalProperties": false
        }`),
		Handler: makeMemoryQueryHandler(memAdapter, modelAdapter),
	}
}

func makeMemoryQueryHandler(mem memory.Adapter, mdl model.Adapter) func(context.Context, ToolInput) (ToolResult, error) {
	return func(ctx context.Context, in ToolInput) (ToolResult, error) {
		var args memoryQueryArgs
		if err := json.Unmarshal(in.Args, &args); err != nil {
			return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
		}
		if args.Query == "" {
			return ToolResult{}, fmt.Errorf("invalid arguments: query is required")
		}
		k := args.K
		if k <= 0 {
			k = 10
		}
		if k > 100 {
			k = 100
		}

		modelID, err := pickEmbeddingModel(ctx, mdl, args.ModelID)
		if err != nil {
			// model:none returns ErrModelDisabled here; surface as
			// isError rather than RPC error so the client sees the
			// semantic answer ("no model configured"), not a
			// dispatch failure.
			if errors.Is(err, model.ErrModelDisabled) || errors.Is(err, errNoEmbeddingModel) {
				return ToolResult{
					Content: []Content{TextContent(
						"semantic search unavailable: no model adapter is configured. " +
							"Configure a model adapter that advertises the \"embeddings\" capability " +
							"(e.g., model:dummy for testing, or a real provider when one is wired).")},
					IsError: true,
				}, nil
			}
			return ToolResult{}, err
		}

		emb, err := mdl.Embed(ctx, model.EmbedRequest{ModelID: modelID, Text: args.Query})
		if err != nil {
			return ToolResult{}, fmt.Errorf("%w: embedding query: %v", ErrToolBackend, err)
		}

		hits, err := mem.Search(ctx, emb.Vector, k, memory.MetadataFilter{})
		if err != nil {
			return ToolResult{}, fmt.Errorf("%w: searching memory: %v", ErrToolBackend, err)
		}

		out := memoryQueryResult{
			Query:   args.Query,
			ModelID: modelID,
			Count:   len(hits),
			Hits:    make([]memoryQueryHit, 0, len(hits)),
		}
		for _, h := range hits {
			out.Hits = append(out.Hits, memoryQueryHit{
				ID:       h.ID,
				Distance: h.Distance,
				Metadata: h.Metadata,
			})
		}
		content, err := JSONContent(out)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Content: []Content{content}}, nil
	}
}

// errNoEmbeddingModel is the sentinel pickEmbeddingModel returns
// when no model adapter advertises the "embeddings" capability.
// Distinguished from ErrModelDisabled because both produce the
// same client-facing isError result but might want different
// diagnostics in the future.
var errNoEmbeddingModel = errors.New("no model in the configured adapter supports embeddings")

// pickEmbeddingModel selects the model id to use for a query. If
// the caller supplied an id, validate it. Otherwise scan Models()
// for the first model with "embeddings" capability. When the
// adapter advertises no embedding model, return errNoEmbeddingModel
// — graceful degradation for model:none.
func pickEmbeddingModel(ctx context.Context, mdl model.Adapter, requested string) (string, error) {
	models, err := mdl.Models(ctx)
	if err != nil {
		return "", fmt.Errorf("listing models: %w", err)
	}
	if requested != "" {
		for _, m := range models {
			if m.ID == requested {
				return m.ID, nil
			}
		}
		return "", fmt.Errorf("%w: %s", model.ErrUnknownModel, requested)
	}
	for _, m := range models {
		for _, cap := range m.Capabilities {
			if cap == "embeddings" {
				return m.ID, nil
			}
		}
	}
	return "", errNoEmbeddingModel
}
