// Package anthropic implements the model:anthropic adapter — the
// first real model provider integration. Maps the Loamss model.Adapter
// SPI onto Anthropic's Messages API (POST /v1/messages) with bearer
// auth via x-api-key + the anthropic-version header.
//
// Capability surface: Generate + GenerateStream only. Anthropic does
// not publish an embedding API; Embed returns ErrEmbedNotSupported.
// Users who want semantic memory.query alongside Anthropic generation
// should configure a second model adapter that advertises the
// "embeddings" capability (e.g., openai, voyage when those adapters
// land, or model:dummy for testing). The runtime's adapter selection
// picks per-capability.
//
// Secrets: the API key is read from the adapter config map's "api_key"
// field. Per the global secrets rule, that value is typically itself
// the name of a macOS Keychain entry the runtime resolves at startup;
// the adapter sees the resolved string, not the env-var lookup.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/loamss/loamss/runtime/internal/adapter/model"
)

const adapterID = "model:anthropic"

// Default Anthropic API host. Overridable via the "base_url" config
// field for testing against httptest or alternative gateways
// (e.g., Vertex/Bedrock-shim proxies that speak the Messages API).
const defaultBaseURL = "https://api.anthropic.com"

// API version sent in the anthropic-version header on every request.
// Bump when adopting a new wire-level revision; the doc string lists
// what changes that's safe to do.
const defaultAPIVersion = "2023-06-01"

// modelCatalog is the static list of models this adapter advertises.
// Kept here (rather than fetched at runtime) because Anthropic doesn't
// expose a /models discovery endpoint; the list updates when the
// adapter is rebuilt against a new Loamss release. Cost hints are
// USD-per-token derived from the published per-1M pricing at the
// commit time of this file.
//
// IDs match Anthropic's model identifiers; the router quotes them
// verbatim to the API.
var modelCatalog = []model.Descriptor{
	{
		ID:           "claude-sonnet-4-5",
		Capabilities: []string{"text", "long_context", "vision", "tool_use"},
		MaxTokens:    200_000,
		Hosted:       true,
		Region:       "us",
		CostHints: model.CostHints{
			InputUSDPerToken:  3.0 / 1_000_000,
			OutputUSDPerToken: 15.0 / 1_000_000,
		},
	},
	{
		ID:           "claude-opus-4-5",
		Capabilities: []string{"text", "long_context", "vision", "tool_use"},
		MaxTokens:    200_000,
		Hosted:       true,
		Region:       "us",
		CostHints: model.CostHints{
			InputUSDPerToken:  15.0 / 1_000_000,
			OutputUSDPerToken: 75.0 / 1_000_000,
		},
	},
	{
		ID:           "claude-haiku-4-5",
		Capabilities: []string{"text", "long_context", "tool_use"},
		MaxTokens:    200_000,
		Hosted:       true,
		Region:       "us",
		CostHints: model.CostHints{
			InputUSDPerToken:  0.80 / 1_000_000,
			OutputUSDPerToken: 4.0 / 1_000_000,
		},
	},
}

func init() {
	model.Register(adapterID, func() model.Adapter { return &Adapter{} })
}

// Adapter is the Anthropic concrete adapter. Zero value is unusable;
// call Init.
type Adapter struct {
	mu sync.RWMutex

	inited     bool
	apiKey     string
	baseURL    string
	apiVersion string
	httpClient *http.Client

	// catalog is a copy of modelCatalog the adapter holds so tests
	// can override (e.g., point cost hints at zero for cost-ceiling
	// regression tests). Production callers see the package
	// defaults.
	catalog []model.Descriptor
}

// Init reads the adapter's config:
//
//	api_key:     string  REQUIRED  Anthropic API key (resolved upstream
//	                                from $ANTHROPIC_API_KEY or
//	                                Keychain by the runtime).
//	base_url:    string  optional  Override the default host. Used by
//	                                tests; production should leave it
//	                                blank.
//	api_version: string  optional  Override the anthropic-version
//	                                header. Default 2023-06-01.
//	timeout_ms:  int     optional  Per-request timeout. Default 60s.
func (a *Adapter) Init(_ context.Context, config map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	key, _ := config["api_key"].(string)
	if strings.TrimSpace(key) == "" {
		return errors.New("model:anthropic: api_key is required (resolve from $ANTHROPIC_API_KEY or Keychain)")
	}
	a.apiKey = key

	baseURL := defaultBaseURL
	if v, ok := config["base_url"].(string); ok && v != "" {
		baseURL = strings.TrimRight(v, "/")
	}
	a.baseURL = baseURL

	apiVersion := defaultAPIVersion
	if v, ok := config["api_version"].(string); ok && v != "" {
		apiVersion = v
	}
	a.apiVersion = apiVersion

	timeout := 60 * time.Second
	if v, ok := config["timeout_ms"]; ok {
		switch t := v.(type) {
		case int:
			timeout = time.Duration(t) * time.Millisecond
		case float64:
			timeout = time.Duration(t) * time.Millisecond
		}
	}
	a.httpClient = &http.Client{Timeout: timeout}

	// Copy the catalog so per-adapter overrides don't bleed across
	// adapter instances in tests.
	a.catalog = append([]model.Descriptor(nil), modelCatalog...)

	a.inited = true
	return nil
}

// Models returns the static catalog. Anthropic doesn't expose a
// runtime discovery endpoint, so this is a compile-time list.
func (a *Adapter) Models(_ context.Context) ([]model.Descriptor, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.inited {
		return nil, model.ErrUninitialized
	}
	out := make([]model.Descriptor, len(a.catalog))
	copy(out, a.catalog)
	return out, nil
}

// Generate maps a Loamss GenerateRequest onto the Messages API.
// The request body shape:
//
//	{
//	  "model": "claude-sonnet-4-5",
//	  "max_tokens": 1024,
//	  "system": "...",                 // extracted from messages
//	  "messages": [{role, content}],   // OpenAI-style → Messages
//	  "temperature": 0.7,
//	  "stop_sequences": ["..."]
//	}
//
// Anthropic's API distinguishes "system" from "messages" — it doesn't
// accept role=system entries in the messages array. We extract the
// first system message into the top-level field; subsequent system
// messages join with newlines (rare but legal).
func (a *Adapter) Generate(ctx context.Context, req model.GenerateRequest) (*model.GenerateResponse, error) {
	a.mu.RLock()
	if !a.inited {
		a.mu.RUnlock()
		return nil, model.ErrUninitialized
	}
	a.mu.RUnlock()

	if err := a.validateModel(req.ModelID); err != nil {
		return nil, err
	}

	body, err := buildMessagesBody(req, false)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, "/v1/messages", body)
	if err != nil {
		return nil, err
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("model:anthropic: HTTP error: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("model:anthropic: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model:anthropic: status %d: %s",
			resp.StatusCode, string(respBytes))
	}

	var apiResp messagesResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("model:anthropic: decoding response: %w", err)
	}

	// Concatenate text content blocks. Multi-block responses arise
	// when Anthropic interleaves text + tool_use; we drop tool_use
	// blocks here because the runtime doesn't yet plumb them through
	// to the caller. (Tool-use support is a future concern; this
	// adapter is the foundation.)
	var text strings.Builder
	for _, block := range apiResp.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}

	return &model.GenerateResponse{
		Text:         text.String(),
		ModelID:      apiResp.Model,
		FinishReason: apiResp.StopReason,
		InputTokens:  apiResp.Usage.InputTokens,
		OutputTokens: apiResp.Usage.OutputTokens,
	}, nil
}

// GenerateStream uses Anthropic's SSE streaming. Each chunk arrives
// as a Server-Sent Event; we parse the event-name + data lines, dispatch
// on event type, and emit text deltas as GenerateChunk{Text: ...}
// until the message_stop event closes the stream.
//
// The returned channel is closed by this method's goroutine when the
// stream ends; consumers select on the channel + ctx.Done.
func (a *Adapter) GenerateStream(ctx context.Context, req model.GenerateRequest) (<-chan model.GenerateChunk, error) {
	a.mu.RLock()
	if !a.inited {
		a.mu.RUnlock()
		return nil, model.ErrUninitialized
	}
	a.mu.RUnlock()

	if err := a.validateModel(req.ModelID); err != nil {
		return nil, err
	}

	body, err := buildMessagesBody(req, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, "/v1/messages", body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("model:anthropic: HTTP error: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("model:anthropic: status %d: %s",
			resp.StatusCode, string(respBytes))
	}

	out := make(chan model.GenerateChunk, 16)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		parseSSEStream(ctx, resp.Body, out)
	}()
	return out, nil
}

// Embed returns ErrEmbedNotSupported. Anthropic does not publish an
// embedding API as of this commit. The runtime's adapter selection
// will pick a different adapter for embeddings; if no
// embedding-capable adapter is configured, memory.query degrades
// gracefully via the runtime's isError contract.
func (a *Adapter) Embed(_ context.Context, _ model.EmbedRequest) (*model.EmbedResponse, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.inited {
		return nil, model.ErrUninitialized
	}
	return nil, model.ErrEmbedNotSupported
}

// EstimateCost computes a rough USD estimate from the published
// per-token pricing. Confident: Anthropic's pricing is per-token and
// stable enough that the estimate matches the actual bill closely.
// The router uses this for cost-ceiling routing decisions.
func (a *Adapter) EstimateCost(_ context.Context, req model.GenerateRequest) (model.Cost, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.inited {
		return model.Cost{}, model.ErrUninitialized
	}
	var inputCost float64
	for _, m := range a.catalog {
		if m.ID == req.ModelID {
			// Estimate input tokens as 4 chars per token (rough but
			// stable). Routing decisions don't need precision.
			est := 0
			for _, msg := range req.Messages {
				est += len(msg.Content) / 4
			}
			inputCost = float64(est) * m.CostHints.InputUSDPerToken
			// Assume MaxTokens output if unset (worst case).
			maxOut := req.MaxTokens
			if maxOut == 0 {
				maxOut = 1024
			}
			outputCost := float64(maxOut) * m.CostHints.OutputUSDPerToken
			return model.Cost{USD: inputCost + outputCost, Confident: true}, nil
		}
	}
	return model.Cost{}, fmt.Errorf("%w: %s", model.ErrUnknownModel, req.ModelID)
}

// HealthCheck pings the API with a tiny request. Returns nil on 200,
// the API error otherwise.
//
// The implementation: send a 1-token completion against the cheapest
// model. Costs ~$0.00001 per check. Run sparingly (the runtime's
// health probe loop, on `loamss doctor`, etc. — not per-request).
func (a *Adapter) HealthCheck(ctx context.Context) error {
	if !a.inited {
		return model.ErrUninitialized
	}
	_, err := a.Generate(ctx, model.GenerateRequest{
		ModelID:   "claude-haiku-4-5",
		MaxTokens: 1,
		Messages:  []model.Message{{Role: "user", Content: "."}},
	})
	return err
}

// Close releases the HTTP client's idle connections.
func (a *Adapter) Close(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.httpClient != nil {
		a.httpClient.CloseIdleConnections()
	}
	a.inited = false
	return nil
}

// --- internal helpers --------------------------------------------------

// validateModel ensures the request's ModelID is one this adapter
// advertises. Cheap defense against the router handing us an id we
// don't know.
func (a *Adapter) validateModel(id string) error {
	for _, m := range a.catalog {
		if m.ID == id {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", model.ErrUnknownModel, id)
}

// newRequest builds an authenticated request. anthropic-version and
// x-api-key headers are required on every call.
func (a *Adapter) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", a.apiVersion)
	req.Header.Set("content-type", "application/json")
	return req, nil
}

// --- request/response shapes -------------------------------------------

// messagesRequest mirrors the wire shape of /v1/messages. JSON tag
// names match the Anthropic API exactly.
type messagesRequest struct {
	Model         string     `json:"model"`
	MaxTokens     int        `json:"max_tokens"`
	System        string     `json:"system,omitempty"`
	Messages      []apiMsg   `json:"messages"`
	Temperature   *float64   `json:"temperature,omitempty"`
	StopSequences []string   `json:"stop_sequences,omitempty"`
	Stream        bool       `json:"stream,omitempty"`
	Metadata      *metadataR `json:"metadata,omitempty"`
}

type apiMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type metadataR struct {
	UserID string `json:"user_id,omitempty"`
}

type messagesResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Content    []contentBlock `json:"content"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"`
	Usage      apiUsage       `json:"usage"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type apiUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// buildMessagesBody translates a Loamss GenerateRequest into the
// Messages API shape. The most subtle bit is the system-message
// extraction: Anthropic doesn't accept role=system inside the
// messages array; it goes on the top-level "system" field.
func buildMessagesBody(req model.GenerateRequest, stream bool) ([]byte, error) {
	out := messagesRequest{
		Model:         req.ModelID,
		MaxTokens:     req.MaxTokens,
		StopSequences: req.Stop,
		Stream:        stream,
	}
	if out.MaxTokens == 0 {
		out.MaxTokens = 1024
	}
	if req.Temperature > 0 {
		t := req.Temperature
		out.Temperature = &t
	}

	var systemParts []string
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			systemParts = append(systemParts, msg.Content)
			continue
		}
		out.Messages = append(out.Messages, apiMsg{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	if len(systemParts) > 0 {
		out.System = strings.Join(systemParts, "\n\n")
	}

	// Anthropic requires at least one user message.
	if len(out.Messages) == 0 {
		return nil, errors.New("model:anthropic: request must contain at least one user/assistant message")
	}

	if uid := req.Metadata["user_id"]; uid != "" {
		out.Metadata = &metadataR{UserID: uid}
	}

	return json.Marshal(out)
}

// --- SSE stream parser -------------------------------------------------

// parseSSEStream reads Anthropic's server-sent events from r and
// forwards text deltas to out. The stream shape:
//
//	event: message_start
//	data: {"type": "message_start", "message": {...}}
//
//	event: content_block_start
//	data: {"type": "content_block_start", "index": 0, "content_block": {...}}
//
//	event: content_block_delta
//	data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "..."}}
//
//	event: content_block_stop
//	data: {"type": "content_block_stop", "index": 0}
//
//	event: message_delta
//	data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": N}}
//
//	event: message_stop
//	data: {"type": "message_stop"}
//
// We only emit GenerateChunk{Text: ...} for content_block_delta
// events; the other events are useful internally (to capture final
// usage in a future extension) but currently dropped.
func parseSSEStream(ctx context.Context, r io.Reader, out chan<- model.GenerateChunk) {
	scanner := bufio.NewScanner(r)
	// SSE messages can include large embedded JSON for tool_use
	// blocks; 1 MiB is comfortable.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := scanner.Text()
		// Anthropic emits "event: <name>" + "data: <json>" + blank
		// line between events. We only need the data lines.
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		switch event.Type {
		case "content_block_delta":
			if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
				select {
				case out <- model.GenerateChunk{Text: event.Delta.Text}:
				case <-ctx.Done():
					return
				}
			}
		case "message_stop":
			select {
			case out <- model.GenerateChunk{Done: true}:
			case <-ctx.Done():
			}
			return
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case out <- model.GenerateChunk{Error: err}:
		case <-ctx.Done():
		}
	}
}
