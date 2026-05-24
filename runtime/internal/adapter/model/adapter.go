// Package model defines the SPI for Loamss model adapters — the
// pluggable layer between the runtime and any provider that can
// embed text or generate completions (Anthropic, OpenAI, local
// Ollama, etc.).
//
// Adapters are pure executors: they do not enforce data-class
// rules, cost ceilings, or task routing. Those decisions live in
// the runtime's model router (future work), which selects an
// adapter + model id and hands the adapter a fully-resolved
// request. See adapter-interface.md §Model adapter for the full
// trust and responsibility split.
//
// Each concrete adapter lives in its own sub-package and registers
// a factory in init(). The runtime resolves the configured adapter
// id at startup, constructs the adapter, then calls Init with the
// user-supplied config map. See `internal/adapter/model/none` and
// `internal/adapter/model/dummy` for the two reference
// implementations.
package model

import (
	"context"
	"errors"
)

// Adapter is the contract every model adapter must satisfy.
//
// Adapters are semi-trusted: they run in the runtime's process for
// performance, with byte-level access to provider APIs. The runtime
// ships a vetted set; third-party adapters are clearly distinguished
// and may move out-of-process in a future version. See
// adapter-interface.md §Trust level.
//
// All methods take a context the runtime uses to bound work. Methods
// are safe for concurrent use; the runtime serializes nothing.
//
// Capability disclosure: an adapter that doesn't support a given
// operation (e.g., an embedding-only adapter cannot Generate)
// returns the corresponding sentinel error (ErrGenerateNotSupported,
// ErrEmbedNotSupported). The router consults Models() to avoid
// dispatching unsupported requests in the first place; the sentinel
// is the defensive fallback for callers that bypass the router.
type Adapter interface {
	// Init binds the adapter to its backend using the user-supplied
	// config map (e.g., api_key, base_url, region). Returns an
	// error on bad config or unreachable backend.
	Init(ctx context.Context, config map[string]any) error

	// Models returns the models this adapter knows how to call,
	// with capabilities, limits, and cost hints. Used by the
	// router to decide which adapter handles a given request.
	Models(ctx context.Context) ([]Descriptor, error)

	// Generate produces text given a prompt + params. Synchronous
	// (no streaming); use GenerateStream for the streaming variant.
	Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error)

	// GenerateStream streams generation incrementally. The returned
	// channel emits chunks until the response is complete or an
	// error chunk arrives; channel close indicates clean end.
	GenerateStream(ctx context.Context, req GenerateRequest) (<-chan GenerateChunk, error)

	// Embed returns a vector embedding for a text. The dimension
	// is adapter-specific and exposed via the corresponding
	// Descriptor.
	Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)

	// EstimateCost returns the expected cost of a request before
	// executing. Used by routing rules with cost_ceiling scopes.
	// Adapters that can't estimate (local models with no monetary
	// cost) return Cost{} with no error.
	EstimateCost(ctx context.Context, req GenerateRequest) (Cost, error)

	// HealthCheck verifies the adapter can reach its backend.
	// Cheap, frequently-callable.
	HealthCheck(ctx context.Context) error

	// Close releases adapter-held resources. Called during runtime
	// shutdown. Multiple calls should be safe.
	Close(ctx context.Context) error
}

// Descriptor describes one model an adapter can call. Used by
// the router to filter and pick.
type Descriptor struct {
	// ID is the model identifier as the provider exposes it
	// ("claude-sonnet-4.7", "gpt-4o", "text-embedding-3-large").
	ID string `json:"id"`

	// Capabilities is the set of capability tags the model supports:
	// "text", "long_context", "vision", "embeddings", "tool_use", ...
	// Tags are open-ended strings; the router treats them as opaque
	// labels and matches via the user's routing rules.
	Capabilities []string `json:"capabilities,omitempty"`

	// MaxTokens is the model's maximum context length in tokens.
	// Zero means unknown / unenforced.
	MaxTokens int `json:"max_tokens,omitempty"`

	// EmbeddingDim is the dimensionality of vectors this model
	// produces, when "embeddings" is among its capabilities. Zero
	// for non-embedding models.
	EmbeddingDim int `json:"embedding_dim,omitempty"`

	// Hosted indicates whether the model runs on third-party
	// infrastructure (true) or locally (false). Used by data-class
	// routing rules that forbid sending certain classes off-device.
	Hosted bool `json:"hosted"`

	// Region is the provider region (e.g., "us-east-1"). Used by
	// data-residency rules. Empty for local models.
	Region string `json:"region,omitempty"`

	// CostHints carries approximate per-token costs in USD. Used by
	// EstimateCost and routing decisions.
	CostHints CostHints `json:"cost_hints,omitempty"`
}

// CostHints is an approximate per-token cost in USD for routing.
// Adapters that operate at zero monetary cost (local models)
// leave both fields zero.
type CostHints struct {
	InputUSDPerToken  float64 `json:"input_usd_per_token,omitempty"`
	OutputUSDPerToken float64 `json:"output_usd_per_token,omitempty"`
}

// Cost is the concrete cost estimate returned by EstimateCost.
// USD only for v0.1; multi-currency lives outside the adapter SPI
// (the router converts before applying user-defined ceilings).
type Cost struct {
	USD       float64 `json:"usd"`
	Confident bool    `json:"confident"` // true for fixed-rate providers; false for variable-cost ones
}

// GenerateRequest is the input to Generate / GenerateStream. The
// router has already selected the model id and filled it in.
type GenerateRequest struct {
	// ModelID is the model the adapter should invoke. Must match
	// one of the IDs the adapter advertises via Models().
	ModelID string `json:"model_id"`

	// Messages is the conversation in OpenAI-style format. Adapters
	// translate to their provider's shape internally.
	Messages []Message `json:"messages"`

	// MaxTokens caps the response length. Zero means
	// adapter/provider default.
	MaxTokens int `json:"max_tokens,omitempty"`

	// Temperature is the sampling temperature; 0.0–2.0. Adapters
	// clamp out-of-range values to their provider's accepted range.
	Temperature float64 `json:"temperature,omitempty"`

	// Stop is an optional list of stop sequences.
	Stop []string `json:"stop,omitempty"`

	// Metadata is opaque to the adapter; round-tripped to the
	// response for audit correlation. The router stuffs the
	// originating principal and capability here.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Message is one element of a chat-style conversation. role is
// "system", "user", "assistant", or "tool".
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// GenerateResponse is the result of a Generate call.
type GenerateResponse struct {
	// Text is the assistant's response.
	Text string `json:"text"`

	// ModelID echoes the model that produced the response (may
	// differ from the request's ModelID if the provider does
	// auto-routing internally).
	ModelID string `json:"model_id"`

	// FinishReason: "stop", "length", "content_filter", etc.
	// Adapter-specific values pass through verbatim.
	FinishReason string `json:"finish_reason,omitempty"`

	// InputTokens / OutputTokens are usage counts when the provider
	// reports them. Zero when unavailable.
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// GenerateChunk is one streaming chunk from GenerateStream. Either
// Text is set (incremental content), Error is set (terminating
// error), or Done is true (clean end).
type GenerateChunk struct {
	Text  string `json:"text,omitempty"`
	Done  bool   `json:"done,omitempty"`
	Error error  `json:"error,omitempty"`
}

// EmbedRequest is the input to Embed. The router (or memory.query
// directly) supplies the model id; the adapter validates it.
type EmbedRequest struct {
	ModelID string `json:"model_id"`
	Text    string `json:"text"`
}

// EmbedResponse is the result of an Embed call.
type EmbedResponse struct {
	Vector      []float32 `json:"vector"`
	ModelID     string    `json:"model_id"`
	InputTokens int       `json:"input_tokens,omitempty"`
	Dimension   int       `json:"dimension"`
}

// Sentinel errors. Adapters wrap these (using fmt.Errorf with %w)
// when surfacing the corresponding condition; callers test with
// errors.Is.
var (
	// ErrModelDisabled is returned by the model:none adapter (or
	// by any adapter the user has explicitly disabled) for every
	// non-introspection method. Callers treat this as "the user
	// chose not to grant model access" and degrade gracefully —
	// see adapter-interface.md §MVP model adapters for the
	// graceful-degradation pattern.
	ErrModelDisabled = errors.New("model: adapter disabled")

	// ErrGenerateNotSupported is returned by adapters that only
	// support embeddings.
	ErrGenerateNotSupported = errors.New("model: generate not supported by this adapter")

	// ErrEmbedNotSupported is returned by adapters that only
	// support generation.
	ErrEmbedNotSupported = errors.New("model: embed not supported by this adapter")

	// ErrUnknownModel is returned when the requested ModelID isn't
	// one this adapter advertises via Models().
	ErrUnknownModel = errors.New("model: unknown model")

	// ErrUninitialized is returned when methods are called before
	// Init. Adapters check this defensively in every entry point.
	ErrUninitialized = errors.New("model: adapter not initialized")
)
