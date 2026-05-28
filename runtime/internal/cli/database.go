package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/database"
)

// openRuntimeDB returns the *database.Database that the runtime.db-
// using subsystems (permission, source, capsule, memory_layer, oauth)
// should share for this CLI invocation or daemon run.
//
// Resolution:
//   - cfg.Runtime.Database.Adapter == "postgres" → open Postgres
//     against cfg.Runtime.Database.DSN. Empty DSN is an error.
//   - cfg.Runtime.Database.Adapter == "sqlite" or "" (default) →
//     open SQLite. DSN is the path; empty defaults to
//     <data_dir>/runtime.db.
//   - Any other adapter id is an error.
//
// The returned *database.Database is the caller's to Close. Daemon
// callers (start.go) hold it for the full lifetime; CLI subcommands
// open + close per invocation.
func openRuntimeDB(ctx context.Context, cfg *config.Config) (*database.Database, error) {
	adapter := cfg.Runtime.Database.Adapter
	dsn := cfg.Runtime.Database.DSN

	switch adapter {
	case "postgres":
		if dsn == "" {
			return nil, fmt.Errorf("database adapter is %q but DSN is empty (set runtime.database.dsn in config or LOAMSS_DATABASE_URL env)", adapter)
		}
		return database.OpenPostgres(ctx, dsn)
	case "", "sqlite":
		path := dsn
		if path == "" {
			path = filepath.Join(cfg.Runtime.DataDir, "runtime.db")
		}
		return database.OpenSQLite(ctx, path)
	default:
		return nil, fmt.Errorf("unknown runtime database adapter %q (want %q or %q)", adapter, "sqlite", "postgres")
	}
}

// openAuditWriter returns the *audit.Store the CLI subcommand or
// daemon should write to. Resolution mirrors openRuntimeDB but
// targets cfg.Runtime.AuditDatabase + LOAMSS_AUDIT_DATABASE_URL.
//
// Defaults to SQLite at <data_dir>/audit.db so the audit log gets
// its own write lock and isn't contending with the runtime.db
// writers.
func openAuditWriter(ctx context.Context, cfg *config.Config) (*audit.Store, error) {
	adapter := cfg.Runtime.AuditDatabase.Adapter
	dsn := cfg.Runtime.AuditDatabase.DSN
	switch adapter {
	case "postgres":
		if dsn == "" {
			return nil, fmt.Errorf("audit database adapter is %q but DSN is empty (set runtime.audit_database.dsn in config or LOAMSS_AUDIT_DATABASE_URL env)", adapter)
		}
		return audit.OpenPostgres(ctx, dsn)
	case "", "sqlite":
		path := dsn
		if path == "" {
			path = filepath.Join(cfg.Runtime.DataDir, "audit.db")
		}
		return audit.OpenSQLite(ctx, path)
	default:
		return nil, fmt.Errorf("unknown audit database adapter %q (want %q or %q)", adapter, "sqlite", "postgres")
	}
}

// dsnKind summarises the database config for the startup banner /
// log lines WITHOUT leaking secrets. A Postgres DSN typically
// contains a password; we only report the kind + a redacted host
// hint, never the raw DSN.
func dsnKind(adapter, dsn string) string {
	switch adapter {
	case "postgres":
		if host := redactPostgresHost(dsn); host != "" {
			return "postgres://" + host
		}
		return "postgres (dsn)"
	case "sqlite":
		return "sqlite path"
	case "":
		return "sqlite path (default)"
	default:
		return adapter
	}
}

// redactPostgresHost pulls host[:port] out of a libpq URL while
// dropping the user[:password] credential and the path/query.
// Returns "" if the input doesn't look like a Postgres URL.
func redactPostgresHost(dsn string) string {
	const p1 = "postgres://"
	const p2 = "postgresql://"
	var rest string
	switch {
	case len(dsn) > len(p1) && dsn[:len(p1)] == p1:
		rest = dsn[len(p1):]
	case len(dsn) > len(p2) && dsn[:len(p2)] == p2:
		rest = dsn[len(p2):]
	default:
		return ""
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] == '@' {
			rest = rest[i+1:]
			break
		}
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' || rest[i] == '?' {
			return rest[:i]
		}
	}
	return rest
}
