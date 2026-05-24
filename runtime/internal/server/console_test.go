package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/config"
)

// newServerForConsoleTest constructs a Server with only the /console/init
// route wired (no engine, so no /mcp + /pair). Writes are redirected to
// a per-test path so the test never touches ~/.loamss.
func newServerForConsoleTest(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	s := New(Options{
		Addr:       "127.0.0.1:0",
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		ConfigPath: cfgPath,
	})
	ts := httptest.NewServer(s.httpSrv.Handler)
	t.Cleanup(ts.Close)
	return ts, cfgPath
}

func validInitPayload() map[string]any {
	return map[string]any{
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
}

func TestConsoleInit_HappyPath(t *testing.T) {
	ts, cfgPath := newServerForConsoleTest(t)

	body, _ := json.Marshal(validInitPayload())
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
	if !out.Capability.WritesConfigFile {
		t.Errorf("writes_config_file should be true now that the writer is wired")
	}
	if out.WrittenTo != cfgPath {
		t.Errorf("WrittenTo = %q, want %q", out.WrittenTo, cfgPath)
	}
	if out.NextStep == "" {
		t.Errorf("response should carry a next-step hint")
	}

	// The file landed and Load round-trips.
	if _, err := os.Stat(cfgPath); err != nil {
		t.Errorf("config file not on disk: %v", err)
	}
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load round-trip: %v", err)
	}
	if loaded.Storage.Adapter != "storage:fs-encrypted" {
		t.Errorf("storage adapter not persisted: %q", loaded.Storage.Adapter)
	}
	if loaded.Memory.Adapter != "memory:sqlite" {
		t.Errorf("memory adapter not persisted: %q", loaded.Memory.Adapter)
	}
	if len(loaded.Models) != 1 || loaded.Models[0].Adapter != "model:anthropic" {
		t.Errorf("models not persisted: %+v", loaded.Models)
	}
}

func TestConsoleInit_RejectsMissingFields(t *testing.T) {
	ts, _ := newServerForConsoleTest(t)
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
	ts, _ := newServerForConsoleTest(t)
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
	ts, _ := newServerForConsoleTest(t)
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

func TestConsoleInit_ConflictsOnExistingFile(t *testing.T) {
	ts, cfgPath := newServerForConsoleTest(t)

	body, _ := json.Marshal(validInitPayload())

	// First write succeeds.
	resp1, err := http.Post(ts.URL+"/console/init",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("first POST: %v", err)
	}
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first write status %d, want 200", resp1.StatusCode)
	}

	// Second write hits the existing file: 409.
	resp2, err := http.Post(ts.URL+"/console/init",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("second POST: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("second write status %d, want 409", resp2.StatusCode)
	}
	var conflict consoleInitConflictResponse
	if err := json.NewDecoder(resp2.Body).Decode(&conflict); err != nil {
		t.Fatalf("decode 409: %v", err)
	}
	if conflict.Code != "config_already_exists" {
		t.Errorf("conflict code = %q, want config_already_exists", conflict.Code)
	}
	if conflict.Path != cfgPath {
		t.Errorf("conflict path = %q, want %q", conflict.Path, cfgPath)
	}
	if !strings.Contains(conflict.Hint, "overwrite=1") {
		t.Errorf("hint should mention overwrite=1 query: %q", conflict.Hint)
	}
}

func TestConsoleInit_OverwriteBacksUpAndReplaces(t *testing.T) {
	ts, cfgPath := newServerForConsoleTest(t)

	// First write.
	body, _ := json.Marshal(validInitPayload())
	resp1, _ := http.Post(ts.URL+"/console/init", "application/json", bytes.NewReader(body))
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first write: %d", resp1.StatusCode)
	}

	// Second write, with overwrite=1, succeeds.
	payload2 := validInitPayload()
	payload2["memory"] = map[string]any{
		"adapter": "memory:sqlite",
		"config":  map[string]any{"path": "/tmp/changed.db"},
	}
	body2, _ := json.Marshal(payload2)
	resp2, err := http.Post(ts.URL+"/console/init?overwrite=1",
		"application/json", bytes.NewReader(body2))
	if err != nil {
		t.Fatalf("overwrite POST: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp2.Body)
		t.Fatalf("overwrite status %d: %s", resp2.StatusCode, raw)
	}

	// New file reflects the new memory config.
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, _ := loaded.Memory.Config["path"].(string); got != "/tmp/changed.db" {
		t.Errorf("memory.config.path = %q, want /tmp/changed.db (overwrite didn't take)", got)
	}

	// A backup was left behind.
	dir := filepath.Dir(cfgPath)
	entries, _ := os.ReadDir(dir)
	var foundBackup bool
	for _, e := range entries {
		if e.Name() == "config.yaml" {
			continue
		}
		if strings.HasPrefix(e.Name(), "config.yaml") && strings.HasSuffix(e.Name(), ".bak") {
			foundBackup = true
			break
		}
	}
	if !foundBackup {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("no timestamped backup found in %v", names)
	}
}

func TestConsoleInit_RejectsInvalidAdapter(t *testing.T) {
	ts, cfgPath := newServerForConsoleTest(t)

	// Adapter ID without the required "namespace:" prefix → validate
	// rejects → WriteAtomic refuses → 500.
	payload := validInitPayload()
	payload["storage"] = map[string]any{"adapter": "not-a-valid-id"}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(ts.URL+"/console/init",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		// (We surface validation as 500 today; if we add a structured
		// validation-error path later this expectation moves.)
		t.Errorf("status %d, want 500 for invalid adapter", resp.StatusCode)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("invalid payload produced a file on disk: stat err=%v", err)
	}
}
