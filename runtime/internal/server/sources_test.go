package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	memadapter "github.com/loamss/loamss/runtime/internal/adapter/memory"
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/sqlite"
	"github.com/loamss/loamss/runtime/internal/adapter/storage"
	_ "github.com/loamss/loamss/runtime/internal/adapter/storage/fsencrypted"
	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/mcp"
	memlayer "github.com/loamss/loamss/runtime/internal/memory"
	"github.com/loamss/loamss/runtime/internal/permission"
	"github.com/loamss/loamss/runtime/internal/source"
	_ "github.com/loamss/loamss/runtime/internal/source/files"
)

// Tests for the /console/sources CRUD endpoints. These stand up a
// real server with real subsystems (storage, memory, memlayer)
// against a t.TempDir so we exercise the same code path the
// production daemon uses.

type sourcesServer struct {
	srv     *httptest.Server
	dir     string
	memdir  string // dir we feed to source:files as a known-good root
	cleanup func()
}

func newSourcesServer(t *testing.T) *sourcesServer {
	t.Helper()
	dir := t.TempDir()
	memdir := t.TempDir()
	ctx := context.Background()

	// Permission + audit
	permStore, err := permission.Open(ctx, filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("permission.Open: %v", err)
	}
	w, err := audit.OpenSQLite(ctx, filepath.Join(dir, "audit.db"))
	if err != nil {
		_ = permStore.Close()
		t.Fatalf("audit.OpenSQLite: %v", err)
	}

	// Source store
	srcStore, err := source.OpenStore(ctx, filepath.Join(dir, "runtime.db"))
	if err != nil {
		_ = permStore.Close()
		_ = w.Close(ctx)
		t.Fatalf("source.OpenStore: %v", err)
	}

	// Capsule store (not needed by /console/sources but required
	// when wiring the full server with engine)
	capStore, err := capsule.OpenStore(ctx, filepath.Join(dir, "runtime.db"))
	if err != nil {
		_ = permStore.Close()
		_ = w.Close(ctx)
		_ = srcStore.Close()
		t.Fatalf("capsule.OpenStore: %v", err)
	}

	// Storage adapter — fs-encrypted with a temp root.
	storageRoot := filepath.Join(dir, "storage")
	if err := os.MkdirAll(storageRoot, 0o700); err != nil {
		t.Fatalf("mkdir storage: %v", err)
	}
	sa, err := storage.New("storage:fs-encrypted")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	if err := sa.Init(ctx, map[string]any{"root": storageRoot, "encrypt": false}); err != nil {
		t.Fatalf("storage Init: %v", err)
	}

	// Memory adapter + layer
	ma, err := memadapter.New("memory:sqlite")
	if err != nil {
		t.Fatalf("memadapter.New: %v", err)
	}
	if err := ma.Init(ctx, map[string]any{"path": filepath.Join(dir, "memory.db")}); err != nil {
		t.Fatalf("memadapter Init: %v", err)
	}
	memStore, err := memlayer.OpenStore(ctx, filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("memlayer.OpenStore: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	layer := memlayer.New(ma, memStore, logger)

	engine := permission.NewEngine(permStore, w)

	cfg := config.Default()
	cfg.Runtime.DataDir = dir

	srv := New(Options{
		Addr:       "127.0.0.1:0",
		Logger:     logger,
		Version:    "test-v",
		BaseConfig: cfg,
		Engine:     engine,
		Audit:      w,
		Tools:      mcp.NewRegistry(),
		Resources:  mcp.NewResourceRegistry(),
		Sources:    srcStore,
		Capsules:   capStore,
		SourceBuildEnv: &source.BuildEnv{
			Storage: sa,
			Memory:  layerBridge{layer: layer},
			Logger:  slogShim{logger},
		},
	})
	ts := httptest.NewServer(srv.httpSrv.Handler)

	cleanup := func() {
		ts.Close()
		_ = layer.Close()
		_ = ma.Close(context.Background())
		_ = sa.Close(context.Background())
		_ = capStore.Close()
		_ = srcStore.Close()
		_ = w.Close(context.Background())
		_ = permStore.Close()
	}
	t.Cleanup(cleanup)

	return &sourcesServer{srv: ts, dir: dir, memdir: memdir, cleanup: cleanup}
}

// layerBridge is the test-local copy of the CLI's memoryBridge —
// the production daemon uses an identical bridge in start.go. The
// types and shapes are stable enough that duplicating one bridge
// here (rather than exporting it from CLI / memlayer) keeps the
// test self-contained.
type layerBridge struct{ layer memlayer.Layer }

func (b layerBridge) Upsert(ctx context.Context, entry source.MemoryEntry) error {
	return b.layer.Upsert(ctx, memlayer.Entry{
		Namespace:  entry.Namespace,
		ID:         entry.ID,
		Content:    entry.Content,
		Metadata:   entry.Metadata,
		Embeddings: entry.Embeddings,
	})
}

func (b layerBridge) Delete(ctx context.Context, namespace, id string) error {
	return b.layer.Delete(ctx, namespace, id)
}

type slogShim struct{ l *slog.Logger }

func (s slogShim) Info(msg string, args ...any)  { s.l.Info(msg, args...) }
func (s slogShim) Warn(msg string, args ...any)  { s.l.Warn(msg, args...) }
func (s slogShim) Error(msg string, args ...any) { s.l.Error(msg, args...) }
func (s slogShim) Debug(msg string, args ...any) { s.l.Debug(msg, args...) }

// --- tests ---

func TestSources_AddListSyncDelete(t *testing.T) {
	s := newSourcesServer(t)

	// Drop a file so source:files has something to ingest.
	if err := os.WriteFile(filepath.Join(s.memdir, "hello.md"), []byte("# hello\nworld\n"), 0o600); err != nil {
		t.Fatalf("write hello.md: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"adapter": "source:files",
		"name":    "docs",
		"config":  map[string]any{"root": s.memdir},
	})
	resp, err := http.Post(s.srv.URL+"/console/sources", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST add: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("add status %d: %s", resp.StatusCode, raw)
	}
	_ = resp.Body.Close()

	// /console/state should now list it.
	state := getStateResponse(t, s.srv.URL)
	if len(state.Sources.Items) != 1 || state.Sources.Items[0].Name != "docs" {
		t.Fatalf("expected one source 'docs', got %+v", state.Sources)
	}

	// Trigger sync (async).
	resp2, err := http.Post(s.srv.URL+"/console/sources/docs/sync", "application/json", nil)
	if err != nil {
		t.Fatalf("POST sync: %v", err)
	}
	if resp2.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp2.Body)
		t.Fatalf("sync status %d: %s", resp2.StatusCode, raw)
	}
	_ = resp2.Body.Close()

	// Poll /console/state until the sync settles. Real syncs are
	// fast (single file, fs-encrypted local); 2s timeout is plenty.
	deadline := time.Now().Add(2 * time.Second)
	var finalStatus string
	for time.Now().Before(deadline) {
		st := getStateResponse(t, s.srv.URL)
		if len(st.Sources.Items) == 1 {
			finalStatus = st.Sources.Items[0].LastSyncStatus
			if finalStatus == "success" || finalStatus == "error" {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if finalStatus != "success" {
		t.Errorf("sync did not settle to success in 2s; final status = %q", finalStatus)
	}

	// Delete.
	req, _ := http.NewRequest(http.MethodDelete, s.srv.URL+"/console/sources/docs", nil)
	resp3, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if resp3.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp3.Body)
		t.Fatalf("delete status %d: %s", resp3.StatusCode, raw)
	}
	_ = resp3.Body.Close()

	state = getStateResponse(t, s.srv.URL)
	if len(state.Sources.Items) != 0 {
		t.Errorf("source should be gone after DELETE, got %+v", state.Sources.Items)
	}
}

func TestSources_AddRejectsDuplicate(t *testing.T) {
	s := newSourcesServer(t)
	body, _ := json.Marshal(map[string]any{
		"adapter": "source:files",
		"name":    "dupes",
		"config":  map[string]any{"root": s.memdir},
	})
	for i := 0; i < 2; i++ {
		resp, err := http.Post(s.srv.URL+"/console/sources", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		want := http.StatusCreated
		if i == 1 {
			want = http.StatusConflict
		}
		if resp.StatusCode != want {
			raw, _ := io.ReadAll(resp.Body)
			t.Errorf("iter %d status %d, want %d: %s", i, resp.StatusCode, want, raw)
		}
		_ = resp.Body.Close()
	}
}

func TestSources_AddRollsBackOnBuildFailure(t *testing.T) {
	s := newSourcesServer(t)
	body, _ := json.Marshal(map[string]any{
		"adapter": "source:files",
		"name":    "bad",
		"config":  map[string]any{"root": "/this/path/does/not/exist/anywhere"},
	})
	resp, err := http.Post(s.srv.URL+"/console/sources", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}

	// Confirm the row was rolled back — the dashboard must not
	// see a source that can never sync.
	state := getStateResponse(t, s.srv.URL)
	for _, it := range state.Sources.Items {
		if it.Name == "bad" {
			t.Errorf("source 'bad' should have been rolled back: %+v", it)
		}
	}
}

func TestSources_SyncRefusesWhileRunning(t *testing.T) {
	s := newSourcesServer(t)
	// Insert a source directly via the store + mark it running, so
	// we can assert the 409 response shape without depending on
	// timing.
	ctx := context.Background()
	srcStore, err := source.OpenStore(ctx, filepath.Join(s.dir, "runtime.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = srcStore.Close() }()

	if _, err := srcStore.Insert(ctx, source.Configured{
		Name:      "stuck",
		AdapterID: "source:files",
		Config:    map[string]any{"root": s.memdir},
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := source.MarkSyncRunning(ctx, srcStore, "stuck"); err != nil {
		t.Fatalf("MarkSyncRunning: %v", err)
	}

	resp, err := http.Post(s.srv.URL+"/console/sources/stuck/sync", "application/json", nil)
	if err != nil {
		t.Fatalf("POST sync: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		raw, _ := io.ReadAll(resp.Body)
		t.Errorf("status %d, want 409: %s", resp.StatusCode, raw)
	}
}

func TestSources_RejectsBadInput(t *testing.T) {
	s := newSourcesServer(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"not-json", "not-json", http.StatusBadRequest},
		{"missing-adapter", `{"name":"x"}`, http.StatusBadRequest},
		{"missing-name", `{"adapter":"source:files"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := http.Post(s.srv.URL+"/console/sources", "application/json", strings.NewReader(c.body))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != c.want {
				t.Errorf("status %d, want %d", resp.StatusCode, c.want)
			}
		})
	}
}

func TestSources_503WhenSubsystemsMissing(t *testing.T) {
	// Build a server WITHOUT the source store + build env.
	srv := New(Options{
		Addr:    "127.0.0.1:0",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	ts := httptest.NewServer(srv.httpSrv.Handler)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/console/sources", "application/json",
		strings.NewReader(`{"adapter":"source:files","name":"x"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", resp.StatusCode)
	}
}
