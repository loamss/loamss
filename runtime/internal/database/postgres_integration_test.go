package database_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/database"
	"github.com/loamss/loamss/runtime/internal/memory"
	"github.com/loamss/loamss/runtime/internal/oauth"
	"github.com/loamss/loamss/runtime/internal/permission"
	"github.com/loamss/loamss/runtime/internal/source"
)

// Integration tests for the Postgres backend. Lives in its own _test
// package so it can import the subsystem stores without creating
// import cycles. Gated by LOAMSS_RUNTIME_PG_TEST_DSN to keep CI
// deterministic — same pattern as memory:pgvector and source:gmail.
//
// Spin up Postgres locally with:
//
//	docker run --rm -d -p 5434:5432 \
//	  -e POSTGRES_PASSWORD=loamss -e POSTGRES_DB=loamsstest \
//	  postgres:17-alpine
//
// Then:
//
//	LOAMSS_RUNTIME_PG_TEST_DSN=\
//	  "postgres://postgres:loamss@127.0.0.1:5434/loamsstest?sslmode=disable" \
//	  go test ./internal/database/... -v -run Integration

func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("LOAMSS_RUNTIME_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set LOAMSS_RUNTIME_PG_TEST_DSN to a live Postgres to run integration tests")
	}
	return dsn
}

// freshSchema drops + recreates a per-test schema so the migrations
// always run against a clean slate. The schema name is derived from
// the test name to keep parallel runs independent.
func freshSchema(t *testing.T, db *database.Database) string {
	t.Helper()
	schema := "loamss_test_" + strings.NewReplacer("/", "_", " ", "_", "-", "_").Replace(t.Name())
	ctx := context.Background()
	if _, err := db.Conn().ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := db.Conn().ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Conn().ExecContext(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Conn().ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	})
	return schema
}

// openPg opens the test Postgres + isolates the test in a fresh
// schema. Returns the Database; caller is responsible for Close
// (handled by t.Cleanup).
func openPg(t *testing.T) *database.Database {
	t.Helper()
	dsn := integrationDSN(t)
	db, err := database.OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	freshSchema(t, db)
	return db
}

// --- migration smoke tests --------------------------------------------

func TestIntegration_Permission_MigratesAndQueriesCleanly(t *testing.T) {
	db := openPg(t)
	store, err := permission.OpenWith(context.Background(), db)
	if err != nil {
		t.Fatalf("permission.OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// The migrate function seeds canonical capabilities on first
	// run; confirm the table has rows.
	caps, err := store.ListCapabilities(context.Background())
	if err != nil {
		t.Fatalf("ListCapabilities: %v", err)
	}
	if len(caps) == 0 {
		t.Error("expected canonical capabilities to be seeded")
	}
	t.Logf("seeded %d canonical capabilities", len(caps))
}

func TestIntegration_Source_MigratesAndQueriesCleanly(t *testing.T) {
	db := openPg(t)
	store, err := source.OpenStoreWith(context.Background(), db)
	if err != nil {
		t.Fatalf("source.OpenStoreWith: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Empty store → List returns no rows, no error.
	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 sources, got %d", len(list))
	}
}

func TestIntegration_Capsule_MigratesAndQueriesCleanly(t *testing.T) {
	db := openPg(t)
	store, err := capsule.OpenStoreWith(context.Background(), db)
	if err != nil {
		t.Fatalf("capsule.OpenStoreWith: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 capsules, got %d", len(list))
	}
}

func TestIntegration_Memory_MigratesAndQueriesCleanly(t *testing.T) {
	db := openPg(t)
	store, err := memory.OpenStoreWith(context.Background(), db)
	if err != nil {
		t.Fatalf("memory.OpenStoreWith: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Empty store, no entities for any namespace.
	ents, err := store.ListEntities(context.Background(), memory.EntityFilter{Namespace: "nonexistent"})
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(ents) != 0 {
		t.Errorf("expected 0 entities, got %d", len(ents))
	}
}

func TestIntegration_OAuth_MigratesAndQueriesCleanly(t *testing.T) {
	db := openPg(t)
	store, err := oauth.OpenClientStoreWith(context.Background(), db)
	if err != nil {
		t.Fatalf("oauth.OpenClientStoreWith: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 OAuth clients, got %d", len(list))
	}
}

// --- end-to-end roundtrip ---------------------------------------------

// TestIntegration_OAuth_ClientRoundtrip is a small but real query
// exercise — write a row, read it back, verify the rebound
// placeholders worked.
func TestIntegration_OAuth_ClientRoundtrip(t *testing.T) {
	db := openPg(t)
	store, err := oauth.OpenClientStoreWith(context.Background(), db)
	if err != nil {
		t.Fatalf("oauth.OpenClientStoreWith: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.Set(ctx, oauth.ClientCredential{
		Provider:     "google",
		ClientID:     "abc.apps.googleusercontent.com",
		ClientSecret: "secret",
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := store.Get(ctx, "google")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ClientID != "abc.apps.googleusercontent.com" {
		t.Errorf("ClientID round-trip: got %q", got.ClientID)
	}
	if got.ClientSecret != "secret" {
		t.Errorf("ClientSecret round-trip: got %q", got.ClientSecret)
	}
}
