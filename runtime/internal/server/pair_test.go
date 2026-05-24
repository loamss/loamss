package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/mcp"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// fullDeps holds everything a full-fledged Server test needs:
// permission store, audit writer, engine, tool registry, plus
// teardown.
type fullDeps struct {
	store  *permission.Store
	audit  *audit.SQLite
	engine *permission.Engine
	tools  *mcp.Registry
	dir    string
}

func newFullDeps(t *testing.T) *fullDeps {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	store, err := permission.Open(ctx, filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("permission.Open: %v", err)
	}
	w, err := audit.OpenSQLite(ctx, filepath.Join(dir, "audit.db"))
	if err != nil {
		_ = store.Close()
		t.Fatalf("audit.OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = w.Close(context.Background())
	})
	return &fullDeps{
		store:  store,
		audit:  w,
		engine: permission.NewEngine(store, w),
		tools:  mcp.NewRegistry(),
		dir:    dir,
	}
}

// startFullServer launches a full Server (with Engine/Audit/Tools)
// on a random port and returns its base URL plus a stop function.
func startFullServer(t *testing.T, d *fullDeps) (string, func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	srv := New(Options{
		Addr:    l.Addr().String(),
		Logger:  silentLogger(),
		Version: "v0.1-test",
		Engine:  d.engine,
		Audit:   d.audit,
		Tools:   d.tools,
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(l) }()
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		if err := <-errCh; err != nil {
			t.Errorf("Serve returned error: %v", err)
		}
	}
	return "http://" + l.Addr().String(), stop
}

// postJSON is a tiny helper around POSTing a JSON body.
func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func TestPair_HappyPath(t *testing.T) {
	d := newFullDeps(t)
	base, stop := startFullServer(t, d)
	defer stop()

	// Seed a pairing code via the engine.
	p, err := d.engine.CreatePairingCode(context.Background(), "ChatGPT", "user", time.Hour)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}

	resp := postJSON(t, base+"/pair", pairRequest{
		Code:     p.Code,
		Metadata: map[string]any{"client_version": "1.2.3"},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var pr pairResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if pr.Client == nil || pr.Client.Name != "ChatGPT" {
		t.Errorf("client: %+v", pr.Client)
	}
	if !strings.HasPrefix(pr.Token, "lck_") {
		t.Errorf("token shape: %q", pr.Token)
	}
	if !strings.HasSuffix(pr.EndpointURL, "/mcp") {
		t.Errorf("endpoint_url should end with /mcp, got %q", pr.EndpointURL)
	}

	// Metadata records paired_via=http so observers can tell HTTP
	// pairings from CLI pairings.
	c, _ := d.store.GetClient(context.Background(), pr.Client.ID)
	if c.Metadata["paired_via"] != "http" {
		t.Errorf("paired_via should be 'http', got %v", c.Metadata["paired_via"])
	}
	if c.Metadata["client_version"] != "1.2.3" {
		t.Errorf("client_version not preserved: %v", c.Metadata["client_version"])
	}
}

func TestPair_UnknownCode(t *testing.T) {
	d := newFullDeps(t)
	base, stop := startFullServer(t, d)
	defer stop()

	resp := postJSON(t, base+"/pair", pairRequest{Code: "NOPE-NOPE"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestPair_ExpiredCode(t *testing.T) {
	d := newFullDeps(t)
	base, stop := startFullServer(t, d)
	defer stop()

	// Inject an already-expired code directly.
	now := time.Now().UTC()
	if err := d.store.InsertPairingCode(context.Background(), permission.PairingCode{
		Code:       "OLD-CODE",
		ClientName: "stale",
		CreatedBy:  "user",
		CreatedAt:  now.Add(-time.Hour),
		ExpiresAt:  now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("InsertPairingCode: %v", err)
	}

	resp := postJSON(t, base+"/pair", pairRequest{Code: "OLD-CODE"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("expected 410 Gone, got %d", resp.StatusCode)
	}
}

func TestPair_AlreadyRedeemed(t *testing.T) {
	d := newFullDeps(t)
	base, stop := startFullServer(t, d)
	defer stop()

	p, _ := d.engine.CreatePairingCode(context.Background(), "x", "user", time.Hour)
	if _, _, err := d.engine.RedeemPairingCode(context.Background(), p.Code, nil); err != nil {
		t.Fatalf("seed redeem: %v", err)
	}
	resp := postJSON(t, base+"/pair", pairRequest{Code: p.Code})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 Conflict, got %d", resp.StatusCode)
	}
}

func TestPair_MissingCodeField(t *testing.T) {
	d := newFullDeps(t)
	base, stop := startFullServer(t, d)
	defer stop()

	resp := postJSON(t, base+"/pair", pairRequest{Code: ""})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestPair_InvalidJSON(t *testing.T) {
	d := newFullDeps(t)
	base, stop := startFullServer(t, d)
	defer stop()

	resp, err := http.Post(base+"/pair", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestPair_NotMountedWhenEngineNil(t *testing.T) {
	// Reproduce the historical /healthz-only server and verify /pair
	// returns 404, not 500.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	srv := New(Options{
		Addr:    l.Addr().String(),
		Logger:  silentLogger(),
		Version: "v0.1-test",
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(l) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-errCh
	}()

	resp, err := http.Post("http://"+l.Addr().String()+"/pair", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 (route not registered), got %d", resp.StatusCode)
	}
}
