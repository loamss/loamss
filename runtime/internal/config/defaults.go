package config

import (
	"os"
	"path/filepath"
)

// Default values applied when neither the config file nor environment
// variables specify a value. These are the safe, all-local defaults —
// they get the runtime up and usable on a single machine without any
// configuration beyond installing the binary.

const (
	defaultListenAddr     = "127.0.0.1:7777"
	defaultStorageAdapter = "storage:fs-encrypted"
	// defaultMemoryAdapter points at the only memory adapter shipped
	// in v0.1. memory:sqlite-vec (with the sqlite-vec extension and
	// proper k-NN indexing) is the planned drop-in upgrade for
	// larger memory stores; until that adapter lands, memory:sqlite
	// is the single supported option.
	defaultMemoryAdapter  = "memory:sqlite"
	defaultRedactionLevel = "default"
	defaultLogLevel       = "info"
	defaultLogFormat      = "text"

	defaultAuditHotMaxDays = 7
	defaultAuditHotMaxMB   = 1024
)

// defaultDataDir returns the default runtime data directory.
// On all platforms this is ~/.loamss; the XDG-compliant alternative
// can be selected by setting LOAMSS_DATA_DIR or runtime.data_dir.
func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// On the unlikely platforms where UserHomeDir fails, fall back to
		// a relative path. The runtime will still complain at write time;
		// this just keeps Default() pure (no error return).
		return ".loamss"
	}
	return filepath.Join(home, ".loamss")
}

// Default returns a Config populated with the runtime's safe defaults.
// Suitable as the starting point that file-based and env-based overrides
// then mutate.
func Default() *Config {
	dataDir := defaultDataDir()
	return &Config{
		Runtime: RuntimeConfig{
			DataDir:    dataDir,
			ListenAddr: defaultListenAddr,
		},
		Storage: AdapterConfig{
			Adapter: defaultStorageAdapter,
			Config: map[string]any{
				"root": filepath.Join(dataDir, "storage"),
			},
		},
		Memory: AdapterConfig{
			Adapter: defaultMemoryAdapter,
			Config: map[string]any{
				"path": filepath.Join(dataDir, "memory.db"),
			},
		},
		Audit: AuditConfig{
			HotStoreMaxDays: defaultAuditHotMaxDays,
			HotStoreMaxMB:   defaultAuditHotMaxMB,
			RedactionLevel:  defaultRedactionLevel,
		},
		Log: LogConfig{
			Level:  defaultLogLevel,
			Format: defaultLogFormat,
		},
	}
}
