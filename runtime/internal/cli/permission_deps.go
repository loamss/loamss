package cli

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// permissionDeps bundles the permission store, audit writer, and
// the engine that wraps both. Constructed at the start of every
// permission-touching CLI subcommand and closed before return.
//
// Lifetime: one set per CLI invocation. The runtime daemon holds a
// long-lived equivalent; the CLI's short-lived instances coexist
// with the daemon because both use SQLite WAL.
type permissionDeps struct {
	engine *permission.Engine
	store  *permission.Store
	audit  *audit.SQLite
}

// Close releases the underlying handles. Both errors are swallowed
// (we log on a best-effort basis; CLI exit dominates).
func (p *permissionDeps) Close() {
	_ = p.store.Close()
	if p.audit != nil {
		_ = p.audit.Close(context.Background())
	}
}

// openPermissionDeps resolves the data dir from config and opens
// both the runtime.db (permission store) and audit.db (audit log)
// at their conventional paths.
func openPermissionDeps(cmd *cobra.Command) (*permissionDeps, error) {
	cfg := config.From(cmd.Context())
	if cfg == nil {
		return nil, errors.New("no config attached to context (programming error in CLI wiring)")
	}
	store, err := permission.Open(cmd.Context(), filepath.Join(cfg.Runtime.DataDir, "runtime.db"))
	if err != nil {
		return nil, err
	}
	w, err := audit.OpenSQLite(cmd.Context(), filepath.Join(cfg.Runtime.DataDir, "audit.db"))
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return &permissionDeps{
		engine: permission.NewEngine(store, w),
		store:  store,
		audit:  w,
	}, nil
}
