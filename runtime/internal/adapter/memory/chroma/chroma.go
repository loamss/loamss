// Package chroma implements the memory:chroma adapter — a memory
// backend that speaks Chroma's HTTP API v2.
//
// Chroma is a vector DB designed specifically for embeddings.
// Trade-offs vs memory:pgvector:
//
//   - Chroma is single-purpose (vectors + metadata only) so its
//     API is smaller. No SQL surface, no relational features.
//   - Easy to spin up: `chroma run --path ./chroma-data` produces
//     a local server. Containerised deployment via the official
//     chromadb/chroma image.
//   - Native indexing (HNSW by default); no per-deployment index
//     provisioning to think about.
//   - No transaction semantics — eventual consistency between
//     writes and reads when sharding is enabled.
//
// Dep choice: no new Go dep. Chroma's v2 HTTP API is small and
// stable enough to hand-roll without an SDK. The community Go
// SDK (amikos-tech/chroma-go) is fine; we just don't need its
// surface area, and avoiding the dep keeps go.mod focused.
//
// Multi-instance pattern: Chroma exposes a "collection" as the
// unit of isolation, plus optional tenant + database namespacing
// for hard separation. Multiple loamss instances share one
// Chroma server by picking distinct collection names.
package chroma

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
)

const adapterID = "memory:chroma"

func init() {
	memory.Register(adapterID, func() memory.Adapter { return &Adapter{} })
}

// Chroma v2 API defaults. Users override via config; the defaults
// match what `chroma run` ships with out of the box.
const (
	defaultBaseURL    = "http://localhost:8000"
	defaultTenant     = "default_tenant"
	defaultDatabase   = "default_database"
	defaultCollection = "loamss_memory"
)

// Adapter is the memory:chroma concrete adapter.
//
// Zero value is unusable; call Init before any other method.
// After Init, methods are safe for concurrent use — net/http's
// Client is goroutine-safe and the only mutable state is the
// `inited` flag guarded by mu.
type Adapter struct {
	mu           sync.RWMutex
	httpClient   *http.Client
	baseURL      string
	tenant       string
	database     string
	collection   string
	collectionID string // set by ensureCollectionLocked; required for upsert/query/delete
	dimension    int
	inited       bool
}

// Init reads config, ensures the collection exists (creating it
// if not), and stashes its UUID for later requests. Expected
// config keys:
//
//	base_url:   "http://localhost:8000"  (default; chroma's run default)
//	tenant:     "default_tenant"          (default)
//	database:   "default_database"        (default)
//	collection: "loamss_memory"           (default; per-instance namespace)
//	dimension:  1536                       (required; Chroma needs the embedding dim
//	            up front to size its HNSW index)
//	timeout:    "30s"                     (default; per-request HTTP timeout)
func (a *Adapter) Init(ctx context.Context, config map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	dim, ok := config["dimension"].(int)
	if !ok {
		// Tolerate JSON-decoded numbers (float64) and yaml-decoded
		// ones; reject obviously-missing.
		switch v := config["dimension"].(type) {
		case int64:
			dim = int(v)
		case float64:
			dim = int(v)
		default:
			return errors.New("chroma: config requires `dimension` (positive int matching the embedding model)")
		}
	}
	if dim <= 0 {
		return errors.New("chroma: dimension must be a positive int")
	}

	a.baseURL = strings.TrimRight(stringField(config, "base_url", defaultBaseURL), "/")
	a.tenant = stringField(config, "tenant", defaultTenant)
	a.database = stringField(config, "database", defaultDatabase)
	a.collection = stringField(config, "collection", defaultCollection)
	a.dimension = dim

	timeout := 30 * time.Second
	if raw, ok := config["timeout"].(string); ok && raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			timeout = d
		}
	}
	a.httpClient = &http.Client{Timeout: timeout}
	a.inited = true

	if err := a.ensureCollectionLocked(ctx); err != nil {
		a.inited = false
		a.httpClient = nil
		return err
	}
	return nil
}

// ensureCollectionLocked tries to fetch the configured collection;
// if Chroma returns 404 we create it. Idempotent across restarts.
// Stashes the collection UUID — Chroma's data-plane endpoints
// (add / query / delete) take the UUID, not the name.
func (a *Adapter) ensureCollectionLocked(ctx context.Context) error {
	// Try fetching by name first.
	getURL := fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections/%s",
		a.tenant, a.database, a.collection)
	resp, err := a.do(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return fmt.Errorf("chroma: probing collection: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		// Existing collection; pull its UUID.
		var info collectionInfo
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return fmt.Errorf("chroma: decoding collection info: %w", err)
		}
		a.collectionID = info.ID
		return nil
	}
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chroma: unexpected status %d probing collection: %s",
			resp.StatusCode, string(body))
	}

	// Create.
	createURL := fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections",
		a.tenant, a.database)
	body, err := json.Marshal(map[string]any{
		"name":          a.collection,
		"get_or_create": true,
		// HNSW config — Chroma defaults are reasonable; we just
		// set the dimension hint so the first insert doesn't
		// renegotiate. Chroma infers dim from first insert too,
		// but pre-setting it makes the failure mode clearer if
		// the caller embeds at the wrong dimension later.
		"configuration": map[string]any{
			"hnsw": map[string]any{
				"space": "cosine",
			},
		},
		"metadata": map[string]any{
			"loamss_managed":    true,
			"loamss_dimension":  a.dimension,
			"loamss_created_at": time.Now().UTC().Format(time.RFC3339),
		},
	})
	if err != nil {
		return fmt.Errorf("chroma: encoding create request: %w", err)
	}
	resp2, err := a.do(ctx, http.MethodPost, createURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("chroma: creating collection: %w", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK && resp2.StatusCode != http.StatusCreated {
		respBytes, _ := io.ReadAll(resp2.Body)
		return fmt.Errorf("chroma: collection create returned %d: %s",
			resp2.StatusCode, string(respBytes))
	}
	var info collectionInfo
	if err := json.NewDecoder(resp2.Body).Decode(&info); err != nil {
		return fmt.Errorf("chroma: decoding create response: %w", err)
	}
	a.collectionID = info.ID
	return nil
}

// --- writes ---------------------------------------------------------------

// Upsert inserts or replaces a single entry.
func (a *Adapter) Upsert(
	ctx context.Context, id string, vector []float32, metadata map[string]any,
) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	if len(vector) != a.dimension {
		return fmt.Errorf("%w: got %d, expected %d", memory.ErrDimensionMismatch, len(vector), a.dimension)
	}
	return a.batchUpsertLocked(ctx, []memory.Entry{{
		ID:       id,
		Vector:   vector,
		Metadata: metadata,
	}})
}

// BatchUpsert inserts/replaces many entries in a single round-trip.
// Chroma's /upsert endpoint accepts parallel arrays: ids[], embeddings[][],
// metadatas[]; matching indices form one record.
func (a *Adapter) BatchUpsert(ctx context.Context, entries []memory.Entry) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	return a.batchUpsertLocked(ctx, entries)
}

func (a *Adapter) batchUpsertLocked(ctx context.Context, entries []memory.Entry) error {
	ids := make([]string, len(entries))
	embeds := make([][]float32, len(entries))
	metas := make([]map[string]any, len(entries))
	for i, e := range entries {
		if len(e.Vector) != a.dimension {
			return fmt.Errorf("%w: entry %s got %d, expected %d",
				memory.ErrDimensionMismatch, e.ID, len(e.Vector), a.dimension)
		}
		ids[i] = e.ID
		embeds[i] = e.Vector
		// Chroma rejects null metadatas — encode a non-null
		// placeholder when the caller passed nil/empty.
		if e.Metadata == nil {
			metas[i] = map[string]any{}
		} else {
			metas[i] = e.Metadata
		}
	}
	body, err := json.Marshal(map[string]any{
		"ids":        ids,
		"embeddings": embeds,
		"metadatas":  metas,
	})
	if err != nil {
		return fmt.Errorf("chroma: encoding upsert: %w", err)
	}
	url := a.dataPlanePath("/upsert")
	resp, err := a.do(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("chroma: upsert: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chroma: upsert status %d: %s", resp.StatusCode, string(respBytes))
	}
	return nil
}

// --- reads ----------------------------------------------------------------

// Get fetches a single entry by id, or returns ErrNotFound.
func (a *Adapter) Get(ctx context.Context, id string) (*memory.Entry, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"ids":     []string{id},
		"include": []string{"embeddings", "metadatas"},
	})
	if err != nil {
		return nil, fmt.Errorf("chroma: encoding get: %w", err)
	}
	resp, err := a.do(ctx, http.MethodPost, a.dataPlanePath("/get"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("chroma: get: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chroma: get status %d: %s", resp.StatusCode, string(respBytes))
	}
	var out getResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("chroma: decoding get response: %w", err)
	}
	if len(out.IDs) == 0 {
		return nil, fmt.Errorf("%w: %s", memory.ErrNotFound, id)
	}
	var meta map[string]any
	if len(out.Metadatas) > 0 {
		meta = out.Metadatas[0]
	}
	var vec []float32
	if len(out.Embeddings) > 0 {
		vec = out.Embeddings[0]
	}
	return &memory.Entry{
		ID:       out.IDs[0],
		Vector:   vec,
		Metadata: meta,
	}, nil
}

// Search returns up to k nearest-neighbour entries to query with
// optional metadata filtering via Chroma's `where` clause.
//
// Distance: Chroma returns smaller-is-closer regardless of which
// space the collection was created with; we pass that through
// verbatim.
func (a *Adapter) Search(
	ctx context.Context, query []float32, k int, filter memory.MetadataFilter,
) ([]memory.SearchHit, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	if len(query) != a.dimension {
		return nil, fmt.Errorf("%w: query has %d, expected %d",
			memory.ErrDimensionMismatch, len(query), a.dimension)
	}
	if k <= 0 {
		k = 10
	}
	payload := map[string]any{
		"query_embeddings": [][]float32{query},
		"n_results":        k,
		"include":          []string{"embeddings", "metadatas", "distances"},
	}
	if len(filter.Equals) > 0 {
		// Chroma's where clause is a JSON object with
		// {"key": {"$eq": value}} per predicate; multiple keys
		// implicitly AND together.
		where := map[string]any{}
		for k, v := range filter.Equals {
			where[k] = map[string]any{"$eq": v}
		}
		payload["where"] = where
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("chroma: encoding query: %w", err)
	}
	resp, err := a.do(ctx, http.MethodPost, a.dataPlanePath("/query"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("chroma: query: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("chroma: query status %d: %s", resp.StatusCode, string(respBytes))
	}
	var out queryResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("chroma: decoding query response: %w", err)
	}
	// Chroma returns parallel arrays nested one level (per-query
	// batch). We only ever submit one query so we read [0].
	if len(out.IDs) == 0 {
		return nil, nil
	}
	ids, metas, dists, embeds :=
		out.IDs[0], firstOr(out.Metadatas), firstOrFloat(out.Distances), firstOrEmbeddings(out.Embeddings)
	hits := make([]memory.SearchHit, 0, len(ids))
	for i, id := range ids {
		hit := memory.SearchHit{ID: id}
		if i < len(metas) {
			hit.Metadata = metas[i]
		}
		if i < len(dists) {
			hit.Distance = dists[i]
		}
		if i < len(embeds) {
			hit.Vector = embeds[i]
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

// Delete removes one entry. Idempotent — Chroma's delete accepts
// missing ids and returns 200.
func (a *Adapter) Delete(ctx context.Context, id string) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"ids": []string{id}})
	if err != nil {
		return fmt.Errorf("chroma: encoding delete: %w", err)
	}
	resp, err := a.do(ctx, http.MethodPost, a.dataPlanePath("/delete"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("chroma: delete: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chroma: delete status %d: %s", resp.StatusCode, string(respBytes))
	}
	return nil
}

// --- introspection --------------------------------------------------------

// Stats returns count + dimension + backend info.
func (a *Adapter) Stats(ctx context.Context) (memory.Stats, error) {
	if err := a.requireInited(); err != nil {
		return memory.Stats{}, err
	}
	resp, err := a.do(ctx, http.MethodGet, a.dataPlanePath("/count"), nil)
	if err != nil {
		return memory.Stats{}, fmt.Errorf("chroma: count: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return memory.Stats{}, fmt.Errorf("chroma: count status %d: %s",
			resp.StatusCode, string(respBytes))
	}
	var count int64
	if err := json.NewDecoder(resp.Body).Decode(&count); err != nil {
		return memory.Stats{}, fmt.Errorf("chroma: decoding count: %w", err)
	}
	return memory.Stats{
		Count:     count,
		Dimension: a.dimension,
		BackendInfo: map[string]string{
			"backend":       "chroma",
			"collection":    a.collection,
			"collection_id": a.collectionID,
			"tenant":        a.tenant,
			"database":      a.database,
		},
	}, nil
}

// HealthCheck hits /heartbeat — the cheapest endpoint Chroma
// publishes. Returns a nanosecond-epoch timestamp on success.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := a.do(pingCtx, http.MethodGet, "/api/v2/heartbeat", nil)
	if err != nil {
		return fmt.Errorf("%w: %v", memory.ErrConnectionLost, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chroma: heartbeat status %d: %s",
			resp.StatusCode, string(respBytes))
	}
	return nil
}

// Close flips the inited flag and drops the HTTP client ref.
func (a *Adapter) Close(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inited = false
	a.httpClient = nil
	return nil
}

// --- helpers --------------------------------------------------------------

func (a *Adapter) requireInited() error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.inited {
		return errors.New("chroma: adapter not initialised (call Init first)")
	}
	return nil
}

// dataPlanePath constructs the URL for collection-scoped data
// endpoints. Chroma keys data-plane requests by collection UUID
// (which we stashed at Init), not by name.
func (a *Adapter) dataPlanePath(suffix string) string {
	return fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections/%s%s",
		a.tenant, a.database, a.collectionID, suffix)
}

// do issues one HTTP request to the configured Chroma server.
// Centralised so headers + base URL are consistent.
func (a *Adapter) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return a.httpClient.Do(req)
}

// --- response shapes -------------------------------------------------------

type collectionInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type getResponse struct {
	IDs        []string         `json:"ids"`
	Embeddings [][]float32      `json:"embeddings"`
	Metadatas  []map[string]any `json:"metadatas"`
}

type queryResponse struct {
	IDs        [][]string         `json:"ids"`
	Embeddings [][][]float32      `json:"embeddings"`
	Metadatas  [][]map[string]any `json:"metadatas"`
	Distances  [][]float32        `json:"distances"`
}

func firstOr(s [][]map[string]any) []map[string]any {
	if len(s) == 0 {
		return nil
	}
	return s[0]
}

func firstOrFloat(s [][]float32) []float32 {
	if len(s) == 0 {
		return nil
	}
	return s[0]
}

func firstOrEmbeddings(s [][][]float32) [][]float32 {
	if len(s) == 0 {
		return nil
	}
	return s[0]
}

func stringField(config map[string]any, key, fallback string) string {
	v, ok := config[key]
	if !ok {
		return fallback
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return fallback
	}
	return s
}
