// Package qdrant implements the memory:qdrant adapter — a memory
// backend that speaks Qdrant's HTTP REST API.
//
// Qdrant trade-offs vs the others:
//
//   - Production-grade vector DB written in Rust; HNSW + filtering
//     is fast at scale.
//   - Rich filtering: full boolean expressions on payload (our
//     "metadata"). Loamss only uses equality today; the SPI's
//     MetadataFilter will grow into Qdrant's richer surface.
//   - Native gRPC + HTTP. We use HTTP for portability + clarity;
//     gRPC is a future commit if benchmarks justify it.
//   - Single-binary deployment via the official qdrant/qdrant
//     image; works as a cluster too for HA.
//
// Dep choice: no new Go dep. Qdrant's REST is small enough to
// hand-roll. The community SDK (qdrant/go-client) wraps gRPC,
// which adds proto + grpc transitives — not worth it for the
// surface area we use.
package qdrant

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // not for security — deterministic UUIDv5 derivation
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

// loamssIDKey is the payload field where we stash the runtime's
// original ID. Qdrant requires UUIDs or unsigned ints for point
// IDs, but the SPI hands us arbitrary strings (typically ULIDs).
// We deterministically derive a UUIDv5 from the string for
// Qdrant's id field and keep the original here for round-trips.
const loamssIDKey = "_loamss_id"

// loamssIDNamespace is a fixed UUID used as the namespace for
// UUIDv5 derivation. Any fixed namespace works — the contract is
// just "same input → same UUID across runs."
var loamssIDNamespace = [16]byte{
	0x6c, 0x6f, 0x61, 0x6d, 0x73, 0x73, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
}

// toQdrantID derives a deterministic UUIDv5 from a runtime string
// ID. UUIDv5 uses SHA-1 over (namespace || name); we set the
// version + variant bits per RFC 4122.
func toQdrantID(loamssID string) string {
	h := sha1.New() //nolint:gosec // not for security — fixed-format id derivation
	h.Write(loamssIDNamespace[:])
	h.Write([]byte(loamssID))
	sum := h.Sum(nil)
	// Take the first 16 bytes; set version (5) + variant (RFC 4122).
	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0F) | 0x50 // version 5
	u[8] = (u[8] & 0x3F) | 0x80 // variant RFC 4122
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

const adapterID = "memory:qdrant"

func init() {
	memory.Register(adapterID, func() memory.Adapter { return &Adapter{} })
}

// Qdrant defaults — local dev image listens on :6333 for REST.
const (
	defaultBaseURL    = "http://localhost:6333"
	defaultCollection = "loamss_memory"
)

// Adapter is the memory:qdrant concrete adapter.
//
// Zero value is unusable; call Init before any other method.
// After Init, methods are safe for concurrent use — net/http's
// Client is goroutine-safe.
type Adapter struct {
	mu         sync.RWMutex
	httpClient *http.Client
	baseURL    string
	collection string
	apiKey     string // optional bearer for managed Qdrant Cloud
	metric     string // Cosine | Dot | Euclid
	dimension  int
	inited     bool
}

// Init reads config + ensures the collection exists. Expected
// config keys:
//
//	base_url:   "http://localhost:6333"  (default; qdrant REST port)
//	collection: "loamss_memory"           (default; per-instance namespace)
//	dimension:  1536                       (required)
//	metric:     "Cosine"                   (default; "Dot" or "Euclid" also accepted)
//	api_key:    ""                         (optional; for Qdrant Cloud)
//	timeout:    "30s"                     (default; per-request HTTP timeout)
func (a *Adapter) Init(ctx context.Context, config map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	dim := intField(config, "dimension", 0)
	if dim <= 0 {
		return errors.New("qdrant: config requires `dimension` (positive int)")
	}
	a.baseURL = strings.TrimRight(stringField(config, "base_url", defaultBaseURL), "/")
	a.collection = stringField(config, "collection", defaultCollection)
	a.apiKey = stringField(config, "api_key", "")
	a.metric = stringField(config, "metric", "Cosine")
	switch a.metric {
	case "Cosine", "Dot", "Euclid":
	default:
		return fmt.Errorf("qdrant: metric %q invalid (use Cosine | Dot | Euclid)", a.metric)
	}
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

// ensureCollectionLocked probes /collections/{name}; creates if 404.
func (a *Adapter) ensureCollectionLocked(ctx context.Context) error {
	resp, err := a.do(ctx, http.MethodGet, "/collections/"+a.collection, nil)
	if err != nil {
		return fmt.Errorf("qdrant: probing collection: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("qdrant: unexpected status %d probing collection", resp.StatusCode)
	}

	body, err := json.Marshal(map[string]any{
		"vectors": map[string]any{
			"size":     a.dimension,
			"distance": a.metric,
		},
	})
	if err != nil {
		return fmt.Errorf("qdrant: encoding create request: %w", err)
	}
	resp2, err := a.do(ctx, http.MethodPut, "/collections/"+a.collection, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("qdrant: creating collection: %w", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp2.Body)
		return fmt.Errorf("qdrant: create collection status %d: %s",
			resp2.StatusCode, string(respBytes))
	}
	return nil
}

// --- writes ---------------------------------------------------------------

// Upsert inserts or replaces a single point.
func (a *Adapter) Upsert(
	ctx context.Context, id string, vector []float32, metadata map[string]any,
) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	if len(vector) != a.dimension {
		return fmt.Errorf("%w: got %d, expected %d", memory.ErrDimensionMismatch, len(vector), a.dimension)
	}
	return a.batchUpsertLocked(ctx, []memory.Entry{{ID: id, Vector: vector, Metadata: metadata}})
}

// BatchUpsert sends many points in one PUT.
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
	points := make([]map[string]any, len(entries))
	for i, e := range entries {
		if len(e.Vector) != a.dimension {
			return fmt.Errorf("%w: entry %s got %d, expected %d",
				memory.ErrDimensionMismatch, e.ID, len(e.Vector), a.dimension)
		}
		// Qdrant requires UUID / unsigned-int point IDs. The runtime
		// hands us arbitrary strings (ULIDs in practice). Derive a
		// deterministic UUIDv5 for the Qdrant id and stash the
		// original loamss id in the payload for round-trips.
		payload := map[string]any{loamssIDKey: e.ID}
		for k, v := range e.Metadata {
			if k == loamssIDKey {
				continue // reserved
			}
			payload[k] = v
		}
		points[i] = map[string]any{
			"id":      toQdrantID(e.ID),
			"vector":  e.Vector,
			"payload": payload,
		}
	}
	body, err := json.Marshal(map[string]any{"points": points})
	if err != nil {
		return fmt.Errorf("qdrant: encoding upsert: %w", err)
	}
	// `?wait=true` blocks until the index is updated — without it
	// a Get immediately after Upsert can race. v0.1 trades latency
	// for consistency; an "async" option could go through a config
	// flag later if benchmarks demand it.
	resp, err := a.do(ctx, http.MethodPut,
		"/collections/"+a.collection+"/points?wait=true",
		bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("qdrant: upsert: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant: upsert status %d: %s", resp.StatusCode, string(respBytes))
	}
	return nil
}

// --- reads ----------------------------------------------------------------

// Get fetches a single point by id, or returns ErrNotFound.
func (a *Adapter) Get(ctx context.Context, id string) (*memory.Entry, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"ids":          []string{toQdrantID(id)},
		"with_payload": true,
		"with_vector":  true,
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant: encoding get: %w", err)
	}
	resp, err := a.do(ctx, http.MethodPost,
		"/collections/"+a.collection+"/points",
		bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("qdrant: get: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("qdrant: get status %d: %s", resp.StatusCode, string(respBytes))
	}

	var out struct {
		Result []point `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("qdrant: decoding get: %w", err)
	}
	if len(out.Result) == 0 {
		return nil, fmt.Errorf("%w: %s", memory.ErrNotFound, id)
	}
	p := out.Result[0]
	loamssID, payload := unstashLoamssID(p.Payload, id)
	return &memory.Entry{
		ID:       loamssID,
		Vector:   p.Vector,
		Metadata: payload,
	}, nil
}

// Search returns up to k nearest neighbours to query, with
// optional metadata equality filtering.
//
// Distance: Qdrant returns *scores* (higher = closer for Cosine;
// lower = closer for Euclid). The SPI calls for distances where
// smaller = closer regardless of metric, so we invert Cosine
// (1 - score) and pass Euclid through.
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
		"vector":       query,
		"limit":        k,
		"with_payload": true,
		"with_vector":  true,
	}
	if len(filter.Equals) > 0 {
		// Qdrant's filter shape: { must: [ {key, match:{value}} ] }
		// Multiple keys in Equals all match (AND).
		conditions := make([]map[string]any, 0, len(filter.Equals))
		for key, val := range filter.Equals {
			conditions = append(conditions, map[string]any{
				"key":   key,
				"match": map[string]any{"value": val},
			})
		}
		payload["filter"] = map[string]any{"must": conditions}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("qdrant: encoding search: %w", err)
	}
	resp, err := a.do(ctx, http.MethodPost,
		"/collections/"+a.collection+"/points/search",
		bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("qdrant: search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("qdrant: search status %d: %s",
			resp.StatusCode, string(respBytes))
	}

	var out struct {
		Result []point `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("qdrant: decoding search: %w", err)
	}

	hits := make([]memory.SearchHit, 0, len(out.Result))
	for _, p := range out.Result {
		dist := p.Score
		if a.metric == "Cosine" {
			// Cosine score = 1 - distance, so distance = 1 - score.
			dist = 1 - p.Score
		}
		// Unstash the original loamss ID from the payload. If
		// the payload is missing it (somebody wrote points
		// outside this adapter), fall back to the UUID string.
		loamssID, payload := unstashLoamssID(p.Payload, fmt.Sprintf("%v", p.ID))
		hits = append(hits, memory.SearchHit{
			ID:       loamssID,
			Vector:   p.Vector,
			Metadata: payload,
			Distance: dist,
		})
	}
	return hits, nil
}

// Delete removes one point. Idempotent — Qdrant returns 200 on
// missing ids.
func (a *Adapter) Delete(ctx context.Context, id string) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"points": []string{toQdrantID(id)}})
	if err != nil {
		return fmt.Errorf("qdrant: encoding delete: %w", err)
	}
	resp, err := a.do(ctx, http.MethodPost,
		"/collections/"+a.collection+"/points/delete?wait=true",
		bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("qdrant: delete: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant: delete status %d: %s", resp.StatusCode, string(respBytes))
	}
	return nil
}

// --- introspection --------------------------------------------------------

// Stats returns count + dimension + backend info.
func (a *Adapter) Stats(ctx context.Context) (memory.Stats, error) {
	if err := a.requireInited(); err != nil {
		return memory.Stats{}, err
	}
	resp, err := a.do(ctx, http.MethodPost,
		"/collections/"+a.collection+"/points/count",
		strings.NewReader(`{"exact": true}`))
	if err != nil {
		return memory.Stats{}, fmt.Errorf("qdrant: count: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return memory.Stats{}, fmt.Errorf("qdrant: count status %d: %s",
			resp.StatusCode, string(respBytes))
	}
	var cr struct {
		Result struct {
			Count int64 `json:"count"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return memory.Stats{}, fmt.Errorf("qdrant: decoding count: %w", err)
	}
	return memory.Stats{
		Count:     cr.Result.Count,
		Dimension: a.dimension,
		BackendInfo: map[string]string{
			"backend":    "qdrant",
			"collection": a.collection,
			"metric":     a.metric,
		},
	}, nil
}

// HealthCheck hits /healthz.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := a.do(pingCtx, http.MethodGet, "/healthz", nil)
	if err != nil {
		return fmt.Errorf("%w: %v", memory.ErrConnectionLost, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant: healthz status %d: %s",
			resp.StatusCode, string(respBytes))
	}
	return nil
}

// Close flips the inited flag.
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
		return errors.New("qdrant: adapter not initialised (call Init first)")
	}
	return nil
}

func (a *Adapter) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.apiKey != "" {
		req.Header.Set("api-key", a.apiKey)
	}
	return a.httpClient.Do(req)
}

// unstashLoamssID extracts the original loamss ID from a Qdrant
// payload + returns the payload with the marker key removed. If
// the payload doesn't carry our marker (legacy / externally-
// written points), the fallback string is used.
func unstashLoamssID(payload map[string]any, fallback string) (string, map[string]any) {
	if payload == nil {
		return fallback, nil
	}
	v, ok := payload[loamssIDKey]
	if !ok {
		return fallback, payload
	}
	id, ok := v.(string)
	if !ok {
		return fallback, payload
	}
	// Return a copy without the marker so the caller can treat
	// payload as user metadata.
	clean := make(map[string]any, len(payload)-1)
	for k, val := range payload {
		if k == loamssIDKey {
			continue
		}
		clean[k] = val
	}
	if len(clean) == 0 {
		clean = nil
	}
	return id, clean
}

// --- response shapes -------------------------------------------------------

type point struct {
	ID      any            `json:"id"`
	Score   float32        `json:"score"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload"`
}

// --- config helpers --------------------------------------------------------

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

func intField(config map[string]any, key string, fallback int) int {
	v, ok := config[key]
	if !ok {
		return fallback
	}
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	}
	return fallback
}
