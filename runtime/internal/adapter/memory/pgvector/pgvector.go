// Package pgvector implements the memory:pgvector adapter — a
// Postgres-backed vector store using the pgvector extension.
//
// Schema (one table; created lazily on Init):
//
//	loamss_memory_<table_suffix>(
//	    id          text primary key,
//	    vector      vector(<dim>) not null,
//	    metadata    jsonb,
//	    created_at  timestamptz not null default now(),
//	    updated_at  timestamptz not null default now()
//	)
//
// Search: native pgvector. Cosine distance by default (lower is
// closer); operator selected at query time via the config's
// `metric` field. ivfflat / hnsw indexes are operator-provisioned
// — we don't try to manage them on behalf of the user; the
// pg_settings, the data distribution, and the workload all
// dictate the right shape. The adapter works without an index
// (sequential scan) and benefits transparently once one is added.
//
// Choice of dependency:
//
//   - github.com/jackc/pgx/v5 — the de-facto Postgres driver in
//     the Go ecosystem. pure Go, well-maintained, MIT-licensed.
//     Already a dep we'd reach for any time we touched Postgres.
//
//   - github.com/pgvector/pgvector-go — official pgvector helper
//     that knows how to encode/decode vectors over the wire.
//     Tiny, pure Go, no transitive deps beyond pgx.
//
// Same dep-policy rationale as storage:s3: the alternative is
// hand-rolling the binary vector format and pgvector's text
// codec, which is error-prone and we'd own forever.
package pgvector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgv "github.com/pgvector/pgvector-go"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
)

const adapterID = "memory:pgvector"

func init() {
	memory.Register(adapterID, func() memory.Adapter { return &Adapter{} })
}

// Default identifier-shape constants. The table name is namespaced
// so multiple loamss instances can share a database without
// stepping on each other.
const (
	defaultTableSuffix = "default"
	defaultMetric      = "cosine" // cosine | l2 | inner
)

// Adapter is the memory:pgvector concrete adapter.
//
// Zero value is unusable; call Init before any other method.
// After Init, all methods are safe for concurrent use — pgxpool
// handles connection multiplexing.
type Adapter struct {
	mu        sync.RWMutex
	pool      *pgxpool.Pool
	table     string
	metric    string // cosine | l2 | inner
	dimension int    // configured vector dimension; required by pgvector schema
	inited    bool

	// cloudSQLCleanup is the close function the Cloud SQL
	// connector returned at Init, if any. Nil when not using
	// the Cloud SQL dial path. Called from Close.
	cloudSQLCleanup func() error
}

// Init reads config + opens a connection pool + ensures the schema
// exists. Expected config:
//
//	dsn:           "postgres://user:pass@host:5432/db?sslmode=disable"  (required;
//	               or set DATABASE_URL / LOAMSS_PGVECTOR_DSN env vars)
//	dimension:    1536    (required; cannot change after first write)
//	table_suffix: "shared" (optional; appended to "loamss_memory_" — lets
//	              multiple loamss instances share one database)
//	metric:       "cosine" (default; "l2" or "inner" also accepted)
//
// Init creates the table + pgvector extension if absent. The
// extension requires SUPERUSER on the database — the more
// production-friendly path is for the operator to `CREATE EXTENSION
// vector` once at provisioning time, and grant our user normal
// privileges. We try, swallow the perm error, and fall through to
// validating the extension is reachable.
func (a *Adapter) Init(ctx context.Context, config map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	dsn := optionalString(config, "dsn", "")
	if dsn == "" {
		return errors.New("pgvector: config requires `dsn` (or set DATABASE_URL / LOAMSS_PGVECTOR_DSN)")
	}
	dim := optionalInt(config, "dimension", 0)
	if dim <= 0 {
		return errors.New("pgvector: config requires `dimension` (a positive int matching the embedding model's output)")
	}
	suffix := optionalString(config, "table_suffix", defaultTableSuffix)
	if !validIdentifier(suffix) {
		return fmt.Errorf("pgvector: table_suffix %q must match [a-z0-9_]+", suffix)
	}
	metric := optionalString(config, "metric", defaultMetric)
	switch metric {
	case "cosine", "l2", "inner":
	default:
		return fmt.Errorf("pgvector: metric %q invalid (use cosine | l2 | inner)", metric)
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("pgvector: parsing dsn: %w", err)
	}
	// Modest defaults; operators tune via the DSN itself
	// (pool_max_conns / pool_min_conns / etc.).
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 10
	}
	// Optional: route through the Cloud SQL Connector for
	// passwordless IAM-based auth against Cloud SQL Postgres.
	// When cloud_sql_instance is unset this is a no-op and the
	// adapter behaves as a standard libpq client.
	cloudSQLCleanup, err := applyCloudSQLDialerIfConfigured(ctx, cfg, config)
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		if cloudSQLCleanup != nil {
			_ = cloudSQLCleanup()
		}
		return fmt.Errorf("pgvector: creating pool: %w", err)
	}

	a.pool = pool
	a.cloudSQLCleanup = cloudSQLCleanup
	a.table = "loamss_memory_" + suffix
	a.metric = metric
	a.dimension = dim
	a.inited = true

	if err := a.ensureSchemaLocked(ctx); err != nil {
		// On schema failure, drop the partially-built state so
		// subsequent calls fail fast rather than hitting a half-
		// configured adapter.
		pool.Close()
		a.inited = false
		a.pool = nil
		return err
	}
	return nil
}

// ensureSchemaLocked creates the pgvector extension + the
// per-instance table if either is missing. Idempotent: the IF NOT
// EXISTS clauses make repeated calls (across daemon restarts)
// no-ops.
func (a *Adapter) ensureSchemaLocked(ctx context.Context) error {
	// CREATE EXTENSION needs CREATE on the database. Many managed
	// Postgres services (RDS, Cloud SQL) pre-install pgvector and
	// don't permit re-CREATE; swallow the permission error and
	// rely on the subsequent table CREATE to surface the missing
	// extension as a clearer error.
	if _, err := a.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		// Continue; the next statement will fail if vector really
		// isn't there.
		_ = err
	}

	// Table identifier is validated by validIdentifier; safe to
	// interpolate. We could go through prepared statements but
	// pgx doesn't allow identifiers as bound parameters.
	create := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
    id          text       PRIMARY KEY,
    vector      vector(%d) NOT NULL,
    metadata    jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);`, a.table, a.dimension)
	if _, err := a.pool.Exec(ctx, create); err != nil {
		return fmt.Errorf("pgvector: creating table %s: %w", a.table, err)
	}
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
	metaJSON, err := encodeMetadata(metadata)
	if err != nil {
		return err
	}
	q := fmt.Sprintf(`
INSERT INTO %s (id, vector, metadata, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (id) DO UPDATE SET
    vector     = EXCLUDED.vector,
    metadata   = EXCLUDED.metadata,
    updated_at = now()`, a.table)
	_, err = a.pool.Exec(ctx, q, id, pgv.NewVector(vector), metaJSON)
	if err != nil {
		return mapErr(err, "upsert", id)
	}
	return nil
}

// BatchUpsert inserts/replaces many entries in a single round-trip.
// Implemented via pgx.Batch for one transaction submission instead
// of N transactions.
func (a *Adapter) BatchUpsert(ctx context.Context, entries []memory.Entry) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	q := fmt.Sprintf(`
INSERT INTO %s (id, vector, metadata, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (id) DO UPDATE SET
    vector     = EXCLUDED.vector,
    metadata   = EXCLUDED.metadata,
    updated_at = now()`, a.table)

	for _, e := range entries {
		if len(e.Vector) != a.dimension {
			return fmt.Errorf("%w: entry %s got %d, expected %d",
				memory.ErrDimensionMismatch, e.ID, len(e.Vector), a.dimension)
		}
		metaJSON, err := encodeMetadata(e.Metadata)
		if err != nil {
			return err
		}
		batch.Queue(q, e.ID, pgv.NewVector(e.Vector), metaJSON)
	}

	br := a.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for i := range entries {
		if _, err := br.Exec(); err != nil {
			return mapErr(err, "batch-upsert", entries[i].ID)
		}
	}
	return nil
}

// --- reads ----------------------------------------------------------------

// Get returns the entry for id or ErrNotFound.
func (a *Adapter) Get(ctx context.Context, id string) (*memory.Entry, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	q := fmt.Sprintf(`SELECT id, vector, metadata FROM %s WHERE id = $1`, a.table)

	var (
		gotID    string
		vec      pgv.Vector
		metaJSON []byte
	)
	err := a.pool.QueryRow(ctx, q, id).Scan(&gotID, &vec, &metaJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", memory.ErrNotFound, id)
	}
	if err != nil {
		return nil, mapErr(err, "get", id)
	}
	meta, err := decodeMetadata(metaJSON)
	if err != nil {
		return nil, err
	}
	return &memory.Entry{
		ID:       gotID,
		Vector:   vec.Slice(),
		Metadata: meta,
	}, nil
}

// Search returns up to k nearest-neighbor entries to query, with
// optional equality-filtering on metadata.
//
// Distance semantics: this adapter returns *distance* values where
// smaller = closer, regardless of which operator was selected. For
// cosine we use `1 - (a <=> b)` so the result is in [0, 2] with
// smaller = closer (pgvector's <=> returns cosine distance which
// is already smaller-is-closer; we just pass it through).
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

	op := metricOperator(a.metric)
	// Build WHERE clause from the filter's equality predicates.
	// We index args starting at $1 for the query vector, then $2+
	// for each filter binding.
	whereSQL := ""
	args := []any{pgv.NewVector(query)}
	for key, val := range filter.Equals {
		args = append(args, val)
		// metadata is jsonb; "key=val" matches when the JSON value
		// at metadata->>key equals the string-form of val. We
		// stringify in Go because pgx jsonb-text comparison is
		// the cleanest cross-type matcher.
		whereSQL += fmt.Sprintf(" AND metadata->>'%s' = ($%d)::text", escapeIdent(key), len(args))
	}

	q := fmt.Sprintf(`
SELECT id, vector, metadata, vector %s $1 AS distance
FROM %s
WHERE 1=1%s
ORDER BY distance ASC
LIMIT %d`, op, a.table, whereSQL, k)

	rows, err := a.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "search", "")
	}
	defer rows.Close()

	var hits []memory.SearchHit
	for rows.Next() {
		var (
			id       string
			vec      pgv.Vector
			metaJSON []byte
			distance float64
		)
		if err := rows.Scan(&id, &vec, &metaJSON, &distance); err != nil {
			return nil, mapErr(err, "search", "")
		}
		meta, err := decodeMetadata(metaJSON)
		if err != nil {
			return nil, err
		}
		hits = append(hits, memory.SearchHit{
			ID:       id,
			Vector:   vec.Slice(),
			Metadata: meta,
			Distance: float32(distance),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err, "search", "")
	}
	return hits, nil
}

// Delete removes the entry for id. Idempotent.
func (a *Adapter) Delete(ctx context.Context, id string) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	q := fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, a.table)
	if _, err := a.pool.Exec(ctx, q, id); err != nil {
		return mapErr(err, "delete", id)
	}
	return nil
}

// --- introspection --------------------------------------------------------

// Stats returns count + dimension + backend-version info.
func (a *Adapter) Stats(ctx context.Context) (memory.Stats, error) {
	if err := a.requireInited(); err != nil {
		return memory.Stats{}, err
	}
	var count int64
	q := fmt.Sprintf(`SELECT count(*) FROM %s`, a.table)
	if err := a.pool.QueryRow(ctx, q).Scan(&count); err != nil {
		return memory.Stats{}, mapErr(err, "stats", "")
	}
	var pgVersion string
	if err := a.pool.QueryRow(ctx, `SELECT version()`).Scan(&pgVersion); err != nil {
		// Best-effort; not fatal.
		pgVersion = ""
	}
	return memory.Stats{
		Count:     count,
		Dimension: a.dimension,
		BackendInfo: map[string]string{
			"postgres": shortPgVersion(pgVersion),
			"metric":   a.metric,
			"table":    a.table,
		},
	}, nil
}

// HealthCheck verifies the pool can still reach Postgres.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := a.pool.Ping(pingCtx); err != nil {
		return fmt.Errorf("%w: %v", memory.ErrConnectionLost, err)
	}
	return nil
}

// Close closes the pool and, if the Cloud SQL dialer was used,
// shuts down the connector's background refresh goroutines.
func (a *Adapter) Close(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pool != nil {
		a.pool.Close()
	}
	if a.cloudSQLCleanup != nil {
		_ = a.cloudSQLCleanup()
	}
	a.pool = nil
	a.cloudSQLCleanup = nil
	a.inited = false
	return nil
}

// --- helpers --------------------------------------------------------------

func (a *Adapter) requireInited() error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.inited {
		return errors.New("pgvector: adapter not initialised (call Init first)")
	}
	return nil
}

// metricOperator maps the config string to the pgvector distance
// operator. <=> is cosine, <-> is L2, <#> is negative inner
// product. All three return distances where smaller = closer
// (for inner, pgvector negates so the operator is consistent).
func metricOperator(metric string) string {
	switch metric {
	case "l2":
		return "<->"
	case "inner":
		return "<#>"
	default:
		return "<=>"
	}
}

// validIdentifier guards the table_suffix against SQL injection.
// We allow only [a-z0-9_]+ — paranoid but trivial to comply with.
func validIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// escapeIdent doubles single quotes so a metadata key with quotes
// can't escape into SQL. Used only for the JSON key-name in
// metadata->>'<key>'; values themselves are bound parameters.
func escapeIdent(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func encodeMetadata(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("pgvector: encoding metadata: %w", err)
	}
	return b, nil
}

func decodeMetadata(b []byte) (map[string]any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("pgvector: decoding metadata: %w", err)
	}
	return m, nil
}

func mapErr(err error, op, id string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s (op=%s)", memory.ErrNotFound, id, op)
	}
	return fmt.Errorf("pgvector %s %s: %w", op, id, err)
}

// shortPgVersion trims "PostgreSQL 16.1 on x86_64-pc-linux-gnu ..."
// down to "16.1" for the Stats.BackendInfo value. Best-effort —
// any failure to parse falls back to the raw string.
func shortPgVersion(full string) string {
	parts := strings.SplitN(full, " ", 3)
	if len(parts) >= 2 {
		return parts[1]
	}
	return full
}

// --- config helpers --------------------------------------------------------

func optionalString(config map[string]any, key, fallback string) string {
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

func optionalInt(config map[string]any, key string, fallback int) int {
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
