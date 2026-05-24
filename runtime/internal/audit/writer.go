package audit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"
)

// Writer is the audit log append + query surface. SQLite-backed for
// the hot store; the cold store (rotation to user storage adapter)
// is a separate component layered on top in a future commit.
type Writer interface {
	// Append validates, IDs, timestamps, chains, hashes, and
	// persists the entry. Returns the fully-populated entry on
	// success.
	Append(ctx context.Context, e Entry) (*Entry, error)

	// Query returns entries matching the filter, ordered oldest
	// first. Use a Limit to bound large queries.
	Query(ctx context.Context, filter Filter) ([]Entry, error)

	// Latest returns the most recent entry, or nil if the log is
	// empty.
	Latest(ctx context.Context) (*Entry, error)

	// Verify replays the hash chain from genesis through head and
	// reports any inconsistency. Linear in the chain length.
	Verify(ctx context.Context) (*VerifyResult, error)

	// Close releases resources. Multiple calls are safe.
	Close(ctx context.Context) error
}

// Filter narrows a Query. Zero-valued fields mean "don't filter on
// this dimension".
type Filter struct {
	Since     time.Time
	Until     time.Time
	Types     []string
	ActorKind ActorKind
	ActorID   string
	SubjectID string
	Outcomes  []Outcome
	Limit     int // 0 means no explicit limit; the writer applies a sensible default

	// Reverse asks the writer to order results by id DESC instead of
	// ASC. Used by tail-style queries: the writer applies the limit
	// after sorting DESC, so the result is the *last* N matching
	// entries rather than the first.
	Reverse bool
}

// SQLite is the hot-store Writer implementation backed by a
// SQLite database. Embeddable so other writers can compose it
// (e.g., a future writer that fans out to a cold-store rotator
// after appending here).
type SQLite struct {
	mu       sync.Mutex
	db       *sql.DB
	path     string
	lastHash string
	ulidEnt  *ulid.MonotonicEntropy
}

// OpenSQLite constructs and initializes a SQLite-backed Writer.
//
// The path is the on-disk audit database file. Its parent directory
// is created (0700) if needed. The schema is applied idempotently;
// repeated opens of the same path resume the chain at the existing
// head.
func OpenSQLite(ctx context.Context, path string) (*SQLite, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("audit: resolving path %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("audit: creating parent dir: %w", err)
	}

	dsn := "file:" + abs + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("audit: opening database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("audit: pinging database: %w", err)
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("audit: applying schema: %w", err)
	}

	w := &SQLite{
		db:      db,
		path:    abs,
		ulidEnt: ulid.Monotonic(rand.Reader, 0),
	}
	if err := w.loadHeadHash(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return w, nil
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS audit_entries (
    id            TEXT PRIMARY KEY,
    timestamp     TEXT NOT NULL,
    type          TEXT NOT NULL,
    actor_kind    TEXT NOT NULL,
    actor_id      TEXT NOT NULL,
    subject_kind  TEXT,
    subject_id    TEXT,
    outcome       TEXT NOT NULL,
    data_json     TEXT,
    context_json  TEXT,
    prev_hash     TEXT NOT NULL,
    hash          TEXT NOT NULL UNIQUE
);

CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_entries(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_type      ON audit_entries(type);
CREATE INDEX IF NOT EXISTS idx_audit_actor     ON audit_entries(actor_kind, actor_id);
CREATE INDEX IF NOT EXISTS idx_audit_outcome   ON audit_entries(outcome);
`

func (w *SQLite) loadHeadHash(ctx context.Context) error {
	var h sql.NullString
	err := w.db.QueryRowContext(ctx,
		`SELECT hash FROM audit_entries ORDER BY id DESC LIMIT 1`,
	).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		w.lastHash = GenesisHash
		return nil
	}
	if err != nil {
		return fmt.Errorf("audit: loading head hash: %w", err)
	}
	if !h.Valid || h.String == "" {
		w.lastHash = GenesisHash
		return nil
	}
	w.lastHash = h.String
	return nil
}

// --- Append ------------------------------------------------------------

// Append validates the supplied entry, assigns id/timestamp/prev_hash,
// computes the chained hash, and persists the row. Returns the
// fully-populated Entry on success.
func (w *SQLite) Append(ctx context.Context, e Entry) (*Entry, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now().UTC()
	id, err := w.nextID(now)
	if err != nil {
		return nil, err
	}

	e.ID = id
	e.Timestamp = now
	e.PrevHash = w.lastHash

	hash, err := computeHash(e.PrevHash, e)
	if err != nil {
		return nil, err
	}
	e.Hash = hash

	dataJSON, err := marshalNullableJSON(e.Data)
	if err != nil {
		return nil, err
	}
	contextJSON, err := marshalNullableJSON(e.Context)
	if err != nil {
		return nil, err
	}
	var subjKind, subjID sql.NullString
	if e.Subject != nil {
		subjKind = sql.NullString{String: string(e.Subject.Kind), Valid: true}
		subjID = sql.NullString{String: e.Subject.ID, Valid: true}
	}

	if _, err := w.db.ExecContext(ctx, `
        INSERT INTO audit_entries (
            id, timestamp, type, actor_kind, actor_id,
            subject_kind, subject_id, outcome,
            data_json, context_json, prev_hash, hash
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `,
		e.ID, e.Timestamp.Format(time.RFC3339Nano), e.Type,
		string(e.Actor.Kind), e.Actor.ID,
		subjKind, subjID, string(e.Outcome),
		dataJSON, contextJSON, e.PrevHash, e.Hash,
	); err != nil {
		return nil, fmt.Errorf("audit: insert: %w", err)
	}

	w.lastHash = e.Hash
	return &e, nil
}

func (w *SQLite) nextID(now time.Time) (string, error) {
	u, err := ulid.New(ulid.Timestamp(now), w.ulidEnt)
	if err != nil {
		return "", fmt.Errorf("audit: generating ULID: %w", err)
	}
	return "aud-" + u.String(), nil
}

// marshalNullableJSON serializes v to JSON, returning an invalid
// NullString for empty / null inputs. Critically, this catches Go's
// typed-nil-pointer-in-interface pitfall: e.Context is a *Context;
// when it's nil, the value passed to this function is `any` wrapping
// a non-nil type with a nil value, so the bare `v == nil` comparison
// returns false. Reflect catches the typed nil and treats it
// equivalently to an untyped nil so the SQL column becomes NULL.
//
// Without this, nil pointer fields are marshaled as the JSON literal
// "null" and stored as a string, breaking hash-chain integrity (the
// pre-store entry had Context=nil omitted from the canonical JSON;
// the post-store entry has Context=&Context{} which appears as an
// empty object). Caught by TestDebug_AppendVsScanCanonicalDiff.
func marshalNullableJSON(v any) (sql.NullString, error) {
	if v == nil {
		return sql.NullString{}, nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Chan, reflect.Func:
		if rv.IsNil() {
			return sql.NullString{}, nil
		}
	}
	// Empty maps round-trip awkwardly too; treat them as null at the
	// storage layer.
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return sql.NullString{}, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("audit: marshaling field: %w", err)
	}
	return sql.NullString{String: string(data), Valid: true}, nil
}

// --- Query / Latest ----------------------------------------------------

// Query reads entries matching filter, ordered ascending by id.
// A default Limit is applied if none is set.
func (w *SQLite) Query(ctx context.Context, filter Filter) ([]Entry, error) {
	q, args := buildQuery(filter)
	rows, err := w.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var entries []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, *e)
	}
	return entries, rows.Err()
}

// Latest returns the most recent entry by id ordering, or nil if
// the log is empty.
func (w *SQLite) Latest(ctx context.Context) (*Entry, error) {
	row := w.db.QueryRowContext(ctx, baseSelectSQL+` ORDER BY id DESC LIMIT 1`)
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// row is the minimal interface scanEntry needs — works with both
// *sql.Row and *sql.Rows.
type row interface {
	Scan(dest ...any) error
}

const baseSelectSQL = `SELECT id, timestamp, type, actor_kind, actor_id,
       subject_kind, subject_id, outcome,
       data_json, context_json, prev_hash, hash
       FROM audit_entries`

func scanEntry(r row) (*Entry, error) {
	var (
		e         Entry
		ts        string
		actorKind string
		outcome   string
		subjKind  sql.NullString
		subjID    sql.NullString
		dataJSON  sql.NullString
		ctxJSON   sql.NullString
	)
	if err := r.Scan(
		&e.ID, &ts, &e.Type, &actorKind, &e.Actor.ID,
		&subjKind, &subjID, &outcome,
		&dataJSON, &ctxJSON, &e.PrevHash, &e.Hash,
	); err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil, fmt.Errorf("audit: parsing timestamp %q: %w", ts, err)
	}
	e.Timestamp = t
	e.Actor.Kind = ActorKind(actorKind)
	e.Outcome = Outcome(outcome)
	if subjKind.Valid {
		e.Subject = &Subject{Kind: SubjectKind(subjKind.String), ID: subjID.String}
	}
	// Treat both empty strings and the literal "null" as absent —
	// older rows written before the typed-nil fix above may contain
	// "null" as a stored JSON value.
	if dataJSON.Valid && dataJSON.String != "" && dataJSON.String != "null" {
		if err := json.Unmarshal([]byte(dataJSON.String), &e.Data); err != nil {
			return nil, fmt.Errorf("audit: decoding data: %w", err)
		}
	}
	if ctxJSON.Valid && ctxJSON.String != "" && ctxJSON.String != "null" {
		var c Context
		if err := json.Unmarshal([]byte(ctxJSON.String), &c); err != nil {
			return nil, fmt.Errorf("audit: decoding context: %w", err)
		}
		e.Context = &c
	}
	return &e, nil
}

// buildQuery composes the SELECT for Query against the given filter.
// Returns the SQL string and the positional arguments.
func buildQuery(f Filter) (string, []any) {
	var (
		conds []string
		args  []any
	)
	if !f.Since.IsZero() {
		conds = append(conds, "timestamp >= ?")
		args = append(args, f.Since.UTC().Format(time.RFC3339Nano))
	}
	if !f.Until.IsZero() {
		conds = append(conds, "timestamp <= ?")
		args = append(args, f.Until.UTC().Format(time.RFC3339Nano))
	}
	if len(f.Types) > 0 {
		conds = append(conds, "type IN ("+placeholders(len(f.Types))+")")
		for _, t := range f.Types {
			args = append(args, t)
		}
	}
	if f.ActorKind != "" {
		conds = append(conds, "actor_kind = ?")
		args = append(args, string(f.ActorKind))
	}
	if f.ActorID != "" {
		conds = append(conds, "actor_id = ?")
		args = append(args, f.ActorID)
	}
	if f.SubjectID != "" {
		conds = append(conds, "subject_id = ?")
		args = append(args, f.SubjectID)
	}
	if len(f.Outcomes) > 0 {
		conds = append(conds, "outcome IN ("+placeholders(len(f.Outcomes))+")")
		for _, o := range f.Outcomes {
			args = append(args, string(o))
		}
	}

	q := baseSelectSQL
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	if f.Reverse {
		q += " ORDER BY id DESC"
	} else {
		q += " ORDER BY id ASC"
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 1000 // sensible default to keep oops queries bounded
	}
	q += fmt.Sprintf(" LIMIT %d", limit)
	return q, args
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, 2*n-1)
	for i := 0; i < n; i++ {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '?')
	}
	return string(out)
}

// --- Verify ------------------------------------------------------------

// Verify walks the chain from oldest to newest, recomputing each
// hash and comparing to the stored value. Returns the first break,
// or Valid=true if the whole chain is intact.
func (w *SQLite) Verify(ctx context.Context) (*VerifyResult, error) {
	rows, err := w.db.QueryContext(ctx, baseSelectSQL+` ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("audit: verify query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	prev := GenesisHash
	var checked int64
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		if e.PrevHash != prev {
			return &VerifyResult{
				EntriesChecked: checked,
				Valid:          false,
				BrokenAt:       e.ID,
				Reason:         fmt.Sprintf("prev_hash %s does not match expected %s", e.PrevHash, prev),
			}, nil
		}
		want, err := computeHash(prev, *e)
		if err != nil {
			return nil, fmt.Errorf("audit: recomputing hash at %s: %w", e.ID, err)
		}
		if want != e.Hash {
			return &VerifyResult{
				EntriesChecked: checked,
				Valid:          false,
				BrokenAt:       e.ID,
				Reason:         "stored hash differs from recomputed hash",
			}, nil
		}
		prev = e.Hash
		checked++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &VerifyResult{EntriesChecked: checked, Valid: true}, nil
}

// --- Close -------------------------------------------------------------

// Close shuts down the underlying database connection. Safe to call
// multiple times.
func (w *SQLite) Close(_ context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.db == nil {
		return nil
	}
	err := w.db.Close()
	w.db = nil
	return err
}

// Path returns the on-disk database path. Useful for tests and
// diagnostic output (`loamss doctor` etc.).
func (w *SQLite) Path() string { return w.path }
