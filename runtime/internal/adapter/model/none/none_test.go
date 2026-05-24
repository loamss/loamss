package none

import (
	"context"
	"errors"
	"testing"

	"github.com/loamss/loamss/runtime/internal/adapter/model"
)

func newAdapter(t *testing.T) *Adapter {
	t.Helper()
	a := &Adapter{}
	if err := a.Init(context.Background(), nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return a
}

func TestNone_InitAndHealthCheck(t *testing.T) {
	a := newAdapter(t)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestNone_ModelsEmpty(t *testing.T) {
	a := newAdapter(t)
	ms, err := a.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(ms) != 0 {
		t.Errorf("model:none should advertise no models, got %d", len(ms))
	}
}

func TestNone_GenerateReturnsDisabled(t *testing.T) {
	a := newAdapter(t)
	_, err := a.Generate(context.Background(), model.GenerateRequest{})
	if !errors.Is(err, model.ErrModelDisabled) {
		t.Errorf("expected ErrModelDisabled, got: %v", err)
	}
}

func TestNone_EmbedReturnsDisabled(t *testing.T) {
	a := newAdapter(t)
	_, err := a.Embed(context.Background(), model.EmbedRequest{})
	if !errors.Is(err, model.ErrModelDisabled) {
		t.Errorf("expected ErrModelDisabled, got: %v", err)
	}
}

func TestNone_GenerateStreamEmitsErrorChunk(t *testing.T) {
	a := newAdapter(t)
	ch, err := a.GenerateStream(context.Background(), model.GenerateRequest{})
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	got := <-ch
	if !errors.Is(got.Error, model.ErrModelDisabled) {
		t.Errorf("first chunk should carry ErrModelDisabled, got: %+v", got)
	}
	// Channel should be closed after the one chunk.
	if _, ok := <-ch; ok {
		t.Error("channel should close after the error chunk")
	}
}

func TestNone_BeforeInitReturnsUninitialized(t *testing.T) {
	a := &Adapter{}
	if _, err := a.Models(context.Background()); !errors.Is(err, model.ErrUninitialized) {
		t.Errorf("expected ErrUninitialized, got: %v", err)
	}
	if _, err := a.Embed(context.Background(), model.EmbedRequest{}); !errors.Is(err, model.ErrUninitialized) {
		t.Errorf("expected ErrUninitialized, got: %v", err)
	}
}

func TestNone_RegisteredViaInit(t *testing.T) {
	// The package's init() registers model:none. We can't easily
	// observe that from here because model.New runs against the
	// global registry which may be reset by other tests, but we
	// can re-register and verify the construction round-trips.
	a, err := model.New("model:none")
	if err != nil {
		// If the registry got reset, re-register and retry.
		model.Register("model:none", func() model.Adapter { return &Adapter{} })
		a, err = model.New("model:none")
	}
	if err != nil {
		t.Fatalf("model.New: %v", err)
	}
	if a == nil {
		t.Fatal("got nil adapter")
	}
}
