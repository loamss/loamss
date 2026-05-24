package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/config"
)

// resetInitFlags clears init's package-level flag state between tests.
// Cobra reuses the rootCmd across t.Run subtests, so flag values persist
// unless we explicitly reset them.
func resetInitFlags() {
	initDataDir = ""
	initForce = false
}

func runInitCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetInitFlags()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(append([]string{"init"}, args...))
	err := rootCmd.Execute()
	return buf.String(), err
}

func TestInit_CreatesDataDirAndConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")

	out, err := runInitCmd(t, "--data-dir", dir)
	if err != nil {
		t.Fatalf("init failed: %v\nOutput:\n%s", err, out)
	}

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("data dir not created: %v", err)
	}

	configPath := filepath.Join(dir, "config.yaml")
	stat, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("config not created: %v", err)
	}
	if stat.Mode()&0o077 != 0 {
		// 0644 means user rw, group r, other r — we want at least that.
		// Just ensure it's readable by the owner.
		if stat.Mode()&0o400 == 0 {
			t.Errorf("config not readable by owner; mode = %o", stat.Mode())
		}
	}

	for _, want := range []string{
		"✓ Initialized data directory",
		"✓ Wrote config:",
		"Next steps:",
		"loamss config show",
		configPath,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestInit_ConfigContentIsValidAndLoadable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loadable")

	if _, err := runInitCmd(t, "--data-dir", dir); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	configPath := filepath.Join(dir, "config.yaml")
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading written config: %v", err)
	}

	// Should contain the substituted paths.
	for _, want := range []string{
		"data_dir: " + dir,
		"root: " + filepath.Join(dir, "storage"),
		"path: " + filepath.Join(dir, "memory.db"),
		"adapter: storage:fs-encrypted",
		"adapter: memory:sqlite-vec",
		"listen_addr: 127.0.0.1:7777",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("config body missing %q", want)
		}
	}

	// And the resulting file must round-trip through Load() without error.
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("written config does not load: %v\nbody:\n%s", err, string(body))
	}
	if cfg.Runtime.DataDir != dir {
		t.Errorf("loaded data_dir: got %q, want %q", cfg.Runtime.DataDir, dir)
	}
	if cfg.Storage.Adapter != "storage:fs-encrypted" {
		t.Errorf("loaded storage adapter: got %q", cfg.Storage.Adapter)
	}
}

func TestInit_RefusesOverwriteWithoutForce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "exists")

	if _, err := runInitCmd(t, "--data-dir", dir); err != nil {
		t.Fatalf("first init failed: %v", err)
	}

	// Second init without --force should fail with a clear error.
	out, err := runInitCmd(t, "--data-dir", dir)
	if err == nil {
		t.Fatalf("expected second init to fail; got nil error\nOutput:\n%s", out)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention 'already exists', got: %v", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should hint at --force, got: %v", err)
	}
}

func TestInit_OverwritesWithForce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "force")

	if _, err := runInitCmd(t, "--data-dir", dir); err != nil {
		t.Fatalf("first init failed: %v", err)
	}

	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("# tampered"), 0o644); err != nil {
		t.Fatalf("tampering with config: %v", err)
	}

	if _, err := runInitCmd(t, "--data-dir", dir, "--force"); err != nil {
		t.Fatalf("init --force failed: %v", err)
	}

	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config after force: %v", err)
	}
	if strings.Contains(string(body), "tampered") {
		t.Error("--force did not overwrite the tampered content")
	}
	if !strings.Contains(string(body), "Loamss runtime configuration") {
		t.Error("--force did not write the template")
	}
}

func TestInit_RespectsEnvDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "env")
	t.Setenv("LOAMSS_DATA_DIR", dir)

	if _, err := runInitCmd(t); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err != nil {
		t.Errorf("LOAMSS_DATA_DIR not honored: %v", err)
	}
}

func TestInit_FlagWinsOverEnv(t *testing.T) {
	envDir := filepath.Join(t.TempDir(), "env")
	flagDir := filepath.Join(t.TempDir(), "flag")
	t.Setenv("LOAMSS_DATA_DIR", envDir)

	if _, err := runInitCmd(t, "--data-dir", flagDir); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(flagDir, "config.yaml")); err != nil {
		t.Errorf("--data-dir was not honored over env: %v", err)
	}
	if _, err := os.Stat(filepath.Join(envDir, "config.yaml")); err == nil {
		t.Errorf("env-pointed dir should not have been written to")
	}
}
