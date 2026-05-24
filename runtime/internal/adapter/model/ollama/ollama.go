// Package ollama implements the model:ollama adapter — local model
// access via the Ollama HTTP API. Unlike model:anthropic, Ollama
// provides embeddings, so a Loamss runtime configured with only
// Ollama gets semantic memory.query out of the box.
//
// Why Ollama specifically: it's the path of least resistance for
// users who want privacy or no API spend. Single binary, single
// host (default http://localhost:11434), real embeddings, real
// generation, growing model catalog (llama, qwen, mistral, gpt-oss,
// gemma, phi, ...).
//
// Capability surface: Generate + GenerateStream + Embed. Model
// catalog is configured by the user (the runtime can't infer which
// pulled Ollama model is for chat vs embeddings); when unset, falls
// back to community defaults (llama3.2 + nomic-embed-text).
//
// No API key, no rate limits, no network in tests — perfect for
// reference-implementation testing of the model.Adapter SPI.
package ollama

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

const adapterID = "model:ollama"

// defaultBaseURL is Ollama's stock listen address. Overridable via
// the "base_url" config field — useful for remote Ollama instances
// or Ollama-compatible servers (LM Studio, vLLM with the Ollama
// shim).
const defaultBaseURL = "http://localhost:11434"

// Default models when the user doesn't explicitly configure the
// catalog. Picked because they're widely available + each covers
// one of the two capabilities we care about today:
//
//	llama3.2          chat / text completion (3B parameter, small + fast)
//	nomic-embed-text  embeddings (768-dim, community standard)
//
// Both must be pulled by the user first (`ollama pull <name>`); the
// adapter doesn't auto-pull because that would interact poorly with
// resource-constrained machines and silent network costs.
const (
	defaultChatModel  = "llama3.2"
	defaultEmbedModel = "nomic-embed-text"
	defaultEmbedDim   = 768
)

func init() {
	model.Register(adapterID, func() model.Adapter { return &Adapter{} })
}

// Adapter is the Ollama concrete adapter. Zero value is unusable;
// call Init.
type Adapter struct {
	mu sync.RWMutex

	inited     bool
	baseURL    string
	httpClient *http.Client

	// catalog is what Models() advertises. Either populated from
	// the user's explicit config or built from the defaults.
	catalog []model.Descriptor
}

// Init reads adapter config:
//
//	base_url:    string  optional  default http://localhost:11434
//	timeout_ms:  int     optional  per-request timeout default 300s
//	                                (Ollama can be slow on cold-loaded
//	                                models; we err generous)
//	models:      list    optional  capability catalog (see below).
//	                                When empty, falls back to
//	                                llama3.2 + nomic-embed-text.
//
// Each entry in `models` shapes the catalog Models() returns:
//
//	models:
//	  - id: qwen2.5-coder
//	    capabilities: [text, tool_use]
//	    max_tokens: 32768
//	  - id: mxbai-embed-large
//	    capabilities: [embeddings]
//	    embedding_dim: 1024
//
// The router consults `capabilities` to pick which model to use for
// which task — memory.query looks for an "embeddings" entry,
// organizer capsules calling model.call look for "text" or
// "long_context".
func (a *Adapter) Init(_ context.Context, config map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	baseURL := defaultBaseURL
	if v, ok := config["base_url"].(string); ok && v != "" {
		baseURL = strings.TrimRight(v, "/")
	}
	a.baseURL = baseURL

	timeout := 5 * time.Minute
	if v, ok := config["timeout_ms"]; ok {
		switch t := v.(type) {
		case int:
			timeout = time.Duration(t) * time.Millisecond
		case float64:
			timeout = time.Duration(t) * time.Millisecond
		}
	}
	a.httpClient = &http.Client{Timeout: timeout}

	catalog, err := buildCatalog(config["models"])
	if err != nil {
		return err
	}
	a.catalog = catalog

	a.inited = true
	return nil
}

// buildCatalog turns the user's `models` config into the typed
// Descriptor slice. Permissive: accepts entries as map[string]any
// (the YAML decoder's natural shape). Defaults when empty.
func buildCatalog(raw any) ([]model.Descriptor, error) {
	if raw == nil {
		return defaultCatalog(), nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("model:ollama: models must be a list, got %T", raw)
	}
	if len(list) == 0 {
		return defaultCatalog(), nil
	}
	out := make([]model.Descriptor, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("model:ollama: models[%d] must be a map, got %T", i, item)
		}
		d := model.Descriptor{Hosted: false} // local
		if v, ok := m["id"].(string); ok {
			d.ID = v
		}
		if d.ID == "" {
			return nil, fmt.Errorf("model:ollama: models[%d].id is required", i)
		}
		// capabilities: []string
		if v, ok := m["capabilities"]; ok {
			switch t := v.(type) {
			case []any:
				for _, c := range t {
					if s, ok := c.(string); ok {
						d.Capabilities = append(d.Capabilities, s)
					}
				}
			case []string:
				d.Capabilities = append(d.Capabilities, t...)
			}
		}
		if v, ok := m["max_tokens"]; ok {
			d.MaxTokens = toInt(v)
		}
		if v, ok := m["embedding_dim"]; ok {
			d.EmbeddingDim = toInt(v)
		}
		// Local models have no monetary cost; CostHints stays zero.
		out = append(out, d)
	}
	return out, nil
}

// defaultCatalog is what we ship when the user doesn't configure
// the model list. Two entries that cover the basic capability set.
// The user must have pulled both via `ollama pull` for these to
// actually work; we don't auto-pull.
func defaultCatalog() []model.Descriptor {
	return []model.Descriptor{
		{
			ID:           defaultChatModel,
			Capabilities: []string{"text", "tool_use"},
			MaxTokens:    8192,
			Hosted:       false,
		},
		{
			ID:           defaultEmbedModel,
			Capabilities: []string{"embeddings"},
			EmbeddingDim: defaultEmbedDim,
			Hosted:       false,
		},
	}
}

// Models returns the configured catalog. Local adapters don't have
// a remote /models endpoint to consult; the user (or our defaults)
// is the source of truth.
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

// Generate maps onto POST /api/chat with stream=false. Ollama's
// chat endpoint accepts the same {role, content} message shape we
// already have — no system-extraction footwork like Anthropic.
//
// Response shape (single object when stream=false):
//
//	{
//	  "model": "...",
//	  "created_at": "...",
//	  "message": { "role": "assistant", "content": "..." },
//	  "done": true,
//	  "prompt_eval_count": 8,
//	  "eval_count": 4
//	}
func (a *Adapter) Generate(ctx context.Context, req model.GenerateRequest) (*model.GenerateResponse, error) {
	a.mu.RLock()
	if !a.inited {
		a.mu.RUnlock()
		return nil, model.ErrUninitialized
	}
	a.mu.RUnlock()

	if err := a.validateModel(req.ModelID, "text"); err != nil {
		return nil, err
	}

	body, err := buildChatBody(req, false)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newRequest(ctx, "/api/chat", body)
	if err != nil {
		return nil, err
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("model:ollama: HTTP error: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("model:ollama: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model:ollama: status %d: %s",
			resp.StatusCode, string(respBytes))
	}

	var apiResp chatResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("model:ollama: decoding response: %w", err)
	}
	return &model.GenerateResponse{
		Text:         apiResp.Message.Content,
		ModelID:      apiResp.Model,
		FinishReason: finishReasonFromDone(apiResp.DoneReason, apiResp.Done),
		InputTokens:  apiResp.PromptEvalCount,
		OutputTokens: apiResp.EvalCount,
	}, nil
}

// GenerateStream uses POST /api/chat with stream=true. Ollama's
// streaming format is newline-delimited JSON (NDJSON), NOT SSE —
// each line is one complete JSON object representing a chunk.
// The final chunk has done=true and includes total token counts.
func (a *Adapter) GenerateStream(ctx context.Context, req model.GenerateRequest) (<-chan model.GenerateChunk, error) {
	a.mu.RLock()
	if !a.inited {
		a.mu.RUnlock()
		return nil, model.ErrUninitialized
	}
	a.mu.RUnlock()

	if err := a.validateModel(req.ModelID, "text"); err != nil {
		return nil, err
	}

	body, err := buildChatBody(req, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newRequest(ctx, "/api/chat", body)
	if err != nil {
		return nil, err
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("model:ollama: HTTP error: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("model:ollama: status %d: %s",
			resp.StatusCode, string(respBytes))
	}

	out := make(chan model.GenerateChunk, 16)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		parseNDJSONStream(ctx, resp.Body, out)
	}()
	return out, nil
}

// Embed maps onto POST /api/embed. Request:
//
//	{ "model": "...", "input": "..." }
//
// Response:
//
//	{ "model": "...", "embeddings": [[...float...]], "prompt_eval_count": 7 }
//
// Ollama returns a list of vectors (input can be batched as []string);
// we always send one input and read embeddings[0].
func (a *Adapter) Embed(ctx context.Context, req model.EmbedRequest) (*model.EmbedResponse, error) {
	a.mu.RLock()
	if !a.inited {
		a.mu.RUnlock()
		return nil, model.ErrUninitialized
	}
	a.mu.RUnlock()

	if err := a.validateModel(req.ModelID, "embeddings"); err != nil {
		return nil, err
	}

	body, err := json.Marshal(embedRequest{Model: req.ModelID, Input: req.Text})
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newRequest(ctx, "/api/embed", body)
	if err != nil {
		return nil, err
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("model:ollama: HTTP error: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("model:ollama: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model:ollama: status %d: %s",
			resp.StatusCode, string(respBytes))
	}

	var apiResp embedResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("model:ollama: decoding response: %w", err)
	}
	if len(apiResp.Embeddings) == 0 {
		return nil, errors.New("model:ollama: empty embeddings array")
	}
	vec := apiResp.Embeddings[0]
	return &model.EmbedResponse{
		Vector:      vec,
		ModelID:     apiResp.Model,
		InputTokens: apiResp.PromptEvalCount,
		Dimension:   len(vec),
	}, nil
}

// EstimateCost returns zero, confident. Local models incur compute
// cost but no monetary cost; the router's cost ceiling never gates
// these calls. (Users who care about machine load can rate-limit
// at the OS level.)
func (a *Adapter) EstimateCost(_ context.Context, _ model.GenerateRequest) (model.Cost, error) {
	if !a.inited {
		return model.Cost{}, model.ErrUninitialized
	}
	return model.Cost{USD: 0, Confident: true}, nil
}

// HealthCheck pings GET /api/tags — Ollama's "what's pulled" endpoint.
// Cheap (no model inference) and confirms the daemon is up.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	if !a.inited {
		return model.ErrUninitialized
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("model:ollama: health check: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("model:ollama: health check returned %d", resp.StatusCode)
	}
	return nil
}

// Close releases idle HTTP connections.
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

// validateModel checks the request's ModelID is in our catalog
// and (when required) declares the needed capability. The capability
// check protects against the router mis-routing (e.g., sending an
// embed request to a chat-only model id).
func (a *Adapter) validateModel(id, requiredCapability string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, m := range a.catalog {
		if m.ID == id {
			if requiredCapability == "" {
				return nil
			}
			for _, c := range m.Capabilities {
				if c == requiredCapability {
					return nil
				}
			}
			return fmt.Errorf("model:ollama: model %s does not declare %s capability",
				id, requiredCapability)
		}
	}
	return fmt.Errorf("%w: %s", model.ErrUnknownModel, id)
}

func (a *Adapter) newRequest(ctx context.Context, path string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	return req, nil
}

// --- request/response shapes -------------------------------------------

type chatRequest struct {
	Model    string           `json:"model"`
	Messages []chatMessageReq `json:"messages"`
	Stream   bool             `json:"stream"`
	Options  *chatOptions     `json:"options,omitempty"`
}

type chatMessageReq struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
	Stop        []string `json:"stop,omitempty"`
}

type chatResponse struct {
	Model           string         `json:"model"`
	Message         chatMessageReq `json:"message"`
	Done            bool           `json:"done"`
	DoneReason      string         `json:"done_reason,omitempty"`
	PromptEvalCount int            `json:"prompt_eval_count"`
	EvalCount       int            `json:"eval_count"`
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Model           string      `json:"model"`
	Embeddings      [][]float32 `json:"embeddings"`
	PromptEvalCount int         `json:"prompt_eval_count"`
}

func buildChatBody(req model.GenerateRequest, stream bool) ([]byte, error) {
	out := chatRequest{
		Model:  req.ModelID,
		Stream: stream,
	}
	for _, msg := range req.Messages {
		out.Messages = append(out.Messages, chatMessageReq{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	if len(out.Messages) == 0 {
		return nil, errors.New("model:ollama: at least one message is required")
	}
	if req.Temperature > 0 || req.MaxTokens > 0 || len(req.Stop) > 0 {
		opts := &chatOptions{Stop: req.Stop}
		if req.Temperature > 0 {
			t := req.Temperature
			opts.Temperature = &t
		}
		if req.MaxTokens > 0 {
			n := req.MaxTokens
			opts.NumPredict = &n
		}
		out.Options = opts
	}
	return json.Marshal(out)
}

// parseNDJSONStream reads Ollama's NDJSON streaming format: one
// JSON object per line, each a chat-response chunk. Last chunk
// has done=true.
func parseNDJSONStream(ctx context.Context, r io.Reader, out chan<- model.GenerateChunk) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var chunk chatResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			// Ollama doesn't emit garbage in practice; log + skip.
			continue
		}
		if chunk.Message.Content != "" {
			select {
			case out <- model.GenerateChunk{Text: chunk.Message.Content}:
			case <-ctx.Done():
				return
			}
		}
		if chunk.Done {
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

// finishReasonFromDone maps Ollama's done/done_reason into the
// reason strings Loamss callers expect. Ollama uses "stop",
// "length", "load" (model swap) — pass through verbatim; default
// to "stop" when done=true and no reason given.
func finishReasonFromDone(reason string, done bool) string {
	if reason != "" {
		return reason
	}
	if done {
		return "stop"
	}
	return ""
}

func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	}
	return 0
}
