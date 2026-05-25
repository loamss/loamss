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
	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/mcp"
	"github.com/loamss/loamss/runtime/internal/permission"
	"github.com/loamss/loamss/runtime/internal/source"
)

// Tests for the /console/capsules endpoints. We stand up the real
// Installer + capsule.Store against a t.TempDir and feed it tiny
// manifest-only capsules (no subprocess code) so the tests don't
// depend on bun / node being on PATH.

type capsulesServer struct {
	srv       *httptest.Server
	dir       string
	capsules  *capsule.Store
	installer *capsule.Installer
}

func newCapsulesServer(t *testing.T) *capsulesServer {
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
	capStore, err := capsule.OpenStore(ctx, filepath.Join(dir, "runtime.db"))
	if err != nil {
		_ = permStore.Close()
		_ = w.Close(ctx)
		t.Fatalf("capsule.OpenStore: %v", err)
	}
	srcStore, err := source.OpenStore(ctx, filepath.Join(dir, "runtime.db"))
	if err != nil {
		_ = permStore.Close()
		_ = w.Close(ctx)
		_ = capStore.Close()
		t.Fatalf("source.OpenStore: %v", err)
	}
	t.Cleanup(func() {
		_ = permStore.Close()
		_ = w.Close(context.Background())
		_ = capStore.Close()
		_ = srcStore.Close()
	})

	engine := permission.NewEngine(permStore, w)
	installer := capsule.NewInstaller(capStore, engine, w, filepath.Join(dir, "capsules"))

	cfg := config.Default()
	cfg.Runtime.DataDir = dir

	srv := New(Options{
		Addr:             "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:          "test-v",
		BaseConfig:       cfg,
		Engine:           engine,
		Audit:            w,
		Tools:            mcp.NewRegistry(),
		Resources:        mcp.NewResourceRegistry(),
		Sources:          srcStore,
		Capsules:         capStore,
		CapsuleInstaller: installer,
		// Host omitted — the test capsules are manifest-only, no
		// subprocess to spawn. The endpoints' "service unavailable"
		// path for start/stop is exercised here.
	})
	ts := httptest.NewServer(srv.httpSrv.Handler)
	t.Cleanup(ts.Close)

	return &capsulesServer{
		srv:       ts,
		dir:       dir,
		capsules:  capStore,
		installer: installer,
	}
}

// writeManifestOnlyCapsule drops a capsule.yaml in a fresh dir
// inside the test's temp area. The installer accepts manifest-only
// capsules for tests (no code copy step), so this is enough to
// exercise the full install pipeline.
func writeManifestOnlyCapsule(t *testing.T, baseDir, name, version string) string {
	t.Helper()
	dir := filepath.Join(baseDir, "src-"+name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := strings.ReplaceAll(strings.ReplaceAll(`spec_version: "0.1"
name: __NAME__
version: __VERSION__
description: A test capsule.
author:
  name: tester
permissions:
  - capability: memory.read
    rationale: testing
tools:
  - name: noop
    description: does nothing
    input_schema:
      type: object
      properties: {}
      additionalProperties: false
runtime:
  type: subprocess
  entrypoint: ["echo", "noop"]
  protocol: mcp
`, "__NAME__", name), "__VERSION__", version)
	if err := os.WriteFile(filepath.Join(dir, "capsule.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write capsule.yaml: %v", err)
	}
	return dir
}

// --- tests ---

func TestCapsules_InstallListUninstall(t *testing.T) {
	s := newCapsulesServer(t)
	src := writeManifestOnlyCapsule(t, s.dir, "demo", "0.1.0")

	body, _ := json.Marshal(map[string]any{"path": src})
	resp, err := http.Post(s.srv.URL+"/console/capsules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST install: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var out capsuleInstallResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = resp.Body.Close()

	if out.Capsule.Name != "demo" {
		t.Errorf("capsule name = %q", out.Capsule.Name)
	}
	if len(out.Manifest.Permissions) != 1 || out.Manifest.Permissions[0].Capability != "memory.read" {
		t.Errorf("permissions slip wrong: %+v", out.Manifest.Permissions)
	}
	if len(out.Grants) != 1 {
		t.Errorf("expected 1 grant, got %d", len(out.Grants))
	}

	// /console/state reflects it.
	state := getStateResponse(t, s.srv.URL)
	if len(state.Capsules.Items) != 1 || state.Capsules.Items[0].Name != "demo" {
		t.Fatalf("state.Capsules = %+v", state.Capsules)
	}

	// Uninstall.
	req, _ := http.NewRequest(http.MethodDelete, s.srv.URL+"/console/capsules/demo", nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if resp2.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp2.Body)
		t.Fatalf("DELETE status %d: %s", resp2.StatusCode, raw)
	}
	_ = resp2.Body.Close()

	state = getStateResponse(t, s.srv.URL)
	if len(state.Capsules.Items) != 0 {
		t.Errorf("capsules should be empty after delete: %+v", state.Capsules)
	}
}

func TestCapsules_InstallRejectsDuplicate(t *testing.T) {
	s := newCapsulesServer(t)
	src := writeManifestOnlyCapsule(t, s.dir, "twice", "0.1.0")

	body, _ := json.Marshal(map[string]any{"path": src})
	for i := 0; i < 2; i++ {
		resp, err := http.Post(s.srv.URL+"/console/capsules", "application/json", bytes.NewReader(body))
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

func TestCapsules_InstallBadPath(t *testing.T) {
	s := newCapsulesServer(t)
	body, _ := json.Marshal(map[string]any{"path": "/does/not/exist"})
	resp, err := http.Post(s.srv.URL+"/console/capsules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(resp.Body)
		t.Errorf("status %d, want 400: %s", resp.StatusCode, raw)
	}
}

func TestCapsules_UninstallNotFound(t *testing.T) {
	s := newCapsulesServer(t)
	req, _ := http.NewRequest(http.MethodDelete, s.srv.URL+"/console/capsules/ghost", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}

func TestCapsules_StartStopWithoutHostReturns503(t *testing.T) {
	s := newCapsulesServer(t)
	// Install one first so the capsule exists; the start/stop
	// endpoint refuses anyway because Host is nil.
	src := writeManifestOnlyCapsule(t, s.dir, "static", "0.1.0")
	body, _ := json.Marshal(map[string]any{"path": src})
	if resp, err := http.Post(s.srv.URL+"/console/capsules", "application/json", bytes.NewReader(body)); err != nil {
		t.Fatalf("install: %v", err)
	} else {
		_ = resp.Body.Close()
	}

	for _, action := range []string{"start", "stop"} {
		resp, err := http.Post(s.srv.URL+"/console/capsules/static/"+action, "application/json", nil)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s status %d, want 503 (no host wired)", action, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

func TestCapsules_RejectsBadInput(t *testing.T) {
	s := newCapsulesServer(t)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"not-json", "not-json", http.StatusBadRequest},
		{"missing-path", `{}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := http.Post(s.srv.URL+"/console/capsules", "application/json", strings.NewReader(c.body))
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

func TestCapsules_503WhenInstallerMissing(t *testing.T) {
	srv := New(Options{
		Addr:    "127.0.0.1:0",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	ts := httptest.NewServer(srv.httpSrv.Handler)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/console/capsules", "application/json",
		strings.NewReader(`{"path":"/anything"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", resp.StatusCode)
	}
}
