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
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/mcp"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// Tests for /console/clients/pair and /console/clients/{id} DELETE.
// We stand up the real permission engine against a t.TempDir; the
// redemption side (/pair) has its own coverage in pair_test.go
// and we don't re-exercise it here — the contract is that
// CreatePairingCode + the existing redemption pipeline produces
// a paired client.

type clientsServer struct {
	srv       *httptest.Server
	dir       string
	engine    *permission.Engine
	permStore *permission.Store
}

func newClientsServer(t *testing.T, withEngine bool) *clientsServer {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()

	out := &clientsServer{dir: dir}
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

// --- tests ---

func TestClients_PairCodeHappyPath(t *testing.T) {
	s := newClientsServer(t, true)

	resp, err := http.Post(s.srv.URL+"/console/clients/pair", "application/json",
		strings.NewReader(`{"client_name":"Claude Desktop"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var out pairCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = resp.Body.Close()

	if out.Code == "" {
		t.Errorf("Code is empty: %+v", out)
	}
	if out.ClientName != "Claude Desktop" {
		t.Errorf("ClientName = %q, want Claude Desktop", out.ClientName)
	}
	if out.ExpiresAt == "" {
		t.Errorf("ExpiresAt is empty")
	}
	exp, err := time.Parse(time.RFC3339Nano, out.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	if time.Until(exp) < time.Minute {
		t.Errorf("ExpiresAt too soon: %v (now %v)", exp, time.Now())
	}
}

func TestClients_PairCodeRequiresClientName(t *testing.T) {
	s := newClientsServer(t, true)

	cases := []string{"", `{}`, `{"client_name":""}`, `not-json`}
	for _, body := range cases {
		resp, err := http.Post(s.srv.URL+"/console/clients/pair",
			"application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %q: %v", body, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestClients_PairCodeCapsTTL(t *testing.T) {
	s := newClientsServer(t, true)

	// Request 24h; expect ≤1h.
	resp, _ := http.Post(s.srv.URL+"/console/clients/pair", "application/json",
		strings.NewReader(`{"client_name":"x","ttl_seconds":86400}`))
	defer func() { _ = resp.Body.Close() }()
	var out pairCodeResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	exp, _ := time.Parse(time.RFC3339Nano, out.ExpiresAt)
	if time.Until(exp) > time.Hour+5*time.Second {
		t.Errorf("TTL not capped: expires_at %v is more than 1h out", exp)
	}
}

func TestClients_RevokeHappyPath(t *testing.T) {
	s := newClientsServer(t, true)

	// Create and redeem a code to produce a Client.
	ctx := context.Background()
	pc, err := s.engine.CreatePairingCode(ctx, "to-revoke", "test", 0)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}
	client, _, err := s.engine.RedeemPairingCode(ctx, pc.Code, nil)
	if err != nil {
		t.Fatalf("RedeemPairingCode: %v", err)
	}

	req, _ := http.NewRequest(http.MethodDelete,
		s.srv.URL+"/console/clients/"+client.ID,
		strings.NewReader(`{"reason":"testing"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}

	// Verify revocation in the store.
	c, err := s.permStore.GetClient(ctx, client.ID)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if c.Active() {
		t.Errorf("client should not be active after revoke")
	}
}

func TestClients_RevokeUnknownReturns404(t *testing.T) {
	s := newClientsServer(t, true)
	req, _ := http.NewRequest(http.MethodDelete, s.srv.URL+"/console/clients/cli_ghost", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}

func TestClients_503WhenEngineMissing(t *testing.T) {
	s := newClientsServer(t, false)
	resp, err := http.Post(s.srv.URL+"/console/clients/pair", "application/json",
		strings.NewReader(`{"client_name":"x"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", resp.StatusCode)
	}
}
