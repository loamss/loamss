package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/loamss/loamss/runtime/internal/database"
)

// RuntimeStateStore is a tiny key/value table inside runtime.db used
// for per-instance state that needs to survive restarts but doesn't
// merit its own package + schema + migration. Today it holds a single
// key — `setup_token_consumed` — recording that the SetupTokenGate
// has burned its token. Adding more keys is fine; the table is
// general-purpose.
//
// Why this exists: when the runtime is on Cloud Run / Fly / GKE, the
// container filesystem is wiped on every cold start. The file-based
// `.setup-consumed` sentinel that works on laptop installs disappears
// on first restart, re-opening the gate. Storing the same signal in
// the runtime DB (which lives on Cloud SQL or another durable backend
// in the cloud profile) makes consumption survive cold starts.
//
// Single table, no joins. Driver-agnostic schema — TEXT columns
// throughout, same as the rest of the runtime's tables.
type RuntimeStateStore struct {
	db     *database.DB
	dbMeta *database.Database
}

// OpenRuntimeStateStoreWith creates a state store on top of an
// already-open Database (the same handle every other subsystem
// shares via cli/start.go). Caller retains ownership of the DB.
func OpenRuntimeStateStoreWith(ctx context.Context, db *database.Database) (*RuntimeStateStore, error) {
	if db == nil || db.Conn() == nil {
		return nil, errors.New("server: OpenRuntimeStateStoreWith requires a non-nil Database")
	}
	s := &RuntimeStateStore{db: db.Conn(), dbMeta: db}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Schema is dialect-portable — TEXT for the value and the timestamp,
// matching the convention used by every other store in the runtime
// (see permission/store.go for the rationale).
var runtimeStateMigrations = []string{
	`
CREATE TABLE IF NOT EXISTS runtime_state (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
`,
}

func (s *RuntimeStateStore) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS runtime_state_schema_migrations (
            version    INTEGER PRIMARY KEY,
            applied_at TEXT NOT NULL
        )`); err != nil {
		return fmt.Errorf("runtime_state: creating schema_migrations: %w", err)
	}
	var current int
	row := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM runtime_state_schema_migrations`)
	if err := row.Scan(&current); err != nil {
		return fmt.Errorf("runtime_state: reading migration version: %w", err)
	}
	for i, sqlText := range runtimeStateMigrations {
		version := i + 1
		if version <= current {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("runtime_state: begin migration tx: %w", err)
		}
		if _, err := tx.ExecContext(ctx, sqlText); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("runtime_state: applying migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO runtime_state_schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("runtime_state: recording migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("runtime_state: commit migration %d: %w", version, err)
		}
	}
	return nil
}

// Exists reports whether a row exists for `key`. Used by the gate
// during construction to decide whether the setup token has been
// previously consumed. Errors propagate — silent false would let
// the gate re-open on a transient DB blip.
func (s *RuntimeStateStore) Exists(ctx context.Context, key string) (bool, error) {
	var v sql.NullString
	row := s.db.QueryRowContext(ctx, `SELECT value FROM runtime_state WHERE key = ?`, key)
	if err := row.Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("runtime_state: reading %q: %w", key, err)
	}
	return true, nil
}

// Set writes (or updates) a row with the supplied value + a current
// updated_at. Idempotent; the gate calls this exactly once per
// consumption via CAS upstream.
//
// Uses driver-portable upsert SQL: INSERT … ON CONFLICT (key) DO
// UPDATE — supported by both SQLite (3.24+, which we require) and
// Postgres.
func (s *RuntimeStateStore) Set(ctx context.Context, key, value string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO runtime_state (key, value, updated_at)
        VALUES (?, ?, ?)
        ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
    `, key, value, now)
	if err != nil {
		return fmt.Errorf("runtime_state: writing %q: %w", key, err)
	}
	return nil
}

// Delete drops a row. Used by `loamss setup-token reset` (when it
// lands) to re-open the gate without touching the filesystem.
func (s *RuntimeStateStore) Delete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM runtime_state WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("runtime_state: deleting %q: %w", key, err)
	}
	return nil
}

// Canonical key names for the keys this package writes. Centralizing
// them here keeps grep cheap and helps prevent typos at call sites.
const (
	StateKeySetupTokenConsumed = "setup_token_consumed"
)
