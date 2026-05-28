package capsule

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/loamss/loamss/runtime/internal/database"
)

// Installed is the persisted record for a capsule that has been
// installed on this runtime. The Manifest is round-tripped through
// JSON in storage; everything the runtime needs to schedule the
// capsule lives on this struct.
type Installed struct {
	// ID is the canonical capsule identifier: "<name>@<version>".
	// Stable across renames; survives version upgrades by being
	// rewritten at install time.
	ID string `json:"id"`

	// Name is the capsule's manifest name. Used as the Principal ID
	// when issuing grants — one capsule has at most one set of
	// active grants regardless of version.
	Name string `json:"name"`

	// Version is the currently-installed semver. Only one version
	// per name is tracked at a time; upgrades replace the record.
	Version string `json:"version"`

	// SpecVersion is the capsule-spec.md version the manifest
	// conforms to. Stored so a future runtime can fast-fail older
	// formats rather than try to interpret them.
	SpecVersion string `json:"spec_version"`

	// AuthorName / AuthorURL are the publisher identity surfaced in
	// `capsule list` / `capsule show`.
	AuthorName string `json:"author_name"`
	AuthorURL  string `json:"author_url,omitempty"`

	// Manifest is the full parsed manifest, round-tripped through
	// JSON for storage. Callers needing typed access (Permissions,
	// Tools, ...) read these fields directly rather than re-parsing.
	Manifest *Manifest `json:"manifest"`

	// InstallPath is the on-disk directory where the capsule's code
	// lives. Subprocess hosting (later commit) execs from here.
	InstallPath string `json:"install_path"`

	// InstalledAt is when the capsule was installed.
	InstalledAt time.Time `json:"installed_at"`
}

// Store is the SQLite-backed persistence for installed capsules.
// Shares runtime.db with the permission store via SQLite WAL —
// concurrent writes from the two packages serialize at the SQLite
// level. capsule_schema_migrations tracks this store's own
// migration version independently from permission's.
type Store struct {
	db     *sql.DB
	dbMeta *database.Database
	ownsDB bool
	path   string

	// mu serializes writes within this process. Cross-process
	// writes serialize via SQLite's write lock (capsules table
	// writes use BEGIN IMMEDIATE).
	mu sync.Mutex
}

// OpenStore opens the capsule store at a filesystem path. Convenience
// wrapper around OpenStoreWith for the single-subsystem case. Callers
// sharing one runtime.db across multiple subsystems (start.go pattern)
// should use OpenStoreWith.
func OpenStore(ctx context.Context, path string) (*Store, error) {
	db, err := database.OpenSQLite(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("capsule: %w", err)
	}
	s, err := OpenStoreWith(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	s.ownsDB = true
	return s, nil
}

// OpenStoreWith creates a capsule Store on top of an already-open
// Database. Caller retains ownership of the Database; Close on the
// returned Store will not close the database.
func OpenStoreWith(ctx context.Context, db *database.Database) (*Store, error) {
	if db == nil || db.DB == nil {
		return nil, errors.New("capsule: OpenStoreWith requires a non-nil Database")
	}
	s := &Store{db: db.DB, dbMeta: db, path: db.DSN()}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Close releases the database handle if the Store opened it.
// Stores constructed via OpenStoreWith never own the database;
// Close is a no-op for the connection.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	if s.ownsDB && s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Path returns the on-disk database path.
func (s *Store) Path() string { return s.path }

// --- migrations --------------------------------------------------------

var capsuleMigrations = []string{
	// 1: initial capsules table.
	`
CREATE TABLE IF NOT EXISTS capsules (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    version         TEXT NOT NULL,
    spec_version    TEXT NOT NULL,
    author_name     TEXT NOT NULL,
    author_url      TEXT,
    manifest_json   TEXT NOT NULL,
    install_path    TEXT NOT NULL,
    installed_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_capsules_name ON capsules(name);
`,
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS capsule_schema_migrations (
            version    INTEGER PRIMARY KEY,
            applied_at TEXT NOT NULL
        )`); err != nil {
		return fmt.Errorf("capsule: creating schema_migrations: %w", err)
	}
	var current int
	row := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM capsule_schema_migrations`)
	if err := row.Scan(&current); err != nil {
		return fmt.Errorf("capsule: reading migration version: %w", err)
	}
	for i, sqlText := range capsuleMigrations {
		version := i + 1
		if version <= current {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("capsule: begin migration tx: %w", err)
		}
		if _, err := tx.ExecContext(ctx, sqlText); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("capsule: applying migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO capsule_schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("capsule: recording migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("capsule: commit migration %d: %w", version, err)
		}
	}
	return nil
}

// --- sentinel errors ---------------------------------------------------

var (
	// ErrCapsuleNotFound is returned by Get / Uninstall when the
	// requested capsule isn't installed.
	ErrCapsuleNotFound = errors.New("capsule: not installed")

	// ErrCapsuleAlreadyInstalled is returned by Insert when a
	// capsule with the same name (any version) is already in the
	// store. Upgrade path (replace existing) is a future commit.
	ErrCapsuleAlreadyInstalled = errors.New("capsule: already installed (uninstall first to replace)")
)

// --- CRUD --------------------------------------------------------------

// Insert persists a capsule record. Fails with
// ErrCapsuleAlreadyInstalled if a capsule with the same name is
// already present (regardless of version). Caller is responsible for
// having validated the manifest and copied the code/ tree to
// InstallPath before calling.
func (s *Store) Insert(ctx context.Context, c Installed) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if c.InstalledAt.IsZero() {
		c.InstalledAt = time.Now().UTC()
	}
	manifestJSON, err := json.Marshal(c.Manifest)
	if err != nil {
		return fmt.Errorf("capsule: encoding manifest: %w", err)
	}
	if c.ID == "" {
		c.ID = c.Name + "@" + c.Version
	}

	_, err = s.db.ExecContext(ctx, `
        INSERT INTO capsules (
            id, name, version, spec_version, author_name, author_url,
            manifest_json, install_path, installed_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Version, c.SpecVersion, c.AuthorName, nullableString(c.AuthorURL),
		string(manifestJSON), c.InstallPath, c.InstalledAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		// SQLite UNIQUE constraint failures look like
		// "UNIQUE constraint failed: capsules.name". Map to the
		// typed sentinel so callers can branch cleanly.
		if isUniqueConstraint(err) {
			return fmt.Errorf("%w: %s", ErrCapsuleAlreadyInstalled, c.Name)
		}
		return fmt.Errorf("capsule: inserting %s: %w", c.Name, err)
	}
	return nil
}

// Get returns the installed capsule with the given name. Returns
// ErrCapsuleNotFound when no record exists.
func (s *Store) Get(ctx context.Context, name string) (*Installed, error) {
	row := s.db.QueryRowContext(ctx, capsuleSelectColumns+` WHERE name = ?`, name)
	c, err := scanCapsule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrCapsuleNotFound, name)
	}
	return c, err
}

// List returns all installed capsules, newest-first.
func (s *Store) List(ctx context.Context) ([]Installed, error) {
	rows, err := s.db.QueryContext(ctx,
		capsuleSelectColumns+` ORDER BY installed_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("capsule: listing: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Installed
	for rows.Next() {
		c, err := scanCapsule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// Delete removes the capsule record. Returns ErrCapsuleNotFound when
// no record exists. Caller is responsible for cascade-revoking the
// capsule's grants (via permission.Engine) and for deleting the
// on-disk InstallPath before calling — this method only touches the
// table row.
func (s *Store) Delete(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM capsules WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("capsule: deleting %s: %w", name, err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("%w: %s", ErrCapsuleNotFound, name)
	}
	return nil
}

const capsuleSelectColumns = `SELECT id, name, version, spec_version, author_name, author_url,
       manifest_json, install_path, installed_at
       FROM capsules`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCapsule(r rowScanner) (*Installed, error) {
	var (
		c            Installed
		authorURL    sql.NullString
		manifestJSON string
		installedStr string
	)
	if err := r.Scan(&c.ID, &c.Name, &c.Version, &c.SpecVersion, &c.AuthorName,
		&authorURL, &manifestJSON, &c.InstallPath, &installedStr); err != nil {
		return nil, err
	}
	if authorURL.Valid {
		c.AuthorURL = authorURL.String
	}
	var m Manifest
	if err := json.Unmarshal([]byte(manifestJSON), &m); err != nil {
		return nil, fmt.Errorf("capsule: decoding manifest for %s: %w", c.Name, err)
	}
	c.Manifest = &m
	if t, err := time.Parse(time.RFC3339Nano, installedStr); err == nil {
		c.InstalledAt = t
	}
	return &c, nil
}

func nullableString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

// isUniqueConstraint inspects a driver error for SQLite's UNIQUE
// constraint failure pattern. Modernc.org/sqlite reports these as
// generic errors with a recognizable substring; we string-match
// rather than depend on the driver's internal error types.
func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
