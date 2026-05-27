package memory

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// stubAdapter is an in-memory Adapter for layer tests. We don't need
// real vector search here; we just want to verify that Upsert/Delete
// are forwarded correctly.
type stubAdapter struct {
	mu      sync.Mutex
	entries map[string]stubEntry
}

type stubEntry struct {
	vector   []float32
	metadata map[string]any
}

func newStubAdapter() *stubAdapter {
	return &stubAdapter{entries: map[string]stubEntry{}}
}

func (a *stubAdapter) Upsert(_ context.Context, id string, vector []float32, metadata map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries[id] = stubEntry{vector: vector, metadata: metadata}
	return nil
}

func (a *stubAdapter) Delete(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.entries, id)
	return nil
}

func (a *stubAdapter) has(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.entries[id]
	return ok
}

// silentLogger discards everything; tests assert on side-effects, not
// on log lines.
var silentLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func newLayer(t *testing.T) (Layer, *stubAdapter, *Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(context.Background(), filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	adapter := newStubAdapter()
	return New(adapter, store, nil, silentLogger), adapter, store
}

// --- Upsert / Delete ----------------------------------------------------

func TestLayer_UpsertWritesToAdapterWhenVectorPresent(t *testing.T) {
	l, adapter, _ := newLayer(t)
	err := l.Upsert(context.Background(), Entry{
		Namespace:  "gmail-personal",
		ID:         "m1",
		Content:    "hello",
		Embeddings: []float32{0.1, 0.2, 0.3},
		Metadata: map[string]any{
			"from": "Sarah Smith <sarah@example.com>",
		},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !adapter.has("gmail-personal:m1") {
		t.Error("adapter did not receive the entry")
	}
}

func TestLayer_UpsertSkipsAdapterWhenNoVector(t *testing.T) {
	l, adapter, store := newLayer(t)
	err := l.Upsert(context.Background(), Entry{
		Namespace: "gmail-personal",
		ID:        "m1",
		Content:   "hello",
		Metadata: map[string]any{
			"from": "Sarah Smith <sarah@example.com>",
		},
		// No embeddings — common in production when no embedding-
		// capable model adapter is configured.
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if adapter.has("gmail-personal:m1") {
		t.Error("adapter should NOT have received an entry with empty vector")
	}
	// But the layer's entity derivation should still run.
	ents, _ := store.ListEntities(context.Background(), EntityFilter{Namespace: "gmail-personal"})
	if len(ents) == 0 {
		t.Error("expected entity derivation to run even without embeddings")
	}
}

// stubEmbedder returns a fixed-length vector regardless of input.
// Tracks the texts it was asked to embed so tests can assert call
// patterns.
type stubEmbedder struct {
	mu     sync.Mutex
	texts  []string
	vector []float32
	err    error
}

func (e *stubEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.texts = append(e.texts, text)
	if e.err != nil {
		return nil, e.err
	}
	return e.vector, nil
}

func TestLayer_UpsertAutoEmbedsWhenEmbedderConfigured(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(context.Background(), filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	adapter := newStubAdapter()
	emb := &stubEmbedder{vector: []float32{0.1, 0.2, 0.3}}
	l := New(adapter, store, emb, silentLogger)

	err = l.Upsert(context.Background(), Entry{
		Namespace: "files-notes",
		ID:        "n1",
		Content:   "Sarah is planning a Q3 contract talk.",
		Metadata: map[string]any{
			"path": "/notes/sarah.md",
		},
		// No Embeddings — should trigger auto-embed.
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !adapter.has("files-notes:n1") {
		t.Fatal("adapter should have received the entry (auto-embedded)")
	}
	if len(emb.texts) != 1 || emb.texts[0] != "Sarah is planning a Q3 contract talk." {
		t.Errorf("embedder.texts = %v, want one entry with the content", emb.texts)
	}
}

func TestLayer_UpsertPrefersCallerVectorOverEmbedder(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(context.Background(), filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	adapter := newStubAdapter()
	emb := &stubEmbedder{vector: []float32{9, 9, 9}}
	l := New(adapter, store, emb, silentLogger)

	err = l.Upsert(context.Background(), Entry{
		Namespace:  "files-notes",
		ID:         "n1",
		Content:    "ignored when caller supplies a vector",
		Embeddings: []float32{0.5, 0.5, 0.5},
		Metadata:   map[string]any{"path": "/x"},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(emb.texts) != 0 {
		t.Errorf("embedder should not have been called; got texts=%v", emb.texts)
	}
}

func TestLayer_UpsertSurvivesEmbedderFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(context.Background(), filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	adapter := newStubAdapter()
	emb := &stubEmbedder{err: errors.New("model unreachable")}
	l := New(adapter, store, emb, silentLogger)

	err = l.Upsert(context.Background(), Entry{
		Namespace: "files-notes",
		ID:        "n1",
		Content:   "anything",
		Metadata:  map[string]any{"path": "/x"},
	})
	if err != nil {
		t.Fatalf("Upsert should not fail when embedder errors: %v", err)
	}
	if adapter.has("files-notes:n1") {
		t.Error("adapter should not have an entry (no vector after embedder failure)")
	}
}

func TestLayer_DeleteRemovesFromAdapterAndUnlinks(t *testing.T) {
	l, adapter, store := newLayer(t)
	ctx := context.Background()
	_ = l.Upsert(ctx, Entry{
		Namespace: "gmail-personal",
		ID:        "m1",
		Metadata: map[string]any{
			"from": "Sarah <sarah@example.com>",
			"to":   "bob@example.com",
		},
	})
	// Confirm pre-state: at least one entity linked to m1.
	ents, _ := store.ListEntities(ctx, EntityFilter{Namespace: "gmail-personal"})
	if len(ents) == 0 {
		t.Fatal("expected entities after Upsert")
	}

	if err := l.Delete(ctx, "gmail-personal", "m1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if adapter.has("gmail-personal:m1") {
		t.Error("adapter still has the entry")
	}
	// Entity rows survive (they may have other entries); but the
	// entry_count for entities ONLY linked to m1 should be 0.
	for _, e := range ents {
		refreshed, err := l.GetEntity(ctx, e.ID)
		if err != nil {
			t.Fatalf("GetEntity %s: %v", e.ID, err)
		}
		if refreshed.EntryCount != 0 {
			t.Errorf("entity %s entry_count: got %d, want 0",
				e.ID, refreshed.EntryCount)
		}
	}
}

// --- entity resolution ----------------------------------------------

func TestLayer_SameEmailAcrossEntries_OneEntity(t *testing.T) {
	l, _, _ := newLayer(t)
	ctx := context.Background()
	// First entry: from address has no display name → canonical
	// falls back to local-part "sarah".
	_ = l.Upsert(ctx, Entry{
		Namespace: "ns",
		ID:        "m1",
		Metadata: map[string]any{
			"from":          "sarah@example.com",
			"internal_date": "2026-05-23T10:00:00Z",
		},
	})
	// Second entry: same email, now with display name → canonical
	// upgrades to "Sarah Smith".
	_ = l.Upsert(ctx, Entry{
		Namespace: "ns",
		ID:        "m2",
		Metadata: map[string]any{
			"from":          `"Sarah Smith" <sarah@example.com>`,
			"internal_date": "2026-05-24T10:00:00Z",
		},
	})
	ents, err := l.ListEntities(ctx, EntityFilter{Namespace: "ns"})
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(ents) != 1 {
		t.Fatalf("expected 1 entity (deduped), got %d", len(ents))
	}
	if ents[0].Canonical != "Sarah Smith" {
		t.Errorf("canonical should have upgraded; got %q", ents[0].Canonical)
	}
	if ents[0].EntryCount != 2 {
		t.Errorf("entry_count: %d", ents[0].EntryCount)
	}
	if ents[0].FirstSeen.IsZero() || ents[0].LastSeen.IsZero() {
		t.Error("time range not populated from entry dates")
	}
	if !ents[0].LastSeen.After(ents[0].FirstSeen) {
		t.Errorf("LastSeen %v not after FirstSeen %v",
			ents[0].LastSeen, ents[0].FirstSeen)
	}
}

func TestLayer_DifferentNamespacesDoNotMerge(t *testing.T) {
	l, _, _ := newLayer(t)
	ctx := context.Background()
	_ = l.Upsert(ctx, Entry{
		Namespace: "gmail-personal",
		ID:        "m1",
		Metadata:  map[string]any{"from": "sarah@example.com"},
	})
	_ = l.Upsert(ctx, Entry{
		Namespace: "gmail-work",
		ID:        "m1",
		Metadata:  map[string]any{"from": "sarah@example.com"},
	})
	all, _ := l.ListEntities(ctx, EntityFilter{})
	if len(all) != 2 {
		t.Errorf("expected 2 entities (cross-namespace not merged), got %d", len(all))
	}
}

func TestLayer_EntriesByEntity(t *testing.T) {
	l, _, _ := newLayer(t)
	ctx := context.Background()
	for i, when := range []string{
		"2026-05-21T10:00:00Z",
		"2026-05-23T10:00:00Z",
		"2026-05-22T10:00:00Z",
	} {
		_ = l.Upsert(ctx, Entry{
			Namespace: "ns",
			ID:        msgID(i),
			Metadata: map[string]any{
				"from":          "alice@example.com",
				"internal_date": when,
			},
		})
	}
	ents, _ := l.ListEntities(ctx, EntityFilter{Namespace: "ns", Alias: "alice@example.com"})
	if len(ents) != 1 {
		t.Fatalf("expected to find Alice, got %d entities", len(ents))
	}
	entries, err := l.EntriesByEntity(ctx, ents[0].ID, 10)
	if err != nil {
		t.Fatalf("EntriesByEntity: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// Should be newest first.
	for i := 0; i+1 < len(entries); i++ {
		if entries[i].Date.Before(entries[i+1].Date) {
			t.Errorf("entries not in descending order: %v then %v",
				entries[i].Date, entries[i+1].Date)
		}
	}
	if entries[0].Role != RoleFrom {
		t.Errorf("role: %v", entries[0].Role)
	}
}

// --- thread resolution ----------------------------------------------

func TestLayer_ThreadGroupsByGmailThreadID(t *testing.T) {
	l, _, _ := newLayer(t)
	ctx := context.Background()
	for i, when := range []string{
		"2026-05-22T10:00:00Z", // older
		"2026-05-24T10:00:00Z", // newer
	} {
		_ = l.Upsert(ctx, Entry{
			Namespace: "ns",
			ID:        msgID(i),
			Metadata: map[string]any{
				"gmail_thread_id": "thr-abc",
				"subject":         "Project status",
				"internal_date":   when,
			},
		})
	}
	threads, err := l.ListThreads(ctx, ThreadFilter{Namespace: "ns"})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected 1 thread, got %d", len(threads))
	}
	if threads[0].EntryCount != 2 {
		t.Errorf("entry_count: %d", threads[0].EntryCount)
	}
	if threads[0].Subject != "Project status" {
		t.Errorf("subject: %q", threads[0].Subject)
	}
	if threads[0].ExternalID != "thr-abc" {
		t.Errorf("external_id: %q", threads[0].ExternalID)
	}

	entries, err := l.EntriesByThread(ctx, threads[0].ID, 10)
	if err != nil {
		t.Fatalf("EntriesByThread: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Should be oldest first (reading order).
	if entries[0].Date.After(entries[1].Date) {
		t.Errorf("entries not in ascending order: %v then %v",
			entries[0].Date, entries[1].Date)
	}
}

// --- error paths -----------------------------------------------------

func TestLayer_GetEntity_NotFound(t *testing.T) {
	l, _, _ := newLayer(t)
	_, err := l.GetEntity(context.Background(), "ent_does_not_exist")
	if !errors.Is(err, ErrEntityNotFound) {
		t.Errorf("err=%v, want ErrEntityNotFound", err)
	}
}

func TestLayer_GetThread_NotFound(t *testing.T) {
	l, _, _ := newLayer(t)
	_, err := l.GetThread(context.Background(), "thr_does_not_exist")
	if !errors.Is(err, ErrThreadNotFound) {
		t.Errorf("err=%v, want ErrThreadNotFound", err)
	}
}

func TestLayer_Upsert_RequiresNamespaceAndID(t *testing.T) {
	l, _, _ := newLayer(t)
	err := l.Upsert(context.Background(), Entry{ID: "m1"})
	if err == nil {
		t.Error("expected error for missing namespace")
	}
	err = l.Upsert(context.Background(), Entry{Namespace: "ns"})
	if err == nil {
		t.Error("expected error for missing id")
	}
}

// --- bench-ish: rough write path timing ------------------------------

func TestLayer_HundredEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	l, _, _ := newLayer(t)
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 100; i++ {
		err := l.Upsert(ctx, Entry{
			Namespace: "ns",
			ID:        msgID(i),
			Metadata: map[string]any{
				"from":            "alice@example.com",
				"to":              "bob@example.com",
				"gmail_thread_id": "thr-" + msgID(i%10), // 10 threads
				"subject":         "thread " + msgID(i%10),
				"internal_date":   time.Now().UTC().Format(time.RFC3339Nano),
			},
		})
		if err != nil {
			t.Fatalf("Upsert i=%d: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	t.Logf("100 Upserts: %v (%v/op)", elapsed, elapsed/100)

	ents, _ := l.ListEntities(ctx, EntityFilter{})
	if len(ents) != 2 {
		t.Errorf("expected 2 entities (alice + bob), got %d", len(ents))
	}
	thr, _ := l.ListThreads(ctx, ThreadFilter{})
	if len(thr) != 10 {
		t.Errorf("expected 10 threads, got %d", len(thr))
	}
}

// --- helpers ---------------------------------------------------------

func msgID(i int) string {
	return "m" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	negative := i < 0
	if negative {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
