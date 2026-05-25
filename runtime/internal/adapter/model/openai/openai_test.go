package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/adapter/model"
)

// Tests for the model:openai adapter. Two tiers:
//
//   - PURE UNIT: drives the adapter against an httptest server that
//     pretends to be OpenAI. Covers config validation, request
//     building, response parsing, error mapping, streaming, embed
//     dispatch. Runs everywhere.
//
//   - INTEGRATION: hits real OpenAI when LOAMSS_OPENAI_API_KEY is
//     set. Skipped by default so CI doesn't burn quota.

// --- fake OpenAI server ----------------------------------------------------

type fakeOpenAI struct {
	srv  *httptest.Server
	auth string // expected bearer token

	// override hooks: if set, the corresponding endpoint uses
	// this body instead of the default response.
	chatBody   string
	streamBody string
	embedBody  string
	statusCode int
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	t.Helper()
	f := &fakeOpenAI{
		auth:       "Bearer test-key",
		statusCode: http.StatusOK,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", f.handleChat)
	mux.HandleFunc("/v1/embeddings", f.handleEmbed)
	mux.HandleFunc("/v1/models", f.handleModels)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpenAI) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != f.auth {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if f.statusCode != http.StatusOK {
		w.WriteHeader(f.statusCode)
		_, _ = w.Write([]byte(`{"error":"injected"}`))
		return
	}
	// Sniff stream flag.
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	stream, _ := body["stream"].(bool)
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		out := f.streamBody
		if out == "" {
			out = `data: {"choices":[{"delta":{"content":"Hello"}}]}` + "\n\n" +
				`data: {"choices":[{"delta":{"content":" world"}}]}` + "\n\n" +
				`data: [DONE]` + "\n\n"
		}
		_, _ = w.Write([]byte(out))
		if fw, ok := w.(http.Flusher); ok {
			fw.Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	out := f.chatBody
	if out == "" {
		out = `{
            "id": "chatcmpl-test",
            "model": "gpt-4o-mini",
            "choices": [{
                "index": 0,
                "finish_reason": "stop",
                "message": {"role": "assistant", "content": "Hello, world."}
            }],
            "usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
        }`
	}
	_, _ = w.Write([]byte(out))
}

func (f *fakeOpenAI) handleEmbed(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != f.auth {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if f.statusCode != http.StatusOK {
		w.WriteHeader(f.statusCode)
		_, _ = w.Write([]byte(`{"error":"injected"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	out := f.embedBody
	if out == "" {
		// 1536-dim small embedding, but we cheat — only emit
		// what the test asserts on (a non-empty prefix).
		vec := make([]string, 1536)
		for i := range vec {
			vec[i] = fmt.Sprintf("%v", float64(i)/1000)
		}
		out = fmt.Sprintf(`{
            "model": "text-embedding-3-small",
            "data": [{"embedding": [%s]}],
            "usage": {"prompt_tokens": 2, "total_tokens": 2}
        }`, strings.Join(vec, ","))
	}
	_, _ = w.Write([]byte(out))
}

func (f *fakeOpenAI) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != f.auth {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if f.statusCode != http.StatusOK {
		w.WriteHeader(f.statusCode)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
}

// --- helpers ---------------------------------------------------------------

func newAdapter(t *testing.T, f *fakeOpenAI) *Adapter {
	t.Helper()
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{
		"api_key":  "test-key",
		"base_url": f.srv.URL,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return a
}

// --- tests -----------------------------------------------------------------

func TestInit_RequiresAPIKey(t *testing.T) {
	a := &Adapter{}
	err := a.Init(context.Background(), map[string]any{"base_url": "https://x"})
	if err == nil {
		t.Fatal("Init without api_key should fail")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("error should mention api_key, got: %v", err)
	}
}

func TestUninited_RejectsCalls(t *testing.T) {
	a := &Adapter{}
	ctx := context.Background()

	if _, err := a.Models(ctx); !errors.Is(err, model.ErrUninitialized) {
		t.Errorf("Models without Init = %v, want ErrUninitialized", err)
	}
	if _, err := a.Generate(ctx, model.GenerateRequest{}); !errors.Is(err, model.ErrUninitialized) {
		t.Errorf("Generate without Init = %v, want ErrUninitialized", err)
	}
	if _, err := a.Embed(ctx, model.EmbedRequest{}); !errors.Is(err, model.ErrUninitialized) {
		t.Errorf("Embed without Init = %v, want ErrUninitialized", err)
	}
}

func TestModels_AdvertisesGenAndEmbedFamily(t *testing.T) {
	f := newFakeOpenAI(t)
	a := newAdapter(t, f)

	models, err := a.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}

	wantGen := []string{"gpt-4o", "gpt-4o-mini", "gpt-4-turbo"}
	wantEmbed := []string{"text-embedding-3-small", "text-embedding-3-large", "text-embedding-ada-002"}
	caps := map[string][]string{}
	for _, m := range models {
		caps[m.ID] = m.Capabilities
	}
	for _, id := range wantGen {
		caps, ok := caps[id]
		if !ok {
			t.Errorf("missing gen model: %s", id)
			continue
		}
		if !contains(caps, "text") {
			t.Errorf("%s should have 'text' capability", id)
		}
	}
	for _, id := range wantEmbed {
		caps, ok := caps[id]
		if !ok {
			t.Errorf("missing embed model: %s", id)
			continue
		}
		if !contains(caps, "embeddings") {
			t.Errorf("%s should have 'embeddings' capability", id)
		}
	}
}

func TestGenerate_RoundTrip(t *testing.T) {
	f := newFakeOpenAI(t)
	a := newAdapter(t, f)
	ctx := context.Background()

	resp, err := a.Generate(ctx, model.GenerateRequest{
		ModelID:  "gpt-4o-mini",
		Messages: []model.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "Hello, world." {
		t.Errorf("Text = %q", resp.Text)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q", resp.FinishReason)
	}
	if resp.InputTokens != 5 || resp.OutputTokens != 3 {
		t.Errorf("token usage = %d/%d, want 5/3", resp.InputTokens, resp.OutputTokens)
	}
}

func TestGenerate_RejectsEmbeddingModel(t *testing.T) {
	f := newFakeOpenAI(t)
	a := newAdapter(t, f)

	_, err := a.Generate(context.Background(), model.GenerateRequest{
		ModelID:  "text-embedding-3-small",
		Messages: []model.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Generate on embedding model should fail")
	}
	if !strings.Contains(err.Error(), "not a generative model") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGenerate_UpstreamError(t *testing.T) {
	f := newFakeOpenAI(t)
	f.statusCode = http.StatusTooManyRequests
	a := newAdapter(t, f)

	_, err := a.Generate(context.Background(), model.GenerateRequest{
		ModelID:  "gpt-4o-mini",
		Messages: []model.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Generate on 429 should error")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should mention 429, got: %v", err)
	}
}

func TestGenerateStream_Deltas(t *testing.T) {
	f := newFakeOpenAI(t)
	a := newAdapter(t, f)

	ch, err := a.GenerateStream(context.Background(), model.GenerateRequest{
		ModelID:  "gpt-4o-mini",
		Messages: []model.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	var assembled string
	doneSeen := false
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		if chunk.Done {
			doneSeen = true
			continue
		}
		assembled += chunk.Text
	}
	if !doneSeen {
		t.Error("stream did not emit a Done chunk")
	}
	if assembled != "Hello world" {
		t.Errorf("assembled = %q, want 'Hello world'", assembled)
	}
}

func TestEmbed_RoundTrip(t *testing.T) {
	f := newFakeOpenAI(t)
	a := newAdapter(t, f)

	resp, err := a.Embed(context.Background(), model.EmbedRequest{
		ModelID: "text-embedding-3-small",
		Text:    "hello",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Vector) != 1536 {
		t.Errorf("Vector length = %d, want 1536", len(resp.Vector))
	}
	if resp.Dimension != 1536 {
		t.Errorf("Dimension = %d", resp.Dimension)
	}
	if resp.InputTokens != 2 {
		t.Errorf("InputTokens = %d", resp.InputTokens)
	}
}

func TestEmbed_RejectsGenerativeModel(t *testing.T) {
	f := newFakeOpenAI(t)
	a := newAdapter(t, f)

	_, err := a.Embed(context.Background(), model.EmbedRequest{
		ModelID: "gpt-4o",
		Text:    "x",
	})
	if err == nil {
		t.Fatal("Embed on generative model should fail")
	}
	if !strings.Contains(err.Error(), "not an embedding model") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEmbed_RequiresText(t *testing.T) {
	f := newFakeOpenAI(t)
	a := newAdapter(t, f)

	_, err := a.Embed(context.Background(), model.EmbedRequest{
		ModelID: "text-embedding-3-small",
	})
	if err == nil {
		t.Fatal("Embed without text should fail")
	}
}

func TestHealthCheck(t *testing.T) {
	f := newFakeOpenAI(t)
	a := newAdapter(t, f)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestHealthCheck_AuthFailure(t *testing.T) {
	f := newFakeOpenAI(t)
	f.auth = "Bearer different-key"
	a := newAdapter(t, f)
	err := a.HealthCheck(context.Background())
	if err == nil {
		t.Error("HealthCheck with bad key should fail")
	}
}

func TestEstimateCost(t *testing.T) {
	f := newFakeOpenAI(t)
	a := newAdapter(t, f)

	cost, err := a.EstimateCost(context.Background(), model.GenerateRequest{
		ModelID:   "gpt-4o-mini",
		Messages:  []model.Message{{Role: "user", Content: strings.Repeat("x", 400)}},
		MaxTokens: 200,
	})
	if err != nil {
		t.Fatalf("EstimateCost: %v", err)
	}
	if cost.USD <= 0 {
		t.Errorf("USD = %v, want > 0", cost.USD)
	}
	if !cost.Confident {
		t.Errorf("Confident should be true for fixed-rate provider")
	}
}

func TestRegistryPickup(t *testing.T) {
	a, err := model.New(adapterID)
	if err != nil {
		t.Fatalf("model.New(%q): %v", adapterID, err)
	}
	if a == nil {
		t.Error("model.New returned nil")
	}
}

// --- integration (LOAMSS_OPENAI_API_KEY) -----------------------------------

func TestIntegration_GenerateAndEmbed(t *testing.T) {
	key := os.Getenv("LOAMSS_OPENAI_API_KEY")
	if key == "" {
		t.Skip("set LOAMSS_OPENAI_API_KEY to run against real OpenAI")
	}
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{"api_key": key}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })

	gen, err := a.Generate(context.Background(), model.GenerateRequest{
		ModelID:   "gpt-4o-mini",
		Messages:  []model.Message{{Role: "user", Content: "Say the single word 'pong'."}},
		MaxTokens: 5,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if gen.Text == "" {
		t.Error("Generate returned empty text")
	}

	emb, err := a.Embed(context.Background(), model.EmbedRequest{
		ModelID: "text-embedding-3-small",
		Text:    "hello world",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if emb.Dimension != 1536 {
		t.Errorf("Dimension = %d, want 1536", emb.Dimension)
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
