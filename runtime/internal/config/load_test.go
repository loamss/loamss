package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefault_IsValid(t *testing.T) {
	cfg := Default()
	if err := validate(cfg); err != nil {
		t.Fatalf("Default() should produce a valid config, got: %v", err)
	}
}

func TestLoad_NoFile_ReturnsDefaults(t *testing.T) {
	// Point at a non-existent default location.
	t.Setenv(envDataDir, t.TempDir()) // unique empty dir; no config.yaml in it
	t.Setenv(envConfigPath, "")       // no explicit override

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load with no file present should succeed, got: %v", err)
	}
	if cfg.Runtime.ListenAddr != defaultListenAddr {
		t.Errorf("expected default listen addr %q, got %q", defaultListenAddr, cfg.Runtime.ListenAddr)
	}
}

func TestLoad_ExplicitPathMissing_IsError(t *testing.T) {
	_, err := Load("/no/such/file/anywhere.yaml")
	if err == nil {
		t.Fatal("expected error for missing explicit path, got nil")
	}
	if !strings.Contains(err.Error(), "no/such/file/anywhere.yaml") {
		t.Errorf("error should mention the path, got: %v", err)
	}
}

func TestLoad_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
runtime:
  data_dir: ` + dir + `
  listen_addr: 0.0.0.0:9999

storage:
  adapter: storage:s3-compat
  config:
    bucket: my-bucket
    region: us-east-1

memory:
  adapter: memory:pgvector
  config:
    dsn: postgres://localhost/loamss

models:
  - adapter: model:anthropic
    config:
      api_key_env: ANTHROPIC_API_KEY
  - adapter: model:ollama
    config:
      endpoint: http://localhost:11434

routing:
  - task: drafting
    prefer: claude-sonnet-4.7
  - data_class: health
    forbidden_models: [hosted]

audit:
  hot_store_max_days: 14
  hot_store_max_mb: 2048
  redaction_level: strict

log:
  level: debug
  format: json
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got, want := cfg.Runtime.ListenAddr, "0.0.0.0:9999"; got != want {
		t.Errorf("listen_addr: got %q, want %q", got, want)
	}
	if got, want := cfg.Storage.Adapter, "storage:s3-compat"; got != want {
		t.Errorf("storage.adapter: got %q, want %q", got, want)
	}
	if got := cfg.Storage.Config["bucket"]; got != "my-bucket" {
		t.Errorf("storage.config.bucket: got %v, want my-bucket", got)
	}
	if got, want := len(cfg.Models), 2; got != want {
		t.Errorf("models count: got %d, want %d", got, want)
	}
	if got, want := cfg.Routing[1].DataClass, "health"; got != want {
		t.Errorf("routing[1].data_class: got %q, want %q", got, want)
	}
	if got, want := cfg.Audit.HotStoreMaxDays, 14; got != want {
		t.Errorf("audit.hot_store_max_days: got %d, want %d", got, want)
	}
	if got, want := cfg.Log.Format, "json"; got != want {
		t.Errorf("log.format: got %q, want %q", got, want)
	}
}

func TestLoad_UnknownKey_IsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
runtime:
  data_dir: ` + dir + `
  listen_addr: 127.0.0.1:7777
  typo_field: oops
storage:
  adapter: storage:fs-encrypted
memory:
  adapter: memory:sqlite-vec
audit:
  hot_store_max_days: 7
  hot_store_max_mb: 100
  redaction_level: default
log:
  level: info
  format: text
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "typo_field") {
		t.Errorf("error should mention the unknown field, got: %v", err)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv(envDataDir, t.TempDir())   // valid empty data dir; no config.yaml present
	t.Setenv(envConfigPath, "")         // no explicit config file
	t.Setenv(envListenAddr, "[::1]:80") // override the default listen addr
	t.Setenv(envLogLevel, "debug")      // override the default log level

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Runtime.ListenAddr, "[::1]:80"; got != want {
		t.Errorf("listen_addr env override: got %q, want %q", got, want)
	}
	if got, want := cfg.Log.Level, "debug"; got != want {
		t.Errorf("log.level env override: got %q, want %q", got, want)
	}
}

func TestValidate_AdapterIDs(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{
			name:    "bad storage adapter",
			mutate:  func(c *Config) { c.Storage.Adapter = "fs-encrypted" },
			wantErr: "storage adapter",
		},
		{
			name:    "bad memory adapter prefix",
			mutate:  func(c *Config) { c.Memory.Adapter = "storage:sqlite-vec" },
			wantErr: "memory adapter",
		},
		{
			name:    "bad model adapter in list",
			mutate:  func(c *Config) { c.Models = []AdapterConfig{{Adapter: "anthropic"}} },
			wantErr: "model adapter",
		},
		{
			name:    "log level invalid",
			mutate:  func(c *Config) { c.Log.Level = "verbose" },
			wantErr: "log.level",
		},
		{
			name:    "redaction level invalid",
			mutate:  func(c *Config) { c.Audit.RedactionLevel = "paranoid" },
			wantErr: "redaction_level",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(cfg)
			err := validate(cfg)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error message should contain %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoad_LoadConfigPathEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alt.yaml")
	yaml := `
runtime:
  data_dir: ` + dir + `
  listen_addr: 1.2.3.4:5555
storage:
  adapter: storage:fs-encrypted
memory:
  adapter: memory:sqlite-vec
audit:
  hot_store_max_days: 7
  hot_store_max_mb: 100
  redaction_level: default
log:
  level: info
  format: text
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	t.Setenv(envConfigPath, path)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load with LOAMSS_CONFIG: %v", err)
	}
	if got, want := cfg.Runtime.ListenAddr, "1.2.3.4:5555"; got != want {
		t.Errorf("LOAMSS_CONFIG fallback: got %q, want %q", got, want)
	}
}

func TestContext_RoundTrip(t *testing.T) {
	want := Default()
	ctx := With(context.Background(), want)
	got := From(ctx)
	if got != want {
		t.Errorf("From(With(ctx, c)) should return the same pointer; got %p, want %p", got, want)
	}
	if From(context.Background()) != nil {
		t.Error("From on empty context should return nil")
	}
}

// Sanity check: yaml.v3 ErrTypeMismatch (or similar) surfaces through Load
// as a wrapped error chain.
func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(path, []byte("this: is: not: valid: yaml: ::: ["), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	// We don't require a specific sentinel — just that the error wraps something
	// recognizable as a parse failure.
	var perr *os.PathError
	if errors.As(err, &perr) {
		t.Errorf("expected a parse-time error, got a filesystem error: %v", err)
	}
}
