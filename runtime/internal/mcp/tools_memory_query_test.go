package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/sqlite"
	"github.com/loamss/loamss/runtime/internal/adapter/model"
	"github.com/loamss/loamss/runtime/internal/adapter/model/dummy"
	"github.com/loamss/loamss/runtime/internal/adapter/model/none"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// queryFixture extends testFixture with a memory adapter, a model
// adapter, and the memory.query tool registered. Two flavors:
// withDummy() uses model:dummy (semantic search returns real
// ranked results); withNone() uses model:none (graceful
// degradation).
type queryFixture struct {
	*testFixture
	mem memory.Adapter
	mdl model.Adapter
}

func newQueryFixture(t *testing.T, modelKind string) *queryFixture {
	t.Helper()
	f := newTestHandler(t)
	mem, err := memory.New("memory:sqlite")
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if err := mem.Init(context.Background(), map[string]any{
		"path": filepath.Join(t.TempDir(), "memory.db"),
	}); err != nil {
		t.Fatalf("mem.Init: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close(context.Background()) })

	var mdl model.Adapter
	switch modelKind {
	case "dummy":
		mdl = &dummy.Adapter{}
	case "none":
		mdl = &none.Adapter{}
	default:
		t.Fatalf("unknown model kind %q", modelKind)
	}
	if err := mdl.Init(context.Background(), nil); err != nil {
		t.Fatalf("model.Init: %v", err)
	}
	t.Cleanup(func() { _ = mdl.Close(context.Background()) })

	if err := f.h.deps.Tools.Register(NewMemoryQueryTool(mem, mdl)); err != nil {
		t.Fatalf("Register memory.query: %v", err)
	}
	return &queryFixture{testFixture: f, mem: mem, mdl: mdl}
}

func TestMemoryQuery_DeniedWithoutGrant(t *testing.T) {
	f := newQueryFixture(t, "dummy")
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "memory.query", "arguments": map[string]any{"query": "anything"}},
	})
	if resp.Error == nil || resp.Error.Code != codePermissionDenied {
		t.Errorf("expected codePermissionDenied, got %+v", resp.Error)
	}
}

func TestMemoryQuery_HappyPath(t *testing.T) {
	f := newQueryFixture(t, "dummy")
	ctx := context.Background()
	_, _ = f.store.IssueGrant(ctx, permission.Grant{
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: f.client.ID},
		Capability: "memory.read",
	})

	// Seed three entries. model:dummy is deterministic but its
	// semantic notion is "input hash" rather than meaning — we
	// pre-embed three different texts and store them with the
	// SAME texts as identifiers, then query for the same text. The
	// closest-distance hit should be the matching entry.
	for _, txt := range []string{"alpha", "beta", "gamma"} {
		emb, err := f.mdl.Embed(ctx, model.EmbedRequest{ModelID: "dummy-embed", Text: txt})
		if err != nil {
			t.Fatalf("seed embed %q: %v", txt, err)
		}
		if err := f.mem.Upsert(ctx, "mem-"+txt, emb.Vector,
			map[string]any{"text": txt}); err != nil {
			t.Fatalf("seed upsert %q: %v", txt, err)
		}
	}

	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name":      "memory.query",
			"arguments": map[string]any{"query": "alpha", "k": 3},
		},
	})
	if resp.Error != nil {
		t.Fatalf("memory.query: %+v", resp.Error)
	}
	rb, _ := json.Marshal(resp.Result)
	var cr callToolResult
	_ = json.Unmarshal(rb, &cr)
	if cr.IsError {
		t.Fatalf("unexpected isError: %s", cr.Content[0].Text)
	}

	var payload memoryQueryResult
	if err := json.Unmarshal([]byte(cr.Content[0].Text), &payload); err != nil {
		t.Fatalf("decode payload: %v\n%s", err, cr.Content[0].Text)
	}
	if payload.Query != "alpha" {
		t.Errorf("query echo: %q", payload.Query)
	}
	if payload.ModelID != "dummy-embed" {
		t.Errorf("model_id: %q", payload.ModelID)
	}
	if payload.Count != 3 {
		t.Errorf("count: %d", payload.Count)
	}
	if len(payload.Hits) == 0 || payload.Hits[0].ID != "mem-alpha" {
		t.Errorf("top hit should be mem-alpha, got %+v", payload.Hits)
	}
	// Vectors must not be inlined; metadata should round-trip.
	if payload.Hits[0].Metadata["text"] != "alpha" {
		t.Errorf("metadata not preserved: %+v", payload.Hits[0].Metadata)
	}
}

func TestMemoryQuery_NoModel_GracefulDegradation(t *testing.T) {
	f := newQueryFixture(t, "none")
	ctx := context.Background()
	_, _ = f.store.IssueGrant(ctx, permission.Grant{
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: f.client.ID},
		Capability: "memory.read",
	})
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name":      "memory.query",
			"arguments": map[string]any{"query": "anything"},
		},
	})
	// model:none should produce a tool-level isError, not an RPC
	// error. The client gets a human-readable "semantic search
	// unavailable" message.
	if resp.Error != nil {
		t.Fatalf("expected ToolResult{IsError:true}, got RPC error: %+v", resp.Error)
	}
	rb, _ := json.Marshal(resp.Result)
	var cr callToolResult
	_ = json.Unmarshal(rb, &cr)
	if !cr.IsError {
		t.Error("expected isError=true on no model configured")
	}
	if !strings.Contains(cr.Content[0].Text, "semantic search unavailable") {
		t.Errorf("expected explanation, got: %s", cr.Content[0].Text)
	}
}

func TestMemoryQuery_RejectsMissingQuery(t *testing.T) {
	f := newQueryFixture(t, "dummy")
	ctx := context.Background()
	_, _ = f.store.IssueGrant(ctx, permission.Grant{
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: f.client.ID},
		Capability: "memory.read",
	})
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{
			"name":      "memory.query",
			"arguments": map[string]any{"query": ""},
		},
	})
	if resp.Error == nil || resp.Error.Code != codeBackendError {
		t.Errorf("expected codeBackendError for empty query, got %+v", resp.Error)
	}
}

func TestMemoryQuery_RejectsUnknownModelID(t *testing.T) {
	f := newQueryFixture(t, "dummy")
	ctx := context.Background()
	_, _ = f.store.IssueGrant(ctx, permission.Grant{
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: f.client.ID},
		Capability: "memory.read",
	})
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]any{
			"name": "memory.query",
			"arguments": map[string]any{
				"query":    "x",
				"model_id": "made-up",
			},
		},
	})
	if resp.Error == nil || resp.Error.Code != codeBackendError {
		t.Errorf("expected codeBackendError, got %+v", resp.Error)
	}
}

func TestMemoryQuery_KCapped(t *testing.T) {
	f := newQueryFixture(t, "dummy")
	ctx := context.Background()
	_, _ = f.store.IssueGrant(ctx, permission.Grant{
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: f.client.ID},
		Capability: "memory.read",
	})
	// Seed two entries.
	for i, txt := range []string{"a", "b"} {
		_ = i
		emb, _ := f.mdl.Embed(ctx, model.EmbedRequest{ModelID: "dummy-embed", Text: txt})
		_ = f.mem.Upsert(ctx, "k-"+txt, emb.Vector, nil)
	}
	// Request k=500; tool should cap to 100.
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "tools/call",
		"params": map[string]any{
			"name":      "memory.query",
			"arguments": map[string]any{"query": "anything", "k": 500},
		},
	})
	if resp.Error != nil {
		t.Fatalf("memory.query: %+v", resp.Error)
	}
	// Only two entries seeded; we should get 2 back regardless of k.
	rb, _ := json.Marshal(resp.Result)
	var cr callToolResult
	_ = json.Unmarshal(rb, &cr)
	var payload memoryQueryResult
	_ = json.Unmarshal([]byte(cr.Content[0].Text), &payload)
	if payload.Count != 2 {
		t.Errorf("count: %d, want 2", payload.Count)
	}
}

func TestPickEmbeddingModel_Cases(t *testing.T) {
	ctx := context.Background()
	d := &dummy.Adapter{}
	_ = d.Init(ctx, nil)

	// Default — picks the first embeddings-capable model.
	id, err := pickEmbeddingModel(ctx, d, "")
	if err != nil {
		t.Fatalf("default pick: %v", err)
	}
	if id != "dummy-embed" {
		t.Errorf("default: got %q", id)
	}

	// Explicit known id.
	id, err = pickEmbeddingModel(ctx, d, "dummy-embed")
	if err != nil || id != "dummy-embed" {
		t.Errorf("explicit known: err=%v id=%q", err, id)
	}

	// Explicit unknown id.
	if _, err := pickEmbeddingModel(ctx, d, "not-here"); err == nil {
		t.Error("expected error for unknown id")
	}

	// model:none — no embedding model.
	n := &none.Adapter{}
	_ = n.Init(ctx, nil)
	if _, err := pickEmbeddingModel(ctx, n, ""); err == nil {
		t.Error("expected error when no embedding model available")
	}
}
