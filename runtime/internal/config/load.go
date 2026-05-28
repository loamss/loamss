package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Loading order:
//   1. Start with Default()
//   2. Merge any YAML file found at the resolved path (file overrides defaults)
//   3. Apply environment-variable overrides (env overrides file)
//   4. Validate
//
// Resolution order for the config file path:
//   1. explicit `path` argument (typically from --config flag) — required to exist if non-empty
//   2. LOAMSS_CONFIG env var — required to exist if non-empty
//   3. <data_dir>/config.yaml (default ~/.loamss/config.yaml) — used if present, defaults if not

// Env vars recognised by the loader. Adding more requires expanding
// applyEnv. We avoid reflection-based magic: explicit list, explicit handling.
const (
	envConfigPath  = "LOAMSS_CONFIG"
	envDataDir     = "LOAMSS_DATA_DIR"
	envListenAddr  = "LOAMSS_LISTEN_ADDR"
	envLogLevel    = "LOAMSS_LOG_LEVEL"
	envLogFormat   = "LOAMSS_LOG_FORMAT"
	envRedactLevel = "LOAMSS_AUDIT_REDACTION_LEVEL"
	envDatabaseURL = "LOAMSS_DATABASE_URL"
	// envDatabaseURLAlt is the conventional Cloud Run / Heroku /
	// Render env var. Checked only when LOAMSS_DATABASE_URL is unset.
	envDatabaseURLAlt   = "DATABASE_URL"
	envAuditDatabaseURL = "LOAMSS_AUDIT_DATABASE_URL"
)

// Load resolves a Config from the given file path, environment, and defaults.
//
// If path is empty, the loader looks at LOAMSS_CONFIG, then falls back to
// the default location. A missing default-location file is not an error —
// defaults are used. A missing explicit path (via argument or env) IS an
// error, since the caller asked for a specific file.
func Load(path string) (*Config, error) {
	cfg := Default()

	resolvedPath, explicit := resolvePath(path)

	if resolvedPath != "" {
		if err := mergeFile(cfg, resolvedPath); err != nil {
			// Default-location file is allowed to be absent (we just keep
			// defaults). Any other error — including an explicit path that
			// doesn't exist — propagates.
			if !errors.Is(err, os.ErrNotExist) || explicit {
				return nil, fmt.Errorf("reading config file %s: %w", resolvedPath, err)
			}
		}
	}

	applyEnv(cfg)

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return cfg, nil
}

// resolvePath returns the config file path to load and whether it was
// explicitly requested (so a missing file is an error rather than silently
// falling back to defaults).
func resolvePath(arg string) (path string, explicit bool) {
	if arg != "" {
		return arg, true
	}
	if v := os.Getenv(envConfigPath); v != "" {
		return v, true
	}
	// Default location depends on the (possibly env-overridden) data dir.
	dataDir := defaultDataDir()
	if v := os.Getenv(envDataDir); v != "" {
		dataDir = v
	}
	return filepath.Join(dataDir, "config.yaml"), false
}

// mergeFile reads YAML from path and unmarshals into cfg. The unmarshal
// behavior is "additive": fields present in the file overwrite defaults,
// fields absent leave defaults intact.
func mergeFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true) // catch typos in config keys early
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("parsing yaml: %w", err)
	}
	return nil
}

// applyEnv overrides individual fields from environment variables.
// We do not attempt to set arbitrary adapter-config fields via env —
// that's what the file is for.
func applyEnv(cfg *Config) {
	if v := os.Getenv(envDataDir); v != "" {
		cfg.Runtime.DataDir = v
	}
	if v := os.Getenv(envListenAddr); v != "" {
		cfg.Runtime.ListenAddr = v
	}
	if v := os.Getenv(envLogLevel); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv(envLogFormat); v != "" {
		cfg.Log.Format = v
	}
	if v := os.Getenv(envRedactLevel); v != "" {
		cfg.Audit.RedactionLevel = v
	}
	// Runtime database URL. LOAMSS_DATABASE_URL is the project's
	// own env var; DATABASE_URL is checked as a fallback for the
	// common Cloud Run / Heroku / Render convention. The env var
	// implies adapter=postgres (no one passes a SQLite path via
	// DATABASE_URL) — operators wanting SQLite at a custom path
	// should set it in the YAML.
	if v := os.Getenv(envDatabaseURL); v != "" {
		cfg.Runtime.Database.Adapter = "postgres"
		cfg.Runtime.Database.DSN = v
	} else if v := os.Getenv(envDatabaseURLAlt); v != "" {
		cfg.Runtime.Database.Adapter = "postgres"
		cfg.Runtime.Database.DSN = v
	}
	// LOAMSS_AUDIT_DATABASE_URL is the audit-specific override.
	// No DATABASE_URL fallback for audit — the operator opts in
	// explicitly. Implies adapter=postgres.
	if v := os.Getenv(envAuditDatabaseURL); v != "" {
		cfg.Runtime.AuditDatabase.Adapter = "postgres"
		cfg.Runtime.AuditDatabase.DSN = v
	}
}

// Validate enforces top-level schema invariants. Adapter-specific config
// validation is the adapter's job (at adapter Init time), not the loader's.
//
// Exposed (capitalised) so callers that construct a Config in memory
// — e.g., the /console/init handler taking a wizard payload — can
// reject bad input at the request boundary with a 400, rather than
// discovering it inside WriteAtomic and bouncing it as a 500.
func Validate(cfg *Config) error { return validate(cfg) }

func validate(cfg *Config) error {
	if cfg.Runtime.DataDir == "" {
		return errors.New("runtime.data_dir must be set")
	}
	if cfg.Runtime.ListenAddr == "" {
		return errors.New("runtime.listen_addr must be set")
	}
	if !isAdapterID(cfg.Storage.Adapter, "storage") {
		return fmt.Errorf("storage.adapter %q is not a valid storage adapter id (expected storage:<name>)", cfg.Storage.Adapter)
	}
	if !isAdapterID(cfg.Memory.Adapter, "memory") {
		return fmt.Errorf("memory.adapter %q is not a valid memory adapter id (expected memory:<name>)", cfg.Memory.Adapter)
	}
	for i, m := range cfg.Models {
		if !isAdapterID(m.Adapter, "model") {
			return fmt.Errorf("models[%d].adapter %q is not a valid model adapter id (expected model:<name>)", i, m.Adapter)
		}
	}
	if !isOneOf(cfg.Log.Level, "debug", "info", "warn", "error") {
		return fmt.Errorf("log.level %q is not one of debug, info, warn, error", cfg.Log.Level)
	}
	if !isOneOf(cfg.Log.Format, "text", "json") {
		return fmt.Errorf("log.format %q is not one of text, json", cfg.Log.Format)
	}
	if !isOneOf(cfg.Audit.RedactionLevel, "default", "strict", "debug") {
		return fmt.Errorf("audit.redaction_level %q is not one of default, strict, debug", cfg.Audit.RedactionLevel)
	}
	if cfg.Audit.HotStoreMaxDays <= 0 {
		return fmt.Errorf("audit.hot_store_max_days must be > 0 (got %d)", cfg.Audit.HotStoreMaxDays)
	}
	if cfg.Audit.HotStoreMaxMB <= 0 {
		return fmt.Errorf("audit.hot_store_max_mb must be > 0 (got %d)", cfg.Audit.HotStoreMaxMB)
	}
	return nil
}

func isAdapterID(s, namespace string) bool {
	prefix := namespace + ":"
	if !strings.HasPrefix(s, prefix) {
		return false
	}
	return len(s) > len(prefix)
}

func isOneOf(s string, allowed ...string) bool {
	for _, a := range allowed {
		if s == a {
			return true
		}
	}
	return false
}

// --- context wiring -----------------------------------------------------

type ctxKey struct{}

// With returns a context that carries the given Config.
func With(ctx context.Context, cfg *Config) context.Context {
	return context.WithValue(ctx, ctxKey{}, cfg)
}

// From extracts the Config from ctx. Returns nil if none was attached;
// callers should treat that as a programming error (the root command
// always attaches one before subcommands run).
func From(ctx context.Context) *Config {
	cfg, _ := ctx.Value(ctxKey{}).(*Config)
	return cfg
}
