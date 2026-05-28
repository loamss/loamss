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
	if db.Conn() == nil {
		t.Fatal("Conn() returned nil")
	}

	// A trivial query should work — parent dirs got created, pragmas applied.
	var n int
	if err := db.Conn().QueryRowContext(context.Background(), "SELECT 1").Scan(&n); err != nil {
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
	if err := db.Conn().QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	var fk int
	if err := db.Conn().QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&fk); err != nil {
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

func TestOpenPostgres_RejectsEmptyDSN(t *testing.T) {
	_, err := OpenPostgres(context.Background(), "")
	if err == nil {
		t.Fatal("expected error from empty Postgres DSN")
	}
	if !strings.Contains(err.Error(), "non-empty DSN") {
		t.Errorf("error %q should mention DSN requirement", err.Error())
	}
}

func TestOpen_DispatchesByDriver(t *testing.T) {
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
	_ = db.Close()

	var zero *Database
	if err := zero.Close(); err != nil {
		t.Errorf("nil Database Close: %v", err)
	}
}

// --- Rebind tests ------------------------------------------------------

func TestRebind(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no placeholders",
			in:   "SELECT 1",
			want: "SELECT 1",
		},
		{
			name: "single placeholder",
			in:   "SELECT * FROM t WHERE id = ?",
			want: "SELECT * FROM t WHERE id = $1",
		},
		{
			name: "multiple placeholders",
			in:   "INSERT INTO t (a, b, c) VALUES (?, ?, ?)",
			want: "INSERT INTO t (a, b, c) VALUES ($1, $2, $3)",
		},
		{
			name: "question mark inside string literal is preserved",
			in:   "SELECT 'is it?' FROM t WHERE x = ?",
			want: "SELECT 'is it?' FROM t WHERE x = $1",
		},
		{
			name: "doubled quotes inside string are escaped quotes, not string end",
			in:   "SELECT 'it''s a ?' FROM t WHERE x = ?",
			want: "SELECT 'it''s a ?' FROM t WHERE x = $1",
		},
		{
			name: "line comment with ? is skipped",
			in:   "SELECT 1 -- what?\nFROM t WHERE x = ?",
			want: "SELECT 1 -- what?\nFROM t WHERE x = $1",
		},
		{
			name: "block comment with ? is skipped",
			in:   "SELECT /* ? not a param */ ? FROM t",
			want: "SELECT /* ? not a param */ $1 FROM t",
		},
		{
			name: "many placeholders enumerate correctly",
			in:   "SELECT ? + ? + ? + ? + ?",
			want: "SELECT $1 + $2 + $3 + $4 + $5",
		},
		{
			name: "backslash-escaped quote inside string",
			in:   `SELECT 'it\'s ?' FROM t WHERE x = ?`,
			want: `SELECT 'it\'s ?' FROM t WHERE x = $1`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Rebind(tc.in)
			if got != tc.want {
				t.Errorf("Rebind(%q) =\n  got:  %q\n  want: %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDBWrapper_RebindsForPostgresButNotSQLite(t *testing.T) {
	// The whole point of the wrapper: SQLite queries pass through,
	// Postgres queries get rebound. We can verify this without a
	// Postgres connection by constructing a DB with the right
	// driver tag and calling rebind directly.
	sqlite := &DB{driver: DriverSQLite}
	if got := sqlite.rebind("SELECT ? FROM t"); got != "SELECT ? FROM t" {
		t.Errorf("SQLite driver should not rebind, got %q", got)
	}
	pg := &DB{driver: DriverPostgres}
	if got := pg.rebind("SELECT ? FROM t"); got != "SELECT $1 FROM t" {
		t.Errorf("Postgres driver should rebind, got %q", got)
	}
}
