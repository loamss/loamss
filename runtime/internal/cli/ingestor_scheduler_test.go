package cli

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/source"
)

// stubInstalled returns a capsule.Installed with just the Manifest
// field populated — enough to drive the scheduler's lifecycle hook
// without spinning up a real capsule subprocess.
func stubInstalled(name string, m *capsule.Manifest) capsule.Installed {
	return capsule.Installed{Name: name, Manifest: m}
}

// bareInstalled returns a capsule.Installed with no manifest. Used
// to confirm the scheduler's nil-manifest defenses.
func bareInstalled(name string, _ any) capsule.Installed {
	return capsule.Installed{Name: name}
}

// --- parseSyncToolResult --------------------------------------------------

func TestParseSyncToolResult_EnvelopeShape(t *testing.T) {
	envelope := map[string]any{
		"content": []map[string]any{{
			"type": "text",
			"text": `{"records_added":12,"records_updated":3,"bytes_ingested":87234,"errors":1}`,
		}},
		"isError": false,
	}
	c, isErr, err := parseSyncToolResult(envelope)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if isErr {
		t.Error("isError should be false")
	}
	if c.RecordsAdded != 12 || c.RecordsUpdated != 3 || c.BytesIngested != 87234 || c.Errors != 1 {
		t.Errorf("counters: %+v", c)
	}
}

func TestParseSyncToolResult_IsErrorPassesThrough(t *testing.T) {
	envelope := map[string]any{
		"content": []map[string]any{{
			"type": "text",
			"text": `{"records_added":0,"errors":1}`,
		}},
		"isError": true,
	}
	_, isErr, err := parseSyncToolResult(envelope)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !isErr {
		t.Error("isError should be true")
	}
}

func TestParseSyncToolResult_DirectShape(t *testing.T) {
	// A capsule that returns counters without the MCP envelope is
	// accepted — defensive, matches the resilience comment in the
	// parser.
	direct := map[string]any{
		"records_added":   int64(7),
		"records_updated": int64(0),
		"bytes_ingested":  int64(1024),
		"errors":          0,
	}
	c, isErr, err := parseSyncToolResult(direct)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if isErr {
		t.Error("direct shape has no isError signal; should be false")
	}
	if c.RecordsAdded != 7 || c.BytesIngested != 1024 {
		t.Errorf("counters: %+v", c)
	}
}

func TestParseSyncToolResult_NilResultIsError(t *testing.T) {
	if _, _, err := parseSyncToolResult(nil); err == nil {
		t.Error("nil result should error")
	}
}

func TestParseSyncToolResult_UnparseableShape(t *testing.T) {
	weird := "not a map"
	if _, _, err := parseSyncToolResult(weird); err == nil {
		t.Error("string result should error")
	}
}

// --- scheduler hook semantics ---------------------------------------------

func TestScheduler_OnCapsuleStarted_NoOpForNonIngestor(t *testing.T) {
	s := newTestScheduler(t)
	defer s.stop()

	// A capsule with no manifest is the null case; should not panic.
	s.OnCapsuleStarted(context.Background(), bareInstalled("plain-capsule", nil))

	// Idempotent stop.
	s.OnCapsuleStopped(context.Background(), "plain-capsule")
}

func TestScheduler_OnCapsuleStarted_NoOpWhenNoIngestorRole(t *testing.T) {
	// Manifest carries an Ingestor block (defensive) but Roles list
	// doesn't include "ingestor" — schedule should not fire.
	s := newTestScheduler(t)
	defer s.stop()

	m := &capsule.Manifest{
		Roles: []string{"actuator"},
		Ingestor: &capsule.IngestorSpec{
			SourceID:  "source:ghost",
			Schedule:  capsule.IngestorSchedule{Interval: "1m"},
			OnTrigger: "sync",
		},
	}
	s.OnCapsuleStarted(context.Background(), stubInstalled("with-ingestor-block-only", m))

	s.mu.Lock()
	_, scheduled := s.tickers["with-ingestor-block-only"]
	s.mu.Unlock()
	if scheduled {
		t.Error("scheduler should not have created a ticker for non-ingestor role")
	}
}

func TestScheduler_OnCapsuleStarted_RejectsBadInterval(t *testing.T) {
	s := newTestScheduler(t)
	defer s.stop()

	m := &capsule.Manifest{
		Roles: []string{"ingestor"},
		Ingestor: &capsule.IngestorSpec{
			SourceID:  "source:calendar",
			Schedule:  capsule.IngestorSchedule{Interval: "not-a-duration"},
			OnTrigger: "sync",
		},
	}
	s.OnCapsuleStarted(context.Background(), stubInstalled("bad-interval", m))

	s.mu.Lock()
	_, scheduled := s.tickers["bad-interval"]
	s.mu.Unlock()
	if scheduled {
		t.Error("scheduler should refuse to schedule with bad interval")
	}
}

// --- test fixture ---------------------------------------------------------

func newTestScheduler(t *testing.T) *ingestorScheduler {
	t.Helper()
	dir := t.TempDir()
	srcStore, err := source.OpenStore(context.Background(), filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("source.OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = srcStore.Close() })

	w, err := audit.OpenSQLite(context.Background(), filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatalf("audit.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = w.Close(context.Background()) })

	// Real but empty Host — Client(name) returns nil for any
	// capsule, which is exactly the "capsule isn't running" branch
	// the runOneSync test wants to exercise.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	capStore, err := capsule.OpenStore(context.Background(), filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("capsule.OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = capStore.Close() })
	host := capsule.NewHost(capStore, nil, w, nil, logger)
	return newIngestorScheduler(host, srcStore, w, logger)
}

// TestScheduler_RunOneSync_PersistsSuccessSummary exercises the
// audit + source.Store side-effects of a successful tick by calling
// runOneSync directly with a stub Host wired to return a canned
// result. Goal: ensure source.SetLastSync gets the right shape and
// that source.sync.{started,completed} pair show up in audit.
func TestScheduler_RunOneSync_RecordsFailedWhenCapsuleMissing(t *testing.T) {
	// We can drive runOneSync via the scheduler's failed path
	// without a Host by passing in a name the Host doesn't know
	// about — host.Client returns nil and the scheduler emits the
	// "capsule is not running" failed entry.
	s := newTestScheduler(t)
	defer s.stop()

	// Insert a sources row so SetLastSync has a target. The bridge
	// would normally do this at capsule install.
	ctx := context.Background()
	_, err := s.sources.Insert(ctx, source.Configured{
		Name:         "ghost-ingestor",
		AdapterID:    "source:ghost",
		OwnerCapsule: "ghost-ingestor",
	})
	if err != nil {
		t.Fatalf("seed sources row: %v", err)
	}

	s.runOneSync(ctx, "ghost-ingestor", "source:ghost", "sync")

	// last_sync_status should be "error".
	got, _ := s.sources.Get(ctx, "ghost-ingestor")
	if got.LastSyncStatus != "error" {
		t.Errorf("last_sync_status: %q, want error", got.LastSyncStatus)
	}
	if got.LastSyncSummary["error_message"] == nil {
		t.Errorf("summary should include error_message; got %+v", got.LastSyncSummary)
	}

	// Audit: one started + one completed=error entry.
	started, _ := s.audit.Query(ctx, audit.Filter{Types: []string{"source.sync.started"}})
	completed, _ := s.audit.Query(ctx, audit.Filter{Types: []string{"source.sync.completed"}})
	if len(started) != 1 || len(completed) != 1 {
		t.Errorf("audit counts: started=%d completed=%d", len(started), len(completed))
	}
	if len(completed) == 1 && completed[0].Outcome != audit.OutcomeError {
		t.Errorf("completed outcome: %s, want error", completed[0].Outcome)
	}
}

// Verify the parser handles a real, end-to-end-shaped tool result
// the way a SDK-built ingestor capsule would return.
func TestParseSyncToolResult_RealisticEnvelope(t *testing.T) {
	counters := map[string]any{
		"records_added":   12,
		"records_updated": 3,
		"bytes_ingested":  87234,
		"errors":          0,
	}
	countersJSON, _ := json.Marshal(counters)
	envelope := map[string]any{
		"content": []map[string]any{{
			"type": "text",
			"text": string(countersJSON),
		}},
		"isError": false,
	}
	c, isErr, err := parseSyncToolResult(envelope)
	if err != nil || isErr {
		t.Fatalf("parse: err=%v isErr=%v", err, isErr)
	}
	if c.RecordsAdded != 12 {
		t.Errorf("RecordsAdded: %d", c.RecordsAdded)
	}
}

// Pin the tick boundary: an interval ≥ 1m means OnCapsuleStarted
// will not fire its first tick during the test's lifetime, so we
// can assert the ticker was created without dealing with timing
// flakes.
func TestScheduler_OnCapsuleStarted_CreatesTickerForIngestor(t *testing.T) {
	s := newTestScheduler(t)
	defer s.stop()

	m := &capsule.Manifest{
		Roles: []string{"ingestor"},
		Ingestor: &capsule.IngestorSpec{
			SourceID: "source:hn",
			Schedule: capsule.IngestorSchedule{
				Interval: "1h",  // far longer than the test
				Initial:  "30m", // also far longer than the test
			},
			OnTrigger: "sync",
		},
	}
	s.OnCapsuleStarted(context.Background(), stubInstalled("hn-ingestor", m))

	// Ticker is registered.
	s.mu.Lock()
	_, scheduled := s.tickers["hn-ingestor"]
	s.mu.Unlock()
	if !scheduled {
		t.Error("expected ticker for ingestor capsule")
	}

	// Stopping releases the ticker.
	s.OnCapsuleStopped(context.Background(), "hn-ingestor")
	s.mu.Lock()
	_, stillScheduled := s.tickers["hn-ingestor"]
	s.mu.Unlock()
	if stillScheduled {
		t.Error("ticker should be gone after OnCapsuleStopped")
	}
}

// Drain timing guards: stop returns quickly even when there's an
// active ticker in its initial-delay state.
func TestScheduler_Stop_DrainsTickersQuickly(t *testing.T) {
	s := newTestScheduler(t)

	m := &capsule.Manifest{
		Roles: []string{"ingestor"},
		Ingestor: &capsule.IngestorSpec{
			SourceID:  "source:hn",
			Schedule:  capsule.IngestorSchedule{Interval: "1h", Initial: "1h"},
			OnTrigger: "sync",
		},
	}
	s.OnCapsuleStarted(context.Background(), stubInstalled("hn", m))

	done := make(chan struct{})
	go func() {
		s.stop()
		close(done)
	}()
	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not drain within 2s")
	}
}
