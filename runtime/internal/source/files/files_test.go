// files_test.go drives source:files end-to-end against:
//   - a real on-disk fixture directory (created in t.TempDir)
//   - a real memory layer (memlayer + the sqlite memory adapter)
//   - a real fs-encrypted storage adapter (for the raw bytes)
//
// What this proves about the substrate, beyond what gmail_test
// covers:
//
//   - The Source SPI is provider-agnostic — source:files plugs in
//     the same way source:gmail does, just without any OAuth.
//   - The memory layer's entity + thread resolvers don't need
//     Gmail-shaped metadata; any source that writes "from / to /
//     subject / gmail_thread_id" (or anything that maps to those)
//     surfaces entities and threads the same way.
//   - Incremental sync works: edit a file, re-sync, only the changed
//     file is re-ingested; delete a file, it's removed from the
//     layer + adapter.

package files

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	memadapter "github.com/loamss/loamss/runtime/internal/adapter/memory"
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/sqlite"
	"github.com/loamss/loamss/runtime/internal/adapter/storage"
	_ "github.com/loamss/loamss/runtime/internal/adapter/storage/fsencrypted"
	memlayer "github.com/loamss/loamss/runtime/internal/memory"
	"github.com/loamss/loamss/runtime/internal/source"
)

// --- harness ---------------------------------------------------------

// silentLogger discards everything; the source's slog calls aren't
// the test surface.
type silentLogger struct{}

func (silentLogger) Info(string, ...any)  {}
func (silentLogger) Warn(string, ...any)  {}
func (silentLogger) Error(string, ...any) {}
func (silentLogger) Debug(string, ...any) {}

// memBridge adapts memlayer.Layer to source.MemoryAdapter (the
// narrow interface sources see). Mirrors cli/source.go's bridge.
type memBridge struct{ layer memlayer.Layer }

func (b memBridge) Upsert(ctx context.Context, e source.MemoryEntry) error {
	return b.layer.Upsert(ctx, memlayer.Entry{
		Namespace:  e.Namespace,
		ID:         e.ID,
		Content:    e.Content,
		Metadata:   e.Metadata,
		Embeddings: e.Embeddings,
	})
}

func (b memBridge) Delete(ctx context.Context, namespace, id string) error {
	return b.layer.Delete(ctx, namespace, id)
}

// harness bundles the real-adapter chain a sync drives through:
//
//	files-source → storage adapter (raw bytes)
//	             → memory layer    → memory adapter (vectors)
//	                               → memory_layer_* tables
type harness struct {
	t          *testing.T
	root       string // where the source reads files from
	src        source.Source
	storage    storage.Adapter
	memAdpt    memadapter.Adapter
	memLayer   memlayer.Layer
	layerStore *memlayer.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()

	root := t.TempDir()    // the source's content root
	dataDir := t.TempDir() // where runtime stores its state

	// Real fs-encrypted storage.
	stor, err := storage.New("storage:fs-encrypted")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	if err := stor.Init(ctx, map[string]any{
		"root": filepath.Join(dataDir, "storage"),
	}); err != nil {
		t.Fatalf("storage.Init: %v", err)
	}
	t.Cleanup(func() { _ = stor.Close(ctx) })

	// Real sqlite memory adapter.
	memAdpt, err := memadapter.New("memory:sqlite")
	if err != nil {
		t.Fatalf("memadapter.New: %v", err)
	}
	if err := memAdpt.Init(ctx, map[string]any{
		"path": filepath.Join(dataDir, "memory.db"),
	}); err != nil {
		t.Fatalf("memadapter.Init: %v", err)
	}
	t.Cleanup(func() { _ = memAdpt.Close(ctx) })

	// Real memory layer.
	layerStore, err := memlayer.OpenStore(ctx, filepath.Join(dataDir, "runtime.db"))
	if err != nil {
		t.Fatalf("memlayer.OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = layerStore.Close() })
	layer := memlayer.New(memAdpt, layerStore, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Construct + Init the source.
	src := New()
	if err := src.Init(ctx, source.Deps{
		SourceName:  "notes",
		Config:      map[string]any{"root": root},
		Storage:     stor,
		Memory:      memBridge{layer: layer},
		Credentials: source.NewMemoryCredentialStore(),
		Logger:      silentLogger{},
	}); err != nil {
		t.Fatalf("source.Init: %v", err)
	}

	return &harness{
		t:          t,
		root:       root,
		src:        src,
		storage:    stor,
		memAdpt:    memAdpt,
		memLayer:   layer,
		layerStore: layerStore,
	}
}

func (h *harness) writeFile(name, content string) string {
	h.t.Helper()
	full := filepath.Join(h.root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		h.t.Fatalf("mkdir %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		h.t.Fatalf("write %s: %v", name, err)
	}
	return full
}

func (h *harness) sync(cursor []byte) source.SyncResult {
	h.t.Helper()
	res, err := h.src.Sync(context.Background(), cursor)
	if err != nil {
		h.t.Fatalf("Sync: %v", err)
	}
	return res
}

// --- ID / Init / AuthStatus ------------------------------------------

func TestFiles_ID(t *testing.T) {
	if SourceID != "source:files" {
		t.Errorf("SourceID: %q", SourceID)
	}
}

func TestFiles_RegisteredInSourceRegistry(t *testing.T) {
	found := false
	for _, id := range source.Registered() {
		if id == SourceID {
			found = true
		}
	}
	if !found {
		t.Error("source:files not in registry")
	}
}

func TestFiles_Init_RequiresRoot(t *testing.T) {
	s := New()
	err := s.Init(context.Background(), source.Deps{
		Storage: stubStorage(),
	})
	if err == nil {
		t.Error("expected error for missing root")
	}
}

func TestFiles_Init_RejectsNonDirectory(t *testing.T) {
	tmp := t.TempDir()
	notDir := filepath.Join(tmp, "file.txt")
	_ = os.WriteFile(notDir, []byte("hi"), 0o600)
	s := New()
	err := s.Init(context.Background(), source.Deps{
		Storage: stubStorage(),
		Config:  map[string]any{"root": notDir},
	})
	if err == nil {
		t.Error("expected error: root is not a directory")
	}
}

func TestFiles_AuthFlowIsNone(t *testing.T) {
	h := newHarness(t)
	flow, err := h.src.BeginAuth(context.Background())
	if err != nil {
		t.Fatalf("BeginAuth: %v", err)
	}
	if flow.Kind != source.AuthFlowNone {
		t.Errorf("flow.Kind: %q (want none)", flow.Kind)
	}
	if err := h.src.CompleteAuth(context.Background(), nil); err != nil {
		t.Errorf("CompleteAuth: %v", err)
	}
	status, _ := h.src.AuthStatus(context.Background())
	if !status.Authenticated {
		t.Errorf("AuthStatus should be authenticated; got %+v", status)
	}
}

// --- end-to-end ingest -----------------------------------------------

func TestFiles_E2E_IngestsFilesWithFrontmatter(t *testing.T) {
	h := newHarness(t)

	// A realistic corpus: three notes in one "thread", two participants.
	h.writeFile("project-alpha/01-kickoff.md", `---
from: Sarah Smith <sarah@example.com>
to: Bob Lee <bob@example.com>
subject: Project Alpha kickoff
thread: proj-alpha-001
date: 2026-05-20T10:00:00Z
---

Kickoff scheduled Monday. Please review the brief beforehand.
`)
	h.writeFile("project-alpha/02-reply.md", `---
from: Bob Lee <bob@example.com>
to: Sarah Smith <sarah@example.com>
subject: Re: Project Alpha kickoff
thread: proj-alpha-001
date: 2026-05-21T09:30:00Z
---

Reviewed. Sending notes shortly.
`)
	h.writeFile("project-alpha/03-followup.md", `---
from: Sarah Smith <sarah@example.com>
to: Bob Lee <bob@example.com>
subject: Re: Project Alpha kickoff
thread: proj-alpha-001
date: 2026-05-22T11:00:00Z
---

Standup at 2pm.
`)
	// One file in a different thread with a different participant.
	h.writeFile("project-beta/01-status.md", `---
from: Carol Wu <carol@example.com>
to: Sarah Smith <sarah@example.com>
subject: Project Beta status
thread: proj-beta-001
date: 2026-05-20T14:00:00Z
---

Vendor confirmed delivery.
`)
	// And a plain text file with no frontmatter — should ingest but
	// not produce entities.
	h.writeFile("scratch.txt", "Random unfiled note, no metadata.\n")

	result := h.sync(nil)

	if result.RecordsAdded != 5 {
		t.Errorf("RecordsAdded: got %d, want 5", result.RecordsAdded)
	}
	if result.BytesIngested == 0 {
		t.Errorf("BytesIngested: zero")
	}
	if len(result.Errors) > 0 {
		t.Errorf("unexpected errors: %+v", result.Errors)
	}

	// --- entities the layer resolved ---
	ctx := context.Background()
	ents, err := h.memLayer.ListEntities(ctx, memlayer.EntityFilter{Namespace: "notes"})
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	// Sarah + Bob + Carol = 3 distinct people across notes namespace.
	if len(ents) != 3 {
		names := make([]string, len(ents))
		for i, e := range ents {
			names[i] = e.Canonical
		}
		t.Errorf("expected 3 entities (Sarah, Bob, Carol), got %d: %v",
			len(ents), names)
	}

	// Spot-check Sarah specifically — she's in 4 entries
	// (3 alpha + 1 beta, all involving her).
	var sarah *memlayer.Entity
	for i := range ents {
		if ents[i].Canonical == "Sarah Smith" {
			sarah = &ents[i]
			break
		}
	}
	if sarah == nil {
		t.Fatal("Sarah Smith not found among entities")
	}
	if sarah.EntryCount != 4 {
		t.Errorf("Sarah entry_count: got %d, want 4", sarah.EntryCount)
	}

	// --- threads the layer derived ---
	threads, err := h.memLayer.ListThreads(ctx, memlayer.ThreadFilter{Namespace: "notes"})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(threads))
	}

	// Alpha thread should have 3 entries in reading order.
	var alpha *memlayer.Thread
	for i := range threads {
		if threads[i].ExternalID == "proj-alpha-001" {
			alpha = &threads[i]
			break
		}
	}
	if alpha == nil {
		t.Fatal("alpha thread not found")
	}
	if alpha.EntryCount != 3 {
		t.Errorf("alpha entry_count: got %d, want 3", alpha.EntryCount)
	}
	if alpha.Subject == "" {
		t.Errorf("alpha thread subject empty (frontmatter subject missing?)")
	}

	refs, err := h.memLayer.EntriesByThread(ctx, alpha.ID, 10)
	if err != nil {
		t.Fatalf("EntriesByThread: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("alpha entries: got %d, want 3", len(refs))
	}
	// Oldest-first reading order.
	for i := 0; i+1 < len(refs); i++ {
		if refs[i].Date.After(refs[i+1].Date) {
			t.Errorf("entries not in reading order: %v then %v",
				refs[i].Date, refs[i+1].Date)
		}
	}

	// --- raw bytes landed in storage ---
	exists, err := h.storage.Exists(ctx, "sources/notes/files/project-alpha/01-kickoff.md")
	if err != nil {
		t.Fatalf("storage.Exists: %v", err)
	}
	if !exists {
		t.Error("expected raw file in storage at sources/notes/files/...")
	}
}

func TestFiles_E2E_IncrementalSync(t *testing.T) {
	h := newHarness(t)

	h.writeFile("a.md", `---
from: alice@example.com
subject: First
thread: t-1
---
body
`)
	h.writeFile("b.md", `---
from: bob@example.com
subject: Second
thread: t-2
---
body
`)
	// First sync.
	r1 := h.sync(nil)
	if r1.RecordsAdded != 2 {
		t.Fatalf("first sync RecordsAdded: %d", r1.RecordsAdded)
	}

	// Re-sync with the cursor — no files changed → zero ingest.
	r2 := h.sync(r1.Cursor)
	if r2.RecordsAdded != 0 || r2.RecordsUpdated != 0 {
		t.Errorf("idempotent re-sync should be 0/0, got %d/%d",
			r2.RecordsAdded, r2.RecordsUpdated)
	}

	// Modify b.md → only it should re-ingest.
	h.writeFile("b.md", `---
from: bob@example.com
subject: Second (edited)
thread: t-2
---
body modified
`)
	r3 := h.sync(r2.Cursor)
	if r3.RecordsUpdated != 1 {
		t.Errorf("after edit, RecordsUpdated: %d, want 1", r3.RecordsUpdated)
	}
	if r3.RecordsAdded != 0 {
		t.Errorf("after edit, RecordsAdded: %d, want 0", r3.RecordsAdded)
	}

	// Delete a.md → should be removed from memory + storage.
	if err := os.Remove(filepath.Join(h.root, "a.md")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	r4 := h.sync(r3.Cursor)
	if r4.RecordsAdded != 0 || r4.RecordsUpdated != 0 {
		t.Errorf("after delete, expected no adds/updates, got %d/%d",
			r4.RecordsAdded, r4.RecordsUpdated)
	}
	ctx := context.Background()
	exists, _ := h.storage.Exists(ctx, "sources/notes/files/a.md")
	if exists {
		t.Error("deleted file's raw bytes should be removed from storage")
	}
}

func TestFiles_E2E_RecursiveOptOut(t *testing.T) {
	h := newHarness(t)
	// Write files at root + in a subdir.
	h.writeFile("top.md", "---\nfrom: x@y\nsubject: top\nthread: t\n---\n")
	h.writeFile("nested/inner.md", "---\nfrom: x@y\nsubject: inner\nthread: t\n---\n")

	// Re-init with recursive=false.
	s := New()
	dir := t.TempDir()
	stor, _ := storage.New("storage:fs-encrypted")
	_ = stor.Init(context.Background(), map[string]any{
		"root": filepath.Join(dir, "storage"),
	})
	if err := s.Init(context.Background(), source.Deps{
		SourceName: "shallow",
		Config: map[string]any{
			"root":      h.root,
			"recursive": false,
		},
		Storage:     stor,
		Credentials: source.NewMemoryCredentialStore(),
		Logger:      silentLogger{},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = stor.Close(context.Background()) }()
	r, err := s.Sync(context.Background(), nil)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if r.RecordsAdded != 1 {
		t.Errorf("recursive=false should only see top.md; got %d records", r.RecordsAdded)
	}
}

func TestFiles_E2E_MaxFilesCap(t *testing.T) {
	h := newHarness(t)
	// Write 15 files; cap to 10.
	for i := 0; i < 15; i++ {
		h.writeFile(fmt.Sprintf("note-%02d.md", i),
			"---\nfrom: x@y\nsubject: s\nthread: t\n---\n")
	}

	s := New()
	dir := t.TempDir()
	stor, _ := storage.New("storage:fs-encrypted")
	_ = stor.Init(context.Background(), map[string]any{
		"root": filepath.Join(dir, "storage"),
	})
	if err := s.Init(context.Background(), source.Deps{
		SourceName: "capped",
		Config: map[string]any{
			"root":      h.root,
			"max_files": 10,
		},
		Storage:     stor,
		Credentials: source.NewMemoryCredentialStore(),
		Logger:      silentLogger{},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = stor.Close(context.Background()) }()
	r, _ := s.Sync(context.Background(), nil)
	if r.RecordsAdded != 10 {
		t.Errorf("max_files=10 cap: got %d adds, want 10", r.RecordsAdded)
	}
}

func TestFiles_HealthCheck(t *testing.T) {
	h := newHarness(t)
	if err := h.src.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck on healthy root: %v", err)
	}

	// Remove the root → health check should fail (but not panic).
	_ = os.RemoveAll(h.root)
	if err := h.src.HealthCheck(context.Background()); err == nil {
		t.Error("HealthCheck on missing root should fail")
	}
}

// --- helpers ---------------------------------------------------------

// stubStorage returns a no-op storage.Adapter for the Init-only tests
// where we don't actually need writes.
type noopStorage struct{}

func (noopStorage) Init(context.Context, map[string]any) error { return nil }
func (noopStorage) Read(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("noop")
}
func (noopStorage) ReadStream(context.Context, string, int64, int64) (io.ReadCloser, error) {
	return nil, fmt.Errorf("noop")
}
func (noopStorage) Write(context.Context, string, []byte) error { return nil }
func (noopStorage) WriteStream(context.Context, string, io.Reader) error {
	return nil
}
func (noopStorage) Delete(context.Context, string) error         { return nil }
func (noopStorage) Exists(context.Context, string) (bool, error) { return false, nil }
func (noopStorage) Metadata(context.Context, string) (storage.ObjectMetadata, error) {
	return storage.ObjectMetadata{}, nil
}
func (noopStorage) List(context.Context, string) (<-chan storage.ListEntry, error) {
	ch := make(chan storage.ListEntry)
	close(ch)
	return ch, nil
}
func (noopStorage) SignedURL(context.Context, string, time.Duration, storage.Op) (string, error) {
	return "", fmt.Errorf("noop")
}
func (noopStorage) HealthCheck(context.Context) error { return nil }
func (noopStorage) Close(context.Context) error       { return nil }

func stubStorage() storage.Adapter { return noopStorage{} }
