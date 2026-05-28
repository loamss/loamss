package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/database"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// permissionDeps bundles the permission store, audit writer, and
// the engine that wraps both. Constructed at the start of every
// permission-touching CLI subcommand and closed before return.
//
// Lifetime: one set per CLI invocation. The runtime daemon holds a
// long-lived equivalent; CLI's short-lived instances coexist with
// the daemon — SQLite WAL handles the concurrency, and on Postgres
// each opens its own pgx connection pool.
type permissionDeps struct {
	engine *permission.Engine
	store  *permission.Store
	audit  *audit.SQLite
	db     *database.Database // owning handle; closed last
}

// Close releases the underlying handles. Errors swallowed (best-
// effort; CLI exit dominates).
func (p *permissionDeps) Close() {
	if p.store != nil {
		_ = p.store.Close()
	}
	if p.audit != nil {
		_ = p.audit.Close(context.Background())
	}
	if p.db != nil {
		_ = p.db.Close()
	}
}

// openPermissionDeps resolves the runtime database from config (SQLite
// at <data_dir>/runtime.db by default; Postgres when configured) +
// opens the audit log at <data_dir>/audit.db.
//
// Audit log is still SQLite-only at the CLI surface — externalizing
// audit to Postgres is the next sub-task after this one.
func openPermissionDeps(cmd *cobra.Command) (*permissionDeps, error) {
	cfg := config.From(cmd.Context())
	if cfg == nil {
		return nil, errors.New("no config attached to context (programming error in CLI wiring)")
	}
	db, err := openRuntimeDB(cmd.Context(), cfg)
	if err != nil {
		return nil, err
	}
	store, err := permission.OpenWith(cmd.Context(), db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	w, err := openAuditWriter(cmd.Context(), cfg)
	if err != nil {
		_ = store.Close()
		_ = db.Close()
		return nil, err
	}
	return &permissionDeps{
		engine: permission.NewEngine(store, w),
		store:  store,
		audit:  w,
		db:     db,
	}, nil
}
