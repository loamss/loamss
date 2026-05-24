package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// testFixture bundles everything an mcp test needs: handler, engine,
// store (for seeding grants), audit writer, authenticated client.
// One construction call instead of returning a 5-tuple at every call
// site.
type testFixture struct {
	h      *Handler
	engine *permission.Engine
	store  *permission.Store
	audit  *audit.SQLite
	client *permission.Client
}

// newTestHandler builds an MCP Handler with a fresh runtime.db +
// audit.db under a temp dir, an empty Registry, and a paired test
// client whose Principal is attached to every request via
// withAuthedContext.
func newTestHandler(t *testing.T) *testFixture {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	store, err := permission.Open(ctx, filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("permission.Open: %v", err)
	}
	w, err := audit.OpenSQLite(ctx, filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatalf("audit.OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = w.Close(context.Background())
	})
	engine := permission.NewEngine(store, w)
	p, err := engine.CreatePairingCode(ctx, "tester", "user", time.Hour)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}
	c, _, err := engine.RedeemPairingCode(ctx, p.Code, nil)
	if err != nil {
		t.Fatalf("RedeemPairingCode: %v", err)
	}

	h := NewHandler(Deps{
		Engine:        engine,
		Audit:         w,
		Tools:         NewRegistry(),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		ServerName:    "loamss",
		ServerVersion: "v0.1-test",
	})
	return &testFixture{h: h, engine: engine, store: store, audit: w, client: c}
}

// withAuthedContext returns a new *http.Request whose context carries
// the supplied authenticated principal/client. Mirrors the wiring
// the server package performs before delegating to the handler.
func withAuthedContext(r *http.Request, c *permission.Client) *http.Request {
	p := &permission.Principal{Kind: permission.PrincipalClient, ID: c.ID}
	ctx := WithPrincipal(r.Context(), p, c)
	return r.WithContext(ctx)
}

// doRPC posts a JSON-RPC request and returns the parsed Response.
// Fails the test on transport or decode errors.
func doRPC(t *testing.T, h *Handler, c *permission.Client, body any) Response {
	t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(buf))
	req = withAuthedContext(req, c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Fatalf("unexpected status %d, body=%s", rec.Code, rec.Body.String())
	}
	if rec.Code == http.StatusAccepted {
		// Notification path — no body.
		return Response{}
	}
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\n%s", err, rec.Body.String())
	}
	return resp
}

func TestInitialize_HappyPath(t *testing.T) {
	f := newTestHandler(t)
	h, c := f.h, f.client

	resp := doRPC(t, h, c, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test-client", "version": "0.0.1"},
		},
	})
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	// Marshal the result back and decode into InitializeResult.
	rb, _ := json.Marshal(resp.Result)
	var ir InitializeResult
	if err := json.Unmarshal(rb, &ir); err != nil {
		t.Fatalf("decode InitializeResult: %v", err)
	}
	if ir.ProtocolVersion != mcpProtocolVersion {
		t.Errorf("protocolVersion: got %q, want %q", ir.ProtocolVersion, mcpProtocolVersion)
	}
	if ir.ServerInfo.Name != "loamss" || ir.ServerInfo.Version != "v0.1-test" {
		t.Errorf("serverInfo: %+v", ir.ServerInfo)
	}
	if ir.Capabilities.Tools == nil {
		t.Error("server should advertise tools capability")
	}
	if ir.Capabilities.Resources == nil {
		t.Error("server should advertise resources capability")
	}
}

func TestInitialize_VersionDowngradeAllowed(t *testing.T) {
	f := newTestHandler(t)
	h, c := f.h, f.client

	// Client asks for a future version; server returns its version
	// and the client decides what to do.
	resp := doRPC(t, h, c, map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "9999-12-31",
			"clientInfo":      map[string]any{"name": "future", "version": "0"},
		},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	rb, _ := json.Marshal(resp.Result)
	var ir InitializeResult
	_ = json.Unmarshal(rb, &ir)
	if ir.ProtocolVersion != mcpProtocolVersion {
		t.Errorf("expected server version, got %q", ir.ProtocolVersion)
	}
}

func TestDispatch_UnknownMethod(t *testing.T) {
	f := newTestHandler(t)
	h, c := f.h, f.client

	resp := doRPC(t, h, c, map[string]any{
		"jsonrpc": "2.0", "id": 42, "method": "nothing/here",
	})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != codeMethodNotFound {
		t.Errorf("code: got %d, want %d", resp.Error.Code, codeMethodNotFound)
	}
	if !strings.Contains(resp.Error.Message, "nothing/here") {
		t.Errorf("message should mention method, got %q", resp.Error.Message)
	}
}

func TestDispatch_RejectsMissingMethod(t *testing.T) {
	f := newTestHandler(t)
	h, c := f.h, f.client

	resp := doRPC(t, h, c, map[string]any{
		"jsonrpc": "2.0", "id": 1,
	})
	if resp.Error == nil || resp.Error.Code != codeInvalidRequest {
		t.Errorf("expected invalid-request, got %+v", resp.Error)
	}
}

func TestDispatch_RejectsBadJSONRPCVersion(t *testing.T) {
	f := newTestHandler(t)
	h, c := f.h, f.client

	resp := doRPC(t, h, c, map[string]any{
		"jsonrpc": "1.0", "id": 1, "method": "initialize",
	})
	if resp.Error == nil || resp.Error.Code != codeInvalidRequest {
		t.Errorf("expected invalid-request, got %+v", resp.Error)
	}
}

func TestDispatch_ParseErrorOnGarbage(t *testing.T) {
	f := newTestHandler(t)
	h, c := f.h, f.client

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("not json"))
	req = withAuthedContext(req, c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	var resp Response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != codeParseError {
		t.Errorf("expected parse-error, got %+v", resp.Error)
	}
}

func TestNotification_NoResponse(t *testing.T) {
	f := newTestHandler(t)
	h, c := f.h, f.client

	// JSON-RPC 2.0: a notification has no id field.
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	req = withAuthedContext(req, c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Errorf("expected 202 Accepted, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("notification should have empty response, got %q", rec.Body.String())
	}
}

func TestMissingPrincipal_InternalError(t *testing.T) {
	// Bypassing withAuthedContext should produce -32603 since the
	// auth middleware is required upstream of the MCP handler.
	f := newTestHandler(t)
	h := f.h

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp Response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != codeInternalError {
		t.Errorf("expected internal-error, got %+v", resp.Error)
	}
}

func TestMethodNotAllowed_PUT(t *testing.T) {
	f := newTestHandler(t)
	h, c := f.h, f.client

	req := httptest.NewRequest(http.MethodPut, "/mcp", nil)
	req = withAuthedContext(req, c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}
