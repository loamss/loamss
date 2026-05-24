package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for WriteAtomic. The contract these lock in:
//
//   - happy path: writes a YAML file that Load can read back into an
//     equivalent Config
//   - refuses to overwrite an existing file unless Overwrite=true
//   - returns ErrAlreadyExists (wrapped) so callers can errors.Is on it
//   - backup-suffix support, including the "%s" timestamp placeholder
//   - refuses to write a config that wouldn't pass validate()
//   - file mode is 0600, parent dir 0700
//   - header is emitted as a "# "-prefixed YAML comment block
//
// We deliberately avoid testing internal helpers (encodeYAML,
// splitLines, yamlBuffer); they're verified through WriteAtomic's
// observable behavior.

// validConfig returns a minimal Config that passes validate().
func validConfig(t *testing.T) *Config {
	t.Helper()
	cfg := Default()
	// Default() points DataDir at the real ~/.loamss; relocate to a
	// per-test path so the validation passes without touching $HOME.
	cfg.Runtime.DataDir = t.TempDir()
	return cfg
}

func TestWriteAtomic_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := WriteAtomic(path, validConfig(t), WriteOptions{}); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 0600", got)
	}

	// Round-trip: Load must accept what WriteAtomic produced.
	if _, err := Load(path); err != nil {
		t.Fatalf("Load round-trip failed: %v", err)
	}
}

func TestWriteAtomic_RefusesExistingByDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := WriteAtomic(path, validConfig(t), WriteOptions{}); err != nil {
		t.Fatalf("first write: %v", err)
	}

	err := WriteAtomic(path, validConfig(t), WriteOptions{})
	if err == nil {
		t.Fatal("expected error on second write, got nil")
	}
	if !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("error %v is not ErrAlreadyExists", err)
	}
}

func TestWriteAtomic_OverwriteReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg1 := validConfig(t)
	cfg1.Runtime.ListenAddr = "127.0.0.1:7777"
	if err := WriteAtomic(path, cfg1, WriteOptions{}); err != nil {
		t.Fatalf("first write: %v", err)
	}

	cfg2 := validConfig(t)
	cfg2.Runtime.DataDir = cfg1.Runtime.DataDir
	cfg2.Runtime.ListenAddr = "127.0.0.1:9999"
	if err := WriteAtomic(path, cfg2, WriteOptions{Overwrite: true}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Runtime.ListenAddr != "127.0.0.1:9999" {
		t.Errorf("ListenAddr = %q, want 127.0.0.1:9999 (overwrite didn't take)", loaded.Runtime.ListenAddr)
	}
}

func TestWriteAtomic_BackupSuffix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := WriteAtomic(path, validConfig(t), WriteOptions{}); err != nil {
		t.Fatalf("first write: %v", err)
	}

	if err := WriteAtomic(path, validConfig(t), WriteOptions{
		Overwrite:    true,
		BackupSuffix: ".bak",
	}); err != nil {
		t.Fatalf("overwrite with backup: %v", err)
	}

	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf("backup file not present: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("new file not present: %v", err)
	}
}

func TestWriteAtomic_BackupSuffixTimestampPlaceholder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := WriteAtomic(path, validConfig(t), WriteOptions{}); err != nil {
		t.Fatalf("first write: %v", err)
	}

	if err := WriteAtomic(path, validConfig(t), WriteOptions{
		Overwrite:    true,
		BackupSuffix: "%s.bak",
	}); err != nil {
		t.Fatalf("overwrite with templated backup: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	// "%s" expands to YYYYMMDD-HHMMSS, so the resulting backup looks
	// like "config.yaml20260524-213059.bak" (no separator dot — the
	// caller is expected to embed any separator in the suffix string
	// itself, e.g. ".%s.bak").
	var foundBackup bool
	for _, e := range entries {
		name := e.Name()
		if name == "config.yaml" {
			continue
		}
		if strings.HasPrefix(name, "config.yaml") && strings.HasSuffix(name, ".bak") {
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

func TestWriteAtomic_RefusesInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := validConfig(t)
	cfg.Storage.Adapter = "" // breaks validation

	err := WriteAtomic(path, cfg, WriteOptions{})
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("invalid config produced a file on disk; nothing should have been written")
	}
}

func TestWriteAtomic_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c")
	path := filepath.Join(nested, "config.yaml")

	if err := WriteAtomic(path, validConfig(t), WriteOptions{}); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}

	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("parent dir mode = %o, want 0700", got)
	}
}

func TestWriteAtomic_HeaderEmittedAsComment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	header := "managed by the loamss console wizard\n\nedits are preserved"
	if err := WriteAtomic(path, validConfig(t), WriteOptions{Header: header}); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(body)
	if !strings.HasPrefix(got, "# managed by the loamss console wizard\n") {
		t.Errorf("first line is not the header comment; file begins:\n%s", firstLines(got, 4))
	}
	if !strings.Contains(got, "#\n") {
		t.Errorf("blank header line not emitted as bare '#':\n%s", firstLines(got, 6))
	}
	if !strings.Contains(got, "# edits are preserved\n") {
		t.Errorf("header second paragraph not present:\n%s", firstLines(got, 6))
	}
	// Round-trip still works — comments must not break YAML decoding.
	if _, err := Load(path); err != nil {
		t.Fatalf("Load after header: %v", err)
	}
}

func TestWriteAtomic_NoTempFilesLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := WriteAtomic(path, validConfig(t), WriteOptions{}); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("temp file %q left in directory after successful write", e.Name())
		}
	}
}

func TestDefaultPath_RespectsDataDirEnv(t *testing.T) {
	t.Setenv(envDataDir, "/tmp/loamss-test-default-path")
	got := DefaultPath()
	want := filepath.Join("/tmp/loamss-test-default-path", "config.yaml")
	if got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
