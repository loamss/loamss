package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/audit"
)

// runExportCmd runs `loamss export` with the test harness's
// cobra root. Returns the captured stdout/stderr and any error.
func runExportCmd(t *testing.T, dataDir string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("LOAMSS_DATA_DIR", dataDir)
	exportOutPath = ""
	exportNoVerify = false
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(append([]string{"export"}, args...))
	err := rootCmd.Execute()
	return buf.String(), err
}

func TestExport_HappyPath(t *testing.T) {
	dataDir := t.TempDir()
	// Seed some files so the archive isn't empty.
	if err := os.MkdirAll(filepath.Join(dataDir, "storage"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "storage", "hello.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Open audit so audit.db exists + has the chain seeded.
	w, err := audit.OpenSQLite(context.Background(), filepath.Join(dataDir, "audit.db"))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if _, err := w.Append(context.Background(), audit.Entry{
		Type:    "test.event",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: "u"},
		Outcome: audit.OutcomeSuccess,
	}); err != nil {
		t.Fatalf("audit Append: %v", err)
	}
	_ = w.Close(context.Background())

	outPath := filepath.Join(t.TempDir(), "export.tar.gz")
	out, err := runExportCmd(t, dataDir, "--out", outPath)
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	if !strings.Contains(out, "✓ Exported to") {
		t.Errorf("expected success line, got:\n%s", out)
	}
	if !strings.Contains(out, "audit chain verified") {
		t.Errorf("expected chain verification in output, got:\n%s", out)
	}

	// Read the archive back.
	files := tarballContents(t, outPath)
	hasManifest := false
	hasStorage := false
	hasAuditDB := false
	for name := range files {
		switch {
		case strings.HasSuffix(name, "/manifest.json"):
			hasManifest = true
		case strings.HasSuffix(name, "/data/storage/hello.txt"):
			hasStorage = true
		case strings.HasSuffix(name, "/data/audit.db"):
			hasAuditDB = true
		}
	}
	if !hasManifest {
		t.Error("manifest.json missing from archive")
	}
	if !hasStorage {
		t.Error("storage tree missing from archive")
	}
	if !hasAuditDB {
		t.Error("audit.db missing from archive")
	}

	// Inspect the manifest.
	for name, data := range files {
		if !strings.HasSuffix(name, "/manifest.json") {
			continue
		}
		var m ExportManifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		if m.SchemaVersion != "0.1" {
			t.Errorf("schema_version: %q", m.SchemaVersion)
		}
		if m.AuditVerify == nil || !m.AuditVerify.Valid {
			t.Errorf("audit verify result: %+v", m.AuditVerify)
		}
		if m.AuditVerify.EntriesChecked != 1 {
			t.Errorf("entries_checked: %d", m.AuditVerify.EntriesChecked)
		}
	}
}

func TestExport_SkipsWALSidecars(t *testing.T) {
	dataDir := t.TempDir()
	// Create a fake audit.db plus its WAL/SHM sidecars.
	for _, name := range []string{"audit.db", "audit.db-wal", "audit.db-shm", "runtime.db-journal"} {
		if err := os.WriteFile(filepath.Join(dataDir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	outPath := filepath.Join(t.TempDir(), "export.tar.gz")
	if _, err := runExportCmd(t, dataDir, "--out", outPath, "--no-verify"); err != nil {
		t.Fatalf("export: %v", err)
	}

	files := tarballContents(t, outPath)
	for name := range files {
		if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") ||
			strings.HasSuffix(name, "-journal") {
			t.Errorf("sidecar file leaked into archive: %s", name)
		}
	}
}

func TestExport_NoVerifyOmitsAuditCheck(t *testing.T) {
	dataDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "export.tar.gz")
	out, err := runExportCmd(t, dataDir, "--out", outPath, "--no-verify")
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	// Should not mention chain verification when --no-verify is set.
	if strings.Contains(out, "audit chain verified") || strings.Contains(out, "chain BROKEN") {
		t.Errorf("--no-verify should suppress verification output, got:\n%s", out)
	}

	files := tarballContents(t, outPath)
	for name, data := range files {
		if !strings.HasSuffix(name, "/manifest.json") {
			continue
		}
		var m ExportManifest
		_ = json.Unmarshal(data, &m)
		if m.AuditVerify != nil {
			t.Errorf("manifest should omit audit_verify when --no-verify, got: %+v", m.AuditVerify)
		}
	}
}

func TestExport_MissingDataDir(t *testing.T) {
	_, err := runExportCmd(t, "/no/such/dir")
	if err == nil {
		t.Error("expected error for nonexistent data dir")
	}
}

// tarballContents extracts the archive and returns a map of
// archive paths → file contents. Used for assertions.
func tarballContents(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		buf := bytes.Buffer{}
		if _, err := io.Copy(&buf, tr); err != nil {
			t.Fatalf("tar read: %v", err)
		}
		out[hdr.Name] = buf.Bytes()
	}
	return out
}
