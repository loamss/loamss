package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/loamss/loamss/runtime/internal/adapter/model"
)

// model.call is the synchronous text-generation tool. Capsules use it
// to summarize threads, classify entries, generate briefings, draft
// replies — anything that needs an LLM. Gated on the model.call
// canonical capability; the grant carries the cost ceiling and task
// allowlist that scope what the capsule can actually invoke.
//
// v0.1 dispatch: pick the first generation-capable adapter from the
// configured set (mirrors memory.query's embedding-adapter dispatch).
// The full model router (per-task routing, cost ceilings, data-class
// filters) is future work; for now the tool just delegates.
//
// Streaming (model.call_stream) is a separate tool in a later commit;
// most capsule use cases (summarize, classify) tolerate synchronous
// generation, and shipping the streaming variant requires the runtime
// to surface MCP streaming responses which the current handler
// doesn't do yet.
//
// Graceful degradation: when no generation-capable adapter is wired
// (e.g., the user picked "skip" in the model wizard), the tool
// returns an isError result rather than a hard RPC failure — capsules
// can branch on this and fall back to non-LLM logic.

type modelCallArgs struct {
	// Messages is the conversation, OpenAI-style. system / user /
	// assistant roles are supported by all adapters.
	Messages []modelCallMessage `json:"messages"`

	// ModelID optionally pins a specific model. Empty means "first
	// model the adapter advertises with generation capability." This
	// is the v0.1 dispatch — the router takes over later.
	ModelID string `json:"model_id,omitempty"`

	// MaxTokens caps the response. Zero means adapter default.
	MaxTokens int `json:"max_tokens,omitempty"`

	// Temperature: 0.0–2.0. Out-of-range values are clamped by the
	// adapter to the provider's accepted range.
	Temperature float64 `json:"temperature,omitempty"`

	// Stop is an optional list of stop sequences.
	Stop []string `json:"stop,omitempty"`
}

type modelCallMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// modelCallResult is the JSON payload returned in the tool's text
// content block.
type modelCallResult struct {
	Text         string `json:"text"`
	ModelID      string `json:"model_id"`
	FinishReason string `json:"finish_reason,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
}

// NewModelCallTool builds the model.call tool. The generation
// adapter is held via closure capture — same pattern as memory.query.
//
// `generator` is the model adapter selected at startup as the
// generation-capable one (or model:none if nothing matches, in which
// case calls return graceful "no model configured" errors).
func NewModelCallTool(generator model.Adapter) Tool {
	return Tool{
		Name: "model.call",
		Description: "Generate text via the configured model. Capsules use this " +
			"to summarize threads, draft replies, classify entries, or answer " +
			"questions over context they've already assembled from memory. " +
			"Requires the model.call capability; cost ceilings live on the grant.",
		Capability: "model.call",
		InputSchema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "messages": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "properties": {
                            "role":    {"type": "string", "enum": ["system","user","assistant","tool"]},
                            "content": {"type": "string"}
                        },
                        "required": ["role","content"],
                        "additionalProperties": false
                    },
                    "minItems": 1
                },
                "model_id":    {"type": "string"},
                "max_tokens":  {"type": "integer", "minimum": 1, "maximum": 16000},
                "temperature": {"type": "number",  "minimum": 0, "maximum": 2},
                "stop":        {"type": "array", "items": {"type": "string"}}
            },
            "required": ["messages"],
            "additionalProperties": false
        }`),
		Handler: makeModelCallHandler(generator),
	}
}

func makeModelCallHandler(generator model.Adapter) func(context.Context, ToolInput) (ToolResult, error) {
	return func(ctx context.Context, in ToolInput) (ToolResult, error) {
		var args modelCallArgs
		if err := json.Unmarshal(in.Args, &args); err != nil {
			return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
		}
		if len(args.Messages) == 0 {
			return ToolResult{}, fmt.Errorf("invalid arguments: at least one message required")
		}

		// Pin a model id when the caller didn't specify one — pick
		// the first model the adapter advertises that has the
		// "generation" capability. Same pattern as
		// pickEmbeddingAdapter at the adapter-selection level.
		modelID := args.ModelID
		if modelID == "" {
			pinned, err := pickGenerativeModel(ctx, generator)
			if err != nil {
				return gracefulNoModel(err), nil
			}
			modelID = pinned
		}

		req := model.GenerateRequest{
			ModelID:     modelID,
			MaxTokens:   args.MaxTokens,
			Temperature: args.Temperature,
			Stop:        args.Stop,
			// Stamp the principal + grant for audit correlation.
			Metadata: map[string]string{
				"principal_kind": string(in.Principal.Kind),
				"principal_id":   in.Principal.ID,
				"grant_id":       in.GrantID,
				"tool":           "model.call",
			},
		}
		req.Messages = make([]model.Message, len(args.Messages))
		for i, m := range args.Messages {
			req.Messages[i] = model.Message{Role: m.Role, Content: m.Content}
		}

		resp, err := generator.Generate(ctx, req)
		if err != nil {
			if errors.Is(err, model.ErrGenerateNotSupported) ||
				errors.Is(err, model.ErrModelDisabled) {
				return gracefulNoModel(err), nil
			}
			return ToolResult{}, fmt.Errorf("%w: %v", ErrToolBackend, err)
		}

		c, err := JSONContent(modelCallResult{
			Text:         resp.Text,
			ModelID:      resp.ModelID,
			FinishReason: resp.FinishReason,
			InputTokens:  resp.InputTokens,
			OutputTokens: resp.OutputTokens,
		})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Content: []Content{c}}, nil
	}
}

// pickGenerativeModel returns the id of the first model the adapter
// advertises with the "generation" capability. Used when the caller
// didn't pin a model id explicitly.
func pickGenerativeModel(ctx context.Context, a model.Adapter) (string, error) {
	models, err := a.Models(ctx)
	if err != nil {
		return "", fmt.Errorf("listing models: %w", err)
	}
	for _, m := range models {
		for _, cap := range m.Capabilities {
			if cap == "text" {
				return m.ID, nil
			}
		}
	}
	return "", model.ErrModelDisabled
}

// gracefulNoModel turns "no model configured" into a tool result
// with isError=true rather than an RPC failure. Capsules see a clean
// "the user hasn't wired a generative model" signal instead of a
// cryptic backend error.
func gracefulNoModel(reason error) ToolResult {
	return ToolResult{
		Content: []Content{TextContent(
			"No generation-capable model is configured. " +
				"Set up a model under Settings → Models (or via `loamss config`) " +
				"and try again. Details: " + reason.Error(),
		)},
		IsError: true,
	}
}
