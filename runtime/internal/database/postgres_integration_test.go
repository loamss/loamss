package database_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/database"
	"github.com/loamss/loamss/runtime/internal/memory"
	"github.com/loamss/loamss/runtime/internal/oauth"
	"github.com/loamss/loamss/runtime/internal/permission"
	"github.com/loamss/loamss/runtime/internal/source"
)

// --- multi-process worker reentry ------------------------------------------
//
// TestMain doubles as the worker entrypoint when the multi-process test
// invokes the test binary again with childMarkerEnv set. Keeps everything
// in one file — no separate worker binary to build or maintain.

const (
	childMarkerEnv = "LOAMSS_AUDIT_MP_DSN"
	childCountEnv  = "LOAMSS_AUDIT_MP_COUNT"
	childIDEnv     = "LOAMSS_AUDIT_MP_WORKER_ID"
)

func TestMain(m *testing.M) {
	if dsn := os.Getenv(childMarkerEnv); dsn != "" {
		runAuditWorker(dsn)
		return
	}
	os.Exit(m.Run())
}

// runAuditWorker is the child-process body for the multi-process
// concurrency test. Opens an audit.Store against the supplied DSN and
// appends LOAMSS_AUDIT_MP_COUNT entries. Exits 0 on success.
// envIntDefault returns the integer parse of an env var, or the
// default if unset/invalid. Used for stress-tunable knobs.
func envIntDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func runAuditWorker(dsn string) {
	count, err := strconv.Atoi(os.Getenv(childCountEnv))
	if err != nil || count <= 0 {
		fmt.Fprintf(os.Stderr, "worker: invalid %s=%q\n", childCountEnv, os.Getenv(childCountEnv))
		os.Exit(2)
	}
	workerID := os.Getenv(childIDEnv)

	ctx := context.Background()
	store, err := audit.OpenPostgres(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "worker %s: OpenPostgres: %v\n", workerID, err)
		os.Exit(3)
	}
	defer func() { _ = store.Close(ctx) }()

	for i := 0; i < count; i++ {
		_, err := store.Append(ctx, audit.Entry{
			Type:    "test.multiprocess",
			Actor:   audit.Actor{Kind: audit.ActorRuntime, ID: "worker-" + workerID},
			Outcome: audit.OutcomeSuccess,
			Data:    map[string]any{"i": i, "worker": workerID},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "worker %s: append #%d: %v\n", workerID, i, err)
			os.Exit(4)
		}
	}
	os.Exit(0)
}

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

func TestIntegration_Audit_MigratesAndQueriesCleanly(t *testing.T) {
	db := openPg(t)
	store, err := audit.OpenWith(context.Background(), db)
	if err != nil {
		t.Fatalf("audit.OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background()) })

	// Empty store → Latest returns nil, no error.
	latest, err := store.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest != nil {
		t.Errorf("expected nil latest entry on empty store, got %+v", latest)
	}

	// Empty chain → Verify reports valid with 0 entries checked.
	r, err := store.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !r.Valid {
		t.Errorf("expected empty chain to be valid, got %+v", r)
	}
	if r.EntriesChecked != 0 {
		t.Errorf("expected 0 entries checked, got %d", r.EntriesChecked)
	}
}

// TestIntegration_Audit_AppendVerifyRoundtrip exercises the Postgres
// append path: pg_advisory_xact_lock serialization, rebinding of ?
// placeholders, and hash-chain integrity across multiple entries.
func TestIntegration_Audit_AppendVerifyRoundtrip(t *testing.T) {
	db := openPg(t)
	store, err := audit.OpenWith(context.Background(), db)
	if err != nil {
		t.Fatalf("audit.OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background()) })

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		e := audit.Entry{
			Type:    "test.event",
			Actor:   audit.Actor{Kind: audit.ActorRuntime, ID: "test"},
			Outcome: audit.OutcomeSuccess,
			Data:    map[string]any{"i": i},
		}
		got, err := store.Append(ctx, e)
		if err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
		if got.ID == "" {
			t.Errorf("entry #%d: expected non-empty ID", i)
		}
		if got.Hash == "" {
			t.Errorf("entry #%d: expected non-empty Hash", i)
		}
	}

	entries, err := store.Query(ctx, audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("expected 5 entries, got %d", len(entries))
	}

	r, err := store.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !r.Valid {
		t.Errorf("chain broken after Append: %+v", r)
	}
	if r.EntriesChecked != 5 {
		t.Errorf("expected 5 entries checked, got %d", r.EntriesChecked)
	}
}

// TestIntegration_Audit_ConcurrentAppendsChainIntact stresses
// pg_advisory_xact_lock by hammering the Append path from many
// goroutines. The chain must remain consistent: every prev_hash
// references the prior entry, every hash recomputes.
func TestIntegration_Audit_ConcurrentAppendsChainIntact(t *testing.T) {
	db := openPg(t)
	store, err := audit.OpenWith(context.Background(), db)
	if err != nil {
		t.Fatalf("audit.OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background()) })

	ctx := context.Background()
	const N = 30

	errs := make(chan error, N)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := store.Append(ctx, audit.Entry{
				Type:    "test.concurrent",
				Actor:   audit.Actor{Kind: audit.ActorRuntime, ID: "test"},
				Outcome: audit.OutcomeSuccess,
				Data:    map[string]any{"i": i},
			})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Append: %v", err)
		}
	}

	r, err := store.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !r.Valid {
		t.Fatalf("concurrent appends broke the chain: %+v", r)
	}
	if r.EntriesChecked != N {
		t.Errorf("expected %d entries checked, got %d", N, r.EntriesChecked)
	}
}

// TestIntegration_Audit_MultiProcessConcurrentAppends proves that
// pg_advisory_xact_lock serializes Append across separate OS processes
// — the actual claim the Postgres backend makes. The single-process
// goroutine test above only exercises in-process serialization, which
// is also covered by the sync.Mutex; the advisory lock matters when
// the runtime daemon and a CLI invocation (separate binaries) write
// concurrently.
//
// Mechanism: spawns N child processes via os.Executable() with the
// magic env var that triggers TestMain's worker path. Each child
// opens its own audit.Store against a per-test schema (passed via
// the search_path runtime parameter on the DSN) and appends K
// entries. After all children exit, parent verifies the chain.
//
// Schema isolation rationale: search_path encoded in the DSN is the
// only way to propagate freshSchema()'s per-test isolation across
// process boundaries — children can't inherit the parent's connection
// state.
func TestIntegration_Audit_MultiProcessConcurrentAppends(t *testing.T) {
	dsn := integrationDSN(t)
	db := openPg(t)
	schema := strings.NewReplacer("/", "_", " ", "_", "-", "_").Replace(t.Name())
	schema = "loamss_test_" + schema

	// The audit schema needs to exist before children spin up. The
	// parent's OpenWith creates audit_entries inside the per-test
	// schema courtesy of freshSchema's SET search_path.
	parent, err := audit.OpenWith(context.Background(), db)
	if err != nil {
		t.Fatalf("audit.OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = parent.Close(context.Background()) })

	// Build the child DSN with search_path encoded. pgx forwards
	// unknown query params as runtime parameters, so every connection
	// in the child's pool sets search_path on startup. Verified above.
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	childDSN := dsn + sep + "search_path=" + schema

	// Defaults sized for a fast laptop loop. Override via env vars
	// for heavier contention runs (e.g., against Cloud SQL).
	numWorkers := envIntDefault("LOAMSS_AUDIT_MP_NUM_WORKERS", 6)
	entriesPerChild := envIntDefault("LOAMSS_AUDIT_MP_ENTRIES_PER_CHILD", 20)
	totalExpected := numWorkers * entriesPerChild

	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	cmds := make([]*exec.Cmd, numWorkers)
	for i := 0; i < numWorkers; i++ {
		cmd := exec.Command(exePath)
		cmd.Env = append(os.Environ(),
			childMarkerEnv+"="+childDSN,
			childCountEnv+"="+strconv.Itoa(entriesPerChild),
			childIDEnv+"="+strconv.Itoa(i),
		)
		// Don't inherit `-test.run=...` etc. — TestMain's child path
		// runs the worker and exits before m.Run() so flag parsing
		// never happens. But to be safe against future TestMain
		// changes, scrub test-related stdio capture.
		cmd.Stderr = os.Stderr
		cmds[i] = cmd
	}

	// Start all children, then wait. The intent is a thundering herd
	// — N processes contending for the advisory lock at once.
	for i, cmd := range cmds {
		if err := cmd.Start(); err != nil {
			t.Fatalf("start worker %d: %v", i, err)
		}
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("worker %d failed: %v", i, err)
		}
	}

	// Parent reopens its own view of the store to make sure the
	// authoritative chain head matches what's persisted (the parent's
	// in-memory lastHash/lastID was last updated at parent open time
	// before any child wrote anything).
	store, err := audit.OpenWith(context.Background(), db)
	if err != nil {
		t.Fatalf("audit.OpenWith (post-children): %v", err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background()) })

	ctx := context.Background()
	entries, err := store.Query(ctx, audit.Filter{Limit: totalExpected + 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != totalExpected {
		t.Fatalf("expected %d entries from %d workers × %d each, got %d",
			totalExpected, numWorkers, entriesPerChild, len(entries))
	}

	r, err := store.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !r.Valid {
		t.Fatalf("multi-process appends broke the chain: %+v", r)
	}
	if r.EntriesChecked != int64(totalExpected) {
		t.Errorf("verify checked %d entries, expected %d", r.EntriesChecked, totalExpected)
	}

	// Sanity: every worker should have contributed entriesPerChild
	// entries. Detects a scenario where one worker silently lost
	// rows due to advisory-lock contention bugs.
	perWorker := make(map[string]int)
	for _, e := range entries {
		perWorker[e.Actor.ID]++
	}
	for i := 0; i < numWorkers; i++ {
		id := "worker-" + strconv.Itoa(i)
		if perWorker[id] != entriesPerChild {
			t.Errorf("worker %s wrote %d entries, expected %d", id, perWorker[id], entriesPerChild)
		}
	}
}

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
