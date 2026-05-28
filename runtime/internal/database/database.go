// Package database is the abstraction over the *sql.DB used by the
// runtime's subsystem stores (permission, source, capsule, memory
// layer, oauth client store).
//
// Two pieces:
//
//   - Database — owns the underlying *sql.DB and remembers which
//     driver opened it (sqlite or postgres). Lifecycle handle:
//     callers obtain one via Open / OpenSQLite / OpenPostgres and
//     pass it to subsystem stores; the caller owns Close.
//
//   - DB / Tx — thin wrappers over *sql.DB and *sql.Tx that auto-
//     rewrite `?` parameter placeholders to Postgres `$N` style at
//     query time when the driver is Postgres. Subsystem code calls
//     ExecContext / QueryContext / QueryRowContext / BeginTx
//     exactly as it would on *sql.DB; the wrapper handles the
//     dialect difference invisibly.
//
// The wrapper approach was chosen so that subsystem stores can keep
// their existing query strings (all written with `?` because SQLite
// was the original target) without per-call-site editing — only the
// type of the store's `db` field changes, from `*sql.DB` to
// `*database.DB`. Migration SQL is plain DDL with no placeholders,
// so it goes through the wrapper unchanged.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the pgx Postgres driver under name "pgx"
	_ "modernc.org/sqlite"             // registers the sqlite driver under name "sqlite"
)

// Driver identifies which backing database a Database was opened
// against. Subsystem migrations branch on this when their SQL
// differs across drivers, and the DB wrapper uses it to decide
// whether to rebind placeholders.
type Driver string

const (
	// DriverSQLite is the laptop / local-install default.
	DriverSQLite Driver = "sqlite"
	// DriverPostgres is the cloud / multi-host default.
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

	// MaxOpenConns caps the connection pool. Zero means use the
	// driver default (unlimited for SQLite; 25 for Postgres in this
	// package). Only used by Postgres.
	MaxOpenConns int

	// MaxIdleConns caps the idle pool. Zero means use the driver
	// default. Only used by Postgres.
	MaxIdleConns int
}

// Database is the lifecycle handle that owns an underlying *sql.DB
// + a driver discriminator + a DSN for diagnostics.
//
// Subsystem stores receive the wrapped DB via Database.Conn() and
// use that for queries. The Database itself is rarely touched after
// Open — its job is to be Closed.
type Database struct {
	conn   *DB
	driver Driver
	dsn    string
}

// Driver reports which backend this Database was opened against.
func (d *Database) Driver() Driver { return d.driver }

// DSN returns the data source string this Database was opened with.
// Used by tests, the doctor command, and store diagnostics; not used
// in query paths.
func (d *Database) DSN() string { return d.dsn }

// Conn returns the wrapped connection subsystem stores use for
// queries. Same handle every call; do not Close from a store.
func (d *Database) Conn() *DB { return d.conn }

// Close releases the underlying *sql.DB. Callers that obtained the
// Database via Open / OpenSQLite / OpenPostgres should call this in
// their cleanup path; subsystem stores that received a *Database
// from elsewhere should NOT close it.
func (d *Database) Close() error {
	if d == nil || d.conn == nil || d.conn.raw == nil {
		return nil
	}
	return d.conn.raw.Close()
}

// Open dispatches to the right driver-specific opener based on
// cfg.Driver. Returns an error if the driver is unknown.
func Open(ctx context.Context, cfg Config) (*Database, error) {
	switch cfg.Driver {
	case DriverSQLite:
		return OpenSQLite(ctx, cfg.DSN)
	case DriverPostgres:
		return openPostgresWith(ctx, cfg)
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
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("database: opening sqlite %q: %w", abs, err)
	}
	if err := raw.PingContext(ctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("database: pinging sqlite %q: %w", abs, err)
	}
	return &Database{
		conn:   &DB{raw: raw, driver: DriverSQLite},
		driver: DriverSQLite,
		dsn:    abs,
	}, nil
}

// OpenPostgres opens a Postgres database at the given libpq URL.
//
//	postgres://user:pass@host:5432/dbname?sslmode=require
//
// Uses the jackc/pgx/v5 driver in database/sql-compatibility mode
// (registered as "pgx" via the stdlib import). Connection pool
// defaults: MaxOpenConns=25, MaxIdleConns=5. Override via Open's
// Config.
func OpenPostgres(ctx context.Context, dsn string) (*Database, error) {
	return openPostgresWith(ctx, Config{Driver: DriverPostgres, DSN: dsn})
}

func openPostgresWith(ctx context.Context, cfg Config) (*Database, error) {
	if cfg.DSN == "" {
		return nil, errors.New("database: OpenPostgres requires a non-empty DSN")
	}
	raw, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("database: opening postgres: %w", err)
	}
	maxOpen := cfg.MaxOpenConns
	if maxOpen == 0 {
		maxOpen = 25
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle == 0 {
		maxIdle = 5
	}
	raw.SetMaxOpenConns(maxOpen)
	raw.SetMaxIdleConns(maxIdle)
	if err := raw.PingContext(ctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("database: pinging postgres: %w", err)
	}
	return &Database{
		conn:   &DB{raw: raw, driver: DriverPostgres},
		driver: DriverPostgres,
		dsn:    cfg.DSN,
	}, nil
}

// --- DB / Tx wrappers --------------------------------------------------

// DB wraps *sql.DB and auto-rewrites `?` parameter placeholders to
// Postgres `$N` style when the underlying driver is Postgres. The
// method surface mirrors *sql.DB exactly enough for subsystem
// stores' usage; if a store needs an unwrapped *sql.DB (e.g. for
// passing to a library), Raw() returns it.
type DB struct {
	raw    *sql.DB
	driver Driver
}

// Driver reports the driver this wrapper sits on top of.
func (db *DB) Driver() Driver { return db.driver }

// Raw returns the unwrapped *sql.DB for callers that need the
// underlying handle (rare — most should use the wrapped methods so
// rebinding stays automatic).
func (db *DB) Raw() *sql.DB { return db.raw }

// ExecContext executes a write query. Placeholders are auto-rebound.
func (db *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.raw.ExecContext(ctx, db.rebind(query), args...)
}

// QueryContext runs a read query returning rows. Placeholders are
// auto-rebound.
func (db *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return db.raw.QueryContext(ctx, db.rebind(query), args...)
}

// QueryRowContext runs a read query returning a single row.
// Placeholders are auto-rebound.
func (db *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return db.raw.QueryRowContext(ctx, db.rebind(query), args...)
}

// BeginTx starts a transaction. The returned *Tx wraps *sql.Tx with
// the same rebind behavior.
func (db *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := db.raw.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{raw: tx, driver: db.driver}, nil
}

// PingContext checks the connection.
func (db *DB) PingContext(ctx context.Context) error {
	return db.raw.PingContext(ctx)
}

func (db *DB) rebind(query string) string {
	if db.driver == DriverPostgres {
		return Rebind(query)
	}
	return query
}

// Tx wraps *sql.Tx and rebinds placeholders for Postgres in the
// same way DB does.
type Tx struct {
	raw    *sql.Tx
	driver Driver
}

// Raw returns the unwrapped *sql.Tx.
func (tx *Tx) Raw() *sql.Tx { return tx.raw }

// ExecContext executes a write query inside the transaction.
func (tx *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.raw.ExecContext(ctx, tx.rebind(query), args...)
}

// QueryContext runs a read query returning rows.
func (tx *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.raw.QueryContext(ctx, tx.rebind(query), args...)
}

// QueryRowContext runs a read query returning a single row.
func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.raw.QueryRowContext(ctx, tx.rebind(query), args...)
}

// Commit finalizes the transaction.
func (tx *Tx) Commit() error { return tx.raw.Commit() }

// Rollback aborts the transaction.
func (tx *Tx) Rollback() error { return tx.raw.Rollback() }

func (tx *Tx) rebind(query string) string {
	if tx.driver == DriverPostgres {
		return Rebind(query)
	}
	return query
}

// --- placeholder rebind ------------------------------------------------

// Rebind rewrites `?` placeholders to `$1`, `$2`, ... in order.
// Question marks inside single-quoted string literals are left alone.
// Single-line `--` and block `/* */` comments are skipped so a `?`
// inside a comment doesn't consume a parameter slot.
//
// Backslash escaping of single quotes is honored (`'it\'s'` stays
// one string). Doubled single quotes (`'it”s'`) — the SQL standard
// quoting form — are also handled correctly.
//
// This is enough for the runtime's stores; it's not a general SQL
// parser. If we ever need more, switch to a real parser at that
// point.
func Rebind(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	inStr := false
	n := 1
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case inStr:
			if c == '\'' {
				// Standard SQL: '' is an escaped quote inside the string.
				if i+1 < len(query) && query[i+1] == '\'' {
					b.WriteByte(c)
					b.WriteByte(query[i+1])
					i++
					continue
				}
				// Backslash escaping: \' is also a single-quote
				// inside the string (common in SQLite + Postgres
				// when standard_conforming_strings is off).
				if i > 0 && query[i-1] == '\\' {
					b.WriteByte(c)
					continue
				}
				inStr = false
			}
			b.WriteByte(c)
		case c == '\'':
			inStr = true
			b.WriteByte(c)
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			// Line comment. Copy through to end-of-line or end-of-string.
			for i < len(query) && query[i] != '\n' {
				b.WriteByte(query[i])
				i++
			}
			if i < len(query) {
				b.WriteByte(query[i])
			}
		case c == '/' && i+1 < len(query) && query[i+1] == '*':
			// Block comment. Copy through to '*/' or end-of-string.
			b.WriteByte(c)
			i++
			for i < len(query) {
				b.WriteByte(query[i])
				if query[i] == '*' && i+1 < len(query) && query[i+1] == '/' {
					b.WriteByte(query[i+1])
					i++
					break
				}
				i++
			}
		case c == '?':
			fmt.Fprintf(&b, "$%d", n)
			n++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
