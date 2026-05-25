package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/mcp"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// Tests for /console/approvals/{id}/approve and /deny. We drive
// the real permission engine against a t.TempDir SQLite store +
// stand up a server that exposes the endpoints. Approvals are
// enqueued directly via the store (skipping the full engine.Check
// flow) so the tests focus on the resolve path.

type approvalsServer struct {
	srv       *httptest.Server
	dir       string
	engine    *permission.Engine
	permStore *permission.Store
}

func newApprovalsServer(t *testing.T, withEngine bool) *approvalsServer {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()

	out := &approvalsServer{dir: dir}
	opts := Options{
		Addr:    "127.0.0.1:0",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	}

	if withEngine {
		permStore, err := permission.Open(ctx, filepath.Join(dir, "runtime.db"))
		if err != nil {
			t.Fatalf("permission.Open: %v", err)
		}
		w, err := audit.OpenSQLite(ctx, filepath.Join(dir, "audit.db"))
		if err != nil {
			_ = permStore.Close()
			t.Fatalf("audit.OpenSQLite: %v", err)
		}
		t.Cleanup(func() {
			_ = permStore.Close()
			_ = w.Close(context.Background())
		})
		out.permStore = permStore
		out.engine = permission.NewEngine(permStore, w)

		cfg := config.Default()
		cfg.Runtime.DataDir = dir
		opts.BaseConfig = cfg
		opts.Engine = out.engine
		opts.Audit = w
		opts.Tools = mcp.NewRegistry()
		opts.Resources = mcp.NewResourceRegistry()
	}

	srv := New(opts)
	ts := httptest.NewServer(srv.httpSrv.Handler)
	t.Cleanup(ts.Close)
	out.srv = ts
	return out
}

// enqueueTestApproval drops a pending approval row directly through
// the store. The real lifecycle (engine.Check → enqueue → resolve)
// has its own tests in internal/permission; this helper just gives
// us an ID to feed the HTTP endpoint.
func enqueueTestApproval(t *testing.T, store *permission.Store) string {
	t.Helper()
	ctx := context.Background()
	a, err := store.EnqueueApproval(ctx, permission.PendingApproval{
		Principal: permission.Principal{
			Kind: permission.PrincipalClient,
			ID:   "cli_test",
		},
		Capability:     "memory.read",
		AttemptedScope: map[string]any{"namespace": "docs"},
		Rationale:      "wants to read recent threads",
	})
	if err != nil {
		t.Fatalf("EnqueueApproval: %v", err)
	}
	return a.ID
}

// --- tests ---

func TestApprovals_Approve(t *testing.T) {
	s := newApprovalsServer(t, true)
	id := enqueueTestApproval(t, s.permStore)

	resp, err := http.Post(
		s.srv.URL+"/console/approvals/"+id+"/approve",
		"application/json",
		strings.NewReader(`{"note":"looks fine"}`),
	)
	if err != nil {
		t.Fatalf("POST approve: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var out approvalDecisionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = resp.Body.Close()

	if out.Decision != "granted" {
		t.Errorf("decision = %q, want granted", out.Decision)
	}

	// Confirm it's no longer pending.
	a, err := s.permStore.GetApproval(context.Background(), id)
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if a.State != permission.ApprovalGranted {
		t.Errorf("state = %q, want granted", a.State)
	}
	if a.DecisionNote != "looks fine" {
		t.Errorf("note not persisted: %q", a.DecisionNote)
	}
}

func TestApprovals_Deny(t *testing.T) {
	s := newApprovalsServer(t, true)
	id := enqueueTestApproval(t, s.permStore)

	resp, err := http.Post(
		s.srv.URL+"/console/approvals/"+id+"/deny",
		"application/json",
		strings.NewReader(""), // empty body is fine — no note
	)
	if err != nil {
		t.Fatalf("POST deny: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	_ = resp.Body.Close()

	a, err := s.permStore.GetApproval(context.Background(), id)
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if a.State != permission.ApprovalDenied {
		t.Errorf("state = %q, want denied", a.State)
	}
}

func TestApprovals_DoubleResolveReturns409(t *testing.T) {
	s := newApprovalsServer(t, true)
	id := enqueueTestApproval(t, s.permStore)

	resp1, _ := http.Post(s.srv.URL+"/console/approvals/"+id+"/approve",
		"application/json", strings.NewReader(""))
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first approve: %d", resp1.StatusCode)
	}

	// Second decision on the same id.
	resp2, err := http.Post(s.srv.URL+"/console/approvals/"+id+"/deny",
		"application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST second decision: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("status %d, want 409", resp2.StatusCode)
	}
}

func TestApprovals_NotFound(t *testing.T) {
	s := newApprovalsServer(t, true)
	resp, err := http.Post(
		s.srv.URL+"/console/approvals/apr_nonexistent/approve",
		"application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}

func TestApprovals_BadJSON(t *testing.T) {
	s := newApprovalsServer(t, true)
	id := enqueueTestApproval(t, s.permStore)
	resp, err := http.Post(s.srv.URL+"/console/approvals/"+id+"/approve",
		"application/json", strings.NewReader("not-json"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", resp.StatusCode)
	}
}

func TestApprovals_503WhenEngineMissing(t *testing.T) {
	s := newApprovalsServer(t, false)
	resp, err := http.Post(s.srv.URL+"/console/approvals/x/approve",
		"application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", resp.StatusCode)
	}
}
