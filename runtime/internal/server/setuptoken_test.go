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

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/mcp"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// gatedFixture spins up a Server with the setup-token gate engaged.
// Each test customizes via the supplied option closures so the fixture
// stays a one-call setup for the common "engine + audit + gate" shape.
type gatedFixture struct {
	srv          *httptest.Server
	gate         *SetupTokenGate
	engine       *permission.Engine
	permStore    *permission.Store
	auditWriter  *audit.Store
	dataDir      string
	consumedPath string
	token        string
}

func newGatedFixture(t *testing.T) *gatedFixture {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()

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

	engine := permission.NewEngine(permStore, w)

	token, err := GenerateSetupToken()
	if err != nil {
		t.Fatalf("GenerateSetupToken: %v", err)
	}
	consumed := filepath.Join(dir, ".setup-consumed")
	gate, err := NewSetupTokenGate(SetupTokenOptions{
		Token:        token,
		Origin:       "test",
		ConsumedPath: consumed,
		Engine:       engine,
	})
	if err != nil {
		t.Fatalf("NewSetupTokenGate: %v", err)
	}

	cfg := config.Default()
	cfg.Runtime.DataDir = dir

	srv := New(Options{
		Addr:       "127.0.0.1:0",
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		Engine:     engine,
		Audit:      w,
		Tools:      mcp.NewRegistry(),
		Resources:  mcp.NewResourceRegistry(),
		BaseConfig: cfg,
		ConfigPath: filepath.Join(dir, "config.yaml"),
		SetupToken: gate,
	})
	ts := httptest.NewServer(srv.httpSrv.Handler)
	t.Cleanup(ts.Close)

	return &gatedFixture{
		srv:          ts,
		gate:         gate,
		engine:       engine,
		permStore:    permStore,
		auditWriter:  w,
		dataDir:      dir,
		consumedPath: consumed,
		token:        token,
	}
}

func (f *gatedFixture) postInit(t *testing.T, authHeader string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"storage": map[string]any{"adapter": "storage:fs-encrypted",
			"config": map[string]any{"root": filepath.Join(f.dataDir, "storage")}},
		"memory": map[string]any{"adapter": "memory:sqlite",
			"config": map[string]any{"path": filepath.Join(f.dataDir, "memory.db")}},
	})
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/console/init?overwrite=1", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// --- behavior tests ---

func TestSetupTokenGate_RejectsMissingAuth(t *testing.T) {
	f := newGatedFixture(t)

	resp := f.postInit(t, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d (want 401): %s", resp.StatusCode, raw)
	}
}

func TestSetupTokenGate_RejectsWrongToken(t *testing.T) {
	f := newGatedFixture(t)

	resp := f.postInit(t, "Bearer not-the-real-token-not-the-real-token-not-the-real")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d (want 401): %s", resp.StatusCode, raw)
	}
}

func TestSetupTokenGate_AcceptsCorrectToken(t *testing.T) {
	f := newGatedFixture(t)

	resp := f.postInit(t, "Bearer "+f.token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d (want 200): %s", resp.StatusCode, raw)
	}
}

func TestSetupTokenGate_ConsumesAfterSuccessfulInit(t *testing.T) {
	f := newGatedFixture(t)

	// First init succeeds with the setup token.
	resp := f.postInit(t, "Bearer "+f.token)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first init: status %d, want 200", resp.StatusCode)
	}

	if !f.gate.IsConsumed() {
		t.Fatal("gate should be consumed after successful init")
	}

	// Sentinel file must exist.
	if _, err := os.Stat(f.consumedPath); err != nil {
		t.Errorf("consumed sentinel not written: %v", err)
	}

	// Second init with the same setup token now rejected.
	resp2 := f.postInit(t, "Bearer "+f.token)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		raw, _ := io.ReadAll(resp2.Body)
		t.Errorf("second init: status %d (want 401 after consume): %s", resp2.StatusCode, raw)
	}
}

func TestSetupTokenGate_HealthzNotGated(t *testing.T) {
	f := newGatedFixture(t)

	resp, err := http.Get(f.srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz status %d (want 200 even with gate active)", resp.StatusCode)
	}

	resp2, err := http.Get(f.srv.URL + "/version")
	if err != nil {
		t.Fatalf("GET /version: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("/version status %d (want 200 even with gate active)", resp2.StatusCode)
	}
}

func TestSetupTokenGate_StatusGated(t *testing.T) {
	f := newGatedFixture(t)

	// /console/state without auth → 401
	resp, err := http.Get(f.srv.URL + "/console/state")
	if err != nil {
		t.Fatalf("GET /console/state: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/console/state without auth: status %d (want 401)", resp.StatusCode)
	}

	// With the setup token → 200
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/console/state", nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authed GET /console/state: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp2.Body)
		t.Errorf("/console/state with token: status %d (want 200): %s", resp2.StatusCode, raw)
	}
}

func TestSetupTokenGate_PreviouslyConsumedSentinelStartsConsumed(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	permStore, err := permission.Open(ctx, filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("permission.Open: %v", err)
	}
	defer func() { _ = permStore.Close() }()
	w, err := audit.OpenSQLite(ctx, filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatalf("audit.OpenSQLite: %v", err)
	}
	defer func() { _ = w.Close(context.Background()) }()
	engine := permission.NewEngine(permStore, w)

	consumed := filepath.Join(dir, ".setup-consumed")
	if err := os.WriteFile(consumed, []byte("consumed\n"), 0o600); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}

	token, _ := GenerateSetupToken()
	gate, err := NewSetupTokenGate(SetupTokenOptions{
		Token:        token,
		ConsumedPath: consumed,
		Engine:       engine,
	})
	if err != nil {
		t.Fatalf("NewSetupTokenGate: %v", err)
	}
	if !gate.IsConsumed() {
		t.Error("gate should start consumed when sentinel exists at construction time")
	}
	if gate.matches(token) {
		t.Error("setup token should not match after gate constructed in consumed state")
	}
}

func TestSetupTokenGate_RejectsTooShortToken(t *testing.T) {
	_, err := NewSetupTokenGate(SetupTokenOptions{
		Token:  "short",
		Engine: &permission.Engine{}, // non-nil; constructor only checks for nil
	})
	if err == nil {
		t.Error("expected error for too-short token, got nil")
	}
	if !strings.Contains(err.Error(), "at least 16") {
		t.Errorf("error message missing length hint: %v", err)
	}
}

func TestSetupTokenGate_NilGateMeansPassthrough(t *testing.T) {
	// Sanity: when SetupToken is nil in Options, the routes mount
	// unauthenticated — the laptop contract is preserved.
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Runtime.DataDir = dir
	srv := New(Options{
		Addr:       "127.0.0.1:0",
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		ConfigPath: filepath.Join(dir, "config.yaml"),
		BaseConfig: cfg,
		// SetupToken intentionally nil.
	})
	ts := httptest.NewServer(srv.httpSrv.Handler)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"storage": map[string]any{"adapter": "storage:fs-encrypted",
			"config": map[string]any{"root": filepath.Join(dir, "storage")}},
		"memory": map[string]any{"adapter": "memory:sqlite",
			"config": map[string]any{"path": filepath.Join(dir, "memory.db")}},
	})
	resp, err := http.Post(ts.URL+"/console/init", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Errorf("status %d (want 200 — laptop path takes no auth): %s", resp.StatusCode, raw)
	}
}

// TestSetupTokenGate_PairedClientFallthrough confirms the gate
// accepts a valid paired-client Bearer credential even before the
// setup token is consumed (and especially after). This is the path a
// returning operator uses on Cloud Run cold-starts: the setup token
// may have been spent on the first deploy and forgotten, but their
// paired client cred still works.
func TestSetupTokenGate_PairedClientFallthrough(t *testing.T) {
	f := newGatedFixture(t)
	ctx := context.Background()

	// Mint a pairing code, redeem it, get a Bearer credential.
	code, err := f.engine.CreatePairingCode(ctx, "test-client", "user:test", 0)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}
	_, bearer, err := f.engine.RedeemPairingCode(ctx, code.Code, nil)
	if err != nil {
		t.Fatalf("RedeemPairingCode: %v", err)
	}

	// Burn the setup token by completing a successful init.
	resp := f.postInit(t, "Bearer "+f.token)
	resp.Body.Close()
	if !f.gate.IsConsumed() {
		t.Fatal("expected gate consumed after init")
	}

	// Paired-client credential must still work post-consumption.
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/console/state", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("paired-client GET: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp2.Body)
		t.Errorf("paired-client post-consume: status %d (want 200): %s", resp2.StatusCode, raw)
	}
}

func TestSetupTokenGate_GenerateProducesHighEntropy(t *testing.T) {
	// Two consecutive generates must produce different tokens, both
	// hex-encoded 64 chars (32 bytes).
	a, err := GenerateSetupToken()
	if err != nil {
		t.Fatalf("Generate #1: %v", err)
	}
	b, err := GenerateSetupToken()
	if err != nil {
		t.Fatalf("Generate #2: %v", err)
	}
	if a == b {
		t.Error("two consecutive generates returned the same token (entropy broken)")
	}
	if len(a) != 64 || len(b) != 64 {
		t.Errorf("token length: a=%d b=%d, want 64 (32 bytes hex)", len(a), len(b))
	}
}
