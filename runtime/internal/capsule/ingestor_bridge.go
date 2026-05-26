package capsule

import "context"

// IngestorBridge mediates between the capsule installer and the
// runtime-level surfaces that need to know an ingestor capsule
// exists — the source store (so it appears in `loamss source list`)
// and the per-capsule data stores (credentials, cursor) that need
// cleanup on uninstall.
//
// Defined as an interface in this package so the installer stays
// free of imports on `runtime/internal/source` and
// `runtime/internal/mcp`. The concrete bridge lives in cli/start.go
// next to the daemon's other adapter wiring; it imports both and
// constructs an instance the installer holds.
//
// All methods are best-effort transactional: any error returned by
// OnInstall causes the installer to roll back the capsule install
// (revoke grants, delete code, remove the capsule row). OnUninstall
// errors are logged and swallowed — by the time it fires, the
// capsule record is already gone and there's no useful path back.
type IngestorBridge interface {
	// OnInstall is invoked by Installer.Install after the capsule
	// record has been persisted but before the install is reported
	// as successful. For ingestor-role capsules, this is where the
	// runtime inserts the corresponding sources-table row.
	//
	// Implementations should be no-ops for capsules without the
	// ingestor role (the manifest carries the Roles list).
	OnInstall(ctx context.Context, c *Installed) error

	// OnUninstall is invoked by Installer.Uninstall after the capsule
	// record is removed. Implementations should:
	//   - delete the sources-table row owned by this capsule
	//   - delete the capsule's credential blob
	//   - delete the capsule's cursor blob
	//
	// Best-effort: errors are logged but do not roll back the
	// uninstall (the capsule is already gone).
	OnUninstall(ctx context.Context, name string) error
}

// nopIngestorBridge is the zero-cost default used when no bridge is
// wired (e.g., in tests that don't exercise the ingestor path).
type nopIngestorBridge struct{}

func (nopIngestorBridge) OnInstall(_ context.Context, _ *Installed) error { return nil }
func (nopIngestorBridge) OnUninstall(_ context.Context, _ string) error   { return nil }
