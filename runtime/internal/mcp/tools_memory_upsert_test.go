package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	memlayer "github.com/loamss/loamss/runtime/internal/memory"
)

// fakeLayer captures Upsert calls for assertions. Only implements
// the methods the upsert tool's handler touches; the rest panic so
// any accidental use is loud.
type fakeLayer struct {
	mu      sync.Mutex
	upserts []memlayer.Entry
	failErr error
}

func (f *fakeLayer) Upsert(_ context.Context, e memlayer.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return f.failErr
	}
	f.upserts = append(f.upserts, e)
	return nil
}

func (f *fakeLayer) Delete(_ context.Context, _, _ string) error {
	panic("fakeLayer.Delete: not used by these tests")
}
func (f *fakeLayer) ListEntities(_ context.Context, _ memlayer.EntityFilter) ([]memlayer.Entity, error) {
	panic("ListEntities: not used")
}
func (f *fakeLayer) GetEntity(_ context.Context, _ string) (*memlayer.Entity, error) {
	panic("GetEntity: not used")
}
func (f *fakeLayer) EntriesByEntity(_ context.Context, _ string, _ int) ([]memlayer.EntryRef, error) {
	panic("EntriesByEntity: not used")
}
func (f *fakeLayer) ListThreads(_ context.Context, _ memlayer.ThreadFilter) ([]memlayer.Thread, error) {
	panic("ListThreads: not used")
}
func (f *fakeLayer) GetThread(_ context.Context, _ string) (*memlayer.Thread, error) {
	panic("GetThread: not used")
}
func (f *fakeLayer) EntriesByThread(_ context.Context, _ string, _ int) ([]memlayer.EntryRef, error) {
	panic("EntriesByThread: not used")
}
func (f *fakeLayer) Close() error { return nil }

func TestMemoryUpsert_HappyPath(t *testing.T) {
	layer := &fakeLayer{}
	tool := NewMemoryUpsertTool(layer)

	args := json.RawMessage(`{
        "namespace": "hackernews-top",
        "id": "42-991",
        "content": "Show HN: Loamss — your data, your AI",
        "metadata": {"url": "https://example.com", "score": 142, "by": "edima"}
    }`)
	res, err := tool.Handler(context.Background(), ToolInput{Args: args})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	var got memoryUpsertResult
	_ = json.Unmarshal([]byte(res.Content[0].Text), &got)
	if !got.OK || got.Namespace != "hackernews-top" || got.ID != "42-991" {
		t.Errorf("result: %+v", got)
	}
	if len(layer.upserts) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(layer.upserts))
	}
	if layer.upserts[0].Metadata["score"] != float64(142) {
		t.Errorf("metadata.score: %v", layer.upserts[0].Metadata["score"])
	}
}

func TestMemoryUpsert_RejectsMissingNamespace(t *testing.T) {
	tool := NewMemoryUpsertTool(&fakeLayer{})
	_, err := tool.Handler(context.Background(), ToolInput{
		Args: json.RawMessage(`{"id":"x"}`),
	})
	if err == nil {
		t.Error("expected error for missing namespace")
	}
}

func TestMemoryUpsert_RejectsMissingID(t *testing.T) {
	tool := NewMemoryUpsertTool(&fakeLayer{})
	_, err := tool.Handler(context.Background(), ToolInput{
		Args: json.RawMessage(`{"namespace":"x"}`),
	})
	if err == nil {
		t.Error("expected error for missing id")
	}
}

func TestMemoryUpsert_SurfaceLayerErr(t *testing.T) {
	layer := &fakeLayer{failErr: errors.New("layer is busted")}
	tool := NewMemoryUpsertTool(layer)
	_, err := tool.Handler(context.Background(), ToolInput{
		Args: json.RawMessage(`{"namespace":"a","id":"b"}`),
	})
	if !errors.Is(err, ErrToolBackend) {
		t.Errorf("expected wrapped ErrToolBackend, got %v", err)
	}
}

func TestMemoryUpsert_Capability(t *testing.T) {
	tool := NewMemoryUpsertTool(&fakeLayer{})
	if tool.Capability != "memory.write" {
		t.Errorf("expected memory.write capability, got %q", tool.Capability)
	}
}
