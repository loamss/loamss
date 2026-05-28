// Package config defines the Loamss runtime's configuration schema and
// loading logic. The schema mirrors the user-facing YAML described in
// adapter-interface.md; the loader resolves a single Config from
// defaults, a YAML file, and environment-variable overrides.
//
// Subcommands receive the resolved Config via cmd.Context() using the
// helpers in this package (From, With). The runtime treats Config as
// immutable after load; the loader is the only writer.
package config

// Config is the top-level runtime configuration. Fields map 1:1 to the
// YAML keys used in the user's config file. Each section is independent;
// callers should never assume cross-section invariants beyond what the
// validator enforces.
type Config struct {
	Runtime RuntimeConfig   `yaml:"runtime" json:"runtime"`
	Storage AdapterConfig   `yaml:"storage" json:"storage"`
	Memory  AdapterConfig   `yaml:"memory" json:"memory"`
	Models  []AdapterConfig `yaml:"models,omitempty" json:"models,omitempty"`
	Routing []RoutingRule   `yaml:"routing,omitempty" json:"routing,omitempty"`
	Audit   AuditConfig     `yaml:"audit" json:"audit"`
	Log     LogConfig       `yaml:"log" json:"log"`
}

// RuntimeConfig captures process-level settings that aren't adapter-specific.
type RuntimeConfig struct {
	// DataDir is the runtime's local state directory.
	// Audit hot store, runtime SQLite (capsule registrations, grants, paired
	// clients, OAuth tokens, etc.), and any other operational state live here.
	// User data (storage adapter content) lives wherever the storage adapter
	// is configured.
	DataDir string `yaml:"data_dir" json:"data_dir"`

	// ListenAddr is the host:port the MCP surface and console API bind to.
	// In `local` profile the default is 127.0.0.1:7777 — never auto-exposes
	// publicly. In `cloud` profile the default is 0.0.0.0:$PORT (the PORT
	// env var Cloud Run / Fly / etc. inject, falling back to 7777). Always
	// honored verbatim when explicitly set.
	ListenAddr string `yaml:"listen_addr" json:"listen_addr"`

	// Profile names the deployment shape. "local" (the default) is the
	// laptop install — 127.0.0.1 binding, no auth front-door on the
	// wizard. "cloud" is the container install — 0.0.0.0:$PORT, setup-
	// token-gated wizard, expects a managed Postgres. When unset, the
	// runtime resolves the profile from LOAMSS_PROFILE or from
	// well-known cloud-platform env vars; see internal/profile.
	Profile string `yaml:"profile,omitempty" json:"profile,omitempty"`
}

// AdapterConfig points the runtime at a specific adapter implementation
// and carries the adapter-specific configuration map.
//
// The runtime validates only that Adapter is a recognized identifier
// (e.g., "storage:fs-encrypted"). The adapter itself validates Config
// when initialized; the runtime treats Config as opaque.
type AdapterConfig struct {
	Adapter string         `yaml:"adapter" json:"adapter"`
	Config  map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
}

// RoutingRule encodes a single model-routing decision. See ARCHITECTURE.md
// §model-router and the model.call capability in permission-model.md for
// the conceptual model.
//
// A rule can be selected by Task or DataClass (or both). The first matching
// rule wins; rules later in the list act as fallbacks. The router applies
// rules after the requesting capsule's grant scope has already filtered
// candidate models.
type RoutingRule struct {
	Task            string   `yaml:"task,omitempty" json:"task,omitempty"`
	DataClass       string   `yaml:"data_class,omitempty" json:"data_class,omitempty"`
	Prefer          string   `yaml:"prefer,omitempty" json:"prefer,omitempty"`
	ForbiddenModels []string `yaml:"forbidden_models,omitempty" json:"forbidden_models,omitempty"`
	CostCeiling     float64  `yaml:"cost_ceiling,omitempty" json:"cost_ceiling,omitempty"`
}

// AuditConfig controls the audit log's hot store and redaction policy.
// See audit-spec.md.
type AuditConfig struct {
	// HotStoreMaxDays bounds the runtime-local SQLite audit cache.
	// Older entries rotate out (the cold store in user storage retains them).
	HotStoreMaxDays int `yaml:"hot_store_max_days" json:"hot_store_max_days"`

	// HotStoreMaxMB caps the hot-store size; rotation triggers when either
	// the days or the MB bound is exceeded, whichever first.
	HotStoreMaxMB int `yaml:"hot_store_max_mb" json:"hot_store_max_mb"`

	// ColdStoreDir, if non-empty, overrides where rotated audit shards are
	// written via the storage adapter. Default: <storage>/audit/.
	ColdStoreDir string `yaml:"cold_store_dir,omitempty" json:"cold_store_dir,omitempty"`

	// RedactionLevel controls payload-redaction aggressiveness:
	//   "default" — the documented policy in audit-spec.md
	//   "strict"  — additionally hashes recipient addresses and paths
	//   "debug"   — retains full payloads (development only; never for production)
	RedactionLevel string `yaml:"redaction_level" json:"redaction_level"`
}

// LogConfig controls runtime stdout/stderr logging (distinct from the audit log).
type LogConfig struct {
	// Level: debug | info | warn | error.
	Level string `yaml:"level" json:"level"`

	// Format: text (human-readable) | json (structured, machine-readable).
	Format string `yaml:"format" json:"format"`
}
