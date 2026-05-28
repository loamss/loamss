package database

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenSQLite_CreatesParentDirsAndPingsCleanly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "subdir", "runtime.db")

	db, err := OpenSQLite(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if db.Driver() != DriverSQLite {
		t.Errorf("Driver() = %q, want %q", db.Driver(), DriverSQLite)
	}
	if !strings.HasSuffix(db.DSN(), "runtime.db") {
		t.Errorf("DSN() = %q, expected absolute path ending in runtime.db", db.DSN())
	}
	if db.DB == nil {
		t.Fatal("DB field is nil")
	}

	// A trivial query should work — parent dirs got created, pragmas applied.
	var n int
	if err := db.DB.QueryRow("SELECT 1").Scan(&n); err != nil {
		t.Fatalf("SELECT 1: %v", err)
	}
	if n != 1 {
		t.Errorf("SELECT 1 returned %d", n)
	}
}

func TestOpenSQLite_AppliesWALAndForeignKeys(t *testing.T) {
	db, err := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var mode string
	if err := db.DB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	var fk int
	if err := db.DB.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestOpenSQLite_RejectsEmptyPath(t *testing.T) {
	_, err := OpenSQLite(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
}

func TestOpenPostgres_ReportsUnimplemented(t *testing.T) {
	_, err := OpenPostgres(context.Background(), "postgres://localhost/x")
	if err == nil {
		t.Fatal("expected error from unimplemented Postgres driver")
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("error %q should mention 'not implemented'", err.Error())
	}
}

func TestOpen_DispatchesByDriver(t *testing.T) {
	// SQLite path — works.
	dbSqlite, err := Open(context.Background(), Config{
		Driver: DriverSQLite,
		DSN:    filepath.Join(t.TempDir(), "via-open.db"),
	})
	if err != nil {
		t.Fatalf("Open(sqlite): %v", err)
	}
	t.Cleanup(func() { _ = dbSqlite.Close() })
	if dbSqlite.Driver() != DriverSQLite {
		t.Errorf("dispatched driver = %q, want %q", dbSqlite.Driver(), DriverSQLite)
	}

	// Postgres path — fails with the unimplemented sentinel.
	_, err = Open(context.Background(), Config{Driver: DriverPostgres, DSN: "x"})
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("Open(postgres) error = %v, want 'not implemented'", err)
	}

	// Unknown driver.
	_, err = Open(context.Background(), Config{Driver: Driver("mongo"), DSN: "x"})
	if err == nil || !strings.Contains(err.Error(), "unknown driver") {
		t.Errorf("Open(mongo) error = %v, want 'unknown driver'", err)
	}
}

func TestDatabase_CloseIsIdempotent(t *testing.T) {
	db, err := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "close-twice.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	// Second Close should not panic. Underlying *sql.DB returns an
	// error on close-after-close; the Database wrapper passes it
	// through, which is fine for this contract.
	_ = db.Close()

	// Close on a zero-value Database is safe (the wrapper guards nil).
	var zero *Database
	if err := zero.Close(); err != nil {
		t.Errorf("nil Database Close: %v", err)
	}
}
