package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/capsule"
)

// writeFixtureCapsule materializes the email-drafter manifest under
// a temp dir plus a tiny code/ tree. Returns the source directory
// the CLI should install from.
func writeFixtureCapsule(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	data, err := os.ReadFile(filepath.Join("..", "capsule", "testdata", "valid-email-drafter.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "capsule.yaml"), data, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	codeDir := filepath.Join(src, "code")
	if err := os.MkdirAll(codeDir, 0o700); err != nil {
		t.Fatalf("mkdir code: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codeDir, "server.js"), []byte("// stub\n"), 0o600); err != nil {
		t.Fatalf("write server.js: %v", err)
	}
	return src
}

func resetCapsuleInstallFlags() {
	capsuleInstallYes = true // tests skip the prompt
	capsuleInstallJSON = false
	capsuleListJSON = false
	capsuleShowJSON = false
	capsuleUninstallYes = true
	capsuleUninstallReason = ""
}

func TestCapsuleInstall_HappyPath(t *testing.T) {
	dataDir := t.TempDir()
	src := writeFixtureCapsule(t)
	defer resetCapsuleInstallFlags()
	resetCapsuleInstallFlags()

	out, err := runCapsuleCmd(t, dataDir, "install", "--yes", src)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Capsule email-drafter@1.4.0",
		"requests the following capabilities",
		"memory.read",
		"memory.write",
		"It will expose these tools",
		"draft_reply",
		"✓ Installed email-drafter@1.4.0",
		"grants issued: 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// Verify state via the store.
	store, err := capsule.OpenStore(context.Background(), filepath.Join(dataDir, "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	c, err := store.Get(context.Background(), "email-drafter")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if c.Version != "1.4.0" {
		t.Errorf("version: %q", c.Version)
	}
}

func TestCapsuleInstall_RejectsBadManifest(t *testing.T) {
	dataDir := t.TempDir()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "capsule.yaml"), []byte(`
spec_version: "0.1"
name: BAD-NAME
version: not-semver
author: {name: x}
permissions:
  - capability: audit.write
tools:
  - name: t
    input_schema: {type: object}
model_requirements: {}
runtime:
  type: wasm
  entrypoint: []
  protocol: smtp
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	defer resetCapsuleInstallFlags()
	resetCapsuleInstallFlags()

	_, err := runCapsuleCmd(t, dataDir, "install", "--yes", src)
	if err == nil {
		t.Error("expected error for invalid manifest")
	}
}

func TestCapsuleList_EmptyShowsNoCapsules(t *testing.T) {
	dataDir := t.TempDir()
	defer resetCapsuleInstallFlags()
	resetCapsuleInstallFlags()
	out, err := runCapsuleCmd(t, dataDir, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "no capsules installed") {
		t.Errorf("expected '(no capsules installed)', got:\n%s", out)
	}
}

func TestCapsuleList_ShowsInstalled(t *testing.T) {
	dataDir := t.TempDir()
	src := writeFixtureCapsule(t)
	defer resetCapsuleInstallFlags()
	resetCapsuleInstallFlags()
	if _, err := runCapsuleCmd(t, dataDir, "install", "--yes", src); err != nil {
		t.Fatalf("install: %v", err)
	}

	out, err := runCapsuleCmd(t, dataDir, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "email-drafter@1.4.0") {
		t.Errorf("expected name@version in list, got:\n%s", out)
	}
	if !strings.Contains(out, "Acme Capsules Inc.") {
		t.Errorf("expected author, got:\n%s", out)
	}
}

func TestCapsuleShow_PrintsDetail(t *testing.T) {
	dataDir := t.TempDir()
	src := writeFixtureCapsule(t)
	defer resetCapsuleInstallFlags()
	resetCapsuleInstallFlags()
	if _, err := runCapsuleCmd(t, dataDir, "install", "--yes", src); err != nil {
		t.Fatalf("install: %v", err)
	}

	out, err := runCapsuleCmd(t, dataDir, "show", "email-drafter")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	for _, want := range []string{
		"Capsule email-drafter@1.4.0",
		"Author:",
		"Acme Capsules Inc.",
		"Spec version:",
		"Tools:",
		"Permissions:",
		"- memory.read",
		"- memory.write",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestCapsuleUninstall_CascadesAndAudits(t *testing.T) {
	dataDir := t.TempDir()
	src := writeFixtureCapsule(t)
	defer resetCapsuleInstallFlags()
	resetCapsuleInstallFlags()
	if _, err := runCapsuleCmd(t, dataDir, "install", "--yes", src); err != nil {
		t.Fatalf("install: %v", err)
	}

	out, err := runCapsuleCmd(t, dataDir, "uninstall", "--yes", "email-drafter")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if !strings.Contains(out, "✓ Uninstalled email-drafter@1.4.0") {
		t.Errorf("expected success line, got:\n%s", out)
	}

	// Record + code dir gone, grants revoked.
	store, _ := capsule.OpenStore(context.Background(), filepath.Join(dataDir, "runtime.db"))
	defer store.Close()
	if _, err := store.Get(context.Background(), "email-drafter"); err == nil {
		t.Error("record should be deleted")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "capsules", "email-drafter@1.4.0")); !os.IsNotExist(err) {
		t.Errorf("install dir should be removed, got: %v", err)
	}

	// Audit verifies.
	w, _ := audit.OpenSQLite(context.Background(), filepath.Join(dataDir, "audit.db"))
	defer w.Close(context.Background())
	un, _ := w.Query(context.Background(), audit.Filter{Types: []string{"capsule.uninstalled"}})
	if len(un) != 1 {
		t.Errorf("expected 1 capsule.uninstalled, got %d", len(un))
	}
}

func TestCapsuleUninstall_Unknown(t *testing.T) {
	dataDir := t.TempDir()
	defer resetCapsuleInstallFlags()
	resetCapsuleInstallFlags()
	_, err := runCapsuleCmd(t, dataDir, "uninstall", "--yes", "no-such")
	if err == nil {
		t.Error("expected error for unknown capsule")
	}
}

func TestCapsuleInstall_RejectsDuplicate(t *testing.T) {
	dataDir := t.TempDir()
	src := writeFixtureCapsule(t)
	defer resetCapsuleInstallFlags()
	resetCapsuleInstallFlags()
	if _, err := runCapsuleCmd(t, dataDir, "install", "--yes", src); err != nil {
		t.Fatalf("first install: %v", err)
	}
	out, err := runCapsuleCmd(t, dataDir, "install", "--yes", src)
	if err == nil {
		t.Errorf("expected error on duplicate install, got:\n%s", out)
	}
}

func TestCapsuleInstall_JSONMode(t *testing.T) {
	dataDir := t.TempDir()
	src := writeFixtureCapsule(t)
	defer resetCapsuleInstallFlags()
	resetCapsuleInstallFlags()
	out, err := runCapsuleCmd(t, dataDir, "install", "--yes", "--json", src)
	if err != nil {
		t.Fatalf("install --json: %v", err)
	}
	// JSON mode skips the human-readable slip — output is one
	// decodable object.
	var payload struct {
		Capsule  *capsule.Installed `json:"Capsule"`
		GrantIDs []string           `json:"GrantIDs"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, out)
	}
	if payload.Capsule == nil || payload.Capsule.Name != "email-drafter" {
		t.Errorf("payload: %+v", payload.Capsule)
	}
	if len(payload.GrantIDs) != 2 {
		t.Errorf("GrantIDs count: %d", len(payload.GrantIDs))
	}
}
