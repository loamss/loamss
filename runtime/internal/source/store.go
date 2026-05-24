package source

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"
)

// Configured is the persisted record for one user-configured source
// instance. A user can have multiple Configured rows with the same
// AdapterID (two Gmail accounts as "gmail-personal" and "gmail-work",
// for example); Name is the unique handle the user chose.
type Configured struct {
	// ID is a runtime-assigned ULID with a "src_" prefix. Stable
	// across renames and across `loamss source authenticate`.
	ID string `json:"id"`

	// Name is the user's chosen handle. Unique across all configured
	// sources. Used as the principal id in audit entries and as the
	// memory namespace the source writes into.
	Name string `json:"name"`

	// AdapterID is the registry id, e.g. "source:gmail".
	AdapterID string `json:"adapter_id"`

	// Config is the opaque per-instance config map the user supplied.
	// The source itself validates the shape at Init.
	Config map[string]any `json:"config,omitempty"`

	// Cursor is the source-defined incremental position from the
	// last successful sync. Opaque to the runtime.
	Cursor []byte `json:"-"`

	// LastSyncAt is when the last Sync finished, success or failure.
	// Zero value means "never synced".
	LastSyncAt time.Time `json:"last_sync_at,omitempty"`

	// LastSyncStatus is one of "success", "error", "running", or "".
	LastSyncStatus string `json:"last_sync_status,omitempty"`

	// LastSyncSummary is a compact JSON-encoded view of the last
	// SyncResult (without the cursor). Kept for `source show`.
	LastSyncSummary map[string]any `json:"last_sync_summary,omitempty"`

	// AddedAt is when the source was first added via `source add`.
	AddedAt time.Time `json:"added_at"`

	// UpdatedAt is when the row was last modified.
	UpdatedAt time.Time `json:"updated_at"`
}

// Store is the SQLite-backed persistence for configured sources.
// Shares runtime.db with the permission and capsule stores via SQLite
// WAL; concurrent writes serialize at the SQLite level. The
// source_schema_migrations table tracks this store's migration
// version independently.
type Store struct {
	db   *sql.DB
	path string

	// mu serializes writes within this process. Cross-process
	// writes serialize via SQLite's write lock.
	mu sync.Mutex

	// ulidMu + ulidEnt produce monotonic ULIDs for the ID column.
	ulidMu  sync.Mutex
	ulidEnt *ulid.MonotonicEntropy
}

// OpenStore creates or opens the source store at path (typically
// <data_dir>/runtime.db). The path is shared with permission.Store
// and capsule.Store; each owns different tables and different
// migration histories.
func OpenStore(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("source: resolving path %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("source: creating parent dir: %w", err)
	}

	dsn := "file:" + abs + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("source: opening database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("source: pinging database: %w", err)
	}

	s := &Store{
		db:      db,
		path:    abs,
		ulidEnt: ulid.Monotonic(rand.Reader, 0),
	}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Path returns the on-disk database path.
func (s *Store) Path() string { return s.path }

// --- migrations --------------------------------------------------------

var sourceMigrations = []string{
	// 1: initial sources table.
	`
CREATE TABLE IF NOT EXISTS sources (
    id                      TEXT PRIMARY KEY,
    name                    TEXT NOT NULL UNIQUE,
    adapter_id              TEXT NOT NULL,
    config_json             TEXT NOT NULL,
    cursor                  BLOB,
    last_sync_at            TEXT,
    last_sync_status        TEXT,
    last_sync_summary_json  TEXT,
    added_at                TEXT NOT NULL,
    updated_at              TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sources_adapter ON sources(adapter_id);
`,
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS source_schema_migrations (
            version    INTEGER PRIMARY KEY,
            applied_at TEXT NOT NULL
        )`); err != nil {
		return fmt.Errorf("source: creating schema_migrations: %w", err)
	}
	var current int
	row := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM source_schema_migrations`)
	if err := row.Scan(&current); err != nil {
		return fmt.Errorf("source: reading migration version: %w", err)
	}
	for i, sqlText := range sourceMigrations {
		version := i + 1
		if version <= current {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("source: begin migration tx: %w", err)
		}
		if _, err := tx.ExecContext(ctx, sqlText); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("source: applying migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO source_schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("source: recording migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("source: commit migration %d: %w", version, err)
		}
	}
	return nil
}

// --- sentinel errors ---------------------------------------------------

var (
	// ErrSourceNotFound is returned by Get/Delete/Update when no
	// configured source matches the lookup.
	ErrSourceNotFound = errors.New("source: not configured")

	// ErrSourceNameTaken is returned by Insert when a source with
	// the same name is already configured. Source names are user-
	// visible handles; silent overwriting would lose audit lineage.
	ErrSourceNameTaken = errors.New("source: name already in use")
)

// --- CRUD --------------------------------------------------------------

// Insert persists a new configured-source record. Fails with
// ErrSourceNameTaken if the Name is already in use. ID, AddedAt,
// UpdatedAt are filled by the store if zero.
func (s *Store) Insert(ctx context.Context, c Configured) (*Configured, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if c.Name == "" {
		return nil, errors.New("source: Name required")
	}
	if c.AdapterID == "" {
		return nil, errors.New("source: AdapterID required")
	}
	if c.ID == "" {
		c.ID = s.nextID()
	}
	now := time.Now().UTC()
	if c.AddedAt.IsZero() {
		c.AddedAt = now
	}
	c.UpdatedAt = now
	if c.Config == nil {
		c.Config = map[string]any{}
	}
	configJSON, err := json.Marshal(c.Config)
	if err != nil {
		return nil, fmt.Errorf("source: encoding config: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
        INSERT INTO sources (
            id, name, adapter_id, config_json, cursor,
            last_sync_at, last_sync_status, last_sync_summary_json,
            added_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.AdapterID, string(configJSON), c.Cursor,
		nil, nil, nil,
		c.AddedAt.Format(time.RFC3339Nano), c.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, fmt.Errorf("%w: %s", ErrSourceNameTaken, c.Name)
		}
		return nil, fmt.Errorf("source: inserting %s: %w", c.Name, err)
	}
	return &c, nil
}

// Get returns the configured source with the given Name. Returns
// ErrSourceNotFound when no record exists.
func (s *Store) Get(ctx context.Context, name string) (*Configured, error) {
	row := s.db.QueryRowContext(ctx, sourceSelectColumns+` WHERE name = ?`, name)
	c, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrSourceNotFound, name)
	}
	return c, err
}

// GetByID is like Get but looks up by the internal ULID.
func (s *Store) GetByID(ctx context.Context, id string) (*Configured, error) {
	row := s.db.QueryRowContext(ctx, sourceSelectColumns+` WHERE id = ?`, id)
	c, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrSourceNotFound, id)
	}
	return c, err
}

// List returns all configured sources, newest-first.
func (s *Store) List(ctx context.Context) ([]Configured, error) {
	rows, err := s.db.QueryContext(ctx, sourceSelectColumns+` ORDER BY added_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("source: listing: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Configured
	for rows.Next() {
		c, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// Delete removes a configured source by Name. Returns
// ErrSourceNotFound if no record matched. Cascade-cleanup of grants
// and stored credentials is the caller's responsibility — this
// method touches only the table row.
func (s *Store) Delete(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM sources WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("source: deleting %s: %w", name, err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("%w: %s", ErrSourceNotFound, name)
	}
	return nil
}

// UpdateCursor persists a new cursor for the named source.
func (s *Store) UpdateCursor(ctx context.Context, name string, cursor []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		`UPDATE sources SET cursor = ?, updated_at = ? WHERE name = ?`,
		cursor, time.Now().UTC().Format(time.RFC3339Nano), name)
	if err != nil {
		return fmt.Errorf("source: updating cursor for %s: %w", name, err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("%w: %s", ErrSourceNotFound, name)
	}
	return nil
}

// SetLastSync persists a sync attempt's outcome. summary is the
// last SyncResult (the cursor is persisted separately via
// UpdateCursor so the cursor write can be atomic with summary in
// the future without a schema change here).
func (s *Store) SetLastSync(ctx context.Context, name, status string, summary map[string]any, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("source: encoding sync summary: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE sources
            SET last_sync_at = ?, last_sync_status = ?, last_sync_summary_json = ?, updated_at = ?
            WHERE name = ?`,
		at.Format(time.RFC3339Nano), status, string(summaryJSON), now, name)
	if err != nil {
		return fmt.Errorf("source: updating last_sync for %s: %w", name, err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("%w: %s", ErrSourceNotFound, name)
	}
	return nil
}

// --- internals ---------------------------------------------------------

func (s *Store) nextID() string {
	s.ulidMu.Lock()
	defer s.ulidMu.Unlock()
	u := ulid.MustNew(ulid.Timestamp(time.Now().UTC()), s.ulidEnt)
	return "src_" + u.String()
}

const sourceSelectColumns = `SELECT id, name, adapter_id, config_json, cursor,
       last_sync_at, last_sync_status, last_sync_summary_json,
       added_at, updated_at
       FROM sources`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSource(r rowScanner) (*Configured, error) {
	var (
		c           Configured
		configJSON  string
		cursor      []byte
		lastAt      sql.NullString
		lastStatus  sql.NullString
		lastSummary sql.NullString
		addedStr    string
		updatedStr  string
	)
	if err := r.Scan(&c.ID, &c.Name, &c.AdapterID, &configJSON, &cursor,
		&lastAt, &lastStatus, &lastSummary, &addedStr, &updatedStr); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(configJSON), &c.Config); err != nil {
		return nil, fmt.Errorf("source: decoding config for %s: %w", c.Name, err)
	}
	c.Cursor = cursor
	if lastAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, lastAt.String); err == nil {
			c.LastSyncAt = t
		}
	}
	if lastStatus.Valid {
		c.LastSyncStatus = lastStatus.String
	}
	if lastSummary.Valid && lastSummary.String != "" {
		if err := json.Unmarshal([]byte(lastSummary.String), &c.LastSyncSummary); err != nil {
			return nil, fmt.Errorf("source: decoding sync summary for %s: %w", c.Name, err)
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, addedStr); err == nil {
		c.AddedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedStr); err == nil {
		c.UpdatedAt = t
	}
	return &c, nil
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
