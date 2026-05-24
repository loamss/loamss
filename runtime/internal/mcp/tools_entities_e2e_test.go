// tools_entities_e2e_test.go drives the entities.* + threads.* MCP
// tool handlers against a real memory layer with real sqlite adapter
// and a realistic Gmail-shaped fixture. The handlers run the same
// path the runtime exposes over MCP, minus the permission-check
// envelope (we test that separately in handler_test.go).

package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	memadapter "github.com/loamss/loamss/runtime/internal/adapter/memory"
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/sqlite"
	memlayer "github.com/loamss/loamss/runtime/internal/memory"
)

// e2eHarness builds a real adapter + layer + fixture and returns
// the layer so individual tests can call tools against it.
func e2eHarness(t *testing.T) memlayer.Layer {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	adapter, err := memadapter.New("memory:sqlite")
	if err != nil {
		t.Fatalf("memory adapter: %v", err)
	}
	if err := adapter.Init(ctx, map[string]any{
		"path": filepath.Join(dir, "memory.db"),
	}); err != nil {
		t.Fatalf("init adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close(ctx) })

	store, err := memlayer.OpenStore(ctx, filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("layer store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	layer := memlayer.New(adapter, store, nil)

	// Seed a tiny fixture: one Gmail thread, three messages between
	// Sarah and Bob. Embeddings are simple character-frequency
	// vectors — enough for the cosine distance metric to behave.
	fixture := []struct {
		id, thread, subject, from, to, date string
	}{
		{"m1", "thr-1", "Project Alpha", `"Sarah" <sarah@x.com>`, `"Bob" <bob@x.com>`, "2026-05-22T10:00:00Z"},
		{"m2", "thr-1", "Re: Project Alpha", `"Bob" <bob@x.com>`, `"Sarah" <sarah@x.com>`, "2026-05-23T10:00:00Z"},
		{"m3", "thr-1", "Re: Project Alpha", `"Sarah" <sarah@x.com>`, `"Bob" <bob@x.com>`, "2026-05-24T10:00:00Z"},
	}
	for _, m := range fixture {
		if err := layer.Upsert(ctx, memlayer.Entry{
			Namespace:  "gmail-e2e",
			ID:         m.id,
			Embeddings: makeFakeVector(m.subject),
			Metadata: map[string]any{
				"from": m.from, "to": m.to,
				"subject":         m.subject,
				"gmail_thread_id": m.thread,
				"internal_date":   m.date,
			},
		}); err != nil {
			t.Fatalf("Upsert %s: %v", m.id, err)
		}
	}
	return layer
}

// makeFakeVector returns a deterministic 8-dim vector based on the
// length of the input text. Not realistic but stable; the e2e tests
// here don't check ranking, just that the adapter accepts the vector.
func makeFakeVector(s string) []float32 {
	v := []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}
	// L2-normalize.
	var norm float32
	for _, x := range v {
		norm += x * x
	}
	if norm == 0 {
		return v
	}
	// Modulate by length so different texts get different vectors.
	for i := range v {
		v[i] *= float32(len(s)) / 100
	}
	return v
}

func runToolHandler(t *testing.T, tool Tool, argsJSON string) ToolResult {
	t.Helper()
	res, err := tool.Handler(context.Background(), ToolInput{
		Args: json.RawMessage(argsJSON),
	})
	if err != nil {
		t.Fatalf("tool %s handler: %v", tool.Name, err)
	}
	return res
}

func firstTextContent(t *testing.T, res ToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("tool result has no content blocks")
	}
	if res.Content[0].Type != "text" {
		t.Fatalf("first content block is %q, want text", res.Content[0].Type)
	}
	return res.Content[0].Text
}

// --- entities.list ----------------------------------------------------

func TestE2E_EntitiesListTool(t *testing.T) {
	layer := e2eHarness(t)
	tool := NewEntitiesListTool(layer)
	res := runToolHandler(t, tool, `{"namespace":"gmail-e2e"}`)
	text := firstTextContent(t, res)

	var decoded struct {
		Count    int               `json:"count"`
		Entities []memlayer.Entity `json:"entities"`
	}
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("decode tool output: %v\n%s", err, text)
	}
	if decoded.Count != 2 {
		t.Errorf("entities count: got %d, want 2 (Sarah + Bob)", decoded.Count)
	}
	names := []string{}
	for _, e := range decoded.Entities {
		names = append(names, e.Canonical)
	}
	for _, want := range []string{"Sarah", "Bob"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing entity %q in output: %v", want, names)
		}
	}
}

func TestE2E_EntitiesListTool_AliasFilter(t *testing.T) {
	layer := e2eHarness(t)
	tool := NewEntitiesListTool(layer)
	res := runToolHandler(t, tool,
		`{"namespace":"gmail-e2e","alias":"sarah@x.com"}`)
	text := firstTextContent(t, res)

	if !strings.Contains(text, "Sarah") {
		t.Errorf("expected Sarah in output: %s", text)
	}
	if strings.Contains(text, "bob@x.com") {
		t.Errorf("alias filter should have excluded Bob: %s", text)
	}
}

// --- entities.show / entities.entries --------------------------------

func TestE2E_EntitiesShowTool(t *testing.T) {
	layer := e2eHarness(t)
	// Find Sarah's id via list first.
	list := runToolHandler(t, NewEntitiesListTool(layer),
		`{"namespace":"gmail-e2e","alias":"sarah@x.com"}`)
	var listed struct {
		Entities []memlayer.Entity `json:"entities"`
	}
	_ = json.Unmarshal([]byte(firstTextContent(t, list)), &listed)
	if len(listed.Entities) == 0 {
		t.Fatal("Sarah not found in list")
	}
	sarahID := listed.Entities[0].ID

	res := runToolHandler(t, NewEntitiesShowTool(layer),
		`{"id":"`+sarahID+`"}`)
	text := firstTextContent(t, res)
	// JSONContent pretty-prints; tolerate whitespace by parsing.
	var shown memlayer.Entity
	if err := json.Unmarshal([]byte(text), &shown); err != nil {
		t.Fatalf("decode show output: %v\n%s", err, text)
	}
	if shown.Canonical != "Sarah" {
		t.Errorf("canonical: got %q, want Sarah", shown.Canonical)
	}
	if shown.EntryCount != 3 {
		t.Errorf("entry_count: got %d, want 3", shown.EntryCount)
	}
}

func TestE2E_EntitiesShowTool_NotFound(t *testing.T) {
	layer := e2eHarness(t)
	res := runToolHandler(t, NewEntitiesShowTool(layer),
		`{"id":"ent_does_not_exist"}`)
	if !res.IsError {
		t.Errorf("expected isError=true on missing entity")
	}
}

func TestE2E_EntitiesEntriesTool(t *testing.T) {
	layer := e2eHarness(t)
	list := runToolHandler(t, NewEntitiesListTool(layer),
		`{"namespace":"gmail-e2e","alias":"sarah@x.com"}`)
	var listed struct {
		Entities []memlayer.Entity `json:"entities"`
	}
	_ = json.Unmarshal([]byte(firstTextContent(t, list)), &listed)
	sarahID := listed.Entities[0].ID

	res := runToolHandler(t, NewEntitiesEntriesTool(layer),
		`{"id":"`+sarahID+`"}`)
	text := firstTextContent(t, res)

	var out struct {
		Count   int                 `json:"count"`
		Entries []memlayer.EntryRef `json:"entries"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Count != 3 {
		t.Errorf("count: got %d, want 3", out.Count)
	}
	// Newest first.
	if !out.Entries[0].Date.After(out.Entries[len(out.Entries)-1].Date) {
		t.Errorf("entries not newest-first: %v then %v",
			out.Entries[0].Date, out.Entries[len(out.Entries)-1].Date)
	}
}

// --- threads.list / threads.entries -----------------------------------

func TestE2E_ThreadsListTool(t *testing.T) {
	layer := e2eHarness(t)
	res := runToolHandler(t, NewThreadsListTool(layer),
		`{"namespace":"gmail-e2e"}`)
	text := firstTextContent(t, res)

	var out struct {
		Count   int               `json:"count"`
		Threads []memlayer.Thread `json:"threads"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Count != 1 {
		t.Errorf("expected 1 thread, got %d", out.Count)
	}
	if out.Threads[0].ExternalID != "thr-1" {
		t.Errorf("external_id: %q", out.Threads[0].ExternalID)
	}
	if out.Threads[0].EntryCount != 3 {
		t.Errorf("entry_count: %d", out.Threads[0].EntryCount)
	}
}

func TestE2E_ThreadsEntriesTool_ReadingOrder(t *testing.T) {
	layer := e2eHarness(t)
	// Find the thread id.
	list := runToolHandler(t, NewThreadsListTool(layer),
		`{"namespace":"gmail-e2e"}`)
	var listed struct {
		Threads []memlayer.Thread `json:"threads"`
	}
	_ = json.Unmarshal([]byte(firstTextContent(t, list)), &listed)
	if len(listed.Threads) == 0 {
		t.Fatal("no thread in list")
	}
	threadID := listed.Threads[0].ID

	res := runToolHandler(t, NewThreadsEntriesTool(layer),
		`{"id":"`+threadID+`"}`)
	text := firstTextContent(t, res)

	var out struct {
		Entries []memlayer.EntryRef `json:"entries"`
	}
	_ = json.Unmarshal([]byte(text), &out)
	if len(out.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(out.Entries))
	}
	// Oldest first (reading order).
	if !out.Entries[0].Date.Before(out.Entries[len(out.Entries)-1].Date) {
		t.Errorf("entries not oldest-first: %v then %v",
			out.Entries[0].Date, out.Entries[len(out.Entries)-1].Date)
	}
	// IDs should be m1, m2, m3 in that order.
	wantIDs := []string{"m1", "m2", "m3"}
	for i, want := range wantIDs {
		if out.Entries[i].ID != want {
			t.Errorf("entry[%d]: got %q, want %q", i, out.Entries[i].ID, want)
		}
	}
}

func TestE2E_ThreadsShowTool_NotFound(t *testing.T) {
	layer := e2eHarness(t)
	res := runToolHandler(t, NewThreadsShowTool(layer),
		`{"id":"thr_does_not_exist"}`)
	if !res.IsError {
		t.Errorf("expected isError=true on missing thread")
	}
}
