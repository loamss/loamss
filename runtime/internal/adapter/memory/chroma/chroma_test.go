package chroma

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

// Two tiers:
//
//   - PURE UNIT: fake Chroma server in-process. Covers full SPI.
//   - INTEGRATION: live Chroma via LOAMSS_CHROMA_TEST_URL env var
//     (e.g. spin up: `docker run --rm -d -p 8000:8000 chromadb/chroma`).

// --- fake server -----------------------------------------------------------

type fakeChroma struct {
	mu             sync.Mutex
	srv            *httptest.Server
	collections    map[string]string // name → uuid
	entries        map[string]fakeEntry
	createDisabled bool
}

type fakeEntry struct {
	id       string
	vector   []float32
	metadata map[string]any
}

func newFakeChroma(t *testing.T) *fakeChroma {
	t.Helper()
	f := &fakeChroma{
		collections: map[string]string{},
		entries:     map[string]fakeEntry{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/heartbeat", f.handleHeartbeat)
	mux.HandleFunc("/api/v2/tenants/", f.handleTenantPath)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeChroma) handleHeartbeat(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"nanosecond_heartbeat": 1234567890}`))
}

// handleTenantPath dispatches everything under
//
//	/api/v2/tenants/{tenant}/databases/{db}/collections/...
func (f *fakeChroma) handleTenantPath(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v2/tenants/"), "/")
	// parts[0] = tenant, parts[1] = "databases", parts[2] = db,
	// parts[3] = "collections", parts[4] (optional) = name|uuid,
	// parts[5] (optional) = operation (upsert/get/query/delete/count)
	if len(parts) < 4 {
		http.Error(w, "bad path", http.StatusNotFound)
		return
	}

	// Create collection: POST .../collections
	if len(parts) == 4 && r.Method == http.MethodPost {
		if f.createDisabled {
			http.Error(w, "creation disabled", http.StatusForbidden)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		name, _ := body["name"].(string)
		uuid := "uuid-" + name
		f.collections[name] = uuid
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": uuid, "name": name})
		return
	}

	// Fetch by name: GET .../collections/{name}
	if len(parts) == 5 && r.Method == http.MethodGet {
		name := parts[4]
		uuid, ok := f.collections[name]
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": uuid, "name": name})
		return
	}

	// Data-plane ops: POST .../collections/{uuid}/{op}
	if len(parts) == 6 && r.Method == http.MethodPost {
		op := parts[5]
		switch op {
		case "upsert":
			f.handleUpsert(w, r)
		case "get":
			f.handleGet(w, r)
		case "query":
			f.handleQuery(w, r)
		case "delete":
			f.handleDelete(w, r)
		default:
			http.Error(w, "unknown op: "+op, http.StatusNotFound)
		}
		return
	}

	// Count: GET .../collections/{uuid}/count
	if len(parts) == 6 && r.Method == http.MethodGet && parts[5] == "count" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(len(f.entries))
		return
	}

	http.Error(w, "unhandled path: "+r.URL.Path, http.StatusNotFound)
}

func (f *fakeChroma) handleUpsert(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs        []string         `json:"ids"`
		Embeddings [][]float32      `json:"embeddings"`
		Metadatas  []map[string]any `json:"metadatas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for i, id := range body.IDs {
		f.entries[id] = fakeEntry{
			id:       id,
			vector:   body.Embeddings[i],
			metadata: body.Metadatas[i],
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"success": true}`))
}

func (f *fakeChroma) handleGet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var (
		ids        []string
		embeddings [][]float32
		metadatas  []map[string]any
	)
	for _, id := range body.IDs {
		e, ok := f.entries[id]
		if !ok {
			continue
		}
		ids = append(ids, e.id)
		embeddings = append(embeddings, e.vector)
		metadatas = append(metadatas, e.metadata)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ids":        ids,
		"embeddings": embeddings,
		"metadatas":  metadatas,
	})
}

func (f *fakeChroma) handleQuery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Where map[string]any `json:"where"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	type row struct {
		id       string
		vec      []float32
		meta     map[string]any
		distance float32
	}
	var hits []row
	idx := 0
	for _, e := range f.entries {
		// Honour where filter (only $eq matchers; matches what
		// the adapter sends).
		if !matchesWhere(e.metadata, body.Where) {
			continue
		}
		hits = append(hits, row{
			id:       e.id,
			vec:      e.vector,
			meta:     e.metadata,
			distance: float32(idx) * 0.1, // deterministic ordering for tests
		})
		idx++
	}
	// Pack as Chroma's nested arrays.
	var ids [][]string
	var embeds [][][]float32
	var metas [][]map[string]any
	var dists [][]float32
	if len(hits) > 0 {
		innerIDs := make([]string, 0, len(hits))
		innerEmbed := make([][]float32, 0, len(hits))
		innerMeta := make([]map[string]any, 0, len(hits))
		innerDist := make([]float32, 0, len(hits))
		for _, h := range hits {
			innerIDs = append(innerIDs, h.id)
			innerEmbed = append(innerEmbed, h.vec)
			innerMeta = append(innerMeta, h.meta)
			innerDist = append(innerDist, h.distance)
		}
		ids = [][]string{innerIDs}
		embeds = [][][]float32{innerEmbed}
		metas = [][]map[string]any{innerMeta}
		dists = [][]float32{innerDist}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ids":        ids,
		"embeddings": embeds,
		"metadatas":  metas,
		"distances":  dists,
	})
}

func (f *fakeChroma) handleDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	for _, id := range body.IDs {
		delete(f.entries, id)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"success": true}`))
}

// matchesWhere applies the chroma `where` filter on a single
// metadata map. Supports just $eq, which is all the adapter
// emits today.
func matchesWhere(meta, where map[string]any) bool {
	for k, v := range where {
		eq, ok := v.(map[string]any)
		if !ok {
			continue
		}
		want, has := eq["$eq"]
		if !has {
			continue
		}
		got, present := meta[k]
		if !present || got != want {
			return false
		}
	}
	return true
}

// --- helpers ---------------------------------------------------------------

func newAdapter(t *testing.T, f *fakeChroma, dim int) *Adapter {
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

func TestInit_CreatesCollectionWhenMissing(t *testing.T) {
	f := newFakeChroma(t)
	a := newAdapter(t, f, 4)
	if a.collectionID == "" {
		t.Errorf("collectionID should be set after Init")
	}
}

func TestInit_ReusesExistingCollection(t *testing.T) {
	f := newFakeChroma(t)
	// Pre-seed the collection.
	f.collections["test_testinit_reusesexistingcollection"] = "uuid-existing"
	a := newAdapter(t, f, 4)
	if a.collectionID != "uuid-existing" {
		t.Errorf("collectionID = %q, expected reuse of uuid-existing", a.collectionID)
	}
}

func TestUpsertGetRoundTrip(t *testing.T) {
	f := newFakeChroma(t)
	a := newAdapter(t, f, 4)
	ctx := context.Background()

	err := a.Upsert(ctx, "alpha", []float32{1, 0, 0, 0},
		map[string]any{"tag": "x"})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := a.Get(ctx, "alpha")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "alpha" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.Metadata["tag"] != "x" {
		t.Errorf("metadata: %v", got.Metadata)
	}
}

func TestBatchUpsertAndSearch(t *testing.T) {
	f := newFakeChroma(t)
	a := newAdapter(t, f, 4)
	ctx := context.Background()

	entries := []memory.Entry{
		{ID: "a", Vector: []float32{1, 0, 0, 0}, Metadata: map[string]any{"k": "1"}},
		{ID: "b", Vector: []float32{0, 1, 0, 0}, Metadata: map[string]any{"k": "2"}},
	}
	if err := a.BatchUpsert(ctx, entries); err != nil {
		t.Fatalf("BatchUpsert: %v", err)
	}

	hits, err := a.Search(ctx, []float32{1, 0, 0, 0}, 5, memory.MetadataFilter{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("expected 2 hits, got %d", len(hits))
	}
}

func TestSearchWithMetadataFilter(t *testing.T) {
	f := newFakeChroma(t)
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

func TestDelete_Idempotent(t *testing.T) {
	f := newFakeChroma(t)
	a := newAdapter(t, f, 4)
	ctx := context.Background()
	_ = a.Upsert(ctx, "doomed", []float32{1, 0, 0, 0}, nil)

	if err := a.Delete(ctx, "doomed"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	_, err := a.Get(ctx, "doomed")
	if !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("Get after delete should be ErrNotFound, got %v", err)
	}
	// Idempotent.
	if err := a.Delete(ctx, "doomed"); err != nil {
		t.Errorf("Delete on missing should be nil, got %v", err)
	}
}

func TestDimensionMismatch(t *testing.T) {
	f := newFakeChroma(t)
	a := newAdapter(t, f, 4)

	err := a.Upsert(context.Background(), "x", []float32{1, 2}, nil)
	if !errors.Is(err, memory.ErrDimensionMismatch) {
		t.Errorf("Upsert with wrong dim should be ErrDimensionMismatch, got %v", err)
	}
}

func TestStats(t *testing.T) {
	f := newFakeChroma(t)
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
	if stats.Dimension != 3 {
		t.Errorf("Dimension = %d, want 3", stats.Dimension)
	}
	if stats.BackendInfo["backend"] != "chroma" {
		t.Errorf("BackendInfo backend = %q", stats.BackendInfo["backend"])
	}
}

func TestHealthCheck(t *testing.T) {
	f := newFakeChroma(t)
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
	if _, err := a.Search(ctx, []float32{1}, 1, memory.MetadataFilter{}); err == nil {
		t.Error("Search without Init should fail")
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

// --- integration (LOAMSS_CHROMA_TEST_URL) -----------------------------------

func TestIntegration_RoundTrip(t *testing.T) {
	url := os.Getenv("LOAMSS_CHROMA_TEST_URL")
	if url == "" {
		t.Skip("set LOAMSS_CHROMA_TEST_URL (e.g. http://localhost:8000) to run against real Chroma")
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
		t.Error("Search returned no hits against live Chroma")
	}
}
