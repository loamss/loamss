// Package sqlite implements the memory:sqlite adapter — a SQLite-backed
// vector store with brute-force k-NN search.
//
// Persistence: a single SQLite database file. Vectors are stored as
// packed little-endian float32 blobs; metadata as JSON; one row per
// entry.
//
// Search: brute force. Every Search reads all rows, computes cosine
// distance to the query, and returns the top-k. O(N · D) per query.
// Acceptable up to ~100k entries; at that scale, plan to swap in a
// sqlite-vec-extension-backed adapter (different adapter id, same SPI).
//
// Driver: modernc.org/sqlite, the pure-Go SQLite transpilation. No
// CGO, no platform headers, clean cross-compilation. Single binary
// remains single binary.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
)

// adapterID is the canonical id under which this adapter registers.
const adapterID = "memory:sqlite"

func init() {
	memory.Register(adapterID, func() memory.Adapter { return &Adapter{} })
}

// Adapter is the memory:sqlite concrete adapter. Zero value is
// unusable; call Init before any other method. After Init, all
// methods are safe for concurrent use; database/sql's pool handles
// the concurrency for us.
type Adapter struct {
	mu     sync.RWMutex
	db     *sql.DB
	path   string
	inited bool

	// dimension is the fixed vector dimension for this store. Lazy
	// set on the first Upsert (or loaded from meta on Init if the
	// store has prior entries). Protected by mu.
	dimension int
}

// SQL strings kept as constants for readability and to make schema
// changes obvious in diffs.
const (
	schemaSQL = `
CREATE TABLE IF NOT EXISTS entries (
    id          TEXT    PRIMARY KEY,
    vector      BLOB    NOT NULL,
    metadata    TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_entries_updated ON entries(updated_at);

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

	upsertSQL = `
INSERT INTO entries (id, vector, metadata, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    vector = excluded.vector,
    metadata = excluded.metadata,
    updated_at = excluded.updated_at;
`
)

// Init opens (or creates) the database at config["path"], applies
// the schema, and loads the persisted vector dimension if any.
//
// Required config:
//
//	path: <filesystem path>
func (a *Adapter) Init(ctx context.Context, config map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	pathRaw, ok := config["path"]
	if !ok {
		return errors.New("memory:sqlite: missing required config: path")
	}
	path, ok := pathRaw.(string)
	if !ok || path == "" {
		return fmt.Errorf("memory:sqlite: path must be a non-empty string (got %T)", pathRaw)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("memory:sqlite: resolving path %q: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return fmt.Errorf("memory:sqlite: creating parent dir: %w", err)
	}

	// Pragmas: WAL for concurrent readers + writer, NORMAL sync for
	// performance with acceptable durability for our use case (we
	// fsync via the underlying storage adapter for the audit cold
	// store separately).
	dsn := "file:" + abs + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("memory:sqlite: opening database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return fmt.Errorf("memory:sqlite: pinging database: %w", err)
	}

	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		_ = db.Close()
		return fmt.Errorf("memory:sqlite: applying schema: %w", err)
	}

	a.db = db
	a.path = abs

	if err := a.loadDimensionLocked(ctx); err != nil {
		_ = db.Close()
		a.db = nil
		return err
	}

	a.inited = true
	return nil
}

func (a *Adapter) loadDimensionLocked(ctx context.Context) error {
	var s string
	err := a.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'dimension'`).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memory:sqlite: reading dimension: %w", err)
	}
	d, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("memory:sqlite: parsing dimension %q: %w", s, err)
	}
	a.dimension = d
	return nil
}

// checkOrSetDimension validates a new vector's dimension. On the
// first ever write, persists the dimension to meta. On subsequent
// writes, returns ErrDimensionMismatch if mismatched.
func (a *Adapter) checkOrSetDimension(ctx context.Context, want int) error {
	a.mu.RLock()
	have := a.dimension
	a.mu.RUnlock()

	if have != 0 {
		if want != have {
			return fmt.Errorf("%w: got %d, want %d", memory.ErrDimensionMismatch, want, have)
		}
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dimension != 0 {
		if want != a.dimension {
			return fmt.Errorf("%w: got %d, want %d", memory.ErrDimensionMismatch, want, a.dimension)
		}
		return nil
	}
	if _, err := a.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO meta (key, value) VALUES ('dimension', ?)`,
		strconv.Itoa(want)); err != nil {
		return fmt.Errorf("memory:sqlite: persisting dimension: %w", err)
	}
	a.dimension = want
	return nil
}

// --- Writes ------------------------------------------------------------

// Upsert inserts or replaces a single entry.
func (a *Adapter) Upsert(ctx context.Context, id string, vector []float32, metadata map[string]any) error {
	if err := a.assertInited(); err != nil {
		return err
	}
	if id == "" {
		return errors.New("memory:sqlite: empty id")
	}
	if len(vector) == 0 {
		return errors.New("memory:sqlite: empty vector")
	}
	if err := a.checkOrSetDimension(ctx, len(vector)); err != nil {
		return err
	}

	metaJSON, err := encodeMetadata(metadata)
	if err != nil {
		return err
	}
	now := time.Now().UnixNano()

	if _, err := a.db.ExecContext(ctx, upsertSQL,
		id, packVector(vector), metaJSON, now, now); err != nil {
		return fmt.Errorf("memory:sqlite: upsert %s: %w", id, err)
	}
	return nil
}

// BatchUpsert inserts or replaces many entries inside a single
// transaction. All-or-nothing in v0.1.
func (a *Adapter) BatchUpsert(ctx context.Context, entries []memory.Entry) error {
	if err := a.assertInited(); err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	// Validate each entry up front (dimension + id + non-empty
	// vector) before opening a transaction. Cheaper than aborting
	// a tx on bad input.
	for i, e := range entries {
		if e.ID == "" {
			return fmt.Errorf("memory:sqlite: entry[%d] has empty id", i)
		}
		if len(e.Vector) == 0 {
			return fmt.Errorf("memory:sqlite: entry[%d] (%s) has empty vector", i, e.ID)
		}
		if err := a.checkOrSetDimension(ctx, len(e.Vector)); err != nil {
			return fmt.Errorf("entry[%d] (%s): %w", i, e.ID, err)
		}
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memory:sqlite: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, upsertSQL)
	if err != nil {
		return fmt.Errorf("memory:sqlite: prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	now := time.Now().UnixNano()
	for _, e := range entries {
		metaJSON, err := encodeMetadata(e.Metadata)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx,
			e.ID, packVector(e.Vector), metaJSON, now, now); err != nil {
			return fmt.Errorf("memory:sqlite: exec %s: %w", e.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memory:sqlite: commit: %w", err)
	}
	return nil
}

// --- Reads -------------------------------------------------------------

// Get returns the entry for a known id, or ErrNotFound.
func (a *Adapter) Get(ctx context.Context, id string) (*memory.Entry, error) {
	if err := a.assertInited(); err != nil {
		return nil, err
	}
	var (
		vecBlob  []byte
		metaJSON sql.NullString
	)
	err := a.db.QueryRowContext(ctx,
		`SELECT vector, metadata FROM entries WHERE id = ?`, id,
	).Scan(&vecBlob, &metaJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", memory.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("memory:sqlite: query %s: %w", id, err)
	}
	meta, err := decodeMetadata(metaJSON)
	if err != nil {
		return nil, err
	}
	return &memory.Entry{
		ID:       id,
		Vector:   unpackVector(vecBlob),
		Metadata: meta,
	}, nil
}

// Search returns up to k nearest-neighbor entries to the query
// vector, ordered by ascending cosine distance.
//
// Distance metric: cosine. Defined as 1 - (q · v) / (|q| · |v|).
// 0 means identical direction; 1 means orthogonal; 2 means
// opposite. Lower is closer.
func (a *Adapter) Search(ctx context.Context, query []float32, k int, filter memory.MetadataFilter) ([]memory.SearchHit, error) {
	if err := a.assertInited(); err != nil {
		return nil, err
	}
	if k <= 0 {
		return nil, nil
	}

	a.mu.RLock()
	have := a.dimension
	a.mu.RUnlock()
	if have != 0 && len(query) != have {
		return nil, fmt.Errorf("%w: query has %d, store has %d", memory.ErrDimensionMismatch, len(query), have)
	}

	queryNorm := norm(query)
	if queryNorm == 0 {
		return nil, errors.New("memory:sqlite: query vector is zero")
	}

	rows, err := a.db.QueryContext(ctx, `SELECT id, vector, metadata FROM entries`)
	if err != nil {
		return nil, fmt.Errorf("memory:sqlite: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	top := make([]memory.SearchHit, 0, k)
	for rows.Next() {
		var id string
		var vecBlob []byte
		var metaJSON sql.NullString
		if err := rows.Scan(&id, &vecBlob, &metaJSON); err != nil {
			return nil, fmt.Errorf("memory:sqlite: scan: %w", err)
		}
		meta, err := decodeMetadata(metaJSON)
		if err != nil {
			return nil, err
		}
		if !matchesFilter(meta, filter) {
			continue
		}
		v := unpackVector(vecBlob)
		vNorm := norm(v)
		if vNorm == 0 {
			continue
		}
		dist := 1 - dotProduct(query, v)/(queryNorm*vNorm)
		top = insertHit(top, memory.SearchHit{
			ID:       id,
			Vector:   v,
			Metadata: meta,
			Distance: dist,
		}, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return top, nil
}

// Delete removes an entry. Idempotent.
func (a *Adapter) Delete(ctx context.Context, id string) error {
	if err := a.assertInited(); err != nil {
		return err
	}
	if _, err := a.db.ExecContext(ctx, `DELETE FROM entries WHERE id = ?`, id); err != nil {
		return fmt.Errorf("memory:sqlite: delete %s: %w", id, err)
	}
	return nil
}

// Stats reports counts and the persisted dimension.
func (a *Adapter) Stats(ctx context.Context) (memory.Stats, error) {
	if err := a.assertInited(); err != nil {
		return memory.Stats{}, err
	}
	a.mu.RLock()
	dim := a.dimension
	a.mu.RUnlock()

	var count int64
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries`).Scan(&count); err != nil {
		return memory.Stats{}, fmt.Errorf("memory:sqlite: count: %w", err)
	}
	return memory.Stats{
		Count:     count,
		Dimension: dim,
		BackendInfo: map[string]string{
			"driver": "modernc.org/sqlite",
			"path":   a.path,
		},
	}, nil
}

// HealthCheck pings the database.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	if err := a.assertInited(); err != nil {
		return err
	}
	return a.db.PingContext(ctx)
}

// Close releases the database handle.
func (a *Adapter) Close(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.db == nil {
		return nil
	}
	err := a.db.Close()
	a.db = nil
	a.inited = false
	return err
}

func (a *Adapter) assertInited() error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.inited {
		return errors.New("memory:sqlite: adapter used before Init")
	}
	return nil
}

// --- Encoding helpers --------------------------------------------------

func packVector(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func unpackVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func encodeMetadata(m map[string]any) (sql.NullString, error) {
	if m == nil {
		return sql.NullString{}, nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("memory:sqlite: encoding metadata: %w", err)
	}
	return sql.NullString{String: string(data), Valid: true}, nil
}

func decodeMetadata(ns sql.NullString) (map[string]any, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(ns.String), &m); err != nil {
		return nil, fmt.Errorf("memory:sqlite: decoding metadata: %w", err)
	}
	return m, nil
}

// --- Distance helpers --------------------------------------------------

func dotProduct(a, b []float32) float32 {
	var d float32
	for i := range a {
		d += a[i] * b[i]
	}
	return d
}

func norm(v []float32) float32 {
	return float32(math.Sqrt(float64(dotProduct(v, v))))
}

// --- Filter ------------------------------------------------------------

func matchesFilter(metadata map[string]any, filter memory.MetadataFilter) bool {
	if len(filter.Equals) == 0 {
		return true
	}
	for key, want := range filter.Equals {
		got, ok := metadata[key]
		if !ok {
			return false
		}
		// JSON numbers come back as float64; allow comparison with
		// any numeric type the caller supplied.
		if !valuesEqual(got, want) {
			return false
		}
	}
	return true
}

func valuesEqual(a, b any) bool {
	if a == b {
		return true
	}
	// Numeric equivalence: caller may pass int, we may have float64.
	af, aok := asFloat(a)
	bf, bok := asFloat(b)
	if aok && bok {
		return af == bf
	}
	return false
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// --- Top-k maintenance -------------------------------------------------

// insertHit maintains a sorted slice of at most k SearchHits, sorted
// by ascending Distance. O(k) per insertion. For small k this is
// faster than a heap due to cache effects.
func insertHit(hits []memory.SearchHit, h memory.SearchHit, k int) []memory.SearchHit {
	if len(hits) < k {
		hits = append(hits, h)
		// Sort the newly-extended slice by ascending Distance.
		for i := len(hits) - 1; i > 0 && hits[i].Distance < hits[i-1].Distance; i-- {
			hits[i], hits[i-1] = hits[i-1], hits[i]
		}
		return hits
	}
	if h.Distance >= hits[len(hits)-1].Distance {
		return hits
	}
	// Replace the worst hit and shuffle into place.
	hits[len(hits)-1] = h
	for i := len(hits) - 1; i > 0 && hits[i].Distance < hits[i-1].Distance; i-- {
		hits[i], hits[i-1] = hits[i-1], hits[i]
	}
	return hits
}
