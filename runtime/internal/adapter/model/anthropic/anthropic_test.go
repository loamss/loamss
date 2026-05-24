package anthropic

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

// fakeAPI returns an httptest.Server that responds with the supplied
// status + body to every request. Used by tests that don't need to
// inspect the request shape.
func fakeAPI(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newAdapter initializes a fresh adapter bound to the given httptest
// server, with a placeholder API key.
func newAdapter(t *testing.T, srv *httptest.Server) *Adapter {
	t.Helper()
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{
		"api_key":  "test-key",
		"base_url": srv.URL,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return a
}

func TestInit_RequiresAPIKey(t *testing.T) {
	a := &Adapter{}
	if err := a.Init(context.Background(), nil); err == nil {
		t.Error("expected error when api_key is missing")
	}
	if err := a.Init(context.Background(), map[string]any{"api_key": ""}); err == nil {
		t.Error("expected error when api_key is empty")
	}
}

func TestModels_AdvertisesCatalog(t *testing.T) {
	srv := fakeAPI(t, 200, "{}")
	a := newAdapter(t, srv)
	ms, err := a.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	// Three models expected (sonnet, opus, haiku).
	if len(ms) != 3 {
		t.Errorf("want 3 models, got %d", len(ms))
	}
	// All should declare "text" capability and be hosted.
	for _, m := range ms {
		if !contains(m.Capabilities, "text") {
			t.Errorf("model %s missing text capability", m.ID)
		}
		if !m.Hosted {
			t.Errorf("model %s should be hosted=true", m.ID)
		}
	}
	// None should advertise embeddings — that's the whole point of
	// this adapter not supporting Embed.
	for _, m := range ms {
		if contains(m.Capabilities, "embeddings") {
			t.Errorf("model %s should NOT advertise embeddings", m.ID)
		}
	}
}

func TestGenerate_HappyPath(t *testing.T) {
	// Inspect the outbound request to verify the wire shape.
	var capturedBody []byte
	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
            "id": "msg_test",
            "type": "message",
            "role": "assistant",
            "content": [{"type": "text", "text": "hello from claude"}],
            "model": "claude-sonnet-4-5",
            "stop_reason": "end_turn",
            "usage": {"input_tokens": 8, "output_tokens": 4}
        }`))
	}))
	defer srv.Close()

	a := newAdapter(t, srv)
	resp, err := a.Generate(context.Background(), model.GenerateRequest{
		ModelID:   "claude-sonnet-4-5",
		MaxTokens: 100,
		Messages: []model.Message{
			{Role: "system", Content: "you are a test"},
			{Role: "user", Content: "say hi"},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "hello from claude" {
		t.Errorf("text: %q", resp.Text)
	}
	if resp.FinishReason != "end_turn" {
		t.Errorf("finish_reason: %q", resp.FinishReason)
	}
	if resp.InputTokens != 8 || resp.OutputTokens != 4 {
		t.Errorf("usage: input=%d output=%d", resp.InputTokens, resp.OutputTokens)
	}

	// Verify headers.
	if capturedHeaders.Get("x-api-key") != "test-key" {
		t.Errorf("x-api-key header missing or wrong: %q", capturedHeaders.Get("x-api-key"))
	}
	if capturedHeaders.Get("anthropic-version") != defaultAPIVersion {
		t.Errorf("anthropic-version: %q", capturedHeaders.Get("anthropic-version"))
	}

	// Verify body shape — system extracted, user message preserved.
	var body messagesRequest
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.System != "you are a test" {
		t.Errorf("system extracted: %q", body.System)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" {
		t.Errorf("messages: %+v", body.Messages)
	}
	if body.Stream {
		t.Error("stream should be false for Generate")
	}
}

func TestGenerate_RejectsUnknownModel(t *testing.T) {
	srv := fakeAPI(t, 200, "{}")
	a := newAdapter(t, srv)
	_, err := a.Generate(context.Background(), model.GenerateRequest{
		ModelID:  "made-up-model",
		Messages: []model.Message{{Role: "user", Content: "x"}},
	})
	if !errors.Is(err, model.ErrUnknownModel) {
		t.Errorf("expected ErrUnknownModel, got: %v", err)
	}
}

func TestGenerate_PropagatesNon200Status(t *testing.T) {
	srv := fakeAPI(t, http.StatusUnauthorized, `{"error":"invalid api key"}`)
	a := newAdapter(t, srv)
	_, err := a.Generate(context.Background(), model.GenerateRequest{
		ModelID:  "claude-sonnet-4-5",
		Messages: []model.Message{{Role: "user", Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected error on non-200")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestGenerate_RequiresAtLeastOneUserMessage(t *testing.T) {
	srv := fakeAPI(t, 200, "{}")
	a := newAdapter(t, srv)
	_, err := a.Generate(context.Background(), model.GenerateRequest{
		ModelID:  "claude-sonnet-4-5",
		Messages: []model.Message{{Role: "system", Content: "no user message"}},
	})
	if err == nil {
		t.Error("expected error when only system messages are present")
	}
}

func TestEmbed_AlwaysNotSupported(t *testing.T) {
	srv := fakeAPI(t, 200, "{}")
	a := newAdapter(t, srv)
	_, err := a.Embed(context.Background(), model.EmbedRequest{
		ModelID: "anything",
		Text:    "x",
	})
	if !errors.Is(err, model.ErrEmbedNotSupported) {
		t.Errorf("expected ErrEmbedNotSupported, got: %v", err)
	}
}

func TestGenerateStream_HappyPath(t *testing.T) {
	// Build a realistic SSE stream. Anthropic interleaves event:
	// and data: lines with blank separators.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept header: %q", r.Header.Get("Accept"))
		}
		w.Header().Set("content-type", "text/event-stream")
		flusher := w.(http.Flusher)
		// Three text deltas + message_stop.
		emit := func(eventName, data string) {
			_, _ = w.Write([]byte("event: " + eventName + "\n"))
			_, _ = w.Write([]byte("data: " + data + "\n\n"))
			flusher.Flush()
		}
		emit("message_start", `{"type":"message_start"}`)
		emit("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		emit("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}`)
		emit("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"streamed "}}`)
		emit("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`)
		emit("content_block_stop", `{"type":"content_block_stop","index":0}`)
		emit("message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	a := newAdapter(t, srv)
	ch, err := a.GenerateStream(context.Background(), model.GenerateRequest{
		ModelID:  "claude-sonnet-4-5",
		Messages: []model.Message{{Role: "user", Content: "say hi"}},
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
		if chunk.Done {
			sawDone = true
		}
		assembled.WriteString(chunk.Text)
	}
	if !sawDone {
		t.Error("expected message_stop → Done chunk")
	}
	if assembled.String() != "hello streamed world" {
		t.Errorf("assembled: %q", assembled.String())
	}
}

func TestHealthCheck_PingsGenerate(t *testing.T) {
	var pinged atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pinged.Store(true)
		if r.URL.Path != "/v1/messages" {
			t.Errorf("HealthCheck should hit /v1/messages, got: %s", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
            "id": "msg_health",
            "type": "message",
            "role": "assistant",
            "content": [{"type": "text", "text": "."}],
            "model": "claude-haiku-4-5",
            "stop_reason": "end_turn",
            "usage": {"input_tokens": 1, "output_tokens": 1}
        }`))
	}))
	defer srv.Close()

	a := newAdapter(t, srv)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
	if !pinged.Load() {
		t.Error("HealthCheck did not reach the server")
	}
}

func TestEstimateCost_RoughOrderOfMagnitude(t *testing.T) {
	srv := fakeAPI(t, 200, "{}")
	a := newAdapter(t, srv)
	// 4000 chars of prompt ≈ 1000 input tokens. MaxTokens 100 output.
	// Sonnet: input 3.0/M, output 15.0/M. Expected cost ~= (1000*3.0 + 100*15.0)/1M = ~$0.0045
	cost, err := a.EstimateCost(context.Background(), model.GenerateRequest{
		ModelID:   "claude-sonnet-4-5",
		MaxTokens: 100,
		Messages:  []model.Message{{Role: "user", Content: strings.Repeat("x", 4000)}},
	})
	if err != nil {
		t.Fatalf("EstimateCost: %v", err)
	}
	if cost.USD < 0.001 || cost.USD > 0.01 {
		t.Errorf("cost out of expected range $0.001-$0.01: %v", cost.USD)
	}
	if !cost.Confident {
		t.Error("Anthropic per-token pricing should be Confident=true")
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
	a, err := model.New("model:anthropic")
	if err != nil {
		model.Register("model:anthropic", func() model.Adapter { return &Adapter{} })
		a, err = model.New("model:anthropic")
	}
	if err != nil {
		t.Fatalf("model.New: %v", err)
	}
	if a == nil {
		t.Fatal("got nil adapter")
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
