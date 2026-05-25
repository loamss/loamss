// Package openai implements the model:openai adapter — the second
// major LLM provider for Loamss, beyond Anthropic. Maps the
// model.Adapter SPI onto OpenAI's REST API:
//
//	POST /v1/chat/completions   — Generate + GenerateStream (SSE)
//	POST /v1/embeddings          — Embed
//
// Why OpenAI matters for Loamss: most pgvector deployments pair
// with OpenAI's text-embedding-3 family because the latency +
// dimension trade-offs are well-understood. Anthropic doesn't
// publish an embedding API; without an OpenAI adapter, users on
// pgvector either run Ollama locally for embeddings or wait. This
// closes that gap.
//
// Auth: bearer token via the `api_key` config field. Per the
// project's secrets rule, that value is the resolved string the
// runtime read from macOS Keychain or env — the adapter never
// sees the lookup name.
//
// Compatibility: this adapter speaks the OpenAI API exactly. Many
// providers (Together, Anyscale, Fireworks, OpenRouter, local
// vLLM/Ollama-OpenAI-shim) expose an identical wire and accept
// `base_url` overrides; they work with this adapter unchanged.
// Provider-specific quirks (Azure's resource/deployment routing,
// Bedrock's model prefixes) belong in separate adapters.
package openai

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

const adapterID = "model:openai"

// Default API host. Override via `base_url` in config to point at
// an OpenAI-compatible gateway (Together, Anyscale, OpenRouter,
// local vLLM, etc.).
const defaultBaseURL = "https://api.openai.com"

// modelCatalog is the static set of OpenAI models this adapter
// advertises. Curated for the categories Loamss capsules
// actually need: a fast text model, a long-context text model,
// and the embedding family.
//
// Prices encoded as USD-per-token from the per-1M-tokens pricing
// public at the time of this commit. They drift over time; this
// adapter's CostHints are best-effort, not invoiced reality.
var modelCatalog = []model.Descriptor{
	{
		ID:           "gpt-4o",
		Capabilities: []string{"text", "long_context", "vision", "tool_use"},
		MaxTokens:    128_000,
		Hosted:       true,
		Region:       "us",
		CostHints: model.CostHints{
			InputUSDPerToken:  2.50 / 1_000_000,
			OutputUSDPerToken: 10.00 / 1_000_000,
		},
	},
	{
		ID:           "gpt-4o-mini",
		Capabilities: []string{"text", "long_context", "vision", "tool_use"},
		MaxTokens:    128_000,
		Hosted:       true,
		Region:       "us",
		CostHints: model.CostHints{
			InputUSDPerToken:  0.150 / 1_000_000,
			OutputUSDPerToken: 0.600 / 1_000_000,
		},
	},
	{
		ID:           "gpt-4-turbo",
		Capabilities: []string{"text", "long_context", "vision", "tool_use"},
		MaxTokens:    128_000,
		Hosted:       true,
		Region:       "us",
		CostHints: model.CostHints{
			InputUSDPerToken:  10.00 / 1_000_000,
			OutputUSDPerToken: 30.00 / 1_000_000,
		},
	},
	{
		ID:           "text-embedding-3-small",
		Capabilities: []string{"embeddings"},
		EmbeddingDim: 1536,
		Hosted:       true,
		Region:       "us",
		CostHints: model.CostHints{
			InputUSDPerToken: 0.020 / 1_000_000,
		},
	},
	{
		ID:           "text-embedding-3-large",
		Capabilities: []string{"embeddings"},
		EmbeddingDim: 3072,
		Hosted:       true,
		Region:       "us",
		CostHints: model.CostHints{
			InputUSDPerToken: 0.130 / 1_000_000,
		},
	},
	{
		ID:           "text-embedding-ada-002",
		Capabilities: []string{"embeddings"},
		EmbeddingDim: 1536,
		Hosted:       true,
		Region:       "us",
		CostHints: model.CostHints{
			InputUSDPerToken: 0.100 / 1_000_000,
		},
	},
}

// Adapter is the model:openai concrete adapter.
//
// Zero value is unusable; call Init before any other method.
// After Init, methods are safe for concurrent use — the HTTP
// client is goroutine-safe and the only mutable state is the
// `inited` flag guarded by mu.
type Adapter struct {
	mu         sync.RWMutex
	inited     bool
	apiKey     string
	baseURL    string
	httpClient *http.Client
	// org is the optional OpenAI-Organization header for users
	// who route requests through a specific org. Empty means
	// "use the api_key's default org."
	org string
}

func init() {
	model.Register(adapterID, func() model.Adapter { return &Adapter{} })
}

// Init reads config + validates required fields. Does not make a
// network call — the first Generate / Embed surfaces auth errors.
// (HealthCheck does an explicit ping; the runtime calls it after
// Init if it wants fail-fast behaviour.)
//
// Config keys:
//
//	api_key:   "sk-..."                  (required)
//	base_url:  "https://api.openai.com"  (default; OpenAI host. Override
//	           for Together / Anyscale / Azure-OpenAI-compat / local proxies)
//	org:       "org-..."                 (optional; OpenAI-Organization
//	           header for org-scoped billing)
//	timeout:   "60s"                     (default; per-request HTTP timeout)
func (a *Adapter) Init(_ context.Context, config map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	apiKey := stringField(config, "api_key", "")
	if apiKey == "" {
		return errors.New("model:openai: config requires `api_key`")
	}
	baseURL := stringField(config, "base_url", defaultBaseURL)
	baseURL = strings.TrimRight(baseURL, "/")
	org := stringField(config, "org", "")

	timeout := 60 * time.Second
	if raw, ok := config["timeout"].(string); ok && raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			timeout = d
		}
	}

	a.apiKey = apiKey
	a.baseURL = baseURL
	a.org = org
	a.httpClient = &http.Client{Timeout: timeout}
	a.inited = true
	return nil
}

// Models returns the static catalog. OpenAI does publish a
// /v1/models endpoint but the response is noisy (includes
// fine-tunes, retired models, internal IDs) and slow; the
// curated catalog gives the router predictable choices.
func (a *Adapter) Models(_ context.Context) ([]model.Descriptor, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	out := make([]model.Descriptor, len(modelCatalog))
	copy(out, modelCatalog)
	return out, nil
}

// Generate calls POST /v1/chat/completions with stream=false. The
// OpenAI response shape is well-known; we just unpack the first
// choice's text + usage and return.
func (a *Adapter) Generate(ctx context.Context, req model.GenerateRequest) (*model.GenerateResponse, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	if err := a.validateGenerateModel(req.ModelID); err != nil {
		return nil, err
	}

	body, err := buildChatBody(req, false)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, "/v1/chat/completions", body)
	if err != nil {
		return nil, err
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("model:openai: HTTP error: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("model:openai: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model:openai: status %d: %s",
			resp.StatusCode, string(respBytes))
	}

	var apiResp chatCompletionsResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("model:openai: decoding response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return nil, errors.New("model:openai: response had no choices")
	}
	first := apiResp.Choices[0]

	return &model.GenerateResponse{
		Text:         first.Message.Content,
		ModelID:      apiResp.Model,
		FinishReason: first.FinishReason,
		InputTokens:  apiResp.Usage.PromptTokens,
		OutputTokens: apiResp.Usage.CompletionTokens,
	}, nil
}

// GenerateStream calls /v1/chat/completions with stream=true. The
// OpenAI SSE shape is `data: {json}\n\ndata: [DONE]\n\n`; each
// JSON payload's choices[0].delta.content carries an incremental
// token. We emit each delta as a GenerateChunk{Text: ...} and
// close the channel on [DONE] or transport error.
func (a *Adapter) GenerateStream(ctx context.Context, req model.GenerateRequest) (<-chan model.GenerateChunk, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	if err := a.validateGenerateModel(req.ModelID); err != nil {
		return nil, err
	}

	body, err := buildChatBody(req, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, "/v1/chat/completions", body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("model:openai: HTTP error: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("model:openai: status %d: %s",
			resp.StatusCode, string(respBytes))
	}

	out := make(chan model.GenerateChunk, 16)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		parseOpenAIStream(ctx, resp.Body, out)
	}()
	return out, nil
}

// Embed calls /v1/embeddings. Single-text request; the OpenAI API
// supports batch input via an array but the SPI is one-at-a-time
// today.
func (a *Adapter) Embed(ctx context.Context, req model.EmbedRequest) (*model.EmbedResponse, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	desc, err := a.findEmbeddingDescriptor(req.ModelID)
	if err != nil {
		return nil, err
	}
	if req.Text == "" {
		return nil, errors.New("model:openai: Embed.Text is required")
	}

	body, err := json.Marshal(map[string]any{
		"model": req.ModelID,
		"input": req.Text,
	})
	if err != nil {
		return nil, fmt.Errorf("model:openai: encoding embed request: %w", err)
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, "/v1/embeddings", body)
	if err != nil {
		return nil, err
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("model:openai: HTTP error: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("model:openai: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model:openai: status %d: %s",
			resp.StatusCode, string(respBytes))
	}

	var apiResp embeddingsResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("model:openai: decoding response: %w", err)
	}
	if len(apiResp.Data) == 0 {
		return nil, errors.New("model:openai: embeddings response had no data")
	}
	return &model.EmbedResponse{
		Vector:      apiResp.Data[0].Embedding,
		ModelID:     apiResp.Model,
		InputTokens: apiResp.Usage.PromptTokens,
		Dimension:   desc.EmbeddingDim,
	}, nil
}

// EstimateCost multiplies the request's expected input + output
// token counts by the catalog's per-token prices. We estimate
// inputs by character count / 4 (a rough OpenAI heuristic);
// outputs use the request's MaxTokens as an upper bound. The
// confidence flag is true because the rates are public + fixed
// per request.
func (a *Adapter) EstimateCost(_ context.Context, req model.GenerateRequest) (model.Cost, error) {
	if err := a.requireInited(); err != nil {
		return model.Cost{}, err
	}
	var desc *model.Descriptor
	for i := range modelCatalog {
		if modelCatalog[i].ID == req.ModelID {
			desc = &modelCatalog[i]
			break
		}
	}
	if desc == nil {
		// Unknown model — no estimate. Don't error; the router
		// may still want to dispatch.
		return model.Cost{}, nil
	}
	var chars int
	for _, m := range req.Messages {
		chars += len(m.Content)
	}
	inputTokens := chars / 4
	outputTokens := req.MaxTokens
	if outputTokens == 0 {
		outputTokens = 512 // fallback estimate when caller didn't set MaxTokens
	}
	cost := float64(inputTokens)*desc.CostHints.InputUSDPerToken +
		float64(outputTokens)*desc.CostHints.OutputUSDPerToken
	return model.Cost{USD: cost, Confident: true}, nil
}

// HealthCheck pings /v1/models — the cheapest authenticated
// endpoint OpenAI exposes. 200 = creds valid; 401 = bad key;
// anything else = transport/upstream issue.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	httpReq, err := a.newRequest(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("model:openai: HealthCheck transport: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("model:openai: HealthCheck status %d: %s",
			resp.StatusCode, string(respBytes))
	}
	return nil
}

// Close is a no-op (HTTP client doesn't need explicit shutdown)
// other than flipping the inited flag so subsequent calls fail
// fast.
func (a *Adapter) Close(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inited = false
	a.httpClient = nil
	return nil
}

// --- internals -------------------------------------------------------------

func (a *Adapter) requireInited() error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.inited {
		return model.ErrUninitialized
	}
	return nil
}

// validateGenerateModel ensures the requested model is in the
// catalog AND has the "text" capability — calling Generate on an
// embedding model is a programming error worth catching early.
func (a *Adapter) validateGenerateModel(id string) error {
	for _, d := range modelCatalog {
		if d.ID != id {
			continue
		}
		for _, c := range d.Capabilities {
			if c == "text" {
				return nil
			}
		}
		return fmt.Errorf("model:openai: %q is not a generative model", id)
	}
	return fmt.Errorf("model:openai: unknown model %q", id)
}

func (a *Adapter) findEmbeddingDescriptor(id string) (*model.Descriptor, error) {
	for i := range modelCatalog {
		if modelCatalog[i].ID != id {
			continue
		}
		for _, c := range modelCatalog[i].Capabilities {
			if c == "embeddings" {
				return &modelCatalog[i], nil
			}
		}
		return nil, fmt.Errorf("model:openai: %q is not an embedding model", id)
	}
	return nil, fmt.Errorf("model:openai: unknown model %q", id)
}

func (a *Adapter) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var br io.Reader
	if body != nil {
		br = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, br)
	if err != nil {
		return nil, fmt.Errorf("model:openai: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.org != "" {
		req.Header.Set("OpenAI-Organization", a.org)
	}
	return req, nil
}

func buildChatBody(req model.GenerateRequest, stream bool) ([]byte, error) {
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, map[string]any{
			"role":    m.Role,
			"content": m.Content,
		})
	}
	payload := map[string]any{
		"model":    req.ModelID,
		"messages": messages,
		"stream":   stream,
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}
	if len(req.Stop) > 0 {
		payload["stop"] = req.Stop
	}
	if stream {
		// stream_options reveals usage on the final chunk; without
		// it we don't know token counts in streaming mode.
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	return json.Marshal(payload)
}

// --- streaming -------------------------------------------------------------

// parseOpenAIStream consumes the SSE response body and pushes
// text deltas onto the out channel. OpenAI's framing:
//
//	data: {"choices":[{"delta":{"content":"..."}, ...}], ...}
//	data: {"choices":[{"delta":{"content":"..."}, ...}], ...}
//	data: {"choices":[{"delta":{}, "finish_reason":"stop"}]}
//	data: [DONE]
//
// We forward each non-empty delta.content. The literal "[DONE]"
// (no JSON body) terminates the stream.
func parseOpenAIStream(ctx context.Context, body io.Reader, out chan<- model.GenerateChunk) {
	// Bigger buffer than bufio's default; long completions can
	// produce a single chunk past 64 KiB.
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			select {
			case out <- model.GenerateChunk{Done: true}:
			case <-ctx.Done():
			}
			return
		}
		var chunk chatCompletionsChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// Skip un-parseable lines silently. OpenAI sometimes
			// emits comment lines (`: ping`) we don't care about.
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta.Content
		if delta == "" {
			continue
		}
		select {
		case out <- model.GenerateChunk{Text: delta}:
		case <-ctx.Done():
			return
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		select {
		case out <- model.GenerateChunk{Error: err}:
		case <-ctx.Done():
		}
	}
}

// --- response shapes -------------------------------------------------------

type chatCompletionsResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int    `json:"index"`
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type chatCompletionsChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

type embeddingsResponse struct {
	Model string `json:"model"`
	Data  []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func stringField(config map[string]any, key, fallback string) string {
	v, ok := config[key]
	if !ok {
		return fallback
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return fallback
	}
	return s
}
