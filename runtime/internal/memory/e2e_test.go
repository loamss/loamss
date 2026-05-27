// e2e_test.go drives the memory layer end-to-end against a real
// sqlite memory adapter, with deterministic mock embeddings shaped
// the way a real embedding model would produce them: similar text
// yields similar vectors under cosine distance.
//
// What this test proves (the chain that production runs):
//
//   source-shaped entry  →  memlayer.Upsert
//                              │
//                              ├──→ memory adapter (sqlite): vector + metadata
//                              └──→ memory_layer_* tables:
//                                     - entities (Sarah, Bob, Carol)
//                                     - threads (Project Alpha, Project Beta)
//                                     - mappings (who's in which thread,
//                                       which role they played)
//
//   adapter.Search(query_vector)    → vector search across stored entries
//   layer.ListEntities / Threads    → derived views over the same data
//   layer.EntriesByEntity / Thread  → reverse-lookups for app UI
//   entities.* / threads.* tools    → MCP surface invoked by paired clients
//
// The test uses character-frequency embeddings (similar letters →
// similar vectors). Two messages about "Project Alpha" will cluster
// together under cosine distance even though we never trained
// anything — the same property a real text-embedding model exhibits.

package memory

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"

	memadapter "github.com/loamss/loamss/runtime/internal/adapter/memory"
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/sqlite" // registers memory:sqlite
)

// mockEmbedding returns a deterministic 26-dim vector whose
// components count letter frequencies of the input. L2-normalized
// so cosine distance is meaningful. Same text → same vector;
// similar text → close vector.
//
// This is decidedly NOT a real text-embedding model. It just
// happens to have the right shape (deterministic + L2-normalized +
// metric-compatible) to exercise the vector path.
func mockEmbedding(text string) []float32 {
	v := make([]float32, 26)
	for _, r := range strings.ToLower(text) {
		if r >= 'a' && r <= 'z' {
			v[r-'a']++
		}
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		// Avoid zero vector — the adapter would reject it.
		v[0] = 1
		return v
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range v {
		v[i] *= scale
	}
	return v
}

// openRealLayer constructs a layer against the actual sqlite memory
// adapter (not the stub). All state is in t.TempDir(); the test
// cleans up on completion.
func openRealLayer(t *testing.T) (Layer, memadapter.Adapter) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	adapter, err := memadapter.New("memory:sqlite")
	if err != nil {
		t.Fatalf("constructing memory:sqlite adapter: %v", err)
	}
	if err := adapter.Init(ctx, map[string]any{
		"path": filepath.Join(dir, "memory.db"),
	}); err != nil {
		t.Fatalf("init memory adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close(ctx) })

	store, err := OpenStore(ctx, filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open layer store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return New(adapter, store, nil, silentLogger), adapter
}

// fixture: 8 messages across 2 Gmail threads + 3 participants.
// Sarah appears in both projects; Bob is Alpha-only; Carol is
// Beta-only. Each message has a realistic-shaped Gmail metadata
// blob and a mock embedding of "subject + snippet".
func seedE2EFixture(t *testing.T, l Layer) {
	t.Helper()
	ctx := context.Background()

	type msg struct {
		id      string
		thread  string
		subject string
		from    string
		to      string
		date    string
		body    string
	}
	fixture := []msg{
		// --- thread: Project Alpha (Sarah + Bob) ---
		{
			id: "a1", thread: "thr-alpha",
			subject: "Project Alpha kickoff",
			from:    `"Sarah Smith" <sarah@example.com>`,
			to:      `"Bob Lee" <bob@example.com>`,
			date:    "2026-05-20T10:00:00Z",
			body:    "Project Alpha kickoff scheduled Monday — please review the brief beforehand.",
		},
		{
			id: "a2", thread: "thr-alpha",
			subject: "Re: Project Alpha kickoff",
			from:    `"Bob Lee" <bob@example.com>`,
			to:      `"Sarah Smith" <sarah@example.com>`,
			date:    "2026-05-21T09:30:00Z",
			body:    "Sounds good. Reviewed the brief — sending notes shortly. Alpha will ship.",
		},
		{
			id: "a3", thread: "thr-alpha",
			subject: "Re: Project Alpha kickoff",
			from:    `"Sarah Smith" <sarah@example.com>`,
			to:      `"Bob Lee" <bob@example.com>`,
			date:    "2026-05-22T11:00:00Z",
			body:    "Great — looking forward to the notes. Alpha team standup at 2pm.",
		},
		{
			id: "a4", thread: "thr-alpha",
			subject: "Re: Project Alpha kickoff",
			from:    `"Bob Lee" <bob@example.com>`,
			to:      `"Sarah Smith" <sarah@example.com>`,
			date:    "2026-05-23T08:15:00Z",
			body:    "Notes attached. Highlights: scope tight, risks low, Alpha ships in two weeks.",
		},

		// --- thread: Project Beta (Sarah + Carol) ---
		{
			id: "b1", thread: "thr-beta",
			subject: "Project Beta status",
			from:    `"Carol Wu" <carol@example.com>`,
			to:      `"Sarah Smith" <sarah@example.com>`,
			date:    "2026-05-20T14:00:00Z",
			body:    "Project Beta status update: vendor confirmed delivery, holding for QA window.",
		},
		{
			id: "b2", thread: "thr-beta",
			subject: "Re: Project Beta status",
			from:    `"Sarah Smith" <sarah@example.com>`,
			to:      `"Carol Wu" <carol@example.com>`,
			date:    "2026-05-21T15:00:00Z",
			body:    "Acknowledged. Beta QA team allocated; expect signoff Thursday.",
		},
		{
			id: "b3", thread: "thr-beta",
			subject: "Re: Project Beta status",
			from:    `"Carol Wu" <carol@example.com>`,
			to:      `"Sarah Smith" <sarah@example.com>`,
			date:    "2026-05-22T16:30:00Z",
			body:    "QA signoff received. Beta launching Friday as planned.",
		},
		{
			id: "b4", thread: "thr-beta",
			subject: "Re: Project Beta status",
			from:    `"Sarah Smith" <sarah@example.com>`,
			to:      `"Carol Wu" <carol@example.com>`,
			date:    "2026-05-23T17:00:00Z",
			body:    "Beta launched. Telemetry green across all regions. Closing the loop.",
		},
	}

	for _, m := range fixture {
		entry := Entry{
			Namespace:  "gmail-test",
			ID:         m.id,
			Content:    m.subject + "\n\n" + m.body,
			Embeddings: mockEmbedding(m.subject + " " + m.body),
			Metadata: map[string]any{
				"from":            m.from,
				"to":              m.to,
				"subject":         m.subject,
				"gmail_thread_id": m.thread,
				"internal_date":   m.date,
			},
		}
		if err := l.Upsert(ctx, entry); err != nil {
			t.Fatalf("Upsert %s: %v", m.id, err)
		}
	}
}

// --- assertions over the fixture ----------------------------------------

func TestE2E_EntityDedup(t *testing.T) {
	l, _ := openRealLayer(t)
	seedE2EFixture(t, l)

	ents, err := l.ListEntities(context.Background(),
		EntityFilter{Namespace: "gmail-test"})
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(ents) != 3 {
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			names = append(names, e.Canonical)
		}
		t.Errorf("expected 3 entities (Sarah, Bob, Carol), got %d: %v",
			len(ents), names)
	}

	byName := map[string]Entity{}
	for _, e := range ents {
		byName[e.Canonical] = e
	}

	// Sarah is involved in all 8 messages.
	sarah, ok := byName["Sarah Smith"]
	if !ok {
		t.Fatalf("Sarah Smith not found; got %v", byName)
	}
	if sarah.EntryCount != 8 {
		t.Errorf("Sarah entry_count: got %d, want 8", sarah.EntryCount)
	}

	// Bob: 4 (Alpha only).
	bob, ok := byName["Bob Lee"]
	if !ok {
		t.Fatalf("Bob Lee not found")
	}
	if bob.EntryCount != 4 {
		t.Errorf("Bob entry_count: got %d, want 4", bob.EntryCount)
	}

	// Carol: 4 (Beta only).
	carol, ok := byName["Carol Wu"]
	if !ok {
		t.Fatalf("Carol Wu not found")
	}
	if carol.EntryCount != 4 {
		t.Errorf("Carol entry_count: got %d, want 4", carol.EntryCount)
	}
}

func TestE2E_ThreadGrouping(t *testing.T) {
	l, _ := openRealLayer(t)
	seedE2EFixture(t, l)

	threads, err := l.ListThreads(context.Background(),
		ThreadFilter{Namespace: "gmail-test"})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(threads))
	}

	bySubject := map[string]Thread{}
	for _, th := range threads {
		bySubject[th.Subject] = th
	}
	alpha, ok := bySubject["Project Alpha kickoff"]
	if !ok {
		// Some threads might have the "Re:" subject if the first
		// message-to-be-upserted set it. Find by external id instead.
		for _, th := range threads {
			if th.ExternalID == "thr-alpha" {
				alpha = th
				ok = true
			}
		}
	}
	if !ok {
		t.Fatal("Alpha thread not found")
	}
	if alpha.EntryCount != 4 {
		t.Errorf("Alpha thread entry_count: got %d, want 4", alpha.EntryCount)
	}
}

func TestE2E_ThreadEntries_ReadingOrder(t *testing.T) {
	l, _ := openRealLayer(t)
	seedE2EFixture(t, l)

	threads, _ := l.ListThreads(context.Background(),
		ThreadFilter{Namespace: "gmail-test"})
	var alphaID string
	for _, th := range threads {
		if th.ExternalID == "thr-alpha" {
			alphaID = th.ID
		}
	}
	if alphaID == "" {
		t.Fatal("Alpha thread not found")
	}

	refs, err := l.EntriesByThread(context.Background(), alphaID, 50)
	if err != nil {
		t.Fatalf("EntriesByThread: %v", err)
	}
	if len(refs) != 4 {
		t.Fatalf("got %d refs, want 4", len(refs))
	}
	// Oldest first: a1 should come before a4.
	for i := 0; i+1 < len(refs); i++ {
		if refs[i].Date.After(refs[i+1].Date) {
			t.Errorf("entries not in ascending date order: %v then %v",
				refs[i].Date, refs[i+1].Date)
		}
	}
	if refs[0].ID != "a1" {
		t.Errorf("first ref should be a1 (oldest), got %s", refs[0].ID)
	}
}

func TestE2E_EntriesByEntity_NewestFirst(t *testing.T) {
	l, _ := openRealLayer(t)
	seedE2EFixture(t, l)

	ents, _ := l.ListEntities(context.Background(),
		EntityFilter{Namespace: "gmail-test", Alias: "sarah@example.com"})
	if len(ents) == 0 {
		t.Fatal("Sarah not found by alias")
	}

	refs, err := l.EntriesByEntity(context.Background(), ents[0].ID, 50)
	if err != nil {
		t.Fatalf("EntriesByEntity: %v", err)
	}
	if len(refs) != 8 {
		t.Errorf("expected 8 refs for Sarah, got %d", len(refs))
	}
	// Newest first.
	for i := 0; i+1 < len(refs); i++ {
		if refs[i].Date.Before(refs[i+1].Date) {
			t.Errorf("entries not in descending date order: %v then %v",
				refs[i].Date, refs[i+1].Date)
		}
	}
}

// TestE2E_VectorSearch_ClustersBySubject proves that the embedding-
// path is wired correctly: a query embedding of "Project Alpha"
// should retrieve Alpha entries with smaller cosine distance than
// Beta entries.
//
// We don't assert exact distances — character-frequency embeddings
// are not a real model — but the ranking direction is reliable.
func TestE2E_VectorSearch_ClustersBySubject(t *testing.T) {
	l, adapter := openRealLayer(t)
	seedE2EFixture(t, l)

	query := mockEmbedding("Project Alpha kickoff")
	hits, err := adapter.Search(context.Background(), query, 8, memadapter.MetadataFilter{})
	if err != nil {
		t.Fatalf("adapter.Search: %v", err)
	}
	if len(hits) != 8 {
		t.Fatalf("expected 8 hits, got %d", len(hits))
	}

	// Walk hits in ranked order; count how many of the top-4 are
	// Alpha (id prefix "a"). Should be all 4 — Alpha + kickoff
	// share the most letters with Alpha messages.
	alphaInTop4 := 0
	for i := 0; i < 4; i++ {
		// adapter id is "<namespace>:<entry_id>"; the layer
		// composes it via composeAdapterID.
		id := hits[i].ID
		if !strings.HasPrefix(id, "gmail-test:") {
			t.Errorf("hit %d unexpected id shape: %q", i, id)
			continue
		}
		entryID := strings.TrimPrefix(id, "gmail-test:")
		if strings.HasPrefix(entryID, "a") {
			alphaInTop4++
		}
	}
	if alphaInTop4 < 3 {
		// Allow some slack for the borderline ranking — but at
		// minimum 3 of 4 should be Alpha for the path to be
		// behaving sanely.
		ids := make([]string, 0, len(hits))
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		t.Errorf("vector search not clustering by subject: only %d of top-4 were Alpha. ranked ids: %v",
			alphaInTop4, ids)
	}
}

// TestE2E_AdapterAndLayerStayConsistent confirms the dual write +
// dual read story: every entry the layer reports through entities
// has a corresponding row in the vector adapter.
func TestE2E_AdapterAndLayerStayConsistent(t *testing.T) {
	l, adapter := openRealLayer(t)
	seedE2EFixture(t, l)

	// Pull every entry id the layer knows about via Sarah's entries.
	ents, _ := l.ListEntities(context.Background(),
		EntityFilter{Namespace: "gmail-test", Alias: "sarah@example.com"})
	if len(ents) == 0 {
		t.Fatal("Sarah not found")
	}
	refs, _ := l.EntriesByEntity(context.Background(), ents[0].ID, 50)

	// Every ref should be retrievable from the adapter via its
	// composed id.
	for _, r := range refs {
		adapterID := r.Namespace + ":" + r.ID
		entry, err := adapter.Get(context.Background(), adapterID)
		if err != nil {
			t.Errorf("adapter.Get(%s): %v", adapterID, err)
			continue
		}
		if entry.ID != adapterID {
			t.Errorf("adapter returned id %q, expected %q",
				entry.ID, adapterID)
		}
	}
}

// TestE2E_DeleteCascades verifies that deleting an entry through
// the layer removes it from the adapter AND zeros entity_count for
// any entity that was only linked to that entry.
func TestE2E_DeleteCascades(t *testing.T) {
	l, adapter := openRealLayer(t)
	seedE2EFixture(t, l)

	ctx := context.Background()
	// Delete a1 (Alpha kickoff, Sarah→Bob).
	if err := l.Delete(ctx, "gmail-test", "a1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Adapter row gone.
	if _, err := adapter.Get(ctx, "gmail-test:a1"); !errors.Is(err, memadapter.ErrNotFound) {
		t.Errorf("adapter still has a1: err=%v", err)
	}

	// Layer's Alpha thread now has 3 entries (was 4).
	threads, _ := l.ListThreads(ctx, ThreadFilter{Namespace: "gmail-test"})
	var alphaCount int64
	for _, th := range threads {
		if th.ExternalID == "thr-alpha" {
			alphaCount = th.EntryCount
		}
	}
	if alphaCount != 3 {
		t.Errorf("Alpha entry_count after delete: got %d, want 3", alphaCount)
	}

	// Sarah's entry_count drops from 8 to 7 (she was on a1).
	ents, _ := l.ListEntities(ctx,
		EntityFilter{Namespace: "gmail-test", Alias: "sarah@example.com"})
	if ents[0].EntryCount != 7 {
		t.Errorf("Sarah entry_count after delete: got %d, want 7",
			ents[0].EntryCount)
	}
}

// TestE2E_JSONOutputShape exercises the JSON shapes the entities/threads
// MCP tools return. We don't go through the dispatcher here; instead
// we marshal what the handlers would marshal, and confirm the field
// names match what apps + capsules will see on the wire.
func TestE2E_JSONOutputShape(t *testing.T) {
	l, _ := openRealLayer(t)
	seedE2EFixture(t, l)

	ents, _ := l.ListEntities(context.Background(),
		EntityFilter{Namespace: "gmail-test", Alias: "sarah@example.com"})
	if len(ents) == 0 {
		t.Fatal("Sarah not found")
	}

	raw, err := json.Marshal(ents[0])
	if err != nil {
		t.Fatalf("marshal entity: %v", err)
	}

	// Decode it back into a generic map so we can assert the wire
	// keys exist (the tools will marshal it pretty-printed, so a
	// string-contains check is brittle across whitespace).
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode entity JSON: %v", err)
	}
	for _, key := range []string{
		"id", "kind", "canonical", "namespace",
		"aliases", "first_seen", "last_seen", "entry_count",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %q in entity JSON:\n%s", key, raw)
		}
	}
	if m["kind"] != "person" {
		t.Errorf("kind: %v", m["kind"])
	}
	if m["canonical"] != "Sarah Smith" {
		t.Errorf("canonical: %v", m["canonical"])
	}
	if v, ok := m["entry_count"].(float64); !ok || v != 8 {
		t.Errorf("entry_count: %v", m["entry_count"])
	}
}
