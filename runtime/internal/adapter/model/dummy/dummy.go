// Package dummy implements the model:dummy adapter — a deterministic
// synthetic model useful for tests and local development demos.
// Embeddings are computed as a hash projection of the input text;
// Generate echoes the prompt with a fixed prefix. The output is
// useless for real semantic work but stable enough to let tests
// pin behavior end-to-end without a network call to a real
// provider.
//
// model:dummy is NOT the default adapter (model:none is). Users
// who want semantic memory for testing select dummy explicitly in
// their config.
package dummy

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/loamss/loamss/runtime/internal/adapter/model"
)

const adapterID = "model:dummy"

// defaultDimension is the embedding dimension this adapter produces
// when the user doesn't override it via config. Chosen small enough
// for fast tests but large enough to behave like a real embedding
// space (cosine similarity discriminates between inputs).
const defaultDimension = 16

// defaultGenerateModel is the model id model:dummy advertises for
// generate. defaultEmbedModel does the same for embeddings. Real
// adapters might advertise dozens of models; dummy advertises one
// per capability.
const (
	defaultGenerateModel = "dummy-generate"
	defaultEmbedModel    = "dummy-embed"
)

func init() {
	model.Register(adapterID, func() model.Adapter { return &Adapter{} })
}

// Adapter is the model:dummy concrete adapter.
type Adapter struct {
	inited        bool
	dimension     int
	embedModel    string
	generateModel string
}

// Init reads optional config:
//
//	dimension:       int — override the default 16-dim embedding
//	embed_model:     string — override the advertised embedding model id
//	generate_model:  string — override the advertised generate model id
//
// All fields are optional; absent fields fall through to defaults.
// The config map type-checking is permissive: int vs float64 (YAML
// unmarshal quirk) is accepted for the dimension field.
func (a *Adapter) Init(_ context.Context, config map[string]any) error {
	a.dimension = defaultDimension
	a.embedModel = defaultEmbedModel
	a.generateModel = defaultGenerateModel
	if v, ok := config["dimension"]; ok {
		switch t := v.(type) {
		case int:
			a.dimension = t
		case int64:
			a.dimension = int(t)
		case float64:
			a.dimension = int(t)
		default:
			return fmt.Errorf("model:dummy: dimension must be int, got %T", v)
		}
		if a.dimension <= 0 {
			return fmt.Errorf("model:dummy: dimension must be > 0, got %d", a.dimension)
		}
	}
	if v, ok := config["embed_model"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("model:dummy: embed_model must be string, got %T", v)
		}
		a.embedModel = s
	}
	if v, ok := config["generate_model"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("model:dummy: generate_model must be string, got %T", v)
		}
		a.generateModel = s
	}
	a.inited = true
	return nil
}

// Models advertises two models — one for generation, one for
// embeddings — so callers can dispatch on capability. The embed
// model declares its dimension so callers (and the router) can
// verify compatibility with the configured memory adapter.
func (a *Adapter) Models(_ context.Context) ([]model.Descriptor, error) {
	if !a.inited {
		return nil, model.ErrUninitialized
	}
	return []model.Descriptor{
		{
			ID:           a.generateModel,
			Capabilities: []string{"text"},
			MaxTokens:    8192,
			Hosted:       false,
		},
		{
			ID:           a.embedModel,
			Capabilities: []string{"embeddings"},
			EmbeddingDim: a.dimension,
			Hosted:       false,
		},
	}, nil
}

// Generate echoes the user's final message back with a deterministic
// prefix. Used to verify the dispatch chain end-to-end without a
// real provider.
func (a *Adapter) Generate(_ context.Context, req model.GenerateRequest) (*model.GenerateResponse, error) {
	if !a.inited {
		return nil, model.ErrUninitialized
	}
	if req.ModelID != a.generateModel {
		return nil, fmt.Errorf("%w: %s", model.ErrUnknownModel, req.ModelID)
	}
	// Find the last user message; default to empty.
	last := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			last = req.Messages[i].Content
			break
		}
	}
	return &model.GenerateResponse{
		Text:         "[dummy] " + last,
		ModelID:      a.generateModel,
		FinishReason: "stop",
		InputTokens:  len(last) / 4, // pretend ~4 chars per token
		OutputTokens: (len("[dummy] ") + len(last)) / 4,
	}, nil
}

// GenerateStream chunks the synthetic response into ~16-char pieces
// so streaming consumers can observe more than one chunk. Last chunk
// has Done=true; channel closes after.
func (a *Adapter) GenerateStream(ctx context.Context, req model.GenerateRequest) (<-chan model.GenerateChunk, error) {
	resp, err := a.Generate(ctx, req)
	if err != nil {
		return nil, err
	}
	const chunkSize = 16
	ch := make(chan model.GenerateChunk)
	go func() {
		defer close(ch)
		text := resp.Text
		for i := 0; i < len(text); i += chunkSize {
			end := i + chunkSize
			if end > len(text) {
				end = len(text)
			}
			select {
			case <-ctx.Done():
				return
			case ch <- model.GenerateChunk{Text: text[i:end]}:
			}
		}
		select {
		case <-ctx.Done():
		case ch <- model.GenerateChunk{Done: true}:
		}
	}()
	return ch, nil
}

// Embed produces a deterministic vector from the input text. The
// algorithm: SHA-256 the input, then unpack the digest into a
// fixed-length float32 vector by taking 4-byte chunks as little-
// endian uint32 and normalizing to [-1, 1]. Same input → same
// vector across runs; slightly different inputs produce slightly
// different vectors (avalanche from SHA-256).
//
// The result is suitable for verifying the embed→search round-trip
// works; it's NOT suitable for actual semantic similarity. Cosine
// similarity between two dummy embeddings reflects the SHA-256
// digest distance, not meaning.
func (a *Adapter) Embed(_ context.Context, req model.EmbedRequest) (*model.EmbedResponse, error) {
	if !a.inited {
		return nil, model.ErrUninitialized
	}
	if req.ModelID != a.embedModel {
		return nil, fmt.Errorf("%w: %s", model.ErrUnknownModel, req.ModelID)
	}
	vec := embedText(req.Text, a.dimension)
	return &model.EmbedResponse{
		Vector:      vec,
		ModelID:     a.embedModel,
		InputTokens: len(req.Text) / 4,
		Dimension:   a.dimension,
	}, nil
}

// embedText is the deterministic embedding kernel. Exported via tests
// only; production callers go through Embed.
func embedText(text string, dim int) []float32 {
	// Normalize so trivial whitespace differences don't change the
	// vector — tiny ergonomic win for testing.
	normalized := strings.ToLower(strings.TrimSpace(text))
	out := make([]float32, dim)
	// Seed: SHA-256 of input. Re-hash with an incrementing salt to
	// stretch beyond 32 bytes when dim is large.
	for i := 0; i < dim; i++ {
		salt := []byte{byte(i & 0xff), byte((i >> 8) & 0xff)}
		buf := append(append([]byte{}, salt...), []byte(normalized)...)
		sum := sha256.Sum256(buf)
		// Take the first 4 bytes of the salted hash, interpret as
		// uint32, map to [-1, 1).
		u := binary.LittleEndian.Uint32(sum[:4])
		// 2^32 is the range; subtract 1 to shift to [-1, 1).
		out[i] = float32(u)/float32(1<<31) - 1
	}
	return out
}

// EstimateCost returns zero, confident. Local synthetic; nothing
// costs anything.
func (a *Adapter) EstimateCost(_ context.Context, _ model.GenerateRequest) (model.Cost, error) {
	return model.Cost{USD: 0, Confident: true}, nil
}

// HealthCheck always succeeds; there's no backend to reach.
func (a *Adapter) HealthCheck(_ context.Context) error { return nil }

// Close is a no-op.
func (a *Adapter) Close(_ context.Context) error { return nil }
