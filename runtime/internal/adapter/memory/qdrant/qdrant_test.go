package qdrant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
)

// Tests for memory:qdrant. Same pattern as chroma: a fake server
// for unit coverage, integration gated by LOAMSS_QDRANT_TEST_URL.

// --- fake server -----------------------------------------------------------

type fakeQdrant struct {
	mu          sync.Mutex
	srv         *httptest.Server
	collections map[string]bool
	points      map[string]storedPoint
}

type storedPoint struct {
	id      string
	vector  []float32
	payload map[string]any
}

func newFakeQdrant(t *testing.T) *fakeQdrant {
	t.Helper()
	f := &fakeQdrant{
		collections: map[string]bool{},
		points:      map[string]storedPoint{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	})
	mux.HandleFunc("/collections/", f.handleCollections)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeQdrant) handleCollections(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// /collections/{name}
	// /collections/{name}/points         POST (get by id) | PUT (upsert)
	// /collections/{name}/points/search  POST
	// /collections/{name}/points/delete  POST
	// /collections/{name}/points/count   POST
	rest := strings.TrimPrefix(r.URL.Path, "/collections/")
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) < 1 || parts[0] == "" {
		http.Error(w, "bad path", http.StatusNotFound)
		return
	}
	name := parts[0]

	// GET /collections/{name} — exists check
	if len(parts) == 1 && r.Method == http.MethodGet {
		if !f.collections[name] {
			http.Error(w, `{"status":{"error":"not found"}}`, http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}

	// PUT /collections/{name} — create
	if len(parts) == 1 && r.Method == http.MethodPut {
		f.collections[name] = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":true,"status":"ok"}`))
		return
	}

	// All remaining ops require an existing collection.
	if !f.collections[name] {
		http.Error(w, `{"status":{"error":"collection not found"}}`, http.StatusNotFound)
		return
	}

	// /collections/{name}/points (PUT for upsert; POST for get-by-id)
	if len(parts) == 2 && parts[1] == "points" {
		switch r.Method {
		case http.MethodPut:
			f.handleUpsert(w, r)
		case http.MethodPost:
			f.handleGet(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// /collections/{name}/points/{op}
	if len(parts) >= 3 && parts[1] == "points" {
		switch parts[2] {
		case "search":
			f.handleSearch(w, r)
		case "delete":
			f.handleDelete(w, r)
		case "count":
			f.handleCount(w, r)
		default:
			http.Error(w, "unknown op: "+parts[2], http.StatusNotFound)
		}
		return
	}

	http.Error(w, "unhandled path: "+r.URL.Path, http.StatusNotFound)
}

func (f *fakeQdrant) handleUpsert(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Points []struct {
			ID      string         `json:"id"`
			Vector  []float32      `json:"vector"`
			Payload map[string]any `json:"payload"`
		} `json:"points"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, p := range body.Points {
		f.points[p.ID] = storedPoint{id: p.ID, vector: p.Vector, payload: p.Payload}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","result":{"operation_id":1,"status":"acknowledged"}}`))
}

func (f *fakeQdrant) handleGet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var out []map[string]any
	for _, id := range body.IDs {
		p, ok := f.points[id]
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"id":      p.id,
			"vector":  p.vector,
			"payload": p.payload,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"result": out, "status": "ok"})
}

func (f *fakeQdrant) handleSearch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Limit  int            `json:"limit"`
		Filter map[string]any `json:"filter"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	limit := body.Limit
	if limit == 0 {
		limit = 10
	}

	var out []map[string]any
	score := float32(1.0)
	for _, p := range f.points {
		if !matchesQdrantFilter(p.payload, body.Filter) {
			continue
		}
		if len(out) >= limit {
			break
		}
		out = append(out, map[string]any{
			"id":      p.id,
			"score":   score,
			"vector":  p.vector,
			"payload": p.payload,
		})
		score -= 0.1
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"result": out, "status": "ok"})
}

func (f *fakeQdrant) handleDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Points []string `json:"points"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	for _, id := range body.Points {
		delete(f.points, id)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","result":{"operation_id":2,"status":"acknowledged"}}`))
}

func (f *fakeQdrant) handleCount(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"count": len(f.points)},
		"status": "ok",
	})
}

// matchesQdrantFilter applies the {must:[{key, match:{value}}]}
// filter shape on a single payload map.
func matchesQdrantFilter(payload map[string]any, filter map[string]any) bool {
	if filter == nil {
		return true
	}
	mustRaw, ok := filter["must"].([]any)
	if !ok {
		return true
	}
	for _, condRaw := range mustRaw {
		cond, ok := condRaw.(map[string]any)
		if !ok {
			continue
		}
		key, _ := cond["key"].(string)
		match, _ := cond["match"].(map[string]any)
		want, ok := match["value"]
		if !ok {
			continue
		}
		got, present := payload[key]
		if !present || got != want {
			return false
		}
	}
	return true
}

// --- helpers ---------------------------------------------------------------

func newAdapter(t *testing.T, f *fakeQdrant, dim int) *Adapter {
	t.Helper()
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{
		"base_url":   f.srv.URL,
		"dimension":  dim,
		"collection": "test_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_")),
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return a
}

// --- tests -----------------------------------------------------------------

func TestInit_RequiresDimension(t *testing.T) {
	a := &Adapter{}
	err := a.Init(context.Background(), map[string]any{"base_url": "http://x"})
	if err == nil {
		t.Fatal("Init without dimension should fail")
	}
	if !strings.Contains(err.Error(), "dimension") {
		t.Errorf("error should mention dimension, got: %v", err)
	}
}

func TestInit_RejectsBadMetric(t *testing.T) {
	a := &Adapter{}
	err := a.Init(context.Background(), map[string]any{
		"base_url":  "http://x",
		"dimension": 4,
		"metric":    "manhattan",
	})
	if err == nil {
		t.Fatal("Init with bad metric should fail")
	}
}

func TestInit_CreatesCollectionWhenMissing(t *testing.T) {
	f := newFakeQdrant(t)
	a := newAdapter(t, f, 4)
	if !a.inited {
		t.Error("adapter should be inited")
	}
}

func TestUpsertGetRoundTrip(t *testing.T) {
	f := newFakeQdrant(t)
	a := newAdapter(t, f, 4)
	ctx := context.Background()

	err := a.Upsert(ctx, "p1", []float32{1, 0, 0, 0}, map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := a.Get(ctx, "p1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "p1" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.Metadata["k"] != "v" {
		t.Errorf("metadata: %v", got.Metadata)
	}
}

func TestBatchUpsertAndSearch(t *testing.T) {
	f := newFakeQdrant(t)
	a := newAdapter(t, f, 4)
	ctx := context.Background()

	_ = a.BatchUpsert(ctx, []memory.Entry{
		{ID: "a", Vector: []float32{1, 0, 0, 0}, Metadata: map[string]any{"k": "1"}},
		{ID: "b", Vector: []float32{0, 1, 0, 0}, Metadata: map[string]any{"k": "2"}},
	})
	hits, err := a.Search(ctx, []float32{1, 0, 0, 0}, 5, memory.MetadataFilter{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("expected 2 hits, got %d", len(hits))
	}
}

func TestSearch_WithMetadataFilter(t *testing.T) {
	f := newFakeQdrant(t)
	a := newAdapter(t, f, 4)
	ctx := context.Background()

	_ = a.BatchUpsert(ctx, []memory.Entry{
		{ID: "x1", Vector: []float32{1, 0, 0, 0}, Metadata: map[string]any{"kind": "a"}},
		{ID: "x2", Vector: []float32{1, 0, 0, 0}, Metadata: map[string]any{"kind": "b"}},
	})

	hits, err := a.Search(ctx, []float32{1, 0, 0, 0}, 5,
		memory.MetadataFilter{Equals: map[string]any{"kind": "a"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "x1" {
		t.Errorf("filtered search wrong: %+v", hits)
	}
}

func TestSearch_CosineInversion(t *testing.T) {
	// Qdrant returns "score" (higher = closer) for Cosine; the
	// SPI's Distance is smaller-is-closer. Verify the adapter
	// inverts.
	f := newFakeQdrant(t)
	a := newAdapter(t, f, 4)
	ctx := context.Background()

	_ = a.Upsert(ctx, "high-score", []float32{1, 0, 0, 0}, nil)
	hits, _ := a.Search(ctx, []float32{1, 0, 0, 0}, 1, memory.MetadataFilter{})
	if len(hits) != 1 {
		t.Fatal("expected 1 hit")
	}
	// fake server emits score=1.0 for the top hit; with Cosine
	// inversion the distance is 0.
	if hits[0].Distance >= 1.0 {
		t.Errorf("Cosine distance = %v, expected near 0 (1 - score)", hits[0].Distance)
	}
}

func TestDelete_Idempotent(t *testing.T) {
	f := newFakeQdrant(t)
	a := newAdapter(t, f, 4)
	ctx := context.Background()

	_ = a.Upsert(ctx, "gone", []float32{1, 0, 0, 0}, nil)
	if err := a.Delete(ctx, "gone"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	_, err := a.Get(ctx, "gone")
	if !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("Get after delete should be ErrNotFound, got %v", err)
	}
	if err := a.Delete(ctx, "gone"); err != nil {
		t.Errorf("Delete on missing should be nil, got %v", err)
	}
}

func TestDimensionMismatch(t *testing.T) {
	f := newFakeQdrant(t)
	a := newAdapter(t, f, 4)
	err := a.Upsert(context.Background(), "x", []float32{1, 2}, nil)
	if !errors.Is(err, memory.ErrDimensionMismatch) {
		t.Errorf("Upsert with wrong dim should be ErrDimensionMismatch, got %v", err)
	}
}

func TestStats(t *testing.T) {
	f := newFakeQdrant(t)
	a := newAdapter(t, f, 3)
	ctx := context.Background()

	_ = a.Upsert(ctx, "a", []float32{1, 0, 0}, nil)
	_ = a.Upsert(ctx, "b", []float32{0, 1, 0}, nil)
	stats, err := a.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Count != 2 {
		t.Errorf("Count = %d, want 2", stats.Count)
	}
	if stats.BackendInfo["backend"] != "qdrant" {
		t.Errorf("backend = %q", stats.BackendInfo["backend"])
	}
}

func TestHealthCheck(t *testing.T) {
	f := newFakeQdrant(t)
	a := newAdapter(t, f, 1)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestUninited_RejectsCalls(t *testing.T) {
	a := &Adapter{}
	ctx := context.Background()

	if err := a.Upsert(ctx, "x", []float32{1}, nil); err == nil {
		t.Error("Upsert without Init should fail")
	}
	if _, err := a.Get(ctx, "x"); err == nil {
		t.Error("Get without Init should fail")
	}
}

func TestRegistryPickup(t *testing.T) {
	a, err := memory.New(adapterID)
	if err != nil {
		t.Fatalf("memory.New(%q): %v", adapterID, err)
	}
	if a == nil {
		t.Error("memory.New returned nil")
	}
}

// --- integration (LOAMSS_QDRANT_TEST_URL) -----------------------------------

func TestIntegration_RoundTrip(t *testing.T) {
	url := os.Getenv("LOAMSS_QDRANT_TEST_URL")
	if url == "" {
		t.Skip("set LOAMSS_QDRANT_TEST_URL (e.g. http://localhost:6333) to run against real Qdrant")
	}
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{
		"base_url":   url,
		"dimension":  4,
		"collection": "loamss_integration_" + strings.ToLower(t.Name()),
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })

	ctx := context.Background()
	_ = a.BatchUpsert(ctx, []memory.Entry{
		{ID: "i1", Vector: []float32{1, 0, 0, 0}, Metadata: map[string]any{"t": "a"}},
		{ID: "i2", Vector: []float32{0, 1, 0, 0}, Metadata: map[string]any{"t": "b"}},
	})

	hits, err := a.Search(ctx, []float32{1, 0, 0, 0}, 2, memory.MetadataFilter{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Error("Search returned no hits against live Qdrant")
	}
}
