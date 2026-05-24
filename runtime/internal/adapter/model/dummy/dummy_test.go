package dummy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/adapter/model"
)

func newDummy(t *testing.T, config map[string]any) *Adapter {
	t.Helper()
	a := &Adapter{}
	if err := a.Init(context.Background(), config); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return a
}

func TestDummy_InitDefaults(t *testing.T) {
	a := newDummy(t, nil)
	if a.dimension != defaultDimension {
		t.Errorf("default dimension: got %d, want %d", a.dimension, defaultDimension)
	}
	if a.embedModel != defaultEmbedModel {
		t.Errorf("default embed_model: got %q", a.embedModel)
	}
}

func TestDummy_InitOverrides(t *testing.T) {
	a := newDummy(t, map[string]any{
		"dimension":      32,
		"embed_model":    "custom-embed",
		"generate_model": "custom-gen",
	})
	if a.dimension != 32 {
		t.Errorf("dimension: got %d, want 32", a.dimension)
	}
	if a.embedModel != "custom-embed" || a.generateModel != "custom-gen" {
		t.Errorf("model overrides: embed=%q generate=%q", a.embedModel, a.generateModel)
	}
}

func TestDummy_InitRejectsBadConfig(t *testing.T) {
	cases := []map[string]any{
		{"dimension": "sixteen"},
		{"dimension": 0},
		{"dimension": -3},
		{"embed_model": 7},
		{"generate_model": []int{1}},
	}
	for i, c := range cases {
		a := &Adapter{}
		if err := a.Init(context.Background(), c); err == nil {
			t.Errorf("case %d: expected error, got nil. config=%+v", i, c)
		}
	}
}

func TestDummy_Models(t *testing.T) {
	a := newDummy(t, nil)
	ms, err := a.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("want 2 models, got %d", len(ms))
	}
	// Embedding model should declare dim and the "embeddings" capability.
	var foundEmbed bool
	for _, m := range ms {
		if m.ID == defaultEmbedModel {
			foundEmbed = true
			if m.EmbeddingDim != defaultDimension {
				t.Errorf("EmbeddingDim: got %d, want %d", m.EmbeddingDim, defaultDimension)
			}
			caps := strings.Join(m.Capabilities, ",")
			if !strings.Contains(caps, "embeddings") {
				t.Errorf("embed model missing 'embeddings' capability: %v", m.Capabilities)
			}
		}
	}
	if !foundEmbed {
		t.Error("did not find embed model in Models()")
	}
}

func TestDummy_Generate_EchoesLastUserMessage(t *testing.T) {
	a := newDummy(t, nil)
	resp, err := a.Generate(context.Background(), model.GenerateRequest{
		ModelID: defaultGenerateModel,
		Messages: []model.Message{
			{Role: "system", Content: "you are a test"},
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "final question"},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "[dummy] final question" {
		t.Errorf("text: %q", resp.Text)
	}
	if resp.ModelID != defaultGenerateModel {
		t.Errorf("model_id: %q", resp.ModelID)
	}
}

func TestDummy_Generate_RejectsUnknownModel(t *testing.T) {
	a := newDummy(t, nil)
	_, err := a.Generate(context.Background(), model.GenerateRequest{ModelID: "made-up"})
	if !errors.Is(err, model.ErrUnknownModel) {
		t.Errorf("expected ErrUnknownModel, got: %v", err)
	}
}

func TestDummy_Embed_Deterministic(t *testing.T) {
	a := newDummy(t, nil)
	v1, err := a.Embed(context.Background(), model.EmbedRequest{
		ModelID: defaultEmbedModel, Text: "hello world",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	v2, _ := a.Embed(context.Background(), model.EmbedRequest{
		ModelID: defaultEmbedModel, Text: "hello world",
	})
	if len(v1.Vector) != defaultDimension {
		t.Errorf("dimension: got %d, want %d", len(v1.Vector), defaultDimension)
	}
	for i := range v1.Vector {
		if v1.Vector[i] != v2.Vector[i] {
			t.Errorf("non-deterministic at index %d: %f vs %f", i, v1.Vector[i], v2.Vector[i])
			break
		}
	}
}

func TestDummy_Embed_DifferentInputsDifferentVectors(t *testing.T) {
	a := newDummy(t, nil)
	ctx := context.Background()
	v1, _ := a.Embed(ctx, model.EmbedRequest{ModelID: defaultEmbedModel, Text: "alpha"})
	v2, _ := a.Embed(ctx, model.EmbedRequest{ModelID: defaultEmbedModel, Text: "beta"})
	same := true
	for i := range v1.Vector {
		if v1.Vector[i] != v2.Vector[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("different inputs should produce different vectors")
	}
}

func TestDummy_Embed_NormalizesTrivialDifferences(t *testing.T) {
	a := newDummy(t, nil)
	ctx := context.Background()
	v1, _ := a.Embed(ctx, model.EmbedRequest{ModelID: defaultEmbedModel, Text: "  Hello  "})
	v2, _ := a.Embed(ctx, model.EmbedRequest{ModelID: defaultEmbedModel, Text: "hello"})
	for i := range v1.Vector {
		if v1.Vector[i] != v2.Vector[i] {
			t.Errorf("case+whitespace difference shouldn't change vector; diff at index %d", i)
			break
		}
	}
}

func TestDummy_Embed_RejectsUnknownModel(t *testing.T) {
	a := newDummy(t, nil)
	_, err := a.Embed(context.Background(), model.EmbedRequest{ModelID: "bad", Text: "x"})
	if !errors.Is(err, model.ErrUnknownModel) {
		t.Errorf("expected ErrUnknownModel, got: %v", err)
	}
}

func TestDummy_GenerateStream(t *testing.T) {
	a := newDummy(t, nil)
	ch, err := a.GenerateStream(context.Background(), model.GenerateRequest{
		ModelID:  defaultGenerateModel,
		Messages: []model.Message{{Role: "user", Content: "stream this please"}},
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
	if !strings.Contains(assembled.String(), "stream this please") {
		t.Errorf("assembled stream missing user content: %q", assembled.String())
	}
}

func TestDummy_BeforeInitReturnsUninitialized(t *testing.T) {
	a := &Adapter{}
	if _, err := a.Models(context.Background()); !errors.Is(err, model.ErrUninitialized) {
		t.Errorf("Models: expected ErrUninitialized, got: %v", err)
	}
	if _, err := a.Embed(context.Background(), model.EmbedRequest{}); !errors.Is(err, model.ErrUninitialized) {
		t.Errorf("Embed: expected ErrUninitialized, got: %v", err)
	}
}

func TestDummy_RegisteredViaInit(t *testing.T) {
	a, err := model.New("model:dummy")
	if err != nil {
		model.Register("model:dummy", func() model.Adapter { return &Adapter{} })
		a, err = model.New("model:dummy")
	}
	if err != nil {
		t.Fatalf("model.New: %v", err)
	}
	if a == nil {
		t.Fatal("got nil adapter")
	}
}
