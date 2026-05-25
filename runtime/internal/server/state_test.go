package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/mcp"
	"github.com/loamss/loamss/runtime/internal/permission"
	"github.com/loamss/loamss/runtime/internal/source"
)

// Tests for GET /console/state. The dashboard reads this on load
// and on refresh; the contract these lock in is the response
// shape and the graceful-degradation behavior when individual
// subsystems aren't wired into the server.

// stateServer builds a Server with whichever subsystems the test
// chose to thread in. The fields that are nil pass through as
// Options{...: nil}; the handler then returns "available: false"
// for the corresponding panes.
type stateServer struct {
	srv     *httptest.Server
	sources *source.Store
	caps    *capsule.Store
	engine  *permission.Engine
	audit   audit.Writer
}

func newStateServer(t *testing.T, withAll bool) *stateServer {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()

	cfg := config.Default()
	cfg.Runtime.DataDir = dir
	cfg.Storage = config.AdapterConfig{Adapter: "storage:fs-encrypted"}
	cfg.Memory = config.AdapterConfig{Adapter: "memory:sqlite"}
	cfg.Models = []config.AdapterConfig{
		{Adapter: "model:ollama"},
	}

	opts := Options{
		Addr:       "127.0.0.1:0",
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test-v",
		BaseConfig: cfg,
	}

	out := &stateServer{}

	if withAll {
		// Stand up the real subsystems against the temp dir. Same
		// SQLite paths the production runtime uses; tests get to
		// drive real Insert calls and observe the response.
		store, err := permission.Open(ctx, filepath.Join(dir, "runtime.db"))
		if err != nil {
			t.Fatalf("permission.Open: %v", err)
		}
		w, err := audit.OpenSQLite(ctx, filepath.Join(dir, "audit.db"))
		if err != nil {
			_ = store.Close()
			t.Fatalf("audit.OpenSQLite: %v", err)
		}
		srcStore, err := source.OpenStore(ctx, filepath.Join(dir, "runtime.db"))
		if err != nil {
			_ = store.Close()
			_ = w.Close(ctx)
			t.Fatalf("source.OpenStore: %v", err)
		}
		capStore, err := capsule.OpenStore(ctx, filepath.Join(dir, "runtime.db"))
		if err != nil {
			_ = store.Close()
			_ = w.Close(ctx)
			_ = srcStore.Close()
			t.Fatalf("capsule.OpenStore: %v", err)
		}
		t.Cleanup(func() {
			_ = store.Close()
			_ = w.Close(context.Background())
			_ = srcStore.Close()
			_ = capStore.Close()
		})
		out.sources = srcStore
		out.caps = capStore
		out.engine = permission.NewEngine(store, w)
		out.audit = w

		opts.Engine = out.engine
		opts.Audit = w
		// Tools+Resources are required when Engine is set. Empty
		// registries are fine — these tests don't exercise tool
		// dispatch.
		opts.Tools = mcp.NewRegistry()
		opts.Resources = mcp.NewResourceRegistry()
		opts.Sources = srcStore
		opts.Capsules = capStore
		// host left nil — the capsule host's start has real subprocess
		// machinery that's overkill for the state-shape tests.
	}

	s := New(opts)
	ts := httptest.NewServer(s.httpSrv.Handler)
	t.Cleanup(ts.Close)
	out.srv = ts
	return out
}

func getStateResponse(t *testing.T, base string) consoleStateResponse {
	t.Helper()
	resp, err := http.Get(base + "/console/state")
	if err != nil {
		t.Fatalf("GET /console/state: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var out consoleStateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// --- tests ---

func TestConsoleState_BareServerReportsUnavailablePanes(t *testing.T) {
	// No engine, no source store, no capsule store — only the
	// runtime/version + the wired-up BaseConfig.
	s := newStateServer(t, false)
	out := getStateResponse(t, s.srv.URL)

	if out.Runtime.Version != "test-v" {
		t.Errorf("Runtime.Version = %q, want test-v", out.Runtime.Version)
	}
	if !out.Config.Available {
		t.Error("Config.Available should be true when BaseConfig is wired")
	}
	if out.Config.StorageAdapter != "storage:fs-encrypted" {
		t.Errorf("Config.StorageAdapter = %q", out.Config.StorageAdapter)
	}

	// Every dynamic pane should report unavailable when its
	// dependency isn't wired.
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{"Sources", out.Sources.Available},
		{"Capsules", out.Capsules.Available},
		{"Clients", out.Clients.Available},
		{"Approvals", out.Approvals.Available},
		{"Activity", out.Activity.Available},
	} {
		if c.ok {
			t.Errorf("%s.Available should be false on bare server", c.name)
		}
	}
}

func TestConsoleState_FullServerWithEmptyStoresReturnsEmptyArrays(t *testing.T) {
	s := newStateServer(t, true)
	out := getStateResponse(t, s.srv.URL)

	if !out.Sources.Available || len(out.Sources.Items) != 0 {
		t.Errorf("Sources = %+v, want available + empty", out.Sources)
	}
	if !out.Capsules.Available || len(out.Capsules.Items) != 0 {
		t.Errorf("Capsules = %+v, want available + empty", out.Capsules)
	}
	if !out.Clients.Available || len(out.Clients.Items) != 0 {
		t.Errorf("Clients = %+v, want available + empty", out.Clients)
	}
	if !out.Approvals.Available || len(out.Approvals.Items) != 0 {
		t.Errorf("Approvals = %+v, want available + empty", out.Approvals)
	}
	if !out.Activity.Available || len(out.Activity.Items) != 0 {
		t.Errorf("Activity = %+v, want available + empty", out.Activity)
	}

	if out.Runtime.UptimeSeconds < 0 {
		t.Errorf("UptimeSeconds negative: %d", out.Runtime.UptimeSeconds)
	}
}

func TestConsoleState_ListsInsertedSources(t *testing.T) {
	s := newStateServer(t, true)
	ctx := context.Background()

	added, err := s.sources.Insert(ctx, source.Configured{
		Name:      "my-files",
		AdapterID: "source:files",
		Config:    map[string]any{"root": "/tmp/x"},
	})
	if err != nil {
		t.Fatalf("Insert source: %v", err)
	}
	if err := s.sources.SetLastSync(ctx, "my-files", "success",
		map[string]any{"entries_added": 7}, time.Now().UTC()); err != nil {
		t.Fatalf("SetLastSync: %v", err)
	}

	out := getStateResponse(t, s.srv.URL)
	if len(out.Sources.Items) != 1 {
		t.Fatalf("Sources count = %d, want 1: %+v", len(out.Sources.Items), out.Sources)
	}
	got := out.Sources.Items[0]
	if got.ID != added.ID {
		t.Errorf("Sources[0].ID = %q, want %q", got.ID, added.ID)
	}
	if got.Adapter != "source:files" {
		t.Errorf("Sources[0].Adapter = %q", got.Adapter)
	}
	if got.LastSyncStatus != "success" {
		t.Errorf("Sources[0].LastSyncStatus = %q", got.LastSyncStatus)
	}
	if got.LastSyncAt == "" {
		t.Errorf("Sources[0].LastSyncAt should be set after SetLastSync")
	}
	if entries, ok := got.Summary["entries_added"]; !ok || entries == nil {
		t.Errorf("Sources[0].Summary missing entries_added: %+v", got.Summary)
	}
}

func TestConsoleState_ListsRecentAuditEvents(t *testing.T) {
	s := newStateServer(t, true)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := s.audit.Append(ctx, audit.Entry{
			Type:  "tool.invoked",
			Actor: audit.Actor{Kind: audit.ActorClient, ID: "cli_test"},
			Subject: &audit.Subject{
				Kind: audit.SubjectTool,
				ID:   "memory.show",
			},
			Outcome: audit.OutcomeSuccess,
			Data:    map[string]any{"capability": "memory.read"},
		})
		if err != nil {
			t.Fatalf("audit Append: %v", err)
		}
	}

	out := getStateResponse(t, s.srv.URL)
	if len(out.Activity.Items) != 3 {
		t.Fatalf("Activity count = %d, want 3", len(out.Activity.Items))
	}
	// Reverse: true → newest first. We can't predict IDs but we can
	// assert the timestamps are non-empty and outcome carries through.
	for i, ev := range out.Activity.Items {
		if ev.Type != "tool.invoked" {
			t.Errorf("Activity[%d].Type = %q", i, ev.Type)
		}
		if ev.Outcome != "success" {
			t.Errorf("Activity[%d].Outcome = %q", i, ev.Outcome)
		}
		if ev.At == "" {
			t.Errorf("Activity[%d].At is empty", i)
		}
		if ev.SubjectID != "memory.show" {
			t.Errorf("Activity[%d].SubjectID = %q", i, ev.SubjectID)
		}
	}
}

func TestConsoleState_RejectsPostMethod(t *testing.T) {
	s := newStateServer(t, false)
	resp, err := http.Post(s.srv.URL+"/console/state", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Subtle: the route is registered as `GET /console/state`, so a
	// POST doesn't match that pattern. The `/` console catch-all
	// then claims the request, sees no static file at that path,
	// and returns 404. We're not getting 405 because the mux
	// doesn't know that "this path with a different method exists"
	// once the catch-all wins. The contract this test locks in is
	// just "not a 2xx" — POST never succeeds against /console/state.
	if resp.StatusCode/100 == 2 {
		t.Errorf("status %d, want a non-2xx for POST", resp.StatusCode)
	}
}
