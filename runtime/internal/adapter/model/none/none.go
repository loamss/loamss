// Package none implements the model:none adapter — a no-op that
// fails every model operation with ErrModelDisabled. Selected by
// users who want Loamss without any model access; semantic memory
// and organizer-capsule functionality degrade gracefully via the
// ErrModelDisabled sentinel.
//
// Why have a registered adapter at all instead of "no adapter
// configured"? Two reasons. First, the runtime's startup path is
// simpler if it can always construct *some* adapter — the
// graceful-degradation logic lives in callers (memory.query
// returns an error citing ErrModelDisabled), not in conditional
// adapter wiring. Second, registering model:none as an explicit
// choice gives users a deliberate way to say "I've thought about
// this; no model access for now" — distinct from "I forgot to
// configure one."
package none

import (
	"context"

	"github.com/loamss/loamss/runtime/internal/adapter/model"
)

const adapterID = "model:none"

func init() {
	model.Register(adapterID, func() model.Adapter { return &Adapter{} })
}

// Adapter is the no-op model adapter. Zero value is usable after
// Init; Init itself does nothing (no backend to connect to).
type Adapter struct {
	inited bool
}

// Init no-ops successfully. The config map is ignored.
func (a *Adapter) Init(_ context.Context, _ map[string]any) error {
	a.inited = true
	return nil
}

// Models returns an empty slice — model:none advertises no models.
// The router will skip past this adapter on any non-trivial routing
// decision, which matches the "graceful degradation" intent.
func (a *Adapter) Models(_ context.Context) ([]model.Descriptor, error) {
	if !a.inited {
		return nil, model.ErrUninitialized
	}
	return nil, nil
}

// Generate returns ErrModelDisabled.
func (a *Adapter) Generate(_ context.Context, _ model.GenerateRequest) (*model.GenerateResponse, error) {
	if !a.inited {
		return nil, model.ErrUninitialized
	}
	return nil, model.ErrModelDisabled
}

// GenerateStream returns a channel that emits one error chunk and
// closes. Streams are typically harder for callers to handle than
// synchronous errors; we still go through the channel because
// breaking the API contract here (returning a non-nil error with a
// nil channel) would force every caller into special-case handling.
func (a *Adapter) GenerateStream(_ context.Context, _ model.GenerateRequest) (<-chan model.GenerateChunk, error) {
	if !a.inited {
		return nil, model.ErrUninitialized
	}
	ch := make(chan model.GenerateChunk, 1)
	ch <- model.GenerateChunk{Error: model.ErrModelDisabled}
	close(ch)
	return ch, nil
}

// Embed returns ErrModelDisabled.
func (a *Adapter) Embed(_ context.Context, _ model.EmbedRequest) (*model.EmbedResponse, error) {
	if !a.inited {
		return nil, model.ErrUninitialized
	}
	return nil, model.ErrModelDisabled
}

// EstimateCost returns zero cost, confident. Calling EstimateCost
// against a disabled adapter shouldn't happen in practice (the
// router filters first), but we don't fail loudly here — returning
// zero is honest: a disabled adapter costs nothing because it does
// nothing.
func (a *Adapter) EstimateCost(_ context.Context, _ model.GenerateRequest) (model.Cost, error) {
	return model.Cost{USD: 0, Confident: true}, nil
}

// HealthCheck always succeeds. The "backend" is the absence of one;
// it's always available.
func (a *Adapter) HealthCheck(_ context.Context) error { return nil }

// Close is a no-op.
func (a *Adapter) Close(_ context.Context) error { return nil }
