// Package database is a thin abstraction over the *sql.DB used by
// the runtime's subsystem stores (permission, source, capsule,
// memory layer, oauth client store).
//
// Each subsystem owns its own schema and its own migration list,
// but all of them share the same connection-opening logic and
// driver-specific tuning (SQLite WAL pragmas, busy timeout, foreign
// keys; Postgres connection-pool sizing). This package is where
// that shared logic lives.
//
// A Database wraps a *sql.DB and remembers which driver opened it
// so that subsystem migrations can branch on driver type when the
// SQL differs (e.g. autoincrement, JSON column types). The wrapped
// *sql.DB is the field DB and is used by callers exactly as they
// used to use sql.Open's return value.
//
// Two ways to obtain a Database:
//
//   - Open(ctx, Config) — the general form. Picks a driver from
//     the config and returns a fresh Database. Caller owns Close.
//   - OpenSQLite / OpenPostgres — convenience wrappers around Open
//     for the common single-driver case.
//
// For now only SQLite is implemented; OpenPostgres returns a "not
// implemented" error. The Postgres implementation lands in W2.2;
// every subsystem store can already accept a *Database opened by
// either, which is the point of this refactor.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the sqlite driver
)

// Driver identifies which backing database a Database was opened
// against. Subsystem migrations branch on this when their SQL
// differs across drivers.
type Driver string

const (
	// DriverSQLite is the laptop / local-install default.
	DriverSQLite Driver = "sqlite"
	// DriverPostgres is the cloud / multi-host default.
	// Implementation lands in W2.2; OpenPostgres currently returns
	// an unimplemented error.
	DriverPostgres Driver = "postgres"
)

// Config describes how to open a Database.
type Config struct {
	// Driver picks the backing database. Required.
	Driver Driver

	// DSN is the driver-specific data source string.
	//   - For SQLite: a filesystem path (absolute or relative;
	//     OpenSQLite resolves to absolute and creates parent dirs).
	//   - For Postgres: a standard libpq connection URL,
	//     e.g. "postgres://user:pass@host:5432/dbname?sslmode=require".
	DSN string
}

// Database is a thin wrapper around *sql.DB plus a driver
// discriminator. Subsystem stores use the embedded *sql.DB exactly
// as they used to; the driver field is for migration dispatch.
type Database struct {
	// DB is the underlying sql.DB. Stores use this for queries.
	DB *sql.DB

	driver Driver
	dsn    string
}

// Driver reports which backend this Database was opened against.
func (d *Database) Driver() Driver { return d.driver }

// DSN returns the data source string this Database was opened with.
// Used by tests and the doctor command; not used in query paths.
func (d *Database) DSN() string { return d.dsn }

// Close releases the underlying *sql.DB. Callers that obtained the
// Database via Open / OpenSQLite / OpenPostgres should call this in
// their cleanup path; callers that received a *Database from
// elsewhere (a shared handle passed in) should NOT close it.
func (d *Database) Close() error {
	if d == nil || d.DB == nil {
		return nil
	}
	return d.DB.Close()
}

// Open dispatches to the right driver-specific opener based on
// cfg.Driver. Returns an error if the driver is unknown or not yet
// implemented.
func Open(ctx context.Context, cfg Config) (*Database, error) {
	switch cfg.Driver {
	case DriverSQLite:
		return OpenSQLite(ctx, cfg.DSN)
	case DriverPostgres:
		return OpenPostgres(ctx, cfg.DSN)
	default:
		return nil, fmt.Errorf("database: unknown driver %q", cfg.Driver)
	}
}

// OpenSQLite opens a SQLite database at the given filesystem path.
//
// The path is resolved to an absolute path and its parent directory
// is created with mode 0700 if it doesn't exist (matching the
// pre-refactor behavior of each subsystem's Open function).
//
// SQLite pragmas applied at open time:
//   - journal_mode = WAL   (concurrent readers don't block writers)
//   - synchronous = NORMAL (durability good enough for our use, faster than FULL)
//   - busy_timeout = 5000  (5s wait on lock contention before failing)
//   - foreign_keys = 1     (default off in SQLite; we want enforcement)
//
// These pragmas are what every subsystem store applied to its own
// connection before the refactor; centralizing them here makes
// future tuning a one-place change.
func OpenSQLite(ctx context.Context, path string) (*Database, error) {
	if path == "" {
		return nil, errors.New("database: OpenSQLite requires a non-empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("database: resolving sqlite path %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("database: creating sqlite parent dir: %w", err)
	}
	dsn := "file:" + abs + "?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("database: opening sqlite %q: %w", abs, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("database: pinging sqlite %q: %w", abs, err)
	}
	return &Database{DB: db, driver: DriverSQLite, dsn: abs}, nil
}

// OpenPostgres opens a Postgres database at the given libpq URL.
//
// Lands in W2.2 alongside the per-subsystem Postgres migration sets.
// Returning an explicit unimplemented error now so the SPI shape is
// the only thing changing in W2.1.
func OpenPostgres(_ context.Context, _ string) (*Database, error) {
	return nil, errors.New("database: Postgres driver not implemented yet (lands in W2.2)")
}
