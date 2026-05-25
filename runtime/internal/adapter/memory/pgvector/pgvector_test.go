package pgvector

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
)

// Tests for the memory:pgvector adapter.
//
// Two tiers:
//
//   - PURE UNIT: validates config parsing, identifier guards, metric
//     mapping, metadata encode/decode. Runs in plain `go test`.
//
//   - INTEGRATION: requires a live Postgres with pgvector. Gated by
//     LOAMSS_PGVECTOR_TEST_DSN; never runs in CI by default. Spin
//     up Postgres locally with:
//
//       docker run --rm -d -p 5433:5432 \
//         -e POSTGRES_PASSWORD=loamss -e POSTGRES_DB=loamsstest \
//         pgvector/pgvector:pg17
//
//     Then:
//
//       LOAMSS_PGVECTOR_TEST_DSN=\
//         "postgres://postgres:loamss@127.0.0.1:5433/loamsstest?sslmode=disable" \
//         go test ./internal/adapter/memory/pgvector/ -v
//
// We avoid testcontainers-go intentionally — it adds non-trivial
// transitive deps for a CI integration that the existing
// integration-via-env-var pattern (same as model:ollama, source:gmail)
// already satisfies.

// --- pure unit tests -------------------------------------------------------

func TestValidIdentifier(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"default", true},
		{"shared_1", true},
		{"abc123", true},
		{"", false},
		{"ABC", false},         // uppercase rejected
		{"hello-world", false}, // hyphen rejected
		{"hi there", false},    // space rejected
		{"drop;table", false},  // injection attempt
	}
	for _, c := range cases {
		got := validIdentifier(c.in)
		if got != c.want {
			t.Errorf("validIdentifier(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestMetricOperator(t *testing.T) {
	cases := map[string]string{
		"cosine":  "<=>",
		"l2":      "<->",
		"inner":   "<#>",
		"unknown": "<=>", // unknown falls back to cosine
	}
	for input, want := range cases {
		got := metricOperator(input)
		if got != want {
			t.Errorf("metricOperator(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestEncodeDecodeMetadata_RoundTrip(t *testing.T) {
	original := map[string]any{
		"source": "gmail",
		"date":   "2025-05-25",
		"score":  0.95,
	}
	bytes, err := encodeMetadata(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decodeMetadata(bytes)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["source"] != "gmail" {
		t.Errorf("source = %v", got["source"])
	}
	if got["date"] != "2025-05-25" {
		t.Errorf("date = %v", got["date"])
	}
	// json.Unmarshal restores numbers as float64; that's the
	// standard library contract and the adapter doesn't try to
	// fight it.
	if got["score"].(float64) != 0.95 {
		t.Errorf("score = %v", got["score"])
	}
}

func TestEncodeMetadata_NilProducesEmptyObject(t *testing.T) {
	b, err := encodeMetadata(nil)
	if err != nil {
		t.Fatalf("encode nil: %v", err)
	}
	if string(b) != "{}" {
		t.Errorf("nil metadata → %q, want {}", string(b))
	}
}

func TestInit_RequiresDSN(t *testing.T) {
	a := &Adapter{}
	err := a.Init(context.Background(), map[string]any{"dimension": 4})
	if err == nil {
		t.Fatal("Init without dsn should fail")
	}
	if !strings.Contains(err.Error(), "dsn") {
		t.Errorf("error should mention dsn, got: %v", err)
	}
}

func TestInit_RequiresDimension(t *testing.T) {
	a := &Adapter{}
	err := a.Init(context.Background(), map[string]any{
		"dsn": "postgres://nobody@localhost/db",
	})
	if err == nil {
		t.Fatal("Init without dimension should fail")
	}
	if !strings.Contains(err.Error(), "dimension") {
		t.Errorf("error should mention dimension, got: %v", err)
	}
}

func TestInit_RejectsBadMetric(t *testing.T) {
	a := &Adapter{}
	err := a.Init(context.Background(), map[string]any{
		"dsn":       "postgres://nobody@localhost/db",
		"dimension": 4,
		"metric":    "manhattan",
	})
	if err == nil {
		t.Fatal("Init with bad metric should fail")
	}
	if !strings.Contains(err.Error(), "metric") {
		t.Errorf("error should mention metric, got: %v", err)
	}
}

func TestInit_RejectsBadTableSuffix(t *testing.T) {
	a := &Adapter{}
	err := a.Init(context.Background(), map[string]any{
		"dsn":          "postgres://nobody@localhost/db",
		"dimension":    4,
		"table_suffix": "BAD-SUFFIX!",
	})
	if err == nil {
		t.Fatal("Init with bad table_suffix should fail")
	}
	if !strings.Contains(err.Error(), "table_suffix") {
		t.Errorf("error should mention table_suffix, got: %v", err)
	}
}

func TestUninitedAdapterRejectsCalls(t *testing.T) {
	a := &Adapter{}
	ctx := context.Background()

	if err := a.Upsert(ctx, "x", []float32{1}, nil); err == nil {
		t.Error("Upsert should fail before Init")
	}
	if _, err := a.Get(ctx, "x"); err == nil {
		t.Error("Get should fail before Init")
	}
	if _, err := a.Search(ctx, []float32{1}, 1, memory.MetadataFilter{}); err == nil {
		t.Error("Search should fail before Init")
	}
	if err := a.HealthCheck(ctx); err == nil {
		t.Error("HealthCheck should fail before Init")
	}
}

func TestRegistryPickup(t *testing.T) {
	a, err := memory.New(adapterID)
	if err != nil {
		t.Fatalf("memory.New(%q): %v", adapterID, err)
	}
	if a == nil {
		t.Error("memory.New returned nil")
	}
}

// --- integration tests (LOAMSS_PGVECTOR_TEST_DSN required) ---------------

func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("LOAMSS_PGVECTOR_TEST_DSN")
	if dsn == "" {
		t.Skip("set LOAMSS_PGVECTOR_TEST_DSN to a live Postgres with pgvector to run integration tests")
	}
	return dsn
}

func newIntegrationAdapter(t *testing.T, dim int) *Adapter {
	t.Helper()
	dsn := integrationDSN(t)
	a := &Adapter{}
	// Use a unique table suffix per test so parallel runs / repeat
	// invocations don't step on each other.
	suffix := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	if err := a.Init(context.Background(), map[string]any{
		"dsn":          dsn,
		"dimension":    dim,
		"table_suffix": "test_" + suffix,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		// Drop the per-test table so subsequent runs start clean.
		// Best-effort; we don't care if it fails.
		ctx := context.Background()
		_, _ = a.pool.Exec(ctx, "DROP TABLE IF EXISTS "+a.table)
		_ = a.Close(ctx)
	})
	return a
}

func TestIntegration_UpsertSearchRoundTrip(t *testing.T) {
	a := newIntegrationAdapter(t, 4)
	ctx := context.Background()

	entries := []memory.Entry{
		{ID: "alpha", Vector: []float32{1, 0, 0, 0}, Metadata: map[string]any{"tag": "a"}},
		{ID: "beta", Vector: []float32{0, 1, 0, 0}, Metadata: map[string]any{"tag": "b"}},
		{ID: "gamma", Vector: []float32{0, 0, 1, 0}, Metadata: map[string]any{"tag": "c"}},
	}
	if err := a.BatchUpsert(ctx, entries); err != nil {
		t.Fatalf("BatchUpsert: %v", err)
	}

	// Query close to "alpha"; expect alpha first.
	hits, err := a.Search(ctx, []float32{1, 0, 0, 0}, 2, memory.MetadataFilter{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if hits[0].ID != "alpha" {
		t.Errorf("nearest hit = %q, want alpha", hits[0].ID)
	}
	if hits[0].Distance > hits[1].Distance {
		t.Errorf("hits not sorted by distance: %v then %v", hits[0].Distance, hits[1].Distance)
	}
}

func TestIntegration_GetAndDelete(t *testing.T) {
	a := newIntegrationAdapter(t, 3)
	ctx := context.Background()

	if err := a.Upsert(ctx, "x", []float32{1, 2, 3}, map[string]any{"k": "v"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	entry, err := a.Get(ctx, "x")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.ID != "x" {
		t.Errorf("ID = %q", entry.ID)
	}
	if entry.Metadata["k"] != "v" {
		t.Errorf("metadata k = %v", entry.Metadata["k"])
	}

	if err := a.Delete(ctx, "x"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	_, err = a.Get(ctx, "x")
	if !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("Get after delete should be ErrNotFound, got %v", err)
	}

	// Idempotent.
	if err := a.Delete(ctx, "x"); err != nil {
		t.Errorf("Delete on missing should be nil, got %v", err)
	}
}

func TestIntegration_DimensionMismatch(t *testing.T) {
	a := newIntegrationAdapter(t, 4)
	ctx := context.Background()

	err := a.Upsert(ctx, "wrong", []float32{1, 2, 3}, nil) // 3-d, table is 4-d
	if !errors.Is(err, memory.ErrDimensionMismatch) {
		t.Errorf("Upsert with wrong dim should be ErrDimensionMismatch, got %v", err)
	}
}

func TestIntegration_SearchWithMetadataFilter(t *testing.T) {
	a := newIntegrationAdapter(t, 4)
	ctx := context.Background()

	if err := a.BatchUpsert(ctx, []memory.Entry{
		{ID: "1", Vector: []float32{1, 0, 0, 0}, Metadata: map[string]any{"kind": "a"}},
		{ID: "2", Vector: []float32{1, 0, 0, 0}, Metadata: map[string]any{"kind": "b"}},
		{ID: "3", Vector: []float32{1, 0, 0, 0}, Metadata: map[string]any{"kind": "a"}},
	}); err != nil {
		t.Fatalf("BatchUpsert: %v", err)
	}

	hits, err := a.Search(ctx, []float32{1, 0, 0, 0}, 10, memory.MetadataFilter{
		Equals: map[string]any{"kind": "a"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("filtered search expected 2 hits, got %d", len(hits))
	}
	for _, h := range hits {
		if h.Metadata["kind"] != "a" {
			t.Errorf("filter leaked entry with kind=%v", h.Metadata["kind"])
		}
	}
}

func TestIntegration_Stats(t *testing.T) {
	a := newIntegrationAdapter(t, 2)
	ctx := context.Background()

	if err := a.Upsert(ctx, "one", []float32{1, 0}, nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := a.Upsert(ctx, "two", []float32{0, 1}, nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	stats, err := a.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Count != 2 {
		t.Errorf("Count = %d, want 2", stats.Count)
	}
	if stats.Dimension != 2 {
		t.Errorf("Dimension = %d, want 2", stats.Dimension)
	}
	if stats.BackendInfo["table"] == "" {
		t.Errorf("BackendInfo should include table name")
	}
}

func TestIntegration_HealthCheck(t *testing.T) {
	a := newIntegrationAdapter(t, 1)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck on live adapter should pass, got %v", err)
	}
}
