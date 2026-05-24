package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
)

// newAdapter creates and initializes a sqlite adapter rooted at a
// unique temp path per test.
func newAdapter(t *testing.T) *Adapter {
	t.Helper()
	dir := t.TempDir()
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{"path": filepath.Join(dir, "memory.db")}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	return a
}

func TestInit_CreatesDatabaseAndParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "memory.db")

	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{"path": path}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer a.Close(context.Background())

	if _, err := os.Stat(path); err != nil {
		t.Errorf("db not created: %v", err)
	}
}

func TestInit_MissingPath(t *testing.T) {
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestInit_BadPathType(t *testing.T) {
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{"path": 42}); err == nil {
		t.Fatal("expected error for non-string path")
	}
}

func TestInit_ReopensExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.db")

	a1 := &Adapter{}
	if err := a1.Init(context.Background(), map[string]any{"path": path}); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := a1.Upsert(context.Background(), "x", []float32{1, 0, 0, 0}, nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	_ = a1.Close(context.Background())

	a2 := &Adapter{}
	if err := a2.Init(context.Background(), map[string]any{"path": path}); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	defer a2.Close(context.Background())

	got, err := a2.Get(context.Background(), "x")
	if err != nil {
		t.Fatalf("Get after re-Init: %v", err)
	}
	if got.ID != "x" {
		t.Errorf("id: %q", got.ID)
	}
	// Dimension should have been loaded from meta.
	s, _ := a2.Stats(context.Background())
	if s.Dimension != 4 {
		t.Errorf("dimension after reopen: %d, want 4", s.Dimension)
	}
}

func TestUpsertGet_RoundTrip(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()

	if err := a.Upsert(ctx, "sarah", []float32{1, 2, 3, 4}, map[string]any{"type": "person", "team": "acme"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := a.Get(ctx, "sarah")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "sarah" {
		t.Errorf("id: %q", got.ID)
	}
	if len(got.Vector) != 4 || got.Vector[0] != 1 || got.Vector[3] != 4 {
		t.Errorf("vector: %v", got.Vector)
	}
	if got.Metadata["type"] != "person" || got.Metadata["team"] != "acme" {
		t.Errorf("metadata: %v", got.Metadata)
	}
}

func TestUpsert_OverwritesExisting(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()

	_ = a.Upsert(ctx, "x", []float32{1, 0, 0, 0}, map[string]any{"v": 1})
	_ = a.Upsert(ctx, "x", []float32{0, 1, 0, 0}, map[string]any{"v": 2})

	got, err := a.Get(ctx, "x")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Vector[0] != 0 || got.Vector[1] != 1 {
		t.Errorf("vector not overwritten: %v", got.Vector)
	}
	if v, _ := asFloat(got.Metadata["v"]); v != 2 {
		t.Errorf("metadata not overwritten: %v", got.Metadata)
	}
}

func TestGet_NotFound(t *testing.T) {
	a := newAdapter(t)
	_, err := a.Get(context.Background(), "ghost")
	if !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestUpsert_RejectsEmpty(t *testing.T) {
	a := newAdapter(t)
	if err := a.Upsert(context.Background(), "", []float32{1, 2}, nil); err == nil {
		t.Error("expected error for empty id")
	}
	if err := a.Upsert(context.Background(), "ok", []float32{}, nil); err == nil {
		t.Error("expected error for empty vector")
	}
}

func TestUpsert_DimensionMismatch(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()

	if err := a.Upsert(ctx, "first", []float32{1, 2, 3, 4}, nil); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	err := a.Upsert(ctx, "second", []float32{1, 2}, nil)
	if !errors.Is(err, memory.ErrDimensionMismatch) {
		t.Errorf("expected ErrDimensionMismatch, got: %v", err)
	}
}

func TestBatchUpsert_AllOrNothing(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()

	good := []memory.Entry{
		{ID: "a", Vector: []float32{1, 0, 0, 0}, Metadata: map[string]any{"i": 1}},
		{ID: "b", Vector: []float32{0, 1, 0, 0}, Metadata: map[string]any{"i": 2}},
		{ID: "c", Vector: []float32{0, 0, 1, 0}, Metadata: map[string]any{"i": 3}},
	}
	if err := a.BatchUpsert(ctx, good); err != nil {
		t.Fatalf("BatchUpsert: %v", err)
	}
	for _, e := range good {
		if _, err := a.Get(ctx, e.ID); err != nil {
			t.Errorf("Get %s after BatchUpsert: %v", e.ID, err)
		}
	}

	// Bad batch: includes a dim mismatch. Nothing should land.
	bad := []memory.Entry{
		{ID: "x", Vector: []float32{1, 0, 0, 0}, Metadata: nil},
		{ID: "y", Vector: []float32{1, 0}, Metadata: nil}, // wrong dim
	}
	err := a.BatchUpsert(ctx, bad)
	if !errors.Is(err, memory.ErrDimensionMismatch) {
		t.Errorf("expected ErrDimensionMismatch, got: %v", err)
	}
	// Neither x nor y should have been written.
	if _, err := a.Get(ctx, "x"); !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("x should not exist after rejected batch")
	}
}

func TestBatchUpsert_EmptyIsNoop(t *testing.T) {
	a := newAdapter(t)
	if err := a.BatchUpsert(context.Background(), nil); err != nil {
		t.Errorf("BatchUpsert(nil): %v", err)
	}
	if err := a.BatchUpsert(context.Background(), []memory.Entry{}); err != nil {
		t.Errorf("BatchUpsert(empty): %v", err)
	}
}

func TestSearch_TopKByCosineDistance(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()

	// 2D vectors at known angles; cosine distance is predictable.
	entries := []memory.Entry{
		{ID: "right", Vector: []float32{1, 0}, Metadata: map[string]any{"angle": 0}},
		{ID: "diag", Vector: []float32{1, 1}, Metadata: map[string]any{"angle": 45}},
		{ID: "up", Vector: []float32{0, 1}, Metadata: map[string]any{"angle": 90}},
		{ID: "left", Vector: []float32{-1, 0}, Metadata: map[string]any{"angle": 180}},
	}
	if err := a.BatchUpsert(ctx, entries); err != nil {
		t.Fatalf("BatchUpsert: %v", err)
	}

	// Query along the +x axis: right wins, then diag, then up, then left.
	hits, err := a.Search(ctx, []float32{1, 0}, 4, memory.MetadataFilter{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 4 {
		t.Fatalf("expected 4 hits, got %d", len(hits))
	}
	wantOrder := []string{"right", "diag", "up", "left"}
	for i, h := range hits {
		if h.ID != wantOrder[i] {
			t.Errorf("hit[%d]: got %s, want %s (full order: %v)", i, h.ID, wantOrder[i], hitIDs(hits))
		}
	}
	// Distances should be monotonically non-decreasing.
	for i := 1; i < len(hits); i++ {
		if hits[i].Distance < hits[i-1].Distance {
			t.Errorf("not sorted: %v", hits)
		}
	}
	// Closest is identical direction → distance ≈ 0.
	if hits[0].Distance > 0.001 {
		t.Errorf("closest distance should be ~0, got %f", hits[0].Distance)
	}
}

func TestSearch_LimitsToK(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		_ = a.Upsert(ctx, string(rune('a'+i)), []float32{float32(i), 0}, nil)
	}
	hits, _ := a.Search(ctx, []float32{1, 0}, 3, memory.MetadataFilter{})
	if len(hits) != 3 {
		t.Errorf("expected k=3 hits, got %d", len(hits))
	}
}

func TestSearch_KZeroReturnsEmpty(t *testing.T) {
	a := newAdapter(t)
	hits, err := a.Search(context.Background(), []float32{1, 0}, 0, memory.MetadataFilter{})
	if err != nil {
		t.Fatalf("Search(k=0): %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected empty result for k=0, got %d", len(hits))
	}
}

func TestSearch_MetadataFilter(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()
	entries := []memory.Entry{
		{ID: "p1", Vector: []float32{1, 0}, Metadata: map[string]any{"type": "person"}},
		{ID: "p2", Vector: []float32{0.9, 0.1}, Metadata: map[string]any{"type": "person"}},
		{ID: "proj1", Vector: []float32{1, 0}, Metadata: map[string]any{"type": "project"}},
	}
	_ = a.BatchUpsert(ctx, entries)

	hits, err := a.Search(ctx, []float32{1, 0}, 10,
		memory.MetadataFilter{Equals: map[string]any{"type": "person"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("expected 2 person hits, got %d: %v", len(hits), hitIDs(hits))
	}
	for _, h := range hits {
		if h.Metadata["type"] != "person" {
			t.Errorf("filter leaked: %v", h)
		}
	}
}

func TestSearch_DimensionMismatch(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()
	_ = a.Upsert(ctx, "x", []float32{1, 2, 3, 4}, nil)

	_, err := a.Search(ctx, []float32{1, 2}, 1, memory.MetadataFilter{})
	if !errors.Is(err, memory.ErrDimensionMismatch) {
		t.Errorf("expected ErrDimensionMismatch, got: %v", err)
	}
}

func TestSearch_ZeroQueryRejected(t *testing.T) {
	a := newAdapter(t)
	_ = a.Upsert(context.Background(), "x", []float32{1, 0}, nil)
	_, err := a.Search(context.Background(), []float32{0, 0}, 1, memory.MetadataFilter{})
	if err == nil {
		t.Error("expected error for zero query vector")
	}
}

func TestDelete_Idempotent(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()
	_ = a.Upsert(ctx, "x", []float32{1, 0}, nil)

	if err := a.Delete(ctx, "x"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if err := a.Delete(ctx, "x"); err != nil {
		t.Errorf("Delete idempotency: %v", err)
	}
	if err := a.Delete(ctx, "never-existed"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}

	if _, err := a.Get(ctx, "x"); !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("expected ErrNotFound after Delete, got: %v", err)
	}
}

func TestStats_Reports(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		_ = a.Upsert(ctx, id, []float32{1, 0, 0, 0}, nil)
	}
	s, err := a.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if s.Count != 3 {
		t.Errorf("Count: got %d, want 3", s.Count)
	}
	if s.Dimension != 4 {
		t.Errorf("Dimension: got %d, want 4", s.Dimension)
	}
	if s.BackendInfo["driver"] != "modernc.org/sqlite" {
		t.Errorf("BackendInfo missing driver: %v", s.BackendInfo)
	}
}

func TestHealthCheck_PassesAndFailsAfterClose(t *testing.T) {
	a := newAdapter(t)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
	_ = a.Close(context.Background())
	if err := a.HealthCheck(context.Background()); err == nil {
		t.Error("expected HealthCheck to fail after Close")
	}
}

func TestUninitializedAdapter_AllOperationsReturnError(t *testing.T) {
	a := &Adapter{}
	ctx := context.Background()
	cases := []struct {
		name string
		fn   func() error
	}{
		{"Upsert", func() error { return a.Upsert(ctx, "x", []float32{1}, nil) }},
		{"Get", func() error { _, err := a.Get(ctx, "x"); return err }},
		{"Delete", func() error { return a.Delete(ctx, "x") }},
		{"Stats", func() error { _, err := a.Stats(ctx); return err }},
		{"HealthCheck", func() error { return a.HealthCheck(ctx) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err == nil {
				t.Errorf("%s on uninitialized adapter should fail", tc.name)
			}
		})
	}
}

func TestVectorPacking_RoundTrip(t *testing.T) {
	v := []float32{0, 1, -1, 0.5, -0.5, 3.14159, -2.71828}
	got := unpackVector(packVector(v))
	if len(got) != len(v) {
		t.Fatalf("length: %d vs %d", len(got), len(v))
	}
	for i := range v {
		if got[i] != v[i] {
			t.Errorf("vec[%d]: got %f, want %f", i, got[i], v[i])
		}
	}
}

func TestConcurrent_ReadsAndWrites(t *testing.T) {
	a := newAdapter(t)
	const goroutines = 8
	const iterations = 25

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for j := 0; j < iterations; j++ {
				id := string(rune('a'+i%26)) + string(rune('0'+j%10))
				// Always-nonzero vector so Search doesn't reject the
				// pathological all-zeros at i==0, j==0.
				v := []float32{float32(i + 1), float32(j + 1), 1, 0}
				if err := a.Upsert(ctx, id, v, nil); err != nil {
					t.Errorf("Upsert: %v", err)
					return
				}
				if _, err := a.Get(ctx, id); err != nil {
					t.Errorf("Get: %v", err)
					return
				}
				if _, err := a.Search(ctx, v, 3, memory.MetadataFilter{}); err != nil {
					t.Errorf("Search: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func hitIDs(hits []memory.SearchHit) []string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	return ids
}

// Compile-time check that *Adapter satisfies memory.Adapter.
var _ memory.Adapter = (*Adapter)(nil)
