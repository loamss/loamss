package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/loamss/loamss/runtime/internal/adapter/model"
)

func fakeAPI(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func newAdapter(t *testing.T, srv *httptest.Server, config ...map[string]any) *Adapter {
	t.Helper()
	cfg := map[string]any{"base_url": srv.URL}
	if len(config) > 0 {
		for k, v := range config[0] {
			cfg[k] = v
		}
	}
	a := &Adapter{}
	if err := a.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return a
}

func TestInit_DefaultsApplied(t *testing.T) {
	srv := fakeAPI(t, func(_ http.ResponseWriter, _ *http.Request) {})
	a := newAdapter(t, srv)
	ms, err := a.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("expected 2 default models, got %d", len(ms))
	}
	// One chat model + one embedding model.
	foundChat, foundEmbed := false, false
	for _, m := range ms {
		for _, c := range m.Capabilities {
			if c == "text" {
				foundChat = true
			}
			if c == "embeddings" {
				foundEmbed = true
			}
		}
	}
	if !foundChat || !foundEmbed {
		t.Errorf("default catalog should cover both capabilities; got %+v", ms)
	}
}

func TestInit_CustomCatalog(t *testing.T) {
	srv := fakeAPI(t, func(_ http.ResponseWriter, _ *http.Request) {})
	a := newAdapter(t, srv, map[string]any{
		"models": []any{
			map[string]any{
				"id":           "qwen2.5-coder",
				"capabilities": []any{"text", "tool_use"},
				"max_tokens":   32768,
			},
			map[string]any{
				"id":            "mxbai-embed-large",
				"capabilities":  []any{"embeddings"},
				"embedding_dim": 1024,
			},
		},
	})
	ms, _ := a.Models(context.Background())
	if len(ms) != 2 {
		t.Fatalf("got %d models", len(ms))
	}
	if ms[0].ID != "qwen2.5-coder" || ms[0].MaxTokens != 32768 {
		t.Errorf("first model: %+v", ms[0])
	}
	if ms[1].EmbeddingDim != 1024 {
		t.Errorf("embed dim: %d", ms[1].EmbeddingDim)
	}
	// All declared as local (hosted=false).
	for _, m := range ms {
		if m.Hosted {
			t.Errorf("Ollama models should be hosted=false; %s shows hosted=true", m.ID)
		}
	}
}

func TestInit_RejectsBadCatalogShape(t *testing.T) {
	srv := fakeAPI(t, func(_ http.ResponseWriter, _ *http.Request) {})
	cases := []map[string]any{
		{"models": "not a list"},
		{"models": []any{"not a map"}},
		{"models": []any{map[string]any{ /* missing id */ "capabilities": []any{"text"}}}},
	}
	for i, cfg := range cases {
		cfg["base_url"] = srv.URL
		a := &Adapter{}
		if err := a.Init(context.Background(), cfg); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestGenerate_HappyPath(t *testing.T) {
	var capturedBody []byte
	srv := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path: %s", r.URL.Path)
		}
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
            "model": "llama3.2",
            "created_at": "2026-01-01T00:00:00Z",
            "message": {"role": "assistant", "content": "hi from llama"},
            "done": true,
            "prompt_eval_count": 8,
            "eval_count": 4
        }`))
	})
	a := newAdapter(t, srv)
	resp, err := a.Generate(context.Background(), model.GenerateRequest{
		ModelID:   defaultChatModel,
		MaxTokens: 100,
		Messages: []model.Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "say hi"},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "hi from llama" {
		t.Errorf("text: %q", resp.Text)
	}
	if resp.InputTokens != 8 || resp.OutputTokens != 4 {
		t.Errorf("usage: input=%d output=%d", resp.InputTokens, resp.OutputTokens)
	}

	// Verify body shape: Ollama accepts role=system inside messages
	// (unlike Anthropic), so we don't need system extraction.
	var body chatRequest
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Messages) != 2 || body.Messages[0].Role != "system" {
		t.Errorf("messages: %+v", body.Messages)
	}
	if body.Stream {
		t.Error("stream should be false for non-streaming Generate")
	}
	if body.Options == nil || body.Options.NumPredict == nil || *body.Options.NumPredict != 100 {
		t.Errorf("options/num_predict not propagated: %+v", body.Options)
	}
}

func TestGenerate_RejectsModelLackingTextCapability(t *testing.T) {
	srv := fakeAPI(t, func(_ http.ResponseWriter, _ *http.Request) {})
	a := newAdapter(t, srv)
	// nomic-embed-text only declares "embeddings"; Generate should reject.
	_, err := a.Generate(context.Background(), model.GenerateRequest{
		ModelID:  defaultEmbedModel,
		Messages: []model.Message{{Role: "user", Content: "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "does not declare text capability") {
		t.Errorf("expected capability error, got: %v", err)
	}
}

func TestEmbed_HappyPath(t *testing.T) {
	srv := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path: %s", r.URL.Path)
		}
		var body embedRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		// Echo a deterministic 4-dim vector.
		_, _ = w.Write([]byte(`{
            "model": "nomic-embed-text",
            "embeddings": [[0.1, 0.2, 0.3, 0.4]],
            "prompt_eval_count": 7
        }`))
	})
	a := newAdapter(t, srv)
	resp, err := a.Embed(context.Background(), model.EmbedRequest{
		ModelID: defaultEmbedModel,
		Text:    "hello world",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Vector) != 4 {
		t.Errorf("vector length: %d", len(resp.Vector))
	}
	if resp.Vector[0] != 0.1 {
		t.Errorf("vector content: %v", resp.Vector)
	}
	if resp.Dimension != 4 {
		t.Errorf("dimension: %d", resp.Dimension)
	}
	if resp.InputTokens != 7 {
		t.Errorf("input tokens: %d", resp.InputTokens)
	}
}

func TestEmbed_RejectsTextModel(t *testing.T) {
	srv := fakeAPI(t, func(_ http.ResponseWriter, _ *http.Request) {})
	a := newAdapter(t, srv)
	// llama3.2 has "text", not "embeddings". Embed must reject.
	_, err := a.Embed(context.Background(), model.EmbedRequest{
		ModelID: defaultChatModel,
		Text:    "x",
	})
	if err == nil || !strings.Contains(err.Error(), "embeddings capability") {
		t.Errorf("expected capability error, got: %v", err)
	}
}

func TestGenerateStream_NDJSONParsing(t *testing.T) {
	srv := fakeAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/x-ndjson")
		flusher := w.(http.Flusher)
		emit := func(s string) {
			_, _ = w.Write([]byte(s + "\n"))
			flusher.Flush()
		}
		emit(`{"model":"llama3.2","message":{"role":"assistant","content":"hello "},"done":false}`)
		emit(`{"model":"llama3.2","message":{"role":"assistant","content":"streamed "},"done":false}`)
		emit(`{"model":"llama3.2","message":{"role":"assistant","content":"local"},"done":false}`)
		emit(`{"model":"llama3.2","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":3}`)
	})
	a := newAdapter(t, srv)
	ch, err := a.GenerateStream(context.Background(), model.GenerateRequest{
		ModelID:  defaultChatModel,
		Messages: []model.Message{{Role: "user", Content: "x"}},
	})
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	var assembled strings.Builder
	sawDone := false
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("chunk error: %v", chunk.Error)
		}
		assembled.WriteString(chunk.Text)
		if chunk.Done {
			sawDone = true
		}
	}
	if !sawDone {
		t.Error("expected Done chunk")
	}
	if assembled.String() != "hello streamed local" {
		t.Errorf("assembled: %q", assembled.String())
	}
}

func TestHealthCheck_PingsTags(t *testing.T) {
	var pinged atomic.Bool
	srv := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			pinged.Store(true)
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		t.Errorf("HealthCheck should hit /api/tags, got %s", r.URL.Path)
	})
	a := newAdapter(t, srv)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
	if !pinged.Load() {
		t.Error("HealthCheck did not reach /api/tags")
	}
}

func TestEstimateCost_AlwaysZero(t *testing.T) {
	srv := fakeAPI(t, func(_ http.ResponseWriter, _ *http.Request) {})
	a := newAdapter(t, srv)
	cost, err := a.EstimateCost(context.Background(), model.GenerateRequest{
		ModelID:   defaultChatModel,
		MaxTokens: 1000,
		Messages:  []model.Message{{Role: "user", Content: strings.Repeat("x", 10000)}},
	})
	if err != nil {
		t.Fatalf("EstimateCost: %v", err)
	}
	if cost.USD != 0 {
		t.Errorf("local model cost should be zero, got: %v", cost.USD)
	}
	if !cost.Confident {
		t.Error("zero-cost should be confident")
	}
}

func TestGenerate_PropagatesNon200(t *testing.T) {
	srv := fakeAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`model not pulled`))
	})
	a := newAdapter(t, srv)
	_, err := a.Generate(context.Background(), model.GenerateRequest{
		ModelID:  defaultChatModel,
		Messages: []model.Message{{Role: "user", Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected error on non-200")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestBeforeInit_ReturnsUninitialized(t *testing.T) {
	a := &Adapter{}
	if _, err := a.Models(context.Background()); !errors.Is(err, model.ErrUninitialized) {
		t.Errorf("Models: %v", err)
	}
	if _, err := a.Generate(context.Background(), model.GenerateRequest{}); !errors.Is(err, model.ErrUninitialized) {
		t.Errorf("Generate: %v", err)
	}
	if _, err := a.Embed(context.Background(), model.EmbedRequest{}); !errors.Is(err, model.ErrUninitialized) {
		t.Errorf("Embed: %v", err)
	}
}

func TestRegisteredViaInit(t *testing.T) {
	a, err := model.New("model:ollama")
	if err != nil {
		model.Register("model:ollama", func() model.Adapter { return &Adapter{} })
		a, err = model.New("model:ollama")
	}
	if err != nil {
		t.Fatalf("model.New: %v", err)
	}
	if a == nil {
		t.Fatal("got nil adapter")
	}
}
