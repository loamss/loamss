package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newServerForConsoleTest constructs a Server with only the /console/init
// route wired (no engine, so no /mcp + /pair). The handler is mounted
// in the same mux setup New() uses for the public endpoints.
func newServerForConsoleTest(t *testing.T) *httptest.Server {
	t.Helper()
	s := New(Options{
		Addr:    "127.0.0.1:0",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		// No Engine, Audit, Tools, Resources — /console/init doesn't
		// need them and we don't want to drag in the full set just
		// for endpoint-shape tests.
	})
	ts := httptest.NewServer(s.httpSrv.Handler)
	t.Cleanup(ts.Close)
	return ts
}

func TestConsoleInit_HappyPath(t *testing.T) {
	ts := newServerForConsoleTest(t)

	payload := map[string]any{
		"storage": map[string]any{
			"adapter": "storage:fs-encrypted",
			"config":  map[string]any{"root": "/home/me/.loamss/storage"},
		},
		"memory": map[string]any{
			"adapter": "memory:sqlite",
			"config":  map[string]any{"path": "/home/me/.loamss/memory.db"},
		},
		"models": []map[string]any{
			{"adapter": "model:anthropic", "config": map[string]any{
				"api_key_env": "ANTHROPIC_API_KEY",
			}},
		},
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(ts.URL+"/console/init",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var out consoleInitResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK {
		t.Errorf("ok=false: %+v", out)
	}
	if out.Echo.Storage.Adapter != "storage:fs-encrypted" {
		t.Errorf("echo lost storage adapter")
	}
	if out.Echo.Memory.Adapter != "memory:sqlite" {
		t.Errorf("echo lost memory adapter")
	}
	if out.Echo.Models[0].Adapter != "model:anthropic" {
		t.Errorf("echo lost model")
	}
	// v0.1: every capability is false because the runtime hasn't
	// shipped the writers yet.
	if out.Capability.WritesConfigFile {
		t.Errorf("v0.1 stub should not claim writes_config_file=true")
	}
	if out.Note == "" {
		t.Errorf("stub response should carry an explanatory note")
	}
}

func TestConsoleInit_RejectsMissingFields(t *testing.T) {
	ts := newServerForConsoleTest(t)
	body, _ := json.Marshal(map[string]any{})
	resp, err := http.Post(ts.URL+"/console/init",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "required") {
		t.Errorf("expected error mentioning required field, got %s", raw)
	}
}

func TestConsoleInit_RejectsNonJSON(t *testing.T) {
	ts := newServerForConsoleTest(t)
	resp, err := http.Post(ts.URL+"/console/init",
		"application/json", strings.NewReader("not-json"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", resp.StatusCode)
	}
}

func TestConsoleInit_RejectsOversizedBody(t *testing.T) {
	ts := newServerForConsoleTest(t)
	// MaxBytesReader caps at 64 KiB; push past that.
	huge := strings.Repeat("a", 70*1024)
	resp, err := http.Post(ts.URL+"/console/init",
		"application/json", strings.NewReader(huge))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", resp.StatusCode)
	}
}
