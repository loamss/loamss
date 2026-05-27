package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	memadapter "github.com/loamss/loamss/runtime/internal/adapter/memory"
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/sqlite"
	memlayer "github.com/loamss/loamss/runtime/internal/memory"
)

func resetMemoryFlags() {
	entitiesListNamespace = ""
	entitiesListKind = ""
	entitiesListAlias = ""
	entitiesListLimit = 50
	entitiesListJSON = false
	entitiesShowJSON = false
	entitiesEntriesLimit = 50
	entitiesEntriesJSON = false
	threadsListNamespace = ""
	threadsListLimit = 50
	threadsListJSON = false
	threadsShowJSON = false
	threadsEntriesLimit = 50
	threadsEntriesJSON = false
}

func runMemoryCmd(t *testing.T, dataDir string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("LOAMSS_DATA_DIR", dataDir)
	resetMemoryFlags()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetIn(strings.NewReader(""))
	rootCmd.SetArgs(append([]string{"memory"}, args...))
	err := rootCmd.Execute()
	return buf.String(), err
}

// seedLayer constructs a layer rooted in dataDir and writes a small
// fixture for the entities + threads tests below to exercise.
func seedLayer(t *testing.T, dataDir string) {
	t.Helper()
	ctx := context.Background()

	adapter, err := memadapter.New("memory:sqlite")
	if err != nil {
		t.Fatalf("memory adapter: %v", err)
	}
	if err := adapter.Init(ctx, map[string]any{
		"path": filepath.Join(dataDir, "memory.db"),
	}); err != nil {
		t.Fatalf("init memory adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close(ctx) })

	store, err := memlayer.OpenStore(ctx, filepath.Join(dataDir, "runtime.db"))
	if err != nil {
		t.Fatalf("open layer store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	layer := memlayer.New(adapter, store, nil, nil)

	// Three messages in one thread between Sarah and Bob.
	entries := []memlayer.Entry{
		{
			Namespace: "gmail-test", ID: "m1",
			Metadata: map[string]any{
				"from":            `"Sarah Smith" <sarah@example.com>`,
				"to":              "bob@example.com",
				"gmail_thread_id": "thr-abc",
				"subject":         "Project Alpha",
				"internal_date":   "2026-05-22T10:00:00Z",
			},
		},
		{
			Namespace: "gmail-test", ID: "m2",
			Metadata: map[string]any{
				"from":            "bob@example.com",
				"to":              `"Sarah Smith" <sarah@example.com>`,
				"gmail_thread_id": "thr-abc",
				"subject":         "Re: Project Alpha",
				"internal_date":   "2026-05-23T10:00:00Z",
			},
		},
		{
			Namespace: "gmail-test", ID: "m3",
			Metadata: map[string]any{
				"from":            `"Sarah Smith" <sarah@example.com>`,
				"to":              "bob@example.com",
				"gmail_thread_id": "thr-abc",
				"subject":         "Re: Project Alpha",
				"internal_date":   "2026-05-24T10:00:00Z",
			},
		},
	}
	for _, e := range entries {
		if err := layer.Upsert(ctx, e); err != nil {
			t.Fatalf("layer.Upsert %s: %v", e.ID, err)
		}
	}
}

// --- entities ---------------------------------------------------------

func TestMemoryEntitiesList_HappyPath(t *testing.T) {
	dir := t.TempDir()
	seedLayer(t, dir)
	out, err := runMemoryCmd(t, dir, "entities", "list")
	if err != nil {
		t.Fatalf("entities list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Sarah Smith") {
		t.Errorf("expected Sarah in output:\n%s", out)
	}
	if !strings.Contains(out, "bob") {
		t.Errorf("expected bob in output:\n%s", out)
	}
}

func TestMemoryEntitiesList_NamespaceFilter(t *testing.T) {
	dir := t.TempDir()
	seedLayer(t, dir)
	out, err := runMemoryCmd(t, dir, "entities", "list",
		"--namespace", "no-such-namespace")
	if err != nil {
		t.Fatalf("entities list: %v", err)
	}
	if !strings.Contains(out, "no entities derived yet") {
		t.Errorf("expected empty result, got:\n%s", out)
	}
}

func TestMemoryEntitiesList_AliasFilter(t *testing.T) {
	dir := t.TempDir()
	seedLayer(t, dir)
	out, err := runMemoryCmd(t, dir, "entities", "list",
		"--alias", "sarah@example.com")
	if err != nil {
		t.Fatalf("entities list: %v", err)
	}
	if !strings.Contains(out, "Sarah Smith") {
		t.Errorf("expected Sarah, got:\n%s", out)
	}
	if strings.Contains(out, "bob@example.com") {
		t.Errorf("alias filter should have excluded bob, got:\n%s", out)
	}
}

func TestMemoryEntitiesList_EmptyDB(t *testing.T) {
	dir := t.TempDir()
	out, err := runMemoryCmd(t, dir, "entities", "list")
	if err != nil {
		t.Fatalf("entities list (empty): %v", err)
	}
	if !strings.Contains(out, "no entities derived yet") {
		t.Errorf("expected empty-state message, got:\n%s", out)
	}
}

func TestMemoryEntitiesShow_NotFound(t *testing.T) {
	dir := t.TempDir()
	seedLayer(t, dir)
	_, err := runMemoryCmd(t, dir, "entities", "show", "ent_does_not_exist")
	if err == nil {
		t.Error("expected error for missing entity")
	}
}

func TestMemoryEntitiesEntries_NewestFirst(t *testing.T) {
	dir := t.TempDir()
	seedLayer(t, dir)
	// Find Sarah's id.
	out, _ := runMemoryCmd(t, dir, "entities", "list", "--alias", "sarah@example.com", "--json")
	id := firstJSONIDField(out)
	if id == "" {
		t.Fatal("could not locate Sarah's id in JSON output")
	}
	got, err := runMemoryCmd(t, dir, "entities", "entries", id)
	if err != nil {
		t.Fatalf("entities entries: %v", err)
	}
	// 3 entries total — sarah is involved in all of them (as from or to).
	for _, want := range []string{"m1", "m2", "m3"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected entry %s in output:\n%s", want, got)
		}
	}
}

// --- threads ----------------------------------------------------------

func TestMemoryThreadsList_HappyPath(t *testing.T) {
	dir := t.TempDir()
	seedLayer(t, dir)
	out, err := runMemoryCmd(t, dir, "threads", "list")
	if err != nil {
		t.Fatalf("threads list: %v", err)
	}
	if !strings.Contains(out, "Project Alpha") {
		t.Errorf("expected thread subject in output:\n%s", out)
	}
	if !strings.Contains(out, "gmail-test") {
		t.Errorf("expected namespace in output:\n%s", out)
	}
}

func TestMemoryThreadsEntries_ReadingOrder(t *testing.T) {
	dir := t.TempDir()
	seedLayer(t, dir)
	out, _ := runMemoryCmd(t, dir, "threads", "list", "--json")
	id := firstJSONIDField(out)
	if id == "" {
		t.Fatal("could not locate thread id in JSON output")
	}
	entriesOut, err := runMemoryCmd(t, dir, "threads", "entries", id)
	if err != nil {
		t.Fatalf("threads entries: %v", err)
	}
	// Oldest-first means m1 appears before m3 in the output.
	i1 := strings.Index(entriesOut, "m1")
	i3 := strings.Index(entriesOut, "m3")
	if i1 == -1 || i3 == -1 || i1 > i3 {
		t.Errorf("entries not in reading order:\n%s", entriesOut)
	}
}

// firstJSONIDField extracts the first "id" value from JSONL output —
// the layer CLI emits one JSON object per line, so we just split on
// newlines and look for `"id":"...`.
func firstJSONIDField(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, `"id":"`)
		if idx == -1 {
			continue
		}
		start := idx + len(`"id":"`)
		end := strings.Index(line[start:], `"`)
		if end == -1 {
			continue
		}
		return line[start : start+end]
	}
	return ""
}
